-- +migrate Up
-- AgentCard declared half (doc 04 §五: 声明半 = creator-curated profile, H
-- source; the earned half stays derived from acceptance stats). One row per
-- bot per space, editable only by the bot's creator.
CREATE TABLE IF NOT EXISTS matter_agent_cards (
    bot_uid     VARCHAR(64)  NOT NULL,
    space_id    VARCHAR(64)  NOT NULL,
    owner_uid   VARCHAR(64)  NOT NULL,
    tagline     VARCHAR(200) NULL,
    description TEXT         NULL,
    skills      JSON         NULL,
    systems     JSON         NULL,
    updated_at  DATETIME(3)  NOT NULL,
    PRIMARY KEY (bot_uid, space_id)
);

-- +migrate Down
DROP TABLE IF EXISTS matter_agent_cards;
