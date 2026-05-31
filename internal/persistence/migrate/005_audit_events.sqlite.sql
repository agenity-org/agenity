-- 005_audit_events.sqlite.sql — #489 Wave AU2.
--
-- Daemon-side audit-event aggregator. Persists §10-step-24 events
-- streamed up from runners over their register WS. Per-org
-- partitioned via org_id column; queries are org-scoped at the
-- repository layer.

CREATE TABLE audit_events (
    id            TEXT PRIMARY KEY,
    org_id        TEXT NOT NULL,
    event_type    TEXT NOT NULL,
    timestamp     TIMESTAMP NOT NULL,
    caller        TEXT NOT NULL DEFAULT '',
    callee        TEXT NOT NULL DEFAULT '',
    method        TEXT NOT NULL DEFAULT '',
    latency_ms    INTEGER NOT NULL DEFAULT 0,
    jti           TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL DEFAULT '',
    error         TEXT NOT NULL DEFAULT '',
    task_id       TEXT NOT NULL DEFAULT '',
    raw_json      TEXT NOT NULL DEFAULT '{}'
);

-- Dashboard query path is (org_id, timestamp DESC); index covers it.
CREATE INDEX idx_audit_events_org_ts ON audit_events (org_id, timestamp DESC);
-- Common caller/callee filter dimensions.
CREATE INDEX idx_audit_events_caller ON audit_events (org_id, caller);
CREATE INDEX idx_audit_events_callee ON audit_events (org_id, callee);
