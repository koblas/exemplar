// Package pathevents fills path_events: which
// paths were touched by a revert on a default branch, or by a change an
// incident links to. Rank boosts candidates on those paths, and the same
// table later backs path_history and merge_safety_score.
//
// Only references are stored: a PR number, commit SHA or incident id, the
// path and the time. No commit message, revert reason or incident text is
// kept. Inserts are idempotent, so every run may overlap the last.
package pathevents

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/koblas/exemplar/internal/exclude"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
)

// Event kinds (path_events.kind).
const (
	KindRevert   = "revert"
	KindIncident = "incident"
)

// Query runs a GraphQL query and decodes its data into out.
type Query func(ctx context.Context, query string, vars map[string]any, out any) error

// CommitFiles lists the paths a commit changed (REST; GraphQL has no file
// list for commits).
type CommitFiles func(ctx context.Context, owner, repo, sha string) ([]string, error)

// Eligibility is the platform check (config.Snapshot.CheckEligible).
type Eligibility interface{ CheckEligible(repo string) error }

// Config configures a Job.
type Config struct {
	// Backfill is how far back the first revert scan of a repo goes
	// (default 365 days; merge_safety looks back 180).
	Backfill time.Duration
	// Overlap is re-scanned on each run to catch commits whose dates
	// predate the last scan (rebases, late pushes). Default 7 days.
	Overlap time.Duration
	// PageSize is commits per GraphQL page (default 50).
	PageSize int
	Logger   *slog.Logger
	// Seeds, when set, also writes revert seeds for the extractor.
	Seeds *Seeds
	// Now is the clock (tests).
	Now func() time.Time
}

// API is GitHub access for one repo.
type API struct {
	Query       Query
	CommitFiles CommitFiles
	PRFiles     PRFiles
}

// Connect returns API for repo, authenticated with a token scoped to it.
type Connect func(ctx context.Context, repo string) (API, error)

// Job scans repos for reverts and loads incident links.
type Job struct {
	cfg     Config
	connect Connect
	st      *store.Store
	elig    Eligibility
}

// New returns a Job.
func New(cfg Config, connect Connect, st *store.Store, elig Eligibility) (*Job, error) {
	if connect == nil || st == nil || elig == nil {
		return nil, errors.New("pathevents: connect, store and eligibility are required")
	}
	if cfg.Backfill <= 0 {
		cfg.Backfill = 365 * 24 * time.Hour
	}
	if cfg.Overlap <= 0 {
		cfg.Overlap = 7 * 24 * time.Hour
	}
	if cfg.PageSize <= 0 || cfg.PageSize > 100 {
		cfg.PageSize = 50
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if s := cfg.Seeds; s != nil {
		if s.Pseudonyms == nil || s.Scanner == nil {
			return nil, errors.New("pathevents: seeds need a pseudonymizer and a secret scanner")
		}
		if s.MaxFiles <= 0 {
			s.MaxFiles = 5
		}
	}
	return &Job{cfg: cfg, connect: connect, st: st, elig: elig}, nil
}

// RevertStats summarize one repo's revert scan.
type RevertStats struct {
	Repo      string    `json:"repo"`
	Since     time.Time `json:"since"`
	Commits   int       `json:"commits"`
	Reverts   int       `json:"reverts"`
	Events    int       `json:"events"`
	Truncated int       `json:"truncated"` // revert PRs with more than 100 files
	Seeds     int       `json:"seeds"`     // seed rows written (new or existing)
	RateLimit bool      `json:"rate_limited,omitempty"`
}

var (
	trailerRE    = regexp.MustCompile(`(?m)^This reverts commit [0-9a-f]{7,40}\b`)
	revertPRBody = regexp.MustCompile(`(?m)^Reverts [\w.-]+/[\w.-]+#\d+`)
)

// isRevertTitle matches GitHub's generated `Revert "…"` titles, but not a
// revert of a revert, which re-lands the original change.
func isRevertTitle(s string) bool {
	return strings.HasPrefix(s, `Revert "`) && !strings.HasPrefix(s, `Revert "Revert `)
}

// classify decides whether a default-branch commit is a revert and returns
// the event ref: "#<pr>" when the revert landed through a PR, else the
// commit SHA. A trailer inside a PR's own commits (reverting a work-in-
// progress commit before merge) is not a revert of anything on the branch,
// so trailers count only on the PR's merge commit or on direct pushes.
func classify(c historyCommit) (ref string, pr *associatedPR, ok bool) {
	headline, _, _ := strings.Cut(c.Message, "\n")
	if len(c.AssociatedPullRequests.Nodes) > 0 {
		p := &c.AssociatedPullRequests.Nodes[0]
		if p.Merged {
			isMerge := p.MergeCommit != nil && p.MergeCommit.Oid == c.Oid
			if isRevertTitle(p.Title) || revertPRBody.MatchString(p.Body) ||
				(isMerge && (trailerRE.MatchString(c.Message) || isRevertTitle(headline))) {
				return fmt.Sprintf("#%d", p.Number), p, true
			}
			return "", nil, false
		}
	}
	if trailerRE.MatchString(c.Message) || isRevertTitle(headline) {
		return c.Oid, nil, true
	}
	return "", nil, false
}

// Reverts scans repo's default branch from its saved point (or the
// backfill window) to now.
func (j *Job) Reverts(ctx context.Context, repo string) (RevertStats, error) {
	stats := RevertStats{Repo: repo}
	if err := j.elig.CheckEligible(repo); err != nil {
		return stats, fmt.Errorf("pathevents: %s is not eligible: %w", repo, err)
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return stats, fmt.Errorf("pathevents: repo %q must be owner/name", repo)
	}
	api, err := j.connect(ctx, repo)
	if err != nil {
		return stats, err
	}
	start := j.cfg.Now()
	since := start.Add(-j.cfg.Backfill)
	switch ts, err := j.st.GetRevertsSince(ctx, repo); {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return stats, fmt.Errorf("pathevents: load cursor: %w", err)
	default:
		since = ts.Time.Add(-j.cfg.Overlap)
	}
	stats.Since = since

	var rules exclude.Rules
	if j.cfg.Seeds != nil {
		if rules, err = exclusions(ctx, api, owner, name); err != nil {
			return stats, err
		}
	}
	seen := map[string]bool{}
	var after *string
	for {
		var page historyPage
		err := api.Query(ctx, historyQuery, map[string]any{
			"owner": owner, "name": name, "first": j.cfg.PageSize, "after": after, "since": since.UTC().Format(time.RFC3339),
		}, &page)
		if errors.Is(err, gh.ErrRateLimited) {
			// Events found so far are stored; the cursor is not advanced.
			stats.RateLimit = true
			return stats, nil
		}
		if err != nil {
			return stats, fmt.Errorf("pathevents: %s history: %w", repo, err)
		}
		ref := page.Repository.DefaultBranchRef
		if ref == nil {
			return stats, fmt.Errorf("pathevents: %s has no default branch", repo)
		}
		hist := ref.Target.History
		var events []queries.InsertPathEventParams
		var links []queries.InsertPRLinkParams
		for _, c := range hist.Nodes {
			stats.Commits++
			evRef, pr, ok := classify(c)
			if !ok || seen[evRef] {
				continue
			}
			seen[evRef] = true
			stats.Reverts++
			var paths []string
			// The reverted PR, for counter-metrics.
			switch n, err := j.target(ctx, api, repo, owner, name, toRevert(evRef, c, pr)); {
			case errors.Is(err, gh.ErrRateLimited):
				stats.RateLimit = true
				return stats, j.insert(ctx, events, links)
			case err != nil:
				return stats, fmt.Errorf("pathevents: reverted PR of %s %s: %w", repo, evRef, err)
			case n > 0:
				links = append(links, queries.InsertPRLinkParams{Repo: repo, PrNumber: int32(n), Kind: KindRevert, Ref: evRef,
					At: pgtype.Timestamptz{Time: c.CommittedDate, Valid: true}})
			}
			if pr != nil {
				for _, f := range pr.Files.Nodes {
					paths = append(paths, f.Path)
				}
				if pr.Files.PageInfo.HasNextPage {
					stats.Truncated++
				}
			} else {
				if paths, err = api.CommitFiles(ctx, owner, name, c.Oid); err != nil {
					return stats, fmt.Errorf("pathevents: files of %s@%s: %w", repo, c.Oid, err)
				}
			}
			for _, p := range paths {
				events = append(events, queries.InsertPathEventParams{
					Repo: repo, Path: p, Kind: KindRevert, Ref: evRef, At: pgtype.Timestamptz{Time: c.CommittedDate, Valid: true},
				})
			}
			if j.cfg.Seeds != nil {
				n, err := j.seed(ctx, api, repo, rules, toRevert(evRef, c, pr))
				if errors.Is(err, gh.ErrRateLimited) {
					stats.RateLimit = true
					return stats, j.insert(ctx, events, links)
				}
				if err != nil {
					return stats, fmt.Errorf("pathevents: seeds for %s %s: %w", repo, evRef, err)
				}
				stats.Seeds += n
			}
		}
		if err := j.insert(ctx, events, links); err != nil {
			return stats, err
		}
		stats.Events += len(events)
		if !hist.PageInfo.HasNextPage || hist.PageInfo.EndCursor == nil {
			break
		}
		after = hist.PageInfo.EndCursor
	}
	if err := j.st.SaveRevertsSince(ctx, queries.SaveRevertsSinceParams{
		Repo: repo, RevertsSince: pgtype.Timestamptz{Time: start, Valid: true},
	}); err != nil {
		return stats, fmt.Errorf("pathevents: save cursor: %w", err)
	}
	return stats, nil
}

// toRevert collects what seeding needs from a classified commit.
func toRevert(ref string, c historyCommit, pr *associatedPR) revert {
	if pr != nil {
		return revert{Ref: ref, Title: pr.Title, Body: pr.Body, URL: pr.URL, Reverter: pr.Author.Login, At: c.CommittedDate}
	}
	r := revert{Ref: ref, Body: c.Message, URL: c.URL, At: c.CommittedDate}
	r.Title, _, _ = strings.Cut(c.Message, "\n")
	if c.Author.User != nil {
		r.Reverter = c.Author.User.Login
	}
	return r
}

// exclusions reads the default branch's .gitattributes and .exemplarignore,
// so seeds skip the paths review and ingest skip.
func exclusions(ctx context.Context, api API, owner, name string) (exclude.Rules, error) {
	var out struct {
		Repository *struct {
			Gitattributes  struct{ Text string } `json:"gitattributes"`
			Exemplarignore struct{ Text string } `json:"exemplarignore"`
		} `json:"repository"`
	}
	if err := api.Query(ctx, exclusionsQuery, map[string]any{"owner": owner, "name": name}, &out); err != nil {
		return exclude.Rules{}, fmt.Errorf("pathevents: read %s/%s exclusions: %w", owner, name, err)
	}
	if out.Repository == nil {
		return exclude.Rules{}, fmt.Errorf("pathevents: repo %s/%s not found", owner, name)
	}
	return exclude.Load(out.Repository.Gitattributes.Text, out.Repository.Exemplarignore.Text), nil
}

const exclusionsQuery = `query($owner: String!, $name: String!) {
  repository(owner: $owner, name: $name) {
    gitattributes: object(expression: "HEAD:.gitattributes") { ... on Blob { text } }
    exemplarignore: object(expression: "HEAD:.exemplarignore") { ... on Blob { text } }
  }
}`

func (j *Job) insert(ctx context.Context, events []queries.InsertPathEventParams, links []queries.InsertPRLinkParams) error {
	if len(events)+len(links) == 0 {
		return nil
	}
	return j.st.InTx(ctx, func(_ pgx.Tx, q *queries.Queries) error {
		for _, e := range events {
			if err := q.InsertPathEvent(ctx, e); err != nil {
				return fmt.Errorf("pathevents: store %s %s %s: %w", e.Kind, e.Ref, e.Path, err)
			}
		}
		for _, l := range links {
			if err := q.InsertPRLink(ctx, l); err != nil {
				return fmt.Errorf("pathevents: store %s link %s#%d: %w", l.Kind, l.Repo, l.PrNumber, err)
			}
		}
		return nil
	})
}

// historyQuery pages the default branch newest first. Each commit carries
// the PR it landed through, with that PR's files (capped at 100).
const historyQuery = `query($owner: String!, $name: String!, $first: Int!, $after: String, $since: GitTimestamp!) {
  repository(owner: $owner, name: $name) {
    defaultBranchRef {
      target {
        ... on Commit {
          history(first: $first, after: $after, since: $since) {
            pageInfo { hasNextPage endCursor }
            nodes {
              oid
              message
              committedDate
              url
              author { user { login } }
              associatedPullRequests(first: 1) {
                nodes {
                  number
                  title
                  body
                  merged
                  url
                  author { login }
                  mergeCommit { oid }
                  files(first: 100) { pageInfo { hasNextPage } nodes { path } }
                }
              }
            }
          }
        }
      }
    }
  }
}`

type historyPage struct {
	Repository struct {
		DefaultBranchRef *struct {
			Target struct {
				History struct {
					PageInfo struct {
						HasNextPage bool    `json:"hasNextPage"`
						EndCursor   *string `json:"endCursor"`
					} `json:"pageInfo"`
					Nodes []historyCommit `json:"nodes"`
				} `json:"history"`
			} `json:"target"`
		} `json:"defaultBranchRef"`
	} `json:"repository"`
}

type historyCommit struct {
	Oid           string    `json:"oid"`
	Message       string    `json:"message"`
	CommittedDate time.Time `json:"committedDate"`
	URL           string    `json:"url"`
	Author        struct {
		User *struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"author"`
	AssociatedPullRequests struct {
		Nodes []associatedPR `json:"nodes"`
	} `json:"associatedPullRequests"`
}

type associatedPR struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	Merged bool   `json:"merged"`
	URL    string `json:"url"`
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
	MergeCommit *struct {
		Oid string `json:"oid"`
	} `json:"mergeCommit"`
	Files struct {
		PageInfo struct {
			HasNextPage bool `json:"hasNextPage"`
		} `json:"pageInfo"`
		Nodes []struct {
			Path string `json:"path"`
		} `json:"nodes"`
	} `json:"files"`
}
