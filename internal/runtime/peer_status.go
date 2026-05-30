package runtime

import (
	"time"
)

// PeerStatus is the live-state shape returned by:
//
//	GET /api/v1/sessions/<name>/peer-status   (HTTP)
//	chepherd.peer_status(name)                 (MCP tool)
//
// Distinct from PeerAgentCard (#404 P0.1) which is the
// capabilities-and-skills surface. PeerStatus answers "what is
// peer X DOING right now" — operator's #404 acceptance criterion
// 7: "ask alpha 'what are your siblings doing right now?' → answer
// cites beta + actual current activity".
//
// Pulls from the runtime's sessionActivity (running byte/chunk
// tallies) + the session's PTY ring buffer (recent output excerpt).
//
// #404 P0.2.
type PeerStatus struct {
	Name             string  `json:"name"`
	State            string  `json:"state"`            // alive | paused | exited
	LastActivityAt   string  `json:"lastActivityAt"`   // RFC3339 UTC, "" when no activity yet
	IdleSeconds      float64 `json:"idleSeconds"`      // wall-clock idle since last chunk
	TotalBytes       int64   `json:"totalBytes"`       // lifetime PTY-output bytes
	Bytes5m          int64   `json:"bytes5m"`          // PTY-output bytes in last 5 minutes
	Chunks5m         int     `json:"chunks5m"`         // distinct chunks in last 5 minutes
	RingExcerptTail  string  `json:"ringExcerptTail"`  // last ~2 KB of PTY output (ANSI-stripped)
	Shepherding      []string `json:"shepherding,omitempty"`
	Paused           bool    `json:"paused"`
}

// BuildPeerStatus constructs a PeerStatus for the named session.
// Returns nil when the runtime doesn't know the name.
//
// Reads:
//   - r.activity[id].snapshot() for activity counters
//   - sess.RingSnapshot() for the recent PTY output excerpt
//   - r.info[id] for lifecycle state
//
// All reads are bounded by the runtime mutex + per-activity mutex
// so this is safe to call from concurrent MCP/HTTP handlers.
//
// #404 P0.2.
func (r *Runtime) BuildPeerStatus(name string) *PeerStatus {
	sess, info := r.Get(name)
	if info == nil {
		return nil
	}
	out := &PeerStatus{
		Name:        info.Name,
		State:       stateLabel(info),
		Paused:      info.Paused,
		Shepherding: info.Shepherding,
	}

	// Look up sessionActivity by session ID. r.Get already locked +
	// returned the SessionInfo; we need a second look-up by ID to
	// pull the activity tracker.
	r.mu.Lock()
	var act *sessionActivity
	for id, i := range r.info {
		if i == info {
			act = r.activity[id]
			break
		}
	}
	r.mu.Unlock()

	if act != nil {
		total, bytes5m, chunks5m, idle := act.snapshot()
		out.TotalBytes = total
		out.Bytes5m = bytes5m
		out.Chunks5m = chunks5m
		out.IdleSeconds = idle
		// last activity timestamp — wall clock minus idle seconds.
		// snapshot() returns idle as seconds since last chunk; if no
		// chunks ever arrived, idle equals time-since-created which
		// is still a valid (if uninformative) last-activity proxy.
		act.mu.Lock()
		if !act.last.IsZero() {
			out.LastActivityAt = act.last.UTC().Format(time.RFC3339)
		}
		act.mu.Unlock()
	}

	// Ring excerpt — tail of the PTY output, ANSI-stripped so peer
	// agents reading via MCP see clean text. Cap at 2 KB so the
	// response stays small enough for the MCP JSON-RPC frame.
	if sess != nil {
		snap := sess.RingSnapshot()
		const maxExcerpt = 2048
		if len(snap) > maxExcerpt {
			snap = snap[len(snap)-maxExcerpt:]
		}
		out.RingExcerptTail = stripANSI(string(snap))
	}

	return out
}
