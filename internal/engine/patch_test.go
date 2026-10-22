package engine

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/koblas/exemplar/internal/synth"
)

func TestApplyPatch(t *testing.T) {
	t.Parallel()
	repo, err := synth.Build(t.Context(), filepath.Join(t.TempDir(), "src"), map[string]string{"a.go": "package a\n\nfunc A() {}\n"}, map[string]string{"a.go": "package a\n\nfunc A() {}\n"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	checkout := func() *Workspace {
		t.Helper()
		ws, err := Checkout(t.Context(), filepath.Join(t.TempDir(), "ws"), CheckoutOptions{CloneURL: repo.Dir, BaseSHA: repo.BaseSHA, HeadSHA: repo.BaseSHA})
		if err != nil {
			t.Fatal(err)
		}
		return ws
	}
	patch := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,3 +1,5 @@\n package a\n \n func A() {}\n+\n+func B() {}\n" +
		"diff --git a/b.go b/b.go\nnew file mode 100644\n--- /dev/null\n+++ b/b.go\n@@ -0,0 +1 @@\n+package a\n"
	ws := checkout()
	if err := ws.ApplyPatch(t.Context(), []byte(patch)); err != nil {
		t.Fatal(err)
	}
	if ws.HeadSHA == repo.BaseSHA || len(ws.HeadSHA) != 40 {
		t.Fatalf("head = %s", ws.HeadSHA)
	}
	d, err := ws.Diff(t.Context())
	if err != nil || len(d.Files()) != 2 {
		t.Fatalf("diff = %+v, %v", d, err)
	}
	if b, err := ws.Show(t.Context(), ws.HeadSHA, "a.go"); err != nil || !strings.Contains(string(b), "func B()") {
		t.Errorf("a.go = %q, %v", b, err)
	}

	for _, c := range []struct{ name, patch string }{
		{"does not apply", "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,1 +1,1 @@\n-package nope\n+package a\n"},
		{"outside the tree", "diff --git a/../x b/../x\nnew file mode 100644\n--- /dev/null\n+++ b/../x\n@@ -0,0 +1 @@\n+x\n"},
		{"absolute path", "diff --git a//etc/x b//etc/x\nnew file mode 100644\n--- /dev/null\n+++ b//etc/x\n@@ -0,0 +1 @@\n+x\n"},
		{"empty", ""},
	} {
		ws := checkout()
		if err := ws.ApplyPatch(t.Context(), []byte(c.patch)); err == nil {
			t.Errorf("%s: applied", c.name)
		} else if ws.HeadSHA != repo.BaseSHA {
			t.Errorf("%s: head moved", c.name)
		}
	}
	if err := checkout().ApplyPatch(t.Context(), make([]byte, MaxPatch+1)); err == nil {
		t.Error("oversized patch applied")
	}
}
