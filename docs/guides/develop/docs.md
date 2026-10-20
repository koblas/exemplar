# How to write documentation

This site is built by [Zensical](https://zensical.org), the successor to Material for MkDocs, from `mkdocs.yml` (Zensical reads the MkDocs format) and the pages in `docs/`, except `docs/spec`, `docs/plans` and `docs/security`, which stay in the repository. It follows the [Divio documentation system](https://docs.divio.com/documentation-system/): every page is a tutorial, a how-to guide, an explanation or reference, never a mix.

## 1. Pick the quadrant

| The reader wants to… | Quadrant | Where | Rules |
|---|---|---|---|
| Learn by doing, from nothing | Tutorial | `docs/tutorials/` | Numbered steps that always work; one path; explain nothing beyond what the step needs. |
| Get one task done | How-to | `docs/guides/<persona>/` | Title starts with "How to"; assumes the reader knows what Exemplar is; steps, not background; links to reference for fields. |
| Understand why or how | Concept | `docs/concepts/` | Prose and diagrams; no steps, no field tables. |
| Look something up | Reference | `docs/reference/` | Tables; every field, flag, variable and page; describes, never instructs. |

One audience per page. No references to the spec's section numbers, requirement ids, test ids or delivery phases: link the spec file on GitHub only where a reader truly needs the long form.

## 2. Write it

- Add the page to `nav:` in `mkdocs.yml` (a test fails otherwise). Use Material content tabs for the tunnel-vs-Ingress variants, admonitions for warnings.
- A behaviour change usually touches one how-to and one reference page; a new feature also gets a concept paragraph.
- Keep old paths working when you move a page: add a line to the `redirects` plugin's map in `mkdocs.yml`.
- Update the pages, not the README, when behaviour changes.

With Claude Code, `/docs <behavior change or page>` applies these rules and runs the checks; `/docs-audit` reports quadrant mixing, coverage gaps and internal references without editing.

## 3. Check it

```bash
make docs
```

builds strictly: a broken link or anchor fails. Then:

```bash
go test ./docs
```

fails when an `EXEMPLAR_*` variable in `cmd/` or `internal/`, a CLI subcommand, or a top-level property of a schema in `schema/` has no entry on its reference page, when a page is missing from the nav, or when a public page cites an internal reference.

```bash
make docs-serve
```

previews at <http://127.0.0.1:8000>. CI's `docs` job runs `make docs` and `go test ./docs` on every PR, and `docs.yml` publishes `main` to GitHub Pages.
