# Talos Log Collector

A node-local Vector DaemonSet that receives Talos machine and kernel logs and
forwards them to the in-cluster VictoriaLogs.

Talos keeps its system and kernel (kmsg) logs inside the OS and does not write
them to a host file that the default Fluent Bit `tail` input could read, so
those logs are never picked up by `monitoring-agents`. This package runs a
Vector receiver on every node; Talos pushes its logs into it over the node
loopback, and Vector forwards them to the tenant VictoriaLogs
(`vlinsert-generic.<target>.svc`) next to the container, audit and event logs
already collected by `monitoring-agents`.

## How it works

Vector runs as a DaemonSet on the pod network, so cluster DNS resolves the
vlinsert service. It listens on a TCP socket published on the node loopback via
a `hostPort` bound to `hostIP: 127.0.0.1`. Because the pod is not on the host
network, the outbound path to vlinsert keeps working; the inbound loopback
socket is provided by the hostPort. `machine.logging` already forwards the
runtime kernel (kmsg) stream as the `kernel` service (verified on Talos v1.12),
so no separate `KmsgLogConfig` is needed for runtime kernel logs.

## Talos configuration (required)

This package deploys only the receiver. Each Talos node must be told to push
its logs to the loopback socket. Add this to the machine config and apply it:

```yaml
machine:
  logging:
    destinations:
      - endpoint: "tcp://127.0.0.1:5170/"
        format: "json_lines"
```

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

| Name               | Description                                                                              | Type       | Value                                                                                   |
| ------------------ | ---------------------------------------------------------------------------------------- | ---------- | --------------------------------------------------------------------------------------- |
| `logLevel`         | Vector log verbosity (trace, debug, info, warn, error).                                  | `string`   | `warn`                                                                                  |
| `listenPort`       | TCP port bound on the node loopback (127.0.0.1) where Talos machine.logging pushes logs. | `int`      | `5170`                                                                                  |
| `image`            | Vector container image.                                                                  | `object`   | `{}`                                                                                    |
| `image.repository` | Image repository.                                                                        | `string`   | `ghcr.io/cozystack/cozystack/vector`                                                    |
| `image.tag`        | Image tag (digest-pinned by the build).                                                  | `string`   | `0.56.0-alpine@sha256:0eb66216f5f9322264e2ba83f4606428ef77a2cd4dea619a5bea610ac80ccc43` |
| `image.pullPolicy` | Image pull policy.                                                                       | `string`   | `IfNotPresent`                                                                          |
| `resources`        | Compute resources for the collector.                                                     | `object`   | `{}`                                                                                    |
| `resources.cpu`    | CPU request and limit.                                                                   | `quantity` | `100m`                                                                                  |
| `resources.memory` | Memory request and limit.                                                                | `quantity` | `128Mi`                                                                                 |


### VictoriaLogs destination

| Name            | Description                                                                  | Type     | Value         |
| --------------- | ---------------------------------------------------------------------------- | -------- | ------------- |
| `global`        | Platform-injected global values.                                             | `object` | `{}`          |
| `global.target` | Tenant whose VictoriaLogs (vlinsert-generic.<target>.svc) receives the logs. | `string` | `tenant-root` |

