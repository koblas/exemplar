---
paths:
  - "internal/setup/**"
  - "internal/web/**"
---

# UX flow guidelines

Applies to every page an operator or user clicks through: the first-run
`/setup` pages (`internal/setup`) and the `/ui/` web UI (`internal/web`).
The user documentation quotes the labels these pages show, so a renamed
button or step is a docs change too (`/docs`).

## Principles

- Minimize time-to-value: count the steps from "user has intent" to "user
  has result". Every step must justify itself.
- Prefer teaching an existing concept or component to do more over adding a
  new one. Every new noun the user must learn is a tax.
- Names (labels, routes, fields) are permanent UX. Pick ones that read
  correctly in a sentence without docs.
- Use the design system's default components and styling over bespoke ones.
- Decide at design time whether a view updates live when its data changes,
  and write the decision down next to the view.

## Every screen has all its states

- Loading, empty, error, and success are each designed and visible. Never
  let empty look like broken.
- No spinner without a timeout. No dead ends: every state offers a next
  action.
- Don't pass "maybe loading" as a boolean (true/false/undefined). Use an
  explicit status ("loading" | "ready" | "error"), or resolve loading and
  error in the parent and pass only settled values.

## Errors tell the user what to do

- An error names what failed, why, and the concrete next action.
- The UI should never have to guess what happened from a status code; the
  error carries enough to act on.

## Forms come from the user's decision, not the API shape

- An optional or repeated API field doesn't automatically become an
  optional or multi-select control.
- Every control has a deliberately chosen default: what most users intend.
  For permissions or sharing, that's the least privilege that still
  accomplishes the flow's purpose.
- A choice that controls what someone else can do is required, picked from
  a short list of named options.
- Don't offer a value just because the server accepts it.
- A control with one option is not a choice. Remove it or fix the options.
- If you're writing helper text to warn about a bad outcome ("they'll have
  no access"), change the default instead.
- Client-side validation matches the server exactly (same limits, same
  counting, same trimming), or the user gets the wrong message.

## Don't offer what the user can't do

- Hide actions the user isn't permitted to perform. Disable them with a
  tooltip explaining why only when the user would otherwise go looking for
  them.
- A button that always answers "permission denied" is a dead end.

## Modals and steps start fresh

- Each open of a modal or wizard step starts clean. Typed input, errors,
  and "dismissed" flags don't survive Cancel, Escape, or close.
- State that must outlive the modal lives in the parent and is cleared once
  consumed.

## Late results are dropped

- If the user cancels, closes, or switches context, a still-pending
  request's result is dropped: it must not update state or move focus. A
  stale request must never leave the next action stuck as "pending".

## Copy

- Write all user-facing strings for a flow up front (labels, empty,
  loading, error and success text, confirmations) and keep them in one
  module per flow (`internal/setup/copy.go` for `/setup`). A sentence used
  twice is one constant.
- Also define interaction details up front: where focus goes after a
  destructive action, how modals dismiss, accessible names for inline edit
  fields.

## Verify

- Test every error state, loading state, and empty state, and assert the
  actual content the user sees, not just that nothing crashed.
- After each change to data, confirm the screen showing that data reflects
  it.
