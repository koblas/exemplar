<a id="10-authoring-time-delivery-shift-left-new"></a>

## 10. Authoring-time delivery (shift left) — *new*

*(Borrowed: AutoCommenter runs in both the IDE and code review; Meta's lesson that feedback is acted on far more at diff/authoring time than in batch reports; commercial reviewers exposing MCP servers to coding agents; path-scoped instruction files for Copilot, Cursor and Claude Code.)*

| ID | Requirement |
|---|---|
| FR-S1 | **Rule exports:** a job generates path-scoped instruction files from active rules with `export.agents: true` — `AGENTS.md` (and nested per-directory files), `CLAUDE.md` , `.github/copilot-instructions.md` plus path-specific `.github/instructions/*.instructions.md` , and `.cursor/rules/*` . Exports are opened as PRs to each repo, owned by the platform team. |
| FR-S2 | Exports respect tool limits (e.g. Copilot code review reads only the first 4,000 characters of an instruction file), so rules are distributed into path-specific files and summarized via `export.summary` . |
| FR-S3 | **MCP server** exposing `list_rules(path)` , `check(diff)` and `explain(rule_id)` so coding agents can retrieve applicable rules and run the engine on their own changes before opening a PR. |
| FR-S4 | **CLI and editor integration:** `exemplar review` on the working tree; an LSP wrapper or editor extension in Phase 5. |
| FR-S5 | Track **upstream effectiveness** : per rule, the rate of violations in PRs authored with agents that received the export versus those that did not. A rule that is exported but still frequently violated needs a better summary. |
| FR-S6 | **Export operations** — *new* . Exports run on a weekly cadence and on any promotion or kill. For opted-in repos, export PRs that touch only the delimited generated sections auto-merge after CI passes; any PR touching hand-written text requires the repo's owners. A dashboard shows, per repo, the config SHA its exports reflect versus the current config SHA, and repos more than two config versions behind are listed in the platform team's digest. Exported files are themselves never auto-approvable (FR-A8). |
| FR-S7 | **MCP and API access control** — *new* . The MCP server and `/v1/evaluate` authenticate callers with org SSO (OIDC) tokens; each caller is identified and rate-limited, and its usage is attributed separately in §13.2. `check(diff)` accepts an explicit base ref so working-tree diffs can be evaluated against a real snapshot; without one it falls back to the default branch head and says so in the result. Callers see only rules for repos they can read. |
