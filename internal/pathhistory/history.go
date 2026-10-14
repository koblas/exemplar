// Package pathhistory answers the path_history(path) context tool (spec
// §6.4): a path's incidents and reverts from path_events, plus its recent
// commits from git log on a mirror of the repo, newest first, at most 20
// entries.
//
// Commit subjects are untrusted text; callers scan them like any other
// context (the engine's toolbox redacts every tool result). No author
// names or emails are returned. When no mirror is available the answer
// carries the events alone and is marked degraded.
package pathhistory

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/koblas/exemplar/internal/search/api"
	"github.com/koblas/exemplar/internal/search/mirror"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
)

// Limits (spec §6.4: 20 entries).
const (
	MaxEntries = 20
	// MaxEvents keeps commits from crowding out incidents and reverts and
	// vice versa: events take at most half the entries, commits fill the rest.
	MaxEvents = MaxEntries / 2
)

// Entry kinds.
const (
	KindIncident = "incident"
	KindRevert   = "revert"
	KindCommit   = "commit"
)

// Entry is one history item.
type Entry struct {
	Kind    string
	Ref     string // incident id, "#<pr>" or SHA for events; short SHA for commits
	At      time.Time
	Summary string // commit subject; empty for events
}

// Result is a path's history.
type Result struct {
	Entries []Entry
	// Degraded is true when git history was unavailable and only events
	// are included.
	Degraded bool
}

// Commits lists commits touching a path as of rev, newest first: from a
// local mirror (Local) or from exemplar-search (Remote).
type Commits interface {
	Commits(ctx context.Context, repo, rev, path string, n int) ([]mirror.Commit, error)
}

// Local reads mirrors directly (exemplar-search itself, tests, local runs).
func Local(m *mirror.Manager) Commits { return localCommits{m} }

type localCommits struct{ m *mirror.Manager }

func (l localCommits) Commits(ctx context.Context, repo, rev, p string, n int) ([]mirror.Commit, error) {
	return l.m.Log(ctx, repo, rev, p, n)
}

// Remote asks exemplar-search, as a worker does. path_history reads only
// the PR's own repo.
func Remote(c *api.Client) Commits { return remoteCommits{c} }

type remoteCommits struct{ c *api.Client }

func (r remoteCommits) Commits(ctx context.Context, repo, rev, p string, n int) ([]mirror.Commit, error) {
	return r.c.Log(ctx, repo, repo, rev, p, n)
}

// Service answers path_history.
type Service struct {
	st      *store.Store
	commits Commits
}

// New returns a Service. commits may be nil (events only, degraded).
func New(st *store.Store, commits Commits) (*Service, error) {
	if st == nil {
		return nil, errors.New("pathhistory: store is required")
	}
	return &Service{st: st, commits: commits}, nil
}

var shaRE = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// History returns p's history in repo as of rev (the PR's base commit).
func (s *Service) History(ctx context.Context, repo, p, rev string) (Result, error) {
	if err := mirror.CleanPath(p); err != nil {
		return Result{}, fmt.Errorf("pathhistory: %w", err)
	}
	rows, err := s.st.PathEventsForPath(ctx, queries.PathEventsForPathParams{Repo: repo, Path: p, MaxRows: MaxEvents})
	if err != nil {
		return Result{}, fmt.Errorf("pathhistory: events: %w", err)
	}
	var res Result
	for _, r := range rows {
		res.Entries = append(res.Entries, Entry{Kind: r.Kind, Ref: r.Ref, At: r.At.Time})
	}
	if s.commits == nil || !shaRE.MatchString(rev) {
		res.Degraded = true
	} else {
		commits, err := s.commits.Commits(ctx, repo, rev, p, MaxEntries-len(res.Entries))
		if err != nil {
			// A missing commit or mirror, or exemplar-search being down,
			// degrades rather than fails: the events are still worth returning.
			res.Degraded = true
		}
		for _, c := range commits {
			res.Entries = append(res.Entries, Entry{Kind: KindCommit, Ref: c.SHA[:min(12, len(c.SHA))], At: c.At, Summary: c.Subject})
		}
	}
	sort.SliceStable(res.Entries, func(i, j int) bool { return res.Entries[i].At.After(res.Entries[j].At) })
	return res, nil
}

// Format renders a result for a model: one line per entry, newest first.
func Format(r Result) string {
	var b strings.Builder
	if len(r.Entries) == 0 {
		b.WriteString("no incidents, reverts or commits recorded for this path\n")
	}
	for _, e := range r.Entries {
		fmt.Fprintf(&b, "%s %s %s", e.At.UTC().Format("2006-01-02"), e.Kind, e.Ref)
		if e.Summary != "" {
			fmt.Fprintf(&b, " %s", e.Summary)
		}
		b.WriteByte('\n')
	}
	if r.Degraded {
		b.WriteString("(degraded: commit history unavailable; incidents and reverts only)\n")
	}
	return b.String()
}
