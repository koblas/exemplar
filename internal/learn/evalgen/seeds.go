package evalgen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/koblas/exemplar/internal/learn/seeds"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/scope"
	"github.com/koblas/exemplar/internal/store/queries"
)

// RunSeeds generates cases for open owner-written seeds (spec §8.1): a seed
// has no source comments, so its negatives are recent CODEOWNER-approved
// PRs in eligible repos touching its file_patterns, and its positives are
// violations injected into them. The cases replace any earlier ones, and
// the seed is not tried again until its rule or PR changes; the propose
// job delivers them to the seed's PR.
func (g *Generator) RunSeeds(ctx context.Context) ([]Report, error) {
	rows, err := g.store.SeedsNeedingEvals(ctx, int32(g.cfg.Candidates))
	if err != nil {
		return nil, fmt.Errorf("list seeds needing evals: %w", err)
	}
	var out []Report
	for _, r := range rows {
		rep, err := g.seed(ctx, r)
		if err != nil {
			return out, err
		}
		out = append(out, rep)
	}
	return out, nil
}

func (g *Generator) seed(ctx context.Context, row queries.SeedsNeedingEvalsRow) (Report, error) {
	rep := Report{CandidateID: row.ID}
	var cases []Case
	if rule, pats, err := parseSeed(deref(row.RuleYaml)); err != nil {
		// The PR's schema check reports it; tried again when the rule changes.
		rep.Skipped = "the rule is not valid: " + err.Error()
	} else if cases, err = g.seedCases(ctx, &rep, rule, pats); err != nil {
		return rep, err
	}
	err := g.store.InTx(ctx, func(_ pgx.Tx, q *queries.Queries) error {
		if err := q.DeleteCandidateEvalCases(ctx, row.ID); err != nil {
			return fmt.Errorf("delete eval cases: %w", err)
		}
		for _, cs := range cases {
			expect, err := json.Marshal(cs.Expect)
			if err != nil {
				return fmt.Errorf("encode json: %w", err)
			}
			var patch *string
			if cs.Patch != "" {
				patch = &cs.Patch
			}
			if err := q.InsertCandidateEvalCase(ctx, queries.InsertCandidateEvalCaseParams{
				CandidateID: row.ID, CaseID: cs.ID, Kind: cs.Kind, Name: cs.Name, Ref: cs.Ref,
				Patch: patch, Expect: expect, SpotCheck: cs.SpotCheck, Paths: append([]string{}, cs.Paths...),
			}); err != nil {
				return fmt.Errorf("insert eval case: %w", err)
			}
		}
		if err := q.MarkSeedEvals(ctx, row.ID); err != nil {
			return fmt.Errorf("mark seed evals: %w", err)
		}
		return nil
	})
	if err != nil {
		return rep, fmt.Errorf("store seed eval cases: %w", err)
	}
	g.log.Info("seed eval cases generated", "candidate_id", row.ID, "rule_id", deref(row.RuleID),
		"negatives", rep.Negatives, "injected", rep.Injected, "rejected", len(rep.Rejected), "skipped", rep.Skipped)
	return rep, nil
}

func parseSeed(yaml string) (schema.Rule, scope.Patterns, error) {
	var rule schema.Rule
	if err := schema.Decode(schema.DocRule, []byte(yaml), &rule); err != nil {
		return rule, scope.Patterns{}, fmt.Errorf("decode rule: %w", err)
	}
	pats, err := scope.CompilePatterns(rule.FilePatterns)
	if err != nil {
		return rule, pats, fmt.Errorf("file_patterns: %w", err)
	}
	return rule, pats, nil
}

func (g *Generator) seedCases(ctx context.Context, rep *Report, rule schema.Rule, pats scope.Patterns) ([]Case, error) {
	prRows, err := g.store.SeedNegativePRs(ctx, int32(g.cfg.MaxNegatives*20))
	if err != nil {
		return nil, fmt.Errorf("list seed negative PRs: %w", err)
	}
	var prs []NegativePR
	for _, p := range prRows {
		if g.cfg.Eligible(p.Repo) {
			prs = append(prs, NegativePR{Repo: p.Repo, PR: int(p.PrNumber), HeadSHA: p.HeadSha, BaseSHA: p.BaseSha, Files: p.Files})
		}
	}
	cases, targets := Negatives(prs, pats, g.cfg.MaxNegatives)
	rep.Negatives = len(cases)
	if len(cases) == 0 {
		rep.Skipped = "no recent CODEOWNER-approved PR in an eligible repo touches the rule's file_patterns"
		return nil, nil
	}
	if g.injector != nil {
		r := Rule{Title: seeds.Title(rule), Description: rule.Description}
		for _, n := range cases[:rep.Negatives] {
			if rep.Injected >= g.cfg.MaxInjected {
				break
			}
			inj, err := g.injector.Inject(ctx, r, n, targets[n.ID])
			if errors.Is(err, ErrRejected) {
				rep.Rejected = append(rep.Rejected, n.ID+": "+err.Error())
				continue
			}
			if err != nil {
				return nil, err
			}
			cases = append(cases, *inj)
			rep.Injected++
		}
	}
	MarkSpotChecks(cases)
	return cases, nil
}
