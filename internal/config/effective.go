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
	rc := s.Repos[repo]
	e := &Effective{
		ConfigVersion: s.Version,
		Repo:          repo,
		Global:        s.Global,
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
