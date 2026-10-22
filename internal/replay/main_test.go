package replay

import (
	"os"
	"testing"

	"github.com/koblas/exemplar/internal/store/storetest"
	"github.com/koblas/exemplar/internal/synth"
)

// TestMain runs the package's Postgres test container.
func TestMain(m *testing.M) {
	os.Exit(synth.RunCached(func() int { return storetest.Run(m) }))
}
