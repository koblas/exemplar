package engine

import (
	"context"
	"strings"

	"github.com/koblas/exemplar/internal/gates"
)

// bugLaneCovered reports whether the bug lane ran and every bug-lane run
// reached full coverage. It is false when no bug class ran.
func bugLaneCovered(runs []RunResult) bool {
	ran := false
	for _, r := range runs {
		if r.Lane != LaneBug {
			continue
		}
		if r.Status != RunOK || r.Coverage < 1 {
			return false
		}
		ran = true
	}
	return ran
}

// policyInput gathers the deterministic gates' policy facts: the touched paths
// and size from the diff, the author, head repo and commits from PRInfo, and
// the repo's auto_approve settings. Team memberships are looked up only when
// auto-approve is on for the repo, since they cost GitHub calls.
func (p *Prepared) policyInput(ctx context.Context, opt Options) gates.PolicyInput {
	in := gates.PolicyInput{
		Repo: p.PR.Repo, Touched: p.Touched(), Files: len(p.Diff.Files()), ChangedLines: p.Diff.ChangedLines(),
		Author: p.PR.Author, AuthorType: p.PR.AuthorType, CommitMessages: p.PR.CommitMessages,
		HeadRepo: p.PR.HeadRepo, Title: p.PR.Title,
	}
	aa := p.Effective.RepoConfig.AutoApprove
	if aa == nil {
		return in
	}
	in.Policy = gates.AutoApprovePolicy{
		Paths: aa.Paths, ExtraDenylist: aa.ExtraDenylist, AllowedAuthors: aa.AllowedAuthors,
		MaxFiles: aa.MaxFiles, MaxChangedLines: aa.MaxChangedLines, TitlePattern: aa.TitlePattern,
	}
	for _, b := range aa.BotAuthors {
		in.Policy.BotAuthors = append(in.Policy.BotAuthors, gates.BotIdentity{Identity: b.Identity, Paths: b.Paths})
	}
	if !aa.Enabled || !p.Effective.Global.AutoApprove || opt.TeamMember == nil || p.PR.Author == "" {
		return in
	}
	in.AuthorTeams = map[string]bool{}
	for _, a := range aa.AllowedAuthors {
		if !strings.HasPrefix(a, "@") || !strings.Contains(a, "/") {
			continue
		}
		if member, err := opt.TeamMember(ctx, a, p.PR.Author); err == nil {
			in.AuthorTeams[strings.ToLower(a)] = member
		}
	}
	return in
}
