package gates

import (
	"regexp"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// Stage 1 policy (spec §6.13): the path, author, fork and size checks.
// Every input is a fact from GitHub, git or config, never model output.
// CheckPolicy turns them into the flags Stage1Input carries and a trace
// of which path or identity failed.

// Size limits. A repo's max_files and max_changed_lines can only lower them.
const (
	MaxFiles        = 10
	MaxChangedLines = 1000
)

// Author kinds in the trace.
const (
	AuthorHuman   = "human"
	AuthorBot     = "bot"
	AuthorUnknown = "unknown"
)

// Policy reasons. They appear in PolicyResult and, flattened, in Stage1Result.
const (
	ReasonPathNotAllowed     = "path_not_in_auto_approve_paths"
	ReasonPathDenylisted     = "path_denylisted"
	ReasonAuthorUnknown      = "author_unknown"
	ReasonCommitsUnknown     = "commits_unknown"
	ReasonAuthorNotAllowed   = "author_not_allowed"
	ReasonAuthorTeamUnknown  = "author_team_membership_unknown"
	ReasonBotNotAllowlisted  = "bot_or_agent_not_allowlisted"
	ReasonBotPathOutOfScope  = "bot_path_outside_its_scope"
	ReasonForkUnknown        = "fork_unknown"
	ReasonFork               = "fork_pr"
	ReasonTooManyFiles       = "too_many_files"
	ReasonTooManyLines       = "too_many_changed_lines"
	ReasonTitlePatternBroken = "title_pattern_invalid" // informational
)

// BotIdentity is an allowlisted bot or agent and the only paths it may
// have auto-approved (auto_approve.bot_authors).
type BotIdentity struct {
	Identity string   `json:"identity"`
	Paths    []string `json:"paths"`
}

// AutoApprovePolicy is a repo's auto_approve settings that Stage 1 uses.
type AutoApprovePolicy struct {
	Paths           []string      `json:"paths"`
	ExtraDenylist   []string      `json:"extra_denylist,omitempty"`
	AllowedAuthors  []string      `json:"allowed_authors"`
	BotAuthors      []BotIdentity `json:"bot_authors,omitempty"`
	MaxFiles        int           `json:"max_files,omitempty"`
	MaxChangedLines int           `json:"max_changed_lines,omitempty"`
	TitlePattern    string        `json:"title_pattern,omitempty"`
}

// PolicyInput is the PR as GitHub and git describe it. Strings here are
// author-controlled (logins, titles, commit messages) or config; none is
// model output.
type PolicyInput struct {
	Repo string
	// Touched is every path the PR touches, both sides of renames included.
	Touched      []string
	Files        int
	ChangedLines int
	// Author is the PR author's login; AuthorType is GitHub's account type
	// ("User", "Bot"; "" when unknown).
	Author, AuthorType string
	// AuthorTeams answers, for each "@org/team" in AllowedAuthors, whether
	// the author is a member. A missing team is unknown and fails closed.
	AuthorTeams map[string]bool
	// CommitMessages are the PR's commit messages, for Co-authored-by
	// trailers. Nil or empty means they were not read, which fails closed:
	// every PR has at least one commit.
	CommitMessages []string
	// HeadRepo is the head branch's repository ("" when unknown).
	HeadRepo string
	Title    string
	Policy   AutoApprovePolicy
}

// PolicyResult is the policy trace stored in approval_decisions.stage1.
type PolicyResult struct {
	PathsOK    bool     `json:"paths_ok"`
	NotAllowed []string `json:"not_allowed,omitempty"`
	Denylisted []string `json:"denylisted,omitempty"`

	AuthorOK   bool     `json:"author_ok"`
	AuthorKind string   `json:"author_kind"`
	Agents     []string `json:"agents,omitempty"` // bot/agent identities found
	// OutOfScope lists paths outside some bot identity's narrower paths.
	OutOfScope []string `json:"out_of_scope,omitempty"`

	NotFork bool `json:"not_fork"`

	SizeOK          bool `json:"size_ok"`
	Files           int  `json:"files"`
	ChangedLines    int  `json:"changed_lines"`
	MaxFiles        int  `json:"max_files"`
	MaxChangedLines int  `json:"max_changed_lines"`

	// TitleMatches records title_pattern; it is informational only, since
	// the title is author-controlled, and never gates. Nil without a pattern.
	TitleMatches *bool `json:"title_matches,omitempty"`

	Reasons []string `json:"reasons"`
	Notes   []string `json:"notes,omitempty"`
}

// CheckPolicy evaluates Stage 1's policy checks.
func CheckPolicy(in PolicyInput) PolicyResult {
	res := PolicyResult{Reasons: []string{}, Files: in.Files, ChangedLines: in.ChangedLines}
	reason := func(r string) {
		if !slices.Contains(res.Reasons, r) {
			res.Reasons = append(res.Reasons, r)
		}
	}
	paths := slices.Clone(in.Touched)
	slices.Sort(paths)
	paths = slices.Compact(paths)

	// Paths: inside auto_approve.paths and off the denylist.
	for _, p := range paths {
		if !matchAny(in.Policy.Paths, p) {
			res.NotAllowed = append(res.NotAllowed, p)
			reason(ReasonPathNotAllowed)
		}
		if Denylisted(in.Repo, p, in.Policy.ExtraDenylist) {
			res.Denylisted = append(res.Denylisted, p)
			reason(ReasonPathDenylisted)
		}
	}
	if len(paths) == 0 {
		reason(ReasonPathNotAllowed) // nothing to vouch for is not a pass
	}
	res.PathsOK = len(res.NotAllowed) == 0 && len(res.Denylisted) == 0 && len(paths) > 0

	// Author.
	res.AuthorKind, res.AuthorOK = AuthorUnknown, false
	switch {
	case in.Author == "" || in.AuthorType == "":
		reason(ReasonAuthorUnknown)
	case len(in.CommitMessages) == 0:
		reason(ReasonCommitsUnknown)
	default:
		res.AuthorOK = checkAuthor(in, paths, &res, reason)
	}

	// Fork.
	switch {
	case in.HeadRepo == "":
		reason(ReasonForkUnknown)
	case !strings.EqualFold(in.HeadRepo, in.Repo):
		reason(ReasonFork)
	default:
		res.NotFork = true
	}

	// Size.
	res.MaxFiles, res.MaxChangedLines = lower(MaxFiles, in.Policy.MaxFiles), lower(MaxChangedLines, in.Policy.MaxChangedLines)
	if in.Files > res.MaxFiles {
		reason(ReasonTooManyFiles)
	}
	if in.ChangedLines > res.MaxChangedLines {
		reason(ReasonTooManyLines)
	}
	res.SizeOK = in.Files <= res.MaxFiles && in.ChangedLines <= res.MaxChangedLines

	// Title: informational.
	if in.Policy.TitlePattern != "" {
		if re, err := regexp.Compile(in.Policy.TitlePattern); err != nil {
			res.Notes = append(res.Notes, ReasonTitlePatternBroken)
		} else {
			m := re.MatchString(in.Title)
			res.TitleMatches = &m
		}
	}
	return res
}

// checkAuthor applies FR-A6: a human must be on allowed_authors; every bot
// or agent identity (the account itself, or a Co-authored-by agent) must be
// allowlisted under bot_authors, and every path must be inside each one's
// narrower paths. A human PR with an agent co-author needs both.
func checkAuthor(in PolicyInput, paths []string, res *PolicyResult, reason func(string)) bool {
	ok := true
	isBot := strings.EqualFold(in.AuthorType, "Bot") || strings.HasSuffix(strings.ToLower(in.Author), "[bot]")
	if isBot {
		res.Agents = append(res.Agents, in.Author)
	}
	for _, msg := range in.CommitMessages {
		for _, a := range AgentCoAuthors(msg) {
			if !slices.ContainsFunc(res.Agents, func(x string) bool { return strings.EqualFold(x, a) }) {
				res.Agents = append(res.Agents, a)
			}
		}
	}
	res.AuthorKind = AuthorHuman
	if isBot {
		res.AuthorKind = AuthorBot
	} else if !humanAllowed(in, reason) {
		ok = false
	}
	for _, id := range res.Agents {
		ba, found := botIdentity(in.Policy.BotAuthors, id)
		if !found {
			reason(ReasonBotNotAllowlisted)
			ok = false
			continue
		}
		for _, p := range paths {
			if !matchAny(ba.Paths, p) && !slices.Contains(res.OutOfScope, p) {
				res.OutOfScope = append(res.OutOfScope, p)
				reason(ReasonBotPathOutOfScope)
				ok = false
			}
		}
	}
	return ok
}

func humanAllowed(in PolicyInput, reason func(string)) bool {
	unknown := false
	for _, a := range in.Policy.AllowedAuthors {
		if strings.HasPrefix(a, "@") && strings.Contains(a, "/") {
			member, known := in.AuthorTeams[strings.ToLower(a)]
			if member {
				return true
			}
			unknown = unknown || !known
			continue
		}
		if strings.EqualFold(strings.TrimPrefix(a, "@"), in.Author) {
			return true
		}
	}
	if unknown {
		reason(ReasonAuthorTeamUnknown)
	} else {
		reason(ReasonAuthorNotAllowed)
	}
	return false
}

func botIdentity(list []BotIdentity, id string) (BotIdentity, bool) {
	for _, b := range list {
		if strings.EqualFold(b.Identity, id) {
			return b, true
		}
	}
	return BotIdentity{}, false
}

func matchAny(patterns []string, p string) bool {
	return slices.ContainsFunc(patterns, func(g string) bool { return doublestar.MatchUnvalidated(g, p) })
}

// lower is the default limit, or a stricter configured one.
func lower(def, configured int) int {
	if configured > 0 && configured < def {
		return configured
	}
	return def
}

// knownAgents are substrings of a Co-authored-by name or email that mark a
// coding agent. A false positive only blocks auto-approve.
var knownAgents = []string{
	"noreply@anthropic.com", "claude",
	"copilot", "cursoragent", "cursor agent", "devin-ai-integration", "devin ai",
	"codex", "noreply@openai.com", "gemini-code-assist", "jules", "aider", "amazon q developer",
}

var coAuthorRE = regexp.MustCompile(`(?im)^[ \t]*co-authored-by:[ \t]*(.+?)[ \t]*$`)

// AgentCoAuthors returns the identities of known agents named in a commit
// message's Co-authored-by trailers: the email when there is one, else the
// name.
func AgentCoAuthors(msg string) []string {
	var out []string
	for _, m := range coAuthorRE.FindAllStringSubmatch(msg, -1) {
		v := m[1]
		lv := strings.ToLower(v)
		if !slices.ContainsFunc(knownAgents, func(k string) bool { return strings.Contains(lv, k) }) {
			continue
		}
		id := v
		if i, j := strings.LastIndex(v, "<"), strings.LastIndex(v, ">"); i >= 0 && j > i {
			id = strings.TrimSpace(v[i+1 : j])
		}
		out = append(out, id)
	}
	return out
}
