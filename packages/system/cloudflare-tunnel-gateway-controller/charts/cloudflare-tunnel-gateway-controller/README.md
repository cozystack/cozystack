# cloudflare-tunnel-gateway-controller

[![Release](https://img.shields.io/github/v/release/lexfrei/cloudflare-tunnel-gateway-controller)](https://github.com/lexfrei/cloudflare-tunnel-gateway-controller/releases)
[![Artifact Hub](https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/cloudflare-tunnel-gateway-controller)](https://artifacthub.io/packages/search?repo=cloudflare-tunnel-gateway-controller)
[![CI](https://github.com/lexfrei/cloudflare-tunnel-gateway-controller/actions/workflows/pr.yaml/badge.svg)](https://github.com/lexfrei/cloudflare-tunnel-gateway-controller/actions/workflows/pr.yaml)
[![Release](https://github.com/lexfrei/cloudflare-tunnel-gateway-controller/actions/workflows/release.yaml/badge.svg)](https://github.com/lexfrei/cloudflare-tunnel-gateway-controller/actions/workflows/release.yaml)

Kubernetes Gateway API controller for Cloudflare Tunnel

**Homepage:** <https://github.com/lexfrei/cloudflare-tunnel-gateway-controller/>

## Features

- Standard Gateway API implementation (GatewayClass, Gateway, HTTPRoute, GRPCRoute, ListenerSet)
- Hot reload of tunnel configuration (no cloudflared restart required)
- In-process L7 proxy embeds cloudflared transport (single data plane, no separate cloudflared deployment)
- Leader election for high availability deployments
- Multi-arch container images (amd64, arm64)
- Signed container images with cosign

> **Warning:** The controller assumes **exclusive ownership** of the tunnel configuration. It will remove any ingress rules not managed by HTTPRoute resources. Do not use a tunnel that has manually configured routes or is shared with other systems.

## Maintainers

| Name | Email | Url |
| ---- | ------ | --- |
| lexfrei | <f@lex.la> | <https://github.com/lexfrei> |

## Source Code

* <https://github.com/lexfrei/cloudflare-tunnel-gateway-controller/>

## Requirements

Kubernetes: `>=1.25.0-0`

## Prerequisites

- Kubernetes 1.25+
- Helm 3.0+
- Gateway API CRDs installed
- Cloudflare Tunnel created

### Install Gateway API CRDs

```bash
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.6.1/standard-install.yaml
```

### Create Cloudflare Tunnel

Before deploying the controller, create a Cloudflare Tunnel:

1. Go to [Cloudflare Zero Trust Dashboard](https://one.dash.cloudflare.com/)
2. Navigate to **Networks** > **Tunnels**
3. Click **Create a tunnel**
4. Choose **Cloudflared** connector type
5. Name your tunnel and save the **Tunnel ID** and **Tunnel Token**

### Cloudflare API Token Permissions

Create an API token at [Cloudflare API Tokens](https://dash.cloudflare.com/profile/api-tokens) with the following permissions:

| Scope | Permission | Access |
|-------|------------|--------|
| Account | Cloudflare Tunnel | Edit |

Account ID is auto-detected from the API token when not explicitly provided (works if the token has access to a single account).

## Installation

### Install from OCI Registry

First create the credentials and tunnel-token Secrets:

```bash
kubectl create namespace cloudflare-tunnel-system
kubectl create secret generic cloudflare-credentials \
  --namespace cloudflare-tunnel-system \
  --from-literal=api-token="YOUR_API_TOKEN"
kubectl create secret generic cloudflare-tunnel-token \
  --namespace cloudflare-tunnel-system \
  --from-literal=tunnel-token="YOUR_TUNNEL_TOKEN"
```

Then install the chart:

```bash
helm install cloudflare-tunnel-gateway-controller \
  oci://ghcr.io/lexfrei/charts/cloudflare-tunnel-gateway-controller \
  --version 1.0.0 \
  --namespace cloudflare-tunnel-system \
  --set gatewayClassConfig.create=true \
  --set gatewayClassConfig.tunnelID="YOUR_TUNNEL_ID" \
  --set gatewayClassConfig.cloudflareCredentialsSecretRef.name=cloudflare-credentials \
  --set proxy.tunnelTokenSecretRef.name=cloudflare-tunnel-token
```

### Verify Chart Signature

Charts are signed with cosign. Verify before installing:

```bash
cosign verify ghcr.io/lexfrei/cloudflare-tunnel-gateway-controller:1.0.0 \
  --certificate-identity-regexp="https://github.com/lexfrei/cloudflare-tunnel-gateway-controller" \
  --certificate-oidc-issuer="https://token.actions.githubusercontent.com"
```

## Configuration Examples

Complete example values files are available in the [examples/](https://github.com/lexfrei/cloudflare-tunnel-gateway-controller/tree/master/charts/cloudflare-tunnel-gateway-controller/examples) directory:

| Example | Description |
|---------|-------------|
| [basic-values.yaml](examples/basic-values.yaml) | Minimal configuration |
| [external-secrets-values.yaml](examples/external-secrets-values.yaml) | Using existing Kubernetes Secret |
| [production-values.yaml](examples/production-values.yaml) | Production-ready with HA, monitoring, and security |

## Documentation

Full documentation lives at <https://cf.k8s.lex.la> (built from the `docs/` tree).

## External Resources

- [Gateway API](https://gateway-api.sigs.k8s.io/) - Kubernetes Gateway API specification
- [Cloudflare Tunnel](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/) - Cloudflare Tunnel documentation
- [Cloudflare API Tokens](https://dash.cloudflare.com/profile/api-tokens) - Create API tokens

### Quick Start (values.yaml)

```yaml
gatewayClassConfig:
  create: true
  tunnelID: "550e8400-e29b-41d4-a716-446655440000"
  cloudflareCredentialsSecretRef:
    name: cloudflare-credentials  # Secret with key "api-token"

proxy:
  tunnelTokenSecretRef:
    name: cloudflare-tunnel-token  # Secret with key "tunnel-token"
```

## Usage

After installation, create Gateway and HTTPRoute resources:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: cloudflare-gateway
spec:
  gatewayClassName: cloudflare-tunnel
  listeners:
    - name: https
      protocol: HTTPS
      port: 443
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: my-app
spec:
  parentRefs:
    - name: cloudflare-gateway
  hostnames:
    - "app.example.com"
  rules:
    - backendRefs:
        - name: my-app-service
          port: 80
```

**Important:** On startup, the controller performs a full synchronization. All existing ingress rules in the tunnel are replaced with rules derived from current HTTPRoutes. Any rules created outside of this controller will be removed.

## Multi-Tunnel Setup

To use multiple Cloudflare Tunnels in the same cluster, deploy multiple instances of the controller with different `controllerName` values. Each controller binds to GatewayClasses by `spec.controllerName`, not by class name:

```bash
# First tunnel for production apps
# gatewayClassName = name of the GatewayClass K8s resource created by this chart (cosmetic, no filtering effect)
# controllerName = spec.controllerName on the GatewayClass; this is what binds it to this controller instance
helm install controller-prod \
  oci://ghcr.io/lexfrei/charts/cloudflare-tunnel-gateway-controller \
  --namespace cloudflare-system \
  --set controller.gatewayClassName=cloudflare-tunnel-prod \
  --set controller.controllerName=cf.k8s.lex.la/tunnel-prod \
  --set gatewayClassConfig.create=true \
  --set gatewayClassConfig.tunnelID="PROD_TUNNEL_ID" \
  --set gatewayClassConfig.cloudflareCredentialsSecretRef.name=cloudflare-credentials-prod \
  --set proxy.tunnelTokenSecretRef.name=cloudflare-tunnel-token-prod

# Second tunnel for staging apps
helm install controller-staging \
  oci://ghcr.io/lexfrei/charts/cloudflare-tunnel-gateway-controller \
  --namespace cloudflare-system \
  --set controller.gatewayClassName=cloudflare-tunnel-staging \
  --set controller.controllerName=cf.k8s.lex.la/tunnel-staging \
  --set gatewayClassConfig.create=true \
  --set gatewayClassConfig.tunnelID="STAGING_TUNNEL_ID" \
  --set gatewayClassConfig.cloudflareCredentialsSecretRef.name=cloudflare-credentials-staging \
  --set proxy.tunnelTokenSecretRef.name=cloudflare-tunnel-token-staging
```

Each controller instance discovers GatewayClasses by `spec.controllerName` (not by resource name) and manages their associated Gateways and routes independently.

## External-DNS Integration

The controller integrates with [external-dns](https://github.com/kubernetes-sigs/external-dns) for automatic DNS record creation.

The controller automatically sets `status.addresses` on the Gateway with the tunnel CNAME (`TUNNEL_ID.cfargotunnel.com`). External-dns reads this value as the DNS target.

Add provider-specific annotations on HTTPRoute:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: my-app
  annotations:
    external-dns.alpha.kubernetes.io/cloudflare-proxied: "true"
spec:
  parentRefs:
    - name: cloudflare-tunnel
      namespace: cloudflare-tunnel-system
      sectionName: http  # Must match listener name
  hostnames:
    - app.example.com
```

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| affinity | object | `{}` | Affinity rules for pod scheduling |
| controller | object | `{"clusterDomain":"","controllerName":"cf.k8s.lex.la/tunnel-controller","gatewayClassName":"cloudflare-tunnel","logFormat":"json","logLevel":"info","tracing":{"enabled":false,"endpoint":"","sampleRate":1}}` | Controller configuration |
| controller.clusterDomain | string | auto-detected from /etc/resolv.conf, fallback: cluster.local | Kubernetes cluster domain for service DNS resolution |
| controller.controllerName | string | `"cf.k8s.lex.la/tunnel-controller"` | Value for GatewayClass spec.controllerName — this is how the controller discovers its GatewayClasses (must be unique per controller instance) |
| controller.gatewayClassName | string | `"cloudflare-tunnel"` | Name of the GatewayClass resource to create |
| controller.logFormat | string | `"json"` | Log format (json, text) |
| controller.logLevel | string | `"info"` | Log level (debug, info, warn, error) |
| controller.tracing | object | `{"enabled":false,"endpoint":"","sampleRate":1}` | Distributed tracing (OpenTelemetry) for the controller. Off by default. When enabled, the controller instruments its outbound Cloudflare API and proxy config-push clients and exports spans over OTLP/gRPC. This is independent of proxy.tracing (the data-plane knob). |
| controller.tracing.enabled | bool | `false` | Enable distributed tracing on the controller. Defaults to false. |
| controller.tracing.endpoint | string | `""` | OTLP/gRPC collector endpoint. A bare host:port uses plaintext gRPC; prefix with http:// or https:// to choose plaintext vs TLS. Empty defers to the standard OTEL_EXPORTER_OTLP_ENDPOINT / OTEL_EXPORTER_OTLP_TRACES_ENDPOINT environment variables. |
| controller.tracing.sampleRate | float | `1` | Head-sampling probability in [0, 1], applied at the trace root via ParentBased(TraceIDRatioBased). 1.0 samples every trace. |
| dnsConfig | object | `{}` | Custom DNS configuration for pod Example for custom DNS servers:   nameservers:     - 1.1.1.1     - 8.8.8.8   searches:     - cloudflare-tunnel-system.svc.cluster.local     - svc.cluster.local     - cluster.local   options:     - name: ndots       value: "2" |
| dnsPolicy | string | `""` | DNS policy for pod (ClusterFirst, Default, ClusterFirstWithHostNet, None) Use "None" with dnsConfig for custom DNS configuration |
| fullnameOverride | string | `""` | Override the full release name |
| gatewayClass | object | `{"create":true}` | GatewayClass configuration |
| gatewayClass.create | bool | `true` | Create GatewayClass resource |
| gatewayClassConfig | object | `{"accountId":"","cloudflareCredentialsSecretRef":{"key":"","name":"","namespace":""},"create":false,"name":"","tunnelID":""}` | GatewayClassConfig configuration This section drives the optional GatewayClassConfig CRD rendered by the chart. The in-process L7 proxy embeds cloudflared transport and is deployed by the chart itself; the tunnel token is supplied directly to the proxy via `proxy.tunnelTokenSecretRef` (see below). |
| gatewayClassConfig.accountId | string | `""` | Cloudflare account ID. Optional - auto-detected when the API token has access to a single account. |
| gatewayClassConfig.cloudflareCredentialsSecretRef | object | `{"key":"","name":"","namespace":""}` | Reference to Secret containing Cloudflare API credentials (REQUIRED) The Secret must contain an "api-token" key with a valid Cloudflare API token. Optionally, it can contain an "account-id" key; if not present, account ID is auto-detected. |
| gatewayClassConfig.cloudflareCredentialsSecretRef.key | string | `""` | Key in the Secret containing the API token (defaults to "api-token") |
| gatewayClassConfig.cloudflareCredentialsSecretRef.name | string | `""` | Name of the Secret containing API credentials |
| gatewayClassConfig.cloudflareCredentialsSecretRef.namespace | string | `""` | Namespace of the Secret (defaults to release namespace) |
| gatewayClassConfig.create | bool | `false` | Create GatewayClassConfig resource |
| gatewayClassConfig.name | string | `""` | Name of the GatewayClassConfig (defaults to release fullname) |
| gatewayClassConfig.tunnelID | string | `""` | Cloudflare Tunnel ID (REQUIRED) Get from: Zero Trust Dashboard > Networks > Tunnels Example: "550e8400-e29b-41d4-a716-446655440000" |
| healthProbes | object | `{"livenessProbe":{"enabled":true,"failureThreshold":3,"initialDelaySeconds":15,"periodSeconds":20,"timeoutSeconds":5},"readinessProbe":{"enabled":true,"failureThreshold":3,"initialDelaySeconds":5,"periodSeconds":10,"timeoutSeconds":3},"startupProbe":{"enabled":true,"failureThreshold":12,"initialDelaySeconds":0,"periodSeconds":5,"timeoutSeconds":3}}` | Health probes configuration |
| healthProbes.livenessProbe | object | `{"enabled":true,"failureThreshold":3,"initialDelaySeconds":15,"periodSeconds":20,"timeoutSeconds":5}` | Liveness probe configuration Restarts container if probe fails |
| healthProbes.readinessProbe | object | `{"enabled":true,"failureThreshold":3,"initialDelaySeconds":5,"periodSeconds":10,"timeoutSeconds":3}` | Readiness probe configuration Removes pod from service endpoints if probe fails |
| healthProbes.startupProbe | object | `{"enabled":true,"failureThreshold":12,"initialDelaySeconds":0,"periodSeconds":5,"timeoutSeconds":3}` | Startup probe configuration Gives controller time to initialize before liveness probe starts |
| hostnameOwnershipPolicy | object | `{"admissionPolicy":true,"enabled":false,"labelKey":"cf.k8s.lex.la/hostname-suffix","namespaceSelector":{}}` | Per-namespace hostname-ownership policy (multi-tenant isolation). When enabled, a namespace is bound to ONE allowed hostname suffix via the labelKey namespace label, and routes claiming hostnames outside it are blocked TWICE (defence in depth): fail-fast by a ValidatingAdmissionPolicy (Kubernetes 1.30+) and authoritatively by the controller at binding time (never programmed into the proxy or the Cloudflare ingress document). Fail-closed inside the policed scope: unlabelled policed namespaces and routes without explicit hostnames are rejected. Label values cannot contain '*' or ',' — one lowercase suffix per namespace. |
| hostnameOwnershipPolicy.admissionPolicy | bool | `true` | Render the ValidatingAdmissionPolicy (requires Kubernetes 1.30+). Set false on older clusters to keep only the controller-side layer. |
| hostnameOwnershipPolicy.enabled | bool | `false` | Master switch: installs the admission policy AND enables the controller-side enforcement flags. |
| hostnameOwnershipPolicy.labelKey | string | `"cf.k8s.lex.la/hostname-suffix"` | Namespace label carrying the tenant's allowed hostname suffix (lowercase, e.g. "team-a.example.com"). |
| hostnameOwnershipPolicy.namespaceSelector | object | `{}` | LabelSelector scoping which namespaces are policed. Applied to BOTH layers (the admission binding's namespaceSelector and the controller flag, derived from this same value). Empty polices EVERY namespace — fail-closed everywhere; scope it to tenant namespaces, e.g. matchExpressions excluding kube-system and the controller namespace. NOTE: the admission layer matches ALL HTTPRoute/GRPCRoute writes in policed namespaces, including routes for OTHER Gateway implementations (admission cannot resolve parentRefs); the controller layer polices only routes binding to this controller's Gateways. On multi-implementation clusters scope the selector accordingly or set admissionPolicy: false. |
| image | object | `{"pullPolicy":"IfNotPresent","repository":"ghcr.io/lexfrei/cloudflare-tunnel-gateway-controller","tag":""}` | Container image configuration |
| image.pullPolicy | string | `"IfNotPresent"` | Image pull policy |
| image.repository | string | `"ghcr.io/lexfrei/cloudflare-tunnel-gateway-controller"` | Image repository |
| image.tag | string | `""` | Image tag (defaults to appVersion) |
| imagePullSecrets | list | `[]` | Image pull secrets for private registries |
| leaderElection | object | `{"enabled":false,"leaseName":"cloudflare-tunnel-gateway-controller-leader","namespace":""}` | Leader election configuration for high availability |
| leaderElection.enabled | bool | `false` | Enable leader election (required for running multiple replicas) |
| leaderElection.leaseName | string | `"cloudflare-tunnel-gateway-controller-leader"` | Name of the leader election lease |
| leaderElection.namespace | string | `""` | Namespace for leader election lease (defaults to release namespace) |
| nameOverride | string | `""` | Override the chart name |
| networkPolicy | object | `{"cloudflareIpRanges":{"ipv4":["173.245.48.0/20","103.21.244.0/22","103.22.200.0/22","103.31.4.0/22","141.101.64.0/18","108.162.192.0/18","190.93.240.0/20","188.114.96.0/20","197.234.240.0/22","198.41.128.0/17","162.158.0.0/15","104.16.0.0/13","104.24.0.0/14","172.64.0.0/13","131.0.72.0/22"],"ipv6":["2606:4700::/32","2803:f800::/32","2405:b500::/32","2405:8100::/32","2a06:98c0::/29","2c0f:f248::/32"]},"enabled":false,"ingress":{"from":[]}}` | NetworkPolicy configuration |
| networkPolicy.cloudflareIpRanges | object | `{"ipv4":["173.245.48.0/20","103.21.244.0/22","103.22.200.0/22","103.31.4.0/22","141.101.64.0/18","108.162.192.0/18","190.93.240.0/20","188.114.96.0/20","197.234.240.0/22","198.41.128.0/17","162.158.0.0/15","104.16.0.0/13","104.24.0.0/14","172.64.0.0/13","131.0.72.0/22"],"ipv6":["2606:4700::/32","2803:f800::/32","2405:b500::/32","2405:8100::/32","2a06:98c0::/29","2c0f:f248::/32"]}` | Cloudflare IP ranges for egress NetworkPolicy Source: https://www.cloudflare.com/ips/ Last updated: 2025-11-25 To update: curl https://www.cloudflare.com/ips-v4 && curl https://www.cloudflare.com/ips-v6 |
| networkPolicy.enabled | bool | `false` | Enable NetworkPolicy for controller pods |
| networkPolicy.ingress | object | `{"from":[]}` | Ingress source configuration |
| networkPolicy.ingress.from | list | `[]` | Allow ingress from specific namespaces/pods Example for monitoring namespace:   from:     - namespaceSelector:         matchLabels:           name: monitoring     - podSelector:         matchLabels:           app: prometheus |
| nodeSelector | object | `{}` | Node selector for pod scheduling |
| podAnnotations | object | `{}` | Annotations to add to pods |
| podDisruptionBudget | object | `{"enabled":false,"maxUnavailable":null,"minAvailable":1,"unhealthyPodEvictionPolicy":"IfHealthyBudget"}` | PodDisruptionBudget configuration for high availability |
| podDisruptionBudget.enabled | bool | `false` | Enable PodDisruptionBudget |
| podDisruptionBudget.maxUnavailable | string | `nil` | Maximum number of unavailable pods during disruptions Must not be used together with minAvailable |
| podDisruptionBudget.minAvailable | int | `1` | Minimum number of available pods during disruptions Must not be used together with maxUnavailable |
| podDisruptionBudget.unhealthyPodEvictionPolicy | string | `"IfHealthyBudget"` | Policy for evicting unhealthy pods (IfHealthyBudget, AlwaysAllow) Requires Kubernetes 1.26+ |
| podLabels | object | `{}` | Additional labels to add to pods |
| podSecurityContext | object | See values.yaml | Pod security context (secure defaults) |
| priorityClassName | string | `""` | Priority class name for pod scheduling priority |
| proxy | object | `{"accessLog":{"enabled":false,"samplingRate":1,"stripQuery":false},"affinity":{},"authTokenSecretRef":{"key":"auth-token","name":""},"configAPIPort":8081,"gracePeriodSeconds":30,"healthProbes":{"livenessProbe":{"enabled":true,"failureThreshold":3,"initialDelaySeconds":15,"periodSeconds":20,"timeoutSeconds":5},"readinessProbe":{"enabled":true,"failureThreshold":3,"initialDelaySeconds":5,"periodSeconds":10,"timeoutSeconds":3},"startupProbe":{"enabled":true,"failureThreshold":30,"initialDelaySeconds":0,"periodSeconds":5,"timeoutSeconds":3}},"image":{"pullPolicy":"IfNotPresent","repository":"ghcr.io/lexfrei/cloudflare-tunnel-gateway-controller-proxy","tag":""},"metrics":{"enabled":true},"networkPolicy":{"egressRestricted":false,"enabled":true,"ingress":{"from":[]},"monitoringNamespaceSelector":{}},"nodeSelector":{},"podAnnotations":{},"podLabels":{},"podSecurityContext":{"runAsNonRoot":true,"runAsUser":65534,"seccompProfile":{"type":"RuntimeDefault"}},"proxyPort":8080,"replicas":2,"resources":{"limits":{"cpu":"500m","memory":"512Mi"},"requests":{"cpu":"100m","memory":"128Mi"}},"securityContext":{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]},"readOnlyRootFilesystem":true},"service":{"annotations":{}},"tolerations":[],"topologySpreadConstraints":[],"tracing":{"enabled":false,"endpoint":"","sampleRate":1},"tunnel":{"protocol":"auto"},"tunnelTokenSecretRef":{"key":"tunnel-token","name":""},"websocket":{"dialTimeout":"","handshakeTimeout":""}}` | L7 Proxy configuration (enhanced-cloudflared data plane). The L7 proxy is the only data plane; the chart always renders the proxy Deployment + Service + (optional) NetworkPolicy and ServiceMonitor. `proxy.tunnelTokenSecretRef` is required. |
| proxy.accessLog | object | `{"enabled":false,"samplingRate":1,"stripQuery":false}` | Structured per-request access logging on the in-process proxy. Off by default (zero allocation on the hot path). When enabled, emits one JSON line per request via the proxy's stdout slog (method, host, path, query, status, bytes_written, duration_ms, user_agent). Cluster logging stack scrapes stdout already, so no additional sink wiring is needed. |
| proxy.accessLog.enabled | bool | `false` | Enable per-request access logging. Defaults to false; setting it true on a high-traffic gateway will materially increase log volume -- pair with samplingRate < 1.0. |
| proxy.accessLog.samplingRate | float | `1` | Fraction of non-5xx requests to log when enabled, in [0, 1]. 1.0 logs everything; 0.0 logs only 5xx (server-side failures are always recorded regardless of rate so the operator never loses error signal). The proxy clamps out-of-range values: a typo like `samplingRate: 50` degrades to "always log" rather than "silently log nothing". |
| proxy.accessLog.stripQuery | bool | `false` | Zero the `query` field in every emitted line. For applications that ride tokens, signed-URL credentials, or PII in query parameters (?token=..., ?sid=..., ?email=...). The `path` field is unaffected -- operators still see WHICH endpoint was hit, just not WHICH parameters carried sensitive values. Defaults to false (verbatim query for triage signal); turn on when the application surface makes query-string privacy a real concern and route-level (headers vs query) / sink-level (log pipeline scrubbing) mitigations aren't viable. |
| proxy.affinity | object | `{}` | Affinity rules for pod scheduling |
| proxy.authTokenSecretRef | object | `{"key":"auth-token","name":""}` | Reference to Secret containing the config API Bearer token. The config API is ALWAYS authenticated -- when name is empty (the default) the CONTROLLER (not chart templating) generates a random token into a Secret named "<fullname>-proxy-auth-token" (<fullname> is the Helm release fullname, typically "<release>-cloudflare-tunnel-gateway-controller", or just "<release>" when the release name already contains the chart name) as one of its first startup actions and reuses it on every subsequent start, so the token never rotates and upgrading never rolls the proxy on its own. Generating it via a live API call rather than at template time means this also works correctly under GitOps controllers that render client-side with no cluster access (e.g. ArgoCD's default `helm template`), where a template-time `lookup` cannot see prior state. Set name to bring your own Secret/token instead, e.g. for external rotation. |
| proxy.authTokenSecretRef.key | string | `"auth-token"` | Key in the Secret containing the auth token |
| proxy.authTokenSecretRef.name | string | `""` | Name of the Secret containing the auth token. Leave empty to let the controller generate and manage the token automatically (recommended). |
| proxy.configAPIPort | int | `8081` | Config API port (controller pushes config here) |
| proxy.gracePeriodSeconds | int | `30` | Connector drain window in seconds (the proxy's PROXY_GRACE_PERIOD). On SIGTERM the proxy unregisters its tunnel connectors from the Cloudflare edge (stopping new requests) and gives in-flight requests this long to finish before exiting. This MUST stay below the pod's terminationGracePeriodSeconds or kubelet SIGKILLs mid-drain; the chart guarantees that by deriving terminationGracePeriodSeconds as this value plus 15s of headroom — do not override the pod field independently. Capped at 180 (3m) by cloudflared. |
| proxy.healthProbes | object | `{"livenessProbe":{"enabled":true,"failureThreshold":3,"initialDelaySeconds":15,"periodSeconds":20,"timeoutSeconds":5},"readinessProbe":{"enabled":true,"failureThreshold":3,"initialDelaySeconds":5,"periodSeconds":10,"timeoutSeconds":3},"startupProbe":{"enabled":true,"failureThreshold":30,"initialDelaySeconds":0,"periodSeconds":5,"timeoutSeconds":3}}` | Health probes configuration |
| proxy.healthProbes.livenessProbe | object | `{"enabled":true,"failureThreshold":3,"initialDelaySeconds":15,"periodSeconds":20,"timeoutSeconds":5}` | Liveness probe |
| proxy.healthProbes.readinessProbe | object | `{"enabled":true,"failureThreshold":3,"initialDelaySeconds":5,"periodSeconds":10,"timeoutSeconds":3}` | Readiness probe (ready when config is loaded and, in tunnel mode, the tunnel has connected to the edge) |
| proxy.healthProbes.startupProbe | object | `{"enabled":true,"failureThreshold":30,"initialDelaySeconds":0,"periodSeconds":5,"timeoutSeconds":3}` | Startup probe (hits /healthz; gives the process time to start serving — waiting for the tunnel is the readiness probe's job) |
| proxy.image | object | `{"pullPolicy":"IfNotPresent","repository":"ghcr.io/lexfrei/cloudflare-tunnel-gateway-controller-proxy","tag":""}` | Proxy container image |
| proxy.image.pullPolicy | string | `"IfNotPresent"` | Image pull policy |
| proxy.image.repository | string | `"ghcr.io/lexfrei/cloudflare-tunnel-gateway-controller-proxy"` | Proxy image repository |
| proxy.image.tag | string | `""` | Image tag (defaults to appVersion) |
| proxy.metrics | object | `{"enabled":true}` | Data-plane Prometheus metrics, served at /metrics on the config API port (no auth — the endpoint carries no secrets and the port is cluster-internal). The endpoint also exposes the embedded cloudflared connector metrics (cloudflared_tunnel_*). |
| proxy.metrics.enabled | bool | `true` | Expose request-level metrics (in-flight, duration, status classes, bytes, backend errors) on the config API port. Disabling this also suppresses the proxy ServiceMonitor (its template is gated on BOTH toggles) — a scrape against a metrics-less proxy would 404. |
| proxy.networkPolicy | object | `{"egressRestricted":false,"enabled":true,"ingress":{"from":[]},"monitoringNamespaceSelector":{}}` | NetworkPolicy configuration for proxy pods. On by default: it locks the config-API port (which can hijack routing via PUT /config) to the controller namespace. No-op where the CNI does not enforce NetworkPolicy. |
| proxy.networkPolicy.egressRestricted | bool | `false` | Also restrict EGRESS to DNS + the Cloudflare edge + cluster services. Off by default: the static Cloudflare CIDR list can drift and break the tunnel. Enable only if you keep cloudflareIpRanges current. |
| proxy.networkPolicy.enabled | bool | `true` | Render the proxy NetworkPolicy (ingress-only; locks the config-API port to the controller namespace by default) |
| proxy.networkPolicy.ingress | object | `{"from":[]}` | Ingress source configuration |
| proxy.networkPolicy.ingress.from | list | `[]` | EXTRA namespaces/pods allowed to reach the config-API port, ADDED to the release (controller) namespace — which is always admitted so a config push is never locked out. Add your monitoring namespace here to let Prometheus scrape /metrics (locking the config-API port also restricts the /metrics it co-serves). |
| proxy.networkPolicy.monitoringNamespaceSelector | object | `{}` | Label selector for namespaces additionally allowed to reach the config-API/metrics port in the PER-GATEWAY (tenant) NetworkPolicies the controller renders (passed to --monitoring-namespace-selector). Empty = controller namespace only. For the SHARED proxy's policy, use `ingress.from` above instead. |
| proxy.nodeSelector | object | `{}` | Node selector for pod scheduling |
| proxy.podAnnotations | object | `{}` | Annotations to add to proxy pods |
| proxy.podLabels | object | `{}` | Additional labels to add to proxy pods |
| proxy.podSecurityContext | object | See values.yaml | Pod security context |
| proxy.proxyPort | int | `8080` | Proxy port (internal, traffic arrives through tunnel) |
| proxy.replicas | int | `2` | Number of proxy replicas |
| proxy.resources | object | `{"limits":{"cpu":"500m","memory":"512Mi"},"requests":{"cpu":"100m","memory":"128Mi"}}` | Container resource requests and limits |
| proxy.securityContext | object | See values.yaml | Container security context |
| proxy.service | object | `{"annotations":{}}` | Service configuration for proxy metrics/health |
| proxy.service.annotations | object | `{}` | Service annotations |
| proxy.tolerations | list | `[]` | Tolerations for pod scheduling |
| proxy.topologySpreadConstraints | list | `[]` | Topology spread constraints for pod distribution |
| proxy.tracing | object | `{"enabled":false,"endpoint":"","sampleRate":1}` | Distributed tracing (OpenTelemetry) on the in-process proxy. Off by default (zero cost on the hot path). When enabled, each request gets a server span, backend calls get a linked client span, and W3C traceparent / tracestate are propagated to backends. Spans export over OTLP/gRPC; the existing log enrichment then surfaces trace_id / span_id on structured log lines. |
| proxy.tracing.enabled | bool | `false` | Enable distributed tracing on the proxy. Defaults to false. |
| proxy.tracing.endpoint | string | `""` | OTLP/gRPC collector endpoint. A bare host:port (e.g. "otel-collector.observability:4317") uses plaintext gRPC; prefix with http:// or https:// to choose plaintext vs TLS explicitly. Empty defers to the standard OTEL_EXPORTER_OTLP_ENDPOINT / OTEL_EXPORTER_OTLP_TRACES_ENDPOINT environment variables. |
| proxy.tracing.sampleRate | float | `1` | Head-sampling probability in [0, 1], applied at the trace root via ParentBased(TraceIDRatioBased). 1.0 samples every trace; 0.1 samples a tenth. An upstream sampling decision is respected, so partial traces stay intact when the proxy sits behind the edge. |
| proxy.tunnel | object | `{"protocol":"auto"}` | Tunnel transport settings. |
| proxy.tunnel.protocol | string | `"auto"` | Edge transport protocol: "auto" (QUIC with HTTP/2 fallback), "http2", or "quic". gRPC needs http2 (cloudflared drops HTTP trailers over QUIC, so grpc-status is lost). With "auto" (or unset) the proxy upgrades to http2 at startup when a GRPCRoute is present, so gRPC works without changing this; a GRPCRoute added after startup needs a proxy restart. An explicit "quic" is never upgraded and cannot serve gRPC. Tradeoff: "auto" waits for the controller's first config push before dialing (bounded ~30s), so on a route-less cluster a proxy can take up to ~30s to establish its tunnel on each start; pin "http2" or "quic" to dial immediately. |
| proxy.tunnelTokenSecretRef | object | `{"key":"tunnel-token","name":""}` | Reference to Secret containing tunnel token (REQUIRED) |
| proxy.tunnelTokenSecretRef.key | string | `"tunnel-token"` | Key in the Secret containing the tunnel token |
| proxy.tunnelTokenSecretRef.name | string | `""` | Name of the Secret containing tunnel token |
| proxy.websocket | object | `{"dialTimeout":"","handshakeTimeout":""}` | WebSocket upgrade-path knobs. Both bound only the pre-upgrade phase: dialTimeout caps the TCP/TLS connect to the backend; handshakeTimeout caps the wait for the backend's 101 Switching Protocols response. Once the upgrade completes, neither bounds the long-lived post-101 bidirectional stream. Empty (the default) leaves both at the proxy binary's built-in 30s. |
| proxy.websocket.dialTimeout | string | `""` | Backend TCP/TLS dial timeout (Go duration, e.g. "10s", "1m"). Empty preserves the 30s default. Use a deliberately large value to effectively disable the bound rather than setting "0". |
| proxy.websocket.handshakeTimeout | string | `""` | 101-Switching-Protocols read deadline (Go duration). Empty preserves the 30s default. |
| replicaCount | int | `1` | Number of controller replicas |
| resources | object | See values.yaml for recommended production defaults | Container resource requests and limits When resources is empty ({}), the chart will use recommended defaults. Specify explicit values to override defaults. |
| ruleNameUniquenessPolicy | object | `{"enabled":false}` | Optional ValidatingAdmissionPolicy that rejects HTTPRoutes/GRPCRoutes whose rules carry duplicate `name` values, enforcing the Gateway API uniqueness MUST at admission (the Standard-channel CRDs omit the experimental CEL that does this). Disabled by default because it is cluster-scoped and a deliberate operator choice: the policy matches ALL HTTPRoutes/GRPCRoutes in the cluster (a Route carries no controllerName at admission, so it cannot be limited to this controller's routes) and can block updates to routes that already have duplicate rule names. Requires a cluster with admissionregistration.k8s.io/v1 ValidatingAdmissionPolicy (Kubernetes >= 1.30). |
| ruleNameUniquenessPolicy.enabled | bool | `false` | Install the rule-name uniqueness ValidatingAdmissionPolicy and its binding |
| securityContext | object | See values.yaml | Container security context (secure defaults) |
| service | object | `{"annotations":{},"healthPort":8081,"metricsPort":8080,"type":"ClusterIP"}` | Service configuration |
| service.annotations | object | `{}` | Service annotations Example: Prometheus scraping annotations   prometheus.io/scrape: "true"   prometheus.io/port: "8080"   prometheus.io/path: "/metrics" |
| service.healthPort | int | `8081` | Health check endpoint port |
| service.metricsPort | int | `8080` | Metrics endpoint port |
| service.type | string | `"ClusterIP"` | Service type |
| serviceAccount | object | `{"annotations":{},"name":""}` | Service account configuration |
| serviceAccount.annotations | object | `{}` | Annotations to add to the service account |
| serviceAccount.name | string | `""` | The name of the service account to use If empty, uses the fullname template (release-name-chart-name) Override this if you want to use a pre-existing service account |
| serviceMonitor | object | `{"enabled":false,"interval":"","labels":{}}` | ServiceMonitor configuration for Prometheus Operator |
| serviceMonitor.enabled | bool | `false` | Enable ServiceMonitor creation |
| serviceMonitor.interval | string | `""` | Scrape interval (uses Prometheus default if empty) |
| serviceMonitor.labels | object | `{}` | Additional labels for ServiceMonitor (for Prometheus selector) |
| terminationGracePeriodSeconds | int | `30` | Termination grace period in seconds for graceful shutdown |
| tolerations | list | `[]` | Tolerations for pod scheduling |
| topologySpreadConstraints | list | `[]` | Topology spread constraints for pod distribution |

----------------------------------------------
Autogenerated from chart metadata using [helm-docs v1.14.2](https://github.com/norwoodj/helm-docs/releases/v1.14.2)
