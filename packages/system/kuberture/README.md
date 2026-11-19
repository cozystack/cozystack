# kuberture

Cozystack system package for [kuberture](https://github.com/lexfrei/kuberture) — a controller that translates the `default/kubernetes` EndpointSlice into annotated headless Services, so that `external-dns` (which cannot use EndpointSlice as a source) can publish the Kubernetes API endpoint to DNS.

```text
EndpointSlice (kubernetes) → kuberture → headless Service(s) with annotations → external-dns → DNS
```

## Status

Optional. Disabled by default. Enable by adding `cozystack.kuberture` to `bundles.enabledPackages` in the platform HelmRelease values.

The package ships no usable default beyond enabling ServiceMonitor: kuberture's deployment template fails fast if `config.outputs` is empty. The operator must declare at least one output describing the DNS name(s) they want published. The package itself cannot infer a sensible default hostname; that is intentional — a placeholder default would silently roll out and produce DNS records pointing at the wrong target.

## Targeting an external-dns instance

Each `config.outputs[*].annotationPrefix` selects which external-dns instance picks up that output's headless Service. The prefix is the namespace used for the `hostname`/`target`/`ttl` annotations that the controller writes onto the Service. An external-dns instance configured with a matching annotation prefix consumes those Services; an instance configured with a different prefix ignores them.

`annotationPrefix` rules:

- Omit the field entirely to inherit the controller default `external-dns.alpha.kubernetes.io/` (the prefix the platform-level system external-dns watches by default).
- Set the field to a non-empty string ending in `/` to match a custom-prefixed external-dns instance (e.g. one started with `--annotation-prefix=<your-prefix>/`).
- The empty string `""` is rejected by the chart's values schema — omission is the only zero-prefix path.

The platform-level system external-dns watches services cluster-wide (`namespaced: false`) and is the consumer for kuberture output Services in `cozy-kuberture`. Tenant-scoped external-dns instances (delivered through `cozystack.external-dns-application`) run `namespaced: true` and will not pick up Services outside their tenant namespace.

## Example: single external-dns instance

The simplest case — publish the API endpoint under one DNS name, consumed by the platform external-dns:

```yaml
apiVersion: cozystack.io/v1alpha1
kind: Package
metadata:
  name: cozystack.kuberture
spec:
  variant: default
  components:
    kuberture:
      values:
        kuberture:
          config:
            outputs:
              - name: api
                hostname:
                  - api.k8s.example.com
                serviceName: kuberture-api
                addressSource: endpointslice
                recordTTL: 60
```

`annotationPrefix` is omitted, so the controller uses the default `external-dns.alpha.kubernetes.io/`. `recordTTL` is set explicitly here for symmetry with the multi-instance example below; omit it to inherit the controller default.

## Example: routing to multiple external-dns instances

A single kuberture install can serve any number of external-dns instances by varying `annotationPrefix` per output. Two outputs — one targeting the platform external-dns, one targeting a custom-prefixed instance scoped to internal DNS:

```yaml
apiVersion: cozystack.io/v1alpha1
kind: Package
metadata:
  name: cozystack.kuberture
spec:
  variant: default
  components:
    kuberture:
      values:
        kuberture:
          config:
            outputs:
              - name: public
                hostname:
                  - api.k8s.example.com
                serviceName: kuberture-public
                addressSource: endpointslice
              - name: internal
                hostname:
                  - api-internal.k8s.example.internal
                annotationPrefix: internal-dns.example.com/
                serviceName: kuberture-internal
                addressSource: endpointslice
```

Each output renders its own headless Service in `cozy-kuberture`, carrying only its own prefix's annotations. The platform external-dns (default `--annotation-prefix=external-dns.alpha.kubernetes.io/`) reads only `kuberture-public`. A second external-dns instance started with `--annotation-prefix=internal-dns.example.com/` rebuilds every `hostname`/`target`/`ttl` annotation key under that prefix on startup and therefore reads only `kuberture-internal`. This is the upstream-documented [Split Horizon DNS pattern](https://kubernetes-sigs.github.io/external-dns/v0.20.0/docs/advanced/split-horizon/) — no cross-pollution between outputs because each external-dns ignores annotations under any prefix other than its own.

## Address resolution

`addressSource` picks where each output gets its target IPs:

- `endpointslice` (default in the examples above) — read addresses directly from the `default/kubernetes` EndpointSlice. Use this when the EndpointSlice IPs are the addresses you want in DNS.
- `node-internal` / `node-external` / `node-public` — resolve each EndpointSlice endpoint to a Node and emit the node's `InternalIP` / `ExternalIP` / a public IP. Use these when the EndpointSlice carries internal IPs but external-dns must publish the node's external IP (cloud-hosted control plane behind a LoadBalancer, etc.).

## Dependencies

The package declares no `dependsOn` on external-dns: kuberture creates annotated Services regardless of whether an external-dns instance exists to consume them. External-dns is a downstream reader of those annotations, not a runtime prerequisite. Operators who want DNS records actually published install external-dns separately (`cozystack.external-dns` for the cluster-wide platform instance).

## NetworkPolicy

The chart's `networkPolicy.enabled` defaults to `false`. Cozystack tenant isolation with Cilium does not require a NetworkPolicy on system packages running in dedicated platform namespaces (`cozy-kuberture` here); the controller only reads EndpointSlices and Nodes cluster-wide and writes Services in its own namespace. Operators running stricter zero-trust models can override `kuberture.networkPolicy.enabled: true` in their HelmRelease values to gate the controller's metrics/health ports.

## Vendoring

The chart is pulled from `oci://ghcr.io/lexfrei/kuberture/charts/kuberture` and the controller image from `ghcr.io/lexfrei/kuberture`. Both are in the maintainer's personal namespace and are intentionally not mirrored under `ghcr.io/cozystack/*` — air-gapped operators must mirror the chart and the controller image into their internal registry and override `kuberture.image.repository` accordingly. The chart version and OCI manifest digest are pinned in [`Makefile`](./Makefile); the controller image tag and image digest are pinned in [`values.yaml`](./values.yaml). All four pins advance in lockstep when bumping; see the in-file comments for the bump procedure.
