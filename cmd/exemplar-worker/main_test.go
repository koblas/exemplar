package main

import (
	"errors"
	"testing"
)

// The runner quota needs the kubernetes launcher, and sets the reviews
// queue's size on each replica.
func TestRunnerQuotaFlag(t *testing.T) {
	t.Parallel()
	base := []string{"--config", t.TempDir(), "--work-dir", t.TempDir()}
	for name, args := range map[string][]string{
		"negative":   {"--launcher=kubernetes", "--runner-quota=-1"},
		"in-process": {"--runner-quota=4"},
	} {
		if err := runServe(append(base, args...)); !errors.Is(err, errBadQuota) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, tc := range []struct{ concurrency, quota, want int }{{4, 0, 4}, {4, 16, 16}, {8, 2, 2}} {
		if got := reviewWorkers(tc.concurrency, tc.quota); got != tc.want {
			t.Errorf("reviewWorkers(%d, %d) = %d, want %d", tc.concurrency, tc.quota, got, tc.want)
		}
	}
}

func TestDiskBounds(t *testing.T) {
	t.Parallel()
	if d, err := diskBounds("2Gi", "50Gi"); err != nil || d.Min != 2<<30 || d.Max != 50<<30 {
		t.Errorf("defaults = %+v, %v", d, err)
	}
	for _, tc := range [][2]string{{"0", "1Gi"}, {"4Gi", "2Gi"}, {"lots", "2Gi"}, {"1Gi", "huge"}} {
		if _, err := diskBounds(tc[0], tc[1]); !errors.Is(err, errBadDisk) {
			t.Errorf("%v: %v", tc, err)
		}
	}
}
