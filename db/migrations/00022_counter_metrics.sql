-- Counter-metrics (spec §12.5, PRD §13.1): which merged PRs were later
-- reverted or named in an incident, so auto-approved and human-approved
-- merges can be compared. Only refs are stored, as in path_events.

-- +goose Up
CREATE TABLE pr_merges (
  repo      text NOT NULL,
  pr_number int  NOT NULL,
  head_sha  text NOT NULL,
  merged_at timestamptz NOT NULL,
  PRIMARY KEY (repo, pr_number)
);

-- A PR a later revert names ("Reverts owner/repo#N") or an incident links.
CREATE TABLE pr_links (
  repo      text NOT NULL,
  pr_number int  NOT NULL,
  kind      text NOT NULL CHECK (kind IN ('revert', 'incident')),
  ref       text NOT NULL,  -- the reverting PR ("#123") or commit, or the incident id
  at        timestamptz NOT NULL,
  PRIMARY KEY (repo, pr_number, kind, ref)
);

-- One row per merged PR: whether Exemplar's gates approved the merged
-- head, what it posted, and whether the merge later failed.
CREATE VIEW merge_outcomes AS
SELECT m.repo, m.pr_number, m.head_sha, m.merged_at,
  EXISTS (SELECT 1 FROM approval_decisions d JOIN reviews r ON r.id = d.review_id
          WHERE r.repo = m.repo AND r.pr_number = m.pr_number AND r.head_sha = m.head_sha
            AND r.caller = 'production' AND d.decision = 'approved') AS auto_approved,
  EXISTS (SELECT 1 FROM reviews r WHERE r.repo = m.repo AND r.pr_number = m.pr_number AND r.caller = 'production') AS reviewed,
  (SELECT count(*) FROM findings f WHERE f.repo = m.repo AND f.pr_number = m.pr_number
     AND f.state IN ('posted', 'resolved', 'outdated', 'minimized'))::int AS findings_posted,
  EXISTS (SELECT 1 FROM pr_links l WHERE l.repo = m.repo AND l.pr_number = m.pr_number AND l.kind = 'revert') AS reverted,
  EXISTS (SELECT 1 FROM pr_links l WHERE l.repo = m.repo AND l.pr_number = m.pr_number AND l.kind = 'incident') AS incident
FROM pr_merges m;

-- +goose Down
DROP VIEW merge_outcomes;
DROP TABLE pr_links;
DROP TABLE pr_merges;
