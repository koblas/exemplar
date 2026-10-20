# Your first rule

You have an installation reviewing at least one repository ([Your first review](first-review.md)), a clone of `exemplar-config`, the `exemplar` CLI ([The CLI](../reference/cli.md)) and a GitHub token that can read a repository with an open pull request. In this tutorial you write a rule, give it eval cases, run it on that pull request without posting anything, and merge it through config CI. About 30 minutes.

## 1. Write the rule

Rules live under `rules/<category>/<id>.yaml`. Create `rules/reliability/close-response-body.yaml`:

```yaml
id: close-response-body
kind: exemplar
category: Reliability
severity: important
owner: "@platform/backend"
status: shadow
review_by: 2027-10-01
file_patterns: ["**/*.go", "!**/*_test.go"]
description: |
  Every *http.Response whose body is read must have its Body closed,
  usually with defer resp.Body.Close() right after the error check.
preferred_approach: |
  Check the error, then defer resp.Body.Close() before reading.
what_to_flag: |
  - A call that returns *http.Response where the body is never closed on
    the success path.
  Do not flag code that hands the response to a helper that closes it.
fix_policy: suggest
citation:
  doc: https://pkg.go.dev/net/http#Client.Do
export:
  agents: true
  summary: "Close every *http.Response body you read."
```

`kind: exemplar` means one agent reads the diff with this rule's text and the repository context. `status: shadow` means findings are stored but never posted, which is where every new rule starts. `owner` is the team whose members may adjudicate feedback and move the rule. [rules/**/*.yaml](../reference/config/rule.md) lists every field.

Check it:

```bash
exemplar validate .
```

## 2. Add eval cases

Config CI refuses a rule that has no cases or whose precision and recall fall below `eval_thresholds` in `global.yaml`. Create `evals/close-response-body/cases.yaml` with at least one positive and one negative case:

```yaml
rule: close-response-body
cases:
  - name: leaked body in fetch helper
    ref: acme/app@1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c
    expect:
      - path: internal/fetch/client.go
        lines: [41, 44]
  - name: body closed with defer
    ref: acme/app@1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c
    paths: [internal/fetch/healthy.go]
    expect: []
```

`ref` is a commit the eval runner can read; `expect` names the lines the rule must flag, and an empty `expect` with `paths` is a negative case. Synthetic cases carry a `patch` instead, so you can write a violation that never existed. [evals/\<rule\>/cases.yaml](../reference/config/eval-cases.md) has the schema and [How to write eval cases](../guides/rules/eval-cases.md) the method.

Run them locally through the gateway, or in the cluster with `--remote` (what CI does):

```bash
exemplar eval --config . --rules close-response-body
```

## 3. Try it on a real pull request

`exemplar try` loads your config with the draft rule added, checks the target repo is eligible, runs only that rule on the PR, and prints the findings. Nothing is posted.

```bash
GITHUB_TOKEN=<token> exemplar try rules/reliability/close-response-body.yaml acme/app#123 --config .
```

The same thing in the browser: open `/ui/playground`, paste the rule, pick a PR you can read, and run it. A finished playground run can be saved as a draft config PR with one click.

## 4. Merge it

Open a pull request in `exemplar-config`. The `validate` job checks the schemas and runs the prompt-injection set; with [config CI](../guides/rules/config-ci.md) set up, the `eval` job evaluates the changed rule in the cluster and fails below the thresholds. A member of the owning team approves (CODEOWNERS), and the merge makes the rule live within seconds: workers refresh their checkout on every push.

Leave it in `shadow` for a week. The rule's page in the web UI shows what it would have posted; when that looks right, change `status: active` in a second PR. From then on its findings are posted, feedback counts toward its precision, and it is demoted back to shadow if people keep rejecting it ([Rule lifecycle](../concepts/rule-lifecycle.md)).

## Next

- [How to write a rule](../guides/rules/write-a-rule.md): compiled rules, severities, exports and linter overlap.
- [How to review mined candidates](../guides/rules/review-candidates.md): the rules the learning plane proposes for you.
