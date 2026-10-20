# How to write eval cases

Every rule carries labelled cases in `evals/<rule>/cases.yaml`. Config CI runs them on each PR that changes the rule and fails below `eval_thresholds` in `global.yaml` (precision, recall, and `min_cases` scored cases). The file format is on [evals/\<rule\>/cases.yaml](../../reference/config/eval-cases.md).

## 1. Collect positives from history

A historical case points at a real commit the eval runner can read and names the lines the rule must flag:

```yaml
rule: propagate-context
cases:
  - name: http.Get in a handler
    ref: acme/app@9f3c2b1a0d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a
    expect:
      - path: internal/api/users.go
        lines: [88, 88]
```

`ref` is `owner/repo@sha`. The runner reads the file with the token in `EXEMPLAR_EVAL_TOKEN` (read-only), or in the worker with `--remote`. Mined candidates arrive with positives taken from the comments they came from.

## 2. Add negatives

A case whose `expect` is empty is a negative: the rule must flag nothing in the listed `paths`. Negatives need `paths` because there is no expected line to infer them from:

```yaml
  - name: context passed through
    ref: acme/app@9f3c2b1a0d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a
    paths: [internal/api/health.go]
    expect: []
```

Put the false positives you named in `what_to_flag` here, one each: `main()`, tests, the helper that already closes the body.

## 3. Write synthetic cases for what history lacks

A synthetic case carries a `patch` applied on top of `ref`, so you can write a violation (or a near miss) that never existed:

```yaml
  - name: synthetic context.Background in handler
    ref: acme/app@9f3c2b1a0d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a
    synthetic: true
    patch: |
      --- a/internal/api/users.go
      +++ b/internal/api/users.go
      @@ -85,3 +85,4 @@
      +  resp, err := http.Get(url)
    expect:
      - path: internal/api/users.go
        lines: [88, 88]
```

The runner reports historical and synthetic precision and recall separately. Synthetic cases are skipped by the linter-overlap check, since no CI ran on them.

## 4. Generate bug-class cases by mutation

For the bug lane's classes, `exemplar mutations` writes recall cases by applying deterministic mutation operators (drop an error check, flip `<` to `<=`, drop a `Close`, remove a lock pair or a nil guard) to Go, C and C++ files in a checkout's `bug_lane.critical_paths`:

```bash
exemplar mutations --config path/to/exemplar-config --checkout path/to/repo --repo acme/app --dry-run
```

Without `--dry-run` it merges the cases into `evals/<bug class>/cases.yaml`, each with its patch. Review and commit the result.

## 5. Run them

```bash
exemplar eval --config . --rules propagate-context
```

Locally this needs the gateway (`EXEMPLAR_GATEWAY_URL`, `EXEMPLAR_GATEWAY_TOKEN`) for exemplar rules; compiled rules need only the engines. `--remote` runs them in the cluster through `/v1/evaluate` as CI does ([How to set up config CI](config-ci.md)). `--markdown` writes the summary table, `--json` the per-rule reports.

The learning plane's `evals` CronJob generates cases for mined candidates in the same format: positives from source comments, negatives from CODEOWNER-approved PRs, and injected violations validated in code.
