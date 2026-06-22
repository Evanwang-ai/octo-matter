-- +migrate Up
ALTER TABLE matters ADD COLUMN mode_config JSON NULL AFTER mode;
