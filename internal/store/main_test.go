package store_test

import (
	"testing"

	"github.com/koblas/exemplar/internal/store/storetest"
)

// TestMain runs the package's Postgres test container.
func TestMain(m *testing.M) { storetest.Main(m) }
