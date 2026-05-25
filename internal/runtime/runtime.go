// Package runtime owns the live operational state of a chepherd instance:
// session registry, tribe + role + shepherding metadata, and the spawn /
// assign / list / grant operations the MCP server and TUI both call.
//
// One Runtime per chepherd process. Goroutine-safe (sync.Mutex-protected).
// Persists per-session metadata to ~/.local/state/chepherd/sessions/<id>.json
// so the runtime can be restarted without losing tribe/role assignments.
package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chepherd/chepherd/internal/ptyhost/agentcatalog"
	"github.com/chepherd/chepherd/internal/ptyhost/session"
)

// Role is what an agent does inside its tribe.
type Role string

const (
	RoleWorker   Role = "worker"
	RoleShepherd Role = "shepherd"
)

// SessionInfo is the metadata chepherd tracks for each live session.
// session.Session is the live process; SessionInfo is the framework
// context (name, team, role, etc.).
type SessionInfo struct {
	ID        string    `json:"id"`        // ptyhost session ID (stable across restart attempts)
	Name      string    `json:"name"`      // canonical @-address (e.g. "iogrid-1")
	AgentSlug string    `json:"agent"`     // claude-code, qwen-code, etc.
	Team      string    `json:"team"`      // team membership — workers in same team @-reach freely
	Role      Role      `json:"role"`      // worker | shepherd
	Cwd       string    `json:"cwd"`       // working directory the agent was spawned in
	CreatedAt time.Time `json:"created_at"`
	Paused    bool      `json:"paused"`

	// Set non-empty only when Role == RoleShepherd. Teams this shepherd oversees.
	Shepherding []string `json:"shepherding,omitempty"`

	// PID of the spawned child process (the actual agent CLI — claude,
	// qwen-code, etc). Surfaces in the dashboard right pane "Process" card.
	PID int `json:"pid,omitempty"`

	// Git context — populated at spawn from `git config --get remote.origin.url`
	// and `git branch --show-current` when cwd is a git repo. Static for
	// the GitHubURL (origin doesn't change mid-session); Branch is refreshed
	// on every List() call.
	GitHubURL string `json:"github_url,omitempty"`
	Branch    string `json:"branch,omitempty"`

	// Exit detection — flipped by the activity sniffer when the PTY EOFs.
	// Failed exits (non-zero code) stay in List() so the operator can see
	// what went wrong; clean exits are dismissed quickly.
	Exited   bool `json:"exited,omitempty"`
	ExitCode int  `json:"exit_code,omitempty"`

	// Activity counters (populated by the runtime's per-session sniffer).
	// Reported on every Get/List; values are wall-clock snapshots.
	TotalBytes  int64   `json:"total_bytes"`
	Bytes5m     int64   `json:"bytes_5m"`
	Chunks5m    int     `json:"chunks_5m"` // distinct PTY writes in last 5 min
	IdleSeconds float64 `json:"idle_seconds"`

	// Latest scorecard produced by shepherd. Fields are 0..10 (Goal,
	// Velocity, Focus, EndState, Discipline). Nil until shepherd's first
	// tick assesses this session; UI shows "—" + "shepherd assessing..."
	// when absent.
	Scorecard *Scorecard `json:"scorecard,omitempty"`

	// Shepherd verdict history — count of coach/intervene verdicts AND
	// the most recent one (with timestamp + message). Empty until first
	// non-silent verdict.
	InterventionCount int       `json:"intervention_count,omitempty"`
	LastVerdict       string    `json:"last_verdict,omitempty"`       // silent|praise|coach|intervene
	LastVerdictAt     time.Time `json:"last_verdict_at,omitempty"`
	LastVerdictMsg    string    `json:"last_verdict_msg,omitempty"`

	// v0.6: operator-configurable per-agent settings.
	// SystemPrompt is the effective prompt the agent was spawned with
	// (either the role default from internal/prompts/*.md, or an operator
	// override). Surfaced read-only in WidgetAgentPrompt; refining a
	// running agent's working instructions goes through POST .../poke-prompt
	// which writes a fresh user message and updates this field.
	SystemPrompt string         `json:"system_prompt,omitempty"`
	StatSheet    AgentStatSheet `json:"stat_sheet,omitempty"`

	// Live Claude-side details, refreshed in List() from the most recent
	// JSONL the spawned agent is writing to. Cheap to compute (we only
	// scan the last ~50 lines of one file). Zero values when not applicable
	// (non-claude-code agents) or when no JSONL has been written yet.
	Model         string `json:"model,omitempty"`           // e.g. "claude-opus-4-7"
	ContextSize   int    `json:"context_size,omitempty"`    // model context window (e.g. 200_000 or 1_000_000)
	ContextTokens int    `json:"context_tokens,omitempty"`  // tokens currently held in the window (last usage block)
	ClaudeUUID    string `json:"claude_uuid,omitempty"`     // sessionId of the JSONL Claude is appending to
}

// Scorecard is shepherd's latest 5-axis assessment of a session.
// Goal/Velocity/Focus/End-state from the legacy supervisor; Discipline
// (CLAUDE.md/canon compliance) added as the 5th. Each axis is 0..10.
type Scorecard struct {
	Goal       float64   `json:"G"`
	Velocity   float64   `json:"V"`
	Focus      float64   `json:"F"`
	EndState   float64   `json:"E"`
	Discipline float64   `json:"D"`
	Note       string    `json:"note,omitempty"`
	At         time.Time `json:"at"`
}

// sessionActivity holds the running tally for one session — used by the
// runtime's per-session sniffer goroutine to populate SessionInfo's
// activity counters without locking the main runtime.
type sessionActivity struct {
	mu         sync.Mutex
	total      int64
	last       time.Time
	created    time.Time
	recent     []recentChunk // chunks within the last 5 minutes
}

type recentChunk struct {
	at   time.Time
	size int
}

// snapshot returns a copy of the activity counters with the 5-min
// window trimmed to the current wall clock. Safe to call from any
// goroutine (locks internally).
func (a *sessionActivity) snapshot() (total int64, bytes5m int64, chunks5m int, idleSeconds float64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	cutoff := time.Now().Add(-5 * time.Minute)
	for len(a.recent) > 0 && a.recent[0].at.Before(cutoff) {
		a.recent = a.recent[1:]
	}
	chunks5m = len(a.recent)
	for _, c := range a.recent {
		bytes5m += int64(c.size)
	}
	total = a.total
	if a.last.IsZero() {
		idleSeconds = time.Since(a.created).Seconds()
	} else {
		idleSeconds = time.Since(a.last).Seconds()
	}
	return
}

// Grant is a cross-team permission edge: agents in fromTeam may
// @<member> agents in toTeam.
type Grant struct {
	FromTeam string `json:"from_team"`
	ToTeam   string `json:"to_team"`
	Scope    string `json:"scope"` // "read" | "write" | "both"
}

// Runtime is the chepherd state authority.
type Runtime struct {
	mu sync.Mutex

	// live process handles (not persisted)
	sessions map[string]*session.Session // by ID
	byName   map[string]string           // name → ID

	// persistent metadata
	info   map[string]*SessionInfo // by ID
	grants []Grant

	// configuration
	stateDir string // ~/.local/state/chepherd

	// human-inbox sink
	humanInbox []HumanInboxEntry

	// for waking up subscribers when state changes (used by TUI ticker, etc.)
	cond *sync.Cond

	// spawnHooks are invoked after every successful Spawn with the new
	// session + its canonical name. The messagebus relay registers a
	// hook here so dynamically-spawned agents (via chepherd.spawn MCP
	// tool) get their output watched for @target relay routing.
	spawnHooks []func(*session.Session, string)

	// activity counters by session ID. Populated by per-session sniffer
	// goroutines attached at Spawn time; read on every List/Get to fill
	// SessionInfo.{TotalBytes,Bytes5m,IdleSeconds}.
	activity map[string]*sessionActivity

	// v0.6 unified data model — Agent + Team + Membership as first-class objects.
	// Coexists with v0.5 SessionInfo during the transition; new MCP tools
	// (create_team / join_team / leave_team / list_teams) operate on these.
	teams        map[string]*Team        // by name
	memberships  map[string]*Membership  // by composite key "agent_name|team_name"

	// Per-axis review records (v0.6-C council pattern). Keyed by target
	// agent name; inner map keyed by axis (G|V|F|E|D|custom).
	axisReviews map[string]map[string]*AxisReview

	// Event log — runtime-wide chronological audit (v0.6-F).
	events *eventBuffer
}

// RecordEvent appends an event to the runtime's audit log. Called by
// runtime internals on spawn/exit/scorecard/etc. and by agents via the
// chepherd.record_event MCP tool.
func (r *Runtime) RecordEvent(e Event) {
	if r.events == nil {
		return
	}
	r.events.push(e)
}

// Events returns the most recent N events (or all if limit == 0).
func (r *Runtime) Events(limit int) []Event {
	if r.events == nil {
		return nil
	}
	return r.events.snapshot(limit)
}

// SubscribeEvents returns a channel that receives future events + an
// unsubscribe function. Used by SSE/WS endpoints.
func (r *Runtime) SubscribeEvents() (<-chan Event, func()) {
	if r.events == nil {
		ch := make(chan Event)
		close(ch)
		return ch, func() {}
	}
	return r.events.subscribe()
}

// AxisReview is one reviewer's score on one axis of one target worker.
// Multiple reviewers can write to the same target; shepherd composes
// the final scorecard by reading the union.
type AxisReview struct {
	Reviewer string    `json:"reviewer"`
	Axis     string    `json:"axis"`
	Score    float64   `json:"score"`
	Note     string    `json:"note,omitempty"`
	At       time.Time `json:"at"`
}

// StateDir returns the root state directory for this runtime
// (~/.local/state/chepherd-v0X). Used by HTTP server for workspace
// persistence paths.
func (r *Runtime) StateDir() string {
	return r.stateDir
}

// AddSpawnHook registers a callback invoked after every successful Spawn.
// Called synchronously in Spawn after persistence, before broadcast.
func (r *Runtime) AddSpawnHook(hook func(*session.Session, string)) {
	r.mu.Lock()
	r.spawnHooks = append(r.spawnHooks, hook)
	r.mu.Unlock()
}

// HumanInboxEntry is a routed @human message in the dashboard's "Messages → Human" view.
type HumanInboxEntry struct {
	ID   string    `json:"id"`
	From string    `json:"from"`
	Body string    `json:"body"`
	At   time.Time `json:"at"`
	Read bool      `json:"read"`
}

// New constructs an empty Runtime rooted at stateDir.
func New(stateDir string) (*Runtime, error) {
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(stateDir, "inbox"), 0o700); err != nil {
		return nil, err
	}
	r := &Runtime{
		sessions:    make(map[string]*session.Session),
		byName:      make(map[string]string),
		info:        make(map[string]*SessionInfo),
		stateDir:    stateDir,
		activity:    make(map[string]*sessionActivity),
		teams:       make(map[string]*Team),
		memberships: make(map[string]*Membership),
		axisReviews: make(map[string]map[string]*AxisReview),
		events:      newEventBuffer(1000),
	}
	r.cond = sync.NewCond(&r.mu)
	return r, nil
}

// SpawnSpec describes how to bring up a new session.
type SpawnSpec struct {
	Name      string // canonical @-address; must be unique
	AgentSlug string // claude-code | qwen-code | aider | ...
	Team      string // default "default"
	Role      Role   // default worker
	Cwd       string // optional working dir
	SystemPrompt string // optional override for the agent's system prompt
	StatSheet   AgentStatSheet // optional override for the default per-role stat sheet

	// AgentArgs is appended to the agent CLI's default args. Useful for
	// passing --resume <uuid> or similar.
	AgentArgs []string

	// Env adds to the spawned process's environment. nil = inherit only.
	Env []string

	// RingBytes overrides ptyhost.Session default (1 MiB).
	RingBytes int
}

// Spawn creates a new session, registers it, persists metadata, and starts
// the underlying PTY child process.
func (r *Runtime) Spawn(spec SpawnSpec) (*SessionInfo, *session.Session, error) {
	if spec.Name == "" {
		return nil, nil, errors.New("runtime.Spawn: Name required")
	}
	if spec.Team == "" {
		spec.Team = "default"
	}
	if spec.Role == "" {
		spec.Role = RoleWorker
	}
	if spec.AgentSlug == "" {
		return nil, nil, errors.New("runtime.Spawn: AgentSlug required")
	}

	r.mu.Lock()
	if _, taken := r.byName[spec.Name]; taken {
		r.mu.Unlock()
		return nil, nil, fmt.Errorf("runtime.Spawn: name %q already in use", spec.Name)
	}
	r.mu.Unlock()

	// Resolve the agent via agentcatalog
	agent, err := agentcatalog.Lookup(spec.AgentSlug)
	if err != nil {
		return nil, nil, fmt.Errorf("runtime.Spawn: unknown agent %q: %w", spec.AgentSlug, err)
	}

	// Inject system prompt via the agent CLI's append-prompt flag when
	// SpawnSpec.SystemPrompt is non-empty. claude-code: --append-system-prompt
	// qwen-code: --system-prompt (qwen takes it as-is). Other agents: skip
	// silently — the prompt is still embedded in our per-agent MCP config
	// payload for agents that read it from there.
	extraArgs := append([]string(nil), spec.AgentArgs...)
	if spec.SystemPrompt != "" {
		switch spec.AgentSlug {
		case "claude-code":
			extraArgs = append(extraArgs, "--append-system-prompt", spec.SystemPrompt)
		case "qwen-code":
			extraArgs = append(extraArgs, "--system-prompt", spec.SystemPrompt)
		}
	}

	// Write per-session MCP config so the agent discovers chepherd's MCP
	// server. Writes a project-scoped .mcp.json next to the agent's cwd
	// AND a per-session env var pointing to it. Idempotent.
	mcpEnv, mcpCfgPath, err := r.writeMCPConfig(spec.Name, spec.Cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "runtime: warning: mcp config write failed for %s: %v\n", spec.Name, err)
	}
	envWithMCP := append(append([]string(nil), spec.Env...), mcpEnv...)
	// Tag the child process with its agent name so the MCP bridge can
	// forward it as actor identity on every JSON-RPC call. Without this
	// the server can't tell which shepherd / worker made which call
	// (#89). Read by the bridge in BridgeStdioToSocket via os.Getenv.
	envWithMCP = append(envWithMCP, "CHEPHERD_AGENT_NAME="+spec.Name)

	// For Claude Code, pass the absolute mcp-config path explicitly.
	// Without --mcp-config, Claude only loads .mcp.json after the
	// operator approves it in the workspace-trust dialog — which our
	// auto-bootstrapped shepherd will never see.
	if spec.AgentSlug == "claude-code" && mcpCfgPath != "" {
		extraArgs = append(extraArgs, "--mcp-config", mcpCfgPath)
	}

	argv, env := agent.Resolve(extraArgs, envSliceToMap(envWithMCP))

	// Strip TMUX env vars so spawned agents don't falsely detect tmux.
	// chepherd's ptyhost allocates a real PTY for each child — they're not
	// inside tmux. But if chepherd-v05 itself was started from a tmux
	// session, $TMUX leaks through to children and Claude Code emits
	// tmux-specific warnings + writes "copied to tmux buffer" messages
	// when text is selected. The fake context is the source of the
	// operator's confusion in the dashboard.
	env = stripEnv(env, "TMUX", "TMUX_PANE", "TMUX_PLUGIN_MANAGER_PATH")

	// Spawn the PTY child via ptyhost
	id := newSessionID(spec.Name)
	s, err := session.New(id, session.Spec{
		Command:   argv,
		Env:       env,
		Cwd:       spec.Cwd,
		RingBytes: spec.RingBytes,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("runtime.Spawn: ptyhost: %w", err)
	}

	// Resolve the effective stat sheet — operator override falls back to
	// the per-role default when fields are zero-valued.
	statSheet := DefaultStatSheet(string(spec.Role))
	if spec.StatSheet.ContextBudget != 0 {
		statSheet.ContextBudget = spec.StatSheet.ContextBudget
	}
	if spec.StatSheet.ModelTier != "" {
		statSheet.ModelTier = spec.StatSheet.ModelTier
	}
	if spec.StatSheet.DisciplineWeight != 0 {
		statSheet.DisciplineWeight = spec.StatSheet.DisciplineWeight
	}
	if spec.StatSheet.VelocityExpect != "" {
		statSheet.VelocityExpect = spec.StatSheet.VelocityExpect
	}
	if spec.StatSheet.TokenBudgetUSD != 0 {
		statSheet.TokenBudgetUSD = spec.StatSheet.TokenBudgetUSD
	}
	if len(spec.StatSheet.ToolAllowlist) > 0 {
		statSheet.ToolAllowlist = spec.StatSheet.ToolAllowlist
	}

	info := &SessionInfo{
		ID:           id,
		Name:         spec.Name,
		AgentSlug:    spec.AgentSlug,
		Team:         spec.Team,
		Role:         spec.Role,
		Cwd:          spec.Cwd,
		CreatedAt:    time.Now().UTC(),
		PID:          s.PID(),
		SystemPrompt: spec.SystemPrompt,
		StatSheet:    statSheet,
	}
	// Extract GitHub URL once at spawn — cheap (single git config read),
	// makes the right-pane "GitHub" link populate immediately. Branch is
	// refreshed in List() since it can change mid-session.
	info.GitHubURL, info.Branch = readGitContext(spec.Cwd)
	if spec.Role == RoleShepherd {
		info.Shepherding = []string{spec.Team}
	}

	act := &sessionActivity{created: time.Now()}
	r.mu.Lock()
	r.sessions[id] = s
	r.byName[spec.Name] = id
	r.info[id] = info
	r.activity[id] = act
	hooks := append([]func(*session.Session, string){}, r.spawnHooks...)
	r.mu.Unlock()

	if err := r.persistInfo(info); err != nil {
		// Non-fatal: session is live, just won't survive restart.
		fmt.Fprintf(os.Stderr, "runtime: persist %s failed: %v\n", id, err)
	}
	// Spawn a sniffer goroutine on the PTY output stream. It writes to
	// the activity tracker without ever touching r.mu so it can't deadlock
	// any caller of List/Get.
	go r.runActivitySniffer(s, act, id)
	for _, h := range hooks {
		h(s, spec.Name)
	}
	// Event: agent spawned
	r.RecordEvent(Event{
		Kind: "spawn", Actor: "runtime",
		Body: fmt.Sprintf("agent %q spawned (%s, team=%s, role=%s)", spec.Name, spec.AgentSlug, spec.Team, spec.Role),
		Meta: map[string]any{"name": spec.Name, "agent_slug": spec.AgentSlug, "team": spec.Team, "role": string(spec.Role), "cwd": spec.Cwd},
	})
	r.broadcast()
	return info, s, nil
}

// runActivitySniffer subscribes to a session's PTY output stream and
// tallies bytes per chunk. Stops when the session closes — and uses
// that close signal to flip SessionInfo.Exited so the dashboard sees
// the agent exit immediately (Ctrl-C / clean exit / killed child).
func (r *Runtime) runActivitySniffer(s *session.Session, act *sessionActivity, id string) {
	sub, _, err := s.Subscribe(256)
	if err != nil {
		return
	}
	defer s.Unsubscribe(sub)
	defer r.markExited(id) // PTY EOF → flip Exited immediately, no GC delay
	for {
		select {
		case <-sub.Done:
			return
		case chunk, ok := <-sub.Ch:
			if !ok {
				return
			}
			act.mu.Lock()
			act.total += int64(len(chunk))
			act.last = time.Now()
			act.recent = append(act.recent, recentChunk{at: act.last, size: len(chunk)})
			cutoff := time.Now().Add(-5 * time.Minute)
			for len(act.recent) > 0 && act.recent[0].at.Before(cutoff) {
				act.recent = act.recent[1:]
			}
			act.mu.Unlock()
		}
	}
}

// markExited flips the session's Exited flag + records its exit code.
// Called by the activity sniffer when the PTY EOFs. Clean exits
// (code == 0) get garbage-collected after 30 s by a separate goroutine;
// failed exits stay visible so the operator can see what went wrong.
func (r *Runtime) markExited(id string) {
	r.mu.Lock()
	info, ok := r.info[id]
	if !ok {
		r.mu.Unlock()
		return
	}
	if info.Exited {
		// Idempotent — sniffer can fire multiple "Done" paths.
		r.mu.Unlock()
		return
	}
	info.Exited = true
	sess := r.sessions[id]
	if sess != nil {
		info.ExitCode = sess.ExitCode()
	}
	name := info.Name
	code := info.ExitCode
	r.mu.Unlock()
	// Event: agent exited
	r.RecordEvent(Event{
		Kind: "exit", Actor: "runtime",
		Body: fmt.Sprintf("agent %q exited (code %d)", name, code),
		Meta: map[string]any{"name": name, "exit_code": code},
	})
	// Inbox: only for failed exits (clean exits = routine, lives in events
	// only per v0.6-F refinement)
	if code != 0 {
		r.HumanInbox("runtime", fmt.Sprintf("[failure] agent %q exited (code %d)", name, code))
	}

	// Clean exits (code 0) disappear from the list immediately — the
	// inbox message preserves the historical record and the operator's
	// expectation is instant cleanup.
	// Failed exits (non-zero) stay visible so the operator can see
	// what went wrong and inspect the final pane content.
	if code == 0 {
		r.mu.Lock()
		cur, ok := r.info[id]
		if ok && cur.Exited {
			delete(r.info, id)
			delete(r.sessions, id)
			delete(r.activity, id)
			delete(r.byName, cur.Name)
		}
		r.mu.Unlock()
	}
	r.broadcast()
}

// Assign updates an existing session's team + role.
func (r *Runtime) Assign(name string, team string, role Role) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.byName[name]
	if !ok {
		return fmt.Errorf("runtime.Assign: unknown session %q", name)
	}
	info := r.info[id]
	info.Team = team
	info.Role = role
	if role == RoleShepherd {
		has := false
		for _, t := range info.Shepherding {
			if t == team {
				has = true
				break
			}
		}
		if !has {
			info.Shepherding = append(info.Shepherding, team)
		}
	}
	_ = r.persistInfoLocked(info)
	r.cond.Broadcast()
	return nil
}

// GrantChannel adds a cross-team edge. Same edge added twice is idempotent.
func (r *Runtime) GrantChannel(fromTeam, toTeam, scope string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.grants {
		if r.grants[i].FromTeam == fromTeam && r.grants[i].ToTeam == toTeam {
			r.grants[i].Scope = scope
			return
		}
	}
	r.grants = append(r.grants, Grant{FromTeam: fromTeam, ToTeam: toTeam, Scope: scope})
	r.cond.Broadcast()
}

// SessionByName implements messagebus.SessionRegistry — returns the
// session pointer + its team name.
func (r *Runtime) SessionByName(name string) (*session.Session, string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.byName[name]
	if !ok {
		return nil, "", false
	}
	return r.sessions[id], r.info[id].Team, true
}

// SessionsByTribe implements messagebus.SessionRegistry — name kept for
// interface compat; semantically returns sessions in the given team.
func (r *Runtime) SessionsByTribe(team string) []*session.Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*session.Session
	for id, info := range r.info {
		if info.Team == team {
			out = append(out, r.sessions[id])
		}
	}
	return out
}

// HumanInbox implements messagebus.SessionRegistry.
func (r *Runtime) HumanInbox(from, body string) {
	r.mu.Lock()
	id := fmt.Sprintf("msg-%d", time.Now().UnixNano())
	r.humanInbox = append(r.humanInbox, HumanInboxEntry{ID: id, From: from, Body: body, At: time.Now(), Read: false})
	if len(r.humanInbox) > 500 {
		r.humanInbox = r.humanInbox[len(r.humanInbox)-500:]
	}
	r.cond.Broadcast()
	r.mu.Unlock()
}

// MarkInboxRead flips Read=true on a specific message by ID (idempotent).
// Returns false if the ID wasn't found.
func (r *Runtime) MarkInboxRead(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.humanInbox {
		if r.humanInbox[i].ID == id {
			r.humanInbox[i].Read = true
			return true
		}
	}
	return false
}

// MarkAllInboxRead marks every entry Read=true. Returns count touched.
func (r *Runtime) MarkAllInboxRead() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for i := range r.humanInbox {
		if !r.humanInbox[i].Read {
			r.humanInbox[i].Read = true
			n++
		}
	}
	return n
}

// IsCrossTribeGranted implements messagebus.SessionRegistry — name
// kept for interface compat; semantically checks cross-team grants.
func (r *Runtime) IsCrossTribeGranted(fromTeam, toTeam string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, g := range r.grants {
		if g.FromTeam == fromTeam && g.ToTeam == toTeam {
			return true
		}
	}
	return false
}

// IsSessionPaused implements messagebus.SessionRegistry. Reports whether
// the session's metadata has Paused=true.
func (r *Runtime) IsSessionPaused(s *session.Session) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, ss := range r.sessions {
		if ss == s {
			return r.info[id].Paused
		}
	}
	return false
}

// List returns a snapshot of all session metadata, augmented with the
// activity counters from each session's sniffer.
func (r *Runtime) List() []*SessionInfo {
	r.mu.Lock()
	type pair struct {
		info *SessionInfo
		act  *sessionActivity
	}
	pairs := make([]pair, 0, len(r.info))
	for id, info := range r.info {
		pairs = append(pairs, pair{info: info, act: r.activity[id]})
	}
	r.mu.Unlock()
	out := make([]*SessionInfo, 0, len(pairs))
	for _, p := range pairs {
		c := *p.info
		if p.act != nil {
			c.TotalBytes, c.Bytes5m, c.Chunks5m, c.IdleSeconds = p.act.snapshot()
		}
		// Refresh branch on every read — cheap and changes mid-session.
		if c.Cwd != "" {
			c.Branch = readGitBranch(c.Cwd)
		}
		// Refresh Claude extras (model + context tokens + sessionId UUID)
		// from the JSONL transcript. Cheap: tail-reads last ~200 lines.
		extras := r.claudeRuntimeExtras(c.AgentSlug, c.Cwd, c.CreatedAt)
		if extras.Model != "" {
			c.Model = extras.Model
		}
		if extras.ContextSize != 0 {
			c.ContextSize = extras.ContextSize
		}
		if extras.ContextTokens != 0 {
			c.ContextTokens = extras.ContextTokens
		}
		if extras.ClaudeUUID != "" {
			c.ClaudeUUID = extras.ClaudeUUID
		}
		out = append(out, &c)
	}
	return out
}

// SetScorecard stores shepherd's latest assessment of a worker. Idempotent:
// each call overwrites the previous scorecard for that name.
// `caller` is the agent name that produced the scorecard (recorded as the
// event actor for audit attribution — see #89).
func (r *Runtime) SetScorecard(caller, name string, sc Scorecard) error {
	r.mu.Lock()
	id, ok := r.byName[name]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("runtime.SetScorecard: unknown session %q", name)
	}
	sc.At = time.Now().UTC()
	r.info[id].Scorecard = &sc
	r.cond.Broadcast()
	r.mu.Unlock()
	if caller == "" {
		caller = "shepherd"
	}
	r.RecordEvent(Event{
		Kind: "scorecard", Actor: caller,
		Body: fmt.Sprintf("scorecard for %q: G=%.1f V=%.1f F=%.1f E=%.1f D=%.1f", name, sc.Goal, sc.Velocity, sc.Focus, sc.EndState, sc.Discipline),
		Meta: map[string]any{"target": name, "G": sc.Goal, "V": sc.Velocity, "F": sc.Focus, "E": sc.EndState, "D": sc.Discipline, "note": sc.Note},
	})
	return nil
}

// RecordVerdict appends to a session's intervention history. Only
// coach/intervene verdicts increment the count; silent/praise are
// surfaced for "last_verdict" but don't bump InterventionCount.
func (r *Runtime) RecordVerdict(caller, name, verdict, msg string) error {
	r.mu.Lock()
	id, ok := r.byName[name]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("runtime.RecordVerdict: unknown session %q", name)
	}
	info := r.info[id]
	info.LastVerdict = verdict
	info.LastVerdictAt = time.Now().UTC()
	info.LastVerdictMsg = msg
	if verdict == "coach" || verdict == "intervene" {
		info.InterventionCount++
	}
	r.cond.Broadcast()
	r.mu.Unlock()
	if caller == "" {
		caller = "shepherd"
	}
	r.RecordEvent(Event{
		Kind: "verdict", Actor: caller,
		Body: fmt.Sprintf("verdict for %q: %s — %s", name, verdict, msg),
		Meta: map[string]any{"target": name, "verdict": verdict, "message": msg},
	})
	return nil
}

// Inbox returns the recent human-inbox entries.
func (r *Runtime) Inbox() []HumanInboxEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]HumanInboxEntry, len(r.humanInbox))
	copy(out, r.humanInbox)
	return out
}

// Get returns the ptyhost.Session by name, or nil.
func (r *Runtime) Get(name string) (*session.Session, *SessionInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.byName[name]
	if !ok {
		return nil, nil
	}
	return r.sessions[id], r.info[id]
}

// UpdateStatSheet replaces the operator-configurable stat sheet on a
// running agent. Zero-valued fields in the patch are interpreted as
// "leave alone" (per-field merge, not whole-sheet replace).
func (r *Runtime) UpdateStatSheet(name string, patch AgentStatSheet) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.byName[name]
	if !ok {
		return fmt.Errorf("runtime.UpdateStatSheet: unknown session %q", name)
	}
	cur := r.info[id].StatSheet
	if patch.ContextBudget != 0 {
		cur.ContextBudget = patch.ContextBudget
	}
	if patch.ModelTier != "" {
		cur.ModelTier = patch.ModelTier
	}
	if patch.DisciplineWeight != 0 {
		cur.DisciplineWeight = patch.DisciplineWeight
	}
	if patch.VelocityExpect != "" {
		cur.VelocityExpect = patch.VelocityExpect
	}
	if patch.TokenBudgetUSD != 0 {
		cur.TokenBudgetUSD = patch.TokenBudgetUSD
	}
	if patch.ToolAllowlist != nil {
		cur.ToolAllowlist = patch.ToolAllowlist
	}
	r.info[id].StatSheet = cur
	_ = r.persistInfoLocked(r.info[id])
	return nil
}

// PokePrompt writes a refined working-instructions message to a running
// agent's PTY and records the new effective prompt on the SessionInfo.
// Equivalent to typing "Your updated working instructions from the
// operator: <body>" into the agent's REPL. Uses the same kitty-kbd-safe
// two-write pattern as the shepherd bootstrap.
func (r *Runtime) PokePrompt(name string, body string) error {
	r.mu.Lock()
	id, ok := r.byName[name]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("runtime.PokePrompt: unknown session %q", name)
	}
	sess := r.sessions[id]
	r.info[id].SystemPrompt = body
	_ = r.persistInfoLocked(r.info[id])
	r.mu.Unlock()
	if sess == nil {
		return fmt.Errorf("runtime.PokePrompt: session %q has no live PTY", name)
	}
	wrapped := "Your updated working instructions from the operator (please incorporate going forward): " + body
	_, _ = sess.Write([]byte(wrapped))
	time.Sleep(120 * time.Millisecond)
	_, _ = sess.Write([]byte("\r"))
	r.RecordEvent(Event{
		Kind:  "prompt_poke",
		Actor: "operator",
		Body:  fmt.Sprintf("operator updated prompt for %q (%d chars)", name, len(body)),
		Meta:  map[string]any{"target": name},
	})
	return nil
}

// Pause sets the .Paused bit on the named session. The relay watcher
// honors this on routed messages.
func (r *Runtime) Pause(name string, paused bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.byName[name]
	if !ok {
		return fmt.Errorf("runtime.Pause: unknown session %q", name)
	}
	r.info[id].Paused = paused
	_ = r.persistInfoLocked(r.info[id])
	r.cond.Broadcast()
	return nil
}

// Stop closes the named session's PTY and removes it from the registry.
func (r *Runtime) Stop(name string) error {
	r.mu.Lock()
	id, ok := r.byName[name]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("runtime.Stop: unknown session %q", name)
	}
	s := r.sessions[id]
	delete(r.sessions, id)
	delete(r.byName, name)
	delete(r.info, id)
	r.mu.Unlock()
	if s != nil {
		_ = s.Close()
	}
	_ = os.Remove(filepath.Join(r.stateDir, "sessions", id+".json"))
	r.broadcast()
	return nil
}

// claudeRuntimeExtras reads the most recent Claude JSONL written under
// ~/.claude/projects/<encoded-cwd>/*.jsonl for this session's cwd, and
// extracts model + last-usage context tokens + sessionId. Cheap: only
// the last ~80 lines of a single file are parsed. Returns zero values
// for non-claude agents or sessions whose JSONL doesn't exist yet.
//
// Why we read JSONL rather than ask Claude over IPC: Claude Code doesn't
// expose a control socket, but it does persist its full transcript with
// per-message usage blocks. Reading is harmless + reflects reality.
type claudeExtras struct {
	Model         string
	ContextTokens int
	ContextSize   int
	ClaudeUUID    string
}

func (r *Runtime) claudeRuntimeExtras(agentSlug, cwd string, spawnedAfter time.Time) claudeExtras {
	if agentSlug != "claude-code" {
		return claudeExtras{}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return claudeExtras{}
	}
	projects := filepath.Join(home, ".claude", "projects")
	entries, err := os.ReadDir(projects)
	if err != nil {
		return claudeExtras{}
	}
	// Find the project dir whose decoded path matches cwd
	var projDir string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if decodeClaudeProjectDirSimple(e.Name()) == cwd {
			projDir = filepath.Join(projects, e.Name())
			break
		}
	}
	if projDir == "" {
		return claudeExtras{}
	}
	files, err := os.ReadDir(projDir)
	if err != nil {
		return claudeExtras{}
	}
	// Pick the most recent .jsonl modified after spawnedAfter (so we
	// attach to THIS spawn's transcript, not a leftover from earlier).
	var bestPath string
	var bestMod time.Time
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
			continue
		}
		info, err := f.Info()
		if err != nil {
			continue
		}
		if !spawnedAfter.IsZero() && info.ModTime().Before(spawnedAfter) {
			continue
		}
		if info.ModTime().After(bestMod) {
			bestMod = info.ModTime()
			bestPath = filepath.Join(projDir, f.Name())
		}
	}
	if bestPath == "" {
		return claudeExtras{}
	}
	uuid := strings.TrimSuffix(filepath.Base(bestPath), ".jsonl")
	// Tail-read the file: only need the last assistant usage block.
	data, err := os.ReadFile(bestPath)
	if err != nil {
		return claudeExtras{ClaudeUUID: uuid}
	}
	// Walk lines back-to-front looking for the last record with message.model + usage
	lines := strings.Split(string(data), "\n")
	var model string
	var ctxTokens int
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-200; i-- {
		ln := strings.TrimSpace(lines[i])
		if ln == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(ln), &rec); err != nil {
			continue
		}
		msg, _ := rec["message"].(map[string]any)
		if msg == nil {
			continue
		}
		if m, ok := msg["model"].(string); ok && m != "" && m != "<synthetic>" {
			if model == "" {
				model = m
			}
		}
		usage, _ := msg["usage"].(map[string]any)
		if usage != nil && ctxTokens == 0 {
			ctxTokens = sumIntField(usage, "input_tokens") +
				sumIntField(usage, "cache_creation_input_tokens") +
				sumIntField(usage, "cache_read_input_tokens")
		}
		if model != "" && ctxTokens > 0 {
			break
		}
	}
	ctxSize := contextSizeFor(model)
	// If observed usage exceeds the default 200k cap, the session is
	// running the 1M extended-context beta. Upgrade the size so the
	// UI's "used %" stays meaningful (otherwise it would render > 100%).
	if ctxTokens > 200_000 && ctxSize <= 200_000 {
		ctxSize = 1_000_000
		// Tag the model so the UI's modelLabel() shows [1m] suffix.
		if model != "" && !strings.Contains(model, "[1m]") {
			model = model + "[1m]"
		}
	}
	return claudeExtras{
		Model:         model,
		ContextTokens: ctxTokens,
		ContextSize:   ctxSize,
		ClaudeUUID:    uuid,
	}
}

func decodeClaudeProjectDirSimple(name string) string {
	// Claude encodes /home/openova/repos/chepherd as -home-openova-repos-chepherd
	// (replaces / with -). To decode, simply replace - back to /. This is the
	// "simple" form — it can confuse hyphenated repo names, but is sufficient
	// for matching since we're checking equality against an absolute path.
	if !strings.HasPrefix(name, "-") {
		return name
	}
	return "/" + strings.ReplaceAll(name[1:], "-", "/")
}

func sumIntField(m map[string]any, k string) int {
	v, ok := m[k]
	if !ok {
		return 0
	}
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	}
	return 0
}

func contextSizeFor(model string) int {
	// Conservative defaults — Anthropic doesn't publish a single source
	// of truth machine-readable. Override the 1M variant by checking
	// for an explicit [1m] tag (our convention).
	m := strings.ToLower(model)
	if strings.Contains(m, "[1m]") {
		return 1_000_000
	}
	if strings.Contains(m, "opus-4") {
		return 200_000
	}
	if strings.Contains(m, "sonnet") {
		return 200_000
	}
	if strings.Contains(m, "haiku") {
		return 200_000
	}
	if strings.Contains(m, "claude-3") {
		return 200_000
	}
	return 0
}

// Restart stops the named session + spawns a replacement with the same
// AgentSlug, Cwd, Role, Team, SystemPrompt, and StatSheet. Used for
// re-priming an agent after editing its prompt/skills (when poke-prompt
// isn't strong enough). Returns the new SessionInfo on success.
func (r *Runtime) Restart(name string) (*SessionInfo, error) {
	r.mu.Lock()
	id, ok := r.byName[name]
	if !ok {
		r.mu.Unlock()
		return nil, fmt.Errorf("runtime.Restart: unknown session %q", name)
	}
	info := r.info[id]
	spec := SpawnSpec{
		Name:         info.Name,
		AgentSlug:    info.AgentSlug,
		Team:         info.Team,
		Role:         info.Role,
		Cwd:          info.Cwd,
		SystemPrompt: info.SystemPrompt,
		StatSheet:    info.StatSheet,
	}
	r.mu.Unlock()
	if err := r.Stop(name); err != nil {
		return nil, err
	}
	time.Sleep(500 * time.Millisecond)
	newInfo, _, err := r.Spawn(spec)
	if err != nil {
		return nil, err
	}
	r.RecordEvent(Event{
		Kind:  "agent_restart",
		Actor: "operator",
		Body:  fmt.Sprintf("agent %q restarted (new id=%s)", name, newInfo.ID),
		Meta:  map[string]any{"target": name},
	})
	return newInfo, nil
}

// Wait blocks until the runtime's state changes (used by long-poll style
// subscribers like the TUI ticker). Cancellable via the returned function.
func (r *Runtime) Wait() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cond.Wait()
}

func (r *Runtime) broadcast() {
	r.mu.Lock()
	r.cond.Broadcast()
	r.mu.Unlock()
}

// --- persistence helpers ---

func (r *Runtime) persistInfo(info *SessionInfo) error {
	path := filepath.Join(r.stateDir, "sessions", info.ID+".json")
	b, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// caller holds r.mu
func (r *Runtime) persistInfoLocked(info *SessionInfo) error {
	r.mu.Unlock()
	err := r.persistInfo(info)
	r.mu.Lock()
	return err
}

// writeMCPConfig writes a per-session .mcp.json into the session's CWD
// (or a chepherd-managed dir if cwd is empty) so the spawned agent
// discovers chepherd's MCP server. Returns env vars to forward to the
// child process for tools that prefer env-pointing over file discovery.
func (r *Runtime) writeMCPConfig(sessionName, cwd string) ([]string, string, error) {
	cfgDir := filepath.Join(r.stateDir, "sessions", sessionName)
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		return nil, "", err
	}
	sockPath := filepath.Join(r.stateDir, "runtime.sock")
	// Use the absolute path of the currently-running chepherd binary so
	// the MCP-bridge subprocess matches the running runtime regardless
	// of PATH or install name (chepherd vs chepherd-v05).
	chepBin, _ := os.Executable()
	if chepBin == "" {
		chepBin = "chepherd"
	}
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"chepherd": map[string]any{
				"command": chepBin,
				"args":    []string{"mcp", "--sock", sockPath},
				"env":     map[string]string{},
			},
		},
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, "", err
	}
	cfgPath := filepath.Join(cfgDir, ".mcp.json")
	if err := os.WriteFile(cfgPath, b, 0o600); err != nil {
		return nil, "", err
	}
	// Symlink into cwd as ./.mcp.json so Claude Code's per-project lookup
	// finds our config. If a symlink already exists, repair it if it
	// points at a missing target (e.g. an old per-session config that
	// has been cleaned up); never clobber a real non-symlink file the
	// user may have authored themselves.
	if cwd != "" {
		target := filepath.Join(cwd, ".mcp.json")
		fi, lerr := os.Lstat(target)
		switch {
		case lerr != nil && os.IsNotExist(lerr):
			_ = os.Symlink(cfgPath, target)
		case lerr == nil && fi.Mode()&os.ModeSymlink != 0:
			// Existing symlink — repair if its target is missing or stale.
			if _, srcErr := os.Stat(target); srcErr != nil {
				_ = os.Remove(target)
				_ = os.Symlink(cfgPath, target)
			} else {
				// Symlink resolves OK — repoint it at the freshly-written
				// config for this session so the operator gets the
				// most-recent .mcp.json semantics.
				_ = os.Remove(target)
				_ = os.Symlink(cfgPath, target)
			}
		}
		// If target is a real file the user wrote, leave it alone.
	}
	// Env hint for agents that read MCP server URL directly (e.g. some
	// experimental SDK paths). Harmless if unused.
	return []string{
		"CHEPHERD_MCP_SOCK=" + sockPath,
		"CHEPHERD_MCP_CONFIG=" + cfgPath,
	}, cfgPath, nil
}

// readGitContext runs `git -C <cwd>` twice to extract the remote-origin
// HTTPS URL and the current branch. Returns ("", "") if cwd isn't a git
// repo or git is unavailable. Called once at spawn; the URL is stored.
func readGitContext(cwd string) (githubURL, branch string) {
	if cwd == "" {
		return "", ""
	}
	url := readGitOriginURL(cwd)
	return githubFromGitURL(url), readGitBranch(cwd)
}

func readGitOriginURL(cwd string) string {
	out, err := execCommand("git", "-C", cwd, "config", "--get", "remote.origin.url")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func readGitBranch(cwd string) string {
	out, err := execCommand("git", "-C", cwd, "branch", "--show-current")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// githubFromGitURL normalizes a git remote URL (ssh or https) to the
// canonical https://github.com/org/repo form. Returns "" for non-github
// remotes (gitea, gitlab) — those still work in the dashboard via the
// raw URL but no special-casing.
func githubFromGitURL(url string) string {
	url = strings.TrimSpace(url)
	url = strings.TrimSuffix(url, ".git")
	// git@github.com:org/repo
	if strings.HasPrefix(url, "git@github.com:") {
		return "https://github.com/" + strings.TrimPrefix(url, "git@github.com:")
	}
	// ssh://git@github.com/org/repo
	if strings.HasPrefix(url, "ssh://git@github.com/") {
		return "https://github.com/" + strings.TrimPrefix(url, "ssh://git@github.com/")
	}
	// https://github.com/org/repo — return as is (already canonical).
	if strings.HasPrefix(url, "https://github.com/") {
		return url
	}
	// Non-github remote (gitea, etc) — return raw URL so the dashboard
	// link still works.
	if url != "" {
		return url
	}
	return ""
}

// execCommand is a thin wrapper so test code can stub git/etc. The
// runtime production path uses os/exec directly.
var execCommand = func(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.Output()
	return string(out), err
}

// stripEnv returns env with the named keys removed. Used to scrub
// inherited env vars (like TMUX) that would falsely orient the child
// process about its execution context.
func stripEnv(env []string, keys ...string) []string {
	drop := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		drop[k] = struct{}{}
	}
	out := env[:0]
	for _, kv := range env {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			out = append(out, kv)
			continue
		}
		if _, skip := drop[kv[:eq]]; !skip {
			out = append(out, kv)
		}
	}
	return out
}

// =========== v0.6 unified-model methods (Agent + Team + Membership) ===========

// CreateTeam adds a new Team to the runtime. Idempotent — if a team with
// the same name already exists, returns it unchanged. Returns the team
// + a "created" bool (true if new, false if pre-existed).
func (r *Runtime) CreateTeam(name string, canonPath string, topology Topology) (*Team, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.teams[name]; ok {
		return existing, false
	}
	if topology == "" {
		topology = TopologyHub
	}
	if canonPath == "" {
		canonPath = filepath.Join(r.stateDir, "teams", name, "CLAUDE.md")
	}
	t := &Team{
		Name:      name,
		CanonPath: canonPath,
		Topology:  topology,
		CreatedAt: time.Now().UTC(),
	}
	r.teams[name] = t
	r.cond.Broadcast()
	return t, true
}

// JoinTeam adds a Membership. Idempotent on (agent, team) — if already
// joined, updates the role + brief override. Auto-creates the team if it
// doesn't exist (with default topology=hub).
func (r *Runtime) JoinTeam(agentName, teamName string, role MembershipRole, briefOverride string) (*Membership, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byName[agentName]; !ok {
		return nil, fmt.Errorf("runtime.JoinTeam: unknown agent %q", agentName)
	}
	if _, ok := r.teams[teamName]; !ok {
		canonPath := filepath.Join(r.stateDir, "teams", teamName, "CLAUDE.md")
		r.teams[teamName] = &Team{
			Name:      teamName,
			CanonPath: canonPath,
			Topology:  TopologyHub,
			CreatedAt: time.Now().UTC(),
		}
	}
	key := agentName + "|" + teamName
	m := &Membership{
		AgentName:     agentName,
		TeamName:      teamName,
		Role:          role,
		BriefOverride: briefOverride,
		JoinedAt:      time.Now().UTC(),
	}
	r.memberships[key] = m
	r.cond.Broadcast()
	return m, nil
}

// DeleteTeam removes the team + all its memberships. Returns an error
// if any agent is still actively rooted in this team (Team field on
// SessionInfo). Refuses to delete the "default" team.
func (r *Runtime) DeleteTeam(name string) error {
	if name == "default" {
		return fmt.Errorf("runtime.DeleteTeam: refusing to delete the default team")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.teams[name]; !ok {
		return fmt.Errorf("runtime.DeleteTeam: unknown team %q", name)
	}
	for _, info := range r.info {
		if info.Team == name && !info.Exited {
			return fmt.Errorf("runtime.DeleteTeam: agent %q still rooted in team %q — move or stop the agent first", info.Name, name)
		}
	}
	for key, m := range r.memberships {
		if m.TeamName == name {
			delete(r.memberships, key)
		}
	}
	delete(r.teams, name)
	r.cond.Broadcast()
	return nil
}

// UpdateTeam renames a team and/or changes its topology. newName == ""
// keeps the existing name; topology == "" keeps the existing topology.
func (r *Runtime) UpdateTeam(name, newName string, topology Topology) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.teams[name]
	if !ok {
		return fmt.Errorf("runtime.UpdateTeam: unknown team %q", name)
	}
	if topology != "" {
		t.Topology = topology
	}
	if newName != "" && newName != name {
		if _, taken := r.teams[newName]; taken {
			return fmt.Errorf("runtime.UpdateTeam: name %q already in use", newName)
		}
		t.Name = newName
		delete(r.teams, name)
		r.teams[newName] = t
		// Cascade: rename in memberships + SessionInfo
		for key, m := range r.memberships {
			if m.TeamName == name {
				m.TeamName = newName
				delete(r.memberships, key)
				r.memberships[m.AgentName+"|"+newName] = m
			}
		}
		for _, info := range r.info {
			if info.Team == name {
				info.Team = newName
			}
			for i, sh := range info.Shepherding {
				if sh == name {
					info.Shepherding[i] = newName
				}
			}
		}
	}
	r.cond.Broadcast()
	return nil
}

// LeaveTeam removes a membership. Returns true if removed, false if not present.
func (r *Runtime) LeaveTeam(agentName, teamName string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := agentName + "|" + teamName
	if _, ok := r.memberships[key]; !ok {
		return false
	}
	delete(r.memberships, key)
	r.cond.Broadcast()
	return true
}

// ListTeams returns all teams (snapshot copy).
func (r *Runtime) ListTeams() []*Team {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Team, 0, len(r.teams))
	for _, t := range r.teams {
		c := *t
		out = append(out, &c)
	}
	return out
}

// ListMemberships returns memberships, optionally filtered by agentName
// and/or teamName (pass "" to skip a filter).
func (r *Runtime) ListMemberships(agentName, teamName string) []*Membership {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Membership, 0, len(r.memberships))
	for _, m := range r.memberships {
		if agentName != "" && m.AgentName != agentName {
			continue
		}
		if teamName != "" && m.TeamName != teamName {
			continue
		}
		c := *m
		out = append(out, &c)
	}
	return out
}

// SetReviewAxis records a per-axis review on a target agent. Used by
// reviewer agents in the council pattern (v0.6-C). Multiple reviewers
// can write different axes for the same target; the shepherd composes
// the final scorecard by reading all of them.
func (r *Runtime) SetReviewAxis(reviewer, target, axis string, score float64, note string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byName[target]; !ok {
		return fmt.Errorf("runtime.SetReviewAxis: unknown target agent %q", target)
	}
	if r.axisReviews[target] == nil {
		r.axisReviews[target] = make(map[string]*AxisReview)
	}
	r.axisReviews[target][axis] = &AxisReview{
		Reviewer: reviewer,
		Axis:     axis,
		Score:    score,
		Note:     note,
		At:       time.Now().UTC(),
	}
	r.cond.Broadcast()
	return nil
}

// ListReviews returns all per-axis reviews for a target agent. Used by
// the shepherd to compose a final scorecard.
func (r *Runtime) ListReviews(target string) []*AxisReview {
	r.mu.Lock()
	defer r.mu.Unlock()
	byAxis := r.axisReviews[target]
	if byAxis == nil {
		return nil
	}
	out := make([]*AxisReview, 0, len(byAxis))
	for _, rev := range byAxis {
		c := *rev
		out = append(out, &c)
	}
	return out
}

// --- utilities ---

func newSessionID(name string) string {
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano())
}

func envSliceToMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, e := range env {
		for i := range e {
			if e[i] == '=' {
				m[e[:i]] = e[i+1:]
				break
			}
		}
	}
	return m
}
