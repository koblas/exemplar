package synth

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCachedBuildMatchesFresh(t *testing.T) {
	base := map[string]string{"a.go": "package a\n", "gone.txt": "x\n"}
	head := map[string]string{"a.go": "package a\n\nfunc A() {}\n", "dir/b.go": "package dir\n"}
	deleted := []string{"gone.txt"}

	fresh, err := Build(t.Context(), filepath.Join(t.TempDir(), "fresh"), base, head, deleted)
	if err != nil {
		t.Fatal(err)
	}

	UseCache(t.TempDir())
	t.Cleanup(func() { UseCache("") })
	copies := make([]*Repo, 0, 2)
	for i := range 2 {
		r, err := Build(t.Context(), filepath.Join(t.TempDir(), "copy"), base, head, deleted)
		if err != nil {
			t.Fatalf("cached build %d: %v", i, err)
		}
		copies = append(copies, r)
	}
	for i, r := range copies {
		if r.BaseSHA != fresh.BaseSHA || r.HeadSHA != fresh.HeadSHA {
			t.Errorf("copy %d SHAs = %s %s, fresh = %s %s", i, r.BaseSHA, r.HeadSHA, fresh.BaseSHA, fresh.HeadSHA)
		}
		// The copy is a working repository on the head branch.
		out, err := exec.CommandContext(t.Context(), "git", "-C", r.Dir, "log", "--format=%H", "pr").Output()
		if err != nil || !strings.HasPrefix(string(out), fresh.HeadSHA) {
			t.Errorf("copy %d log = %q, %v", i, out, err)
		}
		if b, err := os.ReadFile(filepath.Join(r.Dir, "dir/b.go")); err != nil || string(b) != head["dir/b.go"] {
			t.Errorf("copy %d dir/b.go = %q, %v", i, b, err)
		}
	}
	if copies[0].Dir == copies[1].Dir {
		t.Error("copies share a directory")
	}

	// A different fixture is built, not served from the cache.
	other, err := Build(t.Context(), filepath.Join(t.TempDir(), "other"), base, map[string]string{"a.go": "package a\n// other\n"}, nil)
	if err != nil || other.HeadSHA == fresh.HeadSHA {
		t.Errorf("other fixture = %+v, %v", other, err)
	}

	// A non-empty target is built in place, as without the cache.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "extra.txt"), []byte("extra\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inPlace, err := Build(t.Context(), dir, base, head, deleted)
	if err != nil || inPlace.BaseSHA == fresh.BaseSHA {
		t.Errorf("non-empty dir = %+v, %v; want a build that commits extra.txt", inPlace, err)
	}
}

func TestBuildRejectsEscapingPaths(t *testing.T) {
	for _, p := range []string{"../x", "/abs", ".git/config", ""} {
		if _, err := Build(t.Context(), t.TempDir(), map[string]string{p: "x"}, nil, nil); err == nil {
			t.Errorf("path %q accepted", p)
		}
	}
}
