# Talos Log Collector

A node-local Vector DaemonSet that receives Talos machine and kernel logs and
forwards them to the in-cluster VictoriaLogs.

Talos keeps its system and kernel (kmsg) logs inside the OS and does not write
them to a host file that the default Fluent Bit `tail` input could read, so
those logs are never picked up by `monitoring-agents`. Fluent Bit there is
tail-only and the vendored chart offers no way to add a loopback TCP receiver,
so extending it is not an option; this package runs a dedicated Vector receiver
on every node instead. Talos pushes its logs into it over the node loopback,
and Vector forwards them to the tenant VictoriaLogs
(`vlinsert-generic.<target>.svc`) next to the container, audit and event logs
already collected by `monitoring-agents`.

## How it works

Vector runs as a DaemonSet on the pod network, so cluster DNS resolves the
vlinsert service. It listens on a TCP socket published on the node loopback via
a `hostPort` bound to `hostIP: 127.0.0.1`. Because the pod is not on the host
network, the outbound path to vlinsert keeps working; the inbound loopback
socket is provided by the hostPort.

Talos exposes its logs through two independent config paths, and both must
point at this socket to collect everything:

- `machine.logging.destinations` forwards the JSON-lines **service** logs
  (kubelet, etcd, apid and the other machine services).
- A `KmsgLogConfig` document forwards the **kernel** (kmsg) stream, including
  driver messages such as DRBD. `machine.logging.destinations` does not carry
  kmsg: Talos builds the kmsg destination list only from `KmsgLogConfig`
  documents and the `talos.logging.kernel=` kernel argument, so without one the
  kernel logs never leave the node. Both endpoints may point at the same
  socket; `KmsgLogConfig` only supports the `json_lines` format.

## Talos configuration (required)

This package deploys only the receiver. Each Talos node must be told to push
both its service logs and its kernel logs to the loopback socket. Add this to
the machine config and apply it:

```yaml
machine:
  logging:
    destinations:
      - endpoint: "tcp://127.0.0.1:5170/"
        format: "json_lines"
---
apiVersion: v1alpha1
kind: KmsgLogConfig
name: talos-log-collector
url: "tcp://127.0.0.1:5170/"
```

> Omitting the `KmsgLogConfig` document collects service logs only; kernel
> (kmsg) logs, including the DRBD messages, will silently never arrive.

> Kernel (kmsg) records carry no wall-clock timestamp (only a monotonic
> clock), so VictoriaLogs stamps them with the ingest time rather than the
> `talos-time` used for service logs.

> Early-boot kernel panics, before the network is up, are not captured by any
> in-cluster collector. Use the node BMC serial console for those.

## Notes

- The package is opt-in via `bundles.enabledPackages` and stays inert until the
  Talos machine config above is applied on the nodes.
- If the target tenant has no VictoriaLogs (vlinsert) yet, Vector's sink retries
  and backpressures the source: the pod stays Running, but Talos logs are
  dropped until vlinsert exists.
- Teardown: removing the package from `bundles.enabledPackages` does not
  uninstall it (Package CRs carry `helm.sh/resource-policy: keep`). Delete it
  explicitly with
  `kubectl delete package.cozystack.io cozystack.talos-log-collector`.

## Parameters

### Common parameters

| Name               | Description                                                                              | Type       | Value   |
| ------------------ | ---------------------------------------------------------------------------------------- | ---------- | ------- |
| `logLevel`         | Vector log verbosity (trace, debug, info, warn, error).                                  | `string`   | `warn`  |
| `listenPort`       | TCP port bound on the node loopback (127.0.0.1) where Talos machine.logging pushes logs. | `int`      | `5170`  |
| `resources`        | Compute resources for the collector.                                                     | `object`   | `{}`    |
| `resources.cpu`    | CPU request.                                                                             | `quantity` | `100m`  |
| `resources.memory` | Memory request and limit.                                                                | `quantity` | `128Mi` |


### VictoriaLogs destination

| Name            | Description                                                                                                                       | Type     | Value         |
| --------------- | --------------------------------------------------------------------------------------------------------------------------------- | -------- | ------------- |
| `global`        | Global values block; `target` selects the destination tenant.                                                                     | `object` | `{}`          |
| `global.target` | Tenant whose VictoriaLogs (vlinsert-generic.<target>.svc) receives the logs. Defaults to tenant-root; override via bundle values. | `string` | `tenant-root` |

