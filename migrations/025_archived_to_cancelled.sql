-- +migrate Up
-- N01: merge archived into cancelled (single terminal state)
UPDATE matters SET status = 'cancelled' WHERE status = 'archived';

-- +migrate Down
-- no-op: cannot reliably distinguish which cancelled rows were originally archived
