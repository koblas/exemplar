---
name: bd-ticket
description: "File or reshape a beads issue the way this repository tracks work: title, spec or plan backlink, State, Approach, Done when, parent epic, phase and order labels, and dependencies (blocks, discovered-from, related). Use when work is found during a task, when a spec section or backlog brief needs an issue, or when an existing issue lacks a backlink or a done condition."
argument-hint: "<what the issue is about, or an existing id to reshape>"
---

# File a beads issue

File or reshape: $ARGUMENTS

Beads (`bd`, prefix `exemplar-`) is the only work tracker; `.claude/CLAUDE.md`
"Work Tracking" has the rules this skill applies. The spec stays the source of
truth for what and why: an issue says where the design lives, what is true in
the code today, how to get there, and when it is done. It never restates the
spec. Run `bd` from the checkout root (`bd context` names the database).

## 1. Find the design

Every issue backlinks the document that owns its design, as a path plus
heading anchor:

| Work comes from | Backlink |
|---|---|
| A spec section | `docs/spec/technical-spec.md#<anchor>` (and the sub-heading or bullet in parentheses, e.g. `(Chaos)`) |
| A PRD requirement | `FR-…` in the title; `docs/spec/prd.md#<anchor>` and the spec section that implements it |
| A backlog brief | `docs/plans/backlog.md#<anchor>, row "<row>"` plus the spec section |
| An open question in a plan | `docs/plans/ready-to-install.md#6-open-questions` (type `decision`) |
| Something the spec does not cover | Say `Not in the spec.` and name the code that motivates it |

Find the anchor by reading the heading (`rg -n '^#' <file>`); anchors are the
heading lower-cased with punctuation dropped and spaces as hyphens, so
`### 16.1 Testing` is `#161-testing`. Read the section before writing, so the
State line is about what the spec asks for, not a guess.

## 2. Write the description

Four parts, in this order, as short prose (one or two sentences each, no
headings or bullets inside the description; `bd` shows it as one block):

1. **Backlink** (always first): `Spec: docs/spec/technical-spec.md#… (…).`
   Brief and status links follow if any.
2. **State:** what the code does today, naming the files or tests that show
   it (`internal/worker/sim_test.go runs seeds sequentially`). Check the code;
   never state from memory.
3. **Approach:** the change in one sentence, with the main design choice if
   one is already made. Alternatives go in the issue, not the spec.
4. **Done when:** the observable condition a closer checks: a test that
   exists and passes, a command that works, a row that is written. For a
   `decision`, the question to answer and who answers it.

Keep the whole description under about 500 characters. Longer reasoning
belongs in the spec or a backlog brief, linked from the backlink.

Title: an imperative or a noun phrase naming the behavior, under 80
characters; prefix it with the requirement id or change row when it has one
(`FR-A9: approval bound to the base SHA`, `C6: per-review disk as an
ephemeral volume`, `Worker sim: base-drift property`).

## 3. Type, parent, labels, priority

- **Type:** `task` for engineering work with no user-visible behavior
  (tests, refactors, tooling), `feature` for behavior users or operators see,
  `bug` for a defect in built behavior, `decision` for a question that blocks
  work, `epic` for a group with children. An issue that grows children
  becomes an epic (`bd update <id> --type epic`).
- **Parent:** the epic whose spec area owns the work. The epics are in
  `bd list --type epic`; read their descriptions to pick by spec section,
  not by name. A sub-issue of a task-turned-epic uses `--parent` on it.
- **Labels:** every issue carries one `phase:0`…`phase:6` label naming its
  PRD §14.1 phase (`docs/spec/prd.md#141-phased-deliverables`); an epic
  carries the phases of its children, so add the child's phase to the epic
  if new. Work from the H1–H5 order in spec §16 carries `order:H1`…`order:H5`.
  Test-technique work carries `testing`. An unadopted idea is `deferred`
  with the `idea` label.
- **Priority:** 0 blocks a release or a running review; 1 is the next thing
  the current plan needs; 2 is the default; 3 is nice to have; 4 is an idea
  kept for reference. Leave 2 unless the work is clearly elsewhere; a flat
  field carries no signal.

## 4. Dependencies

- `discovered-from:<id>`: always, when the work came up while on another
  issue; it is how the history reads later.
- `blocks:<id>`: when this issue must land before another can start. Add it
  on the issue that blocks, and set the blocked side's status to `blocked`
  if nothing else about it can proceed.
- `related:<id>`: shared code or a split of one brief into two issues.
- Work that waits on something outside the repository (a real org, a
  provider key, Security sign-off) is `blocked`, and the description says on
  what, so `bd ready` does not list it as workable.

## 5. Create or update

```bash
bd create "<title>" -t <type> -p <priority> \
  -d "Spec: docs/spec/technical-spec.md#<anchor> (<part>). State: <what the code does>. Approach: <the change>. Done when: <observable condition>." \
  --parent <epic> -l phase:<n>[,order:H<n>][,testing] \
  --deps discovered-from:<id>[,blocks:<id>]
```

For an existing issue, `bd update <id> -d "…"`, `--type`, `--parent`,
`-l` and `bd dep add <id> <type>:<other>` change the parts that are missing;
keep what is there and correct.

Then `bd show <id>` and check: the backlink resolves (the heading exists),
the State line matches the code, there is a Done when, the phase label is
set, and the parent is an epic. Report the id, the one-line title, and what
it blocks or was discovered from.

Do not export or commit here: `bd export -o .beads/issues.jsonl` runs at
session end with the work it belongs to (`/pr-close` does it).
