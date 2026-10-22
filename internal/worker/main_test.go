package worker

import (
	"os"
	"testing"

	"github.com/koblas/exemplar/internal/store/storetest"
	"github.com/koblas/exemplar/internal/synth"
)

func TestMain(m *testing.M) {
	os.Exit(synth.RunCached(func() int { return storetest.Run(m) }))
}
