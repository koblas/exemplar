-- Calibration results (spec §6.8, FR-E10): every verifier or categorizer
-- calibration run the installation makes. Keyed by what the result is
-- valid for (calibrate.VerifierKey), not by config commit, so editing a
-- rule does not undo it. Stage 1 lets a same-family verification pass
-- only while the latest verifier row for the deployed key passed.

-- +goose Up
CREATE TABLE calibration_results (
  id                  bigserial PRIMARY KEY,
  kind                text NOT NULL CHECK (kind IN ('verifier', 'categorizer')),
  key                 text NOT NULL,             -- calibrate.VerifierKey (or the categorizer's)
  config_version      text NOT NULL,             -- the exemplar-config version it ran on, for the record
  providers           text[] NOT NULL,           -- provider:model, as configured
  known_wrong_refuted real,
  known_true_refuted  real,
  labels              int NOT NULL,
  passed              boolean NOT NULL,
  ran_at              timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX calibration_results_latest_idx ON calibration_results (kind, key, ran_at DESC);

-- +goose Down
DROP TABLE calibration_results;
