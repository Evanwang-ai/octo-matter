-- 017_input_attachments.sql
-- Input materials attached at creation time, readable directly from GET /matters/:id.
-- Separates input (creator uploads) from output (timeline work product).

ALTER TABLE matters ADD COLUMN input_attachments JSON NULL AFTER source_msg_ids;
