---
name: issue
description: "File or reshape a GitHub issue the way this repository tracks work: title, Why, Design ids, State, Approach, Done when, parent epic (sub-issue), type, phase and order labels, and blocked-by dependencies. Use when work is found during a task, when a spec section or PRD requirement needs an issue, when a debt is deferred, or when an existing issue lacks a backlink or a done condition."
argument-hint: "<what the issue is about, or an existing #number to reshape>"
---

# File a GitHub issue

File or reshape: $ARGUMENTS

GitHub Issues on `koblas/exemplar` is the only work tracker; `.claude/CLAUDE.md`
"Work Tracking" has the rules this skill applies. `docs/product/` stays the source
of truth for the design: an issue says in one line why the work matters, which
spec ids hold the design, what is true in the code today, how to get there, and
when it is done. It reads on its own and never restates the spec.

## 1. Find the design

The body must make sense to someone who never opens the repository, so it says
why in its own words and names the design by stable id, never by file path or
heading anchor (files get split and headings renamed; ids do not move):

| Work comes from | Design line |
|---|---|
| A spec section | `spec §6.4.6` (and the sub-heading or bullet in parentheses, e.g. `(Chaos)`) |
| A PRD requirement | `FR-…` in the title; `FR-…` and the spec section that implements it |
| An acceptance test or deliverable | `T-0.3`, `D6.5` |
| An unadopted idea | Where it came from (a post, a review, a retro) |
| Something the spec does not cover | `Not in the spec.` and the code that motivates it |

`docs/product/README.md` maps a `§` number or PRD section to its file; open it and
read the section before writing, so Why and State are about what the spec asks
for, not a guess. Check the id exists (`git grep -n 'FR-L1.9' docs/product`).

## 2. Write the body

Five parts, each on its own line, one or two sentences each:

```
Why: <what is wrong or missing, and for whom>.

Design: spec §<n> (<part>); FR-<id>.

State: <what the code does today, naming the file or test that shows it>.

Approach: <the change in one sentence, with the main design choice if made>.

Done when: <the observable condition a closer checks>.
```

Check the code for State; never state from memory. Name code by repository path
(`internal/worker/maintain.go`), which a reader can search; don't link docs pages
or pin commits. For a `decision`, Done when is the question to answer and who
answers it. For a `debt`, add **Trigger:** what should make someone pay it. For an
`idea`, add **Worth it when:**. Work found while on another issue ends with
`Found in #N.` Keep the body under about 500 characters; longer reasoning belongs
in the spec, named on the Design line, not copied into the issue.

Title: an imperative or a noun phrase naming the behavior, under 80 characters,
prefixed with the requirement id or change row when it has one
(`FR-A9: approval bound to the base SHA`, `C6: per-review disk as an ephemeral volume`).

## 3. Labels

- **Type, one of:** `task` (engineering work with no user-visible behavior), `feature`
  (behavior users or operators see), `bug` (behavior that contradicts the product
  spec), `debt` (knowingly left undone or done poorly), `decision` (a question only
  the owner can answer), `security` (a security-review finding), `epic` (a parent
  with sub-issues), `idea` (not adopted).
- **Phase:** one `phase:0`…`phase:6` naming its PRD §14.1 phase
  (`docs/product/prd/14-rollout-plan.md#141-phased-deliverables`); an epic carries
  its children's phases, so add the child's phase to the epic if new.
- **Order:** `order:H1`…`order:H5` for work in the spec §16 order of work.
- **Other:** `blocked` when it waits on something outside the repository (a real org, a
  provider key, a sign-off), with the body saying on what; `testing` for test-technique work; `lane:direct|light|full` when triaged.
- Priority labels (`priority:1`…`priority:4`) came with the migrated issues; set one only when the
  work is clearly not the default.

## 4. Create, then link

```bash
gh issue create -R koblas/exemplar -t "<title>" -F body.md -l <type>,phase:<n>[,order:H<n>]
```

Then link it (`n` is the new issue's number, `id` from `gh api repos/koblas/exemplar/issues/<n> --jq .id`):

```bash
# under its epic
gh api -X POST repos/koblas/exemplar/issues/<epic>/sub_issues -F sub_issue_id=<id>
# blocked by another issue
gh api -X POST repos/koblas/exemplar/issues/<n>/dependencies/blocked_by -F issue_id=<blocking issue's id>
```

Pick the epic by reading the epics' bodies (`gh issue list -l epic`), by spec section,
not by name. For an existing issue, `gh issue edit <n> --body-file`, `--add-label` and
the same API calls fix what is missing; keep what is there and correct. Reshaping
an older body also means replacing path or anchor links with ids, dropping links
pinned to old commits, and cutting copied briefs down to a Why line.

Then `gh issue view <n>` and check: it reads on its own (Why says what and for whom),
the Design ids exist, the State line matches the code, there is a Done when, the type and phase labels are set,
and it sits under an epic. Report the number, the title, and what blocks it or what it
was found in.
