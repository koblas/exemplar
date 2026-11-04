package config

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"

	"github.com/koblas/exemplar/internal/schema"
)

// Eligibility outcomes. A repo is reviewed only when it is opted in, not
// denylisted, and Exemplar is enabled globally.
var (
	ErrDisabledGlobally = errors.New("exemplar is disabled in global.yaml")
	ErrNotOptedIn       = errors.New("repo is not opted in")
	ErrRepoDisabled     = errors.New("repo kill switch is off")
	ErrDenylisted       = errors.New("repo is on the platform eligibility denylist")
)

// ErrShadowedRule is returned when a repo rule reuses a global rule id.
var ErrShadowedRule = errors.New("repo rule shadows a global rule")

// Effective is the configuration one review runs with.
type Effective struct {
	ConfigVersion string
	Repo          string
	Global        schema.GlobalConfig
	RepoConfig    schema.RepoConfig
	// Rules are the active and shadow rules that apply to the repo: global
	// rules minus disabled_global_rules, plus the repo's own rules. Killed
	// and candidate rules never run.
	Rules []Rule
}

// RoundsMax is the round cap for this repo: global.yaml's, or the repo's
// lower one.
func (e *Effective) RoundsMax() int {
	n := e.Global.RoundsMax()
	if r := e.RepoConfig.Rounds; r != nil && r.Max >= 1 && r.Max < n {
		n = r.Max
	}
	return n
}

// CheckEligible reports whether repo may be reviewed at all. Callers must
// check this before anything about the repo reaches a model provider.
func (s *Snapshot) CheckEligible(repo string) error {
	if !s.Global.Enabled {
		return ErrDisabledGlobally
	}
	for _, d := range s.Eligibility.Denylist {
		if strings.EqualFold(d.Repo, repo) {
			return fmt.Errorf("%w: %s", ErrDenylisted, d.Reason)
		}
	}
	rc, ok := s.Repos[repo]
	if !ok {
		return ErrNotOptedIn
	}
	if !rc.Enabled {
		return ErrRepoDisabled
	}
	return nil
}

// Effective builds the configuration for repo. repoRules come from
// LoadRepoRules on the PR's base tree. It returns the eligibility error if
// the repo may not be reviewed.
func (s *Snapshot) Effective(repo string, repoRules []Rule) (*Effective, error) {
	if err := s.CheckEligible(repo); err != nil {
		return nil, err
	}
	return s.effective(repo, s.Global, s.Repos[repo], repoRules)
}

func (s *Snapshot) effective(repo string, g schema.GlobalConfig, rc schema.RepoConfig, repoRules []Rule) (*Effective, error) {
	e := &Effective{
		ConfigVersion: s.Version,
		Repo:          repo,
		Global:        g,
		RepoConfig:    rc,
	}
	seen := map[string]bool{}
	for _, r := range s.Rules {
		if !runnable(r.Status) || slices.Contains(rc.DisabledGlobalRules, r.ID) {
			continue
		}
		e.Rules = append(e.Rules, r)
		seen[r.ID] = true
	}
	for _, r := range repoRules {
		if !runnable(r.Status) {
			continue
		}
		if seen[r.ID] {
			return nil, fmt.Errorf("%w: repo rule %s (%s) shadows a global rule with the same id", ErrShadowedRule, r.ID, r.Path)
		}
		e.Rules = append(e.Rules, r)
		seen[r.ID] = true
	}
	return e, nil
}

// The eval profile's fixed repository and owner. A synthetic case reads
// no real repository, so neither names one; the repo matches no
// compliance or config-repo name.
const (
	EvalRepo  = "exemplar-eval/synthetic"
	EvalOwner = "@exemplar-eval/owners"
)

// EvalProfile is the configuration a synthetic eval case runs with
// (spec §8.8): the organization's settings and rules with auto_approve on,
// and a repo config that posts and auto-approves every path for author.
// Policy inputs a synthetic workspace cannot supply are set to pass, so a
// case is approved or not on what the review computes. Eligibility is not
// checked: no repository is read. Callers must only use it for a
// workspace built from the case.
func (s *Snapshot) EvalProfile(author string, repoRules []Rule) (*Effective, error) {
	g := s.Global
	g.AutoApprove = true
	all := []string{"**"}
	rc := schema.RepoConfig{
		Enabled: true,
		AutoApprove: &schema.RepoAutoApprove{
			Enabled:          true,
			Paths:            all,
			AllowedAuthors:   []string{author},
			ApprovableOwners: []string{EvalOwner},
			BotAuthors:       []schema.BotAuthor{{Identity: author, Paths: all}},
		},
	}
	return s.effective(EvalRepo, g, rc, repoRules)
}

func runnable(status string) bool {
	return status == schema.StatusActive || status == schema.StatusShadow
}

// RepoRulesDir is where a target repo keeps its own rules.
const RepoRulesDir = ".exemplar/rules"

// LoadRepoRules reads .exemplar/rules/*.yaml from a target repo's base tree.
// A repo without the directory has no repo rules.
func LoadRepoRules(base fs.FS, repo string) ([]Rule, error) {
	entries, err := fs.ReadDir(base, RepoRulesDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", RepoRulesDir, err)
	}
	var (
		rules []Rule
		probs Problems
	)
	for _, e := range entries {
		if e.IsDir() || path.Ext(e.Name()) != ".yaml" {
			continue
		}
		p := path.Join(RepoRulesDir, e.Name())
		data, err := fs.ReadFile(base, p)
		if err != nil {
			probs.add(p, err)
			continue
		}
		r, err := parseRule(p, data, repo)
		if err == nil {
			err = checkReserved(r)
		}
		if err != nil {
			probs.add(p, err)
			continue
		}
		rules = append(rules, r)
	}
	if len(probs) > 0 {
		return nil, probs
	}
	return rules, nil
}

// Denylisted reports whether repo is on the platform eligibility denylist.
// Context-only repos (context.extra_repos) need not be opted in, but a
// denylisted repo is never mirrored or read.
func (s *Snapshot) Denylisted(repo string) bool {
	for _, d := range s.Eligibility.Denylist {
		if strings.EqualFold(d.Repo, repo) {
			return true
		}
	}
	return false
}
