// Package buglane defines Lane 3, the bug lane (spec §6.7): the allowlist
// of bug classes and their prompts, and the risk gate that decides when the
// lane runs. The engine runs one agent per enabled class.
package buglane

import "slices"

// Class is one bug class: effectively a built-in rule with its own prompt
// and kill switch (global.yaml bug_lane.classes.<id>.enabled).
type Class struct {
	ID string
	// Version changes whenever the guidance changes, like a rule's SHA.
	Version string
	// LooksFor is the class's one-line scope, as in the spec.
	LooksFor string
	// Guidance is the class-specific part of the agent's task.
	Guidance string
	// NeedsReferences is set for classes that need find_references from
	// exemplar-search; they are skipped when it is unavailable.
	NeedsReferences bool
}

// Class ids.
const (
	NilDeref      = "bug.nil-deref"
	ErrorHandling = "bug.error-handling"
	Concurrency   = "bug.concurrency"
	ResourceLeak  = "bug.resource-leak"
	OffByOne      = "bug.off-by-one"
	Contract      = "bug.contract"
)

// Classes is the allowlist, in a fixed run order. global.yaml can only
// enable or disable these; it cannot add one.
var Classes = []Class{
	{
		ID: NilDeref, Version: "nil-deref-v1",
		LooksFor: "Dereference of values that can be nil/null/None on a reachable path",
		Guidance: "Look for a value that can be nil, null or None on some path the changed code can take " +
			"(a map miss, an error return whose other result is unset, an optional field, a failed lookup or type assertion) " +
			"and is then dereferenced, indexed, called or has a field read. Name the path that produces the nil.",
	},
	{
		ID: ErrorHandling, Version: "error-handling-v1",
		LooksFor: "Swallowed errors, wrong error propagated, missing cleanup on error paths",
		Guidance: "Look for errors that are dropped or ignored, an error path that returns a different or nil error than the one that happened, " +
			"success reported after a failure, and cleanup (unlock, rollback, close, release) skipped when an error returns early.",
	},
	{
		ID: Concurrency, Version: "concurrency-v1",
		LooksFor: "Data races, lock ordering, unguarded shared state, goroutine/thread leaks",
		Guidance: "Look for shared state read or written from more than one goroutine or thread without the lock that guards it elsewhere, " +
			"locks taken in inconsistent order, a lock held across a blocking call that another holder needs, and goroutines or threads that can block forever with no way to stop them.",
	},
	{
		ID: ResourceLeak, Version: "resource-leak-v1",
		LooksFor: "Unclosed handles, connections, files; missing defer/RAII",
		Guidance: "Look for files, network connections, response bodies, database rows or statements, timers and similar handles that are opened on some path and " +
			"not closed or released on every path out, including early returns and errors.",
	},
	{
		ID: OffByOne, Version: "off-by-one-v1",
		LooksFor: "Boundary errors in loops, slices, pagination",
		Guidance: "Look for loop bounds, slice or substring indexes, length checks, page offsets and range comparisons that are off by one: < where <= is needed or the reverse, " +
			"a last element skipped or read past the end, an empty input that indexes out of range, or a page that repeats or drops an item.",
	},
	{
		ID: Contract, Version: "contract-v1", NeedsReferences: true,
		LooksFor: "Changed function behavior that breaks callers found via find_references",
		Guidance: "For each function whose behavior the change alters (what it returns, when it errors, what it accepts, side effects), use find_references to read its callers, " +
			"and report a caller that the new behavior breaks. Anchor the finding on the changed function.",
	},
}

// ByID returns the class with id.
func ByID(id string) (Class, bool) {
	i := slices.IndexFunc(Classes, func(c Class) bool { return c.ID == id })
	if i < 0 {
		return Class{}, false
	}
	return Classes[i], true
}

// Enabled returns the allowlisted classes global.yaml enables, in run
// order. A class global.yaml does not mention is off.
func Enabled(flags map[string]bool) []Class {
	var out []Class
	for _, c := range Classes {
		if flags[c.ID] {
			out = append(out, c)
		}
	}
	return out
}
