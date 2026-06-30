-- N01: merge archived into cancelled (single terminal state)
UPDATE matters SET status = 'cancelled' WHERE status = 'archived';
