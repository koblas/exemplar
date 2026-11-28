# Feature specs

One file per Full-lane or Light-lane feature, `<slug>.md`, and a retro beside it for
Full-lane features, `<slug>.retro.md`. The product spec (`docs/product/`) holds the
current rules; a feature spec links to it and does not restate it.

A feature spec has four sections:

1. **Intent and out of scope:** what the change is for, and what it deliberately leaves out.
2. **Decisions:** dated and numbered, from the kickoff.
3. **Scenarios:** one per user action, each with a case table (happy case and the
   outcomes that matter: errors, empty and loading states, with their copy). Each
   scenario has a sub-issue under the feature's issue.
4. **Links:** the product spec sections, the feature issue, and the pull requests.

`spec-check.py <slug> --approval`, run once the scenarios are approved, writes the approval
record `**Approved**: <date> scenarios <digest>`. `pipeline.py open-issues <slug>` refuses a
spec without it, or whose scenarios changed after it was written: re-approve and re-run
`--approval`. `open-issues` opens the feature issue and one sub-issue per scenario, labelled
with the lane; the PR closes them with `Closes #N`. Work found and not fixed is an issue draft
(`pipeline.py debt`), filed with `pipeline.py file-issues <slug> --yes`. Both scripts are in the
pipeline plugin's `scripts/`.

A retro (`/pipeline:retro <slug>`) records escapes by class and stage, cost by stage, and rule
candidates.
Run output (plans for large scenarios, check output, metrics) goes to `.runs/<slug>/`,
which is gitignored.
