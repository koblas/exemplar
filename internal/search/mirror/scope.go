package mirror

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/koblas/exemplar/internal/config"
)

// ErrForbidden is a request for context the PR may not see.
var ErrForbidden = errors.New("mirror: not allowed")

// InScope returns the repos to mirror: every eligible, opted-in repo and
// its context.extra_repos, minus anything denylisted. Sorted.
func InScope(snap *config.Snapshot) []string {
	seen := map[string]bool{}
	for repo, rc := range snap.Repos {
		if snap.CheckEligible(repo) != nil {
			continue
		}
		seen[strings.ToLower(repo)] = true
		if rc.Context != nil {
			for _, x := range rc.Context.ExtraRepos {
				if !snap.Denylisted(x) {
					seen[strings.ToLower(x)] = true
				}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	slices.Sort(out)
	return out
}

// Allowed checks that a review of prRepo may read repo: prRepo must be
// eligible, and repo must be prRepo itself or one of its extra_repos that
// is not denylisted. Visibility tiers are checked separately (SameTier),
// since they come from the mirrors.
func Allowed(snap *config.Snapshot, prRepo, repo string) error {
	if err := snap.CheckEligible(prRepo); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrForbidden, prRepo, err)
	}
	if strings.EqualFold(prRepo, repo) {
		return nil
	}
	rc := snap.Repos[prRepo]
	if rc.Context == nil || !slices.ContainsFunc(rc.Context.ExtraRepos, func(x string) bool { return strings.EqualFold(x, repo) }) {
		return fmt.Errorf("%w: %s is not in %s's context.extra_repos", ErrForbidden, repo, prRepo)
	}
	if snap.Denylisted(repo) {
		return fmt.Errorf("%w: %s is denylisted", ErrForbidden, repo)
	}
	return nil
}

// SameTier reports whether two GitHub visibilities share a tier: public,
// or private/internal. Unknown values share no tier.
func SameTier(a, b string) bool {
	tier := func(v string) string {
		switch v {
		case "public":
			return "public"
		case "private", "internal":
			return "private"
		}
		return ""
	}
	return tier(a) != "" && tier(a) == tier(b)
}
