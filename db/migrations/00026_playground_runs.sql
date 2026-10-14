-- Playground runs from the web UI (spec §12.8): a draft rule tried on one
-- PR. exemplar-api records the request after checking the viewer can read
-- the PR's repo; a worker job runs the rule alone and stores what it
-- found. Results carry finding text, so they keep findings' retention.

-- +goose Up
CREATE TABLE playground_runs (
  id          bigserial PRIMARY KEY,
  actor       text NOT NULL,
  repo        text NOT NULL,
  pr_number   int  NOT NULL,
  rule_id     text NOT NULL,
  rule_yaml   text NOT NULL,
  status      text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'done', 'refused', 'failed')),
  reason      text,
  result      jsonb,
  cost_usd    numeric(10,4) NOT NULL DEFAULT 0,
  created_at  timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz
);
CREATE INDEX playground_runs_actor_idx ON playground_runs (actor, created_at DESC);

ALTER TABLE owner_actions DROP CONSTRAINT owner_actions_action_check;
ALTER TABLE owner_actions ADD CONSTRAINT owner_actions_action_check
  CHECK (action IN ('shadow_rule', 'kill_rule', 'kill_candidate', 'kill_switch', 'propose_rule'));

-- +goose Down
DELETE FROM owner_actions WHERE action = 'propose_rule';
ALTER TABLE owner_actions DROP CONSTRAINT owner_actions_action_check;
ALTER TABLE owner_actions ADD CONSTRAINT owner_actions_action_check
  CHECK (action IN ('shadow_rule', 'kill_rule', 'kill_candidate', 'kill_switch'));
DROP TABLE playground_runs;
