// Package owneract carries out the web UI's owner actions.
// exemplar-api records each request with its signed-in actor; a worker
// job runs it here: it checks the actor may take the action, then edits
// exemplar-config through a PR (git stays the source of truth) and
// merges it where branch protection allows, or updates the database for
// a candidate.
//
//   - shadow_rule, kill_rule: a member of the rule's owning team (or the
//     platform team) moves a global rule to shadow or killed.
//   - kill_candidate: the candidate's owner (or the platform team) kills
//     it with a reason.
//   - kill_switch: the platform team, or a member of one of the repo's
//     auto_approve.approvable_owners, turns auto-approve off for a repo.
//     Turning it back on is a reviewed config PR, never one click.
//   - propose_rule: the person who ran a playground try saves its rule as
//     a draft config PR: a new rule file as a candidate (anyone), or the
//     edited file of an existing global rule (its owners or the platform
//     team). Draft PRs are never merged here; owners review them.
package owneract

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/store/queries"
)

// Actions.
const (
	ShadowRule    = "shadow_rule"
	KillRule      = "kill_rule"
	KillCandidate = "kill_candidate"
	KillSwitch    = "kill_switch"
	ProposeRule   = "propose_rule"
)

// Statuses (owner_actions.status).
const (
	StatusDone    = "done"
	StatusRefused = "refused"
	StatusFailed  = "failed"
)

// Repo writes exemplar-config (gh.ConfigRepo, config-writer token).
type Repo interface {
	DefaultBranch(ctx context.Context) (name, sha string, err error)
	FindPR(ctx context.Context, branch string) (string, bool, error)
	GetFile(ctx context.Context, ref, path string) ([]byte, error)
	EnsureBranch(ctx context.Context, branch, sha string) error
	PutFile(ctx context.Context, branch, path string, content []byte, message string) error
	OpenPR(ctx context.Context, branch, base, title, body string) (string, int, error)
	OpenDraftPR(ctx context.Context, branch, base, title, body string) (string, error)
	MergePR(ctx context.Context, number int, message string) error
}

// Store reads and finishes actions (store.Store).
type Store interface {
	GetOwnerAction(ctx context.Context, id int64) (queries.OwnerAction, error)
	FinishOwnerAction(ctx context.Context, arg queries.FinishOwnerActionParams) error
	GetCandidateOwner(ctx context.Context, id int64) (queries.GetCandidateOwnerRow, error)
	KillCandidate(ctx context.Context, arg queries.KillCandidateParams) (int64, error)
	GetPlaygroundRun(ctx context.Context, id int64) (queries.PlaygroundRun, error)
}

// Runner runs actions.
type Runner struct {
	Store Store
	// Repo opens the config-writer client when an action needs one.
	Repo func(ctx context.Context) (Repo, error)
	// Member reports whether login is in an @org/team.
	Member func(ctx context.Context, team, login string) (bool, error)
	// PlatformTeam may take every action.
	PlatformTeam string
	Log          *slog.Logger
}

// Outcome is what an action did.
type Outcome struct {
	Status string `json:"status"`
	Result string `json:"result"`
	URL    string `json:"url,omitempty"`
}

// refused is an action that will not be taken; it is not retried.
type refused struct{ why string }

func (r refused) Error() string { return r.why }

// Run carries out action id and records the outcome. An error (a GitHub
// or database failure) leaves the action queued for a retry; Fail
// records the last one.
func (r Runner) Run(ctx context.Context, snap *config.Snapshot, id int64) (Outcome, error) {
	if r.Log == nil {
		r.Log = slog.Default()
	}
	a, err := r.Store.GetOwnerAction(ctx, id)
	if err != nil {
		return Outcome{}, fmt.Errorf("get owner action %d: %w", id, err)
	}
	if a.Status != "queued" {
		return Outcome{Status: a.Status, Result: "already finished"}, nil
	}
	var out Outcome
	switch a.Action {
	case ShadowRule, KillRule:
		out, err = r.rule(ctx, snap, a)
	case KillCandidate:
		out, err = r.candidate(ctx, a)
	case KillSwitch:
		out, err = r.killSwitch(ctx, snap, a)
	case ProposeRule:
		out, err = r.propose(ctx, snap, a)
	default:
		err = refused{"unknown action " + a.Action}
	}
	if no, ok := errors.AsType[refused](err); ok {
		out, err = Outcome{Status: StatusRefused, Result: no.why}, nil
	}
	if err != nil {
		return Outcome{}, err
	}
	return out, r.finish(ctx, id, out)
}

// Fail records an action that could not be carried out.
func (r Runner) Fail(ctx context.Context, id int64, cause error) error {
	return r.finish(ctx, id, Outcome{Status: StatusFailed, Result: cause.Error()})
}

func (r Runner) finish(ctx context.Context, id int64, o Outcome) error {
	p := queries.FinishOwnerActionParams{ID: id, Status: o.Status, Result: &o.Result}
	if o.URL != "" {
		p.PrUrl = &o.URL
	}
	if err := r.Store.FinishOwnerAction(ctx, p); err != nil {
		return fmt.Errorf("finish owner action %d: %w", id, err)
	}

	return nil
}

// allowed reports whether actor is one of owners (an @user or a member of
// an @org/team) or in the platform team. Only a GitHub login can match:
// an actor with a provider prefix ("oidc:", "svc:") has no GitHub identity.
func (r Runner) allowed(ctx context.Context, actor string, owners ...string) (bool, error) {
	if actor == "" || strings.Contains(actor, ":") {
		return false, nil
	}
	for _, o := range append(owners, r.PlatformTeam) {
		switch {
		case o == "":
		case strings.Contains(o, "/"):
			ok, err := r.Member(ctx, o, actor)
			if err != nil {
				return false, fmt.Errorf("check membership of %s: %w", o, err)
			}
			if ok {
				return true, nil
			}
		case strings.EqualFold(strings.TrimPrefix(o, "@"), actor):
			return true, nil
		}
	}
	return false, nil
}

var errStatusLine = errors.New("expected one top-level status line")

var statusRE = regexp.MustCompile(`(?m)^status:[ \t]*["']?(candidate|shadow|active|killed)["']?[ \t]*(#.*)?$`)

// withStatus rewrites a rule file's top-level status value, keeping the
// rest (quotes and comments included) byte for byte.
func withStatus(content []byte, to string) ([]byte, string, error) {
	locs := statusRE.FindAllSubmatchIndex(content, -1)
	if len(locs) != 1 {
		return nil, "", fmt.Errorf("%w, found %d", errStatusLine, len(locs))
	}
	from := string(content[locs[0][2]:locs[0][3]])
	out := append([]byte{}, content[:locs[0][2]]...)
	out = append(out, to...)
	return append(out, content[locs[0][3]:]...), from, nil
}

func (r Runner) rule(ctx context.Context, snap *config.Snapshot, a queries.OwnerAction) (Outcome, error) {
	var rule *config.Rule
	for i := range snap.Rules {
		if snap.Rules[i].ID == a.Target {
			rule = &snap.Rules[i]
		}
	}
	if rule == nil || rule.Scope != "global" {
		return Outcome{}, refused{"no global rule " + a.Target + " in exemplar-config (a repo's own rules change in that repo)"}
	}
	if ok, err := r.allowed(ctx, a.Actor, rule.Owner); err != nil {
		return Outcome{}, err
	} else if !ok {
		return Outcome{}, refused{fmt.Sprintf("only %s or the platform team can change %s", rule.Owner, rule.ID)}
	}
	to, verb := "shadow", "Shadow"
	if a.Action == KillRule {
		to, verb = "killed", "Kill"
	}
	edit := func(content []byte) ([]byte, error) {
		next, from, err := withStatus(content, to)
		switch {
		case err != nil:
			return nil, err
		case from == to:
			return nil, refused{fmt.Sprintf("%s is already %s", rule.ID, to)}
		case to == "shadow" && from != "active":
			return nil, refused{fmt.Sprintf("%s is %s, not active", rule.ID, from)}
		}
		return next, nil
	}
	title := fmt.Sprintf("%s %s (requested by @%s)", verb, rule.ID, a.Actor)
	return r.change(ctx, a, rule.Path, edit, title)
}

func (r Runner) candidate(ctx context.Context, a queries.OwnerAction) (Outcome, error) {
	id, err := strconv.ParseInt(a.Target, 10, 64)
	if err != nil {
		return Outcome{}, refused{"no candidate " + a.Target}
	}
	c, err := r.Store.GetCandidateOwner(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Outcome{}, refused{"no candidate " + a.Target}
	}
	if err != nil {
		return Outcome{}, fmt.Errorf("get candidate %d owner: %w", id, err)
	}
	if ok, err := r.allowed(ctx, a.Actor, c.Owner); err != nil {
		return Outcome{}, err
	} else if !ok {
		return Outcome{}, refused{fmt.Sprintf("only %s or the platform team can kill candidate %d", c.Owner, id)}
	}
	if strings.TrimSpace(a.Reason) == "" {
		return Outcome{}, refused{"killing a candidate needs a reason: it teaches the extractor"}
	}
	reason := fmt.Sprintf("%s (killed by @%s in the web UI)", a.Reason, a.Actor)
	n, err := r.Store.KillCandidate(ctx, queries.KillCandidateParams{ID: id, KillReason: &reason})
	if err != nil {
		return Outcome{}, fmt.Errorf("kill candidate %d: %w", id, err)
	}
	if n == 0 {
		return Outcome{}, refused{fmt.Sprintf("candidate %d is %s, not open", id, c.Status)}
	}
	return Outcome{Status: StatusDone, Result: fmt.Sprintf("candidate %d killed", id)}, nil
}

var (
	topKeyRE   = regexp.MustCompile(`(?m)^[A-Za-z_][A-Za-z0-9_]*:`)
	autoRE     = regexp.MustCompile(`(?m)^auto_approve:[ \t]*(#.*)?$`)
	enabledRE  = regexp.MustCompile(`(?m)^([ \t]+)enabled:[ \t]*(true|false)\b`)
	repoNameRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
)

// autoApproveOff sets auto_approve.enabled to false in a repo file.
func autoApproveOff(content []byte) ([]byte, error) {
	loc := autoRE.FindIndex(content)
	if loc == nil {
		return nil, refused{"auto-approve is not configured for the repo, so it is already off"}
	}
	end := len(content)
	if next := topKeyRE.FindIndex(content[loc[1]:]); next != nil {
		end = loc[1] + next[0]
	}
	block := content[loc[1]:end]
	m := enabledRE.FindSubmatchIndex(block)
	if m == nil || string(block[m[4]:m[5]]) == "false" {
		return nil, refused{"auto-approve is already off for the repo"}
	}
	out := append([]byte{}, content[:loc[1]+m[4]]...)
	out = append(out, "false"...)
	return append(out, content[loc[1]+m[5]:]...), nil
}

func (r Runner) killSwitch(ctx context.Context, snap *config.Snapshot, a queries.OwnerAction) (Outcome, error) {
	rc, ok := snap.Repos[a.Target]
	if !repoNameRE.MatchString(a.Target) || !ok {
		return Outcome{}, refused{a.Target + " is not opted in"}
	}
	var owners []string
	if rc.AutoApprove != nil {
		owners = rc.AutoApprove.ApprovableOwners
	}
	if ok, err := r.allowed(ctx, a.Actor, owners...); err != nil {
		return Outcome{}, err
	} else if !ok {
		return Outcome{}, refused{"only the platform team or the repo's approvable owners can turn auto-approve off"}
	}
	title := fmt.Sprintf("Turn auto-approve off for %s (requested by @%s)", a.Target, a.Actor)
	return r.change(ctx, a, "repos/"+a.Target+".yaml", autoApproveOff, title)
}

var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

// change opens the action's config PR with edit applied to path, and
// merges it if branch protection allows.
func (r Runner) change(ctx context.Context, a queries.OwnerAction, path string, edit func([]byte) ([]byte, error), title string) (Outcome, error) {
	repo, err := r.Repo(ctx)
	if err != nil {
		return Outcome{}, fmt.Errorf("open config repo: %w", err)
	}
	branch := fmt.Sprintf("exemplar/owner/%s/%s/%d", strings.ReplaceAll(a.Action, "_", "-"), strings.Trim(slugRE.ReplaceAllString(strings.ToLower(a.Target), "-"), "-"), a.ID)
	if url, found, err := repo.FindPR(ctx, branch); err != nil {
		return Outcome{}, fmt.Errorf("find PR for %s: %w", branch, err)
	} else if found {
		return Outcome{Status: StatusDone, Result: "the PR is open", URL: url}, nil
	}
	base, sha, err := repo.DefaultBranch(ctx)
	if err != nil {
		return Outcome{}, fmt.Errorf("get default branch: %w", err)
	}
	content, err := repo.GetFile(ctx, base, path)
	if err != nil {
		return Outcome{}, fmt.Errorf("get %s: %w", path, err)
	}
	next, err := edit(content)
	if err != nil {
		if _, ok := errors.AsType[refused](err); ok {
			return Outcome{}, err
		}
		return Outcome{}, refused{fmt.Sprintf("%s: %v", path, err)}
	}
	if err := repo.EnsureBranch(ctx, branch, sha); err != nil {
		return Outcome{}, fmt.Errorf("ensure branch %s: %w", branch, err)
	}
	if err := repo.PutFile(ctx, branch, path, next, title); err != nil {
		return Outcome{}, fmt.Errorf("put %s: %w", path, err)
	}
	body := fmt.Sprintf("Requested by @%s in the Exemplar web UI (owner action %d).\n", a.Actor, a.ID)
	if strings.TrimSpace(a.Reason) != "" {
		body += "\nReason: " + strings.TrimSpace(a.Reason) + "\n"
	}
	url, number, err := repo.OpenPR(ctx, branch, base, title, body)
	if err != nil {
		return Outcome{}, fmt.Errorf("open PR for %s: %w", branch, err)
	}
	if err := repo.MergePR(ctx, number, title); err != nil {
		r.Log.Warn("owner action PR opened but not merged", "action", a.ID, "url", url, "err", err)
		return Outcome{Status: StatusDone, Result: "PR opened; it waits for a review: " + err.Error(), URL: url}, nil
	}
	return Outcome{Status: StatusDone, Result: "merged", URL: url}, nil
}

// propose saves a playground try's rule as a draft config PR.
func (r Runner) propose(ctx context.Context, snap *config.Snapshot, a queries.OwnerAction) (Outcome, error) {
	id, err := strconv.ParseInt(a.Target, 10, 64)
	if err != nil {
		return Outcome{}, refused{"no playground run " + a.Target}
	}
	run, err := r.Store.GetPlaygroundRun(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Outcome{}, refused{"no playground run " + a.Target}
	}
	if err != nil {
		return Outcome{}, fmt.Errorf("get playground run %d: %w", id, err)
	}
	if !strings.EqualFold(run.Actor, a.Actor) {
		return Outcome{}, refused{"only the person who ran the try can save it"}
	}
	var rule schema.Rule
	if err := schema.Decode(schema.DocRule, []byte(run.RuleYaml), &rule); err != nil {
		return Outcome{}, refused{"the rule is not valid: " + err.Error()}
	}
	content := []byte(run.RuleYaml)
	if !bytes.HasSuffix(content, []byte("\n")) {
		content = append(content, '\n')
	}
	var existing *config.Rule
	for i := range snap.Rules {
		if snap.Rules[i].ID == rule.ID {
			existing = &snap.Rules[i]
		}
	}
	path := fmt.Sprintf("rules/%s/%s.yaml", strings.ToLower(rule.Category), rule.ID)
	var title string
	if existing != nil {
		if existing.Scope != "global" {
			return Outcome{}, refused{rule.ID + " is a repo's own rule; change it in that repo"}
		}
		if ok, err := r.allowed(ctx, a.Actor, existing.Owner); err != nil {
			return Outcome{}, err
		} else if !ok {
			return Outcome{}, refused{fmt.Sprintf("only %s or the platform team can change %s; give the draft a new id to propose it as a candidate", existing.Owner, rule.ID)}
		}
		path, title = existing.Path, fmt.Sprintf("Edit %s (from the playground, by @%s)", rule.ID, a.Actor)
	} else {
		// A new rule starts as a candidate, whatever the draft says.
		if content, _, err = withStatus(content, "candidate"); err != nil {
			return Outcome{}, refused{"the rule needs one top-level status line"}
		}
		title = fmt.Sprintf("Candidate rule: %s (from the playground, by @%s)", rule.ID, a.Actor)
	}
	repo, err := r.Repo(ctx)
	if err != nil {
		return Outcome{}, fmt.Errorf("open config repo: %w", err)
	}
	branch := fmt.Sprintf("exemplar/owner/propose/%s/%d", strings.Trim(slugRE.ReplaceAllString(rule.ID, "-"), "-"), a.ID)
	if url, found, err := repo.FindPR(ctx, branch); err != nil {
		return Outcome{}, fmt.Errorf("find PR for %s: %w", branch, err)
	} else if found {
		return Outcome{Status: StatusDone, Result: "the draft PR is open", URL: url}, nil
	}
	base, sha, err := repo.DefaultBranch(ctx)
	if err != nil {
		return Outcome{}, fmt.Errorf("get default branch: %w", err)
	}
	if existing != nil {
		cur, err := repo.GetFile(ctx, base, path)
		if err != nil {
			return Outcome{}, fmt.Errorf("get %s: %w", path, err)
		}
		if bytes.Equal(cur, content) {
			return Outcome{}, refused{"the draft is the same as " + path}
		}
	}
	if err := repo.EnsureBranch(ctx, branch, sha); err != nil {
		return Outcome{}, fmt.Errorf("ensure branch %s: %w", branch, err)
	}
	if err := repo.PutFile(ctx, branch, path, content, title); err != nil {
		return Outcome{}, fmt.Errorf("put %s: %w", path, err)
	}
	body := fmt.Sprintf("Requested by @%s in the Exemplar web UI (owner action %d), from playground run %d on %s#%d.\n",
		a.Actor, a.ID, run.ID, run.Repo, run.PrNumber)
	if strings.TrimSpace(a.Reason) != "" {
		body += "\nReason: " + strings.TrimSpace(a.Reason) + "\n"
	}
	body += "\nIt needs eval cases (`evals/" + rule.ID + "/cases.yaml`) and the owners' review before it can merge.\n"
	url, err := repo.OpenDraftPR(ctx, branch, base, title, body)
	if err != nil {
		return Outcome{}, fmt.Errorf("open draft PR for %s: %w", branch, err)
	}
	return Outcome{Status: StatusDone, Result: "draft PR opened", URL: url}, nil
}
