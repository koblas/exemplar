# How to troubleshoot an installation

Start with `/ui/setup`, then the logs. Every service logs JSON to stdout, and never logs credentials or code:

```bash
kubectl -n exemplar logs deploy/exemplar-worker
```

Set `EXEMPLAR_LOG_LEVEL=debug` on a workload for more.

## Pods wait on a missing Secret

On a fresh install the worker, search, learn and git-sync containers wait in `CreateContainerConfigError` or `ContainerCreating`, naming `exemplar-root-key` or a key of `exemplar-github-app`, until setup finishes ([Quickstart, step 5](../install/quickstart.md#5-register-the-github-app)): setup writes the root key last, when you click **Finish setup**. They start on their own once it's there.

A setup page saying the `exemplar-setup` Secret (or another Secret setup writes) isn't in the cluster means the manifests weren't all applied: apply your overlay again. The Secrets are declared empty in the manifests, and applying them again keeps what setup wrote.

A pod that refuses to start naming a value such as `example-org` or `exemplar.example.invalid` still has a placeholder from the example overlay in `exemplar-settings`.

## The gateway isn't ready

A provider in `global.yaml` has no credentials: the gateway's log names the variable (`anthropic-api-key` in `exemplar-providers` for `claude`, `openai-api-key` for `openai`; workload identity for `bedrock`). The gateway skips a provider whose credentials are missing with a warning, and fails only when a stage is left with none. The embedding provider is the exception: without `voyage-api-key` the gateway starts anyway, logs `embedding provider skipped`, and `POST /v1/embeddings` answers 503, so the `exemplar-learn-embed` CronJob fails and nothing reaches clustering until the key is set.

## No review arrives

Work down the chain:

1. **Did GitHub deliver?** `/ui/setup` reads GitHub's delivery log for the App's webhook. A failed delivery with a connection error means the host isn't reachable: with the tunnel, check the `exemplar-cloudflared` pods and `cloudflared tunnel route dns`; with an Ingress, the host's DNS and the `exemplar-tls` Secret. A 401 means the webhook secret in `exemplar-github-app` doesn't match the App's. The rejection log's `remote` behind the tunnel is a cloudflared pod's address, not GitHub's.
2. **Was it stored?** `/ui/setup` also counts the events stored. If deliveries arrive but nothing is stored, the webhook pod's log says why (database, signature).
3. **Was it routed?** The worker's log shows the `gh_event` job and what it enqueued. A repository with no file under `repos/` in `exemplar-config`, with `enabled: false`, or on the eligibility denylist is skipped silently. Draft PRs need `review_drafts: true`; fork PRs need `forks.review: true`.
4. **Did the review run?** The check run on the PR says `in_progress`, then neutral with the summary. In a `shadow: true` repo there is no check run: look at the rule pages in the web UI.

The `sweep_prs` periodic job re-enqueues open PRs the worker missed, and `reconcile_deliveries` re-reads GitHub's delivery log, so a short outage catches up on its own.

## The review did not complete

The check run is neutral with "Review did not complete" and a **Re-run** action. Its summary gives the reason: every provider for a stage failed, the diff could not be redacted, or the review runner failed on every retry (out of memory, evicted, or crashing on this PR; the worker logs `review abandoned after its last attempt`). Nothing was approved; a runner that failed may have posted its comments first, and a Re-run does not post them again. Fix the cause (usually the gateway or the provider) and press **Re-run**, or push again.

## Findings are stored but not posted

The finding's page in the web UI shows its state and, when suppressed, the reason: refuted by the verifier, below `importance_min`, a linter duplicate, agreed by a human comment, over the comment budget, held back by the round cap (listed in the "review rounds" comment), or the rule is in shadow. A rule that was skipped as one too many shows up in the check run as "not run on this review".

## New findings appear only in a "review rounds" comment

The PR passed its round limit: `rounds.max` pushes (three by default) each raised new findings with no review from a person in between. New findings except blocking and Security ones are listed in one summary comment until someone reviews the PR, comments on it, or it leaves draft; then they post inline on the next push. If it happens often for one rule, its findings or suggestions are probably not converging: look at the rule's acceptance on the rule-health board. A repo can lower the limit with `rounds.max` in its repo file ([reference](../../reference/config/global.md#rounds)).

## `exemplar-search` is behind or missing

If the installation runs without search on purpose (`EXEMPLAR_SEARCH_URL` empty; [How to run without exemplar-search](../install/without-search.md)), the worker says so once at start, every check run ends with "Cross-repository callers were not checked (search is not configured)", and on a repository with `context.extra_repos` the bug lane's contract class is skipped; that is expected, not a fault. Otherwise:

Responses switch to degraded mode (`git grep`, tighter caps) and the bug lane's contract class is skipped, which blocks auto-approve. Check the fetch and index logs on the StatefulSet. Delete a replica's volume to force a clean rebuild; the other replica keeps serving. With both replicas down, the context tools return errors the agents see, the bug lane is skipped, and the exemplar lane continues with the PR checkout only; reviews are not blocked.

## Feedback links fail

Slash commands and reactions keep working, so no feedback is lost beyond the links. Check exemplar-api: the link key must match between api and worker (both derive it from the root key unless `EXEMPLAR_LINK_KEY_FILE` overrides it on one of them), and the person clicking must be signed in with a GitHub identity.

## Sign-in fails

- GitHub: the person is not a member of an allowed organization (`EXEMPLAR_OAUTH2_GITHUB_ALLOWED_ORGS`, defaulting to the config repo's owner), or the App's callback URL doesn't match `EXEMPLAR_API_URL` plus `/oauth/callback`.
- OIDC: the issuer must serve discovery at `EXEMPLAR_OIDC_ISSUER_URL`; the redirect URI must be `https://<api>/oidc/callback`; the email domain and allowed groups must match. An OIDC user who sees no repositories has no group in `EXEMPLAR_OIDC_GROUP_REPOS`.

## exemplar-api stays in setup mode

After **Finish setup**, every exemplar-api pod reads the Secrets setup wrote and leaves setup mode within 10 s. If the page keeps showing **Setup is complete**, the exemplar-api log says why:

- `setup: reading the installation's Secrets failed`: the pod can't reach the Kubernetes API. Check the `exemplar-api-setup` Role and RoleBinding, and that the exemplar-api NetworkPolicy allows the API server's address ([Workloads, ports and probes](../../reference/workloads.md)).
- `EXEMPLAR_DATABASE_URL is required` or a database error: `exemplar-database` has no working URL ([Secrets and ConfigMaps](../../reference/secrets-and-configmaps.md)).

Restarting exemplar-api also finishes it: a new pod reads the same Secrets.

## exemplar-api crash-loops after setup

Once `exemplar-github-app` has an `app-id`, exemplar-api starts in App mode, and the setup page is gone. Its log names what's missing:

- `EXEMPLAR_DATABASE_URL is required`: create `exemplar-database` with your Postgres URL ([Secrets and ConfigMaps](../../reference/secrets-and-configmaps.md)). The pod starts on its own once the Secret has it.
- `link key: … exemplar-root-key`: `exemplar-root-key` has no `key`. Setup writes it at **Finish setup**; otherwise set one (`openssl rand -hex 32`).
- GitHub refusing the App (in the log, or on `/ui/setup`): `exemplar-github-app` holds another App's key or a revoked one.

To run setup again, empty the App's Secret and restart exemplar-api, which then serves the setup page:

```bash
kubectl -n exemplar patch secret exemplar-github-app --type=json -p '[{"op":"remove","path":"/data"}]'
```

```bash
kubectl -n exemplar rollout restart deployment/exemplar-api
```

Delete the old App in your GitHub settings, then follow [Quickstart, step 5](../install/quickstart.md#5-register-the-github-app). Secrets that already hold values (root key, database) keep them.

## Setup says exemplar-api couldn't reach its Secrets

Setup reads and patches its Secrets through the Kubernetes API with the pod's own service account. The page names the cause; the exemplar-api log has the API server's answer. A `Forbidden` means the `exemplar-api-setup` Role or RoleBinding is missing or names another namespace; a timeout means the NetworkPolicy, or an overlay that narrowed it, doesn't reach the API server.

## Config CI is red on a fresh copy

Only `validate` should run. The other jobs run when their repository variables exist; a red `eval`, `volume`, `linter-overlap` or `verifier-calibration` job on a fresh copy means a variable was set without its secrets ([How to set up config CI](../rules/config-ci.md)).
