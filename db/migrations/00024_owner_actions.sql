-- Owner actions from the web UI (spec §12.8): each request is recorded
-- with its actor, then a worker job checks the actor may take it and
-- opens (and where it can, merges) the exemplar-config PR. Git stays the
-- source of truth; this table is the audit of who asked for what.

-- +goose Up
CREATE TABLE owner_actions (
  id          bigserial PRIMARY KEY,
  action      text NOT NULL CHECK (action IN ('shadow_rule', 'kill_rule', 'kill_candidate', 'kill_switch')),
  target      text NOT NULL,                -- rule id, candidate id, or owner/repo
  actor       text NOT NULL,                -- GitHub login
  reason      text NOT NULL DEFAULT '',
  status      text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'done', 'refused', 'failed')),
  result      text,                         -- why refused or failed, or what was done
  pr_url      text,
  created_at  timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz
);
CREATE INDEX owner_actions_actor_idx ON owner_actions (actor, created_at DESC);

-- +goose Down
DROP TABLE owner_actions;
