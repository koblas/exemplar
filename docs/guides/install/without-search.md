# How to run without exemplar-search

`exemplar-search` holds a git mirror and a code index for every opted-in repository, and answers the agents' `find_references` and `path_history` tools. It needs local SSD and is the one stateful service besides Postgres. An installation can leave it out: reviews still run over the pull request's own checkout, and what they lose is bounded ([How a review works](../../concepts/how-a-review-works.md#without-exemplar-search)). Run without it for a trial, for a single repository, or until the cross-repository context is worth a search volume.

!!! note "Auto-approve needs search"
    Without search the bug lane's contract class is skipped, and a review with a skipped class never auto-approves. Shadow mode and routed-to-humans outcomes work as usual. Add search before turning on [auto-approve](../operate/auto-approve.md).

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
EXEMPLAR_SEARCH_URL not set: agents get no find_references or path_history, and bug.contract is skipped
```

Reviews run. On a PR the bug lane would have covered, the check run routes to humans with "the bug lane did not run to full coverage" among its reasons, and the rule page in the web UI shows the contract class as skipped.

## Adding search later

Nothing else changes when search is added. Remove the three patches, give the StatefulSet a storage class and size ([How to install for production](production.md#4-write-the-overlay); the quickstart overlay's one replica on 20Gi is enough for a few repositories), apply, and restart the worker once the search pods are ready:

```bash
kubectl --context <your-cluster> -n exemplar rollout restart deploy/exemplar-worker
```

The first index of each opted-in repository takes from seconds to, for a monorepo, an hour; until it finishes, `find_references` answers in degraded mode and says so to the agent.
