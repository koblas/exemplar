-- Per-review disk (spec §2.4.2 C6): each review records the size of its
-- checkout, and a runner Job's ephemeral volume is sized from the repo's
-- recent checkouts.

-- +goose Up
ALTER TABLE reviews ADD COLUMN checkout_bytes bigint;

-- +goose Down
ALTER TABLE reviews DROP COLUMN checkout_bytes;
