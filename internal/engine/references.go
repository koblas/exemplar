package engine

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/koblas/exemplar/internal/search/api"
	"github.com/koblas/exemplar/internal/search/index"
)

// References answers the find_references tool from exemplar-search
// *api.Client implements it.
type References interface {
	SnapshotStatus(ctx context.Context, prRepo, repo string) (api.SnapshotResponse, error)
	FindReferences(ctx context.Context, prRepo, repo, snapshot, name, definedIn string) (api.FindReferencesResponse, error)
}

// WithReferences offers agents find_references, answered by r.
func (e *Engine) WithReferences(r References) *Engine {
	e.refs = r
	return e
}

// searchPin is the exemplar-search snapshot a review is pinned to:
// every find_references call passes it.
type searchPin struct {
	snapshot string
	// degraded is set when search for the repo was in degraded mode when
	// the review was pinned.
	degraded bool
	// err is set when exemplar-search could not be reached; the tool is
	// then not offered.
	err error
}

// pinSearch pins p's review to the repo's current search snapshot.
func (e *Engine) pinSearch(ctx context.Context, p *Prepared) {
	if e.refs == nil {
		p.search = nil
		return
	}
	st, err := e.refs.SnapshotStatus(ctx, p.PR.Repo, p.PR.Repo)
	p.search = &searchPin{snapshot: st.SnapshotSHA, degraded: st.Degraded, err: err}
}

// searchUsable reports whether find_references can be offered, and why not.
func (p *Prepared) searchUsable() (bool, string) {
	switch {
	case p.search == nil:
		return false, "exemplar-search is not configured"
	case p.search.err != nil:
		return false, "exemplar-search is unavailable: " + p.search.err.Error()
	}
	return true, ""
}

// toolbox returns an agent's tools for p.
func (e *Engine) toolbox(p *Prepared) *toolbox {
	tb := newToolbox(p, e.scanner, e.history)
	if ok, _ := p.searchUsable(); ok {
		tb.refs, tb.pin = e.refs, p.search
		tb.overlay, tb.touched = p.Files, p.Touched()
	}
	return tb
}

var findReferencesDef = toolDef{"find_references",
	"Find where a function, method, type or variable is referenced: the package that defines it and the files that import that package, ranked by closeness to the definition, at most 50 sites with two lines of context. Pass defined_in, the file that defines the name; it is required for common names.",
	map[string]any{
		"name":       map[string]any{"type": "string", "description": "the identifier, optionally qualified (pkg.Func, Type.Method)"},
		"defined_in": map[string]any{"type": "string", "description": "path of the file that defines it"},
	}, []string{"name"}}

// findReferences answers the tool: exemplar-search at the pinned
// snapshot, with the PR overlay: index hits in files the PR
// touches are replaced by matches in the PR's own versions of them.
func (t *toolbox) findReferences(ctx context.Context, name, definedIn string) (string, error) {
	res, err := t.refs.FindReferences(ctx, t.repo, t.repo, t.pin.snapshot, name, definedIn)
	if err != nil {
		t.markDegraded()
		return "", fmt.Errorf("find_references failed: %w", err)
	}
	if res.Degraded {
		t.markDegraded()
	}
	base := name
	if i := strings.LastIndexAny(name, ".:#/"); i >= 0 {
		base = name[i+1:]
	}
	refs := slices.DeleteFunc(res.References, func(r index.Reference) bool { return slices.Contains(t.touched, r.Path) })
	refs = append(t.overlayRefs(base), refs...)
	truncated := res.Truncated
	if len(refs) > index.MaxReferences {
		refs, truncated = refs[:index.MaxReferences], true
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d references to %s", len(refs), base)
	if truncated {
		b.WriteString(" (more exist; narrow with defined_in)")
	}
	if res.Degraded {
		b.WriteString(" (degraded search: results may be incomplete)")
	}
	b.WriteString("\n")
	for _, r := range refs {
		fmt.Fprintf(&b, "\n%s:%d\n", r.Path, r.Line)
		for i, l := range strings.Split(strings.TrimRight(r.Context, "\n"), "\n") {
			fmt.Fprintf(&b, "%d\t%s\n", r.ContextStart+i, l)
		}
	}
	return b.String(), nil
}

// overlayRefs finds base, as a whole word, in the PR's reviewable files at
// the head, where the index (the default branch) is stale.
func (t *toolbox) overlayRefs(base string) []index.Reference {
	word, err := regexp.Compile(`\b` + regexp.QuoteMeta(base) + `\b`)
	if err != nil {
		return nil
	}
	var out []index.Reference
	for _, f := range t.overlay {
		full, err := t.resolve(f)
		if err != nil {
			continue
		}
		data, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		lines := strings.Split(string(data), "\n")
		for i, l := range lines {
			if !word.MatchString(l) {
				continue
			}
			lo, hi := max(0, i-index.RefContext), min(len(lines), i+index.RefContext+1)
			out = append(out, index.Reference{Path: f, Line: i + 1, Text: l, Context: strings.Join(lines[lo:hi], "\n"), ContextStart: lo + 1})
		}
	}
	return out
}

func (t *toolbox) markDegraded() {
	t.mu.Lock()
	t.degraded = true
	t.mu.Unlock()
}

// contextDegraded reports whether any repo-context answer was degraded or
// failed.
func (t *toolbox) contextDegraded() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.degraded
}
