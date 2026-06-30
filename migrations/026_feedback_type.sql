-- +migrate Up
ALTER TABLE matter_feedbacks
    ADD COLUMN type VARCHAR(16) NOT NULL DEFAULT 'feedback' AFTER content;

-- +migrate Down
ALTER TABLE matter_feedbacks
    DROP COLUMN type;
