-- path-events job: where each repo's revert scan of
-- the default branch has reached.

-- +goose Up
CREATE TABLE path_event_cursors (
  repo          text PRIMARY KEY,
  reverts_since timestamptz NOT NULL,  -- commits before this were scanned
  updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX path_events_repo_at_idx ON path_events (repo, at);

-- +goose Down
DROP INDEX path_events_repo_at_idx;
DROP TABLE path_event_cursors;
