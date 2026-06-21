// Package lightsignals refreshes the cheap, free-to-compute signals for
// each discovered session — gh issue counts, git commits, TRACKER mtime,
// JSONL last-event-age — independently of the LLM judge cadence.
//
// The judge runs at the trust band's adaptive interval (2-30 min) and costs
// money. The dashboard needs to feel live even on trusted sessions that the
// judge ticks every 30 min. So this package polls only the local-cheap
// signals at ~5 sec and writes them back into the session JSON file under
// the dedicated `live_signals` key. The TUI reads that key alongside the
// last-judge state.
//
// Future v0.2: replace polling with fsnotify on .git/refs/heads/* + the
// session's JSONL + TRACKER.md for truly event-driven updates.
package lightsignals

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/agenity-org/agenity/internal/scrummaster"
)

// Interval is how often the light-signal goroutine refreshes each session's
// cheap signals. 5 sec strikes a balance: feels live in the dashboard, well
// under any GitHub API rate limit (5000/hr per token = 83/min budget).
const Interval = 5 * time.Second

// Live is the JSON-serialisable bundle written into each session's state
// file under the `live_signals` key.
type Live struct {
	RefreshedAt       time.Time `json:"refreshed_at"`
	InProgressCount   int       `json:"in_progress_count"`
	BacklogCount      int       `json:"backlog_count"`
	UnclaimedCount    int       `json:"unclaimed_backlog_count"`
	CommitCountLast1H int       `json:"commits_last_hour_count"`
	LastCommitAgeMin  float64   `json:"git_last_commit_age_min"`
	TrackerMtimeMin   float64   `json:"tracker_mtime_age_min"`
	LastEventAgeMin   float64   `json:"jsonl_last_event_age_min"`
}

// Refresher runs in the background for one session.
type Refresher struct {
	Session  *scrummaster.Session
	StateDir string
}

// Loop is the entry point for goroutine launch. Returns when ctx is done.
func (r *Refresher) Loop(ctx context.Context) {
	t := time.NewTicker(Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = r.Refresh()
		}
	}
}

// Refresh computes the current Live snapshot + merges it into the session's
// state file (preserves all judge-written fields).
func (r *Refresher) Refresh() error {
	live := r.computeLive()
	return r.writeLive(live)
}

func (r *Refresher) computeLive() Live {
	now := time.Now().UTC()
	out := Live{RefreshedAt: now}

	if r.Session.GHRepo != "" {
		out.InProgressCount, out.BacklogCount = ghIssueCounts(r.Session.GHRepo)
		out.UnclaimedCount = ghUnclaimedBacklogCount(r.Session.GHRepo)
	} else {
		out.InProgressCount, out.BacklogCount, out.UnclaimedCount = -1, -1, -1
	}

	if t, ok := gitLastCommitTime(r.Session.CWD); ok {
		out.LastCommitAgeMin = now.Sub(t).Minutes()
	}
	out.CommitCountLast1H = gitCommitCountSince(r.Session.CWD, 60)

	tracker := filepath.Join(r.Session.CWD, "docs", "ledger", "TRACKER.md")
	if st, err := os.Stat(tracker); err == nil {
		out.TrackerMtimeMin = now.Sub(st.ModTime()).Minutes()
	}

	if t, ok := jsonlLastModified(r.Session.JSONLPath); ok {
		out.LastEventAgeMin = now.Sub(t).Minutes()
	}
	return out
}

func (r *Refresher) writeLive(live Live) error {
	statePath := filepath.Join(r.StateDir, r.Session.UUID+".json")
	state, _ := loadJSON(statePath) // tolerates missing
	state["live_signals"] = live
	return saveJSONAtomic(statePath, state)
}

func loadJSON(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{}, err
	}
	if len(b) == 0 {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return map[string]any{}, err
	}
	return m, nil
}

func saveJSONAtomic(path string, m map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ─── gh + git helpers (deliberately duplicated from daemon/ so this
//    package can be imported without pulling in the whole judge stack) ──

func ghIssueCounts(repo string) (inProg, total int) {
	out, err := exec.Command("gh", "issue", "list", "--repo", repo,
		"--state", "open", "--limit", "200", "--json", "labels,number").Output()
	if err != nil {
		return -1, -1
	}
	var data []struct {
		Labels []struct{ Name string }
	}
	if json.Unmarshal(out, &data) != nil {
		return -1, -1
	}
	total = len(data)
	for _, item := range data {
		for _, l := range item.Labels {
			if l.Name == "status/in-progress" {
				inProg++
				break
			}
		}
	}
	return
}

func ghUnclaimedBacklogCount(repo string) int {
	out, err := exec.Command("gh", "issue", "list", "--repo", repo,
		"--state", "open", "--limit", "200", "--json", "labels").Output()
	if err != nil {
		return -1
	}
	var data []struct {
		Labels []struct{ Name string }
	}
	if json.Unmarshal(out, &data) != nil {
		return -1
	}
	cnt := 0
	for _, item := range data {
		hasStatus := false
		for _, l := range item.Labels {
			if strings.HasPrefix(l.Name, "status/") {
				hasStatus = true
				break
			}
		}
		if !hasStatus {
			cnt++
		}
	}
	return cnt
}

func gitLastCommitTime(cwd string) (time.Time, bool) {
	out, err := exec.Command("git", "-C", cwd, "log", "-1", "--format=%ct").Output()
	if err != nil {
		return time.Time{}, false
	}
	var ts int64
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &ts); err != nil {
		return time.Time{}, false
	}
	return time.Unix(ts, 0).UTC(), true
}

func gitCommitCountSince(cwd string, sinceMin int) int {
	out, err := exec.Command("git", "-C", cwd, "log",
		fmt.Sprintf("--since=%d minutes ago", sinceMin),
		"--format=%h").Output()
	if err != nil {
		return 0
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	cnt := 0
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			cnt++
		}
	}
	return cnt
}

func jsonlLastModified(path string) (time.Time, bool) {
	st, err := os.Stat(path)
	if err != nil {
		return time.Time{}, false
	}
	return st.ModTime().UTC(), true
}
