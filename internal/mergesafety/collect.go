// Package mergesafety measures the facts behind merge_safety_score for one
// PR and scores them with gates.MergeSafety. Every fact is measured by
// code:
//
//   - incidents and reverts per path come from path_events
//     (exemplar-learn path-events);
//   - churn comes from mined_prs (exemplar-learn ingest), so it is as fresh
//     as the last ingest run;
//   - no_tests and sensitive paths come from the diff and the repo's
//     sensitive_paths;
//   - force pushes and the author's tenure come from GitHub, read with the
//     PR's worker token.
//
// A fact that cannot be measured is an error, never a default: the score
// gates auto-approval, so it fails closed.
package mergesafety

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/koblas/exemplar/internal/gates"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
)

// Query runs a GraphQL query and decodes its data into out (gh.GraphQL
// bound to the PR's worker client).
type Query func(ctx context.Context, query string, vars map[string]any, out any) error

// PR identifies the PR being scored.
type PR struct {
	Repo   string // owner/name
	Number int
	Author string // login
	// Paths are every path the diff touches, excluded ones included: the
	// score is about where the change lands, not what was reviewed.
	Paths []string
}

// Collector measures merge-safety facts.
type Collector struct {
	st    *store.Store
	query Query
	now   func() time.Time
}

// New returns a Collector. now defaults to time.Now.
func New(st *store.Store, q Query, now func() time.Time) (*Collector, error) {
	if st == nil || q == nil {
		return nil, errors.New("mergesafety: store and query are required")
	}
	if now == nil {
		now = time.Now
	}
	return &Collector{st: st, query: q, now: now}, nil
}

// Collect measures the facts for pr. sensitive is the repo's
// sensitive_paths.
func (c *Collector) Collect(ctx context.Context, pr PR, sensitive []string) (gates.MergeSafetyInput, error) {
	in := gates.MergeSafetyInput{Paths: make([]gates.PathFacts, len(pr.Paths)), NoTests: gates.NoTests(pr.Paths)}
	owner, name, ok := strings.Cut(pr.Repo, "/")
	if !ok {
		return in, fmt.Errorf("mergesafety: repo %q must be owner/name", pr.Repo)
	}
	byPath := map[string]*gates.PathFacts{}
	for i, p := range pr.Paths {
		in.Paths[i] = gates.PathFacts{Path: p, Sensitive: gates.Sensitive(p, sensitive)}
		byPath[p] = &in.Paths[i]
	}
	now := c.now()
	if len(pr.Paths) > 0 {
		events, err := c.st.RecentPathEvents(ctx, queries.RecentPathEventsParams{
			Repo: pr.Repo, Paths: pr.Paths, Since: ts(now.AddDate(0, 0, -max(gates.IncidentWindowDays, gates.RevertWindowDays))),
		})
		if err != nil {
			return in, fmt.Errorf("mergesafety: path events: %w", err)
		}
		for _, e := range events {
			if f := byPath[e.Path]; f != nil {
				switch e.Kind {
				case "incident":
					f.Incident = true
				case "revert":
					f.Revert = true
				}
				if e.Latest.Valid && !e.Latest.Time.Before(now.AddDate(0, 0, -gates.RecentEventDays)) {
					f.Recent = true
				}
			}
		}
		churn, err := c.st.PathChurn(ctx, queries.PathChurnParams{
			Repo: pr.Repo, Since: ts(now.AddDate(0, 0, -gates.ChurnWindowDays)), PrNumber: int32(pr.Number), Paths: pr.Paths,
		})
		if err != nil {
			return in, fmt.Errorf("mergesafety: churn: %w", err)
		}
		for _, r := range churn {
			if f := byPath[r.Path]; f != nil {
				f.MergedPRs = int(r.Prs)
			}
		}
	}

	var out struct {
		Repository struct {
			PullRequest *struct {
				TimelineItems struct {
					TotalCount int `json:"totalCount"`
				} `json:"timelineItems"`
			} `json:"pullRequest"`
		} `json:"repository"`
	}
	if err := c.query(ctx, forcePushQuery, map[string]any{"owner": owner, "name": name, "number": pr.Number}, &out); err != nil {
		return in, fmt.Errorf("mergesafety: force pushes: %w", err)
	}
	if out.Repository.PullRequest == nil {
		return in, fmt.Errorf("mergesafety: %s#%d not found", pr.Repo, pr.Number)
	}
	in.ForcePush = out.Repository.PullRequest.TimelineItems.TotalCount > 0

	newAuthor, err := c.newAuthor(ctx, pr.Repo, pr.Author, now)
	if err != nil {
		return in, err
	}
	in.NewAuthor = newAuthor
	return in, nil
}

var loginRE = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})(?:\[bot\])?$`)

// newAuthor reports whether the author is in their first 30 days in the
// repo: no PR of theirs merged into it before then. A login that cannot
// be put in a search query safely counts as new.
func (c *Collector) newAuthor(ctx context.Context, repo, login string, now time.Time) (bool, error) {
	if !loginRE.MatchString(login) {
		return true, nil
	}
	cutoff := now.AddDate(0, 0, -gates.NewAuthorDays).UTC().Format("2006-01-02")
	q := fmt.Sprintf("repo:%s is:pr is:merged author:%s merged:<%s", repo, login, cutoff)
	var out struct {
		Search struct {
			IssueCount int `json:"issueCount"`
		} `json:"search"`
	}
	if err := c.query(ctx, authorQuery, map[string]any{"q": q}, &out); err != nil {
		return false, fmt.Errorf("mergesafety: author tenure: %w", err)
	}
	return out.Search.IssueCount == 0, nil
}

// Score collects the facts and scores them with weights (from
// gates.Weights over global.yaml merge_safety.weights).
func (c *Collector) Score(ctx context.Context, pr PR, sensitive []string, weights map[string]float64) (gates.MergeSafetyResult, error) {
	in, err := c.Collect(ctx, pr, sensitive)
	if err != nil {
		return gates.MergeSafetyResult{}, err
	}
	return gates.MergeSafety(in, weights), nil
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

const forcePushQuery = `query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      timelineItems(itemTypes: [HEAD_REF_FORCE_PUSHED_EVENT], first: 1) { totalCount }
    }
  }
}`

const authorQuery = `query($q: String!) {
  search(query: $q, type: ISSUE, first: 1) { issueCount }
}`
