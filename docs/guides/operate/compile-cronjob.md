# How to enable the compile CronJob

The `exemplar-learn-compile` CronJob asks the high-tier model for an ast-grep or Semgrep pattern per candidate rule and runs that model-written pattern over the candidate's eval cases. Because the pattern was written by a model, it runs only inside a sandbox: `unshare --net --map-root-user` plus `prlimit` (4 GiB data and 120 s CPU per engine run by default, `--sandbox-memory` and `--sandbox-cpu`; Semgrep runs one job). Unprivileged user namespaces are normally refused under RuntimeDefault seccomp at the `restricted` Pod Security level, so the CronJob ships **suspended**.

Without it, candidates are proposed as `kind: exemplar` rules only; nothing else depends on it.

## 1. Provide user namespaces

Give the pod user namespaces, for example `hostUsers: false` on the pod spec with a seccomp profile that allows `unshare`, on a cluster whose runtime supports them.

## 2. Unsuspend

In your overlay, patch the CronJob:

```yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: exemplar-learn-compile
  namespace: exemplar
spec:
  suspend: false
```

## 3. Check a run

The job's log records every attempt; an accepted compile makes the candidate PR `kind: compiled`. If `unshare` fails, the job refuses to run the pattern: that is the sandbox doing its job.

!!! danger "Never turn off `--require-sandbox` instead"
    The flag exists for development on a machine without user namespaces. In a cluster it would run model-written code with the pod's network and identity.
