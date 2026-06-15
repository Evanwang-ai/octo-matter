-- +migrate Up
-- Preference lifecycle metadata for matter_summaries. The table remains the
-- approval ledger, but authorized rows can now carry scope, evidence and
-- hit/miss calibration instead of being only a markdown blob.
ALTER TABLE matter_summaries
    ADD COLUMN scope_type VARCHAR(32) NOT NULL DEFAULT 'matter' AFTER scope,
    ADD COLUMN scope_key VARCHAR(128) NULL AFTER scope_type,
    ADD COLUMN evidence_matter_id CHAR(36) NULL AFTER scope_key,
    ADD COLUMN evidence_entry_ids JSON NULL AFTER evidence_matter_id,
    ADD COLUMN evidence_feedback_ids JSON NULL AFTER evidence_entry_ids,
    ADD COLUMN confidence TINYINT UNSIGNED NOT NULL DEFAULT 50 AFTER evidence_feedback_ids,
    ADD COLUMN hit_count INT UNSIGNED NOT NULL DEFAULT 0 AFTER confidence,
    ADD COLUMN miss_count INT UNSIGNED NOT NULL DEFAULT 0 AFTER hit_count,
    ADD COLUMN last_applied_at DATETIME(3) NULL AFTER miss_count,
    ADD KEY idx_summaries_scope (space_id, target_bot_uid, status, scope_type, scope_key);

-- +migrate Down
ALTER TABLE matter_summaries
    DROP KEY idx_summaries_scope,
    DROP COLUMN last_applied_at,
    DROP COLUMN miss_count,
    DROP COLUMN hit_count,
    DROP COLUMN confidence,
    DROP COLUMN evidence_feedback_ids,
    DROP COLUMN evidence_entry_ids,
    DROP COLUMN evidence_matter_id,
    DROP COLUMN scope_key,
    DROP COLUMN scope_type;
