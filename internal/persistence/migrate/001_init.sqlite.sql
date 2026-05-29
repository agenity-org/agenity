-- chepherd v0.9.2 initial persistence schema (SQLite dialect).
-- See internal/persistence/interface.go for the canonical Repository
-- contracts. Tables 1-7 hold migrated v0.9.1 entities; tables 8-13
-- back NEW v0.9.2 components per docs/V0.9.2-ARCHITECTURE.md inventory.
-- Refs #208.

-- ─── 1. sessions (SessionRepository) ────────────────────────────
CREATE TABLE sessions (
    session_id TEXT PRIMARY KEY,
    state_json TEXT NOT NULL,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- ─── 2. skills (SkillRepository) ────────────────────────────────
CREATE TABLE skills (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    default_body TEXT NOT NULL DEFAULT '',
    override_body TEXT NOT NULL DEFAULT '',
    read_only INTEGER NOT NULL DEFAULT 0,
    source TEXT NOT NULL DEFAULT '',
    path TEXT NOT NULL DEFAULT '',
    sort_order INTEGER NOT NULL DEFAULT 0,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- ─── 3. agents (AgentRepository) ────────────────────────────────
CREATE TABLE agents (
    id TEXT PRIMARY KEY,
    type TEXT NOT NULL,
    label TEXT NOT NULL DEFAULT '',
    role_id TEXT NOT NULL DEFAULT '',
    creator_account TEXT NOT NULL DEFAULT '',
    owned_skills_json TEXT NOT NULL DEFAULT '[]',
    owned_skills_scope_json TEXT NOT NULL DEFAULT '{}',
    sessions_json TEXT NOT NULL DEFAULT '[]',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- ─── 4. canon (CanonRepository) ─────────────────────────────────
-- Singleton entity with history; version 0 is reserved for "no canon set".
CREATE TABLE canon (
    version INTEGER PRIMARY KEY AUTOINCREMENT,
    body TEXT NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    updated_by TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    is_current INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_canon_current ON canon (is_current) WHERE is_current = 1;

-- ─── 5. keychain (KeychainRepository) ───────────────────────────
-- Used when the SQLite backend is the chosen Keychain.Backend (otherwise
-- the OS keychain or sealed file backend is in use; this table stays empty).
CREATE TABLE keychain (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- ─── 6. templates (TemplateRepository) ──────────────────────────
CREATE TABLE templates (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    body BLOB NOT NULL,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- ─── 7. auth_secrets (AuthSecretRepository) ─────────────────────
-- One row per purpose (e.g. "dashboard-hs256", "a2a-es256-priv").
CREATE TABLE auth_secrets (
    purpose TEXT PRIMARY KEY,
    key BLOB NOT NULL,
    algorithm TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- ─── 8. events (EventRepository) ────────────────────────────────
-- Append-only audit log. v0.9.2 A2A fields are nullable for pre-A2A events.
CREATE TABLE events (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    actor TEXT NOT NULL DEFAULT '',
    body TEXT NOT NULL DEFAULT '',
    timestamp TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    a2a_method TEXT NOT NULL DEFAULT '',
    caller_org TEXT NOT NULL DEFAULT '',
    caller_sid TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_events_timestamp ON events (timestamp);
CREATE INDEX idx_events_kind ON events (kind);

-- ─── 9. rbac_grants (RBACGrantRepository) ───────────────────────
-- Cross-org peering grants.
CREATE TABLE rbac_grants (
    id TEXT PRIMARY KEY,
    granter_org TEXT NOT NULL,
    grantee_org TEXT NOT NULL,
    scope_json TEXT NOT NULL,
    permissions_json TEXT NOT NULL DEFAULT '[]',
    rate_limit_json TEXT NOT NULL DEFAULT 'null',
    expires_at TIMESTAMP,
    accepted INTEGER NOT NULL DEFAULT 0,
    created_by TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_grants_granter ON rbac_grants (granter_org);
CREATE INDEX idx_grants_grantee ON rbac_grants (grantee_org);

-- ─── 10. tasks (TaskRepository) ─────────────────────────────────
-- A2A task state machine. State maps to A2A spec enum values.
CREATE TABLE tasks (
    id TEXT PRIMARY KEY,
    runner_sid TEXT NOT NULL,
    state TEXT NOT NULL,
    method TEXT NOT NULL,
    input_blob BLOB,
    output_blob BLOB,
    auth_challenge TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_tasks_runner ON tasks (runner_sid);
CREATE INDEX idx_tasks_state ON tasks (state);

-- ─── 11. push_notification_configs (PushNotificationConfigRepository) ───
CREATE TABLE push_notification_configs (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL,
    url TEXT NOT NULL,
    signing_key BLOB NOT NULL,
    filters_json TEXT NOT NULL DEFAULT '[]',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_push_task ON push_notification_configs (task_id);

-- ─── 12. agent_cards (AgentCardRepository) ──────────────────────
-- Cached Agent Cards keyed by sid. Canonical Cards are served by
-- individual runners; this caches them for the daemon's directory.
CREATE TABLE agent_cards (
    sid TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    body BLOB NOT NULL,
    public_visibility INTEGER NOT NULL DEFAULT 0,
    synced_at TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_cards_public ON agent_cards (public_visibility) WHERE public_visibility = 1;

-- ─── 13. accounts (AccountRepository, doc #54) ──────────────────
-- Operator account identity + LLM credential bindings.
CREATE TABLE accounts (
    id TEXT PRIMARY KEY,
    class TEXT NOT NULL,
    label TEXT NOT NULL DEFAULT '',
    keychain_key TEXT NOT NULL DEFAULT '',
    email TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_accounts_class ON accounts (class);
