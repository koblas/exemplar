package precheck

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/schema"
)

// ExportTeam must approve every change to an export: block (spec §8.9,
// §10.2), in addition to the rule owner.
const ExportTeam = "@platform/devex"

// ExportChange is a rule whose export: block differs between base and head.
// Before is nil for an added block (or rule), After for a removed one.
type ExportChange struct {
	Rule   string         `json:"rule"`
	Before *schema.Export `json:"before,omitempty"`
	After  *schema.Export `json:"after,omitempty"`
}

// ExportChanges compares every rule's export: block, so a block cannot
// change unnoticed by moving or renaming the rule's file.
func ExportChanges(base, head *config.Snapshot) []ExportChange {
	exports := func(s *config.Snapshot) map[string]*schema.Export {
		m := map[string]*schema.Export{}
		for _, r := range s.Rules {
			m[r.ID] = r.Export
		}
		return m
	}
	b, h := exports(base), exports(head)
	var ids []string
	for id := range b {
		ids = append(ids, id)
	}
	for id := range h {
		if _, ok := b[id]; !ok {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	var out []ExportChange
	for _, id := range ids {
		before, after := b[id], h[id]
		if before == nil && after == nil {
			continue
		}
		if before != nil && after != nil && *before == *after {
			continue
		}
		out = append(out, ExportChange{Rule: id, Before: before, After: after})
	}
	return out
}

// TeamMember reports whether login is an active member of team.
type TeamMember func(ctx context.Context, team, login string) (bool, error)

// ExportApproval is the export-approval check's result.
type ExportApproval struct {
	Changes  []ExportChange `json:"changes"`
	Team     string         `json:"team"`
	Pass     bool           `json:"pass"`
	Approver string         `json:"approver,omitempty"`
	Reason   string         `json:"reason"`
}

// CheckExportApproval passes when there are no export changes, or when a
// member of team other than the PR author approved the PR's current head.
// Each reviewer's latest decisive review counts (a later changes-requested
// or dismissal replaces an approval; comments do not), and an approval of
// an older commit does not count: export text may have changed since.
func CheckExportApproval(ctx context.Context, changes []ExportChange, team, author, headSHA string, reviews []gh.Review, member TeamMember) (ExportApproval, error) {
	res := ExportApproval{Changes: changes, Team: team}
	if len(changes) == 0 {
		res.Pass, res.Reason = true, "no export: block changed"
		return res, nil
	}
	latest := map[string]gh.Review{}
	var order []string
	for _, r := range reviews {
		if r.State == "COMMENTED" || r.State == "PENDING" {
			continue
		}
		if _, seen := latest[r.User]; !seen {
			order = append(order, r.User)
		}
		latest[r.User] = r
	}
	stale := 0
	for _, user := range order {
		r := latest[user]
		if r.State != "APPROVED" || r.UserType == "Bot" || strings.EqualFold(user, author) {
			continue
		}
		if r.CommitID != headSHA {
			stale++
			continue
		}
		ok, err := member(ctx, team, user)
		if err != nil {
			return res, fmt.Errorf("precheck: is %s in %s: %w", user, team, err)
		}
		if ok {
			res.Pass, res.Approver = true, user
			res.Reason = fmt.Sprintf("approved by %s (%s)", user, team)
			return res, nil
		}
	}
	res.Reason = fmt.Sprintf("changes to export: blocks need an approval from %s on the current head", team)
	if stale > 0 {
		res.Reason += fmt.Sprintf(" (%d %s an earlier commit)", stale, pluralWord(stale, "approval is for", "approvals are for"))
	}
	return res, nil
}

func pluralWord(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
