<a id="13-success-metrics"></a>

## 13. Success Metrics

| Metric | Baseline | Target |
|---|---|---|
| Severity-weighted accepted findings (weekly) — *v0.9* | ~21% (generic bot, unweighted, from the source talk — not yet measured in this org; re-measure in Phase 2) | **≥ 65%** |
| Incident-set recall (FR-X11 set replayed against current config) — *v0.9* | 0% | **≥ 50%**, rising as each incident adds a case |
| Effective false-positive rate per rule | — | **≤ 10%** for every active rule |
| Accepted findings vs. in-house baseline reviewer (FR-X8) | baseline | ≥ 2× precision |
| LGTM recall on core constraints | human baseline | > human baseline |
| Share of findings with an applied suggestion | 0% | ≥ 25% of accepted findings |
| Share of PRs merged fully AI-reviewed | 0% | 5% at launch, then grow |
| Violations per PR for exported rules (G6) | measure at export | falling quarter over quarter |
| Median time-to-merge, auto-approve-eligible PRs | measure | material reduction |

**Definitions.** A finding is *accepted* if the author applied the suggestion, marked it Fixed, or changed the anchored lines in a way the light-model check (FR-B3) judges to address it. It is *rejected* if dismissed with a reason or 👎. It is *ignored* if the PR merged with it untouched. Acceptance rate = accepted ÷ (accepted + rejected + ignored); ignored findings are in the denominator. Findings withheld as human duplicates (FR-E15, FR-B8) count toward rule precision but not toward the acceptance rate, since no one saw them.

<a id="131-counter-metrics-new"></a>

### 13.1 Counter-metrics — *new*

Borrowed from code-review research (Bacchelli & Bird; Sadowski et al. on code review at Google): review's biggest benefits are knowledge transfer and shared ownership, not only defect finding. Auto-approve and fewer human comments can quietly erode both.

| Counter-metric | Alarm |
|---|---|
| Change-failure rate and revert rate of auto-approved PRs vs. human-approved PRs | Any statistically significant increase |
| Incidents linked to PRs where Exemplar posted no findings and a human approved quickly | Trend up |
| Share of a CODEOWNER's paths changed with no human from that team seeing the PR (per quarter) | > agreed threshold per team |
| Human reviewer comment volume on substantive (design) topics | Falls with acceptance flat — reviewers may be over-trusting the bot |

<a id="132-informational-metrics-new"></a>

### 13.2 Informational metrics (tracked, not targeted) — *new*

These are reported on every review (§17.4) and in dashboards so the platform team can see trends and catch regressions, but they are not goals, have no targets, and are not exit criteria for any phase. A phase that hits its precision bar at a higher cost than expected still exits.

| Metric | Why we track it |
|---|---|
| Cost per run, and per PR lifetime (FR-C7), by lane and stage | Sizing, provider negotiation, spotting a rule or PR shape that is unexpectedly expensive |
| Time to first comment, and run duration | Authors notice slowness; a sudden change usually means a dependency (context service, provider, CI wait in FR-E7) regressed |
| Coverage per run, and share of runs degraded or incomplete (§8.10) | Degradation that becomes routine is a precision problem in disguise |
| Queue depth and wait time per repo (FR-D6) | Capacity planning |
| Usage and cost by caller (production, CLI, MCP, agents — FR-S7) | Attribution; a runaway agent loop shows up here first |
| Raw (unweighted) acceptance rate — *v0.9* | Comparability with the ~21% generic-bot figure and with other tools' numbers; the weighted rate in §13 is the goal |
| Review rounds to merge per PR (FR-C8): mean, and the share needing five or more — *v0.7* | Whether the bot converges. Anthropic reports 2.08 → 1.32 mean rounds and 8.9% → 1.5% needing five or more after adding convergence controls; a rising figure here means findings are not being fixed in one pass |
| Human approvals per PR (median and p95), and time waiting on a person — *v0.7* | What auto-approve and fewer human comments actually buy. Paired with the §13.1 counter-metrics: time saved that reappears as fixing time was not saved |
