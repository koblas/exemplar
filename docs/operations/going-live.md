# Going live

1. **A first review.** Open a pull request in the shadow repo. The worker reviews it and stores the result, but posts nothing. When the findings look right, set `shadow: false` in that repo's file in `exemplar-config` and merge. The next push gets the `exemplar` check run and review comments.
2. **Feedback.** On a posted finding, reply `/exemplar wrong <why>` (or use its feedback link). The finding's page in the web UI shows the event.

If something is wrong, start with `/ui/setup`, then the logs. Every service logs JSON to stdout, and never logs credentials or code:

```bash
kubectl --context <your-cluster> -n exemplar logs deploy/exemplar-worker
```
