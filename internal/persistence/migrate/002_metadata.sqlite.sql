-- chepherd v0.9.2 migration 002: add metadata JSON column to skills,
-- agents, templates so v0.9.1 domain types with rich shapes (Skill,
-- Agent, Template) can round-trip through the Repository without
-- losing domain-specific fields not modeled at the column level
-- (e.g. Skill.DefaultTools, Skill.AgentTypeCompat, Agent.Frontmatter,
-- Template.Slots, Template.Icon, Template.WhenToUse).
-- Refs #208.

ALTER TABLE skills    ADD COLUMN metadata_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE agents    ADD COLUMN metadata_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE templates ADD COLUMN metadata_json TEXT NOT NULL DEFAULT '{}';
