# Cluster Autoscaler for Hetzner Cloud

This guide explains how to configure cluster-autoscaler for automatic node scaling in Hetzner Cloud with Talos Linux.

## Prerequisites

- Hetzner Cloud account with API token
- `hcloud` CLI installed
- Existing Talos Kubernetes cluster
- Talos worker machine config

## Step 1: Create Talos Image in Hetzner Cloud

Hetzner doesn't support direct image uploads, so we need to create a snapshot via a temporary server.

### 1.1 Configure hcloud CLI

```bash
export HCLOUD_TOKEN="<your-hetzner-api-token>"
```

### 1.2 Create temporary server in rescue mode

```bash
# Create server (without starting)
hcloud server create \
  --name talos-image-builder \
  --type cpx22 \
  --image ubuntu-24.04 \
  --location fsn1 \
  --ssh-key <your-ssh-key-name> \
  --start-after-create=false

# Enable rescue mode and start
hcloud server enable-rescue --type linux64 --ssh-key <your-ssh-key-name> talos-image-builder
hcloud server poweron talos-image-builder
```

### 1.3 Get server IP and write Talos image

```bash
# Get server IP
SERVER_IP=$(hcloud server ip talos-image-builder)

# SSH into rescue mode and write image
ssh root@$SERVER_IP

# Inside rescue mode:
wget -O- "https://factory.talos.dev/image/<SCHEMATIC_ID>/<VERSION>/hcloud-amd64.raw.xz" \
  | xz -d \
  | dd of=/dev/sda bs=4M status=progress
sync
exit
```

Get your schematic ID from https://factory.talos.dev with required extensions:
- `siderolabs/qemu-guest-agent` (required for Hetzner)
- Other extensions as needed (zfs, drbd, etc.)

### 1.4 Create snapshot and cleanup

```bash
# Power off and create snapshot
hcloud server poweroff talos-image-builder
hcloud server create-image --type snapshot --description "Talos v1.11.6" talos-image-builder

# Get snapshot ID (save this for later)
hcloud image list --type snapshot

# Delete temporary server
hcloud server delete talos-image-builder
```

## Step 2: Create Hetzner vSwitch (Optional but Recommended)

Create a private network for communication between nodes:

```bash
# Create network
hcloud network create --name cozystack-vswitch --ip-range 10.100.0.0/16

# Add subnet for your region (eu-central covers FSN1, NBG1)
hcloud network add-subnet cozystack-vswitch \
  --type cloud \
  --network-zone eu-central \
  --ip-range 10.100.0.0/24
```

## Step 3: Create Talos Machine Config

Create a worker machine config for autoscaled nodes. Important fields:

```yaml
version: v1alpha1
machine:
  type: worker
  token: <worker-token>
  ca:
    crt: <base64-encoded-ca-cert>
  # Node labels (applied automatically on join)
  nodeLabels:
    kilo.squat.ai/location: hetzner-cloud
    topology.kubernetes.io/zone: hetzner-cloud
  kubelet:
    image: ghcr.io/siderolabs/kubelet:v1.33.1
    # Use vSwitch IP as internal IP
    nodeIP:
      validSubnets:
        - 10.100.0.0/24
    # Required for external cloud provider
    extraArgs:
      cloud-provider: external
    extraConfig:
      maxPods: 512
    defaultRuntimeSeccompProfileEnabled: true
    disableManifestsDirectory: true
  # Registry mirrors (recommended to avoid rate limiting)
  registries:
    mirrors:
      docker.io:
        endpoints:
          - https://mirror.gcr.io
cluster:
  controlPlane:
    endpoint: https://<control-plane-ip>:6443
  clusterName: <cluster-name>
  network:
    cni:
      name: none
    podSubnets:
      - 10.244.0.0/16
    serviceSubnets:
      - 10.96.0.0/16
  token: <cluster-token>
  ca:
    crt: <base64-encoded-cluster-ca>
```

> **Important**: Ensure kubelet version matches your cluster version. Talos 1.11.6 doesn't support Kubernetes 1.35+.

## Step 4: Create Kubernetes Secrets

### 4.1 Create secret with Hetzner API token

```bash
kubectl -n cozy-cluster-autoscaler-hetzner create secret generic hetzner-credentials \
  --from-literal=token=<your-hetzner-api-token>
```

### 4.2 Create secret with Talos machine config

The machine config must be base64-encoded:

```bash
# Encode your worker.yaml (single line base64)
base64 -i worker.yaml -o worker.b64

# Create secret
kubectl -n cozy-cluster-autoscaler-hetzner create secret generic talos-config \
  --from-file=cloud-init=worker.b64
```

## Step 5: Deploy Cluster Autoscaler

Create the Package resource:

```yaml
apiVersion: cozystack.io/v1alpha1
kind: Package
metadata:
  name: cozystack.cluster-autoscaler-hetzner
spec:
  variant: default
  components:
    cluster-autoscaler-hetzner:
      values:
        cluster-autoscaler:
          autoscalingGroups:
            - name: workers-fsn1
              minSize: 0
              maxSize: 10
              instanceType: cpx22
              region: FSN1
          extraEnv:
            HCLOUD_IMAGE: "<snapshot-id>"
            HCLOUD_SSH_KEY: "<ssh-key-name>"
            HCLOUD_NETWORK: "cozystack-vswitch"
            HCLOUD_PUBLIC_IPV4: "true"
            HCLOUD_PUBLIC_IPV6: "false"
          extraEnvSecrets:
            HCLOUD_TOKEN:
              name: hetzner-credentials
              key: token
            HCLOUD_CLOUD_INIT:
              name: talos-config
              key: cloud-init
          rbac:
            additionalRules:
              - apiGroups:
                  - coordination.k8s.io
                resources:
                  - leases
                verbs:
                  - create
                  - get
                  - update
```

Apply:
```bash
kubectl apply -f package.yaml
```

## Step 6: Test Autoscaling

Create a deployment with pod anti-affinity to force scale-up:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: test-autoscaler
spec:
  replicas: 5
  selector:
    matchLabels:
      app: test-autoscaler
  template:
    metadata:
      labels:
        app: test-autoscaler
    spec:
      affinity:
        podAntiAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
          - labelSelector:
              matchLabels:
                app: test-autoscaler
            topologyKey: kubernetes.io/hostname
      containers:
      - name: nginx
        image: nginx
        resources:
          requests:
            cpu: "100m"
            memory: "128Mi"
```

If you have fewer nodes than replicas, the autoscaler will create new Hetzner servers.

## Step 7: Verify

```bash
# Check autoscaler logs
kubectl -n cozy-cluster-autoscaler-hetzner logs deployment/cluster-autoscaler-hetzner-hetzner-cluster-autoscaler -f

# Check nodes
kubectl get nodes -o wide

# Verify node labels and internal IP
kubectl get node <node-name> --show-labels
```

Expected result for autoscaled nodes:
- Internal IP from vSwitch range (e.g., 10.100.0.2)
- Label `kilo.squat.ai/location=hetzner-cloud`

## Configuration Reference

### Environment Variables

| Variable | Description | Required |
|----------|-------------|----------|
| `HCLOUD_TOKEN` | Hetzner API token | Yes |
| `HCLOUD_IMAGE` | Talos snapshot ID | Yes |
| `HCLOUD_CLOUD_INIT` | Base64-encoded machine config | Yes |
| `HCLOUD_NETWORK` | vSwitch network name/ID | No |
| `HCLOUD_SSH_KEY` | SSH key name/ID | No |
| `HCLOUD_FIREWALL` | Firewall name/ID | No |
| `HCLOUD_PUBLIC_IPV4` | Assign public IPv4 | No (default: true) |
| `HCLOUD_PUBLIC_IPV6` | Assign public IPv6 | No (default: false) |

### Hetzner Server Types

| Type | vCPU | RAM | Good for |
|------|------|-----|----------|
| cpx22 | 2 | 4GB | Small workloads |
| cpx32 | 4 | 8GB | General purpose |
| cpx42 | 8 | 16GB | Medium workloads |
| cpx52 | 16 | 32GB | Large workloads |
| ccx13 | 2 dedicated | 8GB | CPU-intensive |
| ccx23 | 4 dedicated | 16GB | CPU-intensive |
| ccx33 | 8 dedicated | 32GB | CPU-intensive |
| cax11 | 2 ARM | 4GB | ARM workloads |
| cax21 | 4 ARM | 8GB | ARM workloads |

> **Note**: Some older server types (cpx11, cpx21, etc.) may be unavailable in certain regions.

### Hetzner Regions

| Code | Location |
|------|----------|
| FSN1 | Falkenstein, Germany |
| NBG1 | Nuremberg, Germany |
| HEL1 | Helsinki, Finland |
| ASH | Ashburn, USA |
| HIL | Hillsboro, USA |

## Troubleshooting

### Nodes not joining cluster

1. Check VNC console via Hetzner Cloud Console or:
   ```bash
   hcloud server request-console <server-name>
   ```
2. Common errors:
   - **"unknown keys found during decoding"**: Check Talos config format. `nodeLabels` goes under `machine`, `nodeIP` goes under `machine.kubelet`
   - **"kubelet image is not valid"**: Kubernetes version mismatch. Use kubelet version compatible with your Talos version
   - **"failed to load config"**: Machine config syntax error

### Nodes have wrong Internal IP

Ensure `machine.kubelet.nodeIP.validSubnets` is set to your vSwitch subnet:
```yaml
machine:
  kubelet:
    nodeIP:
      validSubnets:
        - 10.100.0.0/24
```

### Scale-up not triggered

1. Check autoscaler logs for errors
2. Verify RBAC permissions (leases access required)
3. Check if pods are actually pending:
   ```bash
   kubectl get pods --field-selector=status.phase=Pending
   ```

### Registry rate limiting (403 errors)

Add registry mirrors to Talos config:
```yaml
machine:
  registries:
    mirrors:
      docker.io:
        endpoints:
          - https://mirror.gcr.io
      registry.k8s.io:
        endpoints:
          - https://registry.k8s.io
```

### Scale-down not working

Talos caches absent nodes for up to 30 minutes. Wait or restart autoscaler:
```bash
kubectl -n cozy-cluster-autoscaler-hetzner rollout restart deployment cluster-autoscaler-hetzner-hetzner-cluster-autoscaler
```

## Integration with Kilo

For multi-location clusters using Kilo mesh networking, add location label to machine config:

```yaml
machine:
  nodeLabels:
    kilo.squat.ai/location: hetzner-cloud
    topology.kubernetes.io/zone: hetzner-cloud
```

This allows Kilo to create proper WireGuard tunnels between your bare-metal nodes and Hetzner Cloud nodes.
