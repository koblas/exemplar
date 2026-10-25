package telemetry

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestFromEnv(t *testing.T) {
	for _, c := range []struct {
		name string
		env  map[string]string
		want Signals
		err  bool
	}{
		{"nothing set", nil, Signals{}, false},
		{"shared endpoint", map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://collector:4318"}, Signals{true, true}, false},
		{"metrics only", map[string]string{"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT": "http://m:4318/v1/metrics"}, Signals{false, true}, false},
		{"traces off", map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://c:4318", "OTEL_TRACES_EXPORTER": "none"}, Signals{false, true}, false},
		{"sdk disabled", map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://c:4318", "OTEL_SDK_DISABLED": "true"}, Signals{}, false},
		{"grpc refused", map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://c:4317", "OTEL_EXPORTER_OTLP_PROTOCOL": "grpc"}, Signals{}, true},
		{"unknown exporter", map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://c:4318", "OTEL_METRICS_EXPORTER": "prometheus"}, Signals{}, true},
	} {
		got, err := FromEnv(func(k string) string { return c.env[k] })
		if (err != nil) != c.err || got != c.want {
			t.Errorf("%s: %+v, %v", c.name, got, err)
		}
	}
}

func TestInstruments(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetMeterProvider(mp)
	defer func() { _ = mp.Shutdown(context.Background()) }()
	ctx := t.Context()
	ModelCall(ctx, "anthropic", "claude-sonnet-5-5", "generator", "production", StatusOutcome(429), time.Second)
	ModelCall(ctx, "anthropic", "claude-sonnet-5-5", "generator", "production", OutcomeOK, 2*time.Second)
	ReviewDone(ctx, "reviewed", time.Minute)
	QueueToPost(ctx, 90*time.Second)
	GitHubResponse(ctx, 42, 200, "graphql", "4321")
	GitHubResponse(ctx, 42, 0, "", "")

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatal(err)
	}
	seen := map[string]metricdata.Aggregation{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			seen[m.Name] = m.Data
		}
	}
	for _, name := range []string{
		"exemplar.model.calls", "exemplar.model.duration", "exemplar.review.duration", "exemplar.review.queue_to_post",
		"exemplar.github.rate_limit.remaining", "exemplar.github.requests",
	} {
		if _, ok := seen[name]; !ok {
			t.Errorf("no %s (have %v)", name, seen)
		}
	}
	if s, ok := seen["exemplar.model.calls"].(metricdata.Sum[int64]); !ok || len(s.DataPoints) != 2 {
		t.Errorf("model calls = %+v", seen["exemplar.model.calls"])
	}
	if g, ok := seen["exemplar.github.rate_limit.remaining"].(metricdata.Gauge[int64]); !ok || len(g.DataPoints) != 1 || g.DataPoints[0].Value != 4321 {
		t.Errorf("rate limit = %+v", seen["exemplar.github.rate_limit.remaining"])
	}
	for status, want := range map[int]string{0: OutcomeError, 200: OutcomeOK, 404: OutcomeClientError, 429: OutcomeRateLimited, 503: OutcomeServerError} {
		if got := StatusOutcome(status); got != want {
			t.Errorf("StatusOutcome(%d) = %s", status, got)
		}
	}
}

func TestObserveQueues(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetMeterProvider(mp)
	defer func() { _ = mp.Shutdown(context.Background()) }()
	unregister, err := ObserveQueues(func(context.Context) ([]QueueStat, error) {
		return []QueueStat{
			{Queue: "reviews", State: "available", Jobs: 3, OldestWaiting: 4 * time.Minute},
			{Queue: "reviews", State: "scheduled", Jobs: 2, OldestWaiting: 6 * time.Minute}, // snoozed on the quota
			{Queue: "reviews", State: "running", Jobs: 4},
			{Queue: "events", State: "running", Jobs: 1},
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unregister() }()

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &rm); err != nil {
		t.Fatal(err)
	}
	depth, age := map[string]int64{}, map[string]float64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch m.Name {
			case "exemplar.queue.depth":
				g, ok := m.Data.(metricdata.Gauge[int64])
				if !ok {
					t.Fatalf("depth is %T", m.Data)
				}
				for _, p := range g.DataPoints {
					q, _ := p.Attributes.Value("queue")
					s, _ := p.Attributes.Value("state")
					depth[q.AsString()+"/"+s.AsString()] = p.Value
				}
			case "exemplar.queue.oldest_age":
				g, ok := m.Data.(metricdata.Gauge[float64])
				if !ok {
					t.Fatalf("oldest age is %T", m.Data)
				}
				for _, p := range g.DataPoints {
					q, _ := p.Attributes.Value("queue")
					age[q.AsString()] = p.Value
				}
			}
		}
	}
	if len(depth) != 4 || depth["reviews/available"] != 3 || depth["reviews/running"] != 4 {
		t.Errorf("depth = %v", depth)
	}
	if len(age) != 2 || age["reviews"] != 360 || age["events"] != 0 {
		t.Errorf("oldest age = %v", age)
	}
}
