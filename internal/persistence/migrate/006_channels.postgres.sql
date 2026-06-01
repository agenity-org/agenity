-- 006_channels.postgres.sql — #655 epic #654 Slack-chat foundation.
-- Mirrors 006_channels.sqlite.sql with Postgres-native types.

CREATE TABLE channels (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL UNIQUE,
    kind         TEXT NOT NULL DEFAULT 'team',
    created_by   TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL,
    visibility   TEXT NOT NULL DEFAULT 'irc'
);

CREATE TABLE channel_members (
    channel_id   TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    member       TEXT NOT NULL,
    joined_at    TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (channel_id, member)
);

CREATE INDEX idx_channel_members_member ON channel_members (member);

CREATE TABLE channel_messages (
    id           TEXT PRIMARY KEY,
    channel_id   TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    author       TEXT NOT NULL,
    body         TEXT NOT NULL,
    mentions     TEXT NOT NULL DEFAULT '[]',
    task_id      TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL
);

CREATE INDEX idx_channel_messages_channel_ts ON channel_messages (channel_id, created_at DESC);
