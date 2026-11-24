## 3. Repositories and code layout

```
exemplar/                          # one Go module: github.com/koblas/exemplar
  cmd/
    exemplar-webhook/  exemplar-worker/   exemplar-api/   exemplar-gateway/
    exemplar-learn/    exemplar-search/
    exemplar/                      # CLI (cobra)
  internal/
    schema/      # CodeReviewRule, Finding, RepoConfig types + validation against schema/
    config/      # layering global/repo config; base-branch resolution; cache + invalidation; snapshots (§2.4 C3)
    scope/       # doublestar globs, diff parsing, hunk index, anchor validation
    engine/      # agent harness, tools, prompt assembly, output parsing, validation, References and PathHistory interfaces
    pipeline/    # the review's stages and the check-run verdict text
    compiled/    # Lane 1: ast-grep / Semgrep runners, output parsers, the rule sandbox
    buglane/     # Lane 3: class prompts, eligibility gate
    verify/      # verifier stage, quorum, evidence resolution
    calibrate/   # verifier and categorizer calibration; calibration_results
    rank/        # dedupe, class grouping, scoring, comment budget
    syntax/      # suggestion validation (apply, go/parser, tree-sitter, formatter checks) and mutation operators
    search/      # exemplar-search: mirror, zoekt index generations, query API, degraded mode; local/ for the checkout-backed References (§6.4.6)
    pathhistory/ # path_events + git log, from the mirror or the checkout
    exports/     # AGENTS.md / CLAUDE.md / Copilot / Cursor renderers; upstream/ for effectiveness
    llm/, redact/ # gateway client, budgets, pricing, providers.go; gitleaks scanning
    gh/          # App JWT, scoped installation tokens, throttled clients, GraphQL, posting
    gates/       # stage 1/2, denylist, policy (author experience, blast radius), compliance constants
    worker/      # River jobs: events, dispatcher and Launcher, runner entry, interdiff, merge policy, maintenance
    learn/       # the learning-plane jobs behind exemplar-learn
    store/       # sqlc-generated queries; River job args
    api/, web/, oidc/, setup/   # /v1/evaluate, sign-in, /ui/, setup mode
    telemetry/   # OTEL + slog setup
  schema/        # JSON Schema: source of truth for Go + Python contracts
  db/
    migrations/  # goose SQL migrations
    queries/     # sqlc .sql files
  learning/      # Python (uv project): cluster/ rank/ (embed/ pending a provider)
  dashboards/    # Grafana boards and alert rules as JSON/YAML (provisioned, version-controlled)
  deploy/        # Kustomize manifests (core, components, base, direct), images, manifests_test.go
  examples/      # exemplar-config starter, k8s overlays (acme, quickstart, quickstart-direct)
  docs/          # spec, PRD, plans, security notes, and the user documentation site

exemplar-config/                   # the human gate (separate repo, CODEOWNERS-protected)
  global.yaml                      # global defaults, model tiers, budgets; extends, disabled_rules, rule_settings (§3.1)
  repos/<org>/<repo>.yaml          # opt-in, auto-approve settings, KILL SWITCH
  rules/<category>/<rule-id>.yaml  # the organization's rules (optional: a config may only extend presets)
  presets/<name>/<rule-id>.yaml    # org:<name> presets: rules that run only where a layer extends them (§3.1)
  evals/<rule-id>/cases.yaml       # eval cases referencing real PRs by sha
  evals/_adversarial/ ...          # the organization's own defence cases, run with the shipped ones (§8.8)
  .github/workflows/eval.yml       # Actions CI: schema check + evals on every PR
  CODEOWNERS
```

**Repo-specific rules** live in the *target* repo at `.exemplar/rules/*.yaml`, and the project's rule choices in `.exemplar/config.yaml` (§3.1), owned by that repo's CODEOWNERS.

> **Critical security rule:** repo-level config and rules are **always read from the PR's base branch (default branch), never from the PR head.** Otherwise any author can disable rules or inject a rule description by editing `.exemplar/` in their own PR. Changes to `.exemplar/` in a PR take effect only after merge, exactly like the PRD's "human gate." The kill switch and opt-in live **only** in `exemplar-config`, never in target repos.

### 3.1 Rule layering and presets

PRD §7, FR-E19 and FR-E20. Four layers feed one effective config, computed in `internal/config`:

| Layer | Where | Owner | May |
|---|---|---|---|
| Presets | embedded in the binary (`exemplar:<name>`), rule ids prefixed `exemplar.` | Exemplar releases | define rules and their eval cases |
| Organization | `exemplar-config/global.yaml`, `rules/`, `presets/` | platform team, rule owners (CODEOWNERS) | `extends`, `presets_accepted`, `preset_owner`, define rules, `disabled_rules`, `rule_settings` (`status: shadow`, `exclude_paths`, `required`, `owner`, `history_from`) |
| Repository file | `exemplar-config/repos/<owner>/<repo>.yaml` | platform team | enrolment, kill switch, shadow, auto-approve and every other safety setting; `disabled_global_rules` |
| Project | target repo `.exemplar/config.yaml` and `.exemplar/rules/`, base branch only | the repo's CODEOWNERS | `extends` (`exemplar:*`, `org:*`), `disabled_rules` and `rule_settings` (`status: shadow`, `exclude_paths`) on rules not `required`; define repo rules |

```yaml
# exemplar-config/global.yaml (excerpt)
extends: [exemplar:recommended]
presets_accepted: "0.12"          # preset rules added after this release run as shadow
preset_owner: "@acme/platform"    # receives feedback for preset rules without an owner setting
disabled_rules: [exemplar.pin-container-images]
rule_settings:
  exemplar.external-call-timeouts: { status: shadow }
  exemplar.wrap-errors-with-cause: { required: true }
  exemplar.no-secrets-in-logs:     { owner: "@acme/security" }   # Security: required by default
  exemplar.propagate-context:      { exclude_paths: ["tools/**"], history_from: propagate-context }
```

```yaml
# <target repo>/.exemplar/config.yaml
extends: [org:payments]
disabled_rules: [exemplar.tests-for-behavior-change]
rule_settings:
  exemplar.propagate-context: { exclude_paths: ["cmd/migrate/**"] }
```

**Resolution.** Rules = the rules of every preset named by the organization's or the project's `extends`, plus `rules/` filtered by `applies_to` (FR-E17), plus the project's `.exemplar/rules/`. Removed: the organization's `disabled_rules`, the repository file's `disabled_global_rules`, then the project's `disabled_rules` except `required` rules. Settings: the organization's `rule_settings`, then the project's; `status: shadow` only lowers a rule's status, `exclude_paths` appends negated globs to `file_patterns`. A preset rule in the Security category or with `blocking` severity is `required` unless the organization sets `required: false`. Errors (validate and load): a rule id defined twice; a non-preset rule id starting `exemplar.`; a project setting or disable on a `required` rule; an unknown preset; an id in `disabled_rules` or `rule_settings` that no extended preset or loaded rule defines (a warning for the project layer, since the organization may stop extending a preset later).

**Preset references.**
- `exemplar:<name>`: embedded with the binary, versioned with it.
- `org:<name>`: the rule files in `presets/<name>/` of the config repository; they load and validate with the config but run only where a layer extends them.
- `github:<owner>/<repo>//<name>@<sha>`: `global.yaml` only. The rule files in `<name>/` of that repository at a full 40-character commit SHA; a branch, tag or short SHA is refused. Fetched with the config repository's installation token, so the repository must be installed for it; the fetched files may not themselves declare `extends`. Cached by SHA, since the content cannot change. A fetch failure at load keeps the last good snapshot, as a broken config repository does today, and never loads a partial rule set.

**Versions.** The snapshot's `ConfigVersion` combines the config repository's SHA, the preset version (the binary's release) and every pinned SHA, so a stored review names the exact rule text it ran. A preset rule added after the organization's `presets_accepted` release runs as `shadow` whatever its file says, until a config PR raises `presets_accepted`; that is the human gate (PRD FR-L6) for preset rules, and config CI on that PR shows `--preset-diff` from the accepted version. Rule text changes to existing preset rules take effect with the binary; a preset rule a release removes stays in the preset as `killed` for one release, so the organization's and projects' references to it still resolve, and `--preset-diff` lists them. `exemplar validate --preset-diff <version>` lists the preset rules added, changed or removed. exemplar-api reports its preset version (a new `GET /v1/version`); config CI validates against that version as well as its own binary's, so a config that passes CI loads in the cluster, and during a rolling deploy every binary resolves rules from the snapshot's recorded preset version, not its own.

**Readers of the effective config.** The review, the inventory (FR-E18) and the rule exports (§10.2, `internal/exports`, today `Effective(repo, nil)`) read the project's `.exemplar/config.yaml` and rules at the repository's base branch, so all three agree on what runs. Config CI's count of repositories a rule starts or stops running on reads only the config repository and says so.

**Learning plane on preset rules.** A preset rule has no file in the config repository, so demotion (`internal/learn/demote`) and expiry (`internal/learn/expiry`) write `rule_settings.<id>.status: shadow` in `global.yaml` instead of editing a rule file. Feedback, the digest and adjudication route to the rule's `owner` setting, else, for a shipped `exemplar:*` rule, `preset_owner`, else the rule file's owner (an `org:*` preset rule keeps the owner its file names); resolution sets it, so every reader of the rule's owner agrees, and the `rules` mirror updates a version's owner in place. A teach-the-bot scope proposal on a shipped rule adds to `rule_settings.<id>.exclude_paths`; an owner's shadow action writes `status: shadow` and a kill is refused in favor of `disabled_rules`. Expiry skips shipped rules: their `review_by` moves with Exemplar releases. `history_from` maps an old organization rule id to the preset rule for every statistic keyed by rule id (acceptance, Wilson bounds, demotion state), so adopting the preset keeps the copied rule's history.

**Change gating.** `.exemplar/**` is already on the auto-approve path denylist (`internal/gates/denylist.go`) and read from the base tree (`internal/engine/prepare.go`); `.exemplar/config.yaml` joins the files `Prepare` reads there.

**Migration.** A config without `extends` loads as before. Once a config extends a preset, `validate` warns on an organization rule whose content matches a preset rule (a copy of the old starter; delete it), and the 9 starter rules keep their unprefixed ids wherever an installation copied them, so nothing collides; deleting the copy and setting `history_from` on the preset rule carries its history over. `disabled_global_rules` stays accepted; the project's `disabled_rules` is the owned alternative.

---
