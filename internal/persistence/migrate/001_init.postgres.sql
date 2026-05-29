-- chepherd v0.9.2 initial persistence schema (PostgreSQL dialect).
-- Mirror of 001_init.sqlite.sql for the HA backend. See
-- internal/persistence/interface.go for the canonical Repository contracts.
-- Refs #208.

-- ─── 1. sessions ─────────────────────────────────────────────────
CREATE TABLE sessions (
    session_id TEXT PRIMARY KEY,
    state_json JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ─── 2. skills ───────────────────────────────────────────────────
CREATE TABLE skills (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    default_body TEXT NOT NULL DEFAULT '',
    override_body TEXT NOT NULL DEFAULT '',
    read_only BOOLEAN NOT NULL DEFAULT FALSE,
    source TEXT NOT NULL DEFAULT '',
    path TEXT NOT NULL DEFAULT '',
    sort_order INTEGER NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ─── 3. agents ───────────────────────────────────────────────────
CREATE TABLE agents (
    id TEXT PRIMARY KEY,
    type TEXT NOT NULL,
    label TEXT NOT NULL DEFAULT '',
    role_id TEXT NOT NULL DEFAULT '',
    creator_account TEXT NOT NULL DEFAULT '',
    owned_skills_json JSONB NOT NULL DEFAULT '[]'::JSONB,
    owned_skills_scope_json JSONB NOT NULL DEFAULT '{}'::JSONB,
    sessions_json JSONB NOT NULL DEFAULT '[]'::JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ─── 4. canon ────────────────────────────────────────────────────
CREATE TABLE canon (
    version SERIAL PRIMARY KEY,
    body TEXT NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    updated_by TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    is_current BOOLEAN NOT NULL DEFAULT FALSE
);
CREATE UNIQUE INDEX idx_canon_current ON canon (is_current) WHERE is_current = TRUE;

-- ─── 5. keychain ─────────────────────────────────────────────────
CREATE TABLE keychain (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ─── 6. templates ────────────────────────────────────────────────
CREATE TABLE templates (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    body BYTEA NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ─── 7. auth_secrets ─────────────────────────────────────────────
CREATE TABLE auth_secrets (
    purpose TEXT PRIMARY KEY,
    key BYTEA NOT NULL,
    algorithm TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ─── 8. events ───────────────────────────────────────────────────
CREATE TABLE events (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    actor TEXT NOT NULL DEFAULT '',
    body TEXT NOT NULL DEFAULT '',
    timestamp TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    a2a_method TEXT NOT NULL DEFAULT '',
    caller_org TEXT NOT NULL DEFAULT '',
    caller_sid TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_events_timestamp ON events (timestamp);
CREATE INDEX idx_events_kind ON events (kind);

-- ─── 9. rbac_grants ──────────────────────────────────────────────
CREATE TABLE rbac_grants (
    id TEXT PRIMARY KEY,
    granter_org TEXT NOT NULL,
    grantee_org TEXT NOT NULL,
    scope_json JSONB NOT NULL,
    permissions_json JSONB NOT NULL DEFAULT '[]'::JSONB,
    rate_limit_json JSONB NOT NULL DEFAULT 'null'::JSONB,
    expires_at TIMESTAMPTZ,
    accepted BOOLEAN NOT NULL DEFAULT FALSE,
    created_by TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_grants_granter ON rbac_grants (granter_org);
CREATE INDEX idx_grants_grantee ON rbac_grants (grantee_org);

-- ─── 10. tasks ───────────────────────────────────────────────────
CREATE TABLE tasks (
    id TEXT PRIMARY KEY,
    runner_sid TEXT NOT NULL,
    state TEXT NOT NULL,
    method TEXT NOT NULL,
    input_blob BYTEA,
    output_blob BYTEA,
    auth_challenge TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_tasks_runner ON tasks (runner_sid);
CREATE INDEX idx_tasks_state ON tasks (state);

-- ─── 11. push_notification_configs ───────────────────────────────
CREATE TABLE push_notification_configs (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL,
    url TEXT NOT NULL,
    signing_key BYTEA NOT NULL,
    filters_json JSONB NOT NULL DEFAULT '[]'::JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_push_task ON push_notification_configs (task_id);

-- ─── 12. agent_cards ─────────────────────────────────────────────
CREATE TABLE agent_cards (
    sid TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    body BYTEA NOT NULL,
    public_visibility BOOLEAN NOT NULL DEFAULT FALSE,
    synced_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_cards_public ON agent_cards (public_visibility) WHERE public_visibility = TRUE;

-- ─── 13. accounts (doc #54) ──────────────────────────────────────
CREATE TABLE accounts (
    id TEXT PRIMARY KEY,
    class TEXT NOT NULL,
    label TEXT NOT NULL DEFAULT '',
    keychain_key TEXT NOT NULL DEFAULT '',
    email TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_accounts_class ON accounts (class);
