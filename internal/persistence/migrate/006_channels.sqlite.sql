-- 006_channels.sqlite.sql — #655 epic #654 Slack-chat foundation.
--
-- Three tables for the chat substrate:
--   channels       — named rooms (one per team auto-created + ad-hoc operator-created)
--   channel_members — membership join table (humans + agents)
--   channel_messages — chat history with author + content

CREATE TABLE channels (
    id           TEXT PRIMARY KEY,           -- UUIDv7
    name         TEXT NOT NULL UNIQUE,        -- '#team-default' / '#incidents' / etc
    kind         TEXT NOT NULL DEFAULT 'team',-- 'team' | 'ad-hoc' | 'dm'
    created_by   TEXT NOT NULL DEFAULT '',    -- @-handle of creator (operator or agent)
    created_at   TIMESTAMP NOT NULL,
    visibility   TEXT NOT NULL DEFAULT 'irc'  -- 'irc' (default; member-readable) | 'dm' (pair-only)
);

CREATE TABLE channel_members (
    channel_id   TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    member       TEXT NOT NULL,               -- @-handle: 'operator', 'tech-lead', ...
    joined_at    TIMESTAMP NOT NULL,
    PRIMARY KEY (channel_id, member)
);

CREATE INDEX idx_channel_members_member ON channel_members (member);

CREATE TABLE channel_messages (
    id           TEXT PRIMARY KEY,            -- UUIDv7
    channel_id   TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    author       TEXT NOT NULL,               -- @-handle of sender
    body         TEXT NOT NULL,               -- raw text; @-mentions are parsed at read time
    mentions     TEXT NOT NULL DEFAULT '[]',  -- JSON array of @-handles called out (cache for fan-out)
    task_id      TEXT NOT NULL DEFAULT '',    -- FK to A2A Task spawned by fan-out (per-recipient)
    created_at   TIMESTAMP NOT NULL
);

CREATE INDEX idx_channel_messages_channel_ts ON channel_messages (channel_id, created_at DESC);
