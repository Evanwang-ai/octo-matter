-- +migrate Up
-- AgentCard visibility tier (doc 04 §五 / goal module B.2: creator 自定义
-- 展示;v1 两级 — space=全空间可见(默认), private=仅主人可见声明半。
-- channel/thread 级留待 v2(需要成员关系联查)。
ALTER TABLE matter_agent_cards
    ADD COLUMN visibility VARCHAR(10) NOT NULL DEFAULT 'space';

-- +migrate Down
ALTER TABLE matter_agent_cards DROP COLUMN visibility;
