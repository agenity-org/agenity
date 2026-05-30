-- chepherd v0.9.3 #350 D4 — claude session UUID column on sessions table.
-- The UUID also exists embedded in the state_json blob (legacy path);
-- this column gives O(log n) lookup for the boot-time auto-resume
-- scan that runtime.NewWithStore performs.
--
-- Migration runs idempotently; ALTER TABLE ADD COLUMN is a no-op when
-- the column already exists (SQLite errors out, which the migrate
-- bookkeeping table catches via the per-file once-only application).
--
-- Refs #350 D4 #208.

ALTER TABLE sessions ADD COLUMN claude_session_uuid TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_sessions_claude_uuid ON sessions (claude_session_uuid)
    WHERE claude_session_uuid != '';
