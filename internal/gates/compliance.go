package gates

import "strings"

// Auto-approve compliance constants. They live in code, CODEOWNERS-owned by
// Security, and change only by code review and deploy; config cannot
// touch them.

// complianceRepos are repos under compliance rules. They keep their native
// two-approval rulesets, and Exemplar's approval counts as at most
// maxBotApprovals of them. Keys are lower-case owner/name.
var complianceRepos = map[string]bool{}

// maxBotApprovals is the most approvals the bot may count as anywhere.
const maxBotApprovals = 1

// IsComplianceRepo reports whether repo is under compliance rules.
func IsComplianceRepo(repo string) bool { return complianceRepos[strings.ToLower(repo)] }
