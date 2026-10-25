# Claude Code Toolkit

Start `claude` from the repository root. Claude Code loads `.claude/CLAUDE.md`
and `.claude/settings.json`; check `/memory` and `/permissions` to inspect them.
The scaffolding provides guidance, not an executable service or Kubernetes chart.

## Workflows

```text
/go-service add readiness and graceful shutdown
/k8s-manifests add a Deployment and ClusterIP Service
/service-review review the working tree
```

Skills live under `skills/<name>/SKILL.md` and can also load automatically when
their descriptions match a request. The review skill is manually invoked.

Path-scoped coding rules live under `rules/` and load when Claude works on
matching files: `rules/go-code.md` covers time and clocks in Go code and tests.

## Permissions

Shared settings pre-approve Go checks, formatting, and selected Git inspection.
Tests and builds execute project code; these approvals assume a trusted checkout.
kubectl and Helm commands require approval, including read-only/local operations.
Dependency changes, commits, pushes, and image publishing also require approval.
No MCP servers or model overrides are configured.

## Session Environment

The `SessionStart` hook runs `devenv print-dev-env --json` from the project
root and uses `jq` to append shell-quoted exports to `CLAUDE_ENV_FILE`. Subsequent Claude
Bash commands load that file, including the devenv PATH and configured variables.
It runs on each session-start event, including resume and compaction, without
opening an interactive shell. Existing exports from other hooks are preserved.
Devenv and `jq` must be on Claude's inherited PATH; failures are reported as hook errors.
Restart Claude to load the environment through the new hook.

The `.env` read rules are a guardrail, not a secret isolation boundary: shell
commands and other tools may access files, and other settings can grant additional
permissions. Never place secrets in prompts or tracked files. Review permissions
before using this toolkit with untrusted code.

Store personal overrides in `.claude/settings.local.json`, which is ignored here.
Do not put tokens, cluster credentials, or machine-specific paths in shared settings.

Devenv includes Go 1.27 and golangci-lint v2. Kubernetes and container tooling still
need to be added separately when required. No application tests or lint checks can
run until a Go module exists. Replace the suggested layout and commands in CLAUDE.md
as actual repository conventions are established.