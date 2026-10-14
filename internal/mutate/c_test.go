package mutate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const cSample = `#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>

static pthread_mutex_t mu = PTHREAD_MUTEX_INITIALIZER;
static int total;

void add(int v) {
    pthread_mutex_lock(&mu);
    total += v;
    pthread_mutex_unlock(&mu);
}

int count(const char *path) {
    FILE *f = fopen(path, "r");
    if (f == NULL) {
        return -1;
    }
    int n = 0;
    while (fgetc(f) != EOF) {
        n++;
    }
    fclose(f);
    return n;
}

int sum(const int *xs, int n) {
    int s = 0;
    for (int i = 0; i < n; i++) {
        s += xs[i];
    }
    return s;
}

int load(void) {
    int rc = init();
    if (rc != 0) {
        return rc;
    }
    return run();
}
`

const cppSample = `#include <mutex>

class Counter {
 public:
  void Add(int v) {
    std::lock_guard<std::mutex> guard(mu_);
    total_ += v;
  }
  void Inc() {
    mu_.lock();
    total_++;
    mu_.unlock();
  }
 private:
  std::mutex mu_;
  int total_ = 0;
};
`

type cCase struct {
	op, class string
	expect    [2]int
	gone, now string
}

func checkC(t *testing.T, path, src string, cases []cCase) {
	t.Helper()
	ms, err := C(t.Context(), path, []byte(src), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != len(cases) {
		t.Errorf("%s: mutations = %d: %+v", path, len(ms), ms)
	}
	got := map[string]Mutation{}
	for _, m := range ms {
		got[m.Op] = m
	}
	lines := strings.Split(strings.TrimSuffix(src, "\n"), "\n")
	for _, tt := range cases {
		m, ok := got[tt.op]
		if !ok || m.Class != tt.class || m.Expect != tt.expect || m.Path != path {
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
		dir := t.TempDir()
		file := filepath.Join(dir, path)
		_ = os.MkdirAll(filepath.Dir(file), 0o755)
		_ = os.WriteFile(file, []byte(src), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "p.diff"), []byte(m.Diff(lines)), 0o644)
		cmd := exec.CommandContext(t.Context(), "git", "apply", "p.diff")
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s: git apply: %v\n%s", tt.op, err, out)
			continue
		}
		if data, _ := os.ReadFile(file); string(data) != strings.Join(after, "\n")+"\n" {
			t.Errorf("%s: applied patch differs from Apply", tt.op)
		}
	}
}

func TestC(t *testing.T) {
	checkC(t, "src/count.c", cSample, []cCase{
		{"drop-lock", Concurrency, [2]int{9, 9}, "pthread_mutex_lock(&mu);", "total += v;"},
		{"drop-null-guard", NilDeref, [2]int{16, 16}, "if (f == NULL) {", "int n = 0;"},
		{"drop-release", ResourceLeak, [2]int{15, 15}, "fclose(f);", `FILE *f = fopen(path, "r");`},
		{"lt-to-le", OffByOne, [2]int{29, 29}, "", "for (int i = 0; i <= n; i++) {"},
		{"drop-err-check", ErrorHandling, [2]int{36, 36}, "if (rc != 0) {", "int rc = init();"},
	})
}

func TestCPP(t *testing.T) {
	checkC(t, "src/counter.cc", cppSample, []cCase{
		{"drop-lock-guard", Concurrency, [2]int{6, 6}, "std::lock_guard", "total_ += v;"},
		{"drop-lock", Concurrency, [2]int{10, 10}, "mu_.lock();", "total_++;"},
	})
	if _, err := C(t.Context(), "x.c", []byte("int f( {"), 5); err == nil {
		t.Error("unparseable source accepted")
	}
	if _, err := C(t.Context(), "x.go", []byte("package x\n"), 5); err == nil {
		t.Error("a Go file accepted")
	}
}
