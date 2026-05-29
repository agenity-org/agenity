// internal/federation/discoverer_registry.go — HostedRegistry
// Discoverer for v0.9.3 #225 row C1. The hosted registry is a simple
// HTTP directory that any chepherd instance can opt into:
//
//	GET  $REG/peers                 → list of {sid, url}
//	POST $REG/announce               → { sid, url } — self-announce
//
// chepherd.org's directory is the canonical hosted registry, but the
// schema is open + operator can stand up their own (e.g., a private
// dial-in directory for an enterprise mesh). The endpoint contract is
// minimal so a static-file CDN can serve a curated peers.json for
// air-gapped fleets without needing the announce side.
//
// Refs #225 row C1.
package federation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// HostedRegistryDiscoverer announces this chepherd instance to a
// remote registry and polls for peer announcements. Operator opts in
// via the --federation-registry-url flag; setting it to "" disables.
type HostedRegistryDiscoverer struct {
	RegistryURL string
	SelfSID     string
	SelfURL     string
	// AnnouncePeriod controls how often we POST /announce. Default 60s.
	AnnouncePeriod time.Duration
	// PollPeriod controls how often we GET /peers. Default 30s.
	PollPeriod time.Duration
	HTTPClient *http.Client
}

func (d *HostedRegistryDiscoverer) Name() string { return "hosted-registry" }

type registryPeer struct {
	SID string `json:"sid"`
	URL string `json:"url"`
}

type registryPeersResponse struct {
	Peers []registryPeer `json:"peers"`
}

// Run blocks until ctx is canceled. The "no-op" path (RegistryURL
// empty) is the operator-friendly default — no traffic to anywhere
// the operator hasn't explicitly configured.
func (d *HostedRegistryDiscoverer) Run(ctx context.Context, out chan<- PeerAnnouncement) {
	if d.RegistryURL == "" {
		return
	}
	if d.HTTPClient == nil {
		d.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	announcePeriod := d.AnnouncePeriod
	if announcePeriod == 0 {
		announcePeriod = 60 * time.Second
	}
	pollPeriod := d.PollPeriod
	if pollPeriod == 0 {
		pollPeriod = 30 * time.Second
	}
	// Single self-announce immediately so the registry knows about us
	// before peers start polling — saves one announce-period of delay.
	go d.announceOnce(ctx)
	// Single immediate poll so we get the current peer set on boot
	// instead of waiting one PollPeriod.
	go d.pollOnce(ctx, out)

	announceTick := time.NewTicker(announcePeriod)
	defer announceTick.Stop()
	pollTick := time.NewTicker(pollPeriod)
	defer pollTick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-announceTick.C:
			go d.announceOnce(ctx)
		case <-pollTick.C:
			go d.pollOnce(ctx, out)
		}
	}
}

func (d *HostedRegistryDiscoverer) announceOnce(ctx context.Context) {
	if d.SelfSID == "" || d.SelfURL == "" {
		return
	}
	body, _ := json.Marshal(registryPeer{SID: d.SelfSID, URL: d.SelfURL})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(d.RegistryURL, "/")+"/announce",
		bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(stderrPrintf, "[federation] announce build: %v\n", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.HTTPClient.Do(req)
	if err != nil {
		fmt.Fprintf(stderrPrintf, "[federation] announce: %v\n", err)
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		fmt.Fprintf(stderrPrintf, "[federation] announce HTTP %d (registry=%s)\n", resp.StatusCode, d.RegistryURL)
	}
}

func (d *HostedRegistryDiscoverer) pollOnce(ctx context.Context, out chan<- PeerAnnouncement) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(d.RegistryURL, "/")+"/peers", nil)
	if err != nil {
		fmt.Fprintf(stderrPrintf, "[federation] poll build: %v\n", err)
		return
	}
	req.Header.Set("Accept", "application/json")
	resp, err := d.HTTPClient.Do(req)
	if err != nil {
		fmt.Fprintf(stderrPrintf, "[federation] poll: %v\n", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(stderrPrintf, "[federation] poll HTTP %d (registry=%s)\n", resp.StatusCode, d.RegistryURL)
		return
	}
	var peers registryPeersResponse
	if err := json.NewDecoder(resp.Body).Decode(&peers); err != nil {
		fmt.Fprintf(stderrPrintf, "[federation] poll decode: %v\n", err)
		return
	}
	for _, p := range peers.Peers {
		if p.SID == "" || p.URL == "" {
			continue
		}
		if p.SID == d.SelfSID {
			continue // don't fan our own announce back to ourselves
		}
		select {
		case out <- PeerAnnouncement{SID: p.SID, URL: p.URL, Source: "hosted-registry"}:
		case <-ctx.Done():
			return
		default:
			// channel full — drop rather than block discovery loops
		}
	}
}
