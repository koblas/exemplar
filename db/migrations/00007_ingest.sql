-- Ingest (spec §8.1): resumable per-repo GraphQL paging over PRs.

-- +goose Up
CREATE TABLE ingest_cursors (
  repo        text PRIMARY KEY,
  cursor      text,                                -- GraphQL endCursor of the last finished page
  updated_at  timestamptz NOT NULL DEFAULT now(),
  prs_seen    int NOT NULL DEFAULT 0,
  kept        int NOT NULL DEFAULT 0,              -- comments stored
  filtered    int NOT NULL DEFAULT 0               -- comments dropped by the pre-filters
);

-- +goose Down
DROP TABLE ingest_cursors;
