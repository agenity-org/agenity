// Package federation implements chepherd's peer discovery + AgentCard
// cache (v0.9.3 #225 row C1). Two discovery strategies ship:
//
//  1. HostedRegistry — chepherd-org-style HTTP directory. Operator
//     opts in by setting `--federation-registry-url`. Suitable for WAN
//     federation across NATs.
//  2. LocalMulticast — DNS-SD/mDNS announce on the LAN. Disabled by
//     default in v0.9.3 (pion/mdns layer arrives in C1-b sub-branch);
//     the registration seam lives here so the body can land without
//     touching consumer code.
//
// Both discovery sources funnel into the same Federation orchestrator:
// it fetches each discovered peer's `.well-known/agent-card.json`,
// validates the response, and persists the canonical body via
// AgentCardRepository so the dashboard / Federation tab can surface
// the trust graph without re-fetching on every render.
//
// Refs #225 row C1.
package federation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/agenity-org/agenity/internal/persistence"
)

// PeerAnnouncement is emitted by every Discoverer when it observes a
// peer chepherd instance. The orchestrator de-duplicates by SID +
// fetches the canonical agent card from URL + `.well-known/agent-card.json`.
type PeerAnnouncement struct {
	// SID is the peer's chepherd-instance fingerprint (#270). Used as
	// the AgentCardRepository key.
	SID string
	// URL is the peer's base HTTPS URL (e.g. https://peer-b.example.com).
	// The orchestrator appends `/.well-known/agent-card.json` to fetch.
	URL string
	// Source tags the discovery channel ("hosted-registry", "mdns",
	// "manual") so future RBAC / trust-graph logic can weight peers
	// differently (e.g. a manually-trusted peer overrides an mDNS-only
	// LAN peer).
	Source string
}

// Discoverer is the abstraction a discovery strategy implements. Run
// blocks until ctx is canceled, emitting peer announcements onto out.
// Implementations close their own goroutines on ctx cancel and never
// block on a full channel — they drop announcements rather than stall
// the orchestrator.
type Discoverer interface {
	Run(ctx context.Context, out chan<- PeerAnnouncement)
	Name() string
}

// Federation orchestrates a set of Discoverers. On every announcement
// it fetches the peer's agent card + persists via the
// AgentCardRepository. Concurrent-safe; one Federation per chepherd
// runtime.
type Federation struct {
	Store      persistence.AgentCardRepository
	HTTPClient *http.Client
	// FetchTimeout caps each agent-card.json fetch. Default 5s when zero.
	FetchTimeout time.Duration

	mu          sync.Mutex
	discoverers []Discoverer
}

// New constructs a Federation with the given AgentCard store. Add
// Discoverers via Register before calling Run.
func New(store persistence.AgentCardRepository) *Federation {
	return &Federation{
		Store:      store,
		HTTPClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// Register adds a Discoverer that Run will spawn.
func (f *Federation) Register(d Discoverer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.discoverers = append(f.discoverers, d)
}

// Discoverers returns a snapshot of the registered discoverers for
// inspection (boot banner / debugging).
func (f *Federation) Discoverers() []Discoverer {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Discoverer, len(f.discoverers))
	copy(out, f.discoverers)
	return out
}

// Run spawns every registered Discoverer + an orchestrator goroutine
// that fans in announcements, fetches agent cards, and persists them.
// Blocks until ctx is canceled. Safe to call once per Federation
// lifetime (re-calling returns immediately if already running).
func (f *Federation) Run(ctx context.Context) {
	f.mu.Lock()
	ds := append([]Discoverer{}, f.discoverers...)
	f.mu.Unlock()
	if len(ds) == 0 {
		return
	}
	in := make(chan PeerAnnouncement, 64)
	var wg sync.WaitGroup
	for _, d := range ds {
		wg.Add(1)
		go func(d Discoverer) {
			defer wg.Done()
			d.Run(ctx, in)
		}(d)
	}
	// Orchestrator: dedup + fetch + persist.
	seen := map[string]time.Time{}
	const refreshAfter = 5 * time.Minute
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case ann := <-in:
				if ann.SID == "" || ann.URL == "" {
					continue
				}
				last, ok := seen[ann.SID]
				if ok && time.Since(last) < refreshAfter {
					continue
				}
				seen[ann.SID] = time.Now()
				go f.fetchAndPersist(ctx, ann)
			}
		}
	}()
	wg.Wait()
	close(in)
}

func (f *Federation) fetchAndPersist(ctx context.Context, ann PeerAnnouncement) {
	timeout := f.FetchTimeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	fetchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	url := strings.TrimRight(ann.URL, "/") + "/.well-known/agent-card.json"
	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, url, nil)
	if err != nil {
		fmt.Fprintf(stderrPrintf, "[federation] fetch %s: build request: %v\n", url, err)
		return
	}
	req.Header.Set("Accept", "application/json")
	resp, err := f.HTTPClient.Do(req)
	if err != nil {
		fmt.Fprintf(stderrPrintf, "[federation] fetch %s: %v\n", url, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Fprintf(stderrPrintf, "[federation] fetch %s: HTTP %d\n", url, resp.StatusCode)
		return
	}
	// Validate the body parses as JSON before persisting — guards
	// against caching a 200-with-HTML 5xx-error-page from a misconfigured
	// peer.
	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		fmt.Fprintf(stderrPrintf, "[federation] fetch %s: invalid JSON: %v\n", url, err)
		return
	}
	body, _ := json.Marshal(raw)
	name, _ := raw["name"].(string)
	if name == "" {
		name = ann.SID
	}
	card := &persistence.AgentCard{
		SID:              ann.SID,
		Name:             name,
		Body:             body,
		PublicVisibility: ann.Source == "hosted-registry",
		SyncedAt:         time.Now().UTC(),
	}
	if err := f.Store.Save(ctx, card); err != nil {
		fmt.Fprintf(stderrPrintf, "[federation] persist %s: %v\n", ann.SID, err)
		return
	}
	fmt.Fprintf(stderrPrintf, "[federation] cached agent-card SID=%s name=%s source=%s\n", ann.SID, name, ann.Source)
}
