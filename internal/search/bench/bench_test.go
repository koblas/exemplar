package bench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/koblas/exemplar/internal/synth"
)

func TestPercentile(t *testing.T) {
	v := []float64{5, 1, 4, 2, 3, 10, 9, 8, 7, 6}
	tests := []struct {
		p    float64
		want float64
	}{{50, 5}, {95, 10}, {10, 1}, {100, 10}}
	for _, tt := range tests {
		if got := Percentile(v, tt.p); got != tt.want {
			t.Errorf("Percentile(%v) = %v, want %v", tt.p, got, tt.want)
		}
	}
	if Percentile(nil, 50) != 0 {
		t.Error("empty input")
	}
}

func TestLanguageOfAndSample(t *testing.T) {
	if LanguageOf("a/b.TSX") != "TypeScript/JavaScript" || LanguageOf("x.kt") != "Java/Kotlin" || LanguageOf("README") != "" {
		t.Fatal("LanguageOf")
	}
	list := make([]string, 1000)
	for i := range list {
		list[i] = string(rune('a' + i%26))
	}
	if got := EvenSample(list, 10); len(got) != 10 {
		t.Fatalf("EvenSample len = %d", len(got))
	}
	if got := EvenSample(list[:3], 10); len(got) != 3 {
		t.Fatalf("EvenSample short list = %d", len(got))
	}
}

func TestTreeStatsAndSizing(t *testing.T) {
	repo, err := synth.Build(t.Context(), filepath.Join(t.TempDir(), "r"),
		map[string]string{"main.go": "package main\n", "web/app.ts": "export const x = 1\n"},
		map[string]string{"main.go": "package main\n\nfunc main() {}\n"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	files, bytes, err := treeStats(t.Context(), os.Environ(), repo.Dir, "pr")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || bytes == 0 {
		t.Fatalf("treeStats = %v, %d", files, bytes)
	}
	r := &Report{}
	r.Repository.Bytes = 1000
	r.Mirror.Bytes = 1000
	r.FullIndex.IndexBytes = 1000
	s := size(r)
	if s.DiskBytesPerReplica != 3750 || s.SourceMultiple != 3.75 {
		t.Fatalf("sizing = %+v", s)
	}
	if !strings.Contains(r.Markdown(), "Disk per replica") {
		t.Fatal("markdown missing sizing")
	}
}

// TestRunEndToEnd needs the real tools; it runs on the benchmark host.
func TestRunEndToEnd(t *testing.T) {
	if err := CheckTools(); err != nil {
		t.Skip(err)
	}
	t.Skip("run `exemplar-search bench` on the benchmark host instead; this needs a repository with history")
}
