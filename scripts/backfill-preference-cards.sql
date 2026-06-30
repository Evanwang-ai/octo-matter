-- One-time backfill: ensure all authorized matter_summaries have corresponding
-- preference_cards rows. Required after switching recall from summaries to cards.
-- Run once during deployment, then discard.
--
-- Safe to re-run: the NOT EXISTS guard prevents duplicates.

INSERT INTO preference_cards (id, space_id, matter_id, project_id, agent_uid, creator_id, status, scope, content, evidence, layer, created_at, updated_at)
SELECT
    UUID(),
    ms.space_id,
    ms.matter_id,
    m.project_id,
    ms.target_bot_uid,
    m.creator_id,
    'authorized',
    CASE
        WHEN ms.scope_type = 'global' THEN 'global'
        WHEN ms.scope_type = 'space' THEN 'global'
        WHEN ms.scope_type = 'project' THEN 'project'
        WHEN ms.scope_type = 'bot' THEN 'global'
        ELSE 'project'
    END,
    COALESCE(ms.content, ''),
    ms.scope,
    1,
    ms.created_at,
    ms.updated_at
FROM matter_summaries ms
JOIN matters m ON ms.matter_id = m.id
WHERE ms.status = 'authorized'
  AND NOT EXISTS (
    SELECT 1 FROM preference_cards pc
    WHERE pc.matter_id = ms.matter_id
      AND pc.creator_id = m.creator_id
      AND pc.status = 'authorized'
  );
