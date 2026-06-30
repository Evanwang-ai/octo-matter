-- +migrate Up
CREATE TABLE IF NOT EXISTS project_members (
    id          CHAR(36)    NOT NULL,
    project_id  CHAR(36)    NOT NULL,
    user_uid    VARCHAR(64) NOT NULL,
    role        VARCHAR(16) NOT NULL DEFAULT 'member',
    added_by    VARCHAR(64) NOT NULL,
    created_at  DATETIME(3) NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_project_user (project_id, user_uid),
    KEY idx_user (user_uid),
    CONSTRAINT fk_pm_project FOREIGN KEY (project_id)
        REFERENCES matter_projects (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS project_bots (
    id          CHAR(36)    NOT NULL,
    project_id  CHAR(36)    NOT NULL,
    bot_uid     VARCHAR(64) NOT NULL,
    owner_uid   VARCHAR(64) NOT NULL,
    created_at  DATETIME(3) NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_project_bot (project_id, bot_uid),
    KEY idx_owner (owner_uid),
    CONSTRAINT fk_pb_project FOREIGN KEY (project_id)
        REFERENCES matter_projects (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +migrate Down
DROP TABLE IF EXISTS project_bots;
DROP TABLE IF EXISTS project_members;
