-- +goose Up
-- Re-anchoring after a rebase or force-push: the
-- normalized code a finding's fingerprint was computed from (the anchor
-- with two lines of context) and its ordinal k among identical code in the
-- file, so the new head can be searched for it.
ALTER TABLE findings ADD COLUMN anchor_code text, ADD COLUMN anchor_ordinal int;

-- +goose Down
ALTER TABLE findings DROP COLUMN anchor_code, DROP COLUMN anchor_ordinal;
