-- Feedback events: one row per signal on a finding, with
-- the actor's role on the PR and its weight.

-- +goose Up
CREATE TABLE feedback_events (
  id          bigserial PRIMARY KEY,
  finding_id  bigint NOT NULL REFERENCES findings(id),
  kind        text NOT NULL CHECK (kind IN ('fb_fixed', 'fb_wrong', 'fb_not_important', 'fb_handled', 'fb_disagree',
                                            'applied_suggestion', 'fixed', 'reply_agree', 'reply_disagree', 'reply_question',
                                            'react_up', 'react_down', 'resolved', 'ignored', 'human_agreed',
                                            'adjudicated_uphold', 'adjudicated_overturn')),
  actor       text NOT NULL,
  -- observer has weight 0 and is kept for audit only.
  role        text NOT NULL CHECK (role IN ('author', 'reviewer', 'owner', 'observer')),
  weight      real NOT NULL,
  via         text NOT NULL CHECK (via IN ('link', 'slash', 'reaction', 'webhook', 'code_scanning', 'inferred')),
  reason_text text,
  teach_class text CHECK (teach_class IN ('scope', 'exception', 'rule_wrong', 'noise')),
  evidence    jsonb,
  observed_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (finding_id, kind, actor)
);

-- +goose Down
DROP TABLE feedback_events;
