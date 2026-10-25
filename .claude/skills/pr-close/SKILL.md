---
name: pr-close
description: "Close out a branch the way this repository records history: run the gate, write the pull request description (what landed, decisions, how it is tested with the spec §16.1 technique used or skipped and why, what is still missing as filed issues), close the beads issues with a reason, export .beads/issues.jsonl and commit it. Use when a change is ready for a pull request, when a PR description or close reason is missing, or at session end."
argument-hint: "<issue id(s) the branch closes, or nothing to read them from the branch>"
---

# Close out a branch

Close out: $ARGUMENTS

Since 2026-10-24 the plan documents take no new log sections; what landed,
the decisions taken, how it is tested and what is still missing live in two
places only: the pull request description and `bd close --reason`. This
skill writes both from the same facts, so neither is thinner than the other.
Run `bd` from the checkout root.

## 1. Collect the facts

- The issues: the ids given, or those named in the branch (`claude/exemplar-<id>-…`),
  commit bodies and `bd list --status in_progress`. `bd show <id>` each one;
  its Done when line is what to check.
- The change: `git log --format='%h %s' origin/main..HEAD` and
  `git diff --stat origin/main...HEAD`. Read the diff, not the memory of it.
- The tests added or changed, by file, and which spec §16.1 technique each
  is: a simulation invariant (worker state machine), a metamorphic relation
  over `replay --scripted` (review output), a fuzz target (parser of outside
  input; wired into `make fuzz` and `ci.yml`), a seeded property test, a
  configuration-matrix row (new setting), a benchmark (hot path), or
  example-based table tests. Time-dependent tests run in a `testing/synctest`
  bubble; note any that do not and why.
- Whether the change touches `cmd/`, `internal/`, `schema/` or `deploy/`
  without `docs/`: if so, run `/docs` first or say in the description why
  no page changes.

## 2. Run the gate

```bash
make check
```

then the checks the change needs: `go test -race ./<touched packages>`,
`make fuzz` when a fuzz target changed, `make docs` when `docs/` changed,
`go test ./deploy` when manifests changed, `make commitlint`. Report exactly
what ran and its result; never claim a check passed that did not run, and
name the ones skipped (no Docker, no engines) in the description.

## 3. Write the pull request description

Four sections, each a short paragraph or list, in the issue's own words:

```markdown
## What landed
<the behavior or test now in the tree, by package, ending with the issue ids
it closes ("Closes exemplar-9iz.9"); beads ids are not GitHub keywords, so the
line is for readers, and bd close in step 4 does the closing>

## Decisions
<each choice that could have gone another way, and why it went this way; "none" if none>

## How it is tested
<the tests by file and the §16.1 technique each is. Then one line of the form:
"Technique skipped: <name> because <reason>" for the technique the §16.1 table
asks of this change and the PR does not add, or "Technique skipped: none.">
<the checks that ran, and the ones that could not (and why)>

## Still missing
<each gap as a filed issue id with one line; file them with /bd-ticket before
writing this section, linked discovered-from the issue closed here. "Nothing"
only when the Done when line is fully met>
```

The subject line is the merge's first commit subject when the branch is one
commit, otherwise a Conventional Commits subject for the whole (`CONTRIBUTING.md`).
Save the description to `out/pr-body.md` (`/out/` is gitignored) so the
user can read it before anything is pushed.

## 4. Close the issues

For each issue the branch completes:

```bash
bd close <id> --reason "<what landed, with the branch and commit; the decisions; what was checked (seeds, -race, which checks); what is not covered and the issue filed for it>"
```

The reason is the PR description's facts in one paragraph (three to eight
sentences): a reader of `bd show` alone learns what landed, where, how it
was verified and what is left. Name the commit (`fdf0014`) or the PR number
when it exists. An issue the branch only advances stays open: add the
progress as a comment (`bd comment <id> "…"`) and leave its status.

If the Done when line is not met, do not close: say what is missing, file it
with `/bd-ticket` as `discovered-from` this issue, and close only if the
remainder is a separate issue the user agrees to split off.

## 5. Export and commit

From the checkout root:

```bash
bd export -o .beads/issues.jsonl
git diff --stat .beads/issues.jsonl
```

Then ask before committing. The export goes in its own commit,
`chore(beads): close exemplar-<id>` (or `… update exemplar-<id>` when only
progress changed), after the code commits on the branch. Only
`.beads/issues.jsonl` and `.beads/` config are committed; the Dolt database
and `bd sync` / `bd dolt push` are never run without asking.

## 6. Report

Say: the PR title and the body file's path, each issue closed with its id,
each issue filed, the checks that ran with their result, and the checks that
did not run. Opening the PR and pushing are the user's call; when asked, use
`gh pr create --body-file` with the saved body.
