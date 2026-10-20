-- Ingest keeps only comments from the repo's owners, members and
-- collaborators. Rows mined before that check carry no trust mark (NULL);
-- `exemplar-learn ingest --recheck-authors` re-pages a repo, marks every
-- comment it still keeps true, and the rest of the repo's human comments
-- false. False rows are neither extracted nor clustered.

-- +goose Up
ALTER TABLE mined_comments ADD COLUMN author_trusted boolean;
ALTER TABLE ingest_cursors ADD COLUMN recheck_since timestamptz;

-- +goose Down
ALTER TABLE ingest_cursors DROP COLUMN recheck_since;
ALTER TABLE mined_comments DROP COLUMN author_trusted;
