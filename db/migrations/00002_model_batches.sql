-- Message Batches routed through the gateway (learning-plane extraction,
-- spec §8.2). The gateway records which provider owns each batch so later
-- calls go to the same place, and books usage to model_calls exactly once.

-- +goose Up
CREATE TABLE model_batches (
  batch_id      text PRIMARY KEY,               -- provider's batch id
  provider      text NOT NULL,
  model         text NOT NULL,
  stage         text NOT NULL,
  caller        text NOT NULL,
  repo          text,
  data_classes  text[] NOT NULL,
  request_count int NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),
  accounted_at  timestamptz                     -- set once usage is booked
);

ALTER TABLE model_calls ADD COLUMN batch_id text;

-- +goose Down
ALTER TABLE model_calls DROP COLUMN batch_id;
DROP TABLE model_batches;
