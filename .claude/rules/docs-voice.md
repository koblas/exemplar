---
paths:
  - "docs/index.md"
  - "docs/tutorials/**"
  - "docs/guides/**"
  - "docs/concepts/**"
  - "docs/reference/**"
---

# Documentation voice

Applies to every public page of the user documentation site. Which quadrant
a sentence belongs in, and the checks, are in the `/docs` skill and
`docs/guides/develop/docs.md`; this file is how the sentence reads once it
is there. Model pages: `docs/guides/operate/auto-approve.md`,
`docs/guides/operate/compile-cronjob.md`.

## Register follows the quadrant

- Tutorial: second person, present tense, one path. "You open the PR. The
  check run appears within a minute."
- How-to: imperative. Step headings are commands ("## 2. Unsuspend"); the
  sentence under a step says what to do, then what you see when it worked.
- Concept: third person, about the system. "The worker posts…", not "you
  will see…". No imperatives.
- Reference: "X does Y" in the code's own words. Never "you should" or "set
  X to".

## Sentences

- Short declaratives. Lead with the fact or the action; the condition
  follows ("Off unless both `global.yaml` and the repo opt in").
- Join a statement and its consequence with a colon, not "which means".
- Present tense for what the system does; no "will" for normal behavior.
  "you will" becomes "you have" or the plain verb.
- No filler or reassurance: "simply", "just", "easily", "please", "of
  course", "note that". No "we" or "our": the subject is Exemplar, a
  component, or the reader.
- No marketing adjectives ("powerful", "seamless", "robust"). Say what it
  does and let the reader judge.
- Numbers and limits are exact and come from the code (`4 GiB`, `120 s`),
  never "a few" or "large".
- **Bold** marks a hard condition ("**must dismiss stale approvals on
  push**") or a UI label, nothing else. Italics only for GitHub's own
  setting names (*Dismiss stale pull request approvals*).

## Words

- `docs/concepts/glossary.md` is the vocabulary. Use its term, spelled and
  capitalized as it is there; a new term gets a glossary entry in the same
  change. Don't invent a synonym for variety.
- US spelling: behavior, organization, categorize, license.
- "PR" and "repo" in running text, as the glossary and the web UI do.
  Spell out "pull request" only where GitHub's own UI or a setting name
  does.
- Outcomes are "approved with no human review needed" and "routed to
  humans". "Auto-approve" is the mechanism's name: config, how-tos and
  reference use it.
- Code identifiers (variables, fields, files, commands, check names, label
  values) are in backticks every time.

## UI labels

- Quote a label exactly as the page shows it, in bold: **Re-run**,
  **Check again**, **Wrong**. A renamed label must be findable by grep, so
  never paraphrase one.
- The verb is "click" for buttons and links, "pick" for a choice from a
  list, "open" for a page (`/ui/playground`).

## Admonitions

- Every admonition has a title that states the point ("Runners only", "Not
  a production database"), never a bare `!!! note`.
- `note`: a precondition or context the reader needs before the steps.
  `warning`: doing it wrong breaks something or loses data. `danger`: doing
  it wrong weakens a security boundary. No `tip` or `info`.
- At most one at the top of a page; anything longer is a section.

## Links

- Link text is the target page's title or what the reader gets there
  ("[How to set up Grafana](grafana.md)"), never "here" or "this page".
- A how-to links the reference for a field instead of describing it; the
  link text is the field or page name.
