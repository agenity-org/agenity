-- chepherd v0.9.3 #225 row H3 — Artifact persistence (PostgreSQL dialect).
-- Mirror of 003_artifacts.sqlite.sql. Refs #225 row H3 #208.

CREATE TABLE artifacts (
    id            TEXT PRIMARY KEY,
    task_id       TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    name          TEXT NOT NULL DEFAULT '',
    parts_json    JSONB NOT NULL DEFAULT '[]'::jsonb,
    metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_artifacts_task ON artifacts (task_id);
