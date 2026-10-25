package telemetry

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// QueueStat is one River queue in one job state.
type QueueStat struct {
	Queue string
	State string
	Jobs  int64
	// OldestWaiting is the age of the state's oldest job waiting to run:
	// not running, and due now (snoozed jobs, which a full runner quota or
	// repo slot makes, included). 0 for running jobs.
	OldestWaiting time.Duration
}

// ObserveQueues exports the River queues (spec §2.4.2 C5, FR-D6):
// exemplar.queue.depth, jobs per queue and state, and
// exemplar.queue.oldest_age, seconds the oldest waiting job of each queue
// has waited (0 when its jobs all run; no series for a queue with no
// unfinished job). read is called on every collection;
// every control replica reports the same values, so aggregate with max.
// The returned func unregisters the callback.
func ObserveQueues(read func(context.Context) ([]QueueStat, error)) (func() error, error) {
	m := otel.Meter("github.com/koblas/exemplar")
	depth, err := m.Int64ObservableGauge("exemplar.queue.depth", metric.WithDescription("River jobs per queue and state"))
	if err != nil {
		return nil, err //nolint:wrapcheck // the SDK's error names the instrument
	}
	age, err := m.Float64ObservableGauge("exemplar.queue.oldest_age", metric.WithUnit("s"),
		metric.WithDescription("Age of each queue's oldest job waiting to run"))
	if err != nil {
		return nil, err //nolint:wrapcheck // the SDK's error names the instrument
	}
	reg, err := m.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		stats, err := read(ctx)
		if err != nil {
			return err
		}
		oldest := map[string]time.Duration{}
		for _, s := range stats {
			o.ObserveInt64(depth, s.Jobs, metric.WithAttributes(attribute.String("queue", s.Queue), attribute.String("state", s.State)))
			oldest[s.Queue] = max(oldest[s.Queue], s.OldestWaiting)
		}
		for q, d := range oldest {
			o.ObserveFloat64(age, d.Seconds(), metric.WithAttributes(attribute.String("queue", q)))
		}
		return nil
	}, depth, age)
	if err != nil {
		return nil, err //nolint:wrapcheck // the SDK's error names the callback
	}
	return reg.Unregister, nil
}
