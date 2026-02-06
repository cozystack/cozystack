# Cluster Autoscaler for Azure

This guide explains how to configure cluster-autoscaler for automatic node scaling in Azure with Talos Linux.

## Prerequisites

- Azure subscription with Contributor Service Principal
- `az` CLI installed
- Existing Talos Kubernetes cluster with Kilo WireGuard mesh
- Talos worker machine config

## Step 1: Create Azure Infrastructure

### 1.1 Login with Service Principal

```bash
az login --service-principal \
  --username "<APP_ID>" \
  --password "<PASSWORD>" \
  --tenant "<TENANT_ID>"
```

### 1.2 Create Resource Group

```bash
az group create \
  --name cozystack-autoscaler \
  --location germanywestcentral
```

### 1.3 Create VNet and Subnet

```bash
az network vnet create \
  --resource-group cozystack-autoscaler \
  --name cozystack-vnet \
  --address-prefix 10.2.0.0/16 \
  --subnet-name workers \
  --subnet-prefix 10.2.0.0/24 \
  --location germanywestcentral
```

### 1.4 Create Network Security Group

```bash
az network nsg create \
  --resource-group cozystack-autoscaler \
  --name cozystack-nsg \
  --location germanywestcentral

# Allow WireGuard
az network nsg rule create \
  --resource-group cozystack-autoscaler \
  --nsg-name cozystack-nsg \
  --name AllowWireGuard \
  --priority 100 \
  --direction Inbound \
  --access Allow \
  --protocol Udp \
  --destination-port-ranges 51820

# Allow Talos API
az network nsg rule create \
  --resource-group cozystack-autoscaler \
  --nsg-name cozystack-nsg \
  --name AllowTalosAPI \
  --priority 110 \
  --direction Inbound \
  --access Allow \
  --protocol Tcp \
  --destination-port-ranges 50000

# Associate NSG with subnet
az network vnet subnet update \
  --resource-group cozystack-autoscaler \
  --vnet-name cozystack-vnet \
  --name workers \
  --network-security-group cozystack-nsg
```

## Step 2: Create Talos Image

### 2.1 Generate Schematic ID

Create a schematic at [factory.talos.dev](https://factory.talos.dev) with required extensions:

```bash
curl -s -X POST https://factory.talos.dev/schematics \
  -H "Content-Type: application/json" \
  -d '{
    "customization": {
      "systemExtensions": {
        "officialExtensions": [
          "siderolabs/amd-ucode",
          "siderolabs/amdgpu-firmware",
          "siderolabs/bnx2-bnx2x",
          "siderolabs/drbd",
          "siderolabs/i915-ucode",
          "siderolabs/intel-ice-firmware",
          "siderolabs/intel-ucode",
          "siderolabs/qlogic-firmware",
          "siderolabs/zfs"
        ]
      }
    }
  }'
```

Save the returned `id` as `SCHEMATIC_ID`.

### 2.2 Create Storage Account and Upload VHD

```bash
# Create storage account
az storage account create \
  --name cozystacktalos \
  --resource-group cozystack-autoscaler \
  --location germanywestcentral \
  --sku Standard_LRS

# Download Talos Azure image
curl -L -o azure-amd64.raw.xz \
  "https://factory.talos.dev/image/${SCHEMATIC_ID}/v1.11.6/azure-amd64.raw.xz"

# Decompress
xz -d azure-amd64.raw.xz

# Convert to VHD
qemu-img convert -f raw -o subformat=fixed,force_size -O vpc \
  azure-amd64.raw azure-amd64.vhd

# Get VHD size
VHD_SIZE=$(stat -f%z azure-amd64.vhd)  # macOS
# VHD_SIZE=$(stat -c%s azure-amd64.vhd)  # Linux

# Create managed disk for upload
az disk create \
  --resource-group cozystack-autoscaler \
  --name talos-v1.11.6 \
  --location germanywestcentral \
  --upload-type Upload \
  --upload-size-bytes $VHD_SIZE \
  --sku Standard_LRS \
  --os-type Linux \
  --hyper-v-generation V2

# Get SAS URL for upload
SAS_URL=$(az disk grant-access \
  --resource-group cozystack-autoscaler \
  --name talos-v1.11.6 \
  --access-level Write \
  --duration-in-seconds 3600 \
  --query accessSAS --output tsv)

# Upload VHD
azcopy copy azure-amd64.vhd "$SAS_URL" --blob-type PageBlob

# Revoke access
az disk revoke-access \
  --resource-group cozystack-autoscaler \
  --name talos-v1.11.6

# Create managed image from disk
az image create \
  --resource-group cozystack-autoscaler \
  --name talos-v1.11.6 \
  --location germanywestcentral \
  --os-type Linux \
  --hyper-v-generation V2 \
  --source $(az disk show --resource-group cozystack-autoscaler --name talos-v1.11.6 --query id --output tsv)
```

## Step 3: Create Talos Machine Config for Azure

Create a machine config similar to the Hetzner one, with these Azure-specific changes:

```yaml
machine:
  nodeLabels:
    kilo.squat.ai/location: azure      # <-- changed from 'hetzner-cloud'
  kubelet:
    nodeIP:
      validSubnets:
        - 10.2.0.0/24                  # <-- Azure VNet subnet
```

All other settings (cluster tokens, control plane endpoint, extensions, etc.) remain the same as the Hetzner config.

## Step 4: Create VMSS (Virtual Machine Scale Set)

```bash
IMAGE_ID=$(az image show \
  --resource-group cozystack-autoscaler \
  --name talos-v1.11.6 \
  --query id --output tsv)

az vmss create \
  --resource-group cozystack-autoscaler \
  --name workers \
  --location germanywestcentral \
  --orchestration-mode Uniform \
  --image "$IMAGE_ID" \
  --vm-sku Standard_D2s_v3 \
  --instance-count 0 \
  --vnet-name cozystack-vnet \
  --subnet workers \
  --public-ip-per-vm \
  --custom-data machineconfig-azure.yaml \
  --security-type Standard \
  --admin-username talos \
  --authentication-type ssh \
  --generate-ssh-keys \
  --upgrade-policy-mode Manual
```

**Important notes:**
- Must use `--orchestration-mode Uniform` (cluster-autoscaler requires Uniform mode)
- Must use `--public-ip-per-vm` for WireGuard connectivity
- Check VM quota in your region: `az vm list-usage --location germanywestcentral`
- `--custom-data` passes the Talos machine config to new instances

### Available VM sizes in Germany West Central

| Family | Example | vCPU | RAM | Use case |
|--------|---------|------|-----|----------|
| D-series | Standard_D2s_v3 | 2 | 8 GB | General purpose |
| D-series | Standard_D4s_v3 | 4 | 16 GB | General purpose |
| NC T4 | Standard_NC4as_T4_v3 | 4 | 28 GB | GPU (T4) |
| NC A100 | Standard_NC24ads_A100_v4 | 24 | 220 GB | GPU (A100) |
| NC H100 | Standard_NC40ads_H100_v5 | 40 | 320 GB | GPU (H100) |

## Step 5: Create Kubernetes Secrets

### 5.1 Azure Credentials Secret

```bash
kubectl create namespace cozy-cluster-autoscaler-azure

kubectl create secret generic azure-credentials \
  --namespace cozy-cluster-autoscaler-azure \
  --from-literal=ClientID="<APP_ID>" \
  --from-literal=ClientSecret="<PASSWORD>" \
  --from-literal=TenantID="<TENANT_ID>" \
  --from-literal=SubscriptionID="<SUBSCRIPTION_ID>" \
  --from-literal=ResourceGroup="cozystack-autoscaler" \
  --from-literal=VMType="vmss"
```

### 5.2 Talos Machine Config Secret

```bash
kubectl create secret generic talos-config \
  --namespace cozy-cluster-autoscaler-azure \
  --from-file=cloud-init=machineconfig-azure.yaml
```

## Step 6: Deploy Cluster Autoscaler

Example Package resource:

```yaml
apiVersion: cozystack.io/v1alpha1
kind: Package
metadata:
  name: cozystack.cluster-autoscaler-azure
  namespace: cozy-cluster-autoscaler-azure
spec:
  type: cluster-autoscaler
  values:
    cluster-autoscaler:
      azureClientID: "<APP_ID>"
      azureClientSecret: "<PASSWORD>"
      azureTenantID: "<TENANT_ID>"
      azureSubscriptionID: "<SUBSCRIPTION_ID>"
      azureResourceGroup: "cozystack-autoscaler"
      azureVMType: "vmss"
      autoscalingGroups:
        - name: workers
          minSize: 0
          maxSize: 10
```

Or configure the HelmRelease directly with `secretKeyRefNameOverride` to use existing secrets:

```yaml
cluster-autoscaler:
  secretKeyRefNameOverride: azure-credentials
  autoscalingGroups:
    - name: workers
      minSize: 0
      maxSize: 10
```

## Step 7: Kilo WireGuard Endpoint Configuration

**Important:** Azure nodes behind NAT need their public IP advertised as the WireGuard endpoint. Without this, the WireGuard tunnel between Hetzner and Azure nodes will not be established.

Each new Azure node needs the annotation:

```bash
kubectl annotate node <NODE_NAME> \
  kilo.squat.ai/force-endpoint=<PUBLIC_IP>:51820
```

### Automated Endpoint Configuration

For automated endpoint detection, create a DaemonSet that runs on Azure nodes (`kilo.squat.ai/location=azure`) and:

1. Queries Azure Instance Metadata Service (IMDS) for the public IP:
   ```bash
   curl -s -H "Metadata: true" \
     "http://169.254.169.254/metadata/instance/network/interface/0/ipv4/ipAddress/0/publicIpAddress?api-version=2021-02-01&format=text"
   ```
2. Annotates the node with `kilo.squat.ai/force-endpoint=<PUBLIC_IP>:51820`

This ensures new autoscaled nodes automatically get proper WireGuard connectivity.

## Testing

### Manual scale test

```bash
# Scale up
az vmss scale --resource-group cozystack-autoscaler --name workers --new-capacity 1

# Check node joined
kubectl get nodes -o wide

# Check WireGuard tunnel
kubectl logs -n cozy-kilo <kilo-pod-on-azure-node>

# Scale down
az vmss scale --resource-group cozystack-autoscaler --name workers --new-capacity 0
```

### Autoscaler test

Deploy a workload with anti-affinity to trigger autoscaling:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: test-azure-autoscale
spec:
  replicas: 3
  selector:
    matchLabels:
      app: test-azure
  template:
    metadata:
      labels:
        app: test-azure
    spec:
      nodeSelector:
        kilo.squat.ai/location: azure
      containers:
        - name: pause
          image: registry.k8s.io/pause:3.9
          resources:
            requests:
              cpu: "500m"
              memory: "512Mi"
```

## Troubleshooting

### Node doesn't join cluster
- Check that the Talos machine config control plane endpoint is reachable from Azure
- Verify NSG rules allow outbound traffic to port 6443
- Check VMSS instance provisioning state: `az vmss list-instances --resource-group cozystack-autoscaler --name workers`

### WireGuard tunnel not established
- Verify `kilo.squat.ai/force-endpoint` annotation is set with the public IP
- Check NSG allows inbound UDP 51820
- Inspect kilo logs: `kubectl logs -n cozy-kilo <kilo-pod>`

### VM quota errors
- Check quota: `az vm list-usage --location germanywestcentral`
- Request quota increase via Azure portal
- Try a different VM family that has available quota

### SkuNotAvailable errors
- Some VM sizes may have capacity restrictions in certain regions
- Try a different VM size: `az vm list-skus --location germanywestcentral --size <prefix>`

## Current Infrastructure Reference

Created in subscription `cdd9c3ff-ef22-46f5-916f-cf529408f367` (Sandbox autoscaler):

| Resource | Name | Details |
|----------|------|---------|
| Resource Group | cozystack-autoscaler | germanywestcentral |
| VNet | cozystack-vnet | 10.2.0.0/16 |
| Subnet | workers | 10.2.0.0/24 |
| NSG | cozystack-nsg | Allow UDP 51820, TCP 50000 |
| Managed Image | talos-v1.11.6 | Talos with extensions (drbd, zfs, etc.) |
| VMSS | workers | Standard_D2s_v3, Uniform, 0 instances |
| Storage Account | cozystacktalos | Used for VHD upload |

Service Principal: `78df2235-89b3-4b94-b6db-5573677eee57` (autoscaler-contributor)
