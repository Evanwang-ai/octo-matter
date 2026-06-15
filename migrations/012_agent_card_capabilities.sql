-- +migrate Up
-- Structured AgentCard abilities. `skills` stays as a legacy/simple string
-- list; capabilities carries source/status/visibility so OpenClaw skill
-- snapshots can be exposed selectively and machine-readably.
ALTER TABLE matter_agent_cards
    ADD COLUMN capabilities JSON NULL AFTER systems;

-- +migrate Down
ALTER TABLE matter_agent_cards DROP COLUMN capabilities;
