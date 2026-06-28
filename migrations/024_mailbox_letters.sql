-- +migrate Up
-- User-level mailbox. Unlike matters, these rows are not scoped by space_id.

CREATE TABLE mailbox_letters (
  id CHAR(36) NOT NULL PRIMARY KEY,
  user_id VARCHAR(64) NOT NULL,
  source_type ENUM('system','agent_mail') NOT NULL DEFAULT 'system',
  source_ref VARCHAR(512) NULL COMMENT 'external id such as rfc_message_id or template id',
  direction ENUM('inbound','outbound') NOT NULL DEFAULT 'inbound',
  thread_id VARCHAR(512) NULL COMMENT 'external thread id for Agent Mail replies',
  title VARCHAR(500) NOT NULL,
  snippet VARCHAR(500) NULL,
  body_text MEDIUMTEXT NULL,
  body_html MEDIUMTEXT NULL,
  from_name VARCHAR(256) NULL,
  from_email VARCHAR(256) NULL,
  metadata JSON NULL,
  read_at DATETIME(3) NULL,
  archived_at DATETIME(3) NULL,
  deleted_at DATETIME(3) NULL,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,

  UNIQUE KEY uk_user_source (user_id, source_type, source_ref),
  INDEX idx_user_visible (user_id, deleted_at, archived_at, created_at, id),
  INDEX idx_user_unread (user_id, deleted_at, read_at),
  INDEX idx_user_source_type (user_id, source_type, created_at),
  INDEX idx_user_thread (user_id, source_type, thread_id, created_at)
);

-- +migrate Down
DROP TABLE IF EXISTS mailbox_letters;
