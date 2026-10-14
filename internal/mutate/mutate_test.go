package mutate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const sample = `package store

import (
	"os"
	"sync"
)

type cache struct {
	mu    sync.Mutex
	items map[string]int
}

func (c *cache) put(k string, v int) {
	c.mu.Lock()
	c.items[k] = v
	c.mu.Unlock()
}

func read(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, 10)
	_, err = f.Read(buf)
	return buf, err
}

func sum(xs []int, p *int) int {
	if p == nil {
		return 0
	}
	t := *p
	for i := 0; i < len(xs); i++ {
		t += xs[i]
	}
	return t
}
`

func TestGo(t *testing.T) {
	ms, err := Go("store/cache.go", []byte(sample), 5)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Mutation{}
	for _, m := range ms {
		got[m.Op] = m
	}
	lines := strings.Split(strings.TrimSuffix(sample, "\n"), "\n")
	tests := []struct {
		op, class string
		expect    [2]int // in the mutated file
		gone      string // a line no longer there (empty: a changed line)
		now       string // the expected range's first line, after
	}{
		{"drop-lock", Concurrency, [2]int{14, 14}, "c.mu.Lock()", "c.items[k] = v"},
		{"drop-err-check", ErrorHandling, [2]int{20, 20}, "return nil, err", "f, err := os.Open(path)"},
		{"drop-defer-close", ResourceLeak, [2]int{20, 20}, "defer f.Close()", "f, err := os.Open(path)"},
		{"drop-nil-guard", NilDeref, [2]int{31, 31}, "if p == nil {", "t := *p"},
		{"lt-to-le", OffByOne, [2]int{35, 35}, "", "for i := 0; i <= len(xs); i++ {"},
	}
	if len(ms) != len(tests) {
		t.Errorf("mutations = %d: %+v", len(ms), ms)
	}
	for _, tt := range tests {
		m, ok := got[tt.op]
		if !ok || m.Class != tt.class || m.Expect != tt.expect || m.Path != "store/cache.go" {
			t.Errorf("%s = %+v", tt.op, m)
			continue
		}
		after := m.Apply(lines)
		if tt.gone != "" && strings.Contains(strings.Join(after, "\n"), tt.gone) {
			t.Errorf("%s: %q still there", tt.op, tt.gone)
		}
		if strings.TrimSpace(after[m.Expect[0]-1]) != tt.now {
			t.Errorf("%s: expected line is %q, want %q", tt.op, after[m.Expect[0]-1], tt.now)
		}
		// The patch applies to the original and gives the mutated file.
		dir := t.TempDir()
		file := filepath.Join(dir, "store", "cache.go")
		_ = os.MkdirAll(filepath.Dir(file), 0o755)
		_ = os.WriteFile(file, []byte(sample), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "p.diff"), []byte(m.Diff(lines)), 0o644)
		cmd := exec.CommandContext(t.Context(), "git", "apply", "p.diff")
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s: git apply: %v\n%s\n%s", tt.op, err, out, m.Diff(lines))
			continue
		}
		data, _ := os.ReadFile(file)
		if string(data) != strings.Join(after, "\n")+"\n" {
			t.Errorf("%s: applied patch differs from Apply:\n%s", tt.op, data)
		}
	}
	// One per operator when asked.
	if one, _ := Go("store/cache.go", []byte(sample), 1); len(one) != len(tests) {
		t.Errorf("perOp 1 = %d", len(one))
	}
	if _, err := Go("x.go", []byte("package x\nfunc {"), 5); err == nil {
		t.Error("unparseable source accepted")
	}
}
