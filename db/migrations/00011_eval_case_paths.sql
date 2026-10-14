-- Eval cases name the files a rule runs on (spec §8.8, §8.9). Negatives
-- need them: their expect list is empty. Positives default to the
-- expected paths.

-- +goose Up
ALTER TABLE candidate_eval_cases ADD COLUMN paths text[] NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE candidate_eval_cases DROP COLUMN paths;
