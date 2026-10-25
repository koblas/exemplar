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
