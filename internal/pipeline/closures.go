package pipeline

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
)

// Closing earlier findings on a new head (spec §6.2).

// Closure reasons.
const (
	ClosedAddressed = "addressed" // the rule ran on the file again and no longer finds it
	ClosedRetired   = "retired"   // the rule is no longer active for the repo
	ClosedOutdated  = "outdated"  // the file is no longer part of the PR
)

// OpenFinding is a posted, still-open finding from an earlier review.
type OpenFinding = queries.OpenFindingsRow

// Closure is one open finding to close, and the thread reply.
type Closure struct {
	Finding OpenFinding
	Reason  string
	Reply   string
	// State is findings.state afterwards.
	State string
}

// PlanClosures decides which open findings a finished review closes. It
// closes nothing for an incomplete review, or for findings of the head
// just reviewed (a retry), and leaves alone any finding the review could
// not judge: one whose rule did not run cleanly on its file this time.
//   - retired: the rule is now killed, shadow or gone for the repo;
//   - addressed: the rule ran on the file with full coverage and the
//     fingerprint is gone, or the verifier now refutes it;
//   - outdated: the file is no longer changed by the PR.
func PlanClosures(open []OpenFinding, p *engine.Prepared, res *engine.Result) []Closure {
	if res.Incomplete != "" {
		return nil
	}
	head := p.PR.HeadSHA
	active := map[string]bool{engine.SecretRuleID: true}
	for _, r := range p.Effective.Rules {
		active[r.ID] = r.Status == schema.StatusActive
	}
	holds := map[string]bool{}
	for _, f := range res.Findings {
		if f.Suppressed != engine.SuppressRefuted {
			holds[f.Fingerprint] = true
		}
	}
	ran := func(rule, path string) bool {
		return slices.ContainsFunc(res.Runs, func(r engine.RunResult) bool {
			return r.RuleID == rule && r.Status == engine.RunOK && !r.Carried && r.Coverage >= 1 && slices.Contains(r.ScopedFiles, path)
		})
	}
	sha7 := head[:min(7, len(head))]
	var out []Closure
	for _, o := range open {
		switch {
		case o.HeadSha == head:
			continue
		case !active[o.RuleID]:
			out = append(out, Closure{
				Finding: o, Reason: ClosedRetired, State: "resolved",
				Reply: fmt.Sprintf("Rule `%s` was retired, so Exemplar no longer reports this.", o.RuleID),
			})
		case holds[o.Fingerprint]:
			continue
		case ran(o.RuleID, o.Path):
			out = append(out, Closure{Finding: o, Reason: ClosedAddressed, State: "resolved", Reply: "Addressed in " + sha7 + "."})
		case !inDiff(p, o.Path):
			out = append(out, Closure{
				Finding: o, Reason: ClosedOutdated, State: "outdated",
				Reply: "No longer part of this pull request as of " + sha7 + ".",
			})
		}
	}
	return out
}

func inDiff(p *engine.Prepared, path string) bool {
	return slices.Contains(p.Touched(), path)
}

// CloseFindings replies to and resolves each closure's thread, then
// records the finding's new state. One failure does not stop the rest;
// the errors are joined. Findings without a comment id are only recorded.
func CloseFindings(ctx context.Context, st *store.Store, poster *gh.Poster, pr gh.PR, cs []Closure) (int, error) {
	closed := 0
	var errs []error
	for _, c := range cs {
		thread := c.Finding.GhThreadID
		if id := c.Finding.GhCommentID; id != nil {
			t := ""
			if thread != nil {
				t = *thread
			}
			got, err := poster.CloseThread(ctx, pr, *id, t, c.Reply)
			if err != nil {
				errs = append(errs, fmt.Errorf("finding %d: %w", c.Finding.ID, err))
				continue
			}
			if got != "" {
				thread = &got
			}
		}
		if st != nil {
			if err := st.CloseFinding(ctx, queries.CloseFindingParams{ID: c.Finding.ID, State: c.State, GhThreadID: thread}); err != nil {
				errs = append(errs, fmt.Errorf("finding %d: %w", c.Finding.ID, err))
				continue
			}
		}
		closed++
	}
	return closed, errors.Join(errs...)
}
