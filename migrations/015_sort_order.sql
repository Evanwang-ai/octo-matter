-- +migrate Up
-- Add sort_order for user-controlled ordering in board/timeline views.

ALTER TABLE matters ADD COLUMN sort_order DOUBLE DEFAULT NULL AFTER deadline;
