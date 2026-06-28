-- +migrate Up
-- User-level Agent Mail bindings. Credentials are intentionally nullable:
-- local agently-cli Keychain state is not a deployable service credential.

CREATE TABLE agent_mail_bindings (
  id CHAR(36) NOT NULL PRIMARY KEY,
  user_id VARCHAR(64) NOT NULL,
  bot_uid VARCHAR(64) NOT NULL,
  mail_address VARCHAR(256) NOT NULL,
  credentials_encrypted VARBINARY(4096) NULL COMMENT 'future service OAuth/token blob; never a local Keychain path',
  sync_cursor VARCHAR(512) NULL,
  sync_status ENUM('active','paused','error') NOT NULL DEFAULT 'paused',
  last_sync_at DATETIME(3) NULL,
  last_error TEXT NULL,
  retry_count INT NOT NULL DEFAULT 0,
  deleted_at DATETIME(3) NULL,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,

  UNIQUE KEY uk_user_bot (user_id, bot_uid),
  UNIQUE KEY uk_mail_address (mail_address),
  INDEX idx_user_visible (user_id, deleted_at, updated_at),
  INDEX idx_sync_status (sync_status, deleted_at, updated_at)
);

-- +migrate Down
DROP TABLE IF EXISTS agent_mail_bindings;
