// Package telemetry sets up structured logging and OpenTelemetry traces and
// metrics. Destinations come only from the standard OTel environment
// variables, so nothing is exported unless an endpoint is set:
//
//	OTEL_EXPORTER_OTLP_ENDPOINT          both signals (OTLP over HTTP)
//	OTEL_EXPORTER_OTLP_TRACES_ENDPOINT   traces only
//	OTEL_EXPORTER_OTLP_METRICS_ENDPOINT  metrics only
//	OTEL_EXPORTER_OTLP_HEADERS (and the per-signal _HEADERS), _TIMEOUT, _INSECURE
//	OTEL_EXPORTER_OTLP_PROTOCOL          http/protobuf (the only one built in)
//	OTEL_TRACES_EXPORTER, OTEL_METRICS_EXPORTER  otlp (default) or none
//	OTEL_METRIC_EXPORT_INTERVAL          milliseconds (default 60000)
//	OTEL_SDK_DISABLED=true               turns both off
//	OTEL_SERVICE_NAME, OTEL_RESOURCE_ATTRIBUTES
//
// Spans and metrics never carry code, prompts, secrets or finding text.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// errUnsupported reports an OTel setting this build cannot honor.
var errUnsupported = errors.New("is not supported")

// Signals says which exporters the environment asks for.
type Signals struct{ Traces, Metrics bool }

// FromEnv reads the OTel environment variables. It fails on settings this
// build cannot honor rather than silently exporting nothing.
func FromEnv(getenv func(string) string) (Signals, error) {
	if strings.EqualFold(getenv("OTEL_SDK_DISABLED"), "true") {
		return Signals{}, nil
	}
	for _, k := range []string{"OTEL_EXPORTER_OTLP_PROTOCOL", "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "OTEL_EXPORTER_OTLP_METRICS_PROTOCOL"} {
		if p := getenv(k); p != "" && p != "http/protobuf" {
			return Signals{}, fmt.Errorf("telemetry: %s=%s %w; use http/protobuf", k, p, errUnsupported)
		}
	}
	on := func(exporterVar, endpointVar string) (bool, error) {
		switch e := strings.ToLower(getenv(exporterVar)); e {
		case "none":
			return false, nil
		case "", "otlp":
		default:
			return false, fmt.Errorf("telemetry: %s=%s %w; use otlp or none", exporterVar, e, errUnsupported)
		}
		return getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" || getenv(endpointVar) != "", nil
	}
	var s Signals
	var err error
	if s.Traces, err = on("OTEL_TRACES_EXPORTER", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"); err != nil {
		return Signals{}, err
	}
	if s.Metrics, err = on("OTEL_METRICS_EXPORTER", "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT"); err != nil {
		return Signals{}, err
	}
	return s, nil
}

// Setup installs a JSON slog logger as the default and, when the
// environment configures them, OTLP trace and metric exporters. The
// returned function flushes and stops both.
func Setup(ctx context.Context, service string) (*slog.Logger, func(context.Context) error, error) {
	level := slog.LevelInfo
	if os.Getenv("EXEMPLAR_LOG_LEVEL") == "debug" {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})).With("service", service)
	slog.SetDefault(log)

	otel.SetTextMapPropagator(propagation.TraceContext{})
	sig, err := FromEnv(os.Getenv)
	if err != nil {
		return nil, nil, err
	}
	if !sig.Traces && !sig.Metrics {
		return log, func(context.Context) error { return nil }, nil
	}
	// The environment's service name and attributes win over the default.
	res, err := sdkresource.Merge(sdkresource.Default(), sdkresource.NewWithAttributes(semconv.SchemaURL, semconv.ServiceName(service)))
	if err != nil && !errors.Is(err, sdkresource.ErrPartialResource) {
		return nil, nil, fmt.Errorf("telemetry: build resource: %w", err)
	}
	if res, err = sdkresource.Merge(res, sdkresource.Environment()); err != nil && !errors.Is(err, sdkresource.ErrPartialResource) {
		return nil, nil, fmt.Errorf("telemetry: merge environment resource: %w", err)
	}
	var stops []func(context.Context) error
	if sig.Traces {
		exp, err := otlptracehttp.New(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("telemetry: trace exporter: %w", err)
		}
		tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp), sdktrace.WithResource(res))
		otel.SetTracerProvider(tp)
		stops = append(stops, tp.Shutdown)
	}
	if sig.Metrics {
		exp, err := otlpmetrichttp.New(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("telemetry: metric exporter: %w", err)
		}
		mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp)), sdkmetric.WithResource(res))
		otel.SetMeterProvider(mp)
		stops = append(stops, mp.Shutdown)
	}
	log.Info("telemetry exporting", "traces", sig.Traces, "metrics", sig.Metrics)
	return log, func(ctx context.Context) error {
		errs := make([]error, 0, len(stops))
		for _, stop := range stops {
			errs = append(errs, stop(ctx))
		}
		return errors.Join(errs...)
	}, nil
}
