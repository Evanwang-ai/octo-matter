-- +migrate Up
-- Prototype-fidelity additions (CURRENT_PROTOTYPE_INTERACTION_SPEC):
--   - Brief 约束 / 输出要求 (doc 02: H-source fields folded into the Brief)
--   - 自动化输出模式 + 发送目标 (Or5/O3: track=立成事项, runonly=结果发回会话;
--     delivery of runonly results is the EXECUTOR's job per O3 report-back)
--   - 项目上下文来源 (PRD 项目=文件夹+共享上下文; sources are H-mounted)

ALTER TABLE matters
    ADD COLUMN brief_constraints TEXT NULL AFTER description,
    ADD COLUMN brief_output_spec TEXT NULL AFTER brief_constraints;

ALTER TABLE matter_schedules
    ADD COLUMN output_mode VARCHAR(10) NOT NULL DEFAULT 'track' AFTER executor_uid,
    ADD COLUMN target_channel_id VARCHAR(255) NULL AFTER output_mode,
    ADD COLUMN target_channel_name VARCHAR(200) NULL AFTER target_channel_id;

CREATE TABLE matter_project_sources (
    id         CHAR(36)     NOT NULL,
    project_id CHAR(36)     NOT NULL,
    space_id   VARCHAR(64)  NOT NULL,
    kind       VARCHAR(20)  NOT NULL DEFAULT 'chat',
    title      VARCHAR(300) NOT NULL,
    ref        VARCHAR(1024) NULL,
    snippet    TEXT         NULL,
    created_by VARCHAR(64)  NOT NULL,
    created_at DATETIME(3)  NOT NULL,
    PRIMARY KEY (id),
    KEY idx_project_sources (project_id, created_at),
    CONSTRAINT fk_sources_project FOREIGN KEY (project_id)
        REFERENCES matter_projects (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +migrate Down
DROP TABLE IF EXISTS matter_project_sources;
ALTER TABLE matter_schedules
    DROP COLUMN target_channel_name,
    DROP COLUMN target_channel_id,
    DROP COLUMN output_mode;
ALTER TABLE matters
    DROP COLUMN brief_output_spec,
    DROP COLUMN brief_constraints;
