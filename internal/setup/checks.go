package setup

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/koblas/exemplar/internal/gh"
)

// Status is a check's outcome.
type Status string

// Outcomes, worst last.
const (
	StatusOK   Status = "ok"
	StatusWarn Status = "warn"
	StatusFail Status = "fail"
)

// Check is one thing an installation needs, as /ui/setup shows it: what
// was checked, what was found and, when it isn't ok, what to do.
type Check struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// App is the review app's view of GitHub that the checks need (gh.App).
type App interface {
	OrgInstallation(ctx context.Context, account string) (int64, error)
	ConfigInstallation(ctx context.Context) (int64, error)
	HookDeliveries(ctx context.Context, since time.Time) ([]gh.Delivery, error)
}

// Checker runs the checks that readiness can't: they depend on GitHub, so
// they are shown to people, never wired into a probe.
type Checker struct {
	App        App
	Org        string // the config repo's owner
	ConfigRepo string
	WebhookURL string
	// LastEvent is when exemplar-webhook last stored an event (ok false:
	// never).
	LastEvent func(ctx context.Context) (at time.Time, ok bool, err error)
}

// deliveryWindow is how far back the webhook checks look.
const deliveryWindow = 24 * time.Hour

// Run runs every check; each is bounded by ctx.
func (c Checker) Run(ctx context.Context) []Check {
	deliveries, dcheck := c.deliveries(ctx, time.Now())
	return []Check{c.installation(ctx), c.configRepo(ctx), dcheck, c.events(ctx, time.Now(), deliveries)}
}

func (c Checker) installation(ctx context.Context) Check {
	ch := Check{Name: "GitHub App installed on " + c.Org}
	id, err := c.App.OrgInstallation(ctx, c.Org)
	switch {
	case errors.Is(err, gh.ErrNotFound):
		ch.Status, ch.Detail = StatusFail, "The App works, but it isn't installed on "+c.Org+"."
		ch.Fix = "Install the App on " + c.Org + " from its settings page on GitHub, choosing the repos to review and exemplar-config."
	case err != nil:
		ch.Status, ch.Detail = StatusFail, "GitHub refused the App's credentials: "+err.Error()
		ch.Fix = "Check that the exemplar-github-app Secret's app-id and private-key belong to the same App; rerun setup if the key was revoked."
	default:
		ch.Status, ch.Detail = StatusOK, fmt.Sprintf("Installation %d.", id)
	}
	return ch
}

func (c Checker) configRepo(ctx context.Context) Check {
	ch := Check{Name: "App can read " + c.ConfigRepo}
	if _, err := c.App.ConfigInstallation(ctx); err != nil {
		ch.Status, ch.Detail = StatusFail, "The App's installation doesn't include "+c.ConfigRepo+"."
		ch.Fix = "Add " + c.ConfigRepo + " to the installation's repositories. Every workload clones its config from it."
		return ch
	}
	ch.Status, ch.Detail = StatusOK, "Configuration syncs from it."
	return ch
}

// deliveries checks GitHub's side: what it sent, and what came back.
func (c Checker) deliveries(ctx context.Context, now time.Time) ([]gh.Delivery, Check) {
	ch := Check{Name: "GitHub delivers webhooks"}
	ds, err := c.App.HookDeliveries(ctx, now.Add(-deliveryWindow))
	switch {
	case err != nil:
		ch.Status, ch.Detail = StatusWarn, "GitHub didn't list the App's deliveries: "+err.Error()
	case len(ds) == 0:
		ch.Status, ch.Detail = StatusWarn, "GitHub sent no events in the last 24 hours."
		ch.Fix = "Open or update a pull request on a repo the App is installed on, then reload."
	case ds[0].StatusCode < 200 || ds[0].StatusCode > 299:
		ch.Status = StatusFail
		ch.Detail = fmt.Sprintf("GitHub's last delivery (%s, %s ago) got %s.", ds[0].Event, now.Sub(ds[0].DeliveredAt).Round(time.Second), code(ds[0].StatusCode))
		ch.Fix = "Check that " + c.WebhookURL + " reaches exemplar-webhook: the tunnel or Ingress routes /webhook, " +
			"and no SSO or WAF rule blocks GitHub's hook addresses (api.github.com/meta). " +
			"A 401 means the webhook secret doesn't match the App's."
	default:
		ch.Status = StatusOK
		ch.Detail = fmt.Sprintf("Last delivery %s ago (%s), answered %d.", now.Sub(ds[0].DeliveredAt).Round(time.Second), ds[0].Event, ds[0].StatusCode)
	}
	return ds, ch
}

// events checks Exemplar's side: that deliveries were stored.
func (c Checker) events(ctx context.Context, now time.Time, ds []gh.Delivery) Check {
	ch := Check{Name: "exemplar-webhook stores events"}
	at, ok, err := c.LastEvent(ctx)
	switch {
	case err != nil:
		ch.Status, ch.Detail = StatusWarn, "Couldn't read the last event: "+err.Error()
	case !ok && len(ds) > 0 && ds[0].StatusCode >= 200 && ds[0].StatusCode <= 299:
		ch.Status, ch.Detail = StatusFail, "GitHub reports deliveries answered 2xx, but none were stored."
		ch.Fix = "Check exemplar-webhook's logs and its database connection."
	case !ok:
		ch.Status, ch.Detail = StatusWarn, "No events stored yet."
	default:
		ch.Status, ch.Detail = StatusOK, fmt.Sprintf("Last event stored %s ago.", now.Sub(at).Round(time.Second))
	}
	return ch
}

func code(status int) string {
	if status == 0 {
		return "no response (timeout or connection refused)"
	}
	return fmt.Sprintf("HTTP %d", status)
}
