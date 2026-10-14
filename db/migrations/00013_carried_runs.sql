-- +goose Up
-- Interdiff scoping (spec §6.2): a rule whose scoped files did not change
-- since the last complete review is carried forward, not re-run. The
-- carried rule run points at the run that actually did the work (always
-- the original, never another carried run), whose findings stay on it.
ALTER TABLE rule_runs ADD COLUMN carried_from bigint REFERENCES rule_runs(id);

-- +goose Down
ALTER TABLE rule_runs DROP COLUMN carried_from;
