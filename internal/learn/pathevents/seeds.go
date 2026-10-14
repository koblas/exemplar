package pathevents

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/koblas/exemplar/internal/exclude"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/learn/ingest"
	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/scope"
	"github.com/koblas/exemplar/internal/store/queries"
)

// Seeds configures revert seeds (spec §8.1, PRD FR-L1.7): a reverted PR's
// hunks that no reviewer commented on become mined_comments rows with
// source 'revert_seed'. The extractor then reads the reverted diff and the
// revert reason in place of a review comment. Seeds carry code and
// free text, so they get ingest's data handling: eligibility, exclusions,
// secret redaction and pseudonyms.
type Seeds struct {
	Pseudonyms *ingest.Pseudonymizer
	Scanner    *redact.Scanner
	BotLogins  []string
	DocHosts   []string
	// MaxFiles bounds the seeds from one revert (default 5).
	MaxFiles int
}

// PRFiles lists a PR's per-file patches (REST; gh.PRFilePatches).
type PRFiles func(ctx context.Context, owner, repo string, number int) ([]gh.FilePatch, error)

// Limits on what one seed carries to the extractor.
const (
	maxSeedDiff   = 6000 // bytes of hunks per seed
	maxSeedReason = 4000
)

var (
	revertsPRRE  = regexp.MustCompile(`(?m)^Reverts ([\w.-]+/[\w.-]+)#(\d+)`)
	revertsSHARE = regexp.MustCompile(`(?m)^This reverts commit ([0-9a-f]{7,40})\b`)
	boilerplate  = regexp.MustCompile(`(?m)^(Reverts [\w.-]+/[\w.-]+#\d+|This reverts commit [0-9a-f]{7,40}\.?)\s*$`)
)

// revert is a detected revert with what seeding needs to know about it.
type revert struct {
	Ref      string // path_events ref
	Title    string
	Body     string // PR body, or the commit message for direct pushes
	URL      string
	Reverter string // login
	At       time.Time
}

// reason is the revert's explanation without GitHub's boilerplate.
func (r revert) reason() string {
	text := strings.TrimSpace(boilerplate.ReplaceAllString(r.Body, ""))
	if text == "" || text == r.Title {
		return ""
	}
	if len(text) > maxSeedReason {
		text = text[:maxSeedReason]
	}
	return text
}

// target finds the PR a revert undid: "Reverts owner/repo#N" in the same
// repo, else the PR that landed the first "This reverts commit" SHA.
func (j *Job) target(ctx context.Context, api API, repo, owner, name string, r revert) (int, error) {
	if m := revertsPRRE.FindStringSubmatch(r.Body); m != nil && strings.EqualFold(m[1], repo) {
		return strconv.Atoi(m[2])
	}
	m := revertsSHARE.FindStringSubmatch(r.Body)
	if m == nil {
		return 0, nil
	}
	var out struct {
		Repository struct {
			Object *struct {
				AssociatedPullRequests struct {
					Nodes []struct {
						Number int  `json:"number"`
						Merged bool `json:"merged"`
					} `json:"nodes"`
				} `json:"associatedPullRequests"`
			} `json:"object"`
		} `json:"repository"`
	}
	err := api.Query(ctx, commitPRQuery, map[string]any{"owner": owner, "name": name, "oid": m[1]}, &out)
	if errors.Is(err, gh.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("PR of %s: %w", m[1], err)
	}
	if o := out.Repository.Object; o != nil {
		for _, p := range o.AssociatedPullRequests.Nodes {
			if p.Merged {
				return p.Number, nil
			}
		}
	}
	return 0, nil
}

// seed writes the seeds for one revert and returns how many it wrote.
func (j *Job) seed(ctx context.Context, api API, repo string, rules exclude.Rules, r revert) (int, error) {
	owner, name, _ := strings.Cut(repo, "/")
	n, err := j.target(ctx, api, repo, owner, name, r)
	if err != nil || n == 0 {
		return 0, err
	}
	var pr revertedPR
	err = api.Query(ctx, revertedPRQuery, map[string]any{"owner": owner, "name": name, "number": n}, &pr)
	if errors.Is(err, gh.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("reverted PR #%d: %w", n, err)
	}
	target := pr.Repository.PullRequest
	if target == nil || !target.Merged {
		return 0, nil
	}
	files, err := api.PRFiles(ctx, owner, name, n)
	if errors.Is(err, gh.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("files of reverted PR #%d: %w", n, err)
	}
	cfg := j.cfg.Seeds
	reason, _, rerr := cfg.Scanner.Redact(r.reason())
	if rerr != nil {
		return 0, nil // a secret in the reason that cannot be removed: no seeds
	}
	var rows []queries.UpsertRevertSeedParams
	for _, f := range files {
		if len(rows) >= cfg.MaxFiles {
			break
		}
		if f.Patch == "" || rules.Reason(f.Path) != "" {
			continue
		}
		hunks, err := parsePatch(f.Path, f.Patch)
		if err != nil {
			continue
		}
		hunks = uncovered(hunks, target.reviewed(f.Path, target.Author.Login, cfg.BotLogins))
		if len(hunks) == 0 {
			continue
		}
		var diff strings.Builder
		for _, h := range hunks {
			if diff.Len() > 0 && diff.Len()+len(h.Text) > maxSeedDiff {
				break
			}
			diff.WriteString(h.Text)
		}
		text := diff.String()
		if len(text) > maxSeedDiff {
			text = text[:maxSeedDiff]
		}
		text, _, err = cfg.Scanner.Redact(text)
		if err != nil {
			continue
		}
		first := hunks[0]
		start, end := int32(first.NewStart), int32(first.NewStart+max(first.NewLines, 1)-1)
		head := target.HeadRefOid
		rows = append(rows, queries.UpsertRevertSeedParams{
			ID: seedID(repo, r.Ref, f.Path), Repo: repo, PrNumber: int32(n), Path: f.Path, Ext: path.Ext(f.Path),
			ReviewerPid: cfg.Pseudonyms.PID(login(r.Reverter)), AuthorPid: cfg.Pseudonyms.PID(login(target.Author.Login)),
			CreatedAt: pgtype.Timestamptz{Time: r.At, Valid: true}, CommentUrl: r.URL,
			Body: seedBody(r.Title, reason), DiffHunk: text, DocUrls: ingest.DocURLs(reason, cfg.DocHosts),
			CommitSha: &head, StartLine: &start, EndLine: &end,
		})
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return len(rows), j.st.InTx(ctx, func(_ pgx.Tx, q *queries.Queries) error {
		for _, row := range rows {
			if err := q.UpsertRevertSeed(ctx, row); err != nil {
				return fmt.Errorf("store seed for %s: %w", row.Path, err)
			}
		}
		return nil
	})
}

// seedBody is what the extractor reads as the "comment": the revert's
// title and reason. Titles are GitHub's `Revert "<original title>"`.
func seedBody(title, reason string) string {
	if reason == "" {
		reason = "(no reason given)"
	}
	return "Revert: " + strings.TrimSpace(title) + "\nReason: " + reason
}

// seedID is a stable id for a seed. Seeds have no GitHub comment id, so
// they take negative ids, which no GitHub comment has.
func seedID(repo, ref, p string) int64 {
	h := fnv.New64a()
	h.Write([]byte(repo + "\x00" + ref + "\x00" + p))
	return -int64(h.Sum64()>>1) - 1
}

func login(l string) string {
	if l == "" {
		return "ghost"
	}
	return l
}

// parsePatch parses one REST file patch into hunks.
func parsePatch(p, patch string) ([]scope.Hunk, error) {
	text := fmt.Sprintf("diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n%s\n", p, p, p, p, strings.TrimSuffix(patch, "\n"))
	d, err := scope.Parse(strings.NewReader(text))
	if err != nil {
		return nil, err
	}
	f, ok := d.File(p)
	if !ok {
		return nil, fmt.Errorf("no file %s in patch", p)
	}
	return f.Hunks, nil
}

// uncovered drops hunks whose new-side lines overlap a reviewed range.
func uncovered(hunks []scope.Hunk, reviewed [][2]int) []scope.Hunk {
	var out []scope.Hunk
	for _, h := range hunks {
		lo, hi := h.NewStart, h.NewStart+max(h.NewLines, 1)-1
		covered := false
		for _, r := range reviewed {
			if r[0] <= hi && r[1] >= lo {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, h)
		}
	}
	return out
}

const commitPRQuery = `query($owner: String!, $name: String!, $oid: GitObjectID!) {
  repository(owner: $owner, name: $name) {
    object(oid: $oid) { ... on Commit { associatedPullRequests(first: 5) { nodes { number merged } } } }
  }
}`

// revertedPRQuery reads the reverted PR and where reviewers commented.
const revertedPRQuery = `query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      merged
      headRefOid
      author { login }
      reviewThreads(first: 100) {
        nodes {
          comments(first: 1) {
            nodes { path line startLine originalLine originalStartLine author { __typename login } }
          }
        }
      }
    }
  }
}`

type revertedPR struct {
	Repository struct {
		PullRequest *targetPR `json:"pullRequest"`
	} `json:"repository"`
}

type targetPR struct {
	Merged     bool   `json:"merged"`
	HeadRefOid string `json:"headRefOid"`
	Author     struct {
		Login string `json:"login"`
	} `json:"author"`
	ReviewThreads struct {
		Nodes []struct {
			Comments struct {
				Nodes []struct {
					Path              string        `json:"path"`
					Line              *int          `json:"line"`
					StartLine         *int          `json:"startLine"`
					OriginalLine      *int          `json:"originalLine"`
					OriginalStartLine *int          `json:"originalStartLine"`
					Author            ingest.Author `json:"author"`
				} `json:"nodes"`
			} `json:"comments"`
		} `json:"nodes"`
	} `json:"reviewThreads"`
}

// reviewed returns the line ranges human reviewers (not bots, not the PR
// author) commented on in p. Ranges use the final line when GitHub still
// maps the comment, else the line where it was written.
func (t targetPR) reviewed(p, author string, bots []string) [][2]int {
	var out [][2]int
	for _, th := range t.ReviewThreads.Nodes {
		for _, c := range th.Comments.Nodes {
			if c.Path != p || ingest.IsBot(c.Author, bots) || strings.EqualFold(c.Author.Login, author) {
				continue
			}
			for _, pair := range [][2]*int{{c.StartLine, c.Line}, {c.OriginalStartLine, c.OriginalLine}} {
				if pair[1] == nil {
					continue
				}
				start := *pair[1]
				if pair[0] != nil {
					start = *pair[0]
				}
				out = append(out, [2]int{start, *pair[1]})
			}
			if c.Line == nil && c.OriginalLine == nil {
				out = append(out, [2]int{1, 1 << 30}) // file-level comment covers the file
			}
		}
	}
	return out
}
