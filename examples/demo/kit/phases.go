package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/google/go-github/v88/github"
)

// pushRepo writes the sandbox repository's base tree and returns main.
func (r *repoClient) pushRepo(ctx context.Context, v Vars) (string, error) {
	files, err := tree("_repo", v)
	if err != nil {
		return "", err
	}
	sha, err := r.pushBase(ctx, files)
	if err != nil {
		return "", err
	}
	r.log.Info("sandbox repository ready", "repo", r.org+"/"+r.repo, "main", sha)
	return sha, nil
}

// writeConfig renders the demo's exemplar-config tree into dir.
func writeConfig(src fs.FS, v Vars, dir string) error {
	files, err := renderConfig(src, v)
	if err != nil {
		return err
	}
	for _, p := range sortedKeys(files) {
		dst := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
			return fmt.Errorf("write config: %w", err)
		}
		if err := os.WriteFile(dst, []byte(files[p]), 0o600); err != nil {
			return fmt.Errorf("write config: %w", err)
		}
	}
	return nil
}

// seed opens, reviews and merges each seed PR. A PR already merged is
// skipped, and a reviewer who already reviewed a PR is not asked again,
// so the phase can be re-run after a failure.
func (r *repoClient) seed(ctx context.Context, seeds []SeedPR) error {
	for _, s := range seeds {
		pr, err := r.openPR(ctx, DemoPR{Branch: s.Branch, Title: s.Title, Body: s.Body, Files: s.Files})
		if err != nil {
			return err
		}
		if pr.GetMerged() || pr.MergedAt != nil {
			r.log.Info("seed PR already merged", "number", pr.GetNumber(), "branch", s.Branch)
			continue
		}
		for i, who := range r.reviewers {
			var comments []*github.DraftReviewComment
			for _, c := range s.Comments {
				if c.Reviewer != i {
					continue
				}
				line, err := lineOf(s.Files[c.Path], c.Match)
				if err != nil {
					return fmt.Errorf("%s: %s: %w", s.Branch, c.Path, err)
				}
				comments = append(comments, &github.DraftReviewComment{Path: new(c.Path), Line: new(line), Side: new("RIGHT"), Body: new(c.Body)})
			}
			if len(comments) == 0 {
				continue
			}
			done, err := r.reviewed(ctx, who, pr.GetNumber())
			if err != nil {
				return err
			}
			if !done {
				if err := r.review(ctx, who, pr, "A couple of things from me.", comments); err != nil {
					return err
				}
			}
		}
		if err := r.merge(ctx, pr); err != nil {
			return err
		}
		r.log.Info("seed PR merged", "number", pr.GetNumber(), "branch", s.Branch)
	}
	return nil
}

// openAll opens the beat A and B PRs.
func (r *repoClient) openAll(ctx context.Context, prs []DemoPR) error {
	for _, p := range prs {
		pr, err := r.openPR(ctx, p)
		if err != nil {
			return err
		}
		expect := "findings"
		if p.Approve != nil {
			expect = map[bool]string{true: "auto-approved", false: "routed to humans"}[*p.Approve]
		}
		fmt.Fprintf(os.Stdout, "beat %s: #%d %s (%s): %s\n", p.Beat, pr.GetNumber(), p.Title, expect, pr.GetHTMLURL())
	}
	return nil
}

var errShort = errors.New("too few findings")

// Feedback steers beat D: volume PRs are opened until Target of the demo
// rule's findings have been marked wrong, at most MaxPRs of them.
type Feedback struct {
	Target, MaxPRs int
	// Poll and Wait bound how long each PR is watched for the bot's
	// review; AckWait how long a reply is watched for the worker's +1.
	Poll, Wait, AckWait time.Duration
}

// WrongReason is the reviewers' reason; it reads as a rejection to the
// teach classifier, not a scope or exception note.
const WrongReason = "/exemplar wrong TODOs are how this team tracks follow-ups; this rule is noise for us."

// emptyRun is how many volume PRs in a row may get no demo-rule finding
// before feedback stops: the findings are being held back, not missed.
const emptyRun = 2

// feedback runs beat D's volume: the demo rule's findings are marked wrong
// by both reviewers in turn, each having reviewed the PR first so the
// feedback counts at reviewer weight. A finding counts as judged once the
// worker has acknowledged the reply with its +1, and a re-run counts the
// replies already made, so it carries on where the last one stopped.
func (r *repoClient) feedback(ctx context.Context, f Feedback) (int, error) {
	judged, turn, empty := 0, 0, 0
	for n := 1; n <= f.MaxPRs && judged < f.Target; n++ {
		pr, err := r.openPR(ctx, volumePR(n))
		if err != nil {
			return judged, err
		}
		comments, replied, err := r.waitBot(ctx, pr.GetNumber(), f)
		if err != nil {
			return judged, err
		}
		if len(comments) == 0 {
			if empty++; empty >= emptyRun {
				return judged, fmt.Errorf("%w: %d volume PRs in a row (the last #%d) got no %s finding; "+
					"read the check run's held-back counts: the verifier may be holding them back", errShort, empty, pr.GetNumber(), DemoRule)
			}
			continue
		}
		empty = 0
		for _, who := range r.reviewers {
			done, err := r.reviewed(ctx, who, pr.GetNumber())
			if err != nil {
				return judged, err
			}
			if !done {
				if err := r.review(ctx, who, pr, "Looking at the new jobs.", nil); err != nil {
					return judged, err
				}
			}
		}
		for _, c := range comments {
			id, ok := replied[c.GetID()]
			if !ok {
				if id, err = r.reply(ctx, r.reviewers[turn%2], pr.GetNumber(), c.GetID(), WrongReason); err != nil {
					return judged, err
				}
				turn++
			}
			acked, err := r.acknowledged(ctx, id, f.Poll, f.AckWait)
			if err != nil {
				return judged, err
			}
			if !acked {
				r.log.Warn("reply not acknowledged; not counted", "number", pr.GetNumber(), "reply", id)
				continue
			}
			judged++
		}
		r.log.Info("volume PR judged", "number", pr.GetNumber(), "findings", len(comments), "judged", judged, "target", f.Target)
	}
	if judged < f.Target {
		return judged, fmt.Errorf("%w: only %d of %d findings judged after %d PRs; raise -max-prs or check the demo rule is active", errShort, judged, f.Target, f.MaxPRs)
	}
	return judged, nil
}

// waitBot polls the PR until the bot has commented with the demo rule or
// Wait passes; no comment is not an error (the model may find nothing).
func (r *repoClient) waitBot(ctx context.Context, number int, f Feedback) ([]*github.PullRequestComment, map[int64]int64, error) {
	deadline := time.Now().Add(f.Wait)
	for {
		comments, replied, err := r.botComments(ctx, number, DemoRule)
		if err != nil || len(comments) > 0 || !time.Now().Before(deadline) {
			if err == nil && len(comments) == 0 {
				r.log.Warn("no demo-rule finding on the PR", "number", number, "waited", f.Wait)
			}
			return comments, replied, err
		}
		if err := sleep(ctx, f.Poll); err != nil {
			return nil, nil, err
		}
	}
}

// TeachReason is the owner's "not important here" for the legacy path.
const TeachReason = "/exemplar not-important internal/legacy is frozen until finance retires it, so TODOs there will never be picked up."

// teach opens the legacy PR and has the rule owner mark its demo-rule
// finding not important here, which proposes narrowing file_patterns.
func (r *repoClient) teach(ctx context.Context, f Feedback) error {
	pr, err := r.openPR(ctx, teachPR())
	if err != nil {
		return err
	}
	comments, replied, err := r.waitBot(ctx, pr.GetNumber(), f)
	if err != nil {
		return err
	}
	if len(comments) == 0 {
		return fmt.Errorf("%w: no %s finding on #%d to teach from", errShort, DemoRule, pr.GetNumber())
	}
	id, ok := replied[comments[0].GetID()]
	if !ok {
		if id, err = r.reply(ctx, r.reviewers[0], pr.GetNumber(), comments[0].GetID(), TeachReason); err != nil {
			return err
		}
	}
	acked, err := r.acknowledged(ctx, id, f.Poll, f.AckWait)
	if err != nil {
		return err
	}
	if !acked {
		return fmt.Errorf("%w: the owner's reply on #%d was not acknowledged", errShort, pr.GetNumber())
	}
	return nil
}
