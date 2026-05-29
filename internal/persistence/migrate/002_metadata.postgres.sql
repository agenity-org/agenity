-- chepherd v0.9.2 migration 002: add metadata JSONB column to skills,
-- agents, templates (PostgreSQL dialect; mirror of the SQLite version).
-- Refs #208.

ALTER TABLE skills    ADD COLUMN metadata_json JSONB NOT NULL DEFAULT '{}'::JSONB;
ALTER TABLE agents    ADD COLUMN metadata_json JSONB NOT NULL DEFAULT '{}'::JSONB;
ALTER TABLE templates ADD COLUMN metadata_json JSONB NOT NULL DEFAULT '{}'::JSONB;
