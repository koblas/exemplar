package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/go-github/v88/github"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/evaluate"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/store"
)

// What the dispatcher mints for each job kind: the tokens the job's own
// row says it needs, never more.

// TokenMinter mints installation tokens; *gh.App implements it.
type TokenMinter interface {
	Token(ctx context.Context, installationID int64, f gh.Flavor, repos []string) (*github.InstallationToken, error)
}

// maxTokenRepos is how many repositories GitHub lets one token name.
const maxTokenRepos = 500

// Granter builds each kind's grant.
type Granter struct {
	Minter TokenMinter
	Store  *store.Store
	// ConfigRepo is exemplar-config, the repo mode eval checks out.
	ConfigRepo string
}

func (g Granter) mint(ctx context.Context, out *Grant, inst int64, f gh.Flavor, repos ...string) error {
	tok, err := g.Minter.Token(ctx, inst, f, repos)
	if err != nil {
		return fmt.Errorf("worker: mint %s token: %w", f, err)
	}
	out.Tokens = append(out.Tokens, GrantToken{
		Repos: repos, Installation: inst, Flavor: f.String(), Token: tok.GetToken(), ExpiresAt: tok.GetExpiresAt().Time,
	})
	return nil
}

// installation is repo's installation from its webhook deliveries; 0 when
// none (the runner refuses the job, as it would without a claim).
func (g Granter) installation(ctx context.Context, repo string) (int64, error) {
	inst, err := g.Store.RepoInstallationID(ctx, &repo)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("worker: repo installation: %w", err)
	}
	return inst, nil
}

// Review grants a PR review a reader (and clone), gate and poster token
// for the PR repo.
func (g Granter) Review(ctx context.Context, a store.PRReviewArgs, _ *config.Snapshot) (Grant, error) {
	var out Grant
	if a.Repo == "" || a.InstallationID == 0 {
		return out, river.JobCancel(fmt.Errorf("%w: %+v", errBadJob, a))
	}
	for _, f := range []gh.Flavor{gh.FlavorWorker, gh.FlavorGate, gh.FlavorPoster} {
		if err := g.mint(ctx, &out, a.InstallationID, f, a.Repo); err != nil {
			return Grant{}, err
		}
	}
	return out, nil
}

// Evaluate grants an `exemplar review` evaluation a reader for its repo.
// Mode eval gets one for exemplar-config and, per installation, a
// read-only token over every eligible repo with a known installation: its
// cases may name any of them, and the runner checks each against the
// requester.
func (g Granter) Evaluate(ctx context.Context, a store.EvaluateArgs, snap *config.Snapshot) (Grant, error) {
	var out Grant
	id, err := uuid.Parse(a.ID)
	if err != nil {
		return out, river.JobCancel(fmt.Errorf("%w: %w", errBadJob, err))
	}
	row, err := g.Store.GetEvaluation(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, river.JobCancel(fmt.Errorf("worker: evaluation %w", errGone))
	}
	if err != nil {
		return out, fmt.Errorf("worker: get evaluation: %w", err)
	}
	if row.Mode != evaluate.ModeEval {
		if !eligible(snap, row.Repo) {
			return out, nil // the runner refuses it
		}
		return out, g.repoReader(ctx, &out, row.Repo)
	}
	if g.ConfigRepo == "" || !strings.EqualFold(row.Repo, g.ConfigRepo) {
		return out, nil
	}
	if err := g.repoReader(ctx, &out, row.Repo); err != nil {
		return Grant{}, err
	}
	installed, err := g.Store.InstalledRepos(ctx)
	if err != nil {
		return Grant{}, fmt.Errorf("worker: installed repos: %w", err)
	}
	byInst := map[int64][]string{}
	var order []int64
	for _, r := range installed {
		if strings.EqualFold(r.Repo, g.ConfigRepo) || !eligible(snap, r.Repo) {
			continue
		}
		if _, ok := byInst[r.InstallationID]; !ok {
			order = append(order, r.InstallationID)
		}
		byInst[r.InstallationID] = append(byInst[r.InstallationID], r.Repo)
	}
	for _, inst := range order {
		repos := byInst[inst]
		for start := 0; start < len(repos); start += maxTokenRepos {
			if err := g.mint(ctx, &out, inst, gh.FlavorEval, repos[start:min(start+maxTokenRepos, len(repos))]...); err != nil {
				return Grant{}, err
			}
		}
	}
	return out, nil
}

// Playground grants a playground run a reader for its PR repo.
func (g Granter) Playground(ctx context.Context, a store.PlaygroundArgs, _ *config.Snapshot) (Grant, error) {
	var out Grant
	run, err := g.Store.GetPlaygroundRun(ctx, a.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, river.JobCancel(fmt.Errorf("worker: playground run %w", errGone))
	}
	if err != nil {
		return out, fmt.Errorf("worker: get playground run: %w", err)
	}
	return out, g.repoReader(ctx, &out, run.Repo)
}

func eligible(snap *config.Snapshot, repo string) bool { return snap.CheckEligible(repo) == nil }

func (g Granter) repoReader(ctx context.Context, out *Grant, repo string) error {
	inst, err := g.installation(ctx, repo)
	if err != nil || inst == 0 {
		return err
	}
	return g.mint(ctx, out, inst, gh.FlavorWorker, repo)
}
