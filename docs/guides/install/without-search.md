# How to run without exemplar-search

Exemplar reviews without `exemplar-search`. Every rule runs over the pull request's own checkout, findings are verified and posted, and feedback, budgets and shadow mode work as usual. Without it, `find_references` and `path_history` answer from each review's own checkout; search adds the installation's other repositories, an index, and history from its mirror. It needs local SSD and is the one stateful service besides Postgres, so leave it out for a trial or a single repository, and add it when cross-repository context is worth a search volume ([How a review works](../../concepts/how-a-review-works.md#without-exemplar-search)).

!!! note "Auto-approve"
    A repository with no `context.extra_repos` can be auto-approved without search: its callers are all in the checkout. One that names other repositories has the bug lane's contract check skipped without search, so its PRs are routed to humans, never auto-approved; add search before turning on [auto-approve](../operate/auto-approve.md) there. Reviews and posted findings are not affected either way.

## 1. Leave the search workload out

Add these patches to the overlay (the quickstart's `kustomization.yaml`, or your production one). They remove the StatefulSet and its two Services and set the worker's search URL to empty:

```yaml
patches:
  - target: {kind: StatefulSet, name: exemplar-search}
    patch: |-
      $patch: delete
      apiVersion: apps/v1
      kind: StatefulSet
      metadata: {name: exemplar-search, namespace: exemplar}
  - target: {kind: Service, name: "exemplar-search.*"}
    patch: |-
      $patch: delete
      apiVersion: v1
      kind: Service
      metadata: {name: placeholder, namespace: exemplar}
  - target: {kind: ConfigMap, name: exemplar-settings}
    patch: |-
      - {op: replace, path: /data/EXEMPLAR_SEARCH_URL, value: ""}
```

The search service account, NetworkPolicy and PodDisruptionBudget stay in the render; with no pods to select they do nothing. No search Secret is needed: the search token derives from the root key and is read only when the URL is set.

## 2. Render and apply

```bash
kubectl kustomize <overlay> > rendered.yaml
```

Check that `rendered.yaml` has no `StatefulSet` named `exemplar-search` and that `exemplar-settings` has `EXEMPLAR_SEARCH_URL: ""`, then apply as in [Quickstart, step 5](quickstart.md#5-apply-it).

## 3. Check it

The worker logs one warning at start:

```text
EXEMPLAR_SEARCH_URL not set: find_references and path_history answer from each review's checkout; other repositories are not searched
```

Open a pull request: the review runs and its findings appear in the web UI (and on the PR once the repository leaves shadow). Every check run ends with "Cross-repository callers were not checked (search is not configured)". On a repository with `context.extra_repos`, a PR the bug lane covers also lists "the bug lane did not run to full coverage" among its reasons: the contract class above, not a fault.

## Adding search later

Nothing else changes when search is added. Remove the three patches, give the StatefulSet a storage class and size ([How to install for production](production.md#4-write-the-overlay); the quickstart overlay's one replica on 20Gi is enough for a few repositories), apply, and restart the worker once the search pods are ready:

```bash
kubectl --context <your-cluster> -n exemplar rollout restart deploy/exemplar-worker
```

The first index of each opted-in repository takes from seconds to, for a monorepo, an hour; until it finishes, `find_references` answers in degraded mode and says so to the agent.
