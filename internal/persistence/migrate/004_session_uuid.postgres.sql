-- chepherd v0.9.3 #350 D4 — claude session UUID column (PostgreSQL).
-- Refs #350 D4 #208.

ALTER TABLE sessions ADD COLUMN claude_session_uuid TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_sessions_claude_uuid ON sessions (claude_session_uuid)
    WHERE claude_session_uuid != '';
