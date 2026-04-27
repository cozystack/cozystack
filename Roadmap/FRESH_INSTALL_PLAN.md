# Fresh CozyStack v0.37.2 Installation Plan

**Date**: 2025-10-24  
**Approach**: Option C (Fresh Install + Proxmox Bundle)  
**Target Version**: v0.37.2  
**Bundle**: paas-proxmox

## Overview

Install clean CozyStack v0.37.2 cluster with paas-proxmox bundle for Proxmox VE integration.

**Timeline**: 3 weeks total
- Week 0: Proxmox infrastructure setup on Hetzner (Days 0.1-0.3)
- Week 1: CozyStack installation and configuration (Days 1-5)
- Week 2: Proxmox integration testing (Days 6-10)
- Week 3+: Migration planning and execution

## Prerequisites

### Hetzner Server Requirements

**Dedicated Server** (for Proxmox VE):
```
Recommended: AX-Line or EX-Line
CPU: AMD EPYC or Intel Xeon (8+ cores, 16+ threads)
RAM: 64GB+ ECC
Disk: 2x NVMe (software RAID) or hardware RAID
Network: 1 Gbps uplink
Location: Europe (Nuremberg, Falkenstein, Helsinki)
```

**Example Configurations**:
```
Budget Option (AX41-NVMe):
- CPU: AMD Ryzen 5 3600 (6 cores, 12 threads)
- RAM: 64 GB DDR4 ECC
- Disk: 2 x 512 GB NVMe SSD (software RAID)
- Network: 1 Gbit/s
- Price: ~€40-50/month

Recommended (AX102):
- CPU: AMD EPYC 7502P (32 cores, 64 threads)
- RAM: 128 GB DDR4 ECC
- Disk: 2 x 3.84 TB NVMe SSD
- Network: 1 Gbit/s
- Price: ~€200/month

High-End (EX130):
- CPU: AMD EPYC 9454P (48 cores, 96 threads)
- RAM: 256 GB DDR5 ECC
- Disk: 2 x 7.68 TB NVMe SSD
- Network: 10 Gbit/s
- Price: ~€400-500/month
```

### Hetzner Account Requirements

- Active Hetzner account
- Server ordered and delivered
- Rescue system access credentials
- Public IP address assigned
- Optional: Additional IPs (/29 subnet for VMs)

### Proxmox Resources Needed

**Control Plane VMs** (3 VMs):
```
Name: cozy-new-cp1, cozy-new-cp2, cozy-new-cp3
CPU: 4 cores each
RAM: 8GB each
Disk: 50GB each (system) + 100GB (LINSTOR)
Network: vmbr0
Template: Talos Linux or Ubuntu 22.04
```

**Worker VM** (1 VM, optional if using Proxmox host):
```
Name: cozy-new-worker1
CPU: 8 cores
RAM: 16GB
Disk: 100GB (system) + 200GB (LINSTOR)
Network: vmbr0
Template: Talos Linux or Ubuntu 22.04
```

**Total Resources**:
- VMs: 4
- CPUs: 20 cores
- RAM: 40GB
- Disk: 550GB

### Network Requirements

- **IP Range**: 10.0.0.200-220 (or different from old cluster)
- **Gateway**: 10.0.0.1
- **DNS**: 10.0.0.1
- **VLAN**: Same as old cluster or dedicated

### Software Requirements

**On Development Machine**:
- kubectl 1.28+
- helm 3.12+
- talosctl (if using Talos)

**On Proxmox Host**:
- Talos Linux image or Ubuntu 22.04 cloud image
- NexCage v0.7.3+ (OCI runtime for LXC integration)
- Dependencies: libcap-dev, libseccomp-dev, libyajl-dev

## Installation Steps

### Week 0: Proxmox Infrastructure on Hetzner

#### Day 0.1: Hetzner Server Preparation (1 hour)

**Step 0.1.1: Order and Access Server**

1. **Order Server** (if not done):
```
- Login to robot.hetzner.com
- Select server model (AX102 recommended)
- Choose location (Nuremberg/Falkenstein)
- Order and wait for delivery email (~5-30 minutes)
```

2. **Activate Rescue System**:
```bash
# Via Hetzner Robot web interface:
# 1. Navigate to your server
# 2. Click "Rescue" tab
# 3. Select "Linux" and "64 bit"
# 4. Click "Activate rescue system"
# 5. Note down root password
# 6. Click "Reset" tab → Execute hardware reset

# Or via API (if you prefer automation):
curl -u "YOUR_USERNAME:YOUR_PASSWORD" \
  -X POST \
  https://robot-ws.your-server.de/boot/YOUR_SERVER_IP/rescue \
  -d "os=linux" \
  -d "arch=64"

curl -u "YOUR_USERNAME:YOUR_PASSWORD" \
  -X POST \
  https://robot-ws.your-server.de/reset/YOUR_SERVER_IP \
  -d "type=hw"
```

3. **SSH to Rescue System**:
```bash
# Wait 2-3 minutes for boot
ssh root@YOUR_SERVER_IP
# Enter rescue password from step 2
```

**Step 0.1.2: Verify Hardware**

```bash
# In rescue system, verify hardware

# Check available disks
lsblk
# Expected: 2x NVMe drives (/dev/nvme0n1, /dev/nvme1n1)
# or 2x SATA/SAS drives (/dev/sda, /dev/sdb)

# Check network interfaces
ip addr show
# Note the interface name (e.g., eno1, enp0s31f6)

# Check RAM
free -h

# Check CPU
lscpu

# Save hardware info for reference
lsblk > /root/hardware-info.txt
ip addr >> /root/hardware-info.txt
free -h >> /root/hardware-info.txt
lscpu >> /root/hardware-info.txt
```

**Note**: The automation script (pve-install.sh) will handle:
- ✅ Disk partitioning (ZFS RAID1 automatic)
- ✅ Network configuration (auto-detection)
- ✅ Storage setup
- ✅ Bridge creation (vmbr0 for VMs)

No manual Debian installation needed - script does everything!

#### Day 0.2: Automated Proxmox Installation (4 hours)

**Step 0.2.1: Use Existing Automation Script**

Use the pre-built automation script from your repository:

```bash
# SSH to Hetzner rescue system
ssh root@YOUR_SERVER_IP

# Download the automated installation script
cd /root
wget https://raw.githubusercontent.com/themoriarti/proxmox-hetzner/refs/heads/main/scripts/pve-install.sh
chmod +x pve-install.sh

# Review the script (optional)
less pve-install.sh
```

**Script Features** (from themoriarti/proxmox-hetzner):

The script provides a fully automated Proxmox VE installation with:

1. **Interactive Configuration**:
   - Network interface selection (auto-detects available interfaces)
   - Hostname and FQDN configuration
   - Timezone selection
   - Email address setup
   - Private subnet configuration
   - Root password setup

2. **Automatic Network Detection**:
   - IPv4 CIDR and gateway
   - IPv6 configuration
   - MAC address
   - Interface altnames support

3. **Installation Process**:
   - Downloads latest Proxmox VE ISO automatically
   - Creates answer.toml for unattended installation
   - Generates autoinstall ISO with proxmox-auto-install-assistant
   - Installs via QEMU/KVM with VNC access
   - UEFI and BIOS support

4. **Post-Installation Configuration**:
   - Network interfaces (public + private bridge)
   - Hosts file configuration
   - DNS resolvers (Hetzner + fallback)
   - Sysctl tweaks (99-proxmox.conf)
   - APT sources (Debian + Proxmox no-subscription)
   - Disables rpcbind
   - Cleans up enterprise repo

5. **Storage Configuration**:
   - ZFS RAID1 (default)
   - Automatic disk detection and partitioning

6. **Template Files** (downloaded from repository):
   - `99-proxmox.conf` - Sysctl optimizations
   - `hosts` - Hostname resolution
   - `interfaces` - Network configuration
   - `debian.sources` - Debian repositories
   - `proxmox.sources` - Proxmox repositories

**Step 0.2.2: Run Automated Installation**

```bash
# Execute installation script (interactive)
./pve-install.sh

# You will be prompted for:
# 1. Interface name (e.g., eno1) - auto-detected
# 2. Hostname (e.g., proxmox-hetzner)
# 3. FQDN (e.g., proxmox.example.com)
# 4. Timezone (e.g., Europe/Kiev)
# 5. Email address (e.g., admin@example.com)
# 6. Private subnet (e.g., 10.0.0.0/24)
# 7. New root password

# Example inputs:
# Interface name: eno1
# Hostname: proxmox-cozy
# FQDN: proxmox-cozy.cp.if.ua
# Timezone: Europe/Kiev
# Email: admin@cp.if.ua
# Private subnet: 10.0.0.0/24
# Root password: <your-secure-password>

# Script will:
# 1. Download latest Proxmox VE ISO
# 2. Create autoinstall configuration
# 3. Install Proxmox via QEMU/KVM
# 4. Configure network (public + private bridge)
# 5. Setup repositories
# 6. Configure SSH access
# 7. Automatic reboot to installed system

# Installation takes approximately 15-30 minutes
# You can monitor via VNC if needed (port 5901)
```

**During Installation** (optional monitoring):

```bash
# From another terminal, you can monitor the installation:

# Check QEMU process
ps aux | grep qemu

# Monitor installation log
tail -f /root/qemu-install.log

# Connect via VNC (optional)
# Use VNC client to connect to localhost:5901
# This shows the Proxmox installation progress
```

**Step 0.2.3: Post-Installation Verification**

```bash
# SSH back after reboot
ssh root@YOUR_SERVER_IP

# Verify Proxmox version
pveversion
# Expected: pve-manager/8.x.x/...

# Check cluster status
pvecm status
# Expected: Cluster information not available (standalone node)

# Check storage
pvesm status
# Expected: local, local-lvm

# Verify bridge
ip addr show vmbr0
# Expected: vmbr0 with 10.0.0.1/24

# Check web interface
curl -k https://localhost:8006
# Expected: HTML response

# Test from browser
echo "Access web UI at: https://YOUR_SERVER_IP:8006"
echo "Login: root / your_root_password"
```

#### Day 0.3: Post-Installation Configuration (2 hours)

**Step 0.3.1: Configure Storage**

```bash
# Create additional storage pools if needed

# Check disk space
pvesm status

# Configure LVM-thin for VMs (recommended)
# Usually auto-configured as 'local-lvm'

# Optional: Create NFS/CIFS storage
# pvesm add nfs backup --server NFS_SERVER --export /backup --content backup

# Optional: Create directory storage
mkdir -p /var/lib/vz/templates
mkdir -p /var/lib/vz/iso
```

**Step 0.3.2: Upload ISO/Templates**

```bash
# Download common ISO images
cd /var/lib/vz/template/iso/

# Debian cloud image (for VMs)
wget https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-generic-amd64.qcow2

# Ubuntu cloud image
wget https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img

# Talos Linux (for Kubernetes)
wget https://github.com/siderolabs/talos/releases/download/v1.8.0/metal-amd64.raw.xz
xz -d metal-amd64.raw.xz
mv metal-amd64.raw talos-v1.8.0-amd64.raw

# Set permissions
chmod 644 *.qcow2 *.img *.raw
```

**Step 0.3.3: Configure Backup**

```bash
# Install backup tools
apt-get install -y proxmox-backup-client

# Configure vzdump (Proxmox backup)
cat > /etc/vzdump.conf <<EOF
# Backup settings
tmpdir: /var/tmp
dumpdir: /var/lib/vz/dump
storage: local
mode: snapshot
compress: zstd
pigz: 1
ionice: 7
EOF

# Create backup script (optional)
cat > /usr/local/bin/backup-all-vms.sh <<'SCRIPT'
#!/bin/bash
vzdump --all --mode snapshot --compress zstd --storage local
SCRIPT

chmod +x /usr/local/bin/backup-all-vms.sh

# Optional: Schedule daily backups
cat > /etc/cron.d/proxmox-backup <<EOF
# Daily backup at 2 AM
0 2 * * * root /usr/local/bin/backup-all-vms.sh
EOF
```

**Step 0.3.4: Install Automation Tools**

```bash
# Install Ansible (for future automation)
apt-get install -y ansible

# Install Terraform (for IaC)
wget -O- https://apt.releases.hashicorp.com/gpg | gpg --dearmor -o /usr/share/keyrings/hashicorp-archive-keyring.gpg
echo "deb [signed-by=/usr/share/keyrings/hashicorp-archive-keyring.gpg] https://apt.releases.hashicorp.com $(lsb_release -cs) main" | tee /etc/apt/sources.list.d/hashicorp.list
apt-get update
apt-get install -y terraform

# Verify installations
ansible --version
terraform --version
```

**Step 0.3.5: Security Hardening**

```bash
# 1. Configure SSH
sed -i 's/#PermitRootLogin yes/PermitRootLogin prohibit-password/' /etc/ssh/sshd_config
sed -i 's/#PasswordAuthentication yes/PasswordAuthentication no/' /etc/ssh/sshd_config
systemctl restart sshd

# 2. Install fail2ban
apt-get install -y fail2ban
systemctl enable fail2ban
systemctl start fail2ban

# 3. Configure firewall (if not using Hetzner firewall)
apt-get install -y ufw
ufw default deny incoming
ufw default allow outgoing
ufw allow 22/tcp    # SSH
ufw allow 8006/tcp  # Proxmox web UI
ufw allow 5900:5999/tcp  # VNC for VMs
# Don't enable yet if accessing via SSH initially
# ufw enable

# 4. Update root password (if using default)
echo "Consider changing root password:"
echo "passwd"

# 5. Set up automatic security updates
apt-get install -y unattended-upgrades
dpkg-reconfigure -plow unattended-upgrades
```

**Step 0.3.6: Documentation**

```bash
# Save installation details
cat > /root/PROXMOX_INSTALL_INFO.txt <<EOF
Proxmox VE Installation Information
====================================
Date: $(date)
Hostname: $(hostname -f)
Version: $(pveversion)

Server Details:
- Provider: Hetzner
- Model: $(dmidecode -s system-product-name)
- CPU: $(lscpu | grep "Model name" | sed 's/Model name:\s*//')
- RAM: $(free -h | awk '/^Mem:/ {print $2}')
- Disks: $(lsblk -d -n -o NAME,SIZE,TYPE | grep disk)

Network:
- Public IP: $(hostname -I | awk '{print $1}')
- Bridge: vmbr0 (10.0.0.1/24)

Storage:
$(pvesm status)

Access:
- Web UI: https://$(hostname -I | awk '{print $1}'):8006
- SSH: root@$(hostname -I | awk '{print $1}')

Next Steps:
1. ✅ Proxmox installed
2. ⏳ Install NexCage
3. ⏳ Create VM templates
4. ⏳ Install CozyStack
EOF

cat /root/PROXMOX_INSTALL_INFO.txt
```

### Week 1: CozyStack Installation

#### Day 1: Proxmox Host Preparation (5 hours)

**Step 1.0: Install NexCage on Proxmox Host**

NexCage is a next-generation OCI runtime for Proxmox VE that enables LXC containers to run as Kubernetes pods.

On Proxmox host:
```bash
# Install dependencies
apt-get update
apt-get install -y libcap-dev libseccomp-dev libyajl-dev git build-essential

# Install Zig 0.15.1 (required for NexCage)
cd /tmp
wget https://ziglang.org/download/0.15.1/zig-linux-x86_64-0.15.1.tar.xz
tar -xf zig-linux-x86_64-0.15.1.tar.xz
mv zig-linux-x86_64-0.15.1 /opt/zig-0.15.1
ln -s /opt/zig-0.15.1/zig /usr/local/bin/zig

# Verify Zig installation
zig version  # Should output: 0.15.1

# Clone and build NexCage
cd /opt
git clone https://github.com/CageForge/nexcage.git
cd nexcage
git checkout v0.7.3  # Latest stable version

# Build NexCage
zig build -Doptimize=ReleaseSafe

# Install binary
cp zig-out/bin/nexcage /usr/local/bin/nexcage
chmod +x /usr/local/bin/nexcage

# Verify installation
nexcage version
nexcage --help

# Create NexCage config directory
mkdir -p /etc/nexcage
cp config.json.example /etc/nexcage/config.json

# Configure NexCage for Kubernetes integration
cat > /etc/nexcage/config.json <<EOF
{
  "runtime": "lxc",
  "lxc_path": "/var/lib/lxc",
  "default_backend": "lxc",
  "oci_fallback": true,
  "logging": {
    "level": "info",
    "file": "/var/log/nexcage.log"
  },
  "kubernetes": {
    "enabled": true,
    "cri_socket": "/run/nexcage/nexcage.sock"
  }
}
EOF

# Create systemd service for NexCage
cat > /etc/systemd/system/nexcage.service <<EOF
[Unit]
Description=NexCage OCI Runtime for LXC
Documentation=https://github.com/CageForge/nexcage
After=network.target

[Service]
Type=notify
ExecStart=/usr/local/bin/nexcage daemon --config /etc/nexcage/config.json
Restart=on-failure
RestartSec=5
LimitNOFILE=1048576
LimitNPROC=infinity
LimitCORE=infinity
TasksMax=infinity

[Install]
WantedBy=multi-user.target
EOF

# Enable and start NexCage service
systemctl daemon-reload
systemctl enable nexcage
systemctl start nexcage

# Verify NexCage is running
systemctl status nexcage
nexcage list --runtime lxc

# Test NexCage basic functionality
nexcage create --help
```

**Expected Result**:
```
NexCage v0.7.3 installed and running
Service: active (running)
Socket: /run/nexcage/nexcage.sock created
```

**Troubleshooting**:
```bash
# Check logs
journalctl -u nexcage -f

# Check socket
ls -la /run/nexcage/nexcage.sock

# Test LXC list
nexcage list --runtime lxc
```

**Step 1.1: Create VM Templates**

On Proxmox host:
```bash
# If using Talos Linux (recommended)
# Download Talos v1.8.0+ image
wget https://github.com/siderolabs/talos/releases/download/v1.8.0/metal-amd64.raw.xz
xz -d metal-amd64.raw.xz

# Create template VM
qm create 9100 --name talos-v1.8-template \
  --memory 8192 --cores 4 --net0 virtio,bridge=vmbr0

# Import disk
qm importdisk 9100 metal-amd64.raw local-lvm

# Attach disk
qm set 9100 --scsihw virtio-scsi-pci --scsi0 local-lvm:vm-9100-disk-0

# Set boot
qm set 9100 --boot c --bootdisk scsi0

# Add second disk for LINSTOR
qm set 9100 --scsi1 local-lvm:100

# Convert to template
qm template 9100
```

**Step 1.2: Clone VMs for Control Plane**

```bash
# Clone 3 control plane VMs
qm clone 9100 1001 --name cozy-new-cp1 --full 1
qm clone 9100 1002 --name cozy-new-cp2 --full 1
qm clone 9100 1003 --name cozy-new-cp3 --full 1

# Optional: Clone worker
qm clone 9100 1011 --name cozy-new-worker1 --full 1

# Configure resources (if needed)
for vm in 1001 1002 1003; do
  qm set $vm --memory 8192 --cores 4
done
```

**Step 1.3: Start VMs**

```bash
# Start control plane VMs
qm start 1001
qm start 1002
qm start 1003

# Get IPs (from Proxmox console or DHCP)
qm guest exec 1001 -- ip addr show | grep inet
# Repeat for 1002, 1003

# Document IPs
echo "cozy-new-cp1: 10.0.0.201" > /root/new-cluster-ips.txt
echo "cozy-new-cp2: 10.0.0.202" >> /root/new-cluster-ips.txt
echo "cozy-new-cp3: 10.0.0.203" >> /root/new-cluster-ips.txt
```

#### Day 2: Talos/Kubernetes Bootstrap (6 hours)

**Step 2.1: Generate Talos Configuration**

```bash
# Install talosctl
curl -sL https://talos.dev/install | sh

# Generate config
talosctl gen config cozy-new \
  https://10.0.0.201:6443 \
  --output-dir /root/cozy-new-config \
  --with-docs=false \
  --with-examples=false

# Patch configuration for 3-node HA
# (Edit controlplane.yaml, worker.yaml as needed)
```

**Step 2.2: Apply Configuration**

```bash
# Apply to all control plane nodes
talosctl apply-config --insecure \
  --nodes 10.0.0.201 \
  --file /root/cozy-new-config/controlplane.yaml

talosctl apply-config --insecure \
  --nodes 10.0.0.202 \
  --file /root/cozy-new-config/controlplane.yaml

talosctl apply-config --insecure \
  --nodes 10.0.0.203 \
  --file /root/cozy-new-config/controlplane.yaml
```

**Step 2.3: Bootstrap Cluster**

```bash
# Bootstrap etcd on first node
talosctl bootstrap --nodes 10.0.0.201 \
  --endpoints 10.0.0.201 \
  --talosconfig /root/cozy-new-config/talosconfig

# Wait for cluster to form
sleep 120

# Get kubeconfig
talosctl kubeconfig --nodes 10.0.0.201 \
  --endpoints 10.0.0.201 \
  --talosconfig /root/cozy-new-config/talosconfig \
  /root/cozy-new-config/kubeconfig

# Verify cluster
export KUBECONFIG=/root/cozy-new-config/kubeconfig
kubectl get nodes
```

**Expected Result**:
```
NAME              STATUS   ROLES           AGE   VERSION
cozy-new-cp1      Ready    control-plane   2m    v1.32.x
cozy-new-cp2      Ready    control-plane   2m    v1.32.x
cozy-new-cp3      Ready    control-plane   2m    v1.32.x
```

#### Day 3: Install CozyStack v0.37.2 (6 hours)

**Step 3.1: Clone CozyStack Repository**

```bash
cd /root
git clone https://github.com/cozystack/cozystack.git cozystack-v0.37.2
cd cozystack-v0.37.2
git checkout v0.37.2

# Verify version
git describe --tags
# Should output: v0.37.2
```

**Step 3.2: Prepare Configuration**

```bash
# Create configuration
cat > /root/cozy-new-config/cozystack-values.yaml <<EOF
bundle: paas-proxmox

# Cluster configuration
cluster-domain: cozy-new.local
root-host: cozy-new.example.com

# Network configuration
ipv4-pod-cidr: 10.244.0.0/16
ipv4-pod-gateway: 10.244.0.1
ipv4-svc-cidr: 10.96.0.0/16
ipv4-join-cidr: 100.64.0.0/16

# API endpoint
api-server-endpoint: https://10.0.0.201:6443

# Disable telemetry if needed
telemetry-enabled: "false"
EOF
```

**Step 3.3: Run CozyStack Installer**

```bash
export KUBECONFIG=/root/cozy-new-config/kubeconfig

# Run installer with paas-proxmox bundle
cd /root/cozystack-v0.37.2

# Method 1: Using installer script
bash scripts/installer.sh \
  --bundle paas-proxmox \
  --config /root/cozy-new-config/cozystack-values.yaml

# OR Method 2: Using Helm directly
kubectl create namespace cozy-system

# Apply CozyStack ConfigMap
kubectl create configmap cozystack \
  --from-file=/root/cozy-new-config/cozystack-values.yaml \
  -n cozy-system

# Install platform chart
helm install cozystack-platform \
  packages/core/platform \
  --namespace cozy-system \
  --create-namespace \
  --set bundle=paas-proxmox \
  --timeout 30m \
  --wait
```

**Step 3.4: Monitor Installation**

```bash
# Watch HelmReleases
watch kubectl get hr -A

# Watch pods
watch kubectl get pods -A

# Check for any issues
kubectl get events -A --sort-by='.lastTimestamp' | tail -50
```

**Expected Duration**: 20-30 minutes for all components to be Ready

**Step 3.5: Verify Installation**

```bash
# Check all HelmReleases
kubectl get hr -A | grep -v True

# Should be empty (all True)

# Check pods
kubectl get pods -A | grep -v Running | grep -v Completed

# Should be minimal or none

# Verify CozyStack components
kubectl get pods -n cozy-system
kubectl get pods -n cozy-cilium
kubectl get pods -n cozy-kubeovn
```

#### Day 4: Configure Proxmox Integration (4 hours)

**Step 4.1: Verify paas-proxmox Bundle Components**

```bash
export KUBECONFIG=/root/cozy-new-config/kubeconfig

# Check installed components
kubectl get hr -A | grep -E 'proxmox|capi|kamaji|linstor'

# Expected components:
# - proxmox-csi (cozy-proxmox-csi namespace)
# - proxmox-ccm (cozy-proxmox-ccm namespace)
# - capi-operator (cozy-cluster-api namespace)
# - capi-providers (includes capmox)
# - kamaji (cozy-kamaji namespace)
# - linstor (cozy-linstor namespace)
# - metallb (cozy-metallb namespace)
```

**Step 4.2: Configure Proxmox Credentials**

```bash
# Create Proxmox API token on Proxmox host
# (if not already created)
pveum user token add capmox@pam capi --privsep 1

# Save token ID and secret
# Token ID: capmox@pam!capi
# Token Secret: <generated-secret>

# Create secret in Kubernetes
kubectl create secret generic proxmox-credentials \
  -n cozy-cluster-api \
  --from-literal=url=https://10.0.0.1:8006/api2/json \
  --from-literal=token_id=capmox@pam!capi \
  --from-literal=token_secret=<token-secret>
```

**Step 4.3: Configure Proxmox CSI**

```bash
# Create CSI configuration
cat > /tmp/proxmox-csi-values.yaml <<EOF
config:
  clusters:
    - url: https://10.0.0.1:8006/api2/json
      insecure: true
      token_id: "capmox@pam!csi"
      token_secret: "<csi-token-secret>"
      region: pve

storageClass:
  - name: proxmox-data-xfs
    storage: local-lvm
    reclaimPolicy: Delete
    fstype: xfs
  - name: proxmox-data-xfs-ssd
    storage: local-ssd
    reclaimPolicy: Delete
    fstype: xfs
EOF

# Apply configuration (if CSI chart supports values)
# Or configure via ConfigMap
```

**Step 4.4: Create ProxmoxCluster**

```bash
# Create ProxmoxCluster for new cluster
kubectl apply -f - <<EOF
apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
kind: ProxmoxCluster
metadata:
  name: cozy-new
  namespace: default
spec:
  allowedNodes:
    - pve
    - mgr
  controlPlaneEndpoint:
    host: 10.0.0.201
    port: 6443
  dnsServers:
    - 10.0.0.1
  ipv4Config:
    addresses:
      - 10.0.0.210-10.0.0.250
    gateway: 10.0.0.1
    prefix: 24
EOF

# Wait for ProxmoxCluster to be Ready
kubectl wait --for=condition=ready proxmoxcluster/cozy-new --timeout=300s
```

**Step 4.5: Verify Proxmox Components**

```bash
# Check capmox controller
kubectl get pods -n capmox-system

# Check Proxmox CSI
kubectl get pods -n cozy-proxmox-csi

# Check Proxmox CCM
kubectl get pods -n cozy-proxmox-ccm

# Check StorageClasses
kubectl get storageclass | grep proxmox

# Check ProxmoxCluster
kubectl get proxmoxclusters -A
```

#### Day 5: Validation (4 hours)

**Step 5.1: Health Check**

```bash
# All nodes Ready
kubectl get nodes

# All pods Running/Completed
kubectl get pods -A | grep -v Running | grep -v Completed

# All HelmReleases Ready
kubectl get hr -A | grep -v True

# All PVCs Bound (should be none yet)
kubectl get pvc -A
```

**Step 5.2: Basic Functionality Tests**

```bash
# Create test namespace
kubectl create namespace test-basic

# Test pod creation
kubectl run test-nginx --image=nginx -n test-basic
kubectl wait --for=condition=ready pod/test-nginx -n test-basic --timeout=60s

# Test service creation
kubectl expose pod test-nginx --port=80 -n test-basic

# Test storage
kubectl apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-pvc
  namespace: test-basic
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 1Gi
  storageClassName: replicated
EOF

kubectl wait --for=condition=bound pvc/test-pvc -n test-basic --timeout=120s

# Cleanup
kubectl delete namespace test-basic
```

**Step 5.3: Document Installation**

```bash
# Save cluster state
kubectl get all,cm,secret,pvc,pv -A -o yaml > /root/cozy-new-config/initial-state.yaml

# Save versions
kubectl get pods -A -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.containers[0].image}{"\n"}' > /root/cozy-new-config/component-versions.txt

# Verify version
kubectl get cm -n cozy-system cozystack -o yaml
```

### Week 2: Proxmox Integration Testing

#### Day 6-7: VM Creation Testing (8 hours)

**Test 1: Create Test ProxmoxMachine**

Already documented in previous test - but now on clean cluster:

```bash
# Create test VM via CAPI (with full workflow)
# This time use Kubernetes CRD if available
kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Kubernetes
metadata:
  name: test-tenant
  namespace: default
spec:
  replicas: 1
  nodeGroups:
    - name: worker
      replicas: 1
      resources:
        cpu: 2
        memory: 4Gi
        disk: 20Gi
EOF

# Monitor tenant cluster creation
watch kubectl get kubernetes test-tenant

# Check if ProxmoxMachine created
watch kubectl get proxmoxmachines -A

# VERIFY IN PROXMOX (critical!)
ssh root@proxmox-host "qm list | grep test"

# Check VM boots and joins
kubectl get machines -A
```

**Test 2: Storage Provisioning**

```bash
# Test Proxmox CSI
kubectl apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-proxmox-storage
  namespace: default
spec:
  accessModes:
    - ReadWriteOnce
  storageClassName: proxmox-data-xfs
  resources:
    requests:
      storage: 10Gi
EOF

# Wait and verify
kubectl wait --for=condition=bound pvc/test-proxmox-storage --timeout=120s

# Check in Proxmox
ssh root@proxmox-host "pvesm list local-lvm"
```

**Test 3: Network Configuration**

```bash
# Test MetalLB
kubectl create deployment test-lb --image=nginx
kubectl expose deployment test-lb --port=80 --type=LoadBalancer

# Check external IP assigned
kubectl get svc test-lb

# Cleanup
kubectl delete deployment test-lb
kubectl delete svc test-lb
```

#### Day 8-9: Advanced Testing (8 hours)

**Test 4: Multi-Node Tenant Cluster**

```bash
# Create larger tenant cluster
kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Kubernetes
metadata:
  name: prod-tenant
  namespace: tenant-prod
spec:
  replicas: 3
  nodeGroups:
    - name: worker
      replicas: 3
      resources:
        cpu: 4
        memory: 8Gi
        disk: 50Gi
EOF

# Monitor creation
watch kubectl get kubernetes prod-tenant -n tenant-prod

# VERIFY: 3 VMs created in Proxmox
ssh root@proxmox-host "qm list | grep prod-tenant"
```

**Test 5: Database Operator**

```bash
# Test PostgreSQL
kubectl create namespace db-test
kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Postgres
metadata:
  name: test-db
  namespace: db-test
spec:
  replicas: 2
  storage: 10Gi
EOF

# Verify
kubectl wait --for=condition=ready postgres/test-db -n db-test --timeout=300s
kubectl get pods -n db-test
```

**Test 6: NexCage LXC Integration**

```bash
# Verify NexCage is running on Proxmox host
ssh root@proxmox-host "systemctl status nexcage"

# Test LXC list via NexCage
ssh root@proxmox-host "nexcage list --runtime lxc"

# Create test LXC container via NexCage
ssh root@proxmox-host "nexcage create --runtime lxc --name test-lxc-db --rootfs /var/lib/lxc/test-lxc-db"

# Test NexCage OCI compatibility
kubectl create namespace lxc-test

# Create pod with LXC runtime annotation (if supported)
kubectl apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: test-lxc-pod
  namespace: lxc-test
  annotations:
    io.kubernetes.cri.runtime: nexcage
spec:
  containers:
  - name: postgres
    image: postgres:15
    env:
    - name: POSTGRES_PASSWORD
      value: test123
EOF

# Verify pod runs via NexCage
kubectl wait --for=condition=ready pod/test-lxc-pod -n lxc-test --timeout=120s

# Check container runtime
kubectl get pod test-lxc-pod -n lxc-test -o jsonpath='{.status.containerStatuses[0].containerID}'

# VERIFY in Proxmox that LXC container was created
ssh root@proxmox-host "pct list | grep test-lxc"

# Cleanup
kubectl delete namespace lxc-test
```

**Expected Result**:
- ✅ NexCage creates LXC container for database pod
- ✅ Pod runs successfully with LXC isolation
- ✅ Container visible in Proxmox `pct list`
- ✅ Database accessible from within cluster

**Test 7: Database on LXC via NexCage**

```bash
# Test PostgreSQL operator with LXC runtime
kubectl create namespace db-lxc-test

kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Postgres
metadata:
  name: test-db-lxc
  namespace: db-lxc-test
  annotations:
    runtime.cozystack.io/type: lxc
    runtime.cozystack.io/backend: nexcage
spec:
  replicas: 1
  storage: 5Gi
  resources:
    cpu: 2
    memory: 4Gi
EOF

# Monitor creation
watch kubectl get postgres test-db-lxc -n db-lxc-test

# Verify pod uses LXC
kubectl get pods -n db-lxc-test -o wide

# Check in Proxmox
ssh root@proxmox-host "pct list | grep test-db-lxc"

# Test database connection
kubectl run -it --rm psql-client --image=postgres:15 --restart=Never -n db-lxc-test -- \
  psql -h test-db-lxc-postgres -U postgres

# Cleanup
kubectl delete namespace db-lxc-test
```

**Test 8: Integration Tests**

```bash
# Run comprehensive integration checks
cd /root/cozystack-v0.37.2
./tests/proxmox-integration/extended-integrity-check.sh

# Test NexCage health
ssh root@proxmox-host "nexcage --version"
ssh root@proxmox-host "nexcage list --runtime lxc"
ssh root@proxmox-host "systemctl is-active nexcage"
```

#### Day 10: Documentation and Handoff (4 hours)

**Document New Cluster**:
```bash
# Create cluster documentation
cat > /root/NEW_CLUSTER_INFO.md <<EOF
# New CozyStack v0.37.2 Cluster

## Details
- Version: v0.37.2
- Bundle: paas-proxmox
- Installed: $(date)
- Nodes: 3 control plane + 1 worker

## Access
- Kubeconfig: /root/cozy-new-config/kubeconfig
- Talosconfig: /root/cozy-new-config/talosconfig

## IPs
- Control Plane 1: 10.0.0.201
- Control Plane 2: 10.0.0.202
- Control Plane 3: 10.0.0.203
- Worker 1: 10.0.0.211

## Proxmox Integration
- ProxmoxCluster: cozy-new (Ready)
- CSI Driver: Configured
- CCM: Running
- Templates: ID 9100 (Talos)
- NexCage: v0.7.3 (LXC runtime)

## Tests Passed
- ✅ VM creation via CAPI
- ✅ Storage provisioning
- ✅ Network configuration
- ✅ Tenant cluster creation
- ✅ Database operators (pods)
- ✅ NexCage LXC integration
- ✅ Database on LXC via NexCage
EOF
```

### Week 3+: Migration Planning

**Create Migration Strategy**:
```bash
# Inventory old cluster
export KUBECONFIG=/root/cozy/mgr-cozy/kubeconfig
kubectl get all -A > /root/old-cluster-inventory.txt

# Identify workloads to migrate
kubectl get deployments,statefulsets,daemonsets -A

# Plan migration sequence
# (To be detailed based on actual workloads)
```

## Quick Start Commands

### For Proxmox on Hetzner (Automated - themoriarti/proxmox-hetzner)

```bash
# Week 0: Automated Proxmox Installation on Hetzner
# Using: https://github.com/themoriarti/proxmox-hetzner

# 1. Boot into Hetzner rescue system
# - Via robot.hetzner.com → Rescue → Activate
# - Reset server

# 2. Download and run automation script
ssh root@YOUR_SERVER_IP
cd /root
wget https://raw.githubusercontent.com/themoriarti/proxmox-hetzner/refs/heads/main/scripts/pve-install.sh
chmod +x pve-install.sh
./pve-install.sh

# 3. Follow interactive prompts:
# - Interface name: eno1 (auto-detected)
# - Hostname: proxmox-cozy
# - FQDN: proxmox-cozy.cp.if.ua
# - Timezone: Europe/Kiev
# - Email: admin@cp.if.ua
# - Private subnet: 10.0.0.0/24
# - Root password: <your-password>

# Script automatically:
# - Downloads latest Proxmox VE ISO
# - Creates autoinstall ISO
# - Installs Proxmox via QEMU/KVM
# - Configures ZFS RAID1
# - Sets up networking (public + vmbr0)
# - Configures repositories
# - Reboots to installed system

# 4. After automatic reboot, verify Proxmox
ssh root@YOUR_SERVER_IP
pveversion
# Access web UI: https://YOUR_SERVER_IP:8006
```

### For Fresh Install (on existing Proxmox)

```bash
# 0. Install NexCage on Proxmox host
ssh root@proxmox-host "apt-get install -y libcap-dev libseccomp-dev libyajl-dev"
ssh root@proxmox-host "cd /opt && git clone https://github.com/CageForge/nexcage.git"
ssh root@proxmox-host "cd /opt/nexcage && zig build && cp zig-out/bin/nexcage /usr/local/bin/"
ssh root@proxmox-host "systemctl enable --now nexcage"

# 1. Prepare VMs in Proxmox
qm clone 9100 1001 --name cozy-new-cp1 --full 1
qm clone 9100 1002 --name cozy-new-cp2 --full 1
qm clone 9100 1003 --name cozy-new-cp3 --full 1
qm start 1001 && qm start 1002 && qm start 1003

# 2. Bootstrap Talos
talosctl gen config cozy-new https://10.0.0.201:6443 \
  --output-dir /root/cozy-new-config
# Apply to nodes...
talosctl bootstrap --nodes 10.0.0.201

# 3. Get kubeconfig
talosctl kubeconfig /root/cozy-new-config/kubeconfig

# 4. Install CozyStack
git clone https://github.com/cozystack/cozystack.git
cd cozystack
git checkout v0.37.2
bash scripts/installer.sh --bundle paas-proxmox

# 5. Verify
kubectl get hr -A
kubectl get pods -A
nexcage version
```

## Success Criteria

### Installation Complete When:

- ✅ All HelmReleases Ready
- ✅ All pods Running/Completed
- ✅ ProxmoxCluster configured and Ready
- ✅ capmox controller running
- ✅ Proxmox CSI/CCM operational
- ✅ Storage classes created
- ✅ Test VM created in Proxmox
- ✅ Basic functionality verified
- ✅ NexCage installed and running

### Integration Complete When:

- ✅ Tenant cluster created (VMs in Proxmox)
- ✅ Storage provisioning working
- ✅ Database operators functional
- ✅ Network connectivity verified
- ✅ All integrity checks pass
- ✅ NexCage LXC runtime tested
- ✅ Database on LXC verified

## Comparison: Old vs New

| Aspect | Old Cluster | New Cluster |
|--------|-------------|-------------|
| **Version** | v0.28.0 | v0.37.2 |
| **Age** | 219 days | Fresh |
| **Bundle** | paas-full | paas-proxmox |
| **Health** | Critical failures | Healthy |
| **VM Management** | KubeVirt (broken) | Proxmox CAPI |
| **Container Runtime** | containerd only | containerd + NexCage |
| **LXC Support** | No | Yes (via NexCage) |
| **Database Isolation** | Pods only | Pods + LXC |
| **Networking** | Broken (52+ days) | Clean |
| **Pods Failing** | 19+ | 0 |
| **Ready for Proxmox** | No | Yes |

## Rollback Plan

**If fresh install fails** (unlikely):
- Old cluster still running
- Can revert to old cluster
- No data lost
- Minimal impact

**Advantage of Option C**: Zero risk to existing cluster

## Timeline Summary

```
Week 0: Hetzner + Proxmox Setup (7 hours)
  Day 0.1: Hetzner preparation (1h)
    - Order server
    - Rescue system activation
    - Hardware verification
  Day 0.2: Automated Proxmox install (4h)
    - Run pve-install.sh script (themoriarti/proxmox-hetzner)
    - Interactive configuration
    - Automatic installation via QEMU/KVM
    - ZFS RAID1 setup
    - Network configuration
    - Repository setup
    - Automatic reboot
  Day 0.3: Post-install config (2h)
    - Upload ISO/templates
    - Security hardening
    - Automation tools (Ansible, Terraform)
    - Backup configuration
  Total: 7 hours

Week 1: CozyStack Installation (25 hours)
  Day 1: Proxmox prep + NexCage install (5h)
  Day 2: K8s bootstrap (6h)
  Day 3: CozyStack install (6h)
  Day 4: Proxmox config (4h)
  Day 5: Validation (4h)
  Total: 25 hours

Week 2: Integration Testing (20 hours)
  Day 6-7: VM testing (8h)
  Day 8-9: Advanced testing + NexCage (8h)
  Day 10: Documentation (4h)
  Total: 20 hours

Overall: 52 hours across 3 weeks (7h + 25h + 20h)
```

**Time Savings**: Using themoriarti/proxmox-hetzner script saves 1 hour
**Compared to Option A**: Would have been 30-44 hours with 30-40% success rate

## Next Immediate Steps

1. ✅ Document plan (this file)
2. ⏳ Order Hetzner server (if needed)
3. ⏳ Automated Proxmox installation on Hetzner
4. ⏳ Install NexCage on Proxmox host
5. ⏳ Create VMs in Proxmox
6. ⏳ Bootstrap Kubernetes
7. ⏳ Install CozyStack v0.37.2
8. ⏳ Test Proxmox integration
9. ⏳ Test NexCage LXC integration

---

**Status**: READY TO EXECUTE  
**Next Action**: Automated Proxmox installation on Hetzner  
**Estimated Start**: Today  
**Estimated Completion**: 3 weeks

## References

- **Hetzner**: https://www.hetzner.com/dedicated-rootserver
- **Hetzner Robot**: https://robot.hetzner.com
- **Proxmox VE**: https://www.proxmox.com/proxmox-ve
- **Proxmox-Hetzner Automation** ⭐: https://github.com/themoriarti/proxmox-hetzner
- **pve-install.sh script**: https://raw.githubusercontent.com/themoriarti/proxmox-hetzner/refs/heads/main/scripts/pve-install.sh
- **NexCage GitHub**: https://github.com/CageForge/nexcage
- **CozyStack**: https://github.com/cozystack/cozystack
- **Proxmox CAPI Provider**: https://github.com/ionos-cloud/cluster-api-provider-proxmox
- **Talos Linux**: https://www.talos.dev
- **Hetzner installimage**: https://docs.hetzner.com/robot/dedicated-server/operating-systems/installimage/

