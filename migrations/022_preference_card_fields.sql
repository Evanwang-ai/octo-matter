-- +migrate Up
ALTER TABLE preference_cards
    ADD COLUMN task_type VARCHAR(200) NULL COMMENT 'comma-separated task type tags for retrieval' AFTER avoid,
    ADD COLUMN underlying VARCHAR(500) NULL COMMENT 'deeper judgment standard behind this rule' AFTER task_type,
    ADD COLUMN source_cards JSON NULL COMMENT 'source card IDs for layer-2 induction' AFTER links,
    ADD COLUMN layer TINYINT UNSIGNED NOT NULL DEFAULT 1 COMMENT '1=direct distillation 2=cross-card induction' AFTER source_cards;
