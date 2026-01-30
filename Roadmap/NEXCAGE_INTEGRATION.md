# NexCage Integration with CozyStack

**Date**: 2025-10-24  
**Project**: Proxmox Integration (#69)  
**Component**: NexCage v0.7.3+  
**Purpose**: Enable LXC containers as Kubernetes pods for database workloads

## Overview

**NexCage** is a next-generation OCI runtime for Proxmox VE that provides hybrid container isolation, supporting both traditional OCI runtimes (crun/runc) and Proxmox LXC containers.

**GitHub**: https://github.com/CageForge/nexcage

### Why NexCage?

1. **LXC as Pod Pattern**: Run database workloads in LXC containers with better isolation than regular pods
2. **Performance**: LXC provides near-native performance for stateful workloads
3. **Resource Efficiency**: Lower overhead than full VMs
4. **Proxmox Native**: Direct integration with Proxmox LXC infrastructure
5. **OCI Compatible**: Seamless integration with Kubernetes CRI

### Use Cases

- **Database-as-a-Service**: PostgreSQL, MySQL, MariaDB in LXC
- **Stateful Applications**: Better isolation for data-intensive workloads
- **Multi-Tenancy**: Enhanced tenant isolation beyond namespaces
- **Hybrid Workloads**: Some pods in containers, some in LXC

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                      Kubernetes Cluster                      │
│                                                              │
│  ┌──────────────────────────────────────────────────────┐  │
│  │                 CozyStack Platform                    │  │
│  │  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  │  │
│  │  │  Database   │  │  Database   │  │  Database   │  │  │
│  │  │  Operators  │  │  Operators  │  │  Operators  │  │  │
│  │  │ (Postgres)  │  │  (MySQL)    │  │  (Redis)    │  │  │
│  │  └─────────────┘  └─────────────┘  └─────────────┘  │  │
│  │         │                 │                 │         │  │
│  │         v                 v                 v         │  │
│  │  ┌──────────────────────────────────────────────┐   │  │
│  │  │    Runtime Selection (annotation-based)      │   │  │
│  │  └──────────────────────────────────────────────┘   │  │
│  │         │                                             │  │
│  │    ┌────┴────┐                                       │  │
│  │    v         v                                       │  │
│  │  Pod      LXC Container                              │  │
│  │  (containerd) (NexCage)                              │  │
│  └──────────────────────────────────────────────────────┘  │
│                     │                                       │
└─────────────────────┼───────────────────────────────────────┘
                      │
                      v
        ┌─────────────────────────────┐
        │      Proxmox VE Host        │
        │                             │
        │  ┌──────────┐  ┌─────────┐ │
        │  │ NexCage  │  │   LXC   │ │
        │  │ Runtime  │→ │ Daemon  │ │
        │  └──────────┘  └─────────┘ │
        │                             │
        │  ┌─────────────────────┐   │
        │  │  LXC Containers     │   │
        │  │  /var/lib/lxc/      │   │
        │  └─────────────────────┘   │
        └─────────────────────────────┘
```

## Installation

### Prerequisites

**On Proxmox Host**:
- Proxmox VE 7.0+ or 8.0+
- LXC support enabled
- Build tools: gcc, make
- Dependencies: libcap-dev, libseccomp-dev, libyajl-dev
- Zig 0.15.1

### Step-by-Step Installation

#### 1. Install Dependencies

```bash
# On Proxmox host
apt-get update
apt-get install -y \
  libcap-dev \
  libseccomp-dev \
  libyajl-dev \
  git \
  build-essential \
  wget \
  xz-utils
```

#### 2. Install Zig 0.15.1

```bash
cd /tmp
wget https://ziglang.org/download/0.15.1/zig-linux-x86_64-0.15.1.tar.xz
tar -xf zig-linux-x86_64-0.15.1.tar.xz
mv zig-linux-x86_64-0.15.1 /opt/zig-0.15.1
ln -s /opt/zig-0.15.1/zig /usr/local/bin/zig

# Verify
zig version
# Output: 0.15.1
```

#### 3. Build NexCage

```bash
cd /opt
git clone https://github.com/CageForge/nexcage.git
cd nexcage

# Checkout stable version
git checkout v0.7.3

# Build
zig build -Doptimize=ReleaseSafe

# Verify build
./zig-out/bin/nexcage version
# Output: NexCage v0.7.3
```

#### 4. Install Binary

```bash
cp zig-out/bin/nexcage /usr/local/bin/nexcage
chmod +x /usr/local/bin/nexcage

# Verify installation
nexcage --help
nexcage version
```

#### 5. Configure NexCage

```bash
# Create config directory
mkdir -p /etc/nexcage

# Create configuration
cat > /etc/nexcage/config.json <<'EOF'
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
  },
  "lxc": {
    "template": "download",
    "distribution": "ubuntu",
    "release": "22.04",
    "architecture": "amd64"
  },
  "security": {
    "apparmor": true,
    "seccomp": true,
    "capabilities": [
      "CAP_NET_ADMIN",
      "CAP_SYS_ADMIN"
    ]
  }
}
EOF
```

#### 6. Create Systemd Service

```bash
cat > /etc/systemd/system/nexcage.service <<'EOF'
[Unit]
Description=NexCage OCI Runtime for LXC
Documentation=https://github.com/CageForge/nexcage
After=network.target lxc.service
Requires=lxc.service

[Service]
Type=notify
ExecStart=/usr/local/bin/nexcage daemon --config /etc/nexcage/config.json
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
RestartSec=5
LimitNOFILE=1048576
LimitNPROC=infinity
LimitCORE=infinity
TasksMax=infinity
Delegate=yes
KillMode=process

# Security
NoNewPrivileges=true
ProtectHome=true
ProtectSystem=strict
ReadWritePaths=/var/lib/lxc /run/nexcage /var/log

[Install]
WantedBy=multi-user.target
EOF
```

#### 7. Enable and Start Service

```bash
systemctl daemon-reload
systemctl enable nexcage
systemctl start nexcage

# Check status
systemctl status nexcage

# Should output:
# ● nexcage.service - NexCage OCI Runtime for LXC
#    Loaded: loaded (/etc/systemd/system/nexcage.service; enabled)
#    Active: active (running)
```

#### 8. Verify Installation

```bash
# Check binary
nexcage version

# Check service
systemctl is-active nexcage

# Check socket
ls -la /run/nexcage/nexcage.sock

# List LXC containers
nexcage list --runtime lxc

# Check logs
journalctl -u nexcage -f
```

## Kubernetes Integration

### Configure Kubernetes to Use NexCage

#### Option 1: Per-Pod Runtime Selection (Recommended)

Use annotations to select NexCage runtime for specific pods:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: postgres-lxc
  annotations:
    io.kubernetes.cri.runtime: nexcage
    nexcage.runtime.backend: lxc
spec:
  containers:
  - name: postgres
    image: postgres:15
    env:
    - name: POSTGRES_PASSWORD
      value: secret
```

#### Option 2: RuntimeClass

Create a RuntimeClass for NexCage:

```yaml
apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata:
  name: nexcage-lxc
handler: nexcage
```

Use in pod spec:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: postgres-lxc
spec:
  runtimeClassName: nexcage-lxc
  containers:
  - name: postgres
    image: postgres:15
```

### Database Operator Integration

Modify database operators to support LXC runtime:

#### PostgreSQL with LXC

```yaml
apiVersion: apps.cozystack.io/v1alpha1
kind: Postgres
metadata:
  name: prod-db
  namespace: databases
  annotations:
    runtime.cozystack.io/type: lxc
    runtime.cozystack.io/backend: nexcage
spec:
  replicas: 2
  storage: 100Gi
  resources:
    cpu: 4
    memory: 8Gi
  version: "15"
```

#### MySQL with LXC

```yaml
apiVersion: apps.cozystack.io/v1alpha1
kind: MySQL
metadata:
  name: prod-mysql
  namespace: databases
  annotations:
    runtime.cozystack.io/type: lxc
    runtime.cozystack.io/backend: nexcage
spec:
  replicas: 3
  storage: 200Gi
  resources:
    cpu: 8
    memory: 16Gi
```

## Testing

### Basic Functionality Tests

#### Test 1: NexCage Binary

```bash
ssh root@proxmox-host "nexcage version"
ssh root@proxmox-host "nexcage --help"
```

#### Test 2: Service Status

```bash
ssh root@proxmox-host "systemctl status nexcage"
ssh root@proxmox-host "systemctl is-active nexcage"
```

#### Test 3: LXC List

```bash
ssh root@proxmox-host "nexcage list --runtime lxc"
ssh root@proxmox-host "pct list"
```

#### Test 4: Create Test LXC Container

```bash
ssh root@proxmox-host "nexcage create --runtime lxc --name test-container"
ssh root@proxmox-host "pct list | grep test-container"
ssh root@proxmox-host "nexcage delete --name test-container"
```

### Kubernetes Integration Tests

#### Test 5: Pod with NexCage Runtime

```bash
kubectl create namespace nexcage-test

kubectl apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: test-nginx-lxc
  namespace: nexcage-test
  annotations:
    io.kubernetes.cri.runtime: nexcage
spec:
  containers:
  - name: nginx
    image: nginx:latest
EOF

# Wait for pod
kubectl wait --for=condition=ready pod/test-nginx-lxc -n nexcage-test --timeout=120s

# Check runtime
kubectl get pod test-nginx-lxc -n nexcage-test -o jsonpath='{.status.containerStatuses[0].containerID}'

# Verify LXC on Proxmox
ssh root@proxmox-host "pct list | grep test-nginx"

# Cleanup
kubectl delete namespace nexcage-test
```

#### Test 6: PostgreSQL on LXC

```bash
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
  storage: 10Gi
  resources:
    cpu: 2
    memory: 4Gi
EOF

# Monitor
watch kubectl get postgres test-db-lxc -n db-lxc-test

# Verify pod
kubectl get pods -n db-lxc-test

# Verify LXC
ssh root@proxmox-host "pct list | grep test-db-lxc"

# Test connection
kubectl run -it --rm psql-client \
  --image=postgres:15 \
  --restart=Never \
  -n db-lxc-test -- \
  psql -h test-db-lxc-postgres -U postgres

# Cleanup
kubectl delete namespace db-lxc-test
```

## Troubleshooting

### Common Issues

#### 1. NexCage Service Not Starting

**Symptoms**:
```
systemctl status nexcage
● nexcage.service - failed to start
```

**Solutions**:
```bash
# Check logs
journalctl -u nexcage -n 50

# Check dependencies
systemctl status lxc.service

# Check permissions
ls -la /var/lib/lxc
ls -la /run/nexcage

# Verify config
nexcage daemon --config /etc/nexcage/config.json --dry-run
```

#### 2. Socket Not Created

**Symptoms**:
```bash
ls /run/nexcage/nexcage.sock
# No such file or directory
```

**Solutions**:
```bash
# Check service logs
journalctl -u nexcage -f

# Verify directory exists
mkdir -p /run/nexcage
chown root:root /run/nexcage
chmod 755 /run/nexcage

# Restart service
systemctl restart nexcage
```

#### 3. Kubernetes Pods Not Using NexCage

**Symptoms**:
```bash
kubectl get pod -o jsonpath='{.status.containerStatuses[0].containerID}'
# Shows containerd:// instead of nexcage://
```

**Solutions**:
```bash
# Check annotation
kubectl get pod <pod-name> -o yaml | grep annotations -A 5

# Verify RuntimeClass
kubectl get runtimeclass nexcage-lxc

# Check kubelet config
ssh <node> "cat /var/lib/kubelet/config.yaml"

# Check CRI socket
ssh <node> "ls -la /run/nexcage/nexcage.sock"
```

#### 4. LXC Container Not Visible in Proxmox

**Symptoms**:
```bash
pct list
# Container not shown
```

**Solutions**:
```bash
# Check LXC path
ls -la /var/lib/lxc/

# Verify NexCage config
cat /etc/nexcage/config.json | jq .lxc_path

# Check container logs
nexcage logs --name <container-name>

# List via NexCage
nexcage list --runtime lxc
```

### Debug Mode

Enable debug logging:

```bash
# Edit config
vi /etc/nexcage/config.json

# Change logging level
{
  "logging": {
    "level": "debug",
    "file": "/var/log/nexcage.log"
  }
}

# Restart service
systemctl restart nexcage

# Watch logs
tail -f /var/log/nexcage.log
```

## Performance Considerations

### LXC vs Pod Comparison

| Metric | Regular Pod | LXC (NexCage) | Improvement |
|--------|-------------|---------------|-------------|
| **Startup Time** | ~2-5s | ~5-8s | -40% slower |
| **Memory Overhead** | ~50MB | ~30MB | 40% less |
| **CPU Overhead** | 2-5% | 1-2% | 50% less |
| **I/O Performance** | 85-90% native | 95-98% native | 10% better |
| **Network Latency** | +0.1ms | +0.05ms | 50% better |
| **Isolation** | namespaces | LXC | Stronger |

### When to Use LXC

**Use LXC (via NexCage) for**:
- ✅ Databases (PostgreSQL, MySQL, MariaDB)
- ✅ Stateful applications
- ✅ I/O intensive workloads
- ✅ Applications needing stronger isolation
- ✅ Multi-tenant environments

**Use Regular Pods for**:
- ✅ Stateless applications
- ✅ Quick startup required
- ✅ Microservices
- ✅ Batch jobs
- ✅ Standard web applications

## Monitoring

### NexCage Metrics

```bash
# Service status
systemctl status nexcage

# Container count
nexcage list --runtime lxc | wc -l

# Resource usage
pct status <container-id>

# Logs
journalctl -u nexcage --since "1 hour ago"
```

### Prometheus Integration

NexCage exposes metrics on `/metrics` endpoint (if configured):

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: nexcage
spec:
  selector:
    matchLabels:
      app: nexcage
  endpoints:
  - port: metrics
    interval: 30s
```

## Security

### Best Practices

1. **AppArmor Profiles**: Enable for all LXC containers
2. **Seccomp Filters**: Use strict filters for databases
3. **Capabilities**: Drop all unnecessary capabilities
4. **Network Policies**: Restrict database access
5. **Volume Permissions**: Use least privilege

### Example Secure Configuration

```json
{
  "security": {
    "apparmor": true,
    "apparmor_profile": "lxc-container-default-cgns",
    "seccomp": true,
    "seccomp_profile": "default",
    "capabilities": [
      "CAP_NET_BIND_SERVICE",
      "CAP_DAC_OVERRIDE"
    ],
    "no_new_privileges": true,
    "read_only_rootfs": false
  }
}
```

## Roadmap

### Current Status

- ✅ LXC backend support
- ✅ OCI compatibility
- ✅ Kubernetes CRI integration
- ✅ Basic resource management
- ⏳ Advanced networking (in progress)

### Future Plans

- ⏳ GPU support for LXC containers
- ⏳ Live migration support
- ⏳ Snapshot/restore functionality
- ⏳ Advanced monitoring
- ⏳ Multi-architecture support

## References

- **NexCage GitHub**: https://github.com/CageForge/nexcage
- **LXC Documentation**: https://linuxcontainers.org/lxc/
- **Proxmox LXC**: https://pve.proxmox.com/wiki/Linux_Container
- **OCI Runtime Spec**: https://github.com/opencontainers/runtime-spec
- **Kubernetes CRI**: https://kubernetes.io/docs/concepts/architecture/cri/

---

**Status**: Installation documented  
**Next**: Install NexCage on Proxmox host  
**Timeline**: Day 1 of fresh installation

