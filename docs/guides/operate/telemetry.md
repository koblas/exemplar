# How to send telemetry

Traces and metrics go wherever the standard OpenTelemetry variables say, and nowhere when none are set. Only OTLP over HTTP (`http/protobuf`) is built in.

## 1. Create the ConfigMap

Every container reads the optional ConfigMap and Secret named `exemplar-otel` through `envFrom`. Put the endpoint and exporter settings in the ConfigMap:

```bash
kubectl -n exemplar create configmap exemplar-otel \
  --from-literal=OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector.observability.svc:4318 \
  --from-literal=OTEL_METRICS_EXPORTER=otlp \
  --from-literal=OTEL_METRIC_EXPORT_INTERVAL=60000
```

The per-signal `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` and `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT` work too.

## 2. Put credentials in the Secret

```bash
kubectl -n exemplar create secret generic exemplar-otel --from-literal=OTEL_EXPORTER_OTLP_HEADERS='Authorization=Bearer <token>'
```

## 3. Open the network path

The `allow-otel` NetworkPolicy opens port 4318 to pods labelled `app.kubernetes.io/component: opentelemetry-collector` in the cluster. A collector elsewhere needs an overlay, unless it is reached on 443 by pods that already have HTTPS egress.

## 4. Restart

The variables are read at start: roll the Deployments and StatefulSet. CronJobs pick them up on their next run.

Metric and trace payloads carry ids, durations, statuses and cost, never code. The `telemetry` Grafana board reads them back through a Prometheus-compatible endpoint ([How to set up Grafana](grafana.md)).
