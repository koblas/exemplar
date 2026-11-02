# End-to-end demo kit

The kit for the live demo designed in [`docs/plans/demo.md`](../../docs/plans/demo.md): a sandbox repository, the review history the learning plane mines, the PRs each beat opens, and a driver (`kit/`) that plays the demo's three people through the GitHub API. This page is the runbook: what to set up, then each phase in order and what the audience should see.

Every kit phase can be run again after a failure; it skips what is already done. `-dry-run` prints a phase's plan without calling anything. Cluster commands are written out with `--context` and `-n`: check both before running them.

## 1. Before you start

From outside this repository:

- **A sandbox GitHub org** with two empty repositories: `ledger` (the reviewed repo) and `exemplar-config`.
- **Three accounts in the org**, each with write access to `ledger`. Each needs a token with repository and pull-request write on it.

  | Role | Environment variable | What it does in the demo |
  |---|---|---|
  | author | `EXEMPLAR_DEMO_AUTHOR_TOKEN` | opens and merges every PR; the auto-approvable author |
  | owner | `EXEMPLAR_DEMO_OWNER_TOKEN` | first reviewer; owns the demo rule and every path in `ledger` |
  | reviewer | `EXEMPLAR_DEMO_REVIEWER_TOKEN` | second reviewer |

  Three distinct people matter. Ingest ignores a PR author's own comments, and demotion needs two non-author reviewers.
- **Model keys**: Anthropic (generator, verifier, extraction), OpenAI (the categorizer's second family, and the first verifier), and Voyage (embeddings).
- **A cluster**: OrbStack's Kubernetes (context `orbstack`), with the in-cluster Postgres component and no `exemplar-search`.
- **A Cloudflare account** and a hostname for the tunnel.
- **Grafana**, installed beside the release ([Dashboards](../../docs/reference/dashboards.md) describes the provisioning).

## 2. Run order

The order matters. Seed history must be merged before ingest first runs. Ingest reads merged PRs, and it never goes back to a PR it has read. Push it before the App is installed, so the seed PRs are not reviewed at model cost.

### 2.1 The sandbox repository and its history

```bash
go run ./examples/demo/kit -phase repo -org <org>
```

```bash
go run ./examples/demo/kit -phase seed -org <org>
```

`seed` opens three PRs as the author. Each reviewer leaves inline comments that state two of the team's conventions (log through `logctx.From(ctx)`; money in int64 cents), and the author merges each PR.

Then protect `ledger`'s `main`:
- no required code-owner review;
- if you require an approval, dismiss stale approvals on push, so the App's approval can count.

### 2.2 Install Exemplar

Follow the [quickstart](../../docs/guides/install/quickstart.md) with the in-cluster Postgres component and [without search](../../docs/guides/install/without-search.md), on the `orbstack` context in the `exemplar` namespace.
- Put all three model keys in the `exemplar-providers` Secret: `anthropic-api-key`, `openai-api-key` and `voyage-api-key`.
- Create the App with the setup page and install it on `ledger` and `exemplar-config`.

Render the demo's config and push it to `exemplar-config`:

```bash
go run ./examples/demo/kit -phase config -org <org> -out out/demo-config
```

```bash
git -C out/demo-config init -b main
```

```bash
git -C out/demo-config add -A && git -C out/demo-config commit -m "Demo configuration"
```

```bash
git -C out/demo-config push https://github.com/<org>/exemplar-config.git main
```

The render is `examples/exemplar-config` plus:
- a repo file for `ledger`: posting on, native approve, auto-approve for `README.md` and `docs/**` from the author;
- the noisy `demo-todo-comments` rule, owned by the owner, with ten eval cases (the example's `min_cases`);
- a CODEOWNERS naming the owner;
- in `global.yaml`: `auto_approve: true`, the categorizer on Claude and OpenAI, OpenAI first among verifiers, and every head sampled for the baseline.
- also in `global.yaml`: `verifier.importance_min` at 0.3 instead of 0.5. The demo rule is noise by design, and at 0.5 the verifier's importance check may hold its findings back before anyone can reject them. A real install keeps 0.5.

Two settings on `exemplar-config` are needed:
- Its ruleset must let the config-writer App merge its own demotion PRs.
- Its eval workflow needs the secrets named in `.github/workflows/eval.yml`.

Check that the App's approval counts toward the branch rules. Open any throwaway PR on `ledger` with no approvals and probe it with the review App's credentials (from the setup page's Secret). The JSON it prints names the `approval_mode` to keep in the repo file; the render sets `native_approve`.

```bash
go run ./cmd/exemplar sandbox probe-approval --repo <org>/ledger --pr <number> --app-id <app id> --installation-id <installation id> --private-key-file <key.pem>
```

### 2.3 Beat C: mining with citations

Run the learning jobs in order. Wait for each to finish (`kubectl --context orbstack -n exemplar get jobs`) before starting the next.

```bash
kubectl --context orbstack -n exemplar create job --from=cronjob/exemplar-learn-ingest demo-ingest
```

Extraction runs twice. The first run submits the batch, and the second, a few minutes later, collects it:

```bash
kubectl --context orbstack -n exemplar create job --from=cronjob/exemplar-learn-extract demo-extract-1
```

```bash
kubectl --context orbstack -n exemplar create job --from=cronjob/exemplar-learn-extract demo-extract-2
```

Then the remaining stages:

```bash
kubectl --context orbstack -n exemplar create job --from=cronjob/exemplar-learn-embed demo-embed
```

```bash
kubectl --context orbstack -n exemplar create job --from=cronjob/exemplar-learn-cluster demo-cluster
```

```bash
kubectl --context orbstack -n exemplar create job --from=cronjob/exemplar-learn-rank demo-rank
```

```bash
kubectl --context orbstack -n exemplar create job --from=cronjob/exemplar-learn-propose demo-propose
```

**Show:**
- candidate rule PRs in `exemplar-config` for the logging and money conventions, each citing the seed comments it came from and routed to the owner;
- the candidates in `/ui/`.

### 2.4 Beats A and B: findings, and approve or route

```bash
go run ./examples/demo/kit -phase prs -org <org>
```

**Show:**
- **Beat A:** *Add the refund endpoint* gets one review, with inline comments for `propagate-context` (`http.Get` in a handler) and `no-raw-sql-concat`. Each comment carries its label and rule link, and a suggestion where the fix is local.
  - The check run's held-back counts show what the verifier refuted. `notify.go` passes `ctx` correctly and is the bait for a refuted finding.
  - The lanes board's *Verifier refutation by rule* shows the same.
- **Beat B:** *Fix a typo in the README* is approved by the App with no human. Approval needs the bug lane to have covered every changed file, the README included; if its check run shows the bug lane incomplete, that is the reason. *Expire sessions after twelve hours* touches `internal/auth`, so its check run says it was routed to humans and gives the gate reasons.

### 2.5 Beat D: feedback, demotion, teach-the-bot and boards

```bash
go run ./examples/demo/kit -phase feedback -org <org>
```

`feedback` opens volume PRs that each add three TODOs. It waits for the bot's `demo-todo-comments` findings, has both reviewers review each PR, and replies `/exemplar wrong …` to each finding, taking turns between the two reviewers. A finding counts once the worker acknowledges the reply with its 👍. The phase stops once 30 findings are judged; demotion needs 30 (spec §9.4). If two volume PRs in a row get no demo-rule finding, it stops and points at the check run's held-back counts.

**Show:** a finding's page in `/ui/` lists each reply within a minute, with the reviewer's role and weight.

Teach next, while the rule is still active: once it is demoted to shadow it posts nothing, and the legacy PR would get no finding to teach from.

```bash
go run ./examples/demo/kit -phase teach -org <org>
```

```bash
kubectl --context orbstack -n exemplar create job --from=cronjob/exemplar-learn-teach demo-teach
```

**Show:** the owner's *not important here* on a finding under `internal/legacy` opens a draft PR. It adds `!internal/legacy/**` to the rule's `file_patterns`.

Then demote:

```bash
kubectl --context orbstack -n exemplar create job --from=cronjob/exemplar-learn-demote demo-demote
```

**Show:** an auto-merged `exemplar-config` PR that moves `demo-todo-comments` to `shadow`, and the rule flagged over the eFP bar in `/ui/`.

**Show** the boards:
- `rule-health.json`: per-rule acceptance and eFP;
- `baseline.json`: Exemplar against the baseline reviewer on the same heads (every head is sampled in the demo config).

## 3. What is in the kit

| Path | What it is |
|---|---|
| `kit/data/_repo/` | The sandbox repository's base tree: a small Go service. `.tmpl` files are rendered with the org's names. |
| `kit/data/_config/` | The demo's additions to `examples/exemplar-config`. |
| `kit/data/seed.yaml` | The review history for beat C. |
| `kit/data/prs.yaml` | The beat A and B PRs. Beat D's PRs are generated (`volumePR`, `teachPR`). |
| `kit/*.go` | The driver. Its tests check the seed data against ingest's own filter, the rendered config against the config loader, and the seed and feedback phases against a fake GitHub. |
