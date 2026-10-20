// Package ingest mines review comments from GitHub into mined_comments.
// Per repo it pages pullRequests(states: [MERGED, CLOSED], orderBy:
// UPDATED_AT) over GraphQL, keeps each review thread's root
// comment, and stores it only after the code pre-filters, the repo's path
// exclusions and secret redaction. Logins are pseudonymized on the way in.
// Merged PRs are also recorded in mined_prs (with whether a CODEOWNER
// approved them) and their files' owners in path_owners.
//
// The job is resumable and idempotent: the GraphQL cursor is saved with
// each page's rows in one transaction, and re-ingesting a comment or PR
// only refreshes it. On RATE_LIMITED or a low point budget it stops and
// reports when to resume.
package ingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/koblas/exemplar/internal/codeowners"
	"github.com/koblas/exemplar/internal/exclude"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
)

// Query runs a GraphQL query and decodes its data into out (gh.GraphQL
// bound to an ingest-flavor client).
type Query func(ctx context.Context, query string, vars map[string]any, out any) error

// Eligibility is the platform check (config.Snapshot.CheckEligible).
type Eligibility interface{ CheckEligible(repo string) error }

// Config configures a Job.
type Config struct {
	// BotLogins are extra logins treated as bots (the Exemplar app's own).
	BotLogins []string
	// DocHosts is the internal docs allowlist for doc-linked comments.
	DocHosts []string
	// PageSize is PRs per GraphQL page (default 25; each carries up to 100
	// threads, which dominates the point cost).
	PageSize int
	// MaxPages bounds one run (0 = until done or out of budget).
	MaxPages int
	// MinBudget stops the run when fewer GraphQL points remain (default 2x
	// the last page's cost).
	MinBudget int
	Logger    *slog.Logger
}

// Job ingests repos.
type Job struct {
	cfg     Config
	query   Query
	st      *store.Store
	elig    Eligibility
	scanner *redact.Scanner
	pseudo  *Pseudonymizer
	teams   map[string][]string // "org/slug" -> member logins (lowercase)
}

var (
	errConfig       = errors.New("ingest: invalid config")
	errNotOwnerName = errors.New("must be owner/name")
	errRepoNotFound = errors.New("not found")
	errNoDatabaseID = errors.New("has no database id")
)

// New returns a Job.
func New(cfg Config, q Query, st *store.Store, elig Eligibility, sc *redact.Scanner, p *Pseudonymizer) (*Job, error) {
	if q == nil || st == nil || elig == nil || sc == nil || p == nil {
		return nil, fmt.Errorf("%w: query, store, eligibility, scanner and pseudonymizer are required", errConfig)
	}
	if cfg.PageSize <= 0 || cfg.PageSize > 100 {
		cfg.PageSize = 25
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Job{cfg: cfg, query: q, st: st, elig: elig, scanner: sc, pseudo: p, teams: map[string][]string{}}, nil
}

// Stats summarize one run over one repo.
type Stats struct {
	Repo    string         `json:"repo"`
	Pages   int            `json:"pages"`
	PRs     int            `json:"prs"`
	Kept    int            `json:"kept"`
	Dropped map[string]int `json:"dropped"`
	// Truncated counts PRs with more threads or files than one page holds.
	Truncated int  `json:"truncated"`
	Done      bool `json:"done"`
	// ResumeAt is set when the run stopped for rate limits.
	ResumeAt *time.Time `json:"resume_at,omitempty"`
}

// Run ingests repo ("owner/name") from its saved cursor.
func (j *Job) Run(ctx context.Context, repo string) (Stats, error) {
	stats := Stats{Repo: repo, Dropped: map[string]int{}}
	if err := j.elig.CheckEligible(repo); err != nil {
		return stats, fmt.Errorf("ingest: %s is not eligible for mining: %w", repo, err)
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return stats, fmt.Errorf("ingest: repo %q %w", repo, errNotOwnerName)
	}
	files, err := j.repoFiles(ctx, owner, name)
	if err != nil {
		return stats, err
	}
	rules := exclude.Load(files.Gitattributes.Text, files.Exemplarignore.Text)
	co := codeowners.Parse(files.codeowners())

	var cursor *string
	switch c, err := j.st.GetIngestCursor(ctx, repo); {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return stats, fmt.Errorf("ingest: load cursor: %w", err)
	default:
		cursor = c
	}

	for j.cfg.MaxPages == 0 || stats.Pages < j.cfg.MaxPages {
		var page prPage
		err := j.query(ctx, prQuery, map[string]any{"owner": owner, "name": name, "first": j.cfg.PageSize, "after": cursor}, &page)
		if errors.Is(err, gh.ErrRateLimited) {
			at := time.Now().Add(time.Minute)
			stats.ResumeAt = &at
			return stats, nil
		}
		if err != nil {
			return stats, fmt.Errorf("ingest: %s page: %w", repo, err)
		}
		prs := page.Repository.PullRequests
		w, err := j.page(ctx, repo, prs.Nodes, rules, co, &stats)
		if err != nil {
			return stats, err
		}
		if prs.PageInfo.EndCursor != nil {
			cursor = prs.PageInfo.EndCursor
		}
		if err := j.save(ctx, repo, cursor, w); err != nil {
			return stats, err
		}
		stats.Pages++
		j.cfg.Logger.Info("ingest page", "repo", repo, "prs", len(prs.Nodes), "kept", len(w.comments), "points_remaining", page.RateLimit.Remaining)
		if !prs.PageInfo.HasNextPage {
			stats.Done = true
			return stats, nil
		}
		minBudget := j.cfg.MinBudget
		if minBudget <= 0 {
			minBudget = 2 * max(page.RateLimit.Cost, 1)
		}
		if page.RateLimit.ResetAt != nil && page.RateLimit.Remaining < minBudget {
			stats.ResumeAt = page.RateLimit.ResetAt
			return stats, nil
		}
	}
	return stats, nil
}

// writes are one page's rows, saved with its cursor.
type writes struct {
	comments []queries.UpsertIngestedCommentParams
	prs      []queries.UpsertMinedPRParams
	owners   map[string]string // path -> owner
	prsSeen  int
	filtered int
}

func (j *Job) page(ctx context.Context, repo string, prs []pullRequest, rules exclude.Rules, co codeowners.File, stats *Stats) (writes, error) {
	w := writes{owners: map[string]string{}, prsSeen: len(prs)}
	for _, pr := range prs {
		stats.PRs++
		if pr.ReviewThreads.PageInfo.HasNextPage || pr.Files.PageInfo.HasNextPage {
			stats.Truncated++
		}
		drop := func(reason string) { stats.Dropped[reason]++; w.filtered++ }
		for _, th := range pr.ReviewThreads.Nodes {
			if len(th.Comments.Nodes) == 0 {
				continue
			}
			c := th.Comments.Nodes[0]
			p, reason, err := j.comment(repo, pr, th.IsResolved, c, rules)
			if err != nil {
				return w, err
			}
			if reason != "" {
				drop(reason)
				continue
			}
			w.comments = append(w.comments, p)
			stats.Kept++
		}

		if pr.MergedAt == nil {
			continue
		}
		var changed []string
		for _, f := range pr.Files.Nodes {
			if rules.Reason(f.Path) != "" {
				continue
			}
			changed = append(changed, f.Path)
			if owners := co.Owners(f.Path); len(owners) > 0 {
				w.owners[f.Path] = owners[0]
			}
		}
		approved, err := j.codeownerApproved(ctx, pr, changed, co)
		if err != nil {
			return w, err
		}
		w.prs = append(w.prs, queries.UpsertMinedPRParams{
			Repo: repo, PrNumber: int32(pr.Number), HeadSha: pr.HeadRefOid, BaseSha: pr.BaseRefOid,
			MergedAt: pgtype.Timestamptz{Time: *pr.MergedAt, Valid: true}, ApprovedByCodeowner: approved,
			Files: append([]string{}, changed...),
		})
	}
	return w, nil
}

// trustedAssociations are the author associations whose review comments
// are mined, as the engine's human dedupe trusts them.
var trustedAssociations = []string{"OWNER", "MEMBER", "COLLABORATOR"}

// comment applies the pre-filters and data handling to a thread root. It
// returns a drop reason, or the row to store.
func (j *Job) comment(repo string, pr pullRequest, resolved bool, c reviewComment, rules exclude.Rules) (queries.UpsertIngestedCommentParams, string, error) {
	var p queries.UpsertIngestedCommentParams
	switch {
	case IsBot(c.Author, j.cfg.BotLogins):
		return p, DropBot, nil
	case pr.Author.Login != "" && strings.EqualFold(c.Author.Login, pr.Author.Login):
		return p, DropSelf, nil
	case !slices.Contains(trustedAssociations, strings.ToUpper(c.AuthorAssociation)):
		// Anyone can comment on a public repo's PRs; only the repo's own
		// reviewers teach the rules.
		return p, DropOutsider, nil
	case rules.Reason(c.Path) != "":
		return p, DropExcluded, nil
	}
	if reason := Prefilter(c.Body); reason != "" {
		return p, reason, nil
	}
	body, _, err1 := j.scanner.Redact(c.Body)
	hunk, _, err2 := j.scanner.Redact(c.DiffHunk)
	if err1 != nil || err2 != nil {
		return p, DropSecret, nil //nolint:nilerr // a secret that cannot be redacted drops the comment; it is an outcome, not a failure
	}
	id, err := c.id()
	if err != nil {
		return p, "", fmt.Errorf("ingest: %s#%d: %w", repo, pr.Number, err)
	}
	p = queries.UpsertIngestedCommentParams{
		ID: id, Repo: repo, PrNumber: int32(pr.Number), Path: c.Path, Ext: path.Ext(c.Path),
		ReviewerPid: j.pseudo.PID(login(c.Author)), AuthorPid: j.pseudo.PID(login(pr.Author)),
		CreatedAt: pgtype.Timestamptz{Time: c.CreatedAt, Valid: true}, CommentUrl: c.URL,
		Body: &body, DiffHunk: &hunk, DocUrls: DocURLs(body, j.cfg.DocHosts), ThreadResolved: resolved,
	}
	if c.OriginalCommit != nil && c.OriginalCommit.Oid != "" {
		p.CommitSha = &c.OriginalCommit.Oid
	}
	if c.OriginalLine != nil {
		end := int32(*c.OriginalLine)
		start := end
		if c.OriginalStartLine != nil {
			start = int32(*c.OriginalStartLine)
		}
		p.StartLine, p.EndLine = &start, &end
	}
	return p, "", nil
}

// codeownerApproved reports whether an approving reviewer owns any changed
// file, directly or through a team.
func (j *Job) codeownerApproved(ctx context.Context, pr pullRequest, changed []string, co codeowners.File) (bool, error) {
	var approvers []string
	for _, r := range pr.Reviews.Nodes {
		if r.Author.Login != "" && !strings.EqualFold(r.Author.Login, pr.Author.Login) {
			approvers = append(approvers, strings.ToLower(r.Author.Login))
		}
	}
	if len(approvers) == 0 {
		return false, nil
	}
	seen := map[string]bool{}
	for _, f := range changed {
		for _, o := range co.Owners(f) {
			if seen[o] || !strings.HasPrefix(o, "@") {
				continue
			}
			seen[o] = true
			members := []string{strings.ToLower(strings.TrimPrefix(o, "@"))}
			if strings.Contains(o, "/") {
				var err error
				if members, err = j.teamMembers(ctx, strings.TrimPrefix(o, "@")); err != nil {
					return false, err
				}
			}
			for _, a := range approvers {
				if slices.Contains(members, a) {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

func (j *Job) teamMembers(ctx context.Context, team string) ([]string, error) {
	if m, ok := j.teams[team]; ok {
		return m, nil
	}
	org, slug, _ := strings.Cut(team, "/")
	var members []string
	var after *string
	for {
		var out struct {
			Organization *struct {
				Team *struct {
					Members struct {
						Nodes    []struct{ Login string } `json:"nodes"`
						PageInfo pageInfo                 `json:"pageInfo"`
					} `json:"members"`
				} `json:"team"`
			} `json:"organization"`
		}
		if err := j.query(ctx, teamQuery, map[string]any{"org": org, "slug": slug, "after": after}, &out); err != nil {
			return nil, fmt.Errorf("ingest: members of @%s: %w", team, err)
		}
		if out.Organization == nil || out.Organization.Team == nil {
			break // unknown team: owns nothing we can check
		}
		for _, n := range out.Organization.Team.Members.Nodes {
			members = append(members, strings.ToLower(n.Login))
		}
		pi := out.Organization.Team.Members.PageInfo
		if !pi.HasNextPage || pi.EndCursor == nil {
			break
		}
		after = pi.EndCursor
	}
	j.teams[team] = members
	return members, nil
}

func (j *Job) save(ctx context.Context, repo string, cursor *string, w writes) error {
	err := j.st.InTx(ctx, func(_ pgx.Tx, q *queries.Queries) error {
		for _, c := range w.comments {
			if err := q.UpsertIngestedComment(ctx, c); err != nil {
				return fmt.Errorf("ingest: store comment %d: %w", c.ID, err)
			}
		}
		for _, p := range w.prs {
			if err := q.UpsertMinedPR(ctx, p); err != nil {
				return fmt.Errorf("ingest: store PR %d: %w", p.PrNumber, err)
			}
		}
		for p, o := range w.owners {
			if err := q.UpsertPathOwner(ctx, queries.UpsertPathOwnerParams{Repo: repo, Path: p, Owner: o}); err != nil {
				return fmt.Errorf("ingest: store owner of %s: %w", p, err)
			}
		}
		return q.SaveIngestCursor(ctx, queries.SaveIngestCursorParams{
			Repo: repo, Cursor: cursor, PrsSeen: int32(w.prsSeen), Kept: int32(len(w.comments)), Filtered: int32(w.filtered),
		})
	})
	if err != nil {
		return fmt.Errorf("ingest: save %s: %w", repo, err)
	}
	return nil
}

func (j *Job) repoFiles(ctx context.Context, owner, name string) (repoFiles, error) {
	var out struct {
		Repository *repoFiles `json:"repository"`
	}
	if err := j.query(ctx, filesQuery, map[string]any{"owner": owner, "name": name}, &out); err != nil {
		return repoFiles{}, fmt.Errorf("ingest: read %s/%s exclusions: %w", owner, name, err)
	}
	if out.Repository == nil {
		return repoFiles{}, fmt.Errorf("ingest: repo %s/%s %w", owner, name, errRepoNotFound)
	}
	return *out.Repository, nil
}

func login(a Author) string {
	if a.Login == "" {
		return "ghost" // deleted account
	}
	return a.Login
}

func (c reviewComment) id() (int64, error) {
	if c.FullDatabaseID != "" {
		id, err := strconv.ParseInt(c.FullDatabaseID, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("comment %s database id: %w", c.URL, err)
		}
		return id, nil
	}
	if c.DatabaseID == 0 {
		return 0, fmt.Errorf("comment %s %w", c.URL, errNoDatabaseID)
	}
	return c.DatabaseID, nil
}
