// Package runtime owns the live operational state of a chepherd instance:
// session registry, tribe + role + shepherding metadata, and the spawn /
// assign / list / grant operations the MCP server and TUI both call.
//
// One Runtime per chepherd process. Goroutine-safe (sync.Mutex-protected).
// Persists per-session metadata to ~/.local/state/chepherd/sessions/<id>.json
// so the runtime can be restarted without losing tribe/role assignments.
package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	agententity "github.com/chepherd/chepherd/internal/agent"
	"github.com/chepherd/chepherd/internal/persistence"
	"github.com/chepherd/chepherd/internal/ptyhost/agentcatalog"
	"github.com/chepherd/chepherd/internal/ptyhost/session"
	"github.com/chepherd/chepherd/internal/scrummaster"
	"github.com/google/uuid"
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
	ID        string `json:"id"`    // ptyhost session ID (stable across restart attempts)
	Name      string `json:"name"`  // canonical @-address (e.g. "iogrid-1")
	AgentSlug string `json:"agent"` // claude-code, qwen-code, etc.

	// #172 — first-class Agent entity backing this session. AgentID is
	// the stable UUID; PVCHandle is the podman-volume / k8s-PVC mounted
	// at /workspace inside the agent container. Both stay constant
	// across resume / handoff (#173 / #STAGE-3); the live session ID
	// (above) rotates per attach.
	AgentID   string    `json:"agent_id,omitempty"`
	PVCHandle string    `json:"pvc_handle,omitempty"`
	Team      string    `json:"team"` // team membership — workers in same team @-reach freely
	Role      Role      `json:"role"` // worker | shepherd
	Cwd       string    `json:"cwd"`  // working directory the agent was spawned in
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

	// #363 — trailing tail of PTY stdout/stderr captured at agent
	// death. Surfaced in dashboard agent-details.
	LastOutput string `json:"last_output,omitempty"`

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

	// TrustBand is derived from Scorecard.D at scorecard-update time.
	// Drives the shepherd's adaptive tick interval.
	TrustBand TrustBand `json:"trust_band,omitempty"`

	// ScrumMaster verdict history — count of coach/intervene verdicts AND
	// the most recent one (with timestamp + message). Empty until first
	// non-silent verdict.
	InterventionCount int       `json:"intervention_count,omitempty"`
	LastVerdict       string    `json:"last_verdict,omitempty"` // silent|praise|coach|intervene
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
	Model         string `json:"model,omitempty"`          // e.g. "claude-opus-4-7"
	ContextSize   int    `json:"context_size,omitempty"`   // model context window (e.g. 200_000 or 1_000_000)
	ContextTokens int    `json:"context_tokens,omitempty"` // tokens currently held in the window (last usage block)
	ClaudeUUID    string `json:"claude_uuid,omitempty"`    // sessionId of the JSONL Claude is appending to

	// ContainerRuntime is "podman", "docker", or "bare" — how this agent was spawned.
	ContainerRuntime string `json:"container_runtime,omitempty"`
	// AgentHomeDir is the per-agent persistent home directory on the host.
	AgentHomeDir string `json:"agent_home_dir,omitempty"`
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

// TrustBand is the adaptive coaching level derived from scorecard data.
// Maps to tick intervals: trusted=30m, standard=10m, concerned=5m, crisis=2m.
type TrustBand string

const (
	TrustBandTrusted   TrustBand = "trusted"   // D >= 8
	TrustBandStandard  TrustBand = "standard"  // D >= 6
	TrustBandConcerned TrustBand = "concerned" // D >= 4
	TrustBandCrisis    TrustBand = "crisis"    // D < 4
)

// BandFromScorecard derives the trust band from a scorecard's discipline
// score. Returns TrustBandStandard if sc is nil (no scorecard yet).
func BandFromScorecard(sc *Scorecard) TrustBand {
	if sc == nil {
		return TrustBandStandard
	}
	switch {
	case sc.Discipline >= 8:
		return TrustBandTrusted
	case sc.Discipline >= 6:
		return TrustBandStandard
	case sc.Discipline >= 4:
		return TrustBandConcerned
	default:
		return TrustBandCrisis
	}
}

// BandTickInterval returns the shepherd tick interval for a given trust band.
func BandTickInterval(b TrustBand) time.Duration {
	switch b {
	case TrustBandTrusted:
		return 30 * time.Minute
	case TrustBandConcerned:
		return 5 * time.Minute
	case TrustBandCrisis:
		return 2 * time.Minute
	default: // standard
		return 10 * time.Minute
	}
}

// sessionActivity holds the running tally for one session — used by the
// runtime's per-session sniffer goroutine to populate SessionInfo's
// activity counters without locking the main runtime.
type sessionActivity struct {
	mu      sync.Mutex
	total   int64
	last    time.Time
	created time.Time
	recent  []recentChunk // chunks within the last 5 minutes
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
	stateDir         string           // ~/.local/state/chepherd
	containerRuntime ContainerRuntime // podman | docker | bare
	spawner          AgentSpawner     // podman-sidecar | operator | direct (#127)

	// mcpListenAddr is the host:port the chepherd MCP server is bound on.
	// Set via SetMCPListenAddr from cmd/run.go after the MCP server boots.
	// Used by writeMCPConfig (#595) to derive the .mcp.json URL when no
	// explicit override is set — replaces the hardcoded :9090 fallback.
	mcpListenAddr string

	// #270 — instanceUUID is the 8-char SHA256 fingerprint of the
	// absolute state-dir path. Two chepherd binaries with distinct
	// --state-dir flags get distinct UUIDs → distinct container-name
	// pools → cross-instance reap impossible. Computed once during
	// NewWithStore + propagated to containerRuntime via SetInstanceUUID.
	instanceUUID string

	// vault is the token vault used to materialize /run/secrets/
	// for agent containers. Nil → fall back to host ~/.claude/.credentials.json.
	// Set via SetVault after vault.Open succeeds in main.
	vault VaultProvider

	// claudeRefreshMu serializes Claude-OAuth refresh-token POSTs in
	// materializeAgentSecrets. #264 — when N agents spawn concurrently
	// (operator launches a 5-agent team from the wizard), all N call
	// refreshClaudeOAuthIfNeeded with the SAME stale refresh_token.
	// Anthropic's OAuth endpoint invalidates the refresh_token on
	// FIRST use, so calls 2…N get HTTP 401 and the spawned containers
	// inherit a stale accessToken → claude-code OAuth-login UI on
	// boot for 4/5 agents. The lock + re-read pattern ensures only
	// the first racer hits Anthropic; the others read the vault entry
	// that the first racer just wrote back, see it's now fresh, and
	// skip the refresh. One mutex covers all token-ids — the spawn
	// rate is bounded by operator clicks (single-digits per minute)
	// so cross-token contention is irrelevant; per-token mutexes
	// would add complexity for zero observable benefit.
	claudeRefreshMu sync.Mutex

	// extraAgentEnv is appended to every agent's spawn env. Used to
	// inject the operator's MCP bearer token (CHEPHERD_TOKEN) so the
	// agent's bridge subprocess can authenticate (#139).
	extraAgentEnv map[string]string

	// agentRegistry is the first-class Agent entity store (#172). Every
	// successful Spawn mints / re-binds an Agent record here, keyed by
	// stable UUID. Session bookkeeping (attach / detach) flows through
	// the registry so resume + handoff (#173) have a durable identity to
	// reference.
	agentRegistry *agententity.Store

	// sessionToAgent maps live session ID → Agent UUID so DetachSession
	// can find the right record without a registry scan.
	sessionToAgent map[string]uuid.UUID
	extraEnvMu     sync.RWMutex

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
	teams       map[string]*Team       // by name
	memberships map[string]*Membership // by composite key "agent_name|team_name"

	// #404 P0.3 — team-event bus. emitTeamEvent pushes onto teamEvents;
	// teamEventLoop reads + fans out PTY notifications + scheduled
	// briefing regens. regenTimers debounces per-session regen at 1s
	// (cancel + reschedule on burst events). regenMu protects
	// regenTimers separately from r.mu so a long-running regen doesn't
	// block the rest of the runtime.
	teamEvents  chan teamEvent
	regenTimers map[string]*time.Timer
	regenMu     sync.Mutex

	// Per-axis review records (v0.6-C council pattern). Keyed by target
	// agent name; inner map keyed by axis (G|V|F|E|D|custom).
	axisReviews map[string]map[string]*AxisReview

	// Event log — runtime-wide chronological audit (v0.6-F).
	events *eventBuffer

	// shepherd is the worker-observation tier (#208 v0.9.2).
	// Nil-OK: when no shepherd is wired, RecordEvent + Observe paths
	// no-op the broadcast and the Runtime behaves as a v0.9.1-only
	// spawn-and-manage runtime. cmd/run.go in v0.9.2 mode calls
	// rt.WithShepherd(scrummaster.New(cfg)) to enable.
	shepherd scrummaster.ScrumMaster

	// peers is the registry of external A2A peers that have registered
	// themselves as team members via POST /api/v1/peers/register (#669).
	// Always non-nil after NewWithStore. External peers are NOT chepherd-
	// managed sessions (no container, no PTY) — they live in another
	// process / host and expose their own /jsonrpc endpoint to receive
	// A2A messages. teamMembersOf in internal/runtimehttp merges
	// peers.ListByTeam with rt.List() so @everyone fans out to both.
	peers *PeerRegistry

	// sessionsRepo is the SessionRepository handle (#208 v0.9.2).
	// Set by NewWithStore when a persistence.Store is provided; Spawn
	// writes an initial session record so shepherd.discoverSessions
	// (which queries store.Sessions().List) can see runtime-spawned
	// sessions. Pre-#216 the agent registry was wired through but the
	// session repo was not — the shepherd tick loop saw an empty list
	// forever even though sessions existed in the runtime's in-memory
	// map. Nil → file-on-disk fallback (v0.9.1 mode).
	sessionsRepo persistence.SessionRepository
}

// WithShepherd attaches a scrummaster.ScrumMaster to this Runtime so every
// RecordEvent broadcast is also delivered to the shepherd's Observe
// path. Idempotent: re-calling replaces the previously-attached
// ScrumMaster. Returns the Runtime for fluent chaining.
//
// Refs #208.
func (r *Runtime) WithShepherd(s scrummaster.ScrumMaster) *Runtime {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.shepherd = s
	return r
}

// RecordEvent appends an event to the runtime's audit log. Called by
// runtime internals on spawn/exit/scorecard/etc. and by agents via the
// chepherd.record_event MCP tool.
//
// v0.9.2 (#208): when a shepherd is attached via WithShepherd, the
// event is also broadcast to scrummaster.Observe so band/judge/signals
// can react. The shepherd broadcast happens AFTER the local audit
// buffer push so any panic in shepherd code doesn't lose the audit
// trail.
func (r *Runtime) RecordEvent(e Event) {
	if r.events == nil {
		return
	}
	r.events.push(e)
	// v0.9.2 (#208): broadcast to attached shepherd, if any. Nil-OK
	// pattern keeps RecordEvent safe when no shepherd is wired.
	if r.shepherd != nil {
		r.shepherd.Observe(context.Background(), e)
	}
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

// VaultProvider is the interface the runtime needs from a token vault to
// materialize /run/secrets/ for agent containers. Implementation lives in
// internal/vault; broken out as an interface here to avoid an import cycle
// (vault may want to call back into runtime in the future).
type VaultProvider interface {
	ListByProvider(provider string) []VaultCredMeta
	GetValue(id string) (string, error)
	// UpdateValue re-encrypts the stored value for an existing id
	// (preserves provider/label/envVar). Used by the refresh-on-spawn
	// path to persist the rotated OAuth pair back to the vault.
	UpdateValue(id, plaintext string) error
}

// VaultCredMeta is the safe (value-less) view of one credential the vault
// exposes. Mirrors internal/vault.CredMeta to break the import cycle.
type VaultCredMeta struct {
	ID            string
	Provider      string
	ProviderLabel string
	Label         string
	EnvVar        string
}

// SetVault wires the token vault into the runtime so AgentSecretsDir
// pulls Claude OAuth credentials from the vault instead of the host
// filesystem. Safe to call before or after Spawn. Pass nil to detach.
func (r *Runtime) SetVault(v VaultProvider) {
	r.vault = v
}

// SetMCPListenAddr records the host:port the chepherd MCP server is
// bound on. Called from cmd/run.go after StartHTTP succeeds. Used by
// writeMCPConfig (#595) to derive .mcp.json's MCP URL when no env
// override is set — replaces the hardcoded ":9090" fallback so any
// non-default --mcp-listen works correctly out-of-the-box.
func (r *Runtime) SetMCPListenAddr(addr string) {
	r.mcpListenAddr = addr
}

// deriveAgentMCPURL builds the .mcp.json `url` field for spawned
// agents when neither CHEPHERD_MCP_URL nor CHEPHERD_AGENT_MCP_URL is
// set explicitly. Detects topology + uses the actual MCP listen port
// instead of hardcoding ":9090" (#595).
//
// Detection:
//   - HOSTNAME == "chepherd": running inside the canonical 'chepherd'
//     container (per scripts/start.sh + chepherd-net). Agents in
//     chepherd-net resolve "chepherd" by container name → use
//     "ws://chepherd:<port>/mcp/ws".
//   - Otherwise (host-direct, k8s service, any other deploy): use
//     "ws://host.containers.internal:<port>/mcp/ws" so agents
//     (typically slirp4netns or bridge-net) reach the host backend
//     via Podman/Docker's host-loopback DNS.
//
// Port comes from r.mcpListenAddr (set via SetMCPListenAddr after
// MCP boot). Falls back to 9090 only when mcpListenAddr is unset
// (legacy / test-mode pre-SetMCPListenAddr).
func (r *Runtime) deriveAgentMCPURL() string {
	port := mcpPortFromListenAddr(r.mcpListenAddr)
	hostname := os.Getenv("HOSTNAME")
	if hostname == "chepherd" {
		// In-container deploy: agents resolve via chepherd-net DNS.
		return fmt.Sprintf("ws://chepherd:%s/mcp/ws", port)
	}
	// Host-direct (or any non-canonical hostname): route through
	// host-loopback DNS that Podman/Docker exposes to containers.
	return fmt.Sprintf("ws://host.containers.internal:%s/mcp/ws", port)
}

// mcpPortFromListenAddr extracts the port from a host:port string.
// Empty / invalid input falls back to "9090" (the legacy hardcode)
// so tests + pre-SetMCPListenAddr code paths still work.
// net.SplitHostPort handles IPv6 bracketed addresses ([::1]:9090) correctly.
func mcpPortFromListenAddr(addr string) string {
	if addr == "" {
		return "9090"
	}
	if _, port, err := net.SplitHostPort(addr); err == nil && port != "" {
		return port
	}
	return "9090"
}

// SetAgentEnv registers a key=value pair that will be appended to
// every subsequent agent spawn's environment. Used to propagate the
// MCP bearer token from cmd/run.go into the runtime spawn path (#139).
func (r *Runtime) SetAgentEnv(key, value string) {
	r.extraEnvMu.Lock()
	defer r.extraEnvMu.Unlock()
	if r.extraAgentEnv == nil {
		r.extraAgentEnv = map[string]string{}
	}
	r.extraAgentEnv[key] = value
}

// agentEnvOverlay returns the registered key=value strings ready to
// concat onto a spawn's env slice. Snapshot — safe across mutations.
func (r *Runtime) agentEnvOverlay() []string {
	r.extraEnvMu.RLock()
	defer r.extraEnvMu.RUnlock()
	out := make([]string, 0, len(r.extraAgentEnv))
	for k, v := range r.extraAgentEnv {
		out = append(out, k+"="+v)
	}
	return out
}

// agentAuthEnv returns the per-flavor Anthropic-auth env-var slice for
// the given agent slug. Scaffolded as an extension point for future
// flavors (qwen-code, aider, gemini-cli, opencode) whose credentials
// need to land in env vars; today returns nil for every slug.
//
// **claude-code returns nil on purpose** (#227). The claude-code CLI's
// canonical credential source is the per-spawn file mount at
// `/run/secrets/claude-credentials` → linked to
// `~/.claude/.credentials.json` inside the container by the agent
// image's entrypoint. That file carries the full OAuth pair
// {accessToken, refreshToken, expiresAt} and claude-code rotates the
// access_token in-process when it sees the file's expiresAt is past.
//
// PR #221 originally added a `CLAUDE_CODE_OAUTH_TOKEN=<vault.accessToken>`
// env injection here, intending it as a redundant credential channel.
// Operator-reported #227: when that env var is set, claude-code v2.1.153+
// uses it instead of the file path. The env-var value is a STATIC
// snapshot of the access_token at spawn time — no refresh metadata —
// so as soon as the token expires (minutes to hours) the spawned worker
// hits HTTP 401 from the Anthropic API. The file-mount path doesn't
// have this problem because claude-code can read the refreshToken next
// to the access_token and rotate in-place.
//
// Reverted in #227. The file-mount + refresh-on-spawn chain in
// `materializeAgentSecrets` (which already calls
// `refreshClaudeOAuthIfNeeded` to pre-refresh stale snapshots) is the
// sole credential source for claude-code. Operator's pre-PR-#221
// workflow (which has been running fine for hours on the same
// credential) is preserved.
//
// Refs #208 #218 #221 #227 #225 row H1.
func (r *Runtime) agentAuthEnv(slug string) []string {
	if r.vault == nil {
		return nil
	}
	specs, ok := agentAuthEnvTable[slug]
	if !ok {
		return nil
	}
	var out []string
	for _, s := range specs {
		metas := r.vault.ListByProvider(s.vaultProvider)
		if len(metas) == 0 {
			continue
		}
		// Take the last entry — vault stores in insert order and we
		// want the most-recently-added credential for the provider.
		latest := metas[len(metas)-1]
		val, err := r.vault.GetValue(latest.ID)
		if err != nil || val == "" {
			continue
		}
		out = append(out, s.envVar+"="+val)
	}
	return out
}

// agentAuthEnvSpec describes one env var to inject for an agent flavor
// whose CLI reads credentials from process env. Keyed by slug in
// agentAuthEnvTable; multiple entries per slug are allowed when the
// CLI accepts more than one credential channel.
//
// Refs #225 row H1.
type agentAuthEnvSpec struct {
	envVar        string // env var name written into the spawned process
	vaultProvider string // VaultCredMeta.Provider key to look up
}

// agentAuthEnvTable maps agent slug → ordered list of env-var specs.
// **claude-code is deliberately absent** (#227 — file-mount is the
// canonical credential channel; env injection pins a snapshot that
// cannot auto-refresh and 401s on expiry).
//
// Adding a flavor: append a row here AND add a matching test in
// spawn_auth_env_test.go so the per-flavor contract stays explicit.
//
// Refs #225 row H1.
var agentAuthEnvTable = map[string][]agentAuthEnvSpec{
	"qwen-code": {
		// Alibaba DashScope key. qwen-code's CLI also reads
		// OPENAI_API_KEY when configured against an OpenAI-compatible
		// gateway (the bp-newapi path), so both channels are wired.
		{envVar: "DASHSCOPE_API_KEY", vaultProvider: "dashscope-api"},
		{envVar: "OPENAI_API_KEY", vaultProvider: "openai-api"},
	},
	"aider": {
		// aider accepts Anthropic or OpenAI by env. Both wired; the
		// --model flag (DefaultArgs) selects at request time.
		{envVar: "ANTHROPIC_API_KEY", vaultProvider: "anthropic-api"},
		{envVar: "OPENAI_API_KEY", vaultProvider: "openai-api"},
	},
	"gemini-cli": {
		{envVar: "GOOGLE_API_KEY", vaultProvider: "google-api"},
	},
}

// StateDir returns the root state directory for this runtime
// (~/.local/state/chepherd-v0X). Used by HTTP server for workspace
// persistence paths.
func (r *Runtime) StateDir() string {
	return r.stateDir
}

// AgentRegistry exposes the first-class Agent store (#172) so the HTTP
// layer + #173 handoff can query / mutate it without poking the runtime's
// internals.
func (r *Runtime) AgentRegistry() *agententity.Store {
	return r.agentRegistry
}

// Peers exposes the external A2A peer registry (#669). Used by the
// runtimehttp package to register / heartbeat / deregister external
// peers and by team_transcript.go's teamMembersOf to merge external
// peers into the team's member list for fan-out.
//
// Always non-nil after NewWithStore. Returns a pointer so callers see
// live state without taking a snapshot.
func (r *Runtime) Peers() *PeerRegistry {
	return r.peers
}

// RegisteredPeers returns the @-handles of external peers registered
// against the given team (filtered + TTL-swept). Convenience wrapper
// used by teamMembersOf to merge external peers with chepherd-managed
// sessions in one call.
//
// Refs #669.
func (r *Runtime) RegisteredPeers(team string) []PeerInfo {
	if r.peers == nil {
		return nil
	}
	return r.peers.ListByTeam(team)
}

// TeamMembers returns the merged @-handle list of every agent on the
// given team — chepherd-managed sessions (Runtime.List filtered by
// info.Team == team) PLUS externally-registered A2A peers
// (peers.ListByTeam(team)). Used as the single source of truth for
// @everyone fan-out so external peers are first-class team members
// (#669 DoD).
//
// Returns deduplicated names (in case an external peer accidentally
// shares a name with a managed session — managed-session wins to
// preserve PTY-delivery semantics).
func (r *Runtime) TeamMembers(team string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, info := range r.List() {
		if info != nil && info.Team == team {
			if _, dup := seen[info.Name]; dup {
				continue
			}
			seen[info.Name] = struct{}{}
			out = append(out, info.Name)
		}
	}
	for _, p := range r.RegisteredPeers(team) {
		if _, dup := seen[p.Name]; dup {
			continue
		}
		seen[p.Name] = struct{}{}
		out = append(out, p.Name)
	}
	return out
}

// AgentForSession returns the registered Agent that the given live
// session is currently attached to, or nil if the session predates
// the v0.9 registry (legacy v0.8 spawn without UUID).
func (r *Runtime) AgentForSession(sessionID string) (*agententity.Agent, error) {
	r.mu.Lock()
	id, ok := r.sessionToAgent[sessionID]
	r.mu.Unlock()
	if !ok {
		return nil, nil
	}
	return r.agentRegistry.Get(id)
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
	return NewWithStore(stateDir, nil)
}

// NewWithStore is the chepherd v0.9.2 constructor: when store is
// non-nil, the agent registry is opened via the Repository-backed
// wrapper from PR #209 (agent.NewStoreFromRepository) instead of the
// file-on-disk path. When store is nil, falls back to v0.9.1 file-on-
// disk for backward compat. cmd/run.go in v0.9.2 mode passes a
// sqlite-backed persistence.Store; v0.9.1 callers can keep using New.
//
// Refs #208.
func NewWithStore(stateDir string, store persistence.Store) (*Runtime, error) {
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(stateDir, "inbox"), 0o700); err != nil {
		return nil, err
	}
	cr := DetectRuntime()
	// #270 — derive a stable 8-char UUID from the absolute state-dir
	// path so two chepherd binaries with distinct state-dirs spawn
	// distinct container-name pools. SHA256 of the resolved absolute
	// path; first 8 hex chars suffice (collision probability ~1e-19
	// across the realistic count of chepherd binaries on one host).
	instUUID := instanceUUIDFromStateDir(stateDir)
	cr.SetInstanceUUID(instUUID)
	if err := os.MkdirAll(filepath.Join(stateDir, "agents"), 0o700); err != nil {
		return nil, err
	}
	// AgentSpawner is the pluggable strategy that decides how the agent
	// container/Pod is brought up (#127). Today's default is the local
	// container runtime; setting CHEPHERD_SPAWNER=operator switches to
	// the K8s CRD path when bp-chepherd-operator is installed.
	spawner, err := NewAgentSpawner(DefaultSpawnerMode(), cr)
	if err != nil {
		return nil, fmt.Errorf("spawner init: %w", err)
	}
	// #172 + #208: Agent registry is repository-backed when a
	// persistence.Store is provided; file-on-disk otherwise.
	var agentStore *agententity.Store
	if store != nil {
		agentStore = agententity.NewStoreFromRepository(store.Agents())
	} else {
		agentStore, err = agententity.NewStore(stateDir)
		if err != nil {
			return nil, fmt.Errorf("agent registry init: %w", err)
		}
	}
	r := &Runtime{
		sessions:         make(map[string]*session.Session),
		byName:           make(map[string]string),
		info:             make(map[string]*SessionInfo),
		stateDir:         stateDir,
		activity:         make(map[string]*sessionActivity),
		teams:            make(map[string]*Team),
		memberships:      make(map[string]*Membership),
		axisReviews:      make(map[string]map[string]*AxisReview),
		events:           newEventBuffer(1000),
		containerRuntime: cr,
		spawner:          spawner,
		agentRegistry:    agentStore,
		sessionToAgent:   make(map[string]uuid.UUID),
		instanceUUID:     instUUID,
		peers:            NewPeerRegistry(),
	}
	// #216 closes the Spawn ↔ SessionRepository seam left open by
	// PR #211 (runtime migration) + PR #213 (daemon retire). With a
	// store wired, Spawn writes the initial session row so shepherd's
	// discoverSessions can see runtime-spawned sessions on every tick.
	if store != nil {
		r.sessionsRepo = store.Sessions()
	}
	r.cond = sync.NewCond(&r.mu)
	// #404 P0.3 — team-event bus + fan-out goroutine. After this
	// emitTeamEvent is safe to call; pre-this calls are no-ops (the
	// channel is nil, emitTeamEvent's nil-check drops the event).
	r.startTeamEventLoop()
	// Background orphan reaper — every 30s, delete sessionsRepo rows
	// whose id has no matching in-memory session. Catches the cases
	// Stop's explicit Delete misses: container crashes, daemon restart
	// where the agent died, raw `podman kill` from outside chepherd.
	// Boot-time CHEPHERD_CLEANUP_ORPHANS_ON_START handles the daemon-
	// restart case at startup; this catches everything that leaks at
	// runtime between restarts.
	if r.sessionsRepo != nil {
		go r.orphanReaperLoop()
	}
	return r, nil
}

// orphanReaperLoop runs forever (until process exit), scanning
// sessionsRepo for rows whose id is not in r.sessions and deleting
// them. Tick interval = 30s. Safe to run concurrent with Spawn/Stop
// because Spawn writes the in-memory map BEFORE the persistence row,
// so "in repo but not in memory" is always a real orphan.
func (r *Runtime) orphanReaperLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		r.reapOnce(context.Background())
	}
}

// reapOnce performs one pass of the orphan reaper. Extracted so tests
// can drive it deterministically without waiting on the ticker.
func (r *Runtime) reapOnce(ctx context.Context) int {
	if r.sessionsRepo == nil {
		return 0
	}
	ids, err := r.sessionsRepo.List(ctx)
	if err != nil {
		return 0
	}
	r.mu.Lock()
	live := make(map[string]struct{}, len(r.sessions))
	for id := range r.sessions {
		live[id] = struct{}{}
	}
	r.mu.Unlock()
	deleted := 0
	for _, id := range ids {
		if _, alive := live[id]; alive {
			continue
		}
		if err := r.sessionsRepo.Delete(ctx, id); err == nil {
			deleted++
		}
	}
	if deleted > 0 {
		fmt.Fprintf(os.Stderr, "[chepherd-reaper] swept %d orphan session row(s)\n", deleted)
	}
	return deleted
}

// SpawnSpec describes how to bring up a new session.
type SpawnSpec struct {
	Name         string         // canonical @-address; must be unique
	AgentSlug    string         // claude-code | qwen-code | aider | ...
	Team         string         // default "default"
	Role         Role           // default worker
	Cwd          string         // optional working dir
	SystemPrompt string         // optional override for the agent's system prompt
	StatSheet    AgentStatSheet // optional override for the default per-role stat sheet

	// AgentArgs is appended to the agent CLI's default args. Useful for
	// passing --resume <uuid> or similar.
	AgentArgs []string

	// Env adds to the spawned process's environment. nil = inherit only.
	Env []string

	// RingBytes overrides ptyhost.Session default (1 MiB).
	RingBytes int

	// ClaudeTokenID picks which Claude OAuth credential from the vault
	// gets mounted at /run/secrets/claude-credentials. "" = pick the most
	// recently updated claude-oauth credential, or fall back to host
	// ~/.claude/.credentials.json when none exists in the vault. Lets
	// operators run agents under different Claude accounts. (R5, R4.)
	ClaudeTokenID string
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

	// Reserve the name atomically: hold the lock and insert a sentinel so
	// concurrent Spawn calls for the same name are rejected before they do
	// any container-start work. (#647 TOCTOU fix — the old code released
	// the lock between check and registration, allowing concurrent callers
	// to both pass the "taken" test and then race to overwrite byName.)
	const spawnPendingSentinel = "__pending__"
	r.mu.Lock()
	if existing, taken := r.byName[spec.Name]; taken && existing != spawnPendingSentinel {
		r.mu.Unlock()
		return nil, nil, fmt.Errorf("runtime.Spawn: name %q already in use", spec.Name)
	} else if taken {
		r.mu.Unlock()
		return nil, nil, fmt.Errorf("runtime.Spawn: name %q spawn already in progress", spec.Name)
	}
	r.byName[spec.Name] = spawnPendingSentinel
	r.mu.Unlock()
	// If we return early before the real ID is registered, remove sentinel.
	reservationCleared := false
	clearReservation := func() {
		if !reservationCleared {
			r.mu.Lock()
			if r.byName[spec.Name] == spawnPendingSentinel {
				delete(r.byName, spec.Name)
			}
			r.mu.Unlock()
			reservationCleared = true
		}
	}
	defer func() {
		// Only fires on error paths (success path sets reservationCleared=true
		// after writing the real ID).
		clearReservation()
	}()

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
	// Pass model_tier through to the CLI so the operator's pick in
	// AgentSettings → Skills actually changes which model the agent runs on.
	if spec.StatSheet.ModelTier != "" {
		switch spec.AgentSlug {
		case "claude-code":
			extraArgs = append(extraArgs, "--model", spec.StatSheet.ModelTier)
		case "qwen-code":
			extraArgs = append(extraArgs, "--model", spec.StatSheet.ModelTier)
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
	// Append the operator's registered agent-env overlay (CHEPHERD_TOKEN
	// for MCP auth, #139). Inserted before AGENT_NAME so per-spawn
	// overrides win.
	envWithMCP = append(envWithMCP, r.agentEnvOverlay()...)
	// #218: append per-flavor Anthropic-auth env. For claude-code this
	// surfaces vault.claude-oauth.accessToken as CLAUDE_CODE_OAUTH_TOKEN
	// so the spawned worker has a valid token from process-start —
	// independent of the /run/secrets/claude-credentials file mount.
	// Without this, claude-code's start-up auth check could race with
	// the file-mount entrypoint script + the container could idle-exit
	// before any operator interaction lands.
	envWithMCP = append(envWithMCP, r.agentAuthEnv(spec.AgentSlug)...)
	// Tag the child process with its agent name so the MCP bridge can
	// forward it as actor identity on every JSON-RPC call. Without this
	// the server can't tell which shepherd / worker made which call
	// (#89). Read by the bridge in BridgeStdioToHTTP via os.Getenv.
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

	// Resolve per-agent home dir + secrets dir, then wrap argv in container.
	// The secrets dir is bind-mounted at /run/secrets inside the container
	// and the agent image's entrypoint links claude-credentials into place.
	agentHomeDir, err := r.containerRuntime.AgentHomeDir(spec.Name, r.stateDir)
	if err != nil {
		return nil, nil, fmt.Errorf("runtime.Spawn: agent home dir: %w", err)
	}
	agentSecretsDir, err := r.materializeAgentSecrets(spec)
	if err != nil {
		return nil, nil, fmt.Errorf("runtime.Spawn: agent secrets: %w", err)
	}
	// #395 P0 + #396 P0 — write the chepherd briefing (CLAUDE.md +
	// skills/) into the per-agent home dir BEFORE spawner.Spawn. The
	// container's bind-mount of agentHomeDir → /home/agent makes
	// these visible to claude-code on session start. Without this,
	// spawned agents are "vanilla" claude-code — they don't know
	// they're in a chepherd team, can't message peers, answer "who
	// are your siblings" with claude-code's local subagent catalog
	// (Explore, Plan, statusline-setup) instead of the actual peer
	// list. Best-effort: failures log + spawn continues.
	materializeAgentBriefing(spec, agentHomeDir, r.snapshotPeersForBriefing(spec.Team, spec.Name))
	// #172 — mint the Agent UUID BEFORE the spawner runs so its PVC
	// handle can be threaded into the container env. The container
	// runtime sees CHEPHERD_PVC_HANDLE and provisions /workspace from
	// a per-agent named volume. Record persistence happens here too;
	// SessionRef gets appended after we know the session ID below.
	ag := agententity.New(spec.AgentSlug, spec.Name, "")
	if err := r.agentRegistry.Save(ag); err != nil {
		fmt.Fprintf(os.Stderr, "runtime: agent registry save %s: %v\n", spec.Name, err)
	}
	env = append(env, "CHEPHERD_PVC_HANDLE="+ag.PVCHandle)
	// Delegate to the configured AgentSpawner (#127). The local path
	// returns argv ready for ptyhost to exec; future K8s paths return a
	// PodName instead and the ptyhost streams from there.
	fmt.Fprintf(os.Stderr, "[chepherd-spawn-pipeline] %s: spawner.Spawn ENTER (mode=%s argv=%v)\n", spec.Name, r.spawner.Mode(), argv)
	artifact, err := r.spawner.Spawn(context.Background(), SpawnRequest{
		Name:         spec.Name,
		AgentHomeDir: agentHomeDir,
		SecretsDir:   agentSecretsDir,
		Cwd:          spec.Cwd,
		Argv:         argv,
		Env:          env,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "[chepherd-spawn-pipeline] %s: spawner.Spawn FAILED: %v\n", spec.Name, err)
		return nil, nil, fmt.Errorf("runtime.Spawn: %w", err)
	}
	fmt.Fprintf(os.Stderr, "[chepherd-spawn-pipeline] %s: spawner.Spawn OK (LocalArgv=%v)\n", spec.Name, artifact.LocalArgv)
	if artifact.PodName != "" {
		return nil, nil, fmt.Errorf("runtime.Spawn: PodName attach path not yet implemented (set CHEPHERD_SPAWNER=podman-sidecar)")
	}
	spawnArgv, spawnEnv := artifact.LocalArgv, artifact.LocalEnv

	// Spawn the PTY child via ptyhost
	id := newSessionID(spec.Name)
	fmt.Fprintf(os.Stderr, "[chepherd-spawn-pipeline] %s: session.New ENTER (cmd=%v cwd=%s)\n", spec.Name, spawnArgv, spec.Cwd)
	s, err := session.New(id, session.Spec{
		Command:   spawnArgv,
		Env:       spawnEnv,
		Cwd:       spec.Cwd,
		RingBytes: spec.RingBytes,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "[chepherd-spawn-pipeline] %s: session.New FAILED: %v\n", spec.Name, err)
		return nil, nil, fmt.Errorf("runtime.Spawn: ptyhost: %w", err)
	}
	fmt.Fprintf(os.Stderr, "[chepherd-spawn-pipeline] %s: session.New OK (id=%s pid=%d)\n", spec.Name, id, s.PID())

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
		ID:               id,
		Name:             spec.Name,
		AgentSlug:        spec.AgentSlug,
		Team:             spec.Team,
		Role:             spec.Role,
		Cwd:              spec.Cwd,
		CreatedAt:        time.Now().UTC(),
		PID:              s.PID(),
		SystemPrompt:     spec.SystemPrompt,
		StatSheet:        statSheet,
		ContainerRuntime: r.containerRuntime.Name(),
		AgentHomeDir:     agentHomeDir,
	}
	// Extract GitHub URL once at spawn — cheap (single git config read),
	// makes the right-pane "GitHub" link populate immediately. Branch is
	// refreshed in List() since it can change mid-session.
	info.GitHubURL, info.Branch = readGitContext(spec.Cwd)
	if spec.Role == RoleShepherd {
		info.Shepherding = []string{spec.Team}
	}

	act := &sessionActivity{created: time.Now()}

	// #172 — link this session to the Agent record created above
	// (before spawner.Spawn so PVC handle could thread through env).
	if err := r.agentRegistry.AttachSession(ag.ID, id); err != nil {
		fmt.Fprintf(os.Stderr, "runtime: agent attach %s: %v\n", spec.Name, err)
	}
	info.AgentID = ag.ID.String()
	info.PVCHandle = ag.PVCHandle

	r.mu.Lock()
	r.sessions[id] = s
	r.byName[spec.Name] = id // replaces sentinel with real ID
	r.info[id] = info
	r.activity[id] = act
	r.sessionToAgent[id] = ag.ID
	hooks := append([]func(*session.Session, string){}, r.spawnHooks...)
	r.mu.Unlock()
	reservationCleared = true // sentinel replaced; defer no-op from here

	if err := r.persistInfo(info); err != nil {
		// Non-fatal: session is live, just won't survive restart.
		fmt.Fprintf(os.Stderr, "runtime: persist %s failed: %v\n", id, err)
	}
	if err := r.persistInitialSessionState(context.Background(), id, spec, info, ag.ID.String()); err != nil {
		// Non-fatal: session is live, just won't be discovered by shepherd
		// until the next Spawn happens to write through.
		fmt.Fprintf(os.Stderr, "runtime: session repo save %s: %v\n", id, err)
	}
	// Spawn a sniffer goroutine on the PTY output stream. It writes to
	// the activity tracker without ever touching r.mu so it can't deadlock
	// any caller of List/Get.
	go r.runActivitySniffer(s, act, id)
	// #592 — post-spawn container health check. After a 2s grace period
	// (time for OCI runtime to attempt container start), probe whether
	// the container actually entered "running" state. Kernel keyring
	// exhaustion (#592) causes podman run to record the container but
	// the OCI runtime silently fails to start it — the session looks
	// healthy from the spawn pipeline's perspective but the PTY is
	// forever silent.
	go r.postSpawnContainerCheck(spec.Name, 2*time.Second)
	for _, h := range hooks {
		h(s, spec.Name)
	}
	// Event: agent spawned
	r.RecordEvent(Event{
		Kind: "spawn", Actor: "runtime",
		Body: fmt.Sprintf("agent %q spawned (%s, team=%s, role=%s)", spec.Name, spec.AgentSlug, spec.Team, spec.Role),
		Meta: map[string]any{"name": spec.Name, "agent_slug": spec.AgentSlug, "team": spec.Team, "role": string(spec.Role), "cwd": spec.Cwd},
	})
	// #404 P0.3 — emit a team-join event so every other peer in the
	// same team gets a [chepherd team-event] PTY notification + a
	// debounced briefing regen (1s). Without this, alpha (spawned
	// first) never learns about beta (spawned later) — exactly the
	// peer-awareness gap operator filed #404 against.
	r.mu.Lock()
	r.emitTeamEvent(teamEvent{
		Kind:    TeamEventJoin,
		Agent:   spec.Name,
		Team:    spec.Team,
		NewRole: string(spec.Role),
		At:      time.Now().UTC(),
	})
	r.mu.Unlock()
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

// postSpawnContainerCheck waits grace, then probes whether the named
// agent container is actually running. Kernel keyring exhaustion (#592)
// causes the OCI runtime to silently fail to start the container while
// podman run itself exits 0 — the session is "spawned" but the PTY is
// forever silent. Loud stderr + HumanInbox on failure; no-op on BareExec.
func (r *Runtime) postSpawnContainerCheck(name string, grace time.Duration) {
	time.Sleep(grace)
	running, ociErr, err := r.containerRuntime.ProbeContainerRunning(name)
	if err != nil {
		// inspect failure — either the container doesn't exist yet (race)
		// or podman/docker is broken. Emit a warning but don't fire failure.
		fmt.Fprintf(os.Stderr, "[chepherd-probe] %s: inspect error: %v\n", name, err)
		return
	}
	if running {
		return
	}
	msg := fmt.Sprintf("[failure] agent %q container failed to start", name)
	if ociErr != "" {
		msg += ": " + ociErr
	}
	fmt.Fprintf(os.Stderr, "[chepherd-probe] %s\n", msg)
	r.HumanInbox("runtime", msg)
	r.RecordEvent(Event{
		Kind:  "container-start-failure",
		Actor: "runtime",
		Body:  msg,
		Meta:  map[string]any{"name": name, "oci_error": ociErr},
	})
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
		// #363 — capture trailing tail of agent output (8KB cap).
		const tailCap = 8 * 1024
		if snap := sess.RingSnapshot(); len(snap) > 0 {
			if len(snap) > tailCap {
				snap = snap[len(snap)-tailCap:]
			}
			info.LastOutput = string(snap)
		}
	}
	name := info.Name
	code := info.ExitCode
	agentID, hasAgent := r.sessionToAgent[id]
	r.mu.Unlock()

	// #172 — close out the SessionRef on the Agent record so resume
	// can see the previous attach as ended.
	if hasAgent {
		_ = r.agentRegistry.DetachSession(agentID, id)
	}
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

// HumanInbox appends a human-inbox entry.
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

// List returns a snapshot of all session metadata, augmented with the
// activity counters from each session's sniffer.
// snapshotPeersForBriefing returns the current peer list (filtered to
// the given team, excluding self) used by materializeAgentBriefing
// to populate the per-agent CLAUDE.md at spawn time. Best-effort:
// peers spawned AFTER this snapshot won't appear in the static list;
// the agent is expected to use chepherd.list_sessions for live data
// (called out in the CLAUDE.md text). #395.
func (r *Runtime) snapshotPeersForBriefing(team, selfName string) []PeerBrief {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]PeerBrief, 0, len(r.info))
	for _, info := range r.info {
		if info == nil || info.Name == selfName {
			continue
		}
		// Filter to same team when team set; otherwise include all peers.
		if team != "" && info.Team != "" && info.Team != team {
			continue
		}
		out = append(out, PeerBrief{
			Name:      info.Name,
			Role:      string(info.Role),
			AgentSlug: info.AgentSlug,
			Team:      info.Team,
		})
	}
	return out
}

// UpsertSessionInfoForTest inserts or replaces a SessionInfo record
// in the live registry, bypassing the full Spawn path. Intended only
// for tests that need to assert behavior dependent on SessionInfo
// presence (e.g. the v0.9.4 §12.2 directory endpoint, #467) without
// paying the cost of provisioning a real container.
//
// The bare-Server tests in runtimehttp_test.go cannot reach r.info
// directly because it is package-private to runtime. This seam is
// the minimum surface needed to inject SessionInfo from external
// packages. Production code MUST NOT call it.
//
// Refs #467.
func (r *Runtime) UpsertSessionInfoForTest(info *SessionInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.info == nil {
		r.info = map[string]*SessionInfo{}
	}
	if r.byName == nil {
		r.byName = map[string]string{}
	}
	r.info[info.ID] = info
	// Mirror name → ID so JoinTeam (which validates against byName) and
	// other byName-keyed lookups (Get, Pause, Stop) work in tests that
	// inject SessionInfo without going through Spawn. Idempotent.
	if info.Name != "" {
		r.byName[info.Name] = info.ID
	}
}

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
			// Persist into the team's apply.json so resurrect can pick
			// up where this session left off after a runtime restart.
			r.maybeUpdateTeamApply(c.Team, c.Name, extras.ClaudeUUID)
		}
		out = append(out, &c)
	}
	return out
}

// maybeUpdateTeamApply patches the saved team apply record with the
// latest observed claude_uuid for a member. Best-effort: silently
// no-ops if the apply.json doesn't exist (solo spawns, manual joins).
func (r *Runtime) maybeUpdateTeamApply(team, member, uuid string) {
	if team == "" || team == "default" || member == "" || uuid == "" {
		return
	}
	p := filepath.Join(r.stateDir, "teams", team, "apply.json")
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var rec map[string]interface{}
	if err := json.Unmarshal(b, &rec); err != nil {
		return
	}
	members, _ := rec["members"].([]interface{})
	changed := false
	for i, m := range members {
		mm, ok := m.(map[string]interface{})
		if !ok {
			continue
		}
		if mm["name"] == member {
			if mm["claude_uuid"] != uuid {
				mm["claude_uuid"] = uuid
				members[i] = mm
				changed = true
			}
			break
		}
	}
	if !changed {
		return
	}
	rec["members"] = members
	rec["last_active"] = time.Now().UTC().Format(time.RFC3339)
	if nb, err := json.MarshalIndent(rec, "", "  "); err == nil {
		_ = os.WriteFile(p, nb, 0o600)
	}
}

// TeamWorstBand returns the most urgent TrustBand among all non-exited workers
// in the given team(s). If teams is empty, considers all workers in the runtime.
// Used by the shepherd tick loop to set its next interval.
func (r *Runtime) TeamWorstBand(teams []string) TrustBand {
	r.mu.Lock()
	defer r.mu.Unlock()
	teamSet := make(map[string]bool, len(teams))
	for _, t := range teams {
		teamSet[t] = true
	}
	worst := TrustBandTrusted
	urgency := map[TrustBand]int{
		TrustBandTrusted:   0,
		TrustBandStandard:  1,
		TrustBandConcerned: 2,
		TrustBandCrisis:    3,
	}
	for _, info := range r.info {
		if info.Role == RoleShepherd || info.Exited {
			continue
		}
		if len(teamSet) > 0 && !teamSet[info.Team] {
			continue
		}
		band := info.TrustBand
		if band == "" {
			band = TrustBandStandard
		}
		if urgency[band] > urgency[worst] {
			worst = band
		}
	}
	return worst
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
	r.info[id].TrustBand = BandFromScorecard(&sc)
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

// GetByContextID resolves a session against EITHER its byID index OR
// its byName index, in that order. Introduced to fix the A2A
// contextId-vs-name ambiguity surfaced by PR #216's e2e walk:
// /api/v1/sessions returns the full long-form session ID
// ("shepherd-1780057429428571338"); historical chepherd convention is
// the short @-name ("shepherd"). Both are legitimate identifiers and
// A2A's spec gloss for contextId is "stable conversation identifier" —
// callers can reasonably pass either.
//
// Lock-safe (same single-mutex contract as Get). Returns nil/nil when
// neither lookup matches. Used by A2ADeliverer; legacy single-shape
// callers can keep using Get.
//
// Refs #208.
func (r *Runtime) GetByContextID(contextID string) (*session.Session, *SessionInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if info, ok := r.info[contextID]; ok {
		return r.sessions[contextID], info
	}
	if id, ok := r.byName[contextID]; ok {
		return r.sessions[id], r.info[id]
	}
	return nil, nil
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
	_, _ = sess.Inject([]byte(wrapped))
	time.Sleep(120 * time.Millisecond)
	_, _ = sess.Inject([]byte("\r"))
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
//
// #258 — also calls containerRuntime.StopContainer so the sibling
// podman/docker container is actually terminated. Pre-#258 this
// function only closed the PTY; in practice `podman run --rm`'s
// cleanup didn't fire reliably when ptyhost dropped the FD (operator
// counted 19 zombie chepherd-agent-* containers on `podman ps -a`).
// StopContainer is best-effort — a container that's already gone is
// not an error.
func (r *Runtime) Stop(name string) error {
	// #272 — log every Stop entry/exit so silent-Stop bugs are traceable.
	// Walker on #258 (round 2) found zero log lines for a Stop click;
	// either Runtime.Stop's name lookup returned "unknown session" + early-
	// returned (no log line at the StopContainer layer), or some other path
	// fired the container removal. Both cases are now diagnosable from
	// stderr.
	fmt.Fprintf(os.Stderr, "[chepherd-stop] %s: enter Runtime.Stop\n", name)
	r.mu.Lock()
	id, ok := r.byName[name]
	if !ok {
		r.mu.Unlock()
		fmt.Fprintf(os.Stderr, "[chepherd-stop] %s: not in byName registry — returning unknown-session error (no container teardown attempted)\n", name)
		return fmt.Errorf("runtime.Stop: unknown session %q", name)
	}
	s := r.sessions[id]
	// #404 P0.3 — snapshot team + role BEFORE we delete info so we can
	// emit a leave event with the right fields after the mutex
	// unlock. The event must fire AFTER the registry mutation so peer
	// fan-out + briefing regen reflect post-leave state.
	var leaveTeam string
	var leaveRole string
	if info := r.info[id]; info != nil {
		leaveTeam = info.Team
		leaveRole = string(info.Role)
	}
	delete(r.sessions, id)
	delete(r.byName, name)
	delete(r.info, id)
	// Emit BEFORE unlock so the event ordering is deterministic with
	// respect to peers' subsequent rt.Get() calls — they'd see the
	// session already gone.
	r.emitTeamEvent(teamEvent{
		Kind:    TeamEventLeave,
		Agent:   name,
		Team:    leaveTeam,
		OldRole: leaveRole,
		At:      time.Now().UTC(),
	})
	r.mu.Unlock()
	fmt.Fprintf(os.Stderr, "[chepherd-stop] %s: removed from registry (session id %s)\n", name, id)
	if s != nil {
		_ = s.Close()
		fmt.Fprintf(os.Stderr, "[chepherd-stop] %s: PTY closed\n", name)
	}
	_ = os.Remove(filepath.Join(r.stateDir, "sessions", id+".json"))
	// Root cause fix: parallel to claudeLoginCancel (#646) — Stop
	// previously only cleared the in-memory registry + legacy JSON file,
	// leaving the sqlite SessionStore row dangling. The dashboard then
	// surfaced the row as "orphan" forever. Mirror the in-memory
	// teardown into the persistence layer.
	if r.sessionsRepo != nil {
		if err := r.sessionsRepo.Delete(context.Background(), id); err != nil {
			fmt.Fprintf(os.Stderr, "[chepherd-stop] %s: sessionsRepo.Delete %s: %v\n", name, id, err)
		}
	}
	// #258 — kill the sibling container explicitly. PTY close alone
	// doesn't reliably propagate to `podman run --rm`'s cleanup. Run
	// before broadcast so the next List() doesn't include a row whose
	// container is still up (small race avoided).
	if r.containerRuntime != nil {
		fmt.Fprintf(os.Stderr, "[chepherd-stop] %s: calling containerRuntime.StopContainer\n", name)
		if err := r.containerRuntime.StopContainer(name); err != nil {
			fmt.Fprintf(os.Stderr, "[chepherd-stop] %s: container teardown error: %v\n", name, err)
		}
	} else {
		fmt.Fprintf(os.Stderr, "[chepherd-stop] %s: containerRuntime nil — no container teardown (BareExec mode)\n", name)
	}
	r.broadcast()
	fmt.Fprintf(os.Stderr, "[chepherd-stop] %s: exit Runtime.Stop ok\n", name)
	return nil
}

// #273 — expectedAgentEntrypointSHA is set at build time via Makefile's
// -X ldflag to the SHA256 of scripts/agent-entrypoint.sh. At chepherd
// boot, VerifyAgentEntrypointSHA shells out to the chepherd-agent:latest
// image's /usr/local/bin/agent-entrypoint and compares — a mismatch
// means the operator's chepherd binary was rebuilt after a scripts/
// agent-entrypoint.sh change but the chepherd-agent image was NOT, so
// every new spawn uses the stale entrypoint and silently regresses to
// whichever bug the entrypoint change was meant to fix (e.g. #254's
// `! -e` skip-if-exists pinning yesterday's credentials).
//
// Empty when chepherd is built without the -X (e.g. plain `go build`).
// In that case the check is skipped — operator-friendly fallback for
// dev builds.
var expectedAgentEntrypointSHA = ""

// VerifyAgentEntrypointSHA computes the SHA256 of the chepherd-agent:latest
// image's /usr/local/bin/agent-entrypoint and compares it to the
// build-time baked expectedAgentEntrypointSHA. Loud stderr warning +
// rebuild instruction on mismatch. Best-effort: podman-call failures
// (image missing, podman unavailable in dev mode) are silent — we
// don't block boot on diagnostic infrastructure.
//
// Returns true when the SHAs match OR the check was intentionally
// skipped (empty expected, podman unavailable, BareExec). Returns
// false ONLY when a real drift was detected.
func VerifyAgentEntrypointSHA(containerRuntime ContainerRuntime) bool {
	if expectedAgentEntrypointSHA == "" {
		return true // build-skipped (dev `go build` without ldflags)
	}
	if containerRuntime == nil || containerRuntime.Name() == "bare" {
		return true // BareExec doesn't use the agent image
	}
	bin := "podman"
	if containerRuntime.Name() == "docker" {
		bin = "docker"
	}
	cmd := exec.Command(bin, "run", "--rm", "--entrypoint=/bin/sha256sum",
		"chepherd-agent:latest", "/usr/local/bin/agent-entrypoint")
	out, err := cmd.Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[chepherd-image-check] skipped: %v (run `make agent-image` to build the agent image)\n", err)
		return true
	}
	fields := strings.Fields(string(out))
	if len(fields) < 1 {
		return true
	}
	actualSHA := fields[0]
	if actualSHA == expectedAgentEntrypointSHA {
		fmt.Fprintf(os.Stderr, "[chepherd-image-check] chepherd-agent:latest entrypoint SHA matches build (#273)\n")
		return true
	}
	fmt.Fprintf(os.Stderr, "\n[chepherd-image-check] ⚠ STALE chepherd-agent:latest IMAGE DETECTED (#273)\n")
	fmt.Fprintf(os.Stderr, "[chepherd-image-check]   expected agent-entrypoint SHA: %s (this chepherd binary)\n", expectedAgentEntrypointSHA)
	fmt.Fprintf(os.Stderr, "[chepherd-image-check]   actual   agent-entrypoint SHA: %s (in chepherd-agent:latest)\n", actualSHA)
	fmt.Fprintf(os.Stderr, "[chepherd-image-check]   every Spawn from this chepherd uses the STALE entrypoint\n")
	fmt.Fprintf(os.Stderr, "[chepherd-image-check]   → rebuild the agent image:  make agent-image  (or: podman build -f Dockerfile.agent -t chepherd-agent:latest .)\n")
	fmt.Fprintf(os.Stderr, "[chepherd-image-check]   spawned agents will inherit pre-#254 stale credentials behaviour until rebuild lands.\n\n")
	return false
}

// instanceUUIDFromStateDir derives the chepherd instance UUID from the
// absolute state-dir path: SHA256 of the resolved absolute path, first
// 8 hex chars. Stable across reboots (same path → same UUID), unique
// across distinct paths. Used by #270 to namespace container names so
// two chepherd binaries on the same host can't cross-kill each other's
// agents. If filepath.Abs fails (genuinely unusual — symlink loop on
// /tmp etc.), falls back to the raw input string so the function never
// returns empty (an empty UUID would silently re-introduce the pre-#270
// unscoped behaviour).
func instanceUUIDFromStateDir(stateDir string) string {
	abs, err := filepath.Abs(stateDir)
	if err != nil {
		abs = stateDir
	}
	sum := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(sum[:])[:8]
}

// InstanceUUID exposes this chepherd's 8-char instance fingerprint
// for cmd/run.go's boot-banner + #270's verification logging.
func (r *Runtime) InstanceUUID() string { return r.instanceUUID }

// ContainerRuntime exposes the active ContainerRuntime so cmd/run.go's
// startup checks (#273 image-drift verification) can interrogate the
// running image without poking r.containerRuntime directly.
func (r *Runtime) ContainerRuntime() ContainerRuntime { return r.containerRuntime }

// ReapOrphanContainers garbage-collects sibling agent containers that
// aren't tracked by the live runtime — they survived a chepherd crash
// or were spawned by a prior chepherd run. Called once at chepherd
// startup from cmd/run.go. Best-effort: a podman-list failure logs +
// returns nil so chepherd boot doesn't block on container-runtime
// flakiness.
//
// #258 — bundled with the Runtime.Stop fix to clean up the 19 zombies
// the operator already has. New chepherd boot enumerates all
// chepherd-agent-* containers via `podman ps -a` then removes any
// whose agent name isn't in the live registry.
//
// #270 — the listing is now instance-scoped: ListAgentContainers's
// filter only matches `chepherd-agent-<this-instance-uuid>-*`. A
// second chepherd binary on the same host has a different UUID, so
// its agents are invisible here + safe from reap. Pre-#270 containers
// (no UUID infix) are also invisible to the new filter and age out
// naturally via operator-side cleanup.
func (r *Runtime) ReapOrphanContainers() int {
	if r.containerRuntime == nil {
		return 0
	}
	all, err := r.containerRuntime.ListAgentContainers()
	if err != nil {
		fmt.Fprintf(os.Stderr, "runtime.ReapOrphanContainers: list: %v\n", err)
		return 0
	}
	prefix := containerNamePrefix(r.instanceUUID)
	live := map[string]struct{}{}
	r.mu.Lock()
	for name := range r.byName {
		live[prefix+name] = struct{}{}
	}
	r.mu.Unlock()
	reaped := 0
	for _, full := range all {
		if _, ok := live[full]; ok {
			continue
		}
		// Strip the prefix so StopContainer can re-add it. Defensive:
		// skip any list entry that doesn't actually carry our prefix
		// (would happen only if ListAgentContainers's --filter ever
		// loosened — belt-and-braces).
		if len(full) <= len(prefix) || full[:len(prefix)] != prefix {
			continue
		}
		name := full[len(prefix):]
		if err := r.containerRuntime.StopContainer(name); err != nil {
			fmt.Fprintf(os.Stderr, "runtime.ReapOrphanContainers: stop %s: %v\n", full, err)
			continue
		}
		reaped++
	}
	if reaped > 0 {
		// #272 — `[chepherd-stop]` prefix so the reaper's mass-removals
		// show up under the same grep token as Stop-click teardowns.
		// Walker's earlier observation of "9 agents Stop+Remove'd between
		// 21:15:07-21:15:24 with ZERO log lines" was specifically about
		// the reaper-vs-Stop-click ambiguity — now it's traceable to the
		// boot-time reap pass via this log line + the per-container
		// `[chepherd-stop] <name>: PodmanRuntime.StopContainer enter`
		// trail above.
		fmt.Fprintf(os.Stderr, "[chepherd-stop] reaper-pass: removed %d orphan agent container(s) for instance %s (#258 #270 #272)\n", reaped, r.instanceUUID)
	}
	return reaped
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

// Rename changes the @-address of a live session. Cascades through
// byName index, memberships, and any in-flight scorecards / verdicts.
// The underlying PTY process is unaffected.
func (r *Runtime) Rename(oldName, newName string) error {
	if oldName == "" || newName == "" {
		return fmt.Errorf("runtime.Rename: oldName and newName required")
	}
	if oldName == newName {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.byName[oldName]
	if !ok {
		return fmt.Errorf("runtime.Rename: unknown session %q", oldName)
	}
	if _, taken := r.byName[newName]; taken {
		return fmt.Errorf("runtime.Rename: name %q already in use", newName)
	}
	r.info[id].Name = newName
	delete(r.byName, oldName)
	r.byName[newName] = id
	// Cascade through memberships keyed by agent name
	for key, m := range r.memberships {
		if m.AgentName == oldName {
			m.AgentName = newName
			delete(r.memberships, key)
			r.memberships[newName+"|"+m.TeamName] = m
		}
	}
	r.cond.Broadcast()
	r.RecordEvent(Event{
		Kind: "agent_renamed", Actor: "operator",
		Body: fmt.Sprintf("agent %q renamed → %q", oldName, newName),
	})
	return nil
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

// Handoff copies the source agent's active Claude JSONL session to the target
// agent's home directory and spawns the target with --resume <uuid> so it
// picks up the conversation where the source left off.
//
// The source agent is stopped after the copy completes.
// Returns the new target SessionInfo on success.
func (r *Runtime) Handoff(source, target string) (*SessionInfo, error) {
	r.mu.Lock()
	srcID, ok := r.byName[source]
	if !ok {
		r.mu.Unlock()
		return nil, fmt.Errorf("runtime.Handoff: unknown source session %q", source)
	}
	srcInfo := r.info[srcID]
	uuid := srcInfo.ClaudeUUID
	srcHome := srcInfo.AgentHomeDir

	dstID, ok2 := r.byName[target]
	if !ok2 {
		r.mu.Unlock()
		return nil, fmt.Errorf("runtime.Handoff: unknown target session %q", target)
	}
	dstInfo := r.info[dstID]
	dstHome := dstInfo.AgentHomeDir
	dstSpec := SpawnSpec{
		Name:         dstInfo.Name,
		AgentSlug:    dstInfo.AgentSlug,
		Team:         dstInfo.Team,
		Role:         dstInfo.Role,
		Cwd:          dstInfo.Cwd,
		SystemPrompt: dstInfo.SystemPrompt,
		StatSheet:    dstInfo.StatSheet,
	}
	r.mu.Unlock()

	if uuid == "" {
		return nil, fmt.Errorf("runtime.Handoff: source %q has no active Claude session UUID", source)
	}
	if srcHome == "" || dstHome == "" {
		return nil, fmt.Errorf("runtime.Handoff: source or target has no agent home dir (bare exec?)")
	}

	// Copy the JSONL session file from source home to target home.
	srcJSONL := filepath.Join(srcHome, ".claude", "projects", "-"+strings.ReplaceAll(filepath.ToSlash(dstSpec.Cwd), "/", "-"), uuid+".jsonl")
	dstProjectDir := filepath.Join(dstHome, ".claude", "projects", "-"+strings.ReplaceAll(filepath.ToSlash(dstSpec.Cwd), "/", "-"))
	dstJSONL := filepath.Join(dstProjectDir, uuid+".jsonl")

	if err := os.MkdirAll(dstProjectDir, 0o700); err != nil {
		return nil, fmt.Errorf("runtime.Handoff: create target project dir: %w", err)
	}
	data, err := os.ReadFile(srcJSONL)
	if err != nil {
		return nil, fmt.Errorf("runtime.Handoff: read source JSONL %s: %w", srcJSONL, err)
	}
	if err := os.WriteFile(dstJSONL, data, 0o600); err != nil {
		return nil, fmt.Errorf("runtime.Handoff: write target JSONL: %w", err)
	}

	// Stop source, then restart target with --resume.
	_ = r.Stop(source)
	time.Sleep(500 * time.Millisecond)
	_ = r.Stop(target)
	time.Sleep(500 * time.Millisecond)

	dstSpec.AgentArgs = append(dstSpec.AgentArgs, "--resume", uuid)
	newInfo, _, err := r.Spawn(dstSpec)
	if err != nil {
		return nil, fmt.Errorf("runtime.Handoff: spawn target: %w", err)
	}
	r.RecordEvent(Event{
		Kind:  "agent_handoff",
		Actor: "operator",
		Body:  fmt.Sprintf("session handed off from %q to %q (uuid=%s)", source, target, uuid),
		Meta:  map[string]any{"source": source, "target": target, "uuid": uuid},
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
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		return nil, "", err
	}
	// MkdirAll does not change permissions on pre-existing dirs, and WriteFile
	// is subject to the process umask. Chmod explicitly so the container user
	// (different UID in the user namespace) can read both.
	_ = os.Chmod(cfgDir, 0o755)
	// MCP URL the agent's bridge subprocess will dial. v0.8 default:
	// resolve the host's primary outbound IP — rootless Podman's reported
	// bridge gateway (10.88.0.1) is a slirp4netns phantom that isn't
	// actually routed back to host services, so we use the eth0-equivalent
	// IP that the container's NAT reaches via the default route. Override
	// via CHEPHERD_MCP_URL when chepherd is in-cluster (K8s Service DNS
	// becomes ws://chepherd:9090).
	mcpURL := os.Getenv("CHEPHERD_MCP_URL")
	if mcpURL == "" {
		// #595 — auto-detect topology + actual listen port instead of
		// hardcoding `ws://chepherd:9090/mcp/ws`. The hardcode broke
		// every host-direct deploy (no `chepherd` container hostname
		// to resolve) AND every deploy that bound MCP on a non-9090
		// port (the wizard reported success but agents couldn't reach
		// the backend → operator-visible /mcp DISCONNECTED).
		mcpURL = r.deriveAgentMCPURL()
	}
	// Use the absolute path of the currently-running chepherd binary so
	// the MCP-bridge subprocess matches the running runtime regardless
	// of PATH or install name.
	chepBin, _ := os.Executable()
	if chepBin == "" {
		chepBin = "chepherd"
	}
	// Pass CHEPHERD_TOKEN + CHEPHERD_AGENT_NAME explicitly in the env
	// block so claude-code's MCP spawner doesn't drop them. Empty env: {}
	// caused "1 MCP server failed" because the bridge couldn't auth.
	mcpEnv := map[string]string{
		"CHEPHERD_AGENT_NAME": sessionName,
	}
	r.extraEnvMu.RLock()
	if r.extraAgentEnv != nil {
		if tok, ok := r.extraAgentEnv["CHEPHERD_TOKEN"]; ok && tok != "" {
			mcpEnv["CHEPHERD_TOKEN"] = tok
		}
	}
	r.extraEnvMu.RUnlock()
	// #478 Wave M2 — Anthropic MCP Streamable HTTP transport. When
	// CHEPHERD_AGENT_MCP_URL is set (the runner injects this after
	// it binds the TCP loopback listener via AddHTTPListener with
	// the actual bound port), the agent's .mcp.json points the HTTP
	// transport at the local MCP server directly — no stdio bridge
	// subprocess, no WS dial to a remote daemon. claude-code's HTTP
	// transport handles the rest.
	//
	// Back-compat: when CHEPHERD_AGENT_MCP_URL is empty (legacy /
	// chepherd-v05 path / unit-test mode), emit the stdio bridge
	// stanza as before so existing deployments keep working until
	// M3 #479 finishes the cutover. Bearer auth flows via the
	// `Authorization` header on the HTTP transport's request
	// headers, which claude-code's HTTP transport supports natively.
	var cfg map[string]any
	if httpURL := os.Getenv("CHEPHERD_AGENT_MCP_URL"); httpURL != "" {
		entry := map[string]any{
			"type": "http",
			"url":  httpURL,
		}
		if tok := mcpEnv["CHEPHERD_TOKEN"]; tok != "" {
			entry["headers"] = map[string]any{
				"Authorization":    "Bearer " + tok,
				"X-Chepherd-Agent": sessionName,
			}
		}
		cfg = map[string]any{"mcpServers": map[string]any{"chepherd": entry}}
	} else {
		cfg = map[string]any{
			"mcpServers": map[string]any{
				"chepherd": map[string]any{
					"command": chepBin,
					"args":    []string{"mcp", "--url", mcpURL},
					"env":     mcpEnv,
				},
			},
		}
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, "", err
	}
	cfgPath := filepath.Join(cfgDir, ".mcp.json")
	if err := os.WriteFile(cfgPath, b, 0o644); err != nil {
		return nil, "", err
	}
	_ = os.Chmod(cfgPath, 0o644)
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
		"CHEPHERD_MCP_URL=" + mcpURL,
		"CHEPHERD_MCP_CONFIG=" + cfgPath,
	}, cfgPath, nil
}

// materializeAgentSecrets prepares the per-agent secrets directory that
// will be bind-mounted at /run/secrets inside the agent container, and
// returns its host path. Source priority for the Claude credentials:
//
//  1. If spec.ClaudeTokenID is set → pull that exact credential from
//     the token vault.
//  2. Else if the vault has any claude-oauth credentials → pick the most
//     recently updated one (R4: shared default token across all agents).
//  3. Else → fall back to the host's ~/.claude/.credentials.json so a
//     fresh chepherd install on a machine that already has claude-code
//     logged in just works.
//
// The agent image's entrypoint script links the file into
// ~/.claude/.credentials.json on container start (see scripts/agent-entrypoint.sh).
func (r *Runtime) materializeAgentSecrets(spec SpawnSpec) (string, error) {
	// Per-spawn UNIQUE secrets dir. The previous spawn's :U mount option
	// chowns the host-side directory into the container's user namespace
	// (~UID 100999), which means the host chepherd process can no longer
	// write to it. Solution: each spawn gets a fresh timestamped dir
	// under agents/<name>/secrets-*. Old dirs are cleaned up by container
	// teardown via `podman unshare` (see #208 cleanup tracker).
	parent, err := agentSecretsDirPath(spec.Name, r.stateDir)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(parent, fmt.Sprintf("spawn-%d", time.Now().UnixNano()))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, "claude-credentials")

	var payload string
	var src string
	// __none__ is a sentinel set by the OAuth login-capture flow (R5):
	// the ephemeral agent must have NO credentials so claude-code falls
	// into its OAuth prompt and prints the URL we'll capture. Skip both
	// vault and host fallbacks in that case.
	if spec.ClaudeTokenID == "__none__" {
		fmt.Fprintf(os.Stderr, "[chepherd-spawn-secrets] %s: ClaudeTokenID=__none__ — empty creds dir intentional (OAuth-capture)\n", spec.Name)
		return dir, nil
	}
	if r.vault != nil {
		if spec.ClaudeTokenID != "" {
			if v, err := r.vault.GetValue(spec.ClaudeTokenID); err == nil {
				payload = v
				src = "vault:by-token-id:" + spec.ClaudeTokenID
			}
		}
		if payload == "" {
			// Most recently updated claude-oauth entry (list returns
			// creation-order; vault re-orders on Set so most-recent is last).
			creds := r.vault.ListByProvider("claude-oauth")
			if len(creds) > 0 {
				latest := creds[len(creds)-1]
				if v, err := r.vault.GetValue(latest.ID); err == nil {
					payload = v
					src = "vault:fallback-most-recent-claude-oauth:" + latest.ID
				}
			}
		}
	}
	// #369 P0 — compare vault payload vs host's ~/.claude/.credentials.json
	// by expiresAt; pick whichever is fresher. Operators who refresh on
	// the host (interactive claude-code use) would otherwise spawn agents
	// with the 13H-stale vault snapshot → claude 401 → 'Please run /login'
	// → operator can't use the agent. Picking the fresher source preserves
	// the existing v0.5–v0.7 host-fallback semantics AND handles the
	// refreshed-on-host-but-stale-in-vault case the architect surfaced.
	if hostSrc := hostClaudeCredentialsPath(); hostSrc != "" {
		if b, err := os.ReadFile(hostSrc); err == nil {
			hostPayload := string(b)
			hostExp := claudeCredsExpiresAt(hostPayload)
			vaultExp := claudeCredsExpiresAt(payload) // 0 if payload empty
			if hostExp > vaultExp {
				if payload != "" {
					fmt.Fprintf(os.Stderr,
						"[chepherd-spawn-secrets] %s: vault payload stale (expiresAt %d) < host (%d); preferring host (#369 P0)\n",
						spec.Name, vaultExp, hostExp)
				}
				payload = hostPayload
				src = "host-fallback:" + hostSrc
			}
		}
	}
	// #273 — diagnostic stderr logging so the operator's bastion logs
	// (journalctl / docker logs / stderr tail) carry a clear trace of
	// which credential source produced the payload for each spawn. Pre-
	// #273 this was silent — when a spawn produced an empty
	// /run/secrets/claude-credentials, the operator had no way to tell
	// whether (a) vault was nil, (b) the token-id mismatched, (c) the
	// claude-oauth fallback returned 0 entries, or (d) the host fallback
	// path didn't exist. Now the log line says exactly which source won.
	if payload == "" {
		fmt.Fprintf(os.Stderr, "[chepherd-spawn-secrets] %s: NO CREDENTIAL SOURCE produced a payload — vault=%v ClaudeTokenID=%q claude-oauth-fallback=tried host-fallback=%q. Spawn will refuse.\n",
			spec.Name, r.vault != nil, spec.ClaudeTokenID, hostClaudeCredentialsPath())
		return "", fmt.Errorf("materializeAgentSecrets: no Claude credential available for spawn (set claude_token_id in spawn POST OR seed ~/.claude/.credentials.json on the chepherd host)")
	}
	fmt.Fprintf(os.Stderr, "[chepherd-spawn-secrets] %s: payload from %s (%d bytes)\n", spec.Name, src, len(payload))
	if payload != "" {
		// REFRESH-ON-SPAWN — empirically verified 2026-05-28 (operator
		// walk #135). The vault stores a one-shot snapshot of the
		// credentials.json captured at OAuth time. claude-code accessToken
		// has short lifetime (~minutes). If it's already past expiresAt
		// by the time we materialize, claude-code on startup shows the
		// OAuth login screen even though a valid refreshToken sits right
		// there in the file. (claude-code does its background refresh,
		// but the login UI doesn't auto-dismiss once drawn.)
		//
		// Solution: before writing into the agent, exchange the snapshot's
		// refreshToken for a fresh access+refresh pair via Anthropic's
		// OAuth /token endpoint. Persist the new pair back to the vault
		// so the next spawn doesn't re-burn the refresh.
		//
		// #264 — when a team of N agents spawn concurrently (operator
		// hits Launch on a Squad/Scrum template), all N hit this branch
		// at the same time with the SAME stale refresh_token. Anthropic
		// invalidates the refresh_token on FIRST successful POST, so
		// calls 2…N get HTTP 401 + the spawned containers inherit an
		// already-stale accessToken → claude-code OAuth-login UI on
		// boot for (N-1)/N agents. Operator hit 4/5 401s on the scrum
		// team. Fix: serialize the refresh step behind
		// claudeRefreshMu, and re-read the vault INSIDE the critical
		// section so the second-and-later racers pick up the first
		// racer's freshly-written pair (no spurious second POST).
		r.claudeRefreshMu.Lock()
		// Re-read vault inside the lock IFF we previously selected from
		// vault — by the time we got here, another racer may have
		// already refreshed + written back, and we want to pick up that
		// fresher pair instead of double-POSTing the same stale refresh.
		//
		// #374 P0 — if we previously selected from the host (#369 P0
		// path), re-reading vault here would CLOBBER the fresh-host
		// payload with the stale vault snapshot. The architect's
		// 2026-05-30 walk showed: log said "preferring host" but the
		// bytes written to /run/secrets/claude-credentials were the
		// 13H-stale vault payload — operator's claude printed
		// "Please run /login · API Error: 401". Pin host bytes
		// through to the WriteFile below by gating the re-read.
		pickedFromHost := strings.HasPrefix(src, "host-fallback:")
		if !pickedFromHost && r.vault != nil && spec.ClaudeTokenID != "" {
			if v, err := r.vault.GetValue(spec.ClaudeTokenID); err == nil && v != "" {
				payload = v
			}
		}
		if refreshed, ok := refreshClaudeOAuthIfNeeded(payload); ok {
			payload = refreshed
			if r.vault != nil && spec.ClaudeTokenID != "" {
				_ = r.vault.UpdateValue(spec.ClaudeTokenID, refreshed)
			}
		}
		r.claudeRefreshMu.Unlock()
		if err := os.WriteFile(dst, []byte(payload), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "[chepherd-spawn-secrets] %s: WRITE FAILED to %s: %v\n", spec.Name, dst, err)
			return "", err
		}
		fmt.Fprintf(os.Stderr, "[chepherd-spawn-secrets] %s: wrote %d-byte credentials.json to %s\n", spec.Name, len(payload), dst)
		// Also write the onboarding stub claude-code v2.1.150 requires
		// in ~/.claude.json. Without hasCompletedOnboarding: true +
		// userID + oauthAccount, claude-code re-runs the welcome /
		// login-method flow even with a valid credentials.json — that
		// was the "4× Enter" bug operators kept hitting.
		onboarding := buildClaudeOnboardingStub()
		if onboarding != "" {
			_ = os.WriteFile(filepath.Join(dir, "claude-onboarding"), []byte(onboarding), 0o644)
		}
	}
	// Best-effort GC: nuke prior per-spawn dirs whose contents are now
	// owned by a container-namespace UID. `podman unshare` enters the
	// rootless user-namespace where the container-UID maps to UID 0.
	go cleanupStaleSecretsDirs(parent, dir)
	return dir, nil
}

// buildClaudeOnboardingStub returns the bytes that should go to
// /run/secrets/claude-onboarding (→ ~/.claude.json inside the agent
// container). Sourced from the operator's host ~/.claude.json, filtered
// to just the identity + first-run-suppression keys claude-code needs:
//
//	hasCompletedOnboarding, userID, oauthAccount, firstStartTime,
//	changelogLastFetched, migrationVersion, opusProMigrationComplete,
//	sonnet1m45MigrationComplete, tipsHistory, theme
//
// Everything else (project-history, per-cwd state, MCP server registry
// for the host machine, etc.) is NOT propagated — it'd leak operator
// project state into every agent and bloat the file.
//
// Returns "" if no host file exists; callers skip writing the stub and
// the agent runs through the onboarding flow as before (only useful for
// the very-first OAuth-capture, which is acceptable).
func buildClaudeOnboardingStub() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		return ""
	}
	var host map[string]any
	if err := json.Unmarshal(raw, &host); err != nil {
		return ""
	}
	allow := []string{
		"hasCompletedOnboarding",
		"userID",
		"oauthAccount",
		"firstStartTime",
		"changelogLastFetched",
		"migrationVersion",
		"opusProMigrationComplete",
		"sonnet1m45MigrationComplete",
		"tipsHistory",
		"theme",
		"installMethod",
		"isQualifiedForDataSharing",
		"autoPermissionsNotificationCount", // suppresses Bypass-permissions warning prompt
		"lastOnboardingVersion",
		"numStartups",
		"claudeCodeFirstTokenDate",
		"opus47LaunchSeenCount",
		"remoteControlUpsellSeenCount",
		"remoteDialogSeen",
		"officialMarketplaceAutoInstallAttempted",
		"officialMarketplaceAutoInstalled",
	}
	stub := map[string]any{}
	for _, k := range allow {
		if v, ok := host[k]; ok {
			stub[k] = v
		}
	}
	// Sane defaults if host lacked anything load-bearing.
	if _, ok := stub["hasCompletedOnboarding"]; !ok {
		stub["hasCompletedOnboarding"] = true
	}
	b, err := json.Marshal(stub)
	if err != nil {
		return ""
	}
	return string(b)
}

// cleanupStaleSecretsDirs removes spawn-* subdirectories of parent
// except the one we just wrote. Uses `podman unshare rm -rf` so files
// chowned into the container's user namespace by a previous :U mount
// are reachable. Failures are swallowed — this is best-effort cleanup.
func cleanupStaleSecretsDirs(parent, keep string) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "spawn-") {
			continue
		}
		full := filepath.Join(parent, e.Name())
		if full == keep {
			continue
		}
		// Try host first; if EACCES, fall back to podman unshare.
		if err := os.RemoveAll(full); err == nil {
			continue
		}
		_ = exec.Command("podman", "unshare", "rm", "-rf", full).Run()
	}
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
	// #404 P0.3 — detect role-change vs first-join so the event carries
	// the right kind. Without this, JoinTeam-as-role-change would emit a
	// "join" event + re-paste the welcome notification on every role
	// update.
	var evKind TeamEventKind
	var oldRole MembershipRole
	if existing, ok := r.memberships[key]; ok {
		if existing.Role == role {
			// No-op update — still return the existing membership but
			// don't emit a noisy event.
			r.cond.Broadcast()
			return existing, nil
		}
		evKind = TeamEventRoleChange
		oldRole = existing.Role
	} else {
		evKind = TeamEventJoin
	}
	m := &Membership{
		AgentName:     agentName,
		TeamName:      teamName,
		Role:          role,
		BriefOverride: briefOverride,
		JoinedAt:      time.Now().UTC(),
	}
	r.memberships[key] = m
	r.emitTeamEvent(teamEvent{
		Kind:    evKind,
		Agent:   agentName,
		Team:    teamName,
		OldRole: string(oldRole),
		NewRole: string(role),
		At:      time.Now().UTC(),
	})
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
	existing, ok := r.memberships[key]
	if !ok {
		return false
	}
	delete(r.memberships, key)
	r.emitTeamEvent(teamEvent{
		Kind:    TeamEventLeave,
		Agent:   agentName,
		Team:    teamName,
		OldRole: string(existing.Role),
		At:      time.Now().UTC(),
	})
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

// claudeOAuthClientID is the public client_id claude-code uses in its
// PKCE OAuth flow against Anthropic's IdP. Surfaced in every login URL
// claude-code prints (operator confirmed by inspecting the OAuth URL
// in their PTY). Stable across operators — there is no per-installation
// secret here.
const claudeOAuthClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"

// claudeOAuthTokenEndpoint is Anthropic's OAuth /token endpoint that
// accepts grant_type=refresh_token. Same one claude-code itself uses
// for its background refresh. Confirmed reachable from agent containers
// (HTTP 200 on /; HTTP 400 on POST {} = endpoint exists, validates body).
//
// `claudeOAuthTokenEndpointOverride` is a test-seam: when non-empty,
// refreshClaudeOAuthIfNeeded POSTs there instead of the canonical URL.
// Production code never sets it; the #264 concurrency regression test
// points it at an httptest.Server that COUNTS requests so we can
// assert claudeRefreshMu serialised the N racers down to 1 POST.
const claudeOAuthTokenEndpoint = "https://console.anthropic.com/v1/oauth/token"

var claudeOAuthTokenEndpointOverride string


// claudeCredsExpiresAt extracts the expiresAt epoch-ms from a
// credentials.json payload. Returns 0 on any parse failure (caller
// treats as 'unknown / oldest possible' → host source preferred).
// Refs #369 P0.
func claudeCredsExpiresAt(payload string) int64 {
	var doc struct {
		ClaudeAiOauth struct {
			ExpiresAt int64 `json:"expiresAt"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal([]byte(payload), &doc); err != nil {
		return 0
	}
	return doc.ClaudeAiOauth.ExpiresAt
}

// refreshClaudeOAuthIfNeeded inspects a credentials.json payload (the
// JSON shape claude-code writes to ~/.claude/.credentials.json). If
// the accessToken inside is already expired OR within 60s of expiring,
// posts the refreshToken to Anthropic's OAuth endpoint, splices the
// fresh access+refresh pair back into the same JSON shape, and returns
// it. Returns (input, false) on any error or if the token is still
// comfortably valid — caller falls back to the unrefreshed payload.
//
// This is the permanent fix for the "agent shows OAuth login screen
// even though credentials are present" bug (operator walk 2026-05-28,
// experimentally verified — see commit log).
//
// Note: this function is intentionally NOT goroutine-safe by itself —
// the caller (`Runtime.materializeAgentSecrets`) serialises it via
// `claudeRefreshMu` to prevent the #264 refresh-token race where N
// concurrent spawns each POST the same refresh_token and Anthropic
// 401s all but the first.
func refreshClaudeOAuthIfNeeded(payload string) (string, bool) {
	var doc struct {
		ClaudeAiOauth struct {
			AccessToken      string   `json:"accessToken"`
			RefreshToken     string   `json:"refreshToken"`
			ExpiresAt        int64    `json:"expiresAt"`
			SubscriptionType string   `json:"subscriptionType,omitempty"`
			Scopes           []string `json:"scopes,omitempty"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal([]byte(payload), &doc); err != nil {
		return payload, false
	}
	if doc.ClaudeAiOauth.RefreshToken == "" {
		return payload, false
	}
	// Skip if comfortably valid (>60s of life left).
	const safetyMargin = 60 * 1000 // 60 seconds in ms
	nowMs := time.Now().UnixMilli()
	if doc.ClaudeAiOauth.ExpiresAt > nowMs+safetyMargin {
		return payload, false
	}

	body := map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": doc.ClaudeAiOauth.RefreshToken,
		"client_id":     claudeOAuthClientID,
	}
	bodyBytes, _ := json.Marshal(body)
	endpoint := claudeOAuthTokenEndpoint
	if claudeOAuthTokenEndpointOverride != "" {
		endpoint = claudeOAuthTokenEndpointOverride
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return payload, false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return payload, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return payload, false
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	if err != nil {
		return payload, false
	}
	var tokRes struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"` // seconds
	}
	if err := json.Unmarshal(raw, &tokRes); err != nil {
		return payload, false
	}
	if tokRes.AccessToken == "" {
		return payload, false
	}

	// Splice the refreshed pair back into the credentials.json shape.
	doc.ClaudeAiOauth.AccessToken = tokRes.AccessToken
	if tokRes.RefreshToken != "" {
		doc.ClaudeAiOauth.RefreshToken = tokRes.RefreshToken
	}
	if tokRes.ExpiresIn > 0 {
		doc.ClaudeAiOauth.ExpiresAt = time.Now().Add(time.Duration(tokRes.ExpiresIn) * time.Second).UnixMilli()
	}

	out, err := json.Marshal(doc)
	if err != nil {
		return payload, false
	}
	return string(out), true
}

// persistInitialSessionState writes the initial session row into the
// SessionRepository (#216). Closes the seam between Runtime.Spawn and
// store.Sessions() left open by PR #211 (runtime migration) + PR #213
// (daemon retire): pre-#216 the shepherd's discoverSessions queried
// store.Sessions().List which was permanently empty because nothing
// in the runtime ever wrote to it. The state map omits next_tick_at
// on purpose — shepherd treats a missing next_tick_at as "due now"
// (cf. tickOnce's adaptive-cadence skip in internal/shepherd/shepherd.go),
// so the first tick after Spawn fires immediately and the worker is
// observed within one tick interval. No-op when r.sessionsRepo is nil
// (v0.9.1 file-on-disk mode).
//
// Refs #208.
func (r *Runtime) persistInitialSessionState(ctx context.Context, sessionID string, spec SpawnSpec, info *SessionInfo, agentID string) error {
	if r.sessionsRepo == nil {
		return nil
	}
	state := map[string]any{
		"agent_id":   agentID,
		"name":       spec.Name,
		"role":       string(spec.Role),
		"team":       spec.Team,
		"created_at": info.CreatedAt.Format(time.RFC3339),
	}
	return r.sessionsRepo.Save(ctx, sessionID, state)
}
