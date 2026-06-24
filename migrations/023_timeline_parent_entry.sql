-- +migrate Up
ALTER TABLE matter_timelines
    ADD COLUMN parent_entry_id CHAR(36) NULL AFTER user_id,
    ADD INDEX idx_parent (parent_entry_id);

-- +migrate Down
ALTER TABLE matter_timelines
    DROP INDEX idx_parent,
    DROP COLUMN parent_entry_id;
