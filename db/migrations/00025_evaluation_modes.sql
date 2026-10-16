-- Mode eval: config CI asks exemplar-api to evaluate a config
-- PR's changed rules at its pushed head; the request has a head and
-- optional extra rule ids instead of a diff.

-- +goose Up
ALTER TABLE evaluations
  ADD COLUMN mode     text NOT NULL DEFAULT 'review' CHECK (mode IN ('review', 'eval')),
  ADD COLUMN head_sha text,
  ADD COLUMN rules    text[] NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE evaluations DROP COLUMN rules, DROP COLUMN head_sha, DROP COLUMN mode;
