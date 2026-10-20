# How to run Exemplar from a coding agent

Coding agents meet Exemplar's rules twice: as instructions before they write code, and as a check before they open a PR. Both come from the same merged rules as the PR review.

## 1. Read the exported rules

When a repository opts in to exports ([How to enable rule exports](../operate/rule-exports.md)), its active rules' summaries are rendered between `BEGIN`/`END exemplar` markers in the instruction files the repository chose: `AGENTS.md`, `CLAUDE.md`, GitHub Copilot's or Cursor's. Agents that read those files get the rules for free; nothing to configure on the agent.

Don't edit inside the markers: a hand-edited block is left alone by the next export and reported to the platform team, so the agent would keep reading stale rules.

## 2. Run the pre-PR check

Give the agent a signed-in CLI ([How to review before pushing](review-before-pushing.md)) and tell it, in its instruction file or its task, to run before opening a PR:

```bash
exemplar review --agent claude_code
```

`--agent` (or `EXEMPLAR_AGENT`) records which agent ran the review, so the shift-left board can show whether exported rules reduce findings per PR. The command exits 1 when there are findings, so an agent loop can fix and re-run until it exits 0; `--format sarif` gives a machine-readable result.

For an unattended agent, sign in once as a service instead of a person: set `EXEMPLAR_API_URL` and `EXEMPLAR_TOKEN` to an Auth0 access token for exemplar-api with `evaluate:<owner>/<repo>` permission, or a GitHub Actions OIDC token from a workflow in that repository ([How to set up sign-in](../install/sign-in.md#service-callers)). Service callers are recorded as `caller=agent`, rate-limited and held to a daily budget.

## 3. Know what the agent will meet in the PR

The PR review runs the same engine, so a diff that passes `exemplar review` gets no new findings from the same rules at the same base. Findings the agent dismisses in the PR thread with `/exemplar wrong` are honoured only from the PR's author, its reviewers and the rule's owners; an agent commenting under its own identity is an observer and its feedback is not recorded.
