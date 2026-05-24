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
}

// HumanInboxEntry is a routed @human message in the dashboard's "Messages → Human" view.
type HumanInboxEntry struct {
	From string    `json:"from"`
	Body string    `json:"body"`
	At   time.Time `json:"at"`
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
	argv, env := agent.Resolve(spec.AgentArgs, envSliceToMap(spec.Env))

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

	r.mu.Lock()
	r.sessions[id] = s
	r.byName[spec.Name] = id
	r.info[id] = info
	r.mu.Unlock()

	if err := r.persistInfo(info); err != nil {
		// Non-fatal: session is live, just won't survive restart.
		fmt.Fprintf(os.Stderr, "runtime: persist %s failed: %v\n", id, err)
	}
	r.broadcast()
	return info, s, nil
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
	r.humanInbox = append(r.humanInbox, HumanInboxEntry{From: from, Body: body, At: time.Now()})
	if len(r.humanInbox) > 500 {
		r.humanInbox = r.humanInbox[len(r.humanInbox)-500:]
	}
	r.cond.Broadcast()
	r.mu.Unlock()
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

// List returns a snapshot of all session metadata.
func (r *Runtime) List() []*SessionInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*SessionInfo, 0, len(r.info))
	for _, info := range r.info {
		c := *info
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
