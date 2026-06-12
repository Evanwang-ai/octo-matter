-- +migrate Up
-- Matter v2 engine (design docs 02/02.5/09): six-state machine, sub-matters,
-- collaboration modes, epoch fencing, CAS, merge-guarantee counters,
-- transactional outbox, feedback, projects, schedules, summaries, bot tasks.
-- Everything here is additive; v1 rows keep working untouched.

ALTER TABLE matters
    MODIFY COLUMN status ENUM('open','in_progress','review','done','blocked','cancelled','archived')
        CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'open',
    ADD COLUMN leader_uid VARCHAR(64) NULL AFTER creator_id,
    ADD COLUMN parent_matter_id CHAR(36) NULL AFTER space_id,
    ADD COLUMN mode VARCHAR(20) NULL AFTER status,
    ADD COLUMN step_id VARCHAR(64) NULL AFTER mode,
    ADD COLUMN step_order INT UNSIGNED NULL AFTER step_id,
    ADD COLUMN project_id CHAR(36) NULL AFTER step_order,
    ADD COLUMN assignment_epoch INT UNSIGNED NOT NULL DEFAULT 0 AFTER project_id,
    ADD COLUMN version BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER assignment_epoch,
    ADD COLUMN events_seq BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER version,
    ADD COLUMN processed_seq BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER events_seq,
    ADD COLUMN inflight TINYINT UNSIGNED NOT NULL DEFAULT 0 AFTER processed_seq,
    ADD COLUMN expected_duration_minutes INT UNSIGNED NULL AFTER inflight,
    ADD COLUMN last_activity_at DATETIME(3) NULL AFTER expected_duration_minutes,
    ADD COLUMN last_transition_at DATETIME(3) NULL AFTER last_activity_at,
    ADD COLUMN last_watchdog_alert_at DATETIME(3) NULL AFTER last_transition_at,
    ADD COLUMN block_reason_kind VARCHAR(10) NULL AFTER last_watchdog_alert_at,
    ADD COLUMN block_reason_text VARCHAR(500) NULL AFTER block_reason_kind,
    ADD COLUMN schedule_id CHAR(36) NULL AFTER block_reason_text,
    ADD COLUMN scheduled_at DATETIME(3) NULL AFTER schedule_id,
    ADD UNIQUE KEY uk_matters_parent_step (parent_matter_id, step_id),
    ADD UNIQUE KEY uk_matters_schedule_run (schedule_id, scheduled_at),
    ADD KEY idx_matters_space_parent (space_id, parent_matter_id),
    ADD KEY idx_matters_project (project_id),
    ADD KEY idx_matters_watchdog (status, last_activity_at),
    ADD CONSTRAINT fk_matters_parent FOREIGN KEY (parent_matter_id)
        REFERENCES matters (id) ON DELETE SET NULL;

ALTER TABLE matter_timelines
    ADD COLUMN on_behalf_of VARCHAR(64) NULL AFTER user_id;

CREATE TABLE matter_projects (
    id                 CHAR(36)     NOT NULL,
    space_id           VARCHAR(64)  NOT NULL,
    name               VARCHAR(200) NOT NULL,
    description        TEXT         NULL,
    scope              VARCHAR(20)  NOT NULL DEFAULT 'space',
    source_channel_id  VARCHAR(255) NULL,
    source_name        VARCHAR(200) NULL,
    default_leader_uid VARCHAR(64)  NULL,
    creator_id         VARCHAR(64)  NOT NULL,
    archived           TINYINT UNSIGNED NOT NULL DEFAULT 0,
    created_at         DATETIME(3)  NOT NULL,
    updated_at         DATETIME(3)  NOT NULL,
    PRIMARY KEY (id),
    KEY idx_projects_space (space_id, archived)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Transactional outbox: doorbell rows are written in the SAME transaction as
-- the status transition that produced them (doc 02.5 "送达保障").
CREATE TABLE matter_outbox (
    id          CHAR(36)     NOT NULL,
    space_id    VARCHAR(64)  NOT NULL,
    matter_id   CHAR(36)     NOT NULL,
    target_uid  VARCHAR(64)  NOT NULL,
    actor_uid   VARCHAR(64)  NOT NULL DEFAULT '',
    event       VARCHAR(50)  NOT NULL,
    message_key VARCHAR(100) NOT NULL DEFAULT '',
    params      JSON         NULL,
    state       VARCHAR(12)  NOT NULL DEFAULT 'pending',
    retry_count INT UNSIGNED NOT NULL DEFAULT 0,
    next_retry_at DATETIME(3) NOT NULL,
    last_error  VARCHAR(500) NULL,
    created_at  DATETIME(3)  NOT NULL,
    updated_at  DATETIME(3)  NOT NULL,
    PRIMARY KEY (id),
    KEY idx_outbox_due (state, next_retry_at),
    KEY idx_outbox_matter_target (matter_id, target_uid, state)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE matter_feedbacks (
    id         CHAR(36)    NOT NULL,
    matter_id  CHAR(36)    NOT NULL,
    space_id   VARCHAR(64) NOT NULL,
    author_id  VARCHAR(64) NOT NULL,
    target_uid VARCHAR(64) NULL,
    entry_id   CHAR(36)    NULL,
    anchor     JSON        NULL,
    content    TEXT        NOT NULL,
    created_at DATETIME(3) NOT NULL,
    PRIMARY KEY (id),
    KEY idx_feedbacks_matter (matter_id, created_at),
    CONSTRAINT fk_feedbacks_matter FOREIGN KEY (matter_id)
        REFERENCES matters (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE matter_summaries (
    id             CHAR(36)    NOT NULL,
    matter_id      CHAR(36)    NOT NULL,
    space_id       VARCHAR(64) NOT NULL,
    status         VARCHAR(12) NOT NULL DEFAULT 'draft',
    content        MEDIUMTEXT  NULL,
    target_bot_uid VARCHAR(64) NULL,
    scope          VARCHAR(100) NULL,
    created_by     VARCHAR(64) NOT NULL,
    created_at     DATETIME(3) NOT NULL,
    updated_at     DATETIME(3) NOT NULL,
    PRIMARY KEY (id),
    KEY idx_summaries_matter (matter_id, created_at),
    CONSTRAINT fk_summaries_matter FOREIGN KEY (matter_id)
        REFERENCES matters (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE matter_schedules (
    id           CHAR(36)     NOT NULL,
    space_id     VARCHAR(64)  NOT NULL,
    title        VARCHAR(500) NOT NULL,
    runbook      TEXT         NULL,
    cron_expr    VARCHAR(100) NOT NULL,
    timezone     VARCHAR(64)  NOT NULL DEFAULT 'Asia/Shanghai',
    executor_uid VARCHAR(64)  NOT NULL,
    project_id   CHAR(36)     NULL,
    creator_id   VARCHAR(64)  NOT NULL,
    enabled      TINYINT UNSIGNED NOT NULL DEFAULT 1,
    last_run_at  DATETIME(3)  NULL,
    next_run_at  DATETIME(3)  NULL,
    created_at   DATETIME(3)  NOT NULL,
    updated_at   DATETIME(3)  NOT NULL,
    PRIMARY KEY (id),
    KEY idx_schedules_due (enabled, next_run_at),
    KEY idx_schedules_space (space_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Bot task queue. Shape mirrors octo-fleet's bot_task row (PR-B.3 moved the
-- queue into octo-matter; fleet returns 410 pointing here).
CREATE TABLE matter_bot_tasks (
    id             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    matter_id      CHAR(36)     NOT NULL,
    space_id       VARCHAR(64)  NOT NULL,
    bot_uid        VARCHAR(64)  NOT NULL,
    requester_uid  VARCHAR(64)  NOT NULL DEFAULT '',
    title          VARCHAR(500) NOT NULL,
    description    TEXT         NULL,
    prompt         MEDIUMTEXT   NULL,
    status         VARCHAR(12)  NOT NULL DEFAULT 'queued',
    claim_token    CHAR(36)     NULL,
    claimed_by     VARCHAR(128) NULL,
    result_summary MEDIUMTEXT   NULL,
    error_msg      TEXT         NULL,
    created_by     VARCHAR(64)  NOT NULL DEFAULT '',
    created_at     DATETIME(3)  NOT NULL,
    updated_at     DATETIME(3)  NOT NULL,
    PRIMARY KEY (id),
    KEY idx_bot_tasks_pull (status, created_at),
    KEY idx_bot_tasks_matter (matter_id),
    KEY idx_bot_tasks_bot (bot_uid, status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +migrate Down
DROP TABLE IF EXISTS matter_bot_tasks;
DROP TABLE IF EXISTS matter_schedules;
DROP TABLE IF EXISTS matter_summaries;
DROP TABLE IF EXISTS matter_feedbacks;
DROP TABLE IF EXISTS matter_outbox;
DROP TABLE IF EXISTS matter_projects;
ALTER TABLE matter_timelines DROP COLUMN on_behalf_of;
ALTER TABLE matters
    DROP FOREIGN KEY fk_matters_parent,
    DROP KEY uk_matters_parent_step,
    DROP KEY uk_matters_schedule_run,
    DROP KEY idx_matters_space_parent,
    DROP KEY idx_matters_project,
    DROP KEY idx_matters_watchdog,
    DROP COLUMN scheduled_at,
    DROP COLUMN schedule_id,
    DROP COLUMN block_reason_text,
    DROP COLUMN block_reason_kind,
    DROP COLUMN last_watchdog_alert_at,
    DROP COLUMN last_transition_at,
    DROP COLUMN last_activity_at,
    DROP COLUMN expected_duration_minutes,
    DROP COLUMN inflight,
    DROP COLUMN processed_seq,
    DROP COLUMN events_seq,
    DROP COLUMN version,
    DROP COLUMN assignment_epoch,
    DROP COLUMN project_id,
    DROP COLUMN step_order,
    DROP COLUMN step_id,
    DROP COLUMN mode,
    DROP COLUMN parent_matter_id,
    DROP COLUMN leader_uid,
    MODIFY COLUMN status ENUM('open','done','archived')
        CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'open';
