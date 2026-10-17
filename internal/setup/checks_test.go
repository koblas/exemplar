package setup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/koblas/exemplar/internal/gh"
)

type fakeApp struct {
	orgErr, configErr, deliveriesErr error
	deliveries                       []gh.Delivery
}

func (f fakeApp) OrgInstallation(context.Context, string) (int64, error) { return 7, f.orgErr }
func (f fakeApp) ConfigInstallation(context.Context) (int64, error)      { return 7, f.configErr }
func (f fakeApp) HookDeliveries(context.Context, time.Time) ([]gh.Delivery, error) {
	return f.deliveries, f.deliveriesErr
}

func TestChecks(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	ok := []gh.Delivery{{StatusCode: 200, Event: "pull_request", DeliveredAt: now.Add(-time.Minute)}}
	bad := []gh.Delivery{{StatusCode: 502, Event: "pull_request", DeliveredAt: now.Add(-time.Minute)}}
	stored := func(context.Context) (time.Time, bool, error) { return now.Add(-time.Minute), true, nil }
	none := func(context.Context) (time.Time, bool, error) { return time.Time{}, false, nil }
	tests := []struct {
		name  string
		app   fakeApp
		last  func(context.Context) (time.Time, bool, error)
		want  []Status
		fixes string // a substring of some check's Fix
	}{
		{"healthy", fakeApp{deliveries: ok}, stored, []Status{StatusOK, StatusOK, StatusOK, StatusOK}, ""},
		{
			"not installed",
			fakeApp{orgErr: fmt.Errorf("%w: x", gh.ErrNotFound), deliveries: ok},
			stored,
			[]Status{StatusFail, StatusOK, StatusOK, StatusOK},
			"Install the App on acme",
		},
		{
			"bad key",
			fakeApp{orgErr: errors.New("401"), deliveries: ok},
			stored,
			[]Status{StatusFail, StatusOK, StatusOK, StatusOK},
			"app-id and private-key",
		},
		{
			"config repo missing",
			fakeApp{configErr: gh.ErrNotFound, deliveries: ok},
			stored,
			[]Status{StatusOK, StatusFail, StatusOK, StatusOK},
			"Add acme/exemplar-config",
		},
		{"no deliveries yet", fakeApp{}, none, []Status{StatusOK, StatusOK, StatusWarn, StatusWarn}, "Open or update a pull request"},
		{"deliveries failing", fakeApp{deliveries: bad}, none, []Status{StatusOK, StatusOK, StatusFail, StatusWarn}, "reaches exemplar-webhook"},
		{"delivered, not stored", fakeApp{deliveries: ok}, none, []Status{StatusOK, StatusOK, StatusOK, StatusFail}, "exemplar-webhook's logs"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Checker{
				App: tc.app, Org: "acme", ConfigRepo: "acme/exemplar-config", WebhookURL: "https://e.acme.com/webhook",
				LastEvent: tc.last, Now: func() time.Time { return now },
			}.Run(t.Context())
			if len(got) != len(tc.want) {
				t.Fatalf("Run = %+v", got)
			}
			var fixes []string
			for i, c := range got {
				if c.Status != tc.want[i] {
					t.Errorf("%s = %s (%s), want %s", c.Name, c.Status, c.Detail, tc.want[i])
				}
				if c.Status != StatusOK && c.Detail == "" {
					t.Errorf("%s has no detail", c.Name)
				}
				fixes = append(fixes, c.Fix)
			}
			if tc.fixes != "" && !strings.Contains(strings.Join(fixes, "\n"), tc.fixes) {
				t.Errorf("fixes %q don't mention %q", fixes, tc.fixes)
			}
		})
	}
}
