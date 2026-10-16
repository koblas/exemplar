package playground

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/redact"
)

// Target is the PR a draft rule is tried on.
type Target struct {
	Repo string
	PR   int
}

func (t Target) String() string { return fmt.Sprintf("%s#%d", t.Repo, t.PR) }

var (
	tryRE    = regexp.MustCompile(`(?m)^\s*/exemplar\s+try\b(.*)$`)
	targetRE = regexp.MustCompile(`^(?:https://github\.com/)?([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)(?:#|/pull/)([0-9]{1,9})/?$`)
)

// ErrUsage is a /exemplar try with no valid target.
var ErrUsage = errors.New("usage: /exemplar try org/repo#1234")

// ParseTry finds a /exemplar try command in a comment. found is false when
// there is none; a command with a bad target returns ErrUsage.
func ParseTry(body string) (t Target, found bool, err error) {
	m := tryRE.FindStringSubmatch(body)
	if m == nil {
		return Target{}, false, nil
	}
	t, err = ParseTarget(strings.TrimSpace(m[1]))
	return t, true, err
}

// ParseTarget reads org/repo#N or a PR URL.
func ParseTarget(s string) (Target, error) {
	m := targetRE.FindStringSubmatch(s)
	if m == nil {
		return Target{}, ErrUsage
	}
	n, _ := strconv.Atoi(m[2])
	if n < 1 {
		return Target{}, ErrUsage
	}
	return Target{Repo: m[1], PR: n}, nil
}

// GitHub is what a run needs from GitHub about the target repo.
type GitHub interface {
	// CanRead reports whether actor can read repo. Errors deny.
	CanRead(ctx context.Context, actor, repo string) (bool, error)
	PullRequest(ctx context.Context, repo string, number int) (gh.PullRequest, error)
	// CloneToken is a read-only token for repo ("" for local remotes).
	CloneToken(ctx context.Context, repo string) (string, error)
}

// Runner runs draft rules on target PRs.
type Runner struct {
	Engine  *engine.Engine
	Scanner *redact.Scanner
	GitHub  GitHub
	// WorkDir holds the target PR checkout for the duration of a run.
	WorkDir string
	// CloneURL returns repo's clone URL (default https://github.com/<repo>.git).
	CloneURL func(repo string) string
	// Caller is recorded on every model call: "playground" for PR
	// comments, "cli" for exemplar try.
	Caller string
}

// Request is one try.
type Request struct {
	Actor string
	// Snapshot is exemplar-config with the draft rules (DraftSnapshot or
	// RuleFileSnapshot): eligibility always from the base branch.
	Snapshot *config.Snapshot
	RuleIDs  []string
	Target   Target
}

// Outcome is a try's result. Reply is safe to post in exemplar-config.
type Outcome struct {
	Ran      bool
	Denied   bool
	Reply    string
	Findings []Finding
	CostUSD  float64
}

// Finding is a draft rule's finding, as the reply shows it.
type Finding struct {
	Rule       string
	Path       string
	Start, End int
	Message    string
	// Kept is false for findings the verifier or validation dropped;
	// Reason says why.
	Kept   bool
	Reason string
}

// Try runs the draft rules on the target PR. It never posts on the
// target PR; the caller posts Reply where the command was given.
func (r *Runner) Try(ctx context.Context, req Request) (Outcome, error) {
	t := req.Target
	// One answer for "ineligible", "cannot read" and "check failed", so a
	// reply never says whether a repo exists or why it was refused.
	deny := Outcome{Denied: true, Reply: fmt.Sprintf("no access to %s", t.Repo)}
	if req.Snapshot == nil || req.Snapshot.CheckEligible(t.Repo) != nil {
		return deny, nil
	}
	if ok, err := r.GitHub.CanRead(ctx, req.Actor, t.Repo); err != nil || !ok {
		return deny, nil
	}
	pr, err := r.GitHub.PullRequest(ctx, t.Repo, t.PR)
	if errors.Is(err, gh.ErrNotFound) {
		return Outcome{Reply: fmt.Sprintf("%s not found", t)}, nil
	}
	if err != nil {
		return Outcome{}, fmt.Errorf("playground: %s: %w", t, err)
	}
	tr, err := r.RunPR(ctx, req.Snapshot, req.RuleIDs, pr)
	if err != nil {
		return Outcome{}, err
	}
	switch {
	case tr.Ineligible:
		return deny, nil
	case tr.NoFiles:
		return Outcome{Reply: fmt.Sprintf("%s: %s match no reviewable file in %s.", ruleList(req.RuleIDs), plural(len(req.RuleIDs), "rule", "rules"), t)}, nil
	case tr.Incomplete != "":
		return Outcome{Reply: fmt.Sprintf("%s could not be reviewed: %s.", t, tr.Incomplete)}, nil
	}
	res := tr.Result
	out := Outcome{Ran: true}
	for _, run := range res.Runs {
		if slices.Contains(req.RuleIDs, run.RuleID) {
			out.CostUSD += run.CostUSD
		}
	}
	for _, f := range res.Findings {
		if !slices.Contains(req.RuleIDs, f.RuleID) {
			continue
		}
		kept := f.State != engine.StateDropped
		out.Findings = append(out.Findings, Finding{
			Rule: f.RuleID, Path: f.Anchor.Path, Start: f.Anchor.StartLine, End: f.Anchor.EndLine,
			Message: SafeMessage(r.Scanner, f.Message), Kept: kept, Reason: f.Suppressed,
		})
	}
	out.Reply = Format(t, pr.HeadSHA, req.RuleIDs, res, out)
	return out, nil
}

// Trial is the result of running rules alone on one PR.
type Trial struct {
	Result *engine.Result
	// NoFiles: the rules match no reviewable file in the PR.
	NoFiles bool
	// Incomplete is why the PR could not be reviewed.
	Incomplete string
	// Ineligible: the repo may not be reviewed under the snapshot.
	Ineligible bool
}

// RunPR checks pr out and runs only ruleIDs on it, with no other rule,
// no secrets lane and no deferred rules. It posts nothing. Callers check
// access and eligibility first; RunPR rechecks eligibility in Prepare.
func (r *Runner) RunPR(ctx context.Context, snap *config.Snapshot, ruleIDs []string, pr gh.PullRequest) (Trial, error) {
	t := Target{Repo: pr.Repo, PR: pr.Number}
	token, err := r.GitHub.CloneToken(ctx, pr.Repo)
	if err != nil {
		return Trial{}, fmt.Errorf("playground: token for %s: %w", pr.Repo, err)
	}
	if err := os.MkdirAll(r.WorkDir, 0o750); err != nil {
		return Trial{}, err
	}
	dir, err := os.MkdirTemp(r.WorkDir, "try-")
	if err != nil {
		return Trial{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	url := "https://github.com/" + pr.Repo + ".git"
	if r.CloneURL != nil {
		url = r.CloneURL(pr.Repo)
	}
	ws, err := engine.Checkout(ctx, filepath.Join(dir, "worktree"), engine.CheckoutOptions{
		CloneURL: url, BaseSHA: pr.BaseSHA, HeadSHA: pr.HeadSHA, Token: token,
	})
	if err != nil {
		return Trial{}, fmt.Errorf("playground: checkout %s: %w", t, err)
	}
	info := engine.PRInfo{Repo: pr.Repo, Number: pr.Number, Title: pr.Title, Body: pr.Body, Author: pr.Author,
		BaseSHA: pr.BaseSHA, HeadSHA: pr.HeadSHA}
	p, err := engine.Prepare(ctx, ws, snap, info, r.Scanner)
	if errors.Is(err, engine.ErrIneligible) {
		return Trial{Ineligible: true}, nil
	}
	if err != nil {
		return Trial{}, fmt.Errorf("playground: prepare %s: %w", t, err)
	}
	p.Scoped = slices.DeleteFunc(p.Scoped, func(s engine.ScopedRule) bool { return !slices.Contains(ruleIDs, s.Rule.ID) })
	p.Deferred = nil
	p.Secrets = nil
	if len(p.Scoped) == 0 {
		return Trial{NoFiles: true}, nil
	}
	if p.Incomplete != "" {
		return Trial{Incomplete: p.Incomplete}, nil
	}
	res, err := r.Engine.Review(ctx, p, engine.Options{Caller: r.Caller})
	if err != nil {
		return Trial{}, fmt.Errorf("playground: review %s: %w", t, err)
	}
	return Trial{Result: res}, nil
}

func ruleList(ids []string) string {
	q := make([]string, len(ids))
	for i, id := range ids {
		q[i] = "`" + id + "`"
	}
	return strings.Join(q, ", ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
