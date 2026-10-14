-- +goose NO TRANSACTION
-- Indexes for the daily retention job (spec §13.1, FR-H7), built without
-- blocking writes.

-- +goose Up
CREATE INDEX CONCURRENTLY IF NOT EXISTS gh_events_payload_age_idx ON gh_events (received_at) WHERE payload IS NOT NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS model_calls_at_idx ON model_calls (at);
CREATE INDEX CONCURRENTLY IF NOT EXISTS findings_created_at_idx ON findings (created_at);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS findings_created_at_idx;
DROP INDEX CONCURRENTLY IF EXISTS model_calls_at_idx;
DROP INDEX CONCURRENTLY IF EXISTS gh_events_payload_age_idx;
