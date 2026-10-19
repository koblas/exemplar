package evalgen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/koblas/exemplar/internal/scope"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
)

// Config configures a Generator.
type Config struct {
	// Candidates is how many candidates are processed per run.
	Candidates int
	// MaxNegatives per candidate (default 10).
	MaxNegatives int
	// MaxInjected per candidate (default 3); 0 with a nil Injector skips
	// injection.
	MaxInjected int
	// Eligible re-checks a source repo against the current exemplar-config
	// (repo opt-in and denylist): a repo can leave eligibility after it was
	// mined, and nothing from it may then reach a model. Required.
	Eligible func(repo string) bool
	Logger   *slog.Logger
}

// Generator writes candidate_eval_cases.
type Generator struct {
	cfg      Config
	store    *store.Store
	injector *Injector // nil: no injected cases
	log      *slog.Logger
}

// errConfig is a Generator configured without a required piece.
var errConfig = errors.New("evalgen: invalid config")

// New returns a Generator. A nil injector generates only history cases.
func New(cfg Config, st *store.Store, inj *Injector) (*Generator, error) {
	if cfg.Eligible == nil {
		return nil, fmt.Errorf("%w: an eligibility check is required", errConfig)
	}
	if cfg.Candidates <= 0 {
		cfg.Candidates = 50
	}
	if cfg.MaxNegatives <= 0 {
		cfg.MaxNegatives = 10
	}
	if cfg.MaxInjected <= 0 {
		cfg.MaxInjected = 3
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Generator{cfg: cfg, store: st, injector: inj, log: cfg.Logger}, nil
}

// Report is what one candidate got.
type Report struct {
	CandidateID int64    `json:"candidate_id"`
	Positives   int      `json:"positives"`
	Negatives   int      `json:"negatives"`
	Injected    int      `json:"injected"`
	Rejected    []string `json:"rejected_injections,omitempty"`
	Skipped     string   `json:"skipped,omitempty"`
}

// Run generates cases for new candidates that have none.
func (g *Generator) Run(ctx context.Context) ([]Report, error) {
	cands, err := g.store.CandidatesNeedingEvals(ctx, int32(g.cfg.Candidates))
	if err != nil {
		return nil, fmt.Errorf("list candidates needing evals: %w", err)
	}
	var out []Report
	for _, c := range cands {
		rep, err := g.one(ctx, c)
		if err != nil {
			return out, err
		}
		out = append(out, rep)
	}
	return out, nil
}

func (g *Generator) one(ctx context.Context, c queries.CandidatesNeedingEvalsRow) (Report, error) {
	rep := Report{CandidateID: c.ID}
	rows, err := g.store.CandidatePositiveSources(ctx, c.ID)
	if err != nil {
		return rep, fmt.Errorf("load positive sources: %w", err)
	}
	var sources []PositiveSource
	repoSet := map[string]bool{}
	for _, r := range rows {
		if !g.cfg.Eligible(r.Repo) {
			continue
		}
		repoSet[r.Repo] = true
		sources = append(sources, PositiveSource{
			CommentID: r.ID, Repo: r.Repo, PR: int(r.PrNumber), Path: r.Path, CommitSHA: deref(r.CommitSha),
			StartLine: int(derefInt(r.StartLine)), EndLine: int(derefInt(r.EndLine)), CommentURL: r.CommentUrl,
		})
	}
	cases := Positives(sources)
	rep.Positives = len(cases)
	if len(cases) == 0 {
		// Without a positive there is nothing to measure recall against.
		rep.Skipped = "no source comment in an eligible repo records its commit and lines"
		return rep, nil
	}

	pats, err := scope.CompilePatterns(filePatterns(c.Ext))
	if err != nil {
		return rep, fmt.Errorf("compile patterns: %w", err)
	}
	repos := make([]string, 0, len(repoSet))
	for r := range repoSet {
		repos = append(repos, r)
	}
	prRows, err := g.store.NegativePRs(ctx, queries.NegativePRsParams{Repos: repos, CandidateID: c.ID, Lim: int32(g.cfg.MaxNegatives * 5)})
	if err != nil {
		return rep, fmt.Errorf("list negative PRs: %w", err)
	}
	prs := make([]NegativePR, len(prRows))
	for i, p := range prRows {
		prs[i] = NegativePR{Repo: p.Repo, PR: int(p.PrNumber), HeadSHA: p.HeadSha, BaseSHA: p.BaseSha, Files: p.Files}
	}
	negatives, targets := Negatives(prs, pats, g.cfg.MaxNegatives)
	rep.Negatives = len(negatives)
	cases = append(cases, negatives...)

	if g.injector != nil {
		rule := Rule{Title: c.Title, Description: c.Description}
		for _, n := range negatives {
			if rep.Injected >= g.cfg.MaxInjected {
				break
			}
			inj, err := g.injector.Inject(ctx, rule, n, targets[n.ID])
			if errors.Is(err, ErrRejected) {
				rep.Rejected = append(rep.Rejected, n.ID+": "+err.Error())
				continue
			}
			if err != nil {
				return rep, err
			}
			cases = append(cases, *inj)
			rep.Injected++
		}
	}
	MarkSpotChecks(cases)

	err = g.store.InTx(ctx, func(_ pgx.Tx, q *queries.Queries) error {
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
				CandidateID: c.ID, CaseID: cs.ID, Kind: cs.Kind, Name: cs.Name, Ref: cs.Ref,
				Patch: patch, Expect: expect, SpotCheck: cs.SpotCheck, Paths: append([]string{}, cs.Paths...),
			}); err != nil {
				return fmt.Errorf("insert eval case: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return rep, fmt.Errorf("store eval cases: %w", err)
	}
	g.log.Info("eval cases generated", "candidate_id", c.ID, "positives", rep.Positives, "negatives", rep.Negatives, "injected", rep.Injected, "rejected", len(rep.Rejected))
	return rep, nil
}

// filePatterns matches propose's drafted scope for the cluster partition.
func filePatterns(ext string) []string {
	if ext == "" || ext == "*" {
		return []string{"**/*"}
	}
	return []string{"**/*" + ext}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func derefInt(n *int32) int32 {
	if n == nil {
		return 0
	}
	return *n
}
