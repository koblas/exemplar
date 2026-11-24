---
name: issue
description: "File or reshape a GitHub issue the way this repository tracks work: title, Design backlink, State, Approach, Done when, parent epic (sub-issue), type, phase and order labels, and blocked-by dependencies. Use when work is found during a task, when a spec section or PRD requirement needs an issue, when a debt is deferred, or when an existing issue lacks a backlink or a done condition."
argument-hint: "<what the issue is about, or an existing #number to reshape>"
---

# File a GitHub issue

File or reshape: $ARGUMENTS

GitHub Issues on `koblas/exemplar` is the only work tracker; `.claude/CLAUDE.md`
"Work Tracking" has the rules this skill applies. `docs/product/` stays the source
of truth for what and why: an issue says where the design lives, what is true in
the code today, how to get there, and when it is done. It never restates the spec.

## 1. Find the design

Every issue links the document that owns its design, as a path plus heading anchor:

| Work comes from | Design line |
|---|---|
| A spec section | `docs/product/spec/<file>.md#<anchor>` (and the sub-heading or bullet in parentheses, e.g. `(Chaos)`) |
| A PRD requirement | `FR-…` in the title; `docs/product/prd/<file>.md#<anchor>` and the spec section that implements it |
| An unadopted idea | Where it came from (a post, a review, a retro) |
| Something the spec does not cover | `Not in the spec.` and the code that motivates it |

`docs/product/README.md` maps a `§` number to its file. Find the anchor by reading
the heading (`rg -n '^#' docs/product/spec/<file>.md`); anchors are the heading
lower-cased with punctuation dropped and spaces as hyphens, so `### 16.1 Testing`
is `#161-testing`. Read the section before writing, so the State line is about
what the spec asks for, not a guess.

## 2. Write the body

Four parts, each on its own line, one or two sentences each:

```
Design: docs/product/spec/<file>.md#<anchor> (<part>).

State: <what the code does today, naming the file or test that shows it>.

Approach: <the change in one sentence, with the main design choice if made>.

Done when: <the observable condition a closer checks>.
```

Check the code for State; never state from memory. For a `decision`, Done when is
the question to answer and who answers it. For a `debt`, add **Trigger:** what
should make someone pay it. For an `idea`, add **Worth it when:**. Work found
while on another issue ends with `Found in #N.` Keep the body under about 500
characters; longer reasoning belongs in the spec, linked from the Design line.

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
- Priority labels (`priority:1`…`priority:4`) came from beads; set one only when the
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
the same API calls fix what is missing; keep what is there and correct.

Then `gh issue view <n>` and check: the Design link resolves (the heading exists), the
State line matches the code, there is a Done when, the type and phase labels are set,
and it sits under an epic. Report the number, the title, and what blocks it or what it
was found in.
