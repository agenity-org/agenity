package daemon

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Signals is the bundle the judge sees. Field names + JSON shape match
// the Python supervisor's _fmt_signals_for_prompt() output so the same
// judge.md prompt works unchanged.
type Signals struct {
	Now                          time.Time
	Events                       []CompressedEvent
	LastToolCallAgeMin           *float64
	LastUserMsgAgeMin            *float64
	LastFounderDirectiveAgeMin   *float64
	LastAssistantEndsWithTool    bool
	MonitorRunning               bool
	GitLastCommitAgeMin          *float64
	TrackerMtimeAgeMin           *float64
	InProgressCount              int
	BacklogCount                 int
	UnclaimedBacklogCount        int
	UnclaimedBacklogTitles       []string
	BannedPhraseHits             []string
	PauseDetected                bool
	LastSupervisorMessage        string
	Repo                         string
	CurrentInProgressTitles      []string
	CommitsLastHour              []string
	PRsLastHour                  []string
	LastCoachTopic               string
	LastCoachAt                  string
	AddressedLastCoach           *bool
	QuietRatioLast30Min          float64
}

// CompressedEvent is one row in the judge's "last ~20 events" feed.
type CompressedEvent struct {
	Ts      time.Time
	Role    string // user | assist | user-tool-result
	Kind    string // text | bash | edit | write | read | agent | playwright | monitor | tool-result | task-notification | other
	Summary string
}

// banned + pause + tool-call kinds match Python supervisor.
var (
	BannedPhrases = []string{
		"Want me to", "Should I ", "Shall I ", "Holding.", "Acknowledged.",
		"Awaiting completion", "Awaiting roll", "Awaiting Wave",
		"Status:", "Session totals:",
	}
	PauseKeywords = []string{
		"stop", "pause", "park", "halt",
		"we'll continue tomorrow", "that's enough",
	}
)

// BuildSignals computes the full signal bundle for a session.
func BuildSignals(s *Session, state map[string]any) (*Signals, error) {
	now := time.Now().UTC()
	events, err := readRecentEvents(s.JSONLPath, 20)
	if err != nil {
		return nil, fmt.Errorf("read events: %w", err)
	}

	sig := &Signals{
		Now:                  now,
		Events:               events,
		Repo:                 s.Repo,
		LastAssistantEndsWithTool: false,
	}

	var lastToolTs, lastUserTs, lastDirectiveTs time.Time
	var lastAssistKind string

	for _, ev := range events {
		if ev.Role == "assist" {
			if isToolKind(ev.Kind) {
				lastToolTs = ev.Ts
				lastAssistKind = "tool"
				if ev.Kind == "monitor" && strings.HasPrefix(ev.Summary, "MONITOR start") {
					sig.MonitorRunning = true
				}
				if ev.Kind == "monitor" && strings.HasPrefix(ev.Summary, "MONITOR stop") {
					sig.MonitorRunning = false
				}
			} else if ev.Kind == "text" {
				lastAssistKind = "text"
				for _, bp := range BannedPhrases {
					if strings.Contains(ev.Summary, bp) {
						sig.BannedPhraseHits = append(sig.BannedPhraseHits, bp)
						break
					}
				}
			}
		} else if ev.Role == "user" {
			lastUserTs = ev.Ts
			if strings.HasPrefix(ev.Summary, "[SUPERVISOR") {
				continue
			}
			if ev.Kind == "task-notification" {
				continue
			}
			lastDirectiveTs = ev.Ts
			lowText := strings.ToLower(ev.Summary)
			for _, kw := range PauseKeywords {
				if strings.Contains(lowText, kw) {
					sig.PauseDetected = true
					break
				}
			}
		}
	}

	sig.LastAssistantEndsWithTool = lastAssistKind == "tool"
	sig.LastToolCallAgeMin = ageMin(lastToolTs, now)
	sig.LastUserMsgAgeMin = ageMin(lastUserTs, now)
	sig.LastFounderDirectiveAgeMin = ageMin(lastDirectiveTs, now)

	// git + tracker
	if out, err := exec.Command("git", "-C", s.CWD, "log", "-1", "--format=%ct").Output(); err == nil {
		var ts int64
		fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &ts)
		if ts > 0 {
			t := time.Unix(ts, 0).UTC()
			sig.GitLastCommitAgeMin = ageMin(t, now)
		}
	}
	if st, err := os.Stat(filepath.Join(s.CWD, "docs", "ledger", "TRACKER.md")); err == nil {
		sig.TrackerMtimeAgeMin = ageMin(st.ModTime().UTC(), now)
	}

	// gh issues + commits + PRs
	if s.GHRepo != "" {
		sig.InProgressCount, sig.BacklogCount = ghIssueCounts(s.GHRepo)
		sig.CurrentInProgressTitles = ghInProgressTitles(s.GHRepo, 5)
		sig.UnclaimedBacklogCount, sig.UnclaimedBacklogTitles = ghUnclaimedBacklog(s.GHRepo, 5)
		sig.PRsLastHour = ghRecentPRs(s.GHRepo, 60)
	} else {
		sig.InProgressCount = -1
		sig.BacklogCount = -1
		sig.UnclaimedBacklogCount = -1
	}
	sig.CommitsLastHour = gitRecentCommits(s.CWD, 60)

	// anti-theater carry-over from prior state
	if lct, ok := state["last_coach_topic"].(string); ok {
		sig.LastCoachTopic = lct
	}
	if lca, ok := state["last_intervention_at"].(string); ok {
		sig.LastCoachAt = lca
		if coachDt, err := time.Parse(time.RFC3339, lca); err == nil {
			if now.Sub(coachDt) < 90*time.Minute {
				commitsSince := gitCommitsSince(s.CWD, coachDt)
				addressed := len(commitsSince) > 0
				sig.AddressedLastCoach = &addressed
			}
		}
	}
	if lm, ok := state["last_message"].(string); ok {
		sig.LastSupervisorMessage = lm
	}

	// quiet ratio — proportion of 1-min windows in last 30min with NO tool calls
	sig.QuietRatioLast30Min = quietRatio(events, now, 30)

	return sig, nil
}

func ageMin(t time.Time, now time.Time) *float64 {
	if t.IsZero() {
		return nil
	}
	m := now.Sub(t).Minutes()
	return &m
}

func isToolKind(k string) bool {
	switch k {
	case "bash", "edit", "write", "read", "agent", "playwright", "monitor", "other":
		return true
	}
	return false
}

// readRecentEvents reads the last N compressed events from a JSONL file.
func readRecentEvents(path string, n int) ([]CompressedEvent, error) {
	// Backward-chunked seek so this works on multi-hundred-megabyte JSONLs.
	const chunk = 65536
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	stat, _ := f.Stat()
	size := stat.Size()
	pos := size
	var buf []byte
	for pos > 0 {
		read := int64(chunk)
		if pos < read {
			read = pos
		}
		pos -= read
		piece := make([]byte, read)
		if _, err := f.ReadAt(piece, pos); err != nil {
			break
		}
		buf = append(piece, buf...)
		// Count lines; bail when we have ~3x desired (compression filters many)
		if countNewlines(buf) >= n*3 {
			break
		}
	}
	scanner := bufio.NewScanner(bytes.NewReader(buf))
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	var out []CompressedEvent
	for scanner.Scan() {
		line := scanner.Bytes()
		var raw map[string]any
		if err := json.Unmarshal(line, &raw); err != nil {
			continue
		}
		out = append(out, compressMessage(raw)...)
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out, nil
}

func countNewlines(b []byte) int {
	c := 0
	for _, x := range b {
		if x == '\n' {
			c++
		}
	}
	return c
}

func compressMessage(d map[string]any) []CompressedEvent {
	tsStr, _ := d["timestamp"].(string)
	if tsStr == "" {
		return nil
	}
	ts, err := time.Parse(time.RFC3339Nano, tsStr)
	if err != nil {
		return nil
	}
	typeVal, _ := d["type"].(string)
	msg, _ := d["message"].(map[string]any)

	var out []CompressedEvent

	switch typeVal {
	case "user":
		switch c := msg["content"].(type) {
		case string:
			if strings.Contains(c, "<task-notification>") {
				out = append(out, CompressedEvent{Ts: ts, Role: "user", Kind: "task-notification",
					Summary: "task-notif: " + firstN(c, 120)})
			} else if !strings.Contains(c, "<local-command-stdout>") &&
				!strings.Contains(c, "<user-prompt-submit-hook>") &&
				!(strings.Contains(c, "<system-reminder>") && len(c) > 500) {
				txt := strings.ReplaceAll(c, "\n", " ")
				out = append(out, CompressedEvent{Ts: ts, Role: "user", Kind: "text",
					Summary: firstN(strings.TrimSpace(txt), 200)})
			}
		case []any:
			for _, b := range c {
				m, ok := b.(map[string]any)
				if !ok {
					continue
				}
				switch m["type"] {
				case "tool_result":
					rc := fmt.Sprintf("%v", m["content"])
					rc = strings.ReplaceAll(rc, "\n", " ")
					out = append(out, CompressedEvent{Ts: ts, Role: "user-tool-result", Kind: "tool-result",
						Summary: firstN(rc, 140)})
				case "text":
					txt, _ := m["text"].(string)
					txt = strings.ReplaceAll(strings.TrimSpace(txt), "\n", " ")
					out = append(out, CompressedEvent{Ts: ts, Role: "user", Kind: "text",
						Summary: firstN(txt, 200)})
				}
			}
		}
	case "assistant":
		c, _ := msg["content"].([]any)
		for _, b := range c {
			m, ok := b.(map[string]any)
			if !ok {
				continue
			}
			switch m["type"] {
			case "text":
				txt, _ := m["text"].(string)
				txt = strings.ReplaceAll(strings.TrimSpace(txt), "\n", " ")
				if txt != "" {
					out = append(out, CompressedEvent{Ts: ts, Role: "assist", Kind: "text",
						Summary: firstN(txt, 200)})
				}
			case "tool_use":
				name, _ := m["name"].(string)
				inp, _ := m["input"].(map[string]any)
				kind, summary := compressToolUse(name, inp)
				out = append(out, CompressedEvent{Ts: ts, Role: "assist", Kind: kind,
					Summary: summary})
			}
		}
	}
	return out
}

func compressToolUse(name string, inp map[string]any) (kind, summary string) {
	switch name {
	case "Bash":
		desc, _ := inp["description"].(string)
		cmd, _ := inp["command"].(string)
		return "bash", fmt.Sprintf(`BASH "%s": %s`, firstN(desc, 60), firstN(cmd, 140))
	case "Edit":
		fp, _ := inp["file_path"].(string)
		return "edit", "EDIT " + fp
	case "Write":
		fp, _ := inp["file_path"].(string)
		return "write", "WRITE " + fp
	case "Read":
		fp, _ := inp["file_path"].(string)
		return "read", "READ " + fp
	case "Agent":
		st, _ := inp["subagent_type"].(string)
		desc, _ := inp["description"].(string)
		return "agent", fmt.Sprintf("AGENT[%s]: %s", st, firstN(desc, 140))
	case "Monitor":
		desc, _ := inp["description"].(string)
		return "monitor", "MONITOR start: " + firstN(desc, 140)
	case "TaskStop":
		tid, _ := inp["task_id"].(string)
		return "monitor", "MONITOR stop: " + tid
	case "ScheduleWakeup":
		delay, _ := inp["delaySeconds"].(float64)
		return "monitor", fmt.Sprintf("SCHEDULE_WAKEUP %vs", delay)
	}
	if strings.HasPrefix(name, "mcp__plugin_playwright") {
		parts := strings.Split(name, "__")
		return "playwright", "PLAYWRIGHT " + parts[len(parts)-1]
	}
	j, _ := json.Marshal(inp)
	return "other", name + " " + firstN(string(j), 140)
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// quietRatio: proportion of 1-min windows in last `windowMin` minutes with no tool calls.
func quietRatio(events []CompressedEvent, now time.Time, windowMin int) float64 {
	if windowMin <= 0 {
		return 0
	}
	cutoff := now.Add(-time.Duration(windowMin) * time.Minute)
	active := map[time.Time]bool{}
	for _, ev := range events {
		if ev.Ts.Before(cutoff) {
			continue
		}
		if ev.Role != "assist" || !isToolKind(ev.Kind) {
			continue
		}
		minuteKey := ev.Ts.Truncate(time.Minute)
		active[minuteKey] = true
	}
	quiet := float64(windowMin - len(active))
	if quiet < 0 {
		quiet = 0
	}
	return quiet / float64(windowMin)
}

// ─── gh + git helpers ──────────────────────────────────────────────────────

func ghIssueCounts(ghRepo string) (inProg, total int) {
	out, err := exec.Command("gh", "issue", "list", "--repo", ghRepo,
		"--state", "open", "--limit", "200", "--json", "labels,number").Output()
	if err != nil {
		return -1, -1
	}
	var data []struct {
		Labels []struct{ Name string }
		Number int
	}
	if err := json.Unmarshal(out, &data); err != nil {
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
	return inProg, total
}

func ghInProgressTitles(ghRepo string, limit int) []string {
	out, err := exec.Command("gh", "issue", "list", "--repo", ghRepo,
		"--state", "open", "--label", "status/in-progress",
		"--limit", fmt.Sprintf("%d", limit),
		"--json", "number,title").Output()
	if err != nil {
		return nil
	}
	var data []struct {
		Number int
		Title  string
	}
	if err := json.Unmarshal(out, &data); err != nil {
		return nil
	}
	out2 := make([]string, 0, len(data))
	for _, i := range data {
		out2 = append(out2, fmt.Sprintf("#%d %s", i.Number, firstN(i.Title, 90)))
	}
	return out2
}

func ghUnclaimedBacklog(ghRepo string, limit int) (int, []string) {
	out, err := exec.Command("gh", "issue", "list", "--repo", ghRepo,
		"--state", "open", "--limit", "100",
		"--json", "number,title,labels").Output()
	if err != nil {
		return -1, nil
	}
	var data []struct {
		Number int
		Title  string
		Labels []struct{ Name string }
	}
	if err := json.Unmarshal(out, &data); err != nil {
		return -1, nil
	}
	var titles []string
	for _, i := range data {
		hasStatus := false
		for _, l := range i.Labels {
			if strings.HasPrefix(l.Name, "status/") {
				hasStatus = true
				break
			}
		}
		if !hasStatus {
			titles = append(titles, fmt.Sprintf("#%d %s", i.Number, firstN(i.Title, 90)))
		}
	}
	cnt := len(titles)
	if len(titles) > limit {
		titles = titles[:limit]
	}
	return cnt, titles
}

func ghRecentPRs(ghRepo string, sinceMin int) []string {
	sinceStr := time.Now().UTC().Add(-time.Duration(sinceMin) * time.Minute).Format("2006-01-02T15:04:05")
	out, err := exec.Command("gh", "pr", "list", "--repo", ghRepo,
		"--state", "all", "--limit", "5",
		"--search", "created:>="+sinceStr,
		"--json", "number,title,state").Output()
	if err != nil {
		return nil
	}
	var data []struct {
		Number int
		Title  string
		State  string
	}
	if err := json.Unmarshal(out, &data); err != nil {
		return nil
	}
	out2 := make([]string, 0, len(data))
	for _, p := range data {
		out2 = append(out2, fmt.Sprintf("#%d [%s] %s", p.Number, p.State, firstN(p.Title, 90)))
	}
	return out2
}

func gitRecentCommits(cwd string, sinceMin int) []string {
	out, err := exec.Command("git", "-C", cwd, "log",
		fmt.Sprintf("--since=%d minutes ago", sinceMin),
		"--format=%h %s", "-10").Output()
	if err != nil {
		return nil
	}
	var lines []string
	for _, l := range strings.Split(string(out), "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		lines = append(lines, firstN(l, 120))
	}
	if len(lines) > 8 {
		lines = lines[:8]
	}
	return lines
}

func gitCommitsSince(cwd string, since time.Time) []string {
	out, err := exec.Command("git", "-C", cwd, "log",
		"--since="+since.Format(time.RFC3339),
		"--format=%h %s", "-20").Output()
	if err != nil {
		return nil
	}
	var lines []string
	for _, l := range strings.Split(string(out), "\n") {
		l = strings.TrimSpace(l)
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}
