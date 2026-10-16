package telemetry

import (
	"context"
	"strconv"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// The instruments behind the operations panels: model calls
// by outcome and latency, review duration, enqueue-to-post time, and
// GitHub rate-limit headroom. Attributes are low-cardinality names, never
// repo content. They are created on the global MeterProvider, which
// forwards to the one Setup installs (or does nothing without one).

var (
	once        sync.Once
	modelCalls  metric.Int64Counter
	modelTime   metric.Float64Histogram
	reviewTime  metric.Float64Histogram
	queueToPost metric.Float64Histogram
	ghRemaining metric.Int64Gauge
	ghRequests  metric.Int64Counter
)

// Latency buckets in seconds, from a fast model call to a slow review.
var buckets = []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600, 1200}

func instruments() {
	once.Do(func() {
		m := otel.Meter("github.com/koblas/exemplar")
		modelCalls, _ = m.Int64Counter("exemplar.model.calls", metric.WithDescription("Model provider calls by outcome"))
		modelTime, _ = m.Float64Histogram("exemplar.model.duration", metric.WithUnit("s"), metric.WithDescription("Model provider call latency"),
			metric.WithExplicitBucketBoundaries(buckets...))
		reviewTime, _ = m.Float64Histogram("exemplar.review.duration", metric.WithUnit("s"), metric.WithDescription("Review job duration by outcome"),
			metric.WithExplicitBucketBoundaries(buckets...))
		queueToPost, _ = m.Float64Histogram("exemplar.review.queue_to_post", metric.WithUnit("s"),
			metric.WithDescription("From the review job's enqueue (the webhook's routing) to its comments being posted"),
			metric.WithExplicitBucketBoundaries(buckets...))
		ghRemaining, _ = m.Int64Gauge("exemplar.github.rate_limit.remaining", metric.WithDescription("GitHub rate limit remaining, as last reported"))
		ghRequests, _ = m.Int64Counter("exemplar.github.requests", metric.WithDescription("GitHub API responses by status class"))
	})
}

// Model call outcomes.
const (
	OutcomeOK          = "ok"
	OutcomeRateLimited = "rate_limited" // 429
	OutcomeServerError = "server_error" // 5xx
	OutcomeClientError = "client_error" // other 4xx
	OutcomeError       = "error"        // no response
)

// StatusOutcome classifies an HTTP status (0: no response).
func StatusOutcome(status int) string {
	switch {
	case status == 0:
		return OutcomeError
	case status == 429:
		return OutcomeRateLimited
	case status >= 500:
		return OutcomeServerError
	case status >= 400:
		return OutcomeClientError
	}
	return OutcomeOK
}

// ModelCall records one provider call.
func ModelCall(ctx context.Context, provider, model, stage, caller, outcome string, d time.Duration) {
	instruments()
	attrs := metric.WithAttributes(attribute.String("provider", provider), attribute.String("model", model),
		attribute.String("stage", stage), attribute.String("caller", caller), attribute.String("outcome", outcome))
	modelCalls.Add(ctx, 1, attrs)
	modelTime.Record(ctx, d.Seconds(), attrs)
}

// ReviewDone records a review job's duration and outcome.
func ReviewDone(ctx context.Context, outcome string, d time.Duration) {
	instruments()
	reviewTime.Record(ctx, d.Seconds(), metric.WithAttributes(attribute.String("outcome", outcome)))
}

// QueueToPost records the time from a review's enqueue to its post.
func QueueToPost(ctx context.Context, d time.Duration) {
	instruments()
	queueToPost.Record(ctx, d.Seconds())
}

// GitHubResponse records a GitHub API response and, when it carried them,
// the rate-limit headers.
func GitHubResponse(ctx context.Context, installation int64, status int, resource, remaining string) {
	instruments()
	class := strconv.Itoa(status/100) + "xx"
	if status == 0 {
		class = "error"
	}
	ghRequests.Add(ctx, 1, metric.WithAttributes(attribute.String("status_class", class)))
	if n, err := strconv.ParseInt(remaining, 10, 64); err == nil {
		if resource == "" {
			resource = "core"
		}
		ghRemaining.Record(ctx, n, metric.WithAttributes(attribute.String("installation", strconv.FormatInt(installation, 10)), attribute.String("resource", resource)))
	}
}
