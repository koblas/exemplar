# How to write a rule

A rule is one YAML file under `rules/<category>/<id>.yaml` in `exemplar-config`. [Your first rule](../../tutorials/first-rule.md) walks through one end to end; this page is the decisions. Every field is on [rules/**/*.yaml](../../reference/config/rule.md).

## 1. Pick the kind

=== "exemplar"

    One agent reads the diff with your rule's text and read-only repository tools. Use it when judgment is needed. The three prose fields are the prompt:

    - `description`: the constraint, in one or two sentences, as a reviewer would state it.
    - `preferred_approach`: what the code should do instead.
    - `what_to_flag`: what to raise and, just as important, what not to. Name the false positives you can foresee (`main()`, tests, setup code).

    Set `context: text-only` to deny the agent the repository tools when the rule can be judged from the diff alone; it is cheaper and sends less code to the provider.

=== "compiled"

    An ast-grep or Semgrep pattern, no model. Use it whenever the constraint can be expressed as a pattern: it is cheaper, deterministic and exactly as precise as the pattern.

    ```yaml
    kind: compiled
    matcher:
      engine: ast-grep
      rule: |
        id: no-raw-sql-concat
        language: go
        rule:
          pattern:
            context: "func _() { $DB.Query($A + $B) }"
            selector: call_expression
    ```

    In Go, give a call pattern a `context` and `selector` as above: a bare `$DB.Query(...)` parses ambiguously and matches nothing. Check a pattern on a small file with `ast-grep scan --rule` or `semgrep scan --config` before you merge it; `exemplar validate` checks the rule file's schema, not the pattern. For a pattern that holds in any language, such as a key format, use Semgrep with `languages: [regex]`.

    `matcher` is required for `compiled` and forbidden otherwise. The learning plane's `compile` job tries to turn every candidate into one of these.

## 2. Scope it

`file_patterns` are globs with `!` exclusions, matched against the changed files; the rule runs only where one matches. Exclude tests and generated code unless the rule is about them:

```yaml
file_patterns: ["**/*.go", "!**/*_test.go", "!**/*.pb.go"]
```

A repository can switch a global rule off for itself with `disabled_global_rules`.

## 3. Set severity and fix policy

| `severity` | Use for | Posted |
|---|---|---|
| `blocking` | Security and correctness problems that must not merge | Always, even over the comment budget |
| `important` | Reliability, maintainability constraints the team agreed on | Within the budget, ranked first |
| `suggestion` | Preferences | Within the budget, ranked last |

Exemplar never requests changes, so `blocking` is a label and a ranking, not a merge block.

`fix_policy: suggest` lets a finding carry a GitHub suggestion block when the fix applied and parsed; `describe` posts the fix as prose; `none` posts the finding alone. Rules in the `Security` category also upload as SARIF code-scanning alerts.

## 4. Own it and date it

`owner` is a team (`@org/team`) that exists on GitHub and matches `CODEOWNERS` for the file. Its members approve the PR, adjudicate feedback, and get the weekly digest. `review_by` is the date the rule expires unless re-confirmed; without it, `expiry.default_months` from `global.yaml` applies.

`status` starts at `shadow`. Promote to `active` in a later PR once the shadow findings look right. A new rule you want Exemplar to generate eval cases for starts at `candidate` instead ([How to seed a rule by hand](seed-a-rule.md)).

## 5. Cite it

`citation.doc`, `citation.comments` and `citation.prs` are the links a posted finding carries as **why**. Mined candidates arrive with the comments they came from; a hand-written rule should link the design doc or the incident.

## 6. Decide on export

`export.agents: true` with a one-line `export.summary` (at most 300 characters) ships the rule to coding-agent instruction files ([How to enable rule exports](../operate/rule-exports.md)). Summaries are instructions to an agent: imperative, specific, no context needed. A change to an `export:` block needs a platform approval as well as yours.

## 7. Avoid overlap with linters

If a linter already reports the problem, map it: `linter_map` names the linter's check run and the rule ids that report the same issue, so a finding that overlaps one of their annotations is dropped as a duplicate instead of posted twice. Config CI's `linter-overlap` job fails a rule whose positive cases a configured linter already flags; such a rule belongs in the linter, not here.

## 8. Choose the model tier

`model_tier: standard` (the default) or `high`. Rules move to `high` on their own when eval precision falls short; blocking and security rules and bug hunting start on `high`.

## 9. Decide whether one verifier is enough

Every finding is checked by a verifier before it posts. A rule whose findings are often plausible but wrong (behaviour that depends on runtime state, say) can ask for a quorum instead:

```yaml
verify: quorum
```

Its findings then post only when `verifier.quorum.k` of `verifier.quorum.n` verifiers confirm them, at `n` times the verifier cost. Leave it out for rules whose findings the verifier's refutation rate on the lanes board shows are rarely wrong. See [`verify`](../../reference/config/rule.md).

Then write [eval cases](eval-cases.md), [test it](test-a-rule.md) and open the PR.
