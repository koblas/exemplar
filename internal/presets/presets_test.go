package presets

import (
	"bytes"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

// TestRecommendedMatchesStarter keeps exemplar:recommended in step with the
// starter rules in testdata/exemplar-config, which differ only in the id
// prefix until the starter extends the preset.
func TestRecommendedMatchesStarter(t *testing.T) {
	starter, err := filepath.Glob("../../testdata/exemplar-config/rules/*/*.yaml")
	if err != nil || len(starter) == 0 {
		t.Fatalf("no starter rules: %v", err)
	}
	embedded, err := fs.Glob(FS, "recommended/*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(embedded) != len(starter) {
		t.Fatalf("recommended has %d rules, the starter %d", len(embedded), len(starter))
	}
	for _, f := range starter {
		id := strings.TrimSuffix(filepath.Base(f), ".yaml")
		want, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		got, err := fs.ReadFile(FS, path.Join("recommended", Prefix+id+".yaml"))
		if err != nil {
			t.Errorf("%s: no preset copy: %v", id, err)
			continue
		}
		want = bytes.Replace(want, []byte("id: "+id+"\n"), []byte("id: "+Prefix+id+"\n"), 1)
		if !bytes.Equal(got, want) {
			t.Errorf("recommended/%s%s.yaml differs from %s beyond the id prefix", Prefix, id, f)
		}
	}
}
