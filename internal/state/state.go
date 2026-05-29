// Package state reads per-session state JSON files written by the chepherd
// daemon (or the legacy Python supervisor at ~/.local/state/workflow/).
//
// This is a READ-ONLY layer. The TUI + status command both use it. The
// daemon writes to disk; observers never write back.
package state

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Session is the in-memory shape of one session's state file. Fields are
// optional — the Python supervisor sometimes omits fields it hasn't computed yet.
type Session struct {
	// UUID (Claude Code session ID) — derived from the filename, not stored
	// inside the JSON.
	UUID string `json:"-"`
	// TmuxName like "openova-27" — populated when the file is loaded by
	// joining with the latest discover_sessions() output, OR (in the legacy
	// Python supervisor) embedded in the state itself.
	TmuxName string `json:"tmux_name,omitempty"`

	// Band: one of "trusted" / "standard" / "concerned" / "crisis" (or empty
	// if the daemon hasn't computed it yet).
	Band string `json:"trust_band,omitempty"`

	// Most recent verdict literal: "silent" / "praise" / "coach" / "intervene"
	LastVerdict string `json:"last_verdict,omitempty"`

	// Most recent scorecard {G,V,F,E} (each 0-10).
	LastScorecard map[string]int `json:"last_scorecard,omitempty"`

	// Last time a coach/intervene was actually injected (ISO-8601 UTC).
	LastInterventionAt string `json:"last_intervention_at,omitempty"`

	// Last coach topic (extracted from the injected message).
	LastCoachTopic string `json:"last_coach_topic,omitempty"`

	// Total injections since the daemon started watching this session.
	InterventionCount int `json:"intervention_count,omitempty"`

	// When the next tick will fire for this session (per adaptive cadence).
	NextTickAt string `json:"next_tick_at,omitempty"`

	// Scorecard history — last ~20 ticks. Each entry has G/V/F/E + at.
	ScorecardHistory []map[string]any `json:"scorecard_history,omitempty"`

	// Live signals refreshed independently of the judge cadence
	// (~5 sec polling by the lightsignals goroutine). Lets the dashboard
	// show near-real-time issue counts + commit activity even when
	// the judge is on a 30-min trusted cadence.
	LiveSignals *LiveSignals `json:"live_signals,omitempty"`

	// AuthLapsed is set true when the tmux pane shows Claude's "Please run
	// /login" prompt — i.e. the session can't reach the Claude API and the
	// founder needs to re-OAuth. Dashboard surfaces this with a red 🔒 tag
	// + a hotkey to drop the user into /login on the affected session.
	AuthLapsed bool `json:"auth_lapsed,omitempty"`
}

// LiveSignals — cheap, free-to-compute snapshot of the session's local
// + GitHub state. Mirror of internal/lightsignals.Live so the TUI can
// read it without importing the daemon package.
type LiveSignals struct {
	RefreshedAt       string  `json:"refreshed_at"`
	InProgressCount   int     `json:"in_progress_count"`
	BacklogCount      int     `json:"backlog_count"`
	UnclaimedCount    int     `json:"unclaimed_backlog_count"`
	CommitCountLast1H int     `json:"commits_last_hour_count"`
	LastCommitAgeMin  float64 `json:"git_last_commit_age_min"`
	TrackerMtimeMin   float64 `json:"tracker_mtime_age_min"`
	LastEventAgeMin   float64 `json:"jsonl_last_event_age_min"`
}

// FormatScorecard returns "G/V/F/E=N/N/N/N" or "?/?/?/?" if unavailable.
func (s *Session) FormatScorecard() string {
	if s.LastScorecard == nil {
		return "?/?/?/?"
	}
	g, v, f, e := s.LastScorecard["G"], s.LastScorecard["V"], s.LastScorecard["F"], s.LastScorecard["E"]
	return fmt.Sprintf("%d/%d/%d/%d", g, v, f, e)
}

// Geomean returns the geometric mean of the four scorecard axes
// (G·V·F·E)^(1/4). Used as the "overall score" in the dashboard list so
// one weak axis drags the overall down — matches the supervisor's
// weakest-link philosophy. Any axis equal to 0 returns 0 (a zero
// strongly indicates one dimension has collapsed). Returns -1 when
// the scorecard isn't available so callers can render '—'.
func (s *Session) Geomean() float64 {
	if s.LastScorecard == nil {
		return -1
	}
	axes := []int{
		s.LastScorecard["G"],
		s.LastScorecard["V"],
		s.LastScorecard["F"],
		s.LastScorecard["E"],
	}
	product := 1.0
	for _, n := range axes {
		if n <= 0 {
			return 0
		}
		product *= float64(n)
	}
	return math.Pow(product, 1.0/float64(len(axes)))
}

// DefaultStateDirs returns the directories to look in for session JSON files,
// in priority order. chepherd run's shepherd tier writes to the first; the
// legacy Python supervisor writes to the second. Observers read from BOTH
// so they can sit alongside the Python daemon during migration.
func DefaultStateDirs() []string {
	home, _ := os.UserHomeDir()
	return []string{
		filepath.Join(home, ".local", "state", "chepherd", "sessions"),
		filepath.Join(home, ".local", "state", "workflow", "sessions"),
	}
}

// LoadAllSessions reads every <uuid>.json from every state directory and
// returns the union, dedup'd in two passes:
//   1. by UUID (chepherd-native files win over Python-legacy)
//   2. by tmux_name (keep the most-recently-active state file) — fixes
//      the case where one tmux pane was attached to multiple claude
//      UUIDs over time, leaving stale state files that all claim the
//      same tmux_name.
func LoadAllSessions() ([]*Session, error) {
	seen := map[string]*Session{}
	for _, dir := range DefaultStateDirs() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read %s: %w", dir, err)
		}
		for _, e := range entries {
			name := e.Name()
			if !strings.HasSuffix(name, ".json") {
				continue
			}
			if strings.HasSuffix(name, ".paused.json") || strings.HasSuffix(name, ".paused") {
				continue
			}
			uuid := strings.TrimSuffix(name, ".json")
			if _, already := seen[uuid]; already {
				continue // chepherd-native (first dir) wins
			}
			path := filepath.Join(dir, name)
			s, err := loadSession(path)
			if err != nil {
				continue
			}
			s.UUID = uuid
			if s.TmuxName == "" {
				s.TmuxName = uuid[:8] + "…" // fallback so the row isn't empty
			}
			seen[uuid] = s
		}
	}

	// Pass 2: dedup by tmux_name. When >1 session share the same tmux_name,
	// keep the one with the most recent activity timestamp — that's the
	// currently-live attachment; the others are stale claude UUIDs from
	// prior attachments to the same pane.
	byTmux := map[string]*Session{}
	for _, s := range seen {
		key := s.TmuxName
		if strings.HasSuffix(key, "…") {
			// UUID-prefix fallback — never dedup these; they represent
			// genuinely-different sessions whose tmux_name wasn't resolved
			byTmux[s.UUID] = s
			continue
		}
		existing, ok := byTmux[key]
		if !ok {
			byTmux[key] = s
			continue
		}
		if sessionActivityTime(s).After(sessionActivityTime(existing)) {
			byTmux[key] = s
		}
	}
	out := make([]*Session, 0, len(byTmux))
	for _, s := range byTmux {
		out = append(out, s)
	}
	return out, nil
}

// sessionActivityTime returns the most-recent activity timestamp on the
// session. Used to pick the live state file when multiple share a
// tmux_name. Walks NextTickAt (daemon-written each tick) → LiveSignals →
// LastInterventionAt; falls back to zero time if none parse.
func sessionActivityTime(s *Session) time.Time {
	if s.NextTickAt != "" {
		if t, err := time.Parse(time.RFC3339, s.NextTickAt); err == nil {
			return t
		}
	}
	if s.LiveSignals != nil && s.LiveSignals.RefreshedAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, s.LiveSignals.RefreshedAt); err == nil {
			return t
		}
		if t, err := time.Parse(time.RFC3339, s.LiveSignals.RefreshedAt); err == nil {
			return t
		}
	}
	if s.LastInterventionAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, s.LastInterventionAt); err == nil {
			return t
		}
		if t, err := time.Parse(time.RFC3339, s.LastInterventionAt); err == nil {
			return t
		}
	}
	return time.Time{}
}

func loadSession(path string) (*Session, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return &Session{}, nil
	}
	var s Session
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("unmarshal %s: %w", path, err)
	}
	return &s, nil
}
