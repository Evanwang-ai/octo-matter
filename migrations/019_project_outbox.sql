-- +migrate Up
-- Project-level structural doorbells. These are reliable FYI rings for
-- shared-context changes when no live Matter can act as the delivery carrier.

CREATE TABLE matter_project_outbox (
    id          CHAR(36)     NOT NULL,
    space_id    VARCHAR(64)  NOT NULL,
    project_id  CHAR(36)     NOT NULL,
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
    KEY idx_project_outbox_due (state, next_retry_at),
    KEY idx_project_outbox_project_target (project_id, target_uid, state)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +migrate Down
DROP TABLE IF EXISTS matter_project_outbox;
