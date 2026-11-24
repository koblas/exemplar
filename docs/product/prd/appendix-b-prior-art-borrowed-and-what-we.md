<a id="appendix-b-prior-art-borrowed-and-what-we-deliberately-did-not-take"></a>

## Appendix B — Prior art borrowed, and what we deliberately did not take

| Source | What we borrow | Where | What we don't take |
|---|---|---|---|
| **Google Tricorder** (static-analysis platform) | Effective-FP ≤ 10% bar; "not useful" with reasons; analyzer-owner accountability; findings shown at review time | §9, §13 | — |
| **Google AutoCommenter** (LLM best-practice enforcement) | Doc-linked comments as seeds and citations; IDE + review delivery; volume calibration by replaying history; suppression of outdated practices | §6.1, §8.6, §10, §7 `review_by` | Fine-tuned in-house model (we use general models + rules) |
| **Google ML-suggested edits** (resolving review comments) | Fixes alongside comments; Fixer for human comments | §8.7 | — |
| **Meta Infer / diff-time analysis** | Diff-time and authoring-time beat batch reports | §10 | — |
| **Uber uReview** | Two false-positive classes; separate grader from a different model family; file eligibility filtering; Fixer | §2.2, §8.2, §8.4 | — |
| **Anthropic Code Review** | Parallel specialized agents; verification that attempts to disprove findings; bug-only focus for low FP. *v0.7, from their October 2026 post:* verifier quorum (FR-V7), round cap and class-of-bug findings (FR-C8, FR-C9), clean-PR false-alarm rate (FR-X12), rounds-to-merge and human-wait metrics (§13.2) | §8.1, §8.4, §8.9, §11 | ~20-minute, $15–25 deep reviews as the default path (our default is cheaper, deeper review reserved for risky PRs). Blocking the merge on open findings, and a reviewer that never approves: Exemplar never blocks (§3.2) and approves within deterministic gates (§12, FR-A2). Nightly agents over existing code, and `CLAUDE.md` as review context (rules flow the other way, §10). The rest of that post's ideas are in [`docs/plans/backlog.md`](../../plans/backlog.md). |
| **Will Larson, `small-change`** (Irrational Exuberance, October 2026) | Eligibility as a CI check humans can't edit; author experience in the touched directories as a gate (FR-A10); service count as blast radius with shared libraries excluded (FR-A11); "an error from a fast-tracked change is a system failure" (FR-A12); review's four purposes (knowledge, errors, malice, compliance) each with a better mechanism than reading every diff; the "meat proxy" argument for a centralized reviewer over ad-hoc harness comments | §12, §3.2 | A reviewer that only approves or ignores and never comments: Exemplar posts findings, with verification, budgets and owned rules as the answer to meat-proxy noise. `review-pr-cluster` is in [`docs/plans/backlog.md`](../../plans/backlog.md). |
| **Greptile** | Learning from team comments and reactions; reply-to-teach; codebase-wide context; MCP for coding agents | §6.6, §8.3, §10 | Automatic rule creation without a human gate |
| **CodeRabbit / declarative config tools** | Config-as-code reviewed like code; precision/recall trade-off made explicit per rule | §7, §11 | — |
| **GitHub Copilot code review** | Path-specific instruction files; approval semantics in rulesets | §10, §12 | — |
| **Semgrep / ast-grep / CodeQL** | Deterministic matchers for compiled rules; SARIF | §6.5, §8.6 | Their hosted AI products |
| **GitHub code scanning** | SARIF ingestion and dismissal reasons for security findings | §8.6 | — |
| **Conventional Comments** | Comment labels that make severity legible | §8.6 | — |
| **Palantir policy-bot** | Merge policy as a status check (fallback approval mechanism) | Implementation plan §5.14 | — |
| **Code-review research** (Bacchelli & Bird 2013; Sadowski et al. 2018) | Knowledge transfer and ownership as outcomes to protect | §13.1 | — |

**Reference links:** Uber Engineering blog, *uReview: Scalable, Trustworthy GenAI for Code Review at Uber*; Google Research, *AI-Assisted Assessment of Coding Practices in Modern Code Review* (AutoCommenter, AIware 2024) and *Resolving Code Review Comments with Machine Learning*; Sadowski et al., *Tricorder: Building a Program Analysis Ecosystem* (ICSE 2015); Anthropic, Code Review for Claude Code (March 2026) and *How the Claude Code team does code review* (claude.dev, October 2026); Will Larson, *Speeding up code review with `small-change`* (lethain.com, October 2026); Greptile documentation, *Memory and learning*; GitHub Docs, Copilot custom instructions and code scanning SARIF upload.
