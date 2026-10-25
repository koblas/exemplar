package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/koblas/exemplar/internal/search/index"
)

// find_references without exemplar-search: the review's own checkout
// (the PR head) searched with ripgrep for the name as a whole word, sites
// told apart with universal-ctags (a definition or a use), ranked by
// closeness to defined_in and capped like the remote answer. It covers
// this repository only, and says so.

// maxCtagsFiles bounds the files handed to ctags in one answer.
const maxCtagsFiles = 50

// localSite is one reference and whether ctags names it a definition.
type localSite struct {
	index.Reference

	def bool
}

func (t *toolbox) localReferences(ctx context.Context, name, definedIn string) (string, error) {
	base := name
	if i := strings.LastIndexAny(name, ".:#/"); i >= 0 {
		base = name[i+1:]
	}
	if base == "" || len(base) > maxGrepPattern {
		return "", fmt.Errorf("%w 1-%d characters", errPatternLength, maxGrepPattern)
	}
	sites, err := t.wordSites(ctx, base)
	if err != nil {
		t.markDegraded()
		return "", err
	}
	defs := t.definitions(ctx, base, sites)
	for i := range sites {
		sites[i].def = defs[sites[i].Path+":"+strconv.Itoa(sites[i].Line)]
	}
	slices.SortStableFunc(sites, func(a, b localSite) int {
		if a.def != b.def {
			if a.def {
				return -1
			}
			return 1
		}
		if pa, pb := proximity(a.Path, definedIn), proximity(b.Path, definedIn); pa != pb {
			return pb - pa
		}
		if c := strings.Compare(a.Path, b.Path); c != 0 {
			return c
		}
		return a.Line - b.Line
	})
	truncated := false
	if len(sites) > index.MaxReferences {
		sites, truncated = sites[:index.MaxReferences], true
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d references to %s (this repository only; other repositories were not searched)", len(sites), base)
	if truncated {
		b.WriteString(" (more exist; narrow with defined_in)")
	}
	b.WriteString("\n")
	for _, s := range sites {
		kind := "use"
		if s.def {
			kind = "definition"
		}
		fmt.Fprintf(&b, "\n%s:%d (%s)\n", s.Path, s.Line, kind)
		for i, l := range strings.Split(strings.TrimRight(s.Context, "\n"), "\n") {
			fmt.Fprintf(&b, "%d\t%s\n", s.ContextStart+i, l)
		}
	}
	return b.String(), nil
}

// wordSites finds base as a whole word, literally, in the checkout.
func (t *toolbox) wordSites(ctx context.Context, base string) ([]localSite, error) {
	ctx, cancel := context.WithTimeout(ctx, grepTimeout)
	defer cancel()
	args := []string{
		"--no-follow", "--no-config", "--line-number", "--no-heading", "--with-filename", "--color=never",
		"--max-columns=300", "--word-regexp", "--fixed-strings", "-e", base, "--", ".",
	}
	cmd := exec.CommandContext(ctx, "rg", args...)
	cmd.Dir = t.ws.Dir
	out, err := cmd.Output()
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return nil, nil
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, errSearchTimedOut
		}
		return nil, errSearchFailed
	}
	files := map[string][]string{}
	var sites []localSite
	for line := range strings.Lines(string(out)) {
		p, rest, ok := strings.Cut(strings.TrimSuffix(line, "\n"), ":")
		if !ok {
			continue
		}
		num, _, ok := strings.Cut(rest, ":")
		n, err := strconv.Atoi(num)
		if !ok || err != nil {
			continue
		}
		p = strings.TrimPrefix(p, "./")
		if t.excluded(p, false) {
			continue
		}
		lines, seen := files[p]
		if !seen {
			full, err := t.resolve(p)
			if err != nil {
				continue
			}
			data, err := os.ReadFile(full)
			if err != nil {
				continue
			}
			lines = strings.Split(string(data), "\n")
			files[p] = lines
		}
		if n < 1 || n > len(lines) {
			continue
		}
		lo, hi := max(0, n-1-index.RefContext), min(len(lines), n+index.RefContext)
		sites = append(sites, localSite{
			Path: p, Line: n, Text: lines[n-1], Context: strings.Join(lines[lo:hi], "\n"), ContextStart: lo + 1,
		})
	}
	return sites, nil
}

// definitions are the "path:line" sites ctags tags as base, over the
// files with hits. Without ctags every site is a use.
func (t *toolbox) definitions(ctx context.Context, base string, sites []localSite) map[string]bool {
	out := map[string]bool{}
	bin, err := exec.LookPath("ctags")
	if err != nil {
		return out
	}
	// "./" keeps every path from reading as an option: ctags takes no "--".
	var files []string
	for _, s := range sites {
		if f := "./" + s.Path; !slices.Contains(files, f) {
			files = append(files, f)
		}
		if len(files) == maxCtagsFiles {
			break
		}
	}
	if len(files) == 0 {
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, grepTimeout)
	defer cancel()
	args := append([]string{"--output-format=json", "--fields=+n", "-f", "-"}, files...)
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = t.ws.Dir
	raw, err := cmd.Output()
	if err != nil {
		return out
	}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var tag struct {
			Type string `json:"_type"`
			Name string `json:"name"`
			Path string `json:"path"`
			Line int    `json:"line"`
		}
		if json.Unmarshal(sc.Bytes(), &tag) != nil || tag.Type != "tag" || tag.Name != base {
			continue
		}
		out[strings.TrimPrefix(tag.Path, "./")+":"+strconv.Itoa(tag.Line)] = true
	}
	return out
}

// proximity is how many leading directories p shares with definedIn.
func proximity(p, definedIn string) int {
	if definedIn == "" {
		return 0
	}
	a := strings.Split(path.Dir(p), "/")
	b := strings.Split(path.Dir(strings.TrimPrefix(definedIn, "./")), "/")
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	if p == definedIn {
		n++
	}
	return n
}
