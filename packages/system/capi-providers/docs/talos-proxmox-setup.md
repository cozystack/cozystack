# Setting up Talos Linux on Proxmox for CozyStack

This guide describes how to set up Talos Linux on Proxmox and run CozyStack.

## Prerequisites

- Proxmox VE server
- Access to Proxmox API
- `talosctl` CLI tool installed
- `kubectl` CLI tool installed
- `clusterctl` CLI tool installed

## Step 1: Download Talos Linux Image

1. Download the latest Talos Linux image for Proxmox:
```bash
curl -LO https://github.com/siderolabs/talos/releases/latest/download/metal-amd64.raw.xz
```

2. Decompress the image:
```bash
xz -d metal-amd64.raw.xz
```

## Step 2: Create VM Template in Proxmox

1. Create a new VM in Proxmox:
   - Name: `talos-template`
   - Memory: 4GB minimum
   - CPU: 2 cores minimum
   - Disk: 20GB minimum
   - Network: Bridge mode

2. Import the Talos image:
```bash
qm importdisk 100 metal-amd64.raw local-lvm
```

3. Attach the disk to the VM:
   - Go to VM hardware
   - Add the imported disk
   - Set as boot disk

4. Configure VM settings:
   - Enable UEFI boot
   - Set machine type to `q35`
   - Add TPM device
   - Enable nested virtualization

5. Create a template from the VM:
   - Right-click on the VM
   - Select "Convert to template"

## Step 3: Configure Talos

1. Create Talos configuration:
```yaml
# talos-config.yaml
version: v1alpha1
machine:
  type: controlplane
  certSANs:
    - 192.168.1.10  # Replace with your VM IP
  kubelet:
    extraArgs:
      cloud-provider: external
  network:
    hostname: talos-control-plane
    interfaces:
      - interface: eth0
        dhcp: true
cluster:
  controlPlane:
    endpoint: https://192.168.1.10:6443  # Replace with your VM IP
  network:
    dnsDomain: cluster.local
    podSubnets:
      - 10.244.0.0/16
    serviceSubnets:
      - 10.96.0.0/12
```

2. Generate Talos configuration:
```bash
talosctl gen config \
  --with-secrets secrets.yaml \
  --with-docs=false \
  --with-examples=false \
  --config-patch-control-plane talos-config.yaml \
  talos-cluster https://192.168.1.10:6443
```

## Step 4: Create VM from Template

1. Clone the template to create a new VM:
```bash
qm clone 100 101 --name talos-control-plane
```

2. Start the VM:
```bash
qm start 101
```

3. Wait for Talos to boot and get its IP address:
```bash
qm status 101
```

## Step 5: Apply Talos Configuration

1. Apply the configuration:
```bash
talosctl apply-config \
  --insecure \
  --nodes 192.168.1.10 \
  --file controlplane.yaml
```

2. Wait for Talos to be ready:
```bash
talosctl health --nodes 192.168.1.10
```

3. Get the kubeconfig:
```bash
talosctl kubeconfig --nodes 192.168.1.10
```

## Step 6: Deploy CozyStack

1. Set up environment variables:
```bash
export KUBECONFIG=./kubeconfig
export COZYSTACK_VERSION=latest
```

2. Deploy CozyStack:
```bash
# Clone CozyStack repository
git clone https://github.com/your-org/cozystack.git
cd cozystack

# Deploy using Helm
helm install cozystack ./packages/system/charts/cozystack \
  --namespace cozystack \
  --create-namespace \
  --version ${COZYSTACK_VERSION}
```

3. Verify deployment:
```bash
kubectl get pods -n cozystack
```

## Troubleshooting

### Common Issues

1. VM won't boot:
   - Check UEFI boot settings
   - Verify disk attachment
   - Check network configuration

2. Talos configuration fails:
   - Verify IP addresses
   - Check network connectivity
   - Review Talos logs: `talosctl logs`

3. CozyStack deployment issues:
   - Check Kubernetes cluster status
   - Verify resource requirements
   - Review pod logs

### Useful Commands

1. Talos management:
```bash
# View Talos logs
talosctl logs

# Check Talos health
talosctl health

# Get Talos version
talosctl version
```

2. Kubernetes management:
```bash
# Check cluster status
kubectl get nodes
kubectl get pods -A

# View CozyStack logs
kubectl logs -n cozystack -l app.kubernetes.io/name=cozystack
```

## Additional Resources

- [Talos Linux Documentation](https://www.talos.dev/docs/latest/)
- [Proxmox VE Documentation](https://pve.proxmox.com/wiki/Main_Page)
- [CozyStack Documentation](https://github.com/your-org/cozystack/docs) 