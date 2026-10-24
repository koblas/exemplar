-- Config snapshots (spec §2.4 C3): each validated exemplar-config version
-- the worker loads, as its file tree, so a runner reviews with the
-- version its job was dispatched with and never reads the config repo.

-- +goose Up
CREATE TABLE config_snapshots (
  version   text PRIMARY KEY,           -- exemplar-config commit, or a tree hash outside git-sync
  files     jsonb NOT NULL,             -- path -> file contents (base64), as validated
  loaded_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE config_snapshots;
