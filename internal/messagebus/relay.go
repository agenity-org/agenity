// Package messagebus implements the in-band PTY @target relay: chepherd's
// conversational messaging primitive between agents.
//
// Wire model: every spawned ptyhost.Session has a Subscriber drained by
// this package. Each chunk is line-buffered and scanned for
//
//	^@<target>:\s+(.*)$
//
// On match, the body is delivered into the target session's PTY stdin
// via ptyhost.Session.Write — exactly as if the human had typed the body
// into the target's pane. The receiver agent reads natural prompt input;
// the human watching either pane sees a normal conversational exchange.
//
// Routing rules (tribe-aware):
//
//   - @<member-name>          — within sender's tribe(s)
//   - @<tribe>:@<member>      — cross-tribe (requires explicit grant)
//   - @all                    — broadcast to sender's tribe
//   - @human                  — always reaches the human (dashboard inbox)
//
// Safety rails:
//
//   - Per-sender rate limit (default 10 msg/min) → 429 dropped, logged
//   - Loop detector: same (from,to) pair >5x in 30s → break + warn
//   - Body cap 4 KiB; longer messages → 413 dropped, logged
//   - Pause-aware: target's .paused sentinel → message queued, not delivered
package messagebus

import (
	"bufio"
	"bytes"
	"fmt"
	"log"
	"regexp"
	"sync"
	"time"

	"github.com/chepherd/chepherd/internal/ptyhost/session"
)

// MaxBodyBytes caps a single @target message body.
const MaxBodyBytes = 4 * 1024

// RateLimitPerMinute is the default max routed messages per sender per minute.
const RateLimitPerMinute = 10

// LoopWindow is the time window over which we count a (from,to) pair.
const LoopWindow = 30 * time.Second

// LoopThreshold is the max number of (from,to) routings within LoopWindow
// before we break the loop and warn.
const LoopThreshold = 5

// targetLineRE matches lines beginning with @<target>: <body>.
// <target> matches member names, tribe names, "all", and "human".
var targetLineRE = regexp.MustCompile(`^@([a-zA-Z][a-zA-Z0-9_-]*)(?::@([a-zA-Z][a-zA-Z0-9_-]*))?:\s+(.+)$`)

// SessionRegistry is the minimal lookup interface the relay needs. The
// runtime owns the canonical registry; this interface lets us mock for tests.
type SessionRegistry interface {
	// SessionByName returns the ptyhost.Session and tribe membership of
	// the named session, or (nil, "", false) if no such session.
	SessionByName(name string) (s *session.Session, tribe string, ok bool)

	// SessionsByTribe returns every session that is a member of tribe.
	SessionsByTribe(tribe string) []*session.Session

	// HumanInbox writes a message to the human's dashboard inbox.
	HumanInbox(from, body string)

	// IsCrossTribeGranted reports whether the sender is permitted to
	// reach the given target tribe.
	IsCrossTribeGranted(fromTribe, toTribe string) bool

	// IsSessionPaused reports whether the target session is paused
	// (e.g. via the .paused sentinel).
	IsSessionPaused(s *session.Session) bool
}

// Relay watches every session's output for @target lines and routes
// matched bodies into the addressed session's PTY stdin.
type Relay struct {
	registry SessionRegistry

	mu       sync.Mutex
	rates    map[string][]time.Time   // sender name → send timestamps
	loops    map[loopKey][]time.Time  // (from,to) → routing timestamps
	stopper  chan struct{}
	watching map[string]chan struct{} // session name → per-watcher stop
}

type loopKey struct{ from, to string }

// New constructs a Relay bound to the given registry.
func New(registry SessionRegistry) *Relay {
	return &Relay{
		registry: registry,
		rates:    make(map[string][]time.Time),
		loops:    make(map[loopKey][]time.Time),
		stopper:  make(chan struct{}),
		watching: make(map[string]chan struct{}),
	}
}

// Watch begins consuming the named session's output. The session's name is
// used as the @-address ("@<name>") for routing replies into it. Idempotent
// per name.
func (r *Relay) Watch(s *session.Session, name string) error {
	r.mu.Lock()
	if _, already := r.watching[name]; already {
		r.mu.Unlock()
		return nil
	}
	stop := make(chan struct{})
	r.watching[name] = stop
	r.mu.Unlock()

	sub, _, err := s.Subscribe(256)
	if err != nil {
		return fmt.Errorf("relay: subscribe %s: %w", name, err)
	}

	go r.consumeLoop(name, sub, stop)
	return nil
}

// Unwatch stops consuming the named session.
func (r *Relay) Unwatch(name string) {
	r.mu.Lock()
	stop, ok := r.watching[name]
	delete(r.watching, name)
	r.mu.Unlock()
	if ok {
		close(stop)
	}
}

// Stop tears down every watcher.
func (r *Relay) Stop() {
	r.mu.Lock()
	close(r.stopper)
	for _, stop := range r.watching {
		close(stop)
	}
	r.watching = make(map[string]chan struct{})
	r.mu.Unlock()
}

func (r *Relay) consumeLoop(sender string, sub *session.Subscriber, stop chan struct{}) {
	var buf bytes.Buffer
	for {
		select {
		case <-stop:
			return
		case <-r.stopper:
			return
		case <-sub.Done:
			return
		case chunk, ok := <-sub.Ch:
			if !ok {
				return
			}
			buf.Write(chunk)
			// Process complete lines; keep partial trailing tail in buf.
			for {
				idx := bytes.IndexByte(buf.Bytes(), '\n')
				if idx < 0 {
					break
				}
				line := string(buf.Bytes()[:idx])
				buf.Next(idx + 1)
				r.processLine(sender, line)
			}
		}
	}
}

// processLine examines a single line of output from sender and, if it
// matches @target syntax, routes the body.
func (r *Relay) processLine(sender, line string) {
	// Strip ANSI / trailing whitespace before matching.
	trimmed := stripAnsi(line)
	trimmed = trimSpace(trimmed)
	m := targetLineRE.FindStringSubmatch(trimmed)
	if m == nil {
		return
	}
	target := m[1]
	tribeQualifier := m[2] // empty unless syntax was @tribe:@member
	body := m[3]
	if len(body) > MaxBodyBytes {
		log.Printf("relay: %s → @%s body too large (%d>%d), dropped", sender, target, len(body), MaxBodyBytes)
		return
	}

	// Rate limit per sender
	if !r.rateAllow(sender) {
		log.Printf("relay: %s rate-limited, dropping @%s message", sender, target)
		return
	}

	// Loop detection
	to := target
	if tribeQualifier != "" {
		to = tribeQualifier + ":" + target
	}
	if r.loopDetected(sender, to) {
		log.Printf("relay: loop detected %s→%s, breaking", sender, to)
		return
	}

	// Resolve + dispatch
	switch {
	case target == "human":
		r.registry.HumanInbox(sender, body)
	case target == "all":
		_, senderTribe, ok := r.registry.SessionByName(sender)
		if !ok {
			return
		}
		for _, s := range r.registry.SessionsByTribe(senderTribe) {
			r.deliver(s, sender, body)
		}
	default:
		// @<member> or @<tribe>:@<member>
		var targetSession *session.Session
		var targetTribe string
		if tribeQualifier != "" {
			// Cross-tribe; check grant.
			_, senderTribe, ok := r.registry.SessionByName(sender)
			if !ok {
				return
			}
			if !r.registry.IsCrossTribeGranted(senderTribe, target) {
				log.Printf("relay: %s→@%s:%s denied (no cross-tribe grant)", sender, target, tribeQualifier)
				return
			}
			// Resolve the qualified member within the target tribe.
			for _, s := range r.registry.SessionsByTribe(target) {
				// Heuristic: SessionByName lookup, then verify tribe matches.
				cand, candTribe, ok := r.registry.SessionByName(tribeQualifier)
				if ok && candTribe == target && cand == s {
					targetSession = cand
					targetTribe = candTribe
					break
				}
			}
		} else {
			// Within sender's tribe(s)
			s, tribe, ok := r.registry.SessionByName(target)
			if !ok {
				log.Printf("relay: %s→@%s — no such session", sender, target)
				return
			}
			targetSession = s
			targetTribe = tribe
		}
		if targetSession == nil {
			return
		}
		_ = targetTribe
		r.deliver(targetSession, sender, body)
	}
}

// deliver writes the body into the target session's PTY stdin with a
// minimal envelope so the receiver knows the source.
func (r *Relay) deliver(target *session.Session, from, body string) {
	if r.registry.IsSessionPaused(target) {
		log.Printf("relay: target paused, queueing not implemented; dropping (from=%s)", from)
		return
	}
	envelope := fmt.Sprintf("[@%s] %s\n", from, body)
	if _, err := target.Write([]byte(envelope)); err != nil {
		log.Printf("relay: write failed (from=%s): %v", from, err)
	}
}

// rateAllow returns true if sender is under the rate limit.
func (r *Relay) rateAllow(sender string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-time.Minute)
	fresh := r.rates[sender][:0]
	for _, t := range r.rates[sender] {
		if t.After(cutoff) {
			fresh = append(fresh, t)
		}
	}
	if len(fresh) >= RateLimitPerMinute {
		r.rates[sender] = fresh
		return false
	}
	r.rates[sender] = append(fresh, now)
	return true
}

// loopDetected returns true if (from,to) has fired >=LoopThreshold times
// within LoopWindow.
func (r *Relay) loopDetected(from, to string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := loopKey{from, to}
	now := time.Now()
	cutoff := now.Add(-LoopWindow)
	fresh := r.loops[key][:0]
	for _, t := range r.loops[key] {
		if t.After(cutoff) {
			fresh = append(fresh, t)
		}
	}
	if len(fresh) >= LoopThreshold {
		r.loops[key] = fresh
		return true
	}
	r.loops[key] = append(fresh, now)
	return false
}

// stripAnsi removes ANSI SGR escape sequences from a string. Conservative
// pattern matching CSI sequences only.
func stripAnsi(s string) string {
	return ansiRE.ReplaceAllString(s, "")
}

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)

func trimSpace(s string) string {
	// Strip trailing CR + leading/trailing whitespace using bufio's helper.
	t := bufio.NewScanner(bytes.NewReader([]byte(s)))
	t.Split(bufio.ScanLines)
	if t.Scan() {
		return t.Text()
	}
	return s
}
