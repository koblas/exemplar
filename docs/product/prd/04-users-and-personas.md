<a id="4-users-and-personas"></a>

## 4. Users and Personas

| Persona | Needs |
|---|---|
| **PR author** | Relevant, cited, actionable findings with fixes; fast low-risk merges; the same checks locally and in their coding agent before pushing |
| **Human reviewer** | Less time on mechanical and repeat-pattern review; focus on design |
| **Rule owner / domain expert** | Review mined candidates, edit and test them, promote or kill rules, see per-rule metrics and dismissal reasons |
| **Platform team** | Operate the system, control cost, manage global config, run evals, flip kill switches |
| **Security / compliance** | Enforce approval policies, audit provenance, confirm the bot cannot be prompt-injected into approving; know exactly which data leaves the org to which model provider, and exclude repos and paths that must never be sent (§8.11) |
| **Coding agents (Claude Code, Copilot, Cursor, internal agents)** | Machine-readable, path-scoped rules and a pre-PR check they can call |
| **Agent developers** | Call the alignment engine to judge output from other agents |

<a id="41-from-install-to-day-to-day-the-adoption-journey"></a>

### 4.1 From install to day-to-day: the adoption journey — *v0.11*

The personas above meet Exemplar in a fixed order: a platform engineer installs it on one repository, a first team lives with its findings, a second team joins and wants rules of its own, and only then do mined candidates, the inventory and auto-approve matter. Each stage adds one component and one promise, and each has a thing that must be true for the next to start. This is the journey a trial follows, so the product must be good at every stage, not only the last one.

| Stage | Who | What they do | What they see | What must be true | Requirements |
|---|---|---|---|---|---|
| **Day 0 — install and trial** | Platform engineer | Install from the starter config (about twenty minutes), enrol one repository in shadow, keep the shipped preset (`extends: [exemplar:recommended]`) as it is. Search and a managed database are optional. | Reviews in the web UI; nothing on the PR. | The starter config enrols nothing and copies no rules; its evals pass with no repository enrolled; the preset's rules say that they run on every repository you enrol; a trial needs no infrastructure beyond a cluster, a database and a model key. | FR-E12 (search optional), FR-E19, FR-X13, §8.11 eligibility |
| **Week 1 — posting on** | The first team's reviewers and rule owner | `shadow: false` on the repository; switch off the one or two starter rules that are wrong for this codebase; give the first structured feedback. | Findings on PRs with citations and fixes; the check-run verdict; a per-rule page with acceptance. | Opt-out of a rule for one repository, by the platform team (`disabled_global_rules`) or by the project in its own `.exemplar/config.yaml`; feedback routed to the rule owner; nothing blocks a merge. | FR-P, FR-B, FR-C, FR-E20 |
| **Month 1 — a second team** | Platform engineer, two rule owners | Enrol the second team's repositories (often several); the team writes three rules that apply to their services and nowhere else, and finds one starter rule wrong for all of them. | Two teams' rules in one config repository, owned by CODEOWNERS. | **A rule can apply to a set of repositories** (by name pattern or by a tag the platform team puts on the repository), not only to all of them or to one; the opt-out list does not grow with repositories × rules. **Someone can answer "what runs on this repository, and why".** | **FR-E17, FR-E18** (proposed) |
| **Quarter 1 — mining and ownership** | Rule owners, platform team | Turn the learning plane on; review mined candidates; promote the good ones; expire the stale ones. | Candidates with their source repositories and citations; the weekly digest. | A promoted candidate runs where its evidence came from, and the owner widens it on purpose; the per-repository inventory shows the blast radius of a rule change before it merges. | FR-L6, FR-E17, FR-E18 |
| **Steady state — approval** | Security, platform team | Turn on auto-approve for low-risk paths on opted-in repositories. | Approvals with their gate trace; counter-metrics. | Search is running (the bug lane's contract check needs it); the boards and alerts exist; a shadow period showed no approvals from degraded runs. | FR-A, §13.1 |

Three consequences for the requirements:

- **Scope is a first-class property of a rule.** Today the rule model has two tiers: a global rule in the config repository, and a repository-local rule in the target repository's `.exemplar/rules`. The month-1 stage needs a middle tier, specified in §7 as `applies_to` (FR-E17). Without it the second team either makes its rules global and asks twelve other repositories to opt out, or duplicates files into each of its repositories.
- **Rules come in layers, and each layer has an owner.** Exemplar ships presets (`exemplar:recommended`) that move with the binary; the platform team extends them in the config repository and adds the organization's own rules; a project extends again, opts out and adds rules in its own `.exemplar/`. Infrastructure repositories that are not enrolled carry nothing. A layer can narrow what the one above gave it but cannot loosen safety: Security and `blocking` preset rules are `required` unless the organization relaxes them, preset rules go active only when the organization accepts the release, and enrolment, the kill switch, shadow, auto-approve and the denylist stay in the config repository (FR-E19, FR-E20).
- **Tags are declared, not detected.** Language is already a per-file matter (`file_patterns`), so a repository never has to say it is "a Go repository". The tags that select rules are organizational — a team, a data classification, a tier — and the platform team declares them in the repository's config file when it enrols it. GitHub topics can be mirrored later as a second source; detection from the code is an idea, not a requirement (§16 question 18).
- **The inventory is per repository.** The web UI lists rules; the stages above ask the question the other way round. A page per repository and a CLI command over the same effective-config computation, with the reason each rule applies (global, selected, repository-local) and the ones disabled there, is FR-E18.
