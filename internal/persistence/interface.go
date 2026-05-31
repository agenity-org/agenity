// Package persistence is the chepherd v0.9.2 storage abstraction layer.
//
// All persistable state goes through Repository interfaces defined here;
// concrete backends (SQLite, PostgreSQL) implement them in the sqlite/
// and postgres/ subpackages. Backend selection is config-driven via
// the CHEPHERD_DB_DRIVER env var (sqlite is the single-instance default;
// postgres is the HA backend).
//
// This package owns the canonical schema (in migrate/*.sql) and the
// interface shape. Existing v0.9.1 domain packages (skills, agent, canon,
// keychain, templateregistry, auth, runtime/events) wrap these
// Repositories with their existing NewStore(...) constructors so v0.9.1
// consumer code paths stay unchanged.
//
// Doc inventory mapping (per docs/V0.9.2-ARCHITECTURE.md §5):
//
//	#6  Session registry         → SessionRepository + AgentCardRepository
//	#7  RBAC / policy store      → RBACGrantRepository
//	#8  Audit log store          → EventRepository (extends v0.9.1 with A2A fields)
//	#10 Short-lived JWT secret   → AuthSecretRepository (extends v0.9.1 dashboard HS256 with new ES256 key)
//	#54 Operator Account         → AccountRepository (operator identity + LLM credential ref bindings)
//
// Plus 8 entity Repositories migrating existing v0.9.1 on-disk JSON
// (Session, Skill, Agent, Canon, Keychain, Template, AuthSecret, Event).
// Refs #208.
package persistence

import (
	"context"
	"time"
)

// Store is the top-level connection holder. Concrete backends expose
// per-Repository handles from a single underlying *sql.DB connection
// pool. Backends MUST be safe for concurrent use across all Repositories.
type Store interface {
	Sessions() SessionRepository
	Skills() SkillRepository
	Agents() AgentRepository
	Canon() CanonRepository
	Keychain() KeychainRepository
	Templates() TemplateRepository
	AuthSecrets() AuthSecretRepository
	Events() EventRepository
	Grants() RBACGrantRepository
	AuditEvents() AuditEventRepository
	Tasks() TaskRepository
	Artifacts() ArtifactRepository
	PushConfigs() PushNotificationConfigRepository
	AgentCards() AgentCardRepository
	Accounts() AccountRepository

	// Close releases the underlying connection pool.
	Close() error
}

// ─── 1. SessionRepository ────────────────────────────────────────
// Migrates from ~/.local/state/chepherd/sessions/<uuid>.json
// (internal/daemon/state.go LoadState / SaveState).

type SessionRepository interface {
	Get(ctx context.Context, sessionID string) (map[string]any, error)
	Save(ctx context.Context, sessionID string, state map[string]any) error
	Delete(ctx context.Context, sessionID string) error
	List(ctx context.Context) ([]string, error)

	// ResumableSessions returns sessions that have a non-empty
	// claude_session_uuid + are NOT marked exited in their state. Used
	// by runtime.NewWithStore for the #350 D4 boot-time auto-resume
	// scan: each returned ResumableSession is spawned with
	// --resume <ClaudeSessionUUID>.
	ResumableSessions(ctx context.Context) ([]ResumableSession, error)
}

// ResumableSession is the minimal payload runtime needs to re-spawn
// a persisted session after a chepherd restart (#350 D4).
type ResumableSession struct {
	SessionID         string
	Name              string // @-handle for the spawn's user-facing name
	AgentSlug         string // "claude-code" | "qwen-code" | ...
	Team              string
	Cwd               string
	ClaudeSessionUUID string // value passed to the agent's --resume flag
}

// ─── 2. SkillRepository ───────────────────────────────────────────
// Migrates from $stateDir/skills-registry/{id}.override.json.

type SkillRepository interface {
	Get(ctx context.Context, id string) (*Skill, error)
	List(ctx context.Context, opts SkillListOpts) ([]Skill, error)
	Save(ctx context.Context, skill *Skill) error
	Delete(ctx context.Context, id string) error
}

type Skill struct {
	ID           string
	Name         string
	DefaultBody  string
	OverrideBody string // empty when no operator override
	ReadOnly     bool
	Source       string // upstream source URL
	Path         string // upstream path
	SortOrder    int
	Metadata     []byte // JSON-encoded domain-specific fields (Frontmatter,
	// DefaultTools, AgentTypeCompat, StatSheet, Tags, TeamOnly, etc.) that
	// don't have dedicated columns. Empty/nil → no extras.
	UpdatedAt time.Time
}

type SkillListOpts struct {
	IncludeOverridden bool
	Source            string
}

// ─── 3. AgentRepository ───────────────────────────────────────────
// Migrates from $stateDir/agents-registry/{id}.json.

type AgentRepository interface {
	Get(ctx context.Context, id string) (*Agent, error)
	List(ctx context.Context) ([]*Agent, error)
	Save(ctx context.Context, agent *Agent) error
	Delete(ctx context.Context, id string) error
}

type Agent struct {
	ID               string // UUID
	Type             string // agent_type slug (claude-code, codex, ...)
	Label            string
	RoleID           string
	CreatorAccount   string
	OwnedSkills      []string
	OwnedSkillsScope map[string]string
	Sessions         []SessionRef
	Metadata         []byte // JSON-encoded extras (status sheet, tags, etc.)
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type SessionRef struct {
	SessionID  string
	AttachedAt time.Time
}

// ─── 4. CanonRepository ───────────────────────────────────────────
// Migrates from $stateDir/canon/{current.json,history/*.json}.

type CanonRepository interface {
	Get(ctx context.Context) (*Canon, error)
	Save(ctx context.Context, body, updatedBy, title string) (*Canon, error)
	History(ctx context.Context, limit int) ([]*Canon, error)
	Rollback(ctx context.Context, toVersion int, actor string) (*Canon, error)
}

type Canon struct {
	Version   int
	Body      string
	UpdatedBy string
	Title     string
	UpdatedAt time.Time
}

// ─── 5. KeychainRepository ────────────────────────────────────────
// Adds a SQLite-backed backend alongside the existing macos/linux/file
// backends in internal/keychain/keychain.go (multi-backend pattern
// preserved; this is one of several Backend implementations).

type KeychainRepository interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string) error
	Delete(ctx context.Context, key string) error
}

// ─── 6. TemplateRepository ────────────────────────────────────────
// Migrates from $stateDir/templates-registry/{id}.yaml.

type TemplateRepository interface {
	Get(ctx context.Context, id string) (*Template, error)
	List(ctx context.Context) ([]*Template, error)
	Save(ctx context.Context, template *Template) error
	Delete(ctx context.Context, id string) error
}

type Template struct {
	ID          string
	Name        string
	Description string
	Body        []byte // canonical YAML / encoded body
	Metadata    []byte // JSON-encoded extras (Icon, WhenToUse, Slots, etc.)
	UpdatedAt   time.Time
}

// ─── 7. AuthSecretRepository ──────────────────────────────────────
// Migrates from $stateDir/auth.secret + adds A2A ES256 private key.
// HS256 dashboard secret and ES256 A2A signing key are stored in
// separate rows keyed by purpose.

type AuthSecretRepository interface {
	Get(ctx context.Context, purpose string) (*AuthSecret, error)
	Save(ctx context.Context, purpose string, key []byte, alg string) error
}

type AuthSecret struct {
	Purpose   string // "dashboard-hs256" | "a2a-es256-priv"
	Key       []byte // raw key bytes (PEM-encoded for ES256, opaque for HS256)
	Algorithm string // "HS256" | "ES256"
	CreatedAt time.Time
}

// ─── 8. EventRepository ───────────────────────────────────────────
// Persistence for the runtime-wide audit log (#8). Extends v0.9.1's
// internal/runtime events ring buffer with v0.9.2 A2A fields
// (Method / CallerOrg / CallerSID) so cross-org audit summaries
// can be derived from a single table.

type EventRepository interface {
	Append(ctx context.Context, event Event) error
	List(ctx context.Context, opts EventListOpts) ([]Event, error)
}

type Event struct {
	ID        string
	Kind      string
	Actor     string
	Body      string
	Timestamp time.Time

	// v0.9.2 A2A fields (empty for non-A2A events).
	A2AMethod string // which A2A method (SendMessage / GetTask / etc.)
	CallerOrg string // cross-org caller's org identifier
	CallerSID string // cross-org caller's agent SID
}

type EventListOpts struct {
	Limit int
	Since time.Time
	Kinds []string
}

// ─── 9. RBACGrantRepository ───────────────────────────────────────
// NEW in v0.9.2 (#7). Per-pair peering grants for cross-org A2A calls.

type RBACGrantRepository interface {
	Get(ctx context.Context, id string) (*Grant, error)
	List(ctx context.Context, opts GrantListOpts) ([]*Grant, error)
	Save(ctx context.Context, grant *Grant) error
	Delete(ctx context.Context, id string) error
}

type Grant struct {
	ID          string
	GranterOrg  string
	GranteeOrg  string
	Scope       GrantScope
	Permissions []string
	RateLimit   *GrantRateLimit
	ExpiresAt   *time.Time
	Accepted    bool // false until grantee accepts
	CreatedBy   string
	CreatedAt   time.Time
}

type GrantScope struct {
	Type        string // "workspace" | "team" | "agent"
	WorkspaceID string
	TeamID      string
	AgentSID    string
}

type GrantRateLimit struct {
	CallsPerMinute int
	CallsPerDay    int
}

type GrantListOpts struct {
	GranterOrg string
	GranteeOrg string
	OnlyActive bool
}

// ─── 9b. AuditEventRepository ─────────────────────────────────────
// NEW in v0.9.4 (#489 Wave AU2). Persists §10-step-24 audit events
// streamed up from runners over the register WS. Per-org partitioned;
// queries are org-scoped via OrgID filter.

type AuditEventRepository interface {
	Save(ctx context.Context, ev *AuditEventRecord) error
	List(ctx context.Context, opts AuditEventListOpts) ([]*AuditEventRecord, error)
}

// AuditEventRecord is the persisted form of runtime.AuditEvent + the
// receiver-daemon's org_id stamped at ingest. RawJSON preserves the
// wire-shape for forward-compat with additive event fields (AU3 +
// future dashboard consumers).
type AuditEventRecord struct {
	ID         string
	OrgID      string
	EventType  string // audit.sent | audit.received
	Timestamp  time.Time
	Caller     string
	Callee     string
	Method     string
	LatencyMS  int64
	JTI        string
	Status     string // success | error
	Error      string
	TaskID     string
	RawJSON    []byte
}

// AuditEventListOpts filters the audit-events query. All fields
// optional; empty = no filter on that dimension. OrgID is the
// canonical privacy boundary — caller MUST set it to the request's
// org context.
type AuditEventListOpts struct {
	OrgID   string
	Caller  string
	Callee  string
	Method  string
	Since   *time.Time
	Until   *time.Time
	Limit   int // default 100, max 1000
}

// ─── 10. TaskRepository ───────────────────────────────────────────
// NEW in v0.9.2. Persists A2A task state machines (one record per
// in-flight or completed A2A task on this runner).

type TaskRepository interface {
	Get(ctx context.Context, taskID string) (*Task, error)
	Save(ctx context.Context, task *Task) error
	List(ctx context.Context, opts TaskListOpts) ([]*Task, error)
	Delete(ctx context.Context, taskID string) error
}

type Task struct {
	ID            string
	RunnerSID     string
	State         string // A2A state enum (SUBMITTED | WORKING | COMPLETED | etc.)
	Method        string // A2A method that created it
	InputBlob     []byte // serialized A2A Message
	OutputBlob    []byte // serialized A2A Artifact / result
	AuthChallenge string // populated when state = AUTH_REQUIRED
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type TaskListOpts struct {
	RunnerSID string
	State     string
	SinceID   string // cursor
	Limit     int
}

// ─── 11. PushNotificationConfigRepository ─────────────────────────
// NEW in v0.9.2. Persists webhook configs registered via the 4 A2A
// push-notification-config methods (Create / Get / List / Delete).

type PushNotificationConfigRepository interface {
	Get(ctx context.Context, id string) (*PushNotificationConfig, error)
	List(ctx context.Context, taskID string) ([]*PushNotificationConfig, error)
	Save(ctx context.Context, config *PushNotificationConfig) error
	Delete(ctx context.Context, id string) error
}

type PushNotificationConfig struct {
	ID         string
	TaskID     string
	URL        string
	SigningKey []byte // HMAC signing secret for outbound webhook
	Filters    []string
	CreatedAt  time.Time
}

// ─── 11b. ArtifactRepository ──────────────────────────────────────
// NEW in v0.9.3 #225 row H3. Persists A2A Artifacts emitted by Tasks
// (one Task → zero-or-more Artifacts). Distinct from Task.OutputBlob
// which is a serialized SendMessageResult snapshot; Artifacts are
// individually addressable, FK'd to the Task (CASCADE on Task delete),
// and survive a TaskRepository.Save that updates output_blob without
// replaying the artifact stream.

type ArtifactRepository interface {
	Get(ctx context.Context, artifactID string) (*Artifact, error)
	List(ctx context.Context, taskID string) ([]*Artifact, error)
	Save(ctx context.Context, artifact *Artifact) error
	Delete(ctx context.Context, artifactID string) error
}

type Artifact struct {
	ID        string    // A2A artifactId (UUIDv7 by convention)
	TaskID    string    // FK → Task.ID
	Name      string    // optional human-readable name
	Parts     []byte    // serialized []a2a.Part JSON
	Metadata  []byte    // serialized map[string]any caller metadata
	CreatedAt time.Time
}

// ─── 12. AgentCardRepository ──────────────────────────────────────
// NEW in v0.9.2. Caches Agent Cards for fast discovery.
// Canonical Cards are served by individual runners at their
// .well-known URI; this caches them for the daemon's directory plus
// chepherd.org sync for public agents.

type AgentCardRepository interface {
	Get(ctx context.Context, agentSID string) (*AgentCard, error)
	Save(ctx context.Context, card *AgentCard) error
	List(ctx context.Context, opts AgentCardListOpts) ([]*AgentCard, error)
	Delete(ctx context.Context, agentSID string) error
}

type AgentCard struct {
	SID              string
	Name             string
	Body             []byte // canonical JSON (signed for public agents)
	PublicVisibility bool   // true → pushed to chepherd.org directory
	SyncedAt         time.Time
	UpdatedAt        time.Time
}

type AgentCardListOpts struct {
	PublicOnly bool
	Capability string
	Tag        string
}

// ─── 13. AccountRepository ────────────────────────────────────────
// NEW in v0.9.2 (#54). Persists operator account identity + LLM
// provider credential bindings. Distinct from #51 LLM credential
// vault (iogrid-side platform-held keys); #54 is operator-side
// identity (OIDC subject, email, org, role, claude-token reference,
// billing customer id). Different from KeychainRepository which
// holds the raw secret bytes — Account binds an operator-named
// identity to a keychain key reference.

type AccountRepository interface {
	Get(ctx context.Context, id string) (*Account, error)
	List(ctx context.Context) ([]*Account, error)
	Save(ctx context.Context, account *Account) error
	Delete(ctx context.Context, id string) error
}

type Account struct {
	ID          string
	Class       string // "anthropic" | "openai" | "google" | ...
	Label       string
	KeychainKey string // ref into KeychainRepository for the actual secret
	Email       string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
