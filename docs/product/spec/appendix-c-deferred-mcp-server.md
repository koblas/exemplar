## Appendix C — Deferred: MCP server

**Status: deferred.** Removed from the initial scope in Draft 1.3 (see §10.3). The design below is kept so it can be picked up unchanged once Phases 0–4 are stable; nothing in Phases 0–5 depends on it. The engine it exposes is the same `/v1/evaluate` used by the CLI, so deferring it costs no functionality — only a protocol surface for agents that cannot run a shell command.

**When to pick it up:** after Phase 4 exports are stable, when a coding-agent tool in use (input #13) cannot invoke the CLI and its users ask for it. Prerequisites: SSO device flow and per-user budgets from §10.1 already in production; per-identity rate limiting in `exemplar-api`.

- Implemented with the official Go SDK (`github.com/modelcontextprotocol/go-sdk`), served two ways: **remote** (Streamable HTTP, mounted in `exemplar-api`, OAuth via org SSO) and **local** (an `exemplar mcp` CLI subcommand over stdio, authenticated with the CLI's device-flow token). The subcommand is added to the CLI only when this appendix is activated.
- **Tools:**

| Tool | Input | Output |
|---|---|---|
| `list_rules` | `path` or glob | Applicable rules: id, severity, one-line summary, doc link |
| `explain` | `rule_id` | Description, preferred approach, positive/negative examples |
| `check` | unified diff (+ optional file contents) and repo | Findings from Lanes 1–2 (+ Lane 3 if requested), verified, with suggestions |

- **Access control** (PRD FR-S7): MCP and `/v1/evaluate` require org SSO (OIDC) tokens; each caller is identified, rate-limited per identity, and attributed separately in `model_calls.caller`. Rules and results are returned only for repos the caller can read (checked via GitHub user OAuth).
- `check` accepts an explicit `base_ref`; diffs are evaluated against that snapshot. Without one, it uses the default-branch head and the result says `base: default-branch-head`.
- Calls are recorded with `caller=mcp`, the user identity, and the agent's declared client name.
- `check` input is the agent's own diff — untrusted like any other — and runs through the identical pipeline. Per-user budgets apply; default `check` cost cap $1.

**Also activated with this appendix:** the `internal/mcp/` package; `github.com/modelcontextprotocol/go-sdk` in the stack (§14); the `mcp` caller value (already reserved in `reviews.caller` and `model_calls.caller`); `budgets.mcp_check_usd` in `global.yaml`; the MCP-usage panel on the shift-left board (§12.5); and the threat-model row below.

| Threat | Control |
|---|---|
| MCP `check` abused for cost or data exfiltration | Per-user budgets and rate limits; results only for repos the user can read (checked via GitHub OAuth); diffs are untrusted input |
