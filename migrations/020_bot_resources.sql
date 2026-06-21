-- +migrate Up
-- Bot resources per matter: tracks which bots are available for dispatch.
-- Only a bot's owner can add their bot (Channel model: add person → person adds bot).

CREATE TABLE IF NOT EXISTS matter_bot_resources (
    id          CHAR(36)    NOT NULL,
    matter_id   CHAR(36)    NOT NULL,
    bot_uid     VARCHAR(64) NOT NULL,
    owner_uid   VARCHAR(64) NOT NULL,
    created_at  DATETIME(3) NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_matter_bot (matter_id, bot_uid),
    KEY idx_owner (owner_uid)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +migrate Down
DROP TABLE IF EXISTS matter_bot_resources;
