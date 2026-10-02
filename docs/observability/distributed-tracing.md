# Distributed tracing

Distributed tracing is the third observability signal in Cozystack, alongside metrics (VictoriaMetrics) and logs (VictoriaLogs). This guide explains what the platform provides, how to send traces from your workloads, what each managed engine can and cannot emit on its own, and how tenants can share one traces store instead of running their own. The design is [cozystack/community `design-proposals/distributed-tracing`](https://github.com/cozystack/community/tree/main/design-proposals/distributed-tracing).

## What the platform provides

A tenant's Monitoring application stands up two pieces once tracing is configured:

- A **VictoriaTraces backend**, one per `tracingStorages` entry (a `VTCluster` in `cluster` mode, a `VTSingle` in `single` mode), with a Grafana Jaeger datasource named `vtraces-<entry name>` for viewing its spans.
- A **per-tenant OpenTelemetry Collector**, the app-facing OTLP ingest gateway, reachable in the tenant namespace at:
  - **`otel-traces:4317`**: OTLP over gRPC
  - **`otel-traces:4318`**: OTLP over HTTP

The collector applies head sampling (10% by default) and optional attribute redaction, then forwards spans to the backend. It lives in your tenant namespace, so traffic from your workloads to it stays inside the namespace. With the default per-tenant backend the spans stay there too; with [shared-central tracing](#shared-central-tracing) the collector forwards them to a store in tenant-root.

What the platform does **not** do is emit spans for you: producing spans is the workload's job. The platform delivers the transport, storage and tenancy; the *depth* of what shows up is a property of how each application or engine is instrumented.

## Sending traces from your application (client-side)

This is the primary path and it works for any workload with OpenTelemetry instrumentation (an OTel SDK in your code, or an auto-instrumentation agent). You do **not** hard-code the endpoint: point your workload at the in-namespace collector through the standard OpenTelemetry environment variables:

```yaml
env:
  # Send to the per-tenant collector over OTLP/HTTP (use :4317 for gRPC).
  - name: OTEL_EXPORTER_OTLP_ENDPOINT
    value: "http://otel-traces:4318"
  - name: OTEL_EXPORTER_OTLP_PROTOCOL
    value: "http/protobuf"
  # Name this service and the environment it runs in.
  - name: OTEL_SERVICE_NAME
    value: "my-app"
  - name: OTEL_RESOURCE_ATTRIBUTES
    value: "deployment.environment.name=production"
```

The variables are defined by the OpenTelemetry specification, and the official SDKs and agents read them, but support for each variable differs per language: check your SDK against the [specification compliance matrix](https://github.com/open-telemetry/opentelemetry-specification/blob/main/spec-compliance-matrix.md). For a JVM workload you can add the OpenTelemetry Java agent (`-javaagent:/otel/opentelemetry-javaagent.jar`), and the same variables drive it with no code changes. To use gRPC instead, set the endpoint to `http://otel-traces:4317` **and** `OTEL_EXPORTER_OTLP_PROTOCOL=grpc`; with [shared-central tracing](#shared-central-tracing), stay on HTTP, since spans refused over the tenant's write rate are retried only over HTTP.

You do not need to tag your tenant: the backend a tenant writes to is its own, and in shared-central mode the platform attributes every span to its tenant itself. `service.namespace` is for grouping your own services (for example by team), not for the tenant.

Once spans arrive, they are visible in Grafana under **Explore**, in the traces datasource.

> **Correlation is not wired yet.** Cross-signal pivots, span to logs by `trace_id` and span to RED metrics, are part of the design but not configured: the traces datasource carries no links to logs or metrics, and no `spanmetrics` connector produces span metrics. Traces are viewable on their own until that follow-up work lands.

## What each managed engine can emit

Tracing depth differs per engine, because a sidecar cannot see *inside* a server process. Most managed servers do **not** emit OTLP spans themselves; their tracing is client-side (instrument the application that talks to them).

| Engine | Server-side OTLP spans? | How you get traces |
| --- | --- | --- |
| **ClickHouse** | Partial: native span *table*, no push, disabled | The server can record internal query spans into `system.opentelemetry_span_log`, which the Cozystack chart removes (see below). Client-side propagation gives end-to-end traces. |
| **PostgreSQL** | No (extension-gated) | Statement-level spans need the `pg_tracing` extension, which is not in the `sharedPreloadLibraries` allowlist, and `shared_preload_libraries` cannot be set through `parameters`. Use client-side instrumentation. |
| **Kafka** | No (brokers) | Brokers do not emit request spans; instrument producers/consumers (client-side). JVM clients can use the OTel Java agent. |
| **RabbitMQ** | No (non-OTLP) | The `rabbitmq_tracing` plugin emits RabbitMQ-format events, not OTLP. Instrument publishers/consumers (client-side). |
| **NATS** | No | Server message-tracing emits NATS-format events, not OTLP. Instrument clients (context is propagated in message headers). |
| **MariaDB** | No | No server-side OTLP. Instrument the application (client-side). |
| **Redis** | No | No server-side OTLP. Instrument the application (client-side). |

The takeaway: for every engine here, the way to get a useful request trace is to **instrument the application** that issues the queries or messages and point it at `otel-traces` as shown above. The engine then appears as a span in your application's trace when context is propagated.

## ClickHouse: internal query spans

ClickHouse is the one engine that produces spans natively: it can record query-execution spans into the `system.opentelemetry_span_log` system table and take part in a trace whose context a client propagates through the W3C `traceparent` header. The Cozystack ClickHouse chart removes that log in its server configuration, alongside the other system logs it disables, and the ClickHouse application exposes no setting to turn it back on, so these spans are not available today.

Even with the log enabled, ClickHouse has **no native OTLP push**: getting those spans into a collector, and so into Grafana, needs a shipper that reads `system.opentelemetry_span_log` and forwards OTLP, which neither Vector nor Fluent Bit does. Both are follow-up work; end-to-end request traces come from client-side instrumentation, as for every other engine.

## Sampling, redaction, and tenancy

- **Sampling:** the collector head-samples traces at 10% by default (`tracingCollector.samplingPercentage`). The decision is made per trace ID, so a kept trace keeps all of its spans and several collector replicas agree on it. A busy application keeps about one trace in ten, so the backend is not overwhelmed.
- **Redaction (PII):** span attributes can carry sensitive data (SQL text with literals, parameters, connection strings). List such keys in `tracingCollector.redactAttributes` to drop them before export.
- **Tenancy:** with the default per-tenant backend, spans never leave the tenant namespace, and only the tenant's own Grafana reads them. With shared-central tracing they are stored in tenant-root; see below for how tenants are kept apart there.

## Shared-central tracing

Instead of a VictoriaTraces backend per tenant, tenants can send their spans to one store in tenant-root. Applications do not change: they keep sending to `otel-traces` in their own namespace, and the collector forwards to the shared store.

### Hosting the store (tenant-root)

tenant-root opts in with `tracingCentralHost: true` on its Monitoring application, next to a `tracingStorages` entry named `generic` in `cluster` mode; that store is then shared with every tenant that opts in. A vmauth in tenant-root fronts it, and every central tenant's collector and Grafana go through that vmauth.

All central tenants share the store's disk and retention with tenant-root, and nothing reserves space per tenant. Every traces store is bounded by disk usage: without `retentionDiskUsageBytes` it drops its oldest day once its volume is 80% full. VictoriaTraces always keeps the last two days, though, so the disk cap alone does not stop a fast writer from filling the volume. Each tenant is therefore also held to a write rate, which the platform sets with `monitoring.tracingCentralTenantBytesPerSecond` in the platform package. By default it is about 97 KiB/s, at which one tenant alone cannot fill the disk cap of a default 10Gi store within two days, so raise it with the store. A change reaches every tenant's collector through the `cozystack-values` Secret, and upgrades every release in every tenant once. Each tenant's collector enforces it and refuses data above it. An OTLP/HTTP exporter gets a 429 and retries; an OTLP/gRPC exporter gets `RESOURCE_EXHAUSTED` without the `RetryInfo` the OTLP specification requires for a retry, so it drops those spans. Applications of central tenants should therefore export over HTTP. Several tenants writing at their limits together can still exceed the cap, so size the volume for the tenants you admit.

There is no per-tenant allow-list: with hosting on, every tenant that sets `tracingCentral` is admitted, nested tenants included. A tenant-root that needs to limit who shares its store keeps `tracingCentralHost` off.

### Opting a tenant in

Set `tracingCentral: true` on the Tenant, which needs `monitoring: true` on the same tenant. The tenant chart then renders the egress rules the collector and Grafana need to reach the vmauth, and switches the tenant's Monitoring to the shared store. If tenant-root does not host the store, the tenant's Monitoring render fails with a message naming the prerequisite, rather than dropping spans.

In the tenant's Grafana the shared store appears as the `traces-central` datasource. The tenant's own local stores, if it lists any, keep their datasources, so traces stored before the switch stay readable until the tenant removes those entries.

### Isolation and the trust boundary

The store itself authorizes nothing; it takes the tenant from request headers. Each central tenant gets a VMUser that pins an account derived from its namespace name and UID, for its collector's writes and its Grafana's reads alike, and vmauth replaces any account a client sends. Tenant pods can reach only the vmauth proxy port, and only from pods carrying the collector's or Grafana's label, with the tenant's credentials. So tenants sharing the store cannot read or write each other's traces.

tenant-root is inside the store's trust boundary. Its workloads reach the store directly, and one that learns a tenant's account can read and write it. Whoever tenant-root admits to its Grafana can read every account through tenant-root's own datasource for the store, which does not go through vmauth. Who that is follows tenant-root Grafana's OIDC mode: with `System`, tenant-root's own groups, which already hold the same access level in every tenant under it; with `CustomConfig` or `None`, whoever that configuration admits.

### Lifecycle

- **The account follows the namespace.** It is derived from the namespace name and UID, so a re-created namespace, a restored one included, starts a new account and does not see the spans stored before.
- **Turning central on** keeps the tenant's local stores and their datasources. Right after it, spans the collector sends before the vmauth has loaded the tenant's login get a 401, which the exporter does not retry, so those spans are dropped. The same happens after the tenant's `traces-central-credentials` Secret is re-created with a new password.
- **Turning central off** removes the tenant's login to the shared store. The spans it wrote there stay until the store's retention drops them and cannot be read from the tenant while it is off; turning it on again on the same namespace makes them readable again. Spans sent between turning it off and the collector switching back to a local store may be dropped, since the egress rule to the shared store goes first.
- **Deleting a tenant** leaves its spans in the shared store until retention drops them.

### Stopping hosting

Turn `tracingCentral` off on every central tenant and let its Monitoring reconcile before tenant-root stops hosting. tenant-root's render refuses to stop hosting while a central tenant's VMUser remains and lists the tenants still holding one. Removing tenant-root's Monitoring altogether is not a render and is not refused; until the central tenants turn central off, every Monitoring render of theirs fails, their metrics, logs and alerting changes included.

The two refusals read each other's objects when they render, so a tenant turning central on while tenant-root stops hosting can pass both. That tenant's collector then exports to a vmauth that is gone while every release reports Ready, until its Monitoring renders again. Recover by hosting again; turning the tenant's `tracingCentral` off instead stops the silent drop, and turning it on again is then refused until tenant-root hosts.

## Prerequisites

- The tenant's Monitoring application with a `tracingStorages` entry, or shared-central tracing turned on for the tenant, and `tracingCollector.enabled` left on (the default).
- For shared-central tracing: tenant-root hosting the store, as described above.
- A workload with OpenTelemetry instrumentation (SDK or auto-instrumentation agent).
