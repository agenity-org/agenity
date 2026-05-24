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
	"path/filepath"
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
// context (name, tribe, role, etc.).
type SessionInfo struct {
	ID        string    `json:"id"`        // ptyhost session ID (stable across restart attempts)
	Name      string    `json:"name"`      // canonical @-address (e.g. "adam", "iogrid-1")
	AgentSlug string    `json:"agent"`     // claude-code, qwen-code, etc.
	Tribe     string    `json:"tribe"`     // membership (one tribe for now; multi later)
	Role      Role      `json:"role"`      // worker | shepherd
	Cwd       string    `json:"cwd"`       // working directory the agent was spawned in
	CreatedAt time.Time `json:"created_at"`
	Paused    bool      `json:"paused"`

	// Set non-empty only when Role == RoleShepherd. Tribes this shepherd oversees.
	Shepherding []string `json:"shepherding,omitempty"`

	// Activity counters (populated by the runtime's per-session sniffer).
	// Reported on every Get/List; values are wall-clock snapshots.
	TotalBytes  int64   `json:"total_bytes"`
	Bytes5m     int64   `json:"bytes_5m"`
	Chunks5m    int     `json:"chunks_5m"` // distinct PTY writes in last 5 min — engagement / burst rate
	IdleSeconds float64 `json:"idle_seconds"`
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

// Grant is a cross-tribe permission edge: agents in fromTribe may
// @<member> agents in toTribe.
type Grant struct {
	FromTribe string `json:"from_tribe"`
	ToTribe   string `json:"to_tribe"`
	Scope     string `json:"scope"` // "read" | "write" | "both"
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
		sessions: make(map[string]*session.Session),
		byName:   make(map[string]string),
		info:     make(map[string]*SessionInfo),
		stateDir: stateDir,
		activity: make(map[string]*sessionActivity),
	}
	r.cond = sync.NewCond(&r.mu)
	return r, nil
}

// SpawnSpec describes how to bring up a new session.
type SpawnSpec struct {
	Name      string // canonical @-address; must be unique
	AgentSlug string // claude-code | qwen-code | aider | ...
	Tribe     string // default "default"
	Role      Role   // default worker
	Cwd       string // optional working dir
	SystemPrompt string // optional override for the agent's system prompt

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
	if spec.Tribe == "" {
		spec.Tribe = "default"
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

	// For Claude Code, pass the absolute mcp-config path explicitly.
	// Without --mcp-config, Claude only loads .mcp.json after the
	// operator approves it in the workspace-trust dialog — which our
	// auto-bootstrapped shepherd will never see.
	if spec.AgentSlug == "claude-code" && mcpCfgPath != "" {
		extraArgs = append(extraArgs, "--mcp-config", mcpCfgPath)
	}

	argv, env := agent.Resolve(extraArgs, envSliceToMap(envWithMCP))

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

	info := &SessionInfo{
		ID:        id,
		Name:      spec.Name,
		AgentSlug: spec.AgentSlug,
		Tribe:     spec.Tribe,
		Role:      spec.Role,
		Cwd:       spec.Cwd,
		CreatedAt: time.Now().UTC(),
	}
	if spec.Role == RoleShepherd {
		info.Shepherding = []string{spec.Tribe}
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
	go r.runActivitySniffer(s, act)
	for _, h := range hooks {
		h(s, spec.Name)
	}
	r.broadcast()
	return info, s, nil
}

// runActivitySniffer subscribes to a session's PTY output stream and
// tallies bytes per chunk. Stops when the session closes.
func (r *Runtime) runActivitySniffer(s *session.Session, act *sessionActivity) {
	sub, _, err := s.Subscribe(256)
	if err != nil {
		return
	}
	defer s.Unsubscribe(sub)
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
			// Trim windows older than 5 min so this slice doesn't grow unbounded.
			cutoff := time.Now().Add(-5 * time.Minute)
			for len(act.recent) > 0 && act.recent[0].at.Before(cutoff) {
				act.recent = act.recent[1:]
			}
			act.mu.Unlock()
		}
	}
}

// Assign updates an existing session's tribe + role. Used for transfer_adam,
// move-between-tribes, promotion/demotion.
func (r *Runtime) Assign(name string, tribe string, role Role) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.byName[name]
	if !ok {
		return fmt.Errorf("runtime.Assign: unknown session %q", name)
	}
	info := r.info[id]
	info.Tribe = tribe
	info.Role = role
	if role == RoleShepherd {
		// Append tribe to Shepherding if not already
		has := false
		for _, t := range info.Shepherding {
			if t == tribe {
				has = true
				break
			}
		}
		if !has {
			info.Shepherding = append(info.Shepherding, tribe)
		}
	}
	_ = r.persistInfoLocked(info)
	r.cond.Broadcast()
	return nil
}

// GrantChannel adds a cross-tribe edge. Same edge added twice is idempotent.
func (r *Runtime) GrantChannel(fromTribe, toTribe, scope string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.grants {
		if r.grants[i].FromTribe == fromTribe && r.grants[i].ToTribe == toTribe {
			r.grants[i].Scope = scope
			return
		}
	}
	r.grants = append(r.grants, Grant{FromTribe: fromTribe, ToTribe: toTribe, Scope: scope})
	r.cond.Broadcast()
}

// SessionByName implements messagebus.SessionRegistry.
func (r *Runtime) SessionByName(name string) (*session.Session, string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.byName[name]
	if !ok {
		return nil, "", false
	}
	return r.sessions[id], r.info[id].Tribe, true
}

// SessionsByTribe implements messagebus.SessionRegistry.
func (r *Runtime) SessionsByTribe(tribe string) []*session.Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*session.Session
	for id, info := range r.info {
		if info.Tribe == tribe {
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

// IsCrossTribeGranted implements messagebus.SessionRegistry.
func (r *Runtime) IsCrossTribeGranted(fromTribe, toTribe string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, g := range r.grants {
		if g.FromTribe == fromTribe && g.ToTribe == toTribe {
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
	// Copy out under the lock; compute snapshot() outside it (sniffer
	// has its own mu).
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
		out = append(out, &c)
	}
	return out
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
