# linstor-gui

Cozystack system package for [LINBIT/linstor-gui](https://github.com/LINBIT/linstor-gui)
— a web UI for managing LINSTOR nodes, resources, volumes and snapshots.

Installed alongside the `linstor` package in the `cozy-linstor` namespace. The UI
proxies the LINSTOR controller REST API at `https://linstor-controller.cozy-linstor.svc:3371`
using mTLS with the `linstor-client-tls` secret created by the `linstor` package.

## Exposing the UI

This package only creates a `ClusterIP` Service. It does **not** ship an ingress,
because authentication depends on the deployment's Keycloak / OIDC setup and
LINSTOR's controller API is a privileged cluster-wide storage management
surface. Cluster admins should wire up ingress + auth explicitly, for example:

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
