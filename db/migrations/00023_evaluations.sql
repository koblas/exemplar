-- Evaluations (spec §10.1, PRD FR-S4, FR-S7): `exemplar review` sends a
-- working-tree diff to exemplar-api, which records it here and enqueues
-- an evaluate job; exemplar-worker runs the full review on the repo's
-- base with the diff applied and stores the result for the requester.
-- The diff is code: it is cleared as soon as the job finishes.

-- +goose Up
CREATE TABLE evaluations (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  repo         text NOT NULL,
  base_sha     text NOT NULL,
  requested_by text NOT NULL,                -- GitHub login, from the caller's token
  caller       text NOT NULL DEFAULT 'cli' CHECK (caller IN ('cli', 'agent')),
  agent        text NOT NULL DEFAULT '',     -- a coding agent the caller declared
  diff         text,                         -- cleared when the job finishes
  status       text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'done', 'refused', 'failed')),
  reason       text,                         -- why refused or failed
  review_id    bigint REFERENCES reviews(id),
  result       jsonb,
  cost_usd     numeric(10,4) NOT NULL DEFAULT 0,
  created_at   timestamptz NOT NULL DEFAULT now(),
  finished_at  timestamptz
);
CREATE INDEX evaluations_requester_idx ON evaluations (requested_by, created_at);

-- +goose Down
DROP TABLE evaluations;
