# Distributed tracing

Distributed tracing is the third observability signal in Cozystack, alongside metrics (VictoriaMetrics) and logs (VictoriaLogs). This guide explains what the platform provides, how to send traces from your workloads, and — honestly — what each managed engine can and cannot emit on its own.

> **Status: in progress.** Distributed tracing is being implemented (design [cozystack/community#38](https://github.com/cozystack/community/pull/38); tracking [#3761](https://github.com/cozystack/cozystack/issues/3761)). This guide describes the intended usage. The service names, ports, and value keys below (`otel-traces`, `tracingStorages`, `tracingCollector`, …) are established by the implementation PRs and are **non-normative until they land** — treat them as illustrative if you are reading this before the feature ships in your cluster.

## What the platform provides

The monitoring stack stands up two pieces per tenant:

- A **VictoriaTraces backend** (`tracingStorages` → `VTCluster`/`VTSingle`) that stores spans, exposed through a Grafana traces datasource for viewing them.
- A **per-tenant OpenTelemetry Collector** — the app-facing OTLP ingest gateway — reachable in the tenant namespace at:
  - **`otel-traces:4317`** — OTLP over gRPC
  - **`otel-traces:4318`** — OTLP over HTTP

The collector applies head sampling (10% by default) and optional attribute redaction, then forwards spans to the backend. It lives in your tenant namespace, so traffic from your workloads to it never crosses the tenant boundary.

What the platform does **not** do is emit spans for you: producing spans is the workload's job. The platform delivers the transport, storage, tenancy, and correlation; the *depth* of what shows up is a property of how each application or engine is instrumented.

## Sending traces from your application (client-side)

This is the primary path and it works for any workload with OpenTelemetry instrumentation (an OTel SDK in your code, or an auto-instrumentation agent). You do **not** hard-code the endpoint — point your workload at the in-namespace collector through the standard OpenTelemetry environment variables:

```yaml
env:
  # Send to the per-tenant collector over OTLP/HTTP (use :4317 for gRPC).
  - name: OTEL_EXPORTER_OTLP_ENDPOINT
    value: "http://otel-traces:4318"
  - name: OTEL_EXPORTER_OTLP_PROTOCOL
    value: "http/protobuf"
  # Name this service and tag its tenant so spans are attributed correctly.
  - name: OTEL_SERVICE_NAME
    value: "my-app"
  - name: OTEL_RESOURCE_ATTRIBUTES
    value: "service.namespace=my-tenant,deployment.environment=prod"
```

Any OpenTelemetry SDK or agent reads these variables automatically, so the same snippet works across languages. For a JVM workload you can add the OpenTelemetry Java agent (`-javaagent:/otel/opentelemetry-javaagent.jar`) and the same variables drive it with zero code changes. To use gRPC instead, set the endpoint to `http://otel-traces:4317` **and** `OTEL_EXPORTER_OTLP_PROTOCOL=grpc`.

Once spans arrive, they are visible in Grafana under **Explore → the traces datasource**.

> **Correlation is not wired yet.** Cross-signal pivots — span → logs (by `trace_id`) and span → RED/span metrics — are part of the design but not yet configured: the traces datasource carries no derived fields, and no `spanmetrics` connector produces span metrics. Trace→metrics in particular is a later-stage capability (it needs the collector's `spanmetrics` connector), and the exact traces datasource type is still being settled in the implementation. Until then, traces are viewable on their own; the logs/metrics pivots are follow-up work.

## What each managed engine can emit

Tracing depth differs per engine, because a sidecar cannot see *inside* a server process. The table below is the honest breakdown — most managed servers do **not** emit OTLP spans themselves; their tracing is client-side (instrument the application that talks to them).

| Engine | Server-side OTLP spans? | How you get traces |
|---|---|---|
| **ClickHouse** | Partial — native span *table*, no push | Enable `opentelemetry_span_log` to record internal query spans (see below); central export needs a shipper. Client-side propagation gives end-to-end traces. |
| **PostgreSQL** | No (extension-gated) | Statement-level spans need the `pg_tracing` extension; it is not available (loading preload extensions via `shared_preload_libraries` is restricted in the chart). Use client-side instrumentation. |
| **Kafka** | No (brokers) | Brokers do not emit request spans; instrument producers/consumers (client-side). JVM clients can use the OTel Java agent. |
| **RabbitMQ** | No (non-OTLP) | The `rabbitmq_tracing` plugin emits RabbitMQ-format events, not OTLP. Instrument publishers/consumers (client-side). |
| **NATS** | No | Server message-tracing emits NATS-format events, not OTLP. Instrument clients (context is propagated in message headers). |
| **MariaDB** | No | No server-side OTLP. Instrument the application (client-side). |
| **Redis** | No | No server-side OTLP. Instrument the application (client-side). |

The takeaway: for every engine here, the way to get a useful request trace is to **instrument the application** that issues the queries/messages and point it at `otel-traces` as shown above. The engine then appears as a span in your application's trace when context is propagated.

## ClickHouse: internal query spans

ClickHouse is the one engine that produces spans natively — it records query-execution spans into the `system.opentelemetry_span_log` system table when the log is enabled (Cozystack currently disables it). Enabling it makes those spans **queryable inside ClickHouse itself**, and lets ClickHouse participate in a trace whose context a client propagates via the W3C `traceparent`.

Note that ClickHouse has **no native OTLP push**: getting those internal spans into the central collector (and thus Grafana/VictoriaTraces) requires a shipper that reads `system.opentelemetry_span_log` and forwards OTLP — a bespoke agent, since neither Vector nor Fluent Bit reads ClickHouse tables directly. That forwarder is tracked as a separate follow-up; until it lands, ClickHouse's internal spans are available by querying the table, while end-to-end request traces come from client-side instrumentation like every other engine.

## Sampling, redaction, and tenancy

- **Sampling:** the collector head-samples at 10% by default (configurable via `tracingCollector.samplingPercentage`). Traces are best-effort; a busy app keeps ~10% of spans so the backend is not overwhelmed.
- **Redaction (PII):** span attributes can carry sensitive data (SQL text with literals, parameters, connection strings). Configure `tracingCollector.redactAttributes` to drop such keys before export.
- **Tenancy:** on the default per-tenant backend, spans never leave the tenant namespace. Always set a tenant-identifying `OTEL_RESOURCE_ATTRIBUTES` so spans are attributed to the right service and tenant.

## Prerequisites

- The monitoring stack deployed with a `tracingStorages` backend and the `tracingCollector` enabled (on by default when tracing is configured).
- A workload with OpenTelemetry instrumentation (SDK or auto-instrumentation agent).
