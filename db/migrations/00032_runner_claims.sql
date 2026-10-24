-- Runner claims (spec §2.4 C2): the GitHub tokens one dispatched job may
-- use, minted by control, redeemed once by the runner with a nonce, and
-- cleared when it exits. The App private key never reaches a runner.

-- +goose Up
CREATE TABLE runner_claims (
  job_id         bigint PRIMARY KEY,              -- River job; a retry replaces the row
  attempt        int NOT NULL,
  nonce_hash     bytea NOT NULL,                  -- sha256 of the one-time nonce
  tokens         bytea,                           -- AES-GCM sealed grant; NULL once released
  config_version text NOT NULL REFERENCES config_snapshots(version),
  created_at     timestamptz NOT NULL DEFAULT now(),
  claimed_at     timestamptz,
  released_at    timestamptz,
  expires_at     timestamptz NOT NULL             -- the earliest token expiry
);

-- +goose Down
DROP TABLE runner_claims;
