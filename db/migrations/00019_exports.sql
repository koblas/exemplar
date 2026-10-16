-- Rule exports: what was last proposed for each generated
-- file, for drift (config_sha) and hand-edit detection (the block on the
-- default branch must be the one proposed, or the one before it).

-- +goose Up
CREATE TABLE exports (
  repo         text NOT NULL,
  path         text NOT NULL,
  target       text NOT NULL CHECK (target IN ('agents_md', 'claude_md', 'copilot', 'cursor')),
  content_hash text NOT NULL,          -- the block proposed
  base_hash    text NOT NULL,          -- the block on the default branch then ('' for none)
  config_sha   text NOT NULL,
  pr_url       text,
  state        text NOT NULL CHECK (state IN ('proposed', 'current', 'edited')),
  auto_merged  boolean NOT NULL DEFAULT false,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (repo, path)
);

-- +goose Down
DROP TABLE exports;
