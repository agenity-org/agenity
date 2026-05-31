-- 005_audit_events.postgres.sql — #489 Wave AU2 (postgres twin).

CREATE TABLE audit_events (
    id            TEXT PRIMARY KEY,
    org_id        TEXT NOT NULL,
    event_type    TEXT NOT NULL,
    timestamp     TIMESTAMPTZ NOT NULL,
    caller        TEXT NOT NULL DEFAULT '',
    callee        TEXT NOT NULL DEFAULT '',
    method        TEXT NOT NULL DEFAULT '',
    latency_ms    BIGINT NOT NULL DEFAULT 0,
    jti           TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL DEFAULT '',
    error         TEXT NOT NULL DEFAULT '',
    task_id       TEXT NOT NULL DEFAULT '',
    raw_json      JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX idx_audit_events_org_ts ON audit_events (org_id, timestamp DESC);
CREATE INDEX idx_audit_events_caller ON audit_events (org_id, caller);
CREATE INDEX idx_audit_events_callee ON audit_events (org_id, callee);
