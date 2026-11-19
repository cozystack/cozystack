# linstor-gui

Cozystack system package for [LINBIT/linstor-gui](https://github.com/LINBIT/linstor-gui)
— a web UI for managing LINSTOR nodes, resources, volumes and snapshots.

Installed alongside the `linstor` package in the `cozy-linstor` namespace. The UI
proxies the LINSTOR controller REST API at `https://linstor-controller.cozy-linstor.svc:3371`
using mTLS with the `linstor-client-tls` secret created by the `linstor` package.

## Exposing the UI

### Option 1 — Keycloak-protected Ingress (recommended)

The chart ships an `oauth2-proxy` based gatekeeper plus a `KeycloakClient` CRD
so the UI can be published on `linstor-gui.<root-host>` behind the cluster
Keycloak realm. Access is restricted to members of the
`cozystack-cluster-admin` Keycloak group — the same group that grants
cluster-admin RBAC on the host cluster. Authenticating against the `cozy`
realm alone is not sufficient; users outside that group receive a 403 from
oauth2-proxy before any request reaches the UI or the LINSTOR controller.

To turn it on, add `linstor-gui` to `publishing.exposedServices` in the core
`cozystack` values (same list that controls `dashboard`). OIDC must be
enabled (`authentication.oidc.enabled: true`) — if it is not, the Ingress and
gatekeeper Deployment are deliberately **not** rendered, because the LINSTOR
REST API surface must not be exposed unauthenticated.

Once enabled, the UI is reachable at `https://linstor-gui.<root-host>` and
authentication is delegated to Keycloak via the `linstor-gui` client
(auto-provisioned through the `KeycloakClient` CRD; the client secret is
persisted in the `linstor-gui-client` Secret in `cozy-linstor`).

### Option 2 — Port-forward

If you have not set up Keycloak or want ad-hoc access, use the `ClusterIP`
Service:

```bash
kubectl -n cozy-linstor port-forward svc/linstor-gui 3373:80
```

then open <http://localhost:3373>.

## Parameters

### Image

| Name               | Description                                                | Value                                     |
| ------------------ | ---------------------------------------------------------- | ----------------------------------------- |
| `image.repository` | LINSTOR GUI container image repository                     | `ghcr.io/cozystack/cozystack/linstor-gui` |
| `image.tag`        | LINSTOR GUI container image tag (digest recommended)       | `2.3.0`                                   |

### Deployment

| Name       | Description                     | Value |
| ---------- | ------------------------------- | ----- |
| `replicas` | Number of linstor-gui replicas  | `1`   |

### LINSTOR controller connection

| Name                    | Description                                                                                                                          | Value                                                    |
| ----------------------- | ------------------------------------------------------------------------------------------------------------------------------------ | -------------------------------------------------------- |
| `linstor.endpoint`      | In-cluster URL of the LINSTOR controller REST API (HTTPS, mTLS)                                                                      | `https://linstor-controller.cozy-linstor.svc:3371`       |
| `linstor.clientSecret`  | Kubernetes Secret with `tls.crt`, `tls.key`, `ca.crt` used as the mTLS client certificate against the LINSTOR controller. Created by the `linstor` package. | `linstor-client-tls`                                     |
