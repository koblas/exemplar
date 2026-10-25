package propose

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
)

// Repo is the part of exemplar-config the writer needs (gh.ConfigRepo).
type Repo interface {
	DefaultBranch(ctx context.Context) (name, sha string, err error)
	FindPR(ctx context.Context, branch string) (url string, found bool, err error)
	EnsureBranch(ctx context.Context, branch, sha string) error
	PutFile(ctx context.Context, branch, path string, content []byte, message string) error
	OpenDraftPR(ctx context.Context, branch, base, title, body string) (url string, err error)
}

// Config configures a Writer.
type Config struct {
	// MaxOpenPerOwner is the throttle on open candidate PRs per owner team
	// (default 3); the rest wait in candidates.
	MaxOpenPerOwner int
	// MaxPerRun caps PRs opened in one run.
	MaxPerRun int
	// Scan is how many top candidates are considered per run.
	Scan   int
	Logger *slog.Logger
}

// Writer opens candidate PRs.
type Writer struct {
	cfg     Config
	store   *store.Store
	repo    Repo // nil for a dry run
	scanner *redact.Scanner
	log     *slog.Logger
}

// New returns a Writer. A nil repo makes Run a dry run: drafts are built
// and validated, nothing is written anywhere.
func New(cfg Config, st *store.Store, repo Repo, sc *redact.Scanner) *Writer {
	if cfg.MaxOpenPerOwner <= 0 {
		cfg.MaxOpenPerOwner = 3
	}
	if cfg.MaxPerRun <= 0 {
		cfg.MaxPerRun = 10
	}
	if cfg.Scan <= 0 {
		cfg.Scan = 200
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Writer{cfg: cfg, store: st, repo: repo, scanner: sc, log: cfg.Logger}
}

// Outcome is what happened to one candidate.
type Outcome struct {
	CandidateID int64  `json:"candidate_id"`
	RuleID      string `json:"rule_id,omitempty"`
	Owner       string `json:"owner"`
	Action      string `json:"action"` // opened | recovered | drafted | throttled | skipped
	URL         string `json:"url,omitempty"`
	Reason      string `json:"reason,omitempty"`
	Draft       *Draft `json:"-"`
}

// Run proposes the top candidates, honouring the per-owner throttle.
func (w *Writer) Run(ctx context.Context) ([]Outcome, error) {
	rows, err := w.store.ProposableCandidates(ctx, int32(w.cfg.Scan))
	if err != nil {
		return nil, fmt.Errorf("list proposable candidates: %w", err)
	}
	openRows, err := w.store.OpenCandidatePRsByOwner(ctx)
	if err != nil {
		return nil, fmt.Errorf("list open candidate PRs: %w", err)
	}
	open := map[string]int{}
	for _, r := range openRows {
		open[r.Owner] = int(r.Open)
	}

	var base, baseSHA string
	if w.repo != nil {
		if base, baseSHA, err = w.repo.DefaultBranch(ctx); err != nil {
			return nil, fmt.Errorf("get default branch: %w", err)
		}
	}

	var outs []Outcome
	opened := 0
	for _, row := range rows {
		if opened >= w.cfg.MaxPerRun {
			break
		}
		out := Outcome{CandidateID: row.ID, Owner: row.Owner}
		if open[row.Owner] >= w.cfg.MaxOpenPerOwner {
			out.Action, out.Reason = "throttled", fmt.Sprintf("%s already has %d open candidate PRs", row.Owner, open[row.Owner])
			outs = append(outs, out)
			continue
		}
		c, err := w.load(ctx, row)
		if err != nil {
			return outs, err
		}
		d, err := BuildDraft(c, time.Now())
		if err != nil {
			out.Action, out.Reason = "skipped", err.Error()
			outs = append(outs, out)
			continue
		}
		out.RuleID, out.Draft = d.RuleID, d
		// exemplar-config is widely readable: refuse to write anything that
		// still scans as a secret.
		if m := append(w.scanner.Scan(string(d.Rule)), w.scanner.Scan(d.Title+"\n"+d.Body)...); len(m) > 0 {
			out.Action, out.Reason = "skipped", "draft contains text that scans as a secret"
			outs = append(outs, out)
			continue
		}
		if w.repo == nil {
			out.Action = "drafted"
			outs = append(outs, out)
			open[row.Owner]++
			opened++
			continue
		}
		url, recovered, err := w.publish(ctx, d, base, baseSHA)
		if err != nil {
			return outs, fmt.Errorf("propose: candidate %d: %w", row.ID, err)
		}
		n, err := w.store.MarkCandidateProposed(ctx, queries.MarkCandidateProposedParams{ID: row.ID, ConfigPrUrl: &url})
		if err != nil {
			return outs, fmt.Errorf("mark candidate proposed: %w", err)
		}
		out.URL = url
		out.Action = "opened"
		if recovered {
			out.Action = "recovered"
		}
		if n == 0 {
			out.Reason = "candidate changed state while proposing; PR left as is"
		}
		w.log.Info("candidate proposed", "candidate_id", row.ID, "rule_id", d.RuleID, "owner", row.Owner, "url", url, "action", out.Action)
		outs = append(outs, out)
		open[row.Owner]++
		opened++
	}
	return outs, nil
}

// publish writes the rule on the candidate's branch and opens the draft
// PR. If a PR from the branch already exists (a run that crashed before
// recording it), it is reused instead of opening another.
func (w *Writer) publish(ctx context.Context, d *Draft, base, baseSHA string) (string, bool, error) {
	if url, found, err := w.repo.FindPR(ctx, d.Branch); err != nil {
		return "", false, fmt.Errorf("find PR: %w", err)
	} else if found {
		return url, true, nil
	}
	if err := w.repo.EnsureBranch(ctx, d.Branch, baseSHA); err != nil {
		return "", false, fmt.Errorf("ensure branch: %w", err)
	}
	if err := w.repo.PutFile(ctx, d.Branch, d.Path, d.Rule, "Propose candidate rule "+d.RuleID); err != nil {
		return "", false, fmt.Errorf("put file: %w", err)
	}
	paths := make([]string, 0, len(d.Files))
	for p := range d.Files {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	for _, p := range paths {
		if err := w.repo.PutFile(ctx, d.Branch, p, d.Files[p], "Add generated eval cases for "+d.RuleID); err != nil {
			return "", false, fmt.Errorf("put file: %w", err)
		}
	}
	url, err := w.repo.OpenDraftPR(ctx, d.Branch, base, d.Title, d.Body)
	if err != nil {
		return "", false, fmt.Errorf("open draft PR: %w", err)
	}
	return url, false, nil
}

var errNoSources = errors.New("propose: candidate has no sources")

func (w *Writer) load(ctx context.Context, row queries.ProposableCandidatesRow) (Candidate, error) {
	c := Candidate{
		ID: row.ID, Ext: row.Ext, Title: row.Title, Description: row.Description,
		ClusterSize: int(row.ClusterSize), DistinctReviewers: int(row.DistinctReviewers),
		Score: float64(row.Score), Owner: row.Owner,
	}
	if err := json.Unmarshal(row.ScoreDetail, &c.ScoreDetail); err != nil {
		return c, fmt.Errorf("propose: candidate %d score_detail: %w", row.ID, err)
	}
	srcs, err := w.store.CandidateSourceDetails(ctx, row.ID)
	if err != nil {
		return c, fmt.Errorf("load candidate sources: %w", err)
	}
	for _, s := range srcs {
		c.Sources = append(c.Sources, Source{
			CommentURL: s.CommentUrl, Repo: s.Repo, Path: s.Path, DocURLs: s.DocUrls, Kind: s.Source,
			Category: deref(s.Category), PreferredApproach: deref(s.PreferredApproach),
		})
	}
	incs, err := w.store.CandidateIncidents(ctx, row.ID)
	if err != nil {
		return c, fmt.Errorf("load candidate incidents: %w", err)
	}
	for _, i := range incs {
		c.Incidents = append(c.Incidents, Incident{Repo: i.Repo, Path: i.Path, Kind: i.Kind, Ref: i.Ref})
	}
	cases, err := w.store.CandidateEvalCases(ctx, row.ID)
	if err != nil {
		return c, fmt.Errorf("load candidate eval cases: %w", err)
	}
	for _, e := range cases {
		ec := EvalCase{ID: e.CaseID, Kind: e.Kind, Name: e.Name, Ref: e.Ref, Patch: deref(e.Patch), SpotCheck: e.SpotCheck, Paths: e.Paths}
		if err := json.Unmarshal(e.Expect, &ec.Expect); err != nil {
			return c, fmt.Errorf("propose: candidate %d case %s: %w", row.ID, e.CaseID, err)
		}
		c.EvalCases = append(c.EvalCases, ec)
	}
	switch cc, err := w.store.LatestAcceptedCompile(ctx, row.ID); {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return c, fmt.Errorf("load latest compile: %w", err)
	default:
		c.Compiled = &Compiled{
			AttemptID: cc.ID, Engine: deref(cc.Engine), Rule: deref(cc.RuleBody), Cases: int(cc.Cases),
			Precision: float64(deref32(cc.Precision)), Recall: float64(deref32(cc.Recall)),
			Baseline: cc.Baseline, BaselinePrecision: float64(cc.BaselinePrecision), BaselineRecall: float64(cc.BaselineRecall),
		}
	}
	if len(c.Sources) == 0 {
		return c, errNoSources
	}
	return c, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func deref32(f *float32) float32 {
	if f == nil {
		return 0
	}
	return *f
}
