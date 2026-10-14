package pathevents

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/store/queries"
)

// Incident is one line of the incident feed: JSON Lines, one incident per
// line, exported from whichever incident tool is chosen (spec open item
// 14). Only the id, time and links are read; any other field (titles,
// summaries) is ignored and never stored.
//
//	{"id": "INC-1234", "at": "2026-09-01T12:00:00Z",
//	 "links": [{"repo": "acme/app", "pr": 812}, {"repo": "acme/app", "commit": "3f2c1a9"}]}
type Incident struct {
	ID    string    `json:"id"`
	At    time.Time `json:"at"`
	Links []Link    `json:"links"`
}

// Link is a PR or commit an incident names.
type Link struct {
	Repo   string `json:"repo"`
	PR     int    `json:"pr,omitempty"`
	Commit string `json:"commit,omitempty"`
}

var (
	incidentIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)
	commitRE     = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
)

func (inc Incident) validate() error {
	if !incidentIDRE.MatchString(inc.ID) {
		return fmt.Errorf("incident id %q must be 1-64 of [A-Za-z0-9._:-]", inc.ID)
	}
	if inc.At.IsZero() {
		return fmt.Errorf("incident %s has no time", inc.ID)
	}
	for _, l := range inc.Links {
		if strings.Count(l.Repo, "/") != 1 {
			return fmt.Errorf("incident %s: repo %q must be owner/name", inc.ID, l.Repo)
		}
		if (l.PR > 0) == (l.Commit != "") {
			return fmt.Errorf("incident %s: each link names exactly one of pr or commit", inc.ID)
		}
		if l.Commit != "" && !commitRE.MatchString(l.Commit) {
			return fmt.Errorf("incident %s: bad commit %q", inc.ID, l.Commit)
		}
	}
	return nil
}

// ReadIncidents parses a feed, rejecting it whole on the first bad line so
// a malformed export never half-loads.
func ReadIncidents(r io.Reader) ([]Incident, error) {
	var out []Incident
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var inc Incident
		if err := json.Unmarshal([]byte(line), &inc); err != nil {
			return nil, fmt.Errorf("pathevents: incident feed line %d: %w", n, err)
		}
		if err := inc.validate(); err != nil {
			return nil, fmt.Errorf("pathevents: incident feed line %d: %w", n, err)
		}
		out = append(out, inc)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("pathevents: read incident feed: %w", err)
	}
	return out, nil
}

// IncidentStats summarize one feed load.
type IncidentStats struct {
	Incidents  int `json:"incidents"`
	Links      int `json:"links"`
	Ineligible int `json:"ineligible"` // links into repos that may not be mined
	Missing    int `json:"missing"`    // PRs or commits GitHub does not know
	Events     int `json:"events"`
}

// Incidents records the paths each incident's linked PRs and commits
// touched. Links into ineligible repos are skipped, not fetched.
func (j *Job) Incidents(ctx context.Context, incs []Incident) (IncidentStats, error) {
	var stats IncidentStats
	apis := map[string]API{}
	for _, inc := range incs {
		stats.Incidents++
		var events []queries.InsertPathEventParams
		var links []queries.InsertPRLinkParams
		for _, l := range inc.Links {
			stats.Links++
			if j.elig.CheckEligible(l.Repo) != nil {
				stats.Ineligible++
				continue
			}
			api, ok := apis[l.Repo]
			if !ok {
				var err error
				if api, err = j.connect(ctx, l.Repo); err != nil {
					return stats, err
				}
				apis[l.Repo] = api
			}
			paths, err := j.linkFiles(ctx, api, l)
			if errors.Is(err, errMissing) {
				stats.Missing++
				continue
			}
			if err != nil {
				return stats, fmt.Errorf("pathevents: incident %s: %w", inc.ID, err)
			}
			for _, p := range paths {
				events = append(events, queries.InsertPathEventParams{
					Repo: l.Repo, Path: p, Kind: KindIncident, Ref: inc.ID, At: pgtype.Timestamptz{Time: inc.At, Valid: true},
				})
			}
			if l.PR > 0 {
				links = append(links, queries.InsertPRLinkParams{Repo: l.Repo, PrNumber: int32(l.PR), Kind: KindIncident, Ref: inc.ID,
					At: pgtype.Timestamptz{Time: inc.At, Valid: true}})
			}
		}
		if err := j.insert(ctx, events, links); err != nil {
			return stats, err
		}
		stats.Events += len(events)
	}
	return stats, nil
}

var errMissing = errors.New("not found")

// linkFiles returns the paths a linked PR or commit changed. Merged PRs
// already ingested come from mined_prs without a GitHub call.
func (j *Job) linkFiles(ctx context.Context, api API, l Link) ([]string, error) {
	owner, name, _ := strings.Cut(l.Repo, "/")
	if l.Commit != "" {
		files, err := api.CommitFiles(ctx, owner, name, l.Commit)
		if errors.Is(err, gh.ErrNotFound) {
			return nil, errMissing
		}
		return files, err
	}
	files, err := j.st.MinedPRFiles(ctx, queries.MinedPRFilesParams{Repo: l.Repo, PrNumber: int32(l.PR)})
	if err == nil {
		return files, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("load %s#%d: %w", l.Repo, l.PR, err)
	}
	var out struct {
		Repository struct {
			PullRequest *struct {
				Files struct {
					Nodes []struct {
						Path string `json:"path"`
					} `json:"nodes"`
				} `json:"files"`
			} `json:"pullRequest"`
		} `json:"repository"`
	}
	err = api.Query(ctx, prFilesQuery, map[string]any{"owner": owner, "name": name, "number": l.PR}, &out)
	if errors.Is(err, gh.ErrNotFound) {
		return nil, errMissing
	}
	if err != nil {
		return nil, fmt.Errorf("files of %s#%d: %w", l.Repo, l.PR, err)
	}
	if out.Repository.PullRequest == nil {
		return nil, errMissing
	}
	for _, f := range out.Repository.PullRequest.Files.Nodes {
		files = append(files, f.Path)
	}
	return files, nil
}

const prFilesQuery = `query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) { files(first: 100) { nodes { path } } }
  }
}`
