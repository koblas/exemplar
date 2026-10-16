package compile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"

	"github.com/koblas/exemplar/internal/compiled"
	"github.com/koblas/exemplar/internal/learn/propose"
	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
)

// Attempt statuses (compile_attempts.status).
const (
	StatusAccepted = "accepted"
	StatusRejected = "rejected"
	StatusInvalid  = "invalid"
	StatusError    = "error"
)

// RecallRatio is how much of the exemplar's recall a compile must keep.
const RecallRatio = 0.9

// Thresholds is global.yaml eval_thresholds, the bar until exemplar
// scores exist.
type Thresholds struct {
	MinCases  int
	Precision float64
	Recall    float64
}

// Config configures a Job.
type Config struct {
	Thresholds Thresholds
	// Scratch holds materialized cases; each attempt uses and removes a
	// subdirectory.
	Scratch string
	// Sandbox names the wrapper the engine runs under, for the record
	// ("none" when unsandboxed).
	Sandbox string
	// Eligible re-checks a case's repo against the current exemplar-config
	// eligibility rules: a repo denylisted since ingest is never read. Required.
	Eligible func(repo string) bool
	Logger   *slog.Logger
}

// Job compiles candidates.
type Job struct {
	cfg     Config
	st      *store.Store
	model   Model
	engine  Engine
	files   FileSource
	scanner *redact.Scanner
}

// New returns a Job.
func New(cfg Config, st *store.Store, m Model, eng Engine, files FileSource, sc *redact.Scanner) (*Job, error) {
	if st == nil || m == nil || eng == nil || files == nil || sc == nil {
		return nil, fmt.Errorf("%w: store, model, engine, files and scanner are required", errConfig)
	}
	if cfg.Scratch == "" {
		return nil, fmt.Errorf("%w: scratch directory is required", errConfig)
	}
	if cfg.Thresholds.MinCases < 1 || cfg.Thresholds.Precision <= 0 || cfg.Thresholds.Recall <= 0 {
		return nil, fmt.Errorf("%w: eval thresholds are required", errConfig)
	}
	if cfg.Eligible == nil {
		return nil, fmt.Errorf("%w: an eligibility check is required", errConfig)
	}
	if cfg.Sandbox == "" {
		cfg.Sandbox = "none"
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Job{cfg: cfg, st: st, model: m, engine: eng, files: files, scanner: sc}, nil
}

// Report summarizes a run.
type Report struct {
	Attempted int            `json:"attempted"`
	ByStatus  map[string]int `json:"by_status"`
	Skipped   int            `json:"skipped"` // language-agnostic or no examples
}

// Run attempts up to limit candidates.
func (j *Job) Run(ctx context.Context, limit int) (Report, error) {
	rep := Report{ByStatus: map[string]int{}}
	rows, err := j.st.CandidatesNeedingCompile(ctx, int32(limit))
	if err != nil {
		return rep, fmt.Errorf("compile: load candidates: %w", err)
	}
	for _, c := range rows {
		status, err := j.compileOne(ctx, c)
		if errors.Is(err, compiled.ErrEngineUnavailable) {
			// Misconfiguration, not the candidate's fault: record nothing.
			return rep, fmt.Errorf("compile: %w", err)
		}
		if err != nil {
			return rep, err
		}
		if status == "" {
			rep.Skipped++
			continue
		}
		rep.Attempted++
		rep.ByStatus[status]++
	}
	return rep, nil
}

// compileOne makes one attempt and records it. It returns "" when the
// candidate cannot be compiled at all (no language, no examples).
func (j *Job) compileOne(ctx context.Context, c queries.CandidatesNeedingCompileRow) (string, error) {
	lang, ok := LanguageFor(c.Ext)
	if !ok {
		return "", nil
	}
	ruleID := propose.RuleID(c.Title, c.ID)
	req := Request{Title: c.Title, Description: c.Description, Language: lang}
	exRows, err := j.st.CompileExamples(ctx, c.ID)
	if err != nil {
		return "", fmt.Errorf("compile: examples for %d: %w", c.ID, err)
	}
	for _, e := range exRows {
		hunk, _, err := j.scanner.Redact(deref(e.DiffHunk))
		if err != nil || hunk == "" {
			continue // a secret that cannot be redacted never reaches a model
		}
		req.Examples = append(req.Examples, Example{Path: e.Path, Hunk: hunk})
		if req.PreferredApproach == "" {
			req.PreferredApproach = deref(e.PreferredApproach)
		}
	}
	if len(req.Examples) == 0 {
		return "", nil
	}
	cases, err := j.cases(ctx, c.ID, lang)
	if err != nil {
		return "", err
	}

	a := queries.InsertCompileAttemptParams{
		CandidateID: c.ID, Baseline: "thresholds", Sandbox: j.cfg.Sandbox, Metrics: []byte("{}"),
		BaselinePrecision: float32(j.cfg.Thresholds.Precision), BaselineRecall: float32(j.cfg.Thresholds.Recall),
	}
	p, err := j.model.Propose(ctx, req)
	a.Model = p.Model
	if a.Model == "" {
		a.Model = "unknown"
	}
	switch {
	case err != nil:
		a.Status, a.Reason = StatusError, err.Error()
		return j.record(ctx, a)
	case p.Engine == "none":
		a.Status, a.Reason = StatusRejected, "model: no deterministic rule fits: "+p.Explanation
		return j.record(ctx, a)
	}
	a.Engine = &p.Engine
	body, err := Normalize(p.Engine, p.Rule, ruleID, c.Title, lang)
	if err != nil {
		a.Status, a.Reason = StatusInvalid, err.Error()
		return j.record(ctx, a)
	}
	a.RuleBody = &body
	if err := j.engine.Validate(ctx, p.Engine, body); err != nil {
		if errors.Is(err, compiled.ErrEngineUnavailable) {
			return "", fmt.Errorf("validate rule: %w", err)
		}
		a.Status, a.Reason = StatusInvalid, "engine validation: "+err.Error()
		return j.record(ctx, a)
	}

	scratch, err := os.MkdirTemp(j.cfg.Scratch, fmt.Sprintf("candidate-%d-", c.ID))
	if err != nil {
		return "", fmt.Errorf("create scratch dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	score, err := Evaluate(ctx, j.engine, j.files, scratch, p.Engine, body, cases)
	if err != nil {
		if errors.Is(err, compiled.ErrEngineUnavailable) {
			return "", err
		}
		a.Status, a.Reason = StatusError, "run: "+err.Error()
		return j.record(ctx, a)
	}
	prec, rec := score.Precision(), score.Recall()
	a.Cases, a.TruePos, a.FalsePos, a.FalseNeg = int32(score.Cases), int32(score.TP), int32(score.FP), int32(score.FN)
	a.Precision, a.Recall = f32(prec), f32(rec)
	if a.Metrics, err = json.Marshal(map[string]any{"outcomes": score.Outcomes, "explanation": p.Explanation, "prompt": Version}); err != nil {
		return "", fmt.Errorf("encode compile metrics: %w", err)
	}
	a.Status, a.Reason = j.decide(score)
	return j.record(ctx, a)
}

// decide applies the acceptance bar. With the thresholds baseline the
// thresholds are the bar itself; against exemplar scores, recall may be
// RecallRatio of the exemplar's.
func (j *Job) decide(s Score) (string, string) {
	t := j.cfg.Thresholds
	switch {
	case s.Cases < t.MinCases:
		return StatusRejected, fmt.Sprintf("%d scored cases, below the minimum %d", s.Cases, t.MinCases)
	case s.TP+s.FN == 0:
		return StatusRejected, "no scorable positive cases"
	case s.Precision() < t.Precision:
		return StatusRejected, fmt.Sprintf("precision %.2f below %.2f", s.Precision(), t.Precision)
	case s.Recall() < t.Recall:
		return StatusRejected, fmt.Sprintf("recall %.2f below %.2f", s.Recall(), t.Recall)
	}
	return StatusAccepted, fmt.Sprintf("precision %.2f, recall %.2f over %d cases", s.Precision(), s.Recall(), s.Cases)
}

func (j *Job) record(ctx context.Context, a queries.InsertCompileAttemptParams) (string, error) {
	if len(a.Reason) > 2000 {
		a.Reason = a.Reason[:2000]
	}
	id, err := j.st.InsertCompileAttempt(ctx, a)
	if err != nil {
		return "", fmt.Errorf("compile: record attempt for %d: %w", a.CandidateID, err)
	}
	j.cfg.Logger.Info("compile attempt", "candidate", a.CandidateID, "attempt", id, "status", a.Status, "reason", a.Reason)
	return a.Status, nil
}

// cases loads the candidate's eval cases with the files each should scan.
func (j *Job) cases(ctx context.Context, candidateID int64, lang Language) ([]Case, error) {
	rows, err := j.st.CandidateEvalCases(ctx, candidateID)
	if err != nil {
		return nil, fmt.Errorf("compile: eval cases for %d: %w", candidateID, err)
	}
	var out []Case
	for _, r := range rows {
		repo, sha, err := ParseRef(r.Ref)
		if err != nil || !j.cfg.Eligible(repo) {
			continue
		}
		exp, err := DecodeExpect(r.Expect)
		if err != nil {
			continue
		}
		c := Case{ID: r.CaseID, Kind: r.Kind, Repo: repo, SHA: sha, Patch: deref(r.Patch), Expect: exp}
		switch {
		case r.Kind == "negative" && len(r.Paths) > 0:
			c.Files = inLanguage(r.Paths, lang)
		case r.Kind == "negative": // cases from before paths were recorded
			files, err := j.st.PRFilesAtHead(ctx, queries.PRFilesAtHeadParams{Repo: repo, HeadSha: sha})
			if err != nil {
				continue // PR no longer in mined_prs
			}
			c.Files = inLanguage(files, lang)
		default:
			for _, e := range exp {
				if !slices.Contains(c.Files, e.Path) {
					c.Files = append(c.Files, e.Path)
				}
			}
		}
		out = append(out, c)
	}
	return out, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func f32(x float64) *float32 {
	v := float32(x)
	return &v
}
