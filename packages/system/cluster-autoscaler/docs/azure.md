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
  --name <resource-group> \
  --location <location>
```

### 1.3 Create VNet and Subnet

```bash
az network vnet create \
  --resource-group <resource-group> \
  --name cozystack-vnet \
  --address-prefix 10.2.0.0/16 \
  --subnet-name workers \
  --subnet-prefix 10.2.0.0/24 \
  --location <location>
```

### 1.4 Create Network Security Group

```bash
az network nsg create \
  --resource-group <resource-group> \
  --name cozystack-nsg \
  --location <location>

# Allow WireGuard
az network nsg rule create \
  --resource-group <resource-group> \
  --nsg-name cozystack-nsg \
  --name AllowWireGuard \
  --priority 100 \
  --direction Inbound \
  --access Allow \
  --protocol Udp \
  --destination-port-ranges 51820

# Allow Talos API
az network nsg rule create \
  --resource-group <resource-group> \
  --nsg-name cozystack-nsg \
  --name AllowTalosAPI \
  --priority 110 \
  --direction Inbound \
  --access Allow \
  --protocol Tcp \
  --destination-port-ranges 50000

# Associate NSG with subnet
az network vnet subnet update \
  --resource-group <resource-group> \
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

### 2.2 Create Managed Image from VHD

```bash
# Download Talos Azure image
curl -L -o azure-amd64.raw.xz \
  "https://factory.talos.dev/image/${SCHEMATIC_ID}/<talos-version>/azure-amd64.raw.xz"

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
  --resource-group <resource-group> \
  --name talos-<talos-version> \
  --location <location> \
  --upload-type Upload \
  --upload-size-bytes $VHD_SIZE \
  --sku Standard_LRS \
  --os-type Linux \
  --hyper-v-generation V2

# Get SAS URL for upload
SAS_URL=$(az disk grant-access \
  --resource-group <resource-group> \
  --name talos-<talos-version> \
  --access-level Write \
  --duration-in-seconds 3600 \
  --query accessSAS --output tsv)

# Upload VHD
azcopy copy azure-amd64.vhd "$SAS_URL" --blob-type PageBlob

# Revoke access
az disk revoke-access \
  --resource-group <resource-group> \
  --name talos-<talos-version>

# Create managed image from disk
az image create \
  --resource-group <resource-group> \
  --name talos-<talos-version> \
  --location <location> \
  --os-type Linux \
  --hyper-v-generation V2 \
  --source $(az disk show --resource-group <resource-group> --name talos-<talos-version> --query id --output tsv)
```

## Step 3: Create Talos Machine Config for Azure

Create a machine config similar to the Hetzner one, with these Azure-specific changes:

```yaml
machine:
  nodeLabels:
    kilo.squat.ai/location: azure
    topology.kubernetes.io/zone: azure
  kubelet:
    nodeIP:
      validSubnets:
        - 10.2.0.0/24                  # <-- Azure VNet subnet
```

All other settings (cluster tokens, control plane endpoint, extensions, etc.) remain the same as the Hetzner config.

## Step 4: Create VMSS (Virtual Machine Scale Set)

```bash
IMAGE_ID=$(az image show \
  --resource-group <resource-group> \
  --name talos-<talos-version> \
  --query id --output tsv)

az vmss create \
  --resource-group <resource-group> \
  --name workers \
  --location <location> \
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
- Check VM quota in your region: `az vm list-usage --location <location>`
- `--custom-data` passes the Talos machine config to new instances

## Step 5: Deploy Cluster Autoscaler

Create the Package resource:

```yaml
apiVersion: cozystack.io/v1alpha1
kind: Package
metadata:
  name: cozystack.cluster-autoscaler-azure
spec:
  variant: default
  components:
    cluster-autoscaler-azure:
      values:
        cluster-autoscaler:
          azureClientID: "<APP_ID>"
          azureClientSecret: "<PASSWORD>"
          azureTenantID: "<TENANT_ID>"
          azureSubscriptionID: "<SUBSCRIPTION_ID>"
          azureResourceGroup: "<RESOURCE_GROUP>"
          azureVMType: "vmss"
          autoscalingGroups:
            - name: workers
              minSize: 0
              maxSize: 10
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

## Step 6: Kilo WireGuard Endpoint Configuration

**Important:** Azure nodes behind NAT need their public IP advertised as the WireGuard endpoint. Without this, the WireGuard tunnel between Hetzner and Azure nodes will not be established.

Each new Azure node needs the annotation:

```bash
kubectl annotate node <NODE_NAME> \
  kilo.squat.ai/force-endpoint=<PUBLIC_IP>:51820
```

### Automated Endpoint Configuration

For automated endpoint detection, create a DaemonSet that runs on Azure nodes (`topology.kubernetes.io/zone=azure`) and:

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
az vmss scale --resource-group <resource-group> --name workers --new-capacity 1

# Check node joined
kubectl get nodes -o wide

# Check WireGuard tunnel
kubectl logs -n cozy-kilo <kilo-pod-on-azure-node>

# Scale down
az vmss scale --resource-group <resource-group> --name workers --new-capacity 0
```

### Autoscaler test

Deploy a workload to trigger autoscaling:

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
        topology.kubernetes.io/zone: azure
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
- Check VMSS instance provisioning state: `az vmss list-instances --resource-group <resource-group> --name workers`

### WireGuard tunnel not established
- Verify `kilo.squat.ai/force-endpoint` annotation is set with the public IP
- Check NSG allows inbound UDP 51820
- Inspect kilo logs: `kubectl logs -n cozy-kilo <kilo-pod>`

### VM quota errors
- Check quota: `az vm list-usage --location <location>`
- Request quota increase via Azure portal
- Try a different VM family that has available quota

### SkuNotAvailable errors
- Some VM sizes may have capacity restrictions in certain regions
- Try a different VM size: `az vm list-skus --location <location> --size <prefix>`
