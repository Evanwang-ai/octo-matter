-- +migrate Up
-- Add Linear-style priority to matters: 0 none, 1 urgent, 2 high, 3 medium, 4 low.

ALTER TABLE matters
    ADD COLUMN priority TINYINT UNSIGNED NOT NULL DEFAULT 0 AFTER status,
    ADD INDEX idx_matters_space_priority (space_id, priority, created_at);

-- +migrate Down
ALTER TABLE matters
    DROP INDEX idx_matters_space_priority,
    DROP COLUMN priority;
