// Package seeds carries owner-written seeds (spec §8.1, FR-L1.8) through
// the candidate pipeline. A seed is a rule an owner writes by hand, saved
// from the playground (owner action propose_rule) or added as a rule file
// with status: candidate in a config PR. It becomes a candidates row with
// source_kind 'owner_seed', pr_open on the PR that carries it, and from
// there takes the mined path: the evals job generates its cases
// (evalgen.Generator.RunSeeds), and this package's Job, run by the propose
// job:
//   - records seeds from the open PRs in exemplar-config (Sync);
//   - delivers generated cases to the seed's PR: committed on the app's
//     own branches, posted as a comment on a person's, never replacing
//     cases the owner wrote (Deliver);
//   - settles proposals whose PR is no longer open (Sweep): promoted once
//     merged with the rule shadow or active on the default branch; killed
//     when closed with the killed label and a person's comment as the
//     reason; otherwise a seed is retired and a mined proposal's PR is
//     reopened to ask for both, since a retired mined candidate would be
//     re-created and proposed again by the next ranking.
//
// Nothing about a seed skips the gate: its PR runs the same checks as a
// mined one, and nothing enforces until an owner merges it.
package seeds

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/learn/propose"
	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
)

// Upsert records rule (content as written at path, on the PR at prURL from
// branch) as a seed by author ("" keeps the author already recorded for
// that PR). It reports false, recording nothing, for a seed killed while
// its PR is open or open on another PR.
func Upsert(ctx context.Context, st interface {
	UpsertSeed(ctx context.Context, arg queries.UpsertSeedParams) (queries.UpsertSeedRow, error)
}, rule schema.Rule, content []byte, rulePath, prURL, branch, author string,
) (int64, bool, error) {
	yaml := string(content)
	p := queries.UpsertSeedParams{
		Title: Title(rule), Description: strings.TrimSpace(rule.Description), Owner: rule.Owner,
		ConfigPrUrl: &prURL, ConfigPrBranch: &branch, RuleID: &rule.ID, RulePath: &rulePath, RuleYaml: &yaml,
	}
	if author != "" {
		p.SeedAuthor = &author
	}
	row, err := st.UpsertSeed(ctx, p)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("seeds: record %s: %w", rule.ID, err)
	}
	return row.ID, true, nil
}

// Title is the first line of the rule's description, or its id.
func Title(rule schema.Rule) string {
	line, _, _ := strings.Cut(strings.TrimSpace(rule.Description), "\n")
	line = strings.TrimSpace(line)
	if r := []rune(line); len(r) > 120 {
		line = string(r[:117]) + "..."
	}
	if line == "" {
		return rule.ID
	}
	return line
}

// Repo is the part of exemplar-config the job needs (gh.ConfigRepo).
type Repo interface {
	DefaultBranch(ctx context.Context) (name, sha string, err error)
	OpenPRs(ctx context.Context) ([]gh.ConfigPR, error)
	GetFile(ctx context.Context, ref, path string) ([]byte, error)
	PutFile(ctx context.Context, branch, path string, content []byte, message string) error
	Comment(ctx context.Context, number int, body string) error
	PRByBranch(ctx context.Context, branch string) (gh.ConfigItem, bool, error)
	PullRequest(ctx context.Context, number int) (gh.PullRequest, error)
	LatestHumanComment(ctx context.Context, number int) (login, body string, found bool, err error)
	ReopenPR(ctx context.Context, number int, comment string) error
}

// KilledLabel on a config PR closed without merging kills its candidate,
// with the latest comment by a person as the reason (spec §8.7).
const KilledLabel = "killed"

// Job syncs, delivers and sweeps.
type Job struct {
	Store   *store.Store
	Repo    Repo
	Scanner *redact.Scanner
	Log     *slog.Logger
}

// Outcome is what happened to one seed or proposal.
type Outcome struct {
	Step        string `json:"step"` // sync | deliver | sweep
	CandidateID int64  `json:"candidate_id,omitempty"`
	RuleID      string `json:"rule_id,omitempty"`
	URL         string `json:"url,omitempty"`
	Action      string `json:"action"`
	Reason      string `json:"reason,omitempty"`
}

// Run sweeps proposals, so a retired seed's new PR can take it over, then
// syncs seeds from open PRs and delivers their cases.
func (j *Job) Run(ctx context.Context) ([]Outcome, error) {
	if j.Log == nil {
		j.Log = slog.Default()
	}
	var outs []Outcome
	for _, step := range []func(context.Context) ([]Outcome, error){j.Sweep, j.Sync, j.Deliver} {
		o, err := step(ctx)
		outs = append(outs, o...)
		if err != nil {
			return outs, err
		}
	}
	return outs, nil
}

// isRuleFile is a global rule file of exemplar-config.
func isRuleFile(p string) bool {
	ext := path.Ext(p)
	return strings.HasPrefix(p, "rules/") && (ext == ".yaml" || ext == ".yml")
}

// Sync records a seed for every rule file an open PR adds with status
// candidate. Files a PR only modifies are not seeds: the rule exists
// already. A PR from a fork is reported and skipped, since its branch is
// out of the config writer's reach.
func (j *Job) Sync(ctx context.Context) ([]Outcome, error) {
	prs, err := j.Repo.OpenPRs(ctx)
	if err != nil {
		return nil, fmt.Errorf("seeds: list open config PRs: %w", err)
	}
	var outs []Outcome
	for _, pr := range prs {
		for _, f := range pr.Added {
			if !isRuleFile(f) {
				continue
			}
			content, err := j.Repo.GetFile(ctx, pr.HeadSHA, f)
			if errors.Is(err, gh.ErrNotFound) {
				continue // pushed over since the listing
			}
			if err != nil {
				return outs, fmt.Errorf("seeds: read %s of %s: %w", f, pr.URL, err)
			}
			var rule schema.Rule
			if err := schema.Decode(schema.DocRule, content, &rule); err != nil {
				continue // the PR's schema check reports it
			}
			if rule.Status != schema.StatusCandidate {
				continue
			}
			out := Outcome{Step: "sync", RuleID: rule.ID, URL: pr.URL}
			if pr.Fork {
				out.Action, out.Reason = "skipped", "the PR comes from a fork; open it from a branch of exemplar-config"
				outs = append(outs, out)
				continue
			}
			author := pr.Author
			if pr.Bot {
				author = "" // the app's own PR (propose_rule recorded the person)
			}
			id, ok, err := Upsert(ctx, j.Store, rule, content, f, pr.URL, pr.HeadRef, author)
			if err != nil {
				return outs, err
			}
			out.CandidateID, out.Action = id, "recorded"
			if !ok {
				out.Action, out.Reason = "skipped", "the rule is a seed killed while this PR is open, or open on another PR"
			}
			outs = append(outs, out)
		}
	}
	return outs, nil
}

// appPR is a PR the config writer opened, from one of its own branches
// (exemplar/...), where it may commit; a person's PR gets a comment instead.
func appPR(pr gh.PullRequest, branch string) bool {
	return strings.EqualFold(pr.AuthorType, "Bot") && strings.HasPrefix(branch, "exemplar/")
}

// Deliver puts each open seed's generated cases on its PR, once. When the
// PR already has evals/<rule>/cases.yaml the owner's cases are kept and
// nothing is written.
func (j *Job) Deliver(ctx context.Context) ([]Outcome, error) {
	rows, err := j.Store.SeedsToDeliver(ctx)
	if err != nil {
		return nil, fmt.Errorf("seeds: list seeds to deliver: %w", err)
	}
	var outs []Outcome
	for _, r := range rows {
		ruleID, branch, url := deref(r.RuleID), deref(r.ConfigPrBranch), deref(r.ConfigPrUrl)
		out := Outcome{Step: "deliver", CandidateID: r.ID, RuleID: ruleID, URL: url}
		item, found, err := j.Repo.PRByBranch(ctx, branch)
		if err != nil {
			return outs, fmt.Errorf("seeds: find PR for %s: %w", branch, err)
		}
		if !found || item.State != "open" {
			continue // the sweep settles it
		}
		cases, err := propose.LoadEvalCases(ctx, j.Store, r.ID)
		if err != nil {
			return outs, fmt.Errorf("seeds: %w", err)
		}
		files, err := propose.EvalFiles(ruleID, cases)
		if err != nil {
			out.Action, out.Reason = "skipped", err.Error()
			outs = append(outs, out)
			continue
		}
		paths := make([]string, 0, len(files))
		var all strings.Builder
		for p, c := range files {
			paths = append(paths, p)
			all.Write(c)
		}
		slices.Sort(paths)
		// Injected patches carry target-repo code into exemplar-config,
		// which more people can read: refuse anything that scans as a secret.
		if len(j.Scanner.Scan(all.String())) > 0 {
			out.Action, out.Reason = "skipped", "generated cases contain text that scans as a secret"
			outs = append(outs, out)
			continue
		}
		pr, err := j.Repo.PullRequest(ctx, item.Number)
		if err != nil {
			return outs, fmt.Errorf("seeds: read %s: %w", url, err)
		}
		casesPath := "evals/" + ruleID + "/cases.yaml"
		switch _, err := j.Repo.GetFile(ctx, branch, casesPath); {
		case err == nil:
			out.Action = "kept"
			out.Reason = "the PR has its own " + casesPath
		case !errors.Is(err, gh.ErrNotFound):
			return outs, fmt.Errorf("seeds: read %s on %s: %w", casesPath, branch, err)
		case appPR(pr, branch):
			for _, p := range paths {
				if err := j.Repo.PutFile(ctx, branch, p, files[p], "Add generated eval cases for "+ruleID); err != nil {
					return outs, fmt.Errorf("seeds: write %s: %w", p, err)
				}
			}
			out.Action = "committed"
		default:
			if err := j.Repo.Comment(ctx, item.Number, comment(ruleID, paths, files)); err != nil {
				return outs, fmt.Errorf("seeds: comment on %s: %w", url, err)
			}
			out.Action = "commented"
		}
		if err := j.Store.MarkSeedDelivered(ctx, r.ID); err != nil {
			return outs, fmt.Errorf("seeds: mark delivered: %w", err)
		}
		j.Log.Info("seed cases delivered", "candidate_id", r.ID, "rule_id", ruleID, "url", url, "action", out.Action)
		outs = append(outs, out)
	}
	return outs, nil
}

// comment lists the generated files for the owner to commit.
func comment(ruleID string, paths []string, files map[string][]byte) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Exemplar generated eval cases for `%s` (spec §8.8): negatives from merged PRs a CODEOWNER approved, "+
		"and injected violations as positives. Eval CI needs `evals/%s/cases.yaml` before this PR can merge; "+
		"commit these files, edited as you see fit, or your own.\n", ruleID, ruleID)
	for _, p := range paths {
		lang := "yaml"
		if strings.HasSuffix(p, ".diff") {
			lang = "diff"
		}
		fmt.Fprintf(&b, "\n<details><summary><code>%s</code></summary>\n\n```%s\n%s\n```\n\n</details>\n", p, lang, bytes.TrimRight(files[p], "\n"))
	}
	return b.String()
}

// Sweep settles proposals whose PR is no longer open (see the package
// comment). A proposal it cannot settle stays pr_open.
func (j *Job) Sweep(ctx context.Context) ([]Outcome, error) {
	rows, err := j.Store.OpenProposals(ctx)
	if err != nil {
		return nil, fmt.Errorf("seeds: list open proposals: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	base, _, err := j.Repo.DefaultBranch(ctx)
	if err != nil {
		return nil, fmt.Errorf("seeds: get default branch: %w", err)
	}
	var outs []Outcome
	for _, r := range rows {
		out := Outcome{Step: "sweep", CandidateID: r.ID, RuleID: deref(r.RuleID), URL: deref(r.ConfigPrUrl)}
		item, found, err := j.Repo.PRByBranch(ctx, deref(r.ConfigPrBranch))
		if err != nil {
			return outs, fmt.Errorf("seeds: find PR for %s: %w", deref(r.ConfigPrBranch), err)
		}
		if !found || item.State == "open" {
			continue
		}
		var n int64
		switch {
		case item.Merged:
			status, err := j.ruleStatus(ctx, base, deref(r.RulePath))
			if err != nil {
				return outs, err
			}
			if status != schema.StatusActive && status != schema.StatusShadow {
				continue // merged as a candidate, or moved: nothing runs yet
			}
			if n, err = j.Store.MarkCandidatePromoted(ctx, r.ID); err != nil {
				return outs, fmt.Errorf("seeds: mark promoted: %w", err)
			}
			out.Action, out.Reason = "promoted", "merged as "+status
		default:
			if n, err = j.closed(ctx, r, item, &out); err != nil {
				return outs, err
			}
		}
		if n == 0 {
			continue
		}
		j.Log.Info("proposal settled", "candidate_id", r.ID, "rule_id", out.RuleID, "action", out.Action)
		outs = append(outs, out)
	}
	return outs, nil
}

// closed settles a proposal whose PR closed without merging.
func (j *Job) closed(ctx context.Context, r queries.OpenProposalsRow, item gh.ConfigItem, out *Outcome) (int64, error) {
	pr, err := j.Repo.PullRequest(ctx, item.Number)
	if err != nil {
		return 0, fmt.Errorf("seeds: read %s: %w", item.URL, err)
	}
	if slices.ContainsFunc(pr.Labels, func(l string) bool { return strings.EqualFold(l, KilledLabel) }) {
		login, body, found, err := j.Repo.LatestHumanComment(ctx, item.Number)
		if err != nil {
			return 0, fmt.Errorf("seeds: comments on %s: %w", item.URL, err)
		}
		if found {
			reason := strings.TrimSpace(body)
			if rs := []rune(reason); len(rs) > 1000 {
				reason = string(rs[:1000])
			}
			reason = fmt.Sprintf("%s (killed by @%s on %s)", reason, login, item.URL)
			n, err := j.Store.KillCandidate(ctx, queries.KillCandidateParams{ID: r.ID, KillReason: &reason})
			if err != nil {
				return 0, fmt.Errorf("seeds: kill: %w", err)
			}
			out.Action, out.Reason = "killed", reason
			return n, nil
		}
	}
	if r.SourceKind == "owner_seed" {
		n, err := j.Store.RetireSeed(ctx, r.ID)
		if err != nil {
			return 0, fmt.Errorf("seeds: retire: %w", err)
		}
		out.Action, out.Reason = "retired", "the PR closed without merging"
		return n, nil
	}
	msg := "Exemplar reopened this candidate PR: closing it without merging needs the `" + KilledLabel +
		"` label and a comment saying why. The reason teaches the extractor what not to propose, and the " +
		"candidate keeps its place in the owner's open-PR limit until it is merged or killed. Add both, then close it again."
	switch err := j.Repo.ReopenPR(ctx, item.Number, msg); {
	case errors.Is(err, gh.ErrNotFound):
		// A deleted branch cannot be reopened; kill it from the web UI.
		out.Action, out.Reason = "skipped", "closed without a reason and cannot be reopened: kill the candidate from the web UI"
		return 1, nil
	case err != nil:
		return 0, fmt.Errorf("seeds: reopen %s: %w", item.URL, err)
	}
	out.Action, out.Reason = "reopened", "closed without the "+KilledLabel+" label and a reason"
	return 1, nil
}

// ruleStatus is the status of the rule file at path on ref, "" if absent
// or unreadable as a rule.
func (j *Job) ruleStatus(ctx context.Context, ref, p string) (string, error) {
	content, err := j.Repo.GetFile(ctx, ref, p)
	if errors.Is(err, gh.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("seeds: read %s: %w", p, err)
	}
	var rule schema.Rule
	if schema.Decode(schema.DocRule, content, &rule) == nil {
		return rule.Status, nil
	}
	return "", nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
