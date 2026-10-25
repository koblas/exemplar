-- Verifier quorum for the bug lane (spec §6.8, FR-V7): a bug-lane
-- candidate, or one of a rule with verify: quorum, goes to
-- verifier.quorum.n verifiers and posts only when k confirm. The share
-- agreed / n is kept on every quorum candidate, posted or not, for the
-- lanes board.

-- +goose Up
ALTER TABLE findings ADD COLUMN verify_quorum_n int, ADD COLUMN verify_quorum_agreed int;

-- +goose Down
ALTER TABLE findings DROP COLUMN verify_quorum_agreed, DROP COLUMN verify_quorum_n;
