package scope

import (
	"errors"
	"strings"
	"testing"
)

// sampleDiff covers a modified file with two hunks, an added file, a removed
// file, a rename with edits, and a binary file.
const sampleDiff = `diff --git a/src/net/fetch.go b/src/net/fetch.go
index 1111111..2222222 100644
--- a/src/net/fetch.go
+++ b/src/net/fetch.go
@@ -10,6 +10,7 @@ func Fetch() {
 	a := 1
 	b := 2
 	c := 3
+	d := 4
 	e := 5
 	f := 6
 	g := 7
@@ -40,4 +41,3 @@ func Close() {
 	x := 1
-	y := 2
 	z := 3
 	return
diff --git a/docs/new.md b/docs/new.md
new file mode 100644
index 0000000..3333333
--- /dev/null
+++ b/docs/new.md
@@ -0,0 +1,3 @@
+# Title
+
+Body
diff --git a/old/gone.go b/old/gone.go
deleted file mode 100644
index 4444444..0000000
--- a/old/gone.go
+++ /dev/null
@@ -1,2 +0,0 @@
-package old
-var x = 1
diff --git a/pkg/a.go b/pkg/b.go
similarity index 90%
rename from pkg/a.go
rename to pkg/b.go
index 5555555..6666666 100644
--- a/pkg/a.go
+++ b/pkg/b.go
@@ -1,3 +1,3 @@
 package pkg
-var A = 1
+var B = 1
 var C = 2
diff --git a/assets/logo.png b/assets/logo.png
index 7777777..8888888 100644
Binary files a/assets/logo.png and b/assets/logo.png differ
`

func mustParse(t testing.TB, s string) *Diff {
	t.Helper()
	d, err := Parse(strings.NewReader(s))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return d
}

func TestParse(t *testing.T) {
	d := mustParse(t, sampleDiff)

	wantPaths := []string{"src/net/fetch.go", "docs/new.md", "old/gone.go", "pkg/b.go", "assets/logo.png"}
	if got := d.Paths(); !equalStrings(got, wantPaths) {
		t.Fatalf("Paths() = %v, want %v", got, wantPaths)
	}
	if got := d.ChangedLines(); got != 9 { // binary logo.png counts 0
		t.Errorf("ChangedLines() = %d, want 9", got)
	}

	tests := []struct {
		path      string
		oldPath   string
		status    Status
		binary    bool
		wantHunks []Hunk // Text not compared
	}{
		{"src/net/fetch.go", "src/net/fetch.go", StatusModified, false, []Hunk{
			{OldStart: 10, OldLines: 6, NewStart: 10, NewLines: 7, Added: 1},
			{OldStart: 40, OldLines: 4, NewStart: 41, NewLines: 3, Deleted: 1},
		}},
		{"docs/new.md", "docs/new.md", StatusAdded, false, []Hunk{
			{OldStart: 0, OldLines: 0, NewStart: 1, NewLines: 3, Added: 3},
		}},
		{"old/gone.go", "old/gone.go", StatusRemoved, false, []Hunk{
			{OldStart: 1, OldLines: 2, NewStart: 0, NewLines: 0, Deleted: 2},
		}},
		{"pkg/b.go", "pkg/a.go", StatusRenamed, false, []Hunk{
			{OldStart: 1, OldLines: 3, NewStart: 1, NewLines: 3, Added: 1, Deleted: 1},
		}},
		{"assets/logo.png", "assets/logo.png", StatusModified, true, nil},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			f, ok := d.File(tt.path)
			if !ok {
				t.Fatalf("File(%q) not found", tt.path)
			}
			if f.OldPath != tt.oldPath || f.Status != tt.status || f.Binary != tt.binary {
				t.Errorf("got OldPath=%q Status=%q Binary=%v, want %q %q %v",
					f.OldPath, f.Status, f.Binary, tt.oldPath, tt.status, tt.binary)
			}
			if len(f.Hunks) != len(tt.wantHunks) {
				t.Fatalf("got %d hunks, want %d", len(f.Hunks), len(tt.wantHunks))
			}
			for i, want := range tt.wantHunks {
				got := f.Hunks[i]
				got.Text = ""
				if got != want {
					t.Errorf("hunk %d = %+v, want %+v", i, got, want)
				}
				if !strings.HasPrefix(f.Hunks[i].Text, "@@ ") {
					t.Errorf("hunk %d text does not start with a header: %q", i, f.Hunks[i].Text)
				}
			}
		})
	}

	if _, ok := d.File("pkg/a.go"); ok {
		t.Error("renamed file is indexed under its old path")
	}
}

func TestParseEmpty(t *testing.T) {
	d := mustParse(t, "")
	if len(d.Files()) != 0 {
		t.Fatalf("got %d files from empty diff", len(d.Files()))
	}
}

func TestParseDuplicatePath(t *testing.T) {
	one := "diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-a\n+b\n"
	_, err := Parse(strings.NewReader(one + one))
	if !errors.Is(err, ErrDuplicatePath) {
		t.Fatalf("Parse duplicate = %v, want ErrDuplicatePath", err)
	}
}

func TestParseMalformedHunks(t *testing.T) {
	const header = "diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n"
	tests := []struct {
		name    string
		hunks   string
		wantErr error // nil means any error
	}{
		{"line counts do not match header", "@@ -1 +1,3 @@\n-a\n+b\n", nil},
		{"lines numbered from zero", "@@ -0,1 +0,1 @@\n-a\n+b\n", ErrMalformedHunk},
		{"overlapping hunks", "@@ -1,3 +1,3 @@\n a\n-b\n+B\n c\n@@ -3,2 +3,2 @@\n c\n-d\n+D\n", ErrMalformedHunk},
		{"hunks out of order", "@@ -10 +10 @@\n-a\n+b\n@@ -1 +1 @@\n-a\n+b\n", ErrMalformedHunk},
		{"range overflows", "@@ -9223372036854775807,2 +1 @@\n-a\n-b\n+c\n", ErrMalformedHunk},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(header + tt.hunks))
			if err == nil {
				t.Fatal("Parse succeeded, want error")
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("Parse = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
