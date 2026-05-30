-- chepherd v0.9.3 #225 row H3 — Artifact persistence (SQLite dialect).
-- A2A v1.0 Tasks emit zero-or-more Artifacts (the agent's structured
-- output). Refs #225 row H3 #208.

CREATE TABLE artifacts (
    id            TEXT PRIMARY KEY,
    task_id       TEXT NOT NULL,
    name          TEXT NOT NULL DEFAULT '',
    parts_json    TEXT NOT NULL DEFAULT '[]',
    metadata_json TEXT NOT NULL DEFAULT '{}',
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE
);
CREATE INDEX idx_artifacts_task ON artifacts (task_id);
