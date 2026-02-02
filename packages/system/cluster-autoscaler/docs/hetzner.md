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

## Step 2: Create Kubernetes Secrets

### 2.1 Create namespace (if not exists)

```bash
kubectl create namespace cozy-cluster-autoscaler-hetzner
```

### 2.2 Create secret with Hetzner API token

```bash
kubectl -n cozy-cluster-autoscaler-hetzner create secret generic hetzner-credentials \
  --from-literal=token=<your-hetzner-api-token>
```

### 2.3 Create secret with Talos machine config

The machine config must be base64-encoded for the `HCLOUD_CLOUD_INIT` environment variable.

```bash
# Encode your worker.yaml
cat worker.yaml | base64 -w0 > worker.b64

# Create secret
kubectl -n cozy-cluster-autoscaler-hetzner create secret generic talos-config \
  --from-file=cloud-init=worker.b64
```

## Step 3: Configure Cluster Autoscaler

Create or update your values file for the cluster-autoscaler-hetzner package:

```yaml
cluster-autoscaler:
  cloudProvider: hetzner

  autoscalingGroups:
    - name: workers-fsn1
      minSize: 0
      maxSize: 10
      instanceType: CPX21    # 3 vCPU, 4GB RAM
      region: FSN1

  extraEnv:
    HCLOUD_IMAGE: "<snapshot-id>"
    HCLOUD_NETWORK: "<network-name-or-id>"      # Optional: private network
    HCLOUD_FIREWALL: "<firewall-name-or-id>"    # Optional: firewall
    HCLOUD_SSH_KEY: "<ssh-key-name-or-id>"      # Optional: SSH key
    HCLOUD_PUBLIC_IPV4: "true"
    HCLOUD_PUBLIC_IPV6: "false"

  extraEnvSecrets:
    HCLOUD_TOKEN:
      name: hetzner-credentials
      key: token
    HCLOUD_CLOUD_INIT:
      name: talos-config
      key: cloud-init
```

## Step 4: Deploy

### Via Cozystack Package

Update the Package resource with your configuration:

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
            HCLOUD_PUBLIC_IPV4: "true"
            HCLOUD_PUBLIC_IPV6: "false"
          extraEnvSecrets:
            HCLOUD_TOKEN:
              name: hetzner-credentials
              key: token
            HCLOUD_CLOUD_INIT:
              name: talos-config
              key: cloud-init
```

Apply with:
```bash
kubectl apply -f package.yaml
```

### Via Helm (direct)

```bash
helm upgrade --install cluster-autoscaler-hetzner \
  ./packages/system/cluster-autoscaler \
  -n cozy-cluster-autoscaler-hetzner \
  -f values-hetzner.yaml \
  -f my-values.yaml
```

## Step 5: Verify

```bash
# Check autoscaler logs
kubectl -n cozy-cluster-autoscaler-hetzner logs -l app.kubernetes.io/name=cluster-autoscaler -f

# Check autoscaler status
kubectl -n cozy-cluster-autoscaler-hetzner get configmap cluster-autoscaler-status -o yaml

# Test scale-up by creating pending pods
kubectl run test-pending --image=nginx --requests='cpu=2,memory=4Gi'
```

## Hetzner Server Types

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

## Hetzner Regions

- `FSN1` - Falkenstein, Germany
- `NBG1` - Nuremberg, Germany
- `HEL1` - Helsinki, Finland
- `ASH` - Ashburn, USA
- `HIL` - Hillsboro, USA

## Troubleshooting

### Nodes not joining cluster

1. Check that machine config is correct and base64-encoded
2. Verify network connectivity (private network, firewall rules)
3. Check Talos image has `qemu-guest-agent` extension

### Scale-down not working

Talos caches absent nodes for up to 30 minutes. Wait or restart autoscaler.

### Image not found

Verify snapshot ID exists: `hcloud image list --type snapshot`
