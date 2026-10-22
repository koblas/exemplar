package playground

import (
	"os"
	"testing"

	"github.com/koblas/exemplar/internal/synth"
)

// Tests build the same fixture repositories many times; the cache copies
// them instead of running git for each.
func TestMain(m *testing.M) { os.Exit(synth.RunCached(m.Run)) }
