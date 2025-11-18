# System Integrity Checks - Usage Guide

## 🎯 Purpose

These tools provide comprehensive validation of Proxmox integration with CozyStack, checking all components and configurations.

## 📋 Prerequisites

### On the Server Running Checks

**Required**:
- `kubectl` installed and configured
- Access to Kubernetes cluster (KUBECONFIG)
- `python3` (3.8+)
- `curl` command
- `bash` shell

**Optional**:
- `jq` for JSON parsing
- Python `requests` library

### Quick Setup

```bash
# On mgr.cp.if.ua server
cd /opt/proxmox-integration

# Copy test files
cp -r /path/to/cozystack/tests/proxmox-integration/* .

# Install Python dependencies
pip3 install requests

# Make scripts executable
chmod +x *.sh *.py

# Set kubeconfig
export KUBECONFIG=/root/cozy/mgr-cozy/kubeconfig
```

## 🚀 Running Integrity Checks

### Method 1: From Server (Recommended)

```bash
# SSH to the cluster server
ssh root@mgr.cp.if.ua

# Set kubeconfig
export KUBECONFIG=/root/cozy/mgr-cozy/kubeconfig

# Run complete integrity check
cd /path/to/tests/proxmox-integration
./run-integrity-checks.sh
```

### Method 2: Remote Execution

```bash
# From your local machine
ssh root@mgr.cp.if.ua "export KUBECONFIG=/root/cozy/mgr-cozy/kubeconfig && cd /path/to/tests/proxmox-integration && ./run-integrity-checks.sh"
```

### Method 3: Individual Check Scripts

```bash
# Run quick shell check (2-3 min)
ssh root@mgr.cp.if.ua "export KUBECONFIG=/root/cozy/mgr-cozy/kubeconfig && /path/to/system-integrity-check.sh"

# Run Python comprehensive check (3-5 min)
ssh root@mgr.cp.if.ua "export KUBECONFIG=/root/cozy/mgr-cozy/kubeconfig && python3 /path/to/integrity_checker.py"
```

## 📊 Sample Output

### Successful Check

```
╔══════════════════════════════════════════════════════════╗
║  PROXMOX INTEGRATION SYSTEM INTEGRITY CHECKER           ║
╚══════════════════════════════════════════════════════════╝

Started: 2025-10-24 16:30:00
Kubeconfig: /root/cozy/mgr-cozy/kubeconfig

============================================================
1. Kubernetes Cluster Health
============================================================

⏳ Checking: Kubernetes API connectivity
✅ PASS: Kubernetes API is accessible
ℹ️  INFO: Kubernetes control plane is running at https://10.0.0.40:6443

⏳ Checking: Node status
✅ PASS: All 4 nodes are Ready

⏳ Checking: Proxmox worker node
✅ PASS: Proxmox worker node(s) found: 1
ℹ️  INFO:   Node: mgr.cp.if.ua, OS: Debian GNU/Linux 13, Kernel: 6.14.11-2-pve

============================================================
2. Cluster API Components
============================================================

⏳ Checking: CAPI namespace
✅ PASS: CAPI namespace exists

⏳ Checking: CAPI controller pods
✅ PASS: CAPI controllers running: 4/5

⏳ Checking: Proxmox CAPI provider (capmox)
✅ PASS: Proxmox CAPI provider running: 1 pod(s)

⏳ Checking: Proxmox CRDs
✅ PASS: All 4 Proxmox CRDs installed

⏳ Checking: ProxmoxCluster resources
✅ PASS: All ProxmoxCluster resources Ready: 1/1

[... more checks ...]

============================================================
INTEGRITY CHECK SUMMARY
============================================================

Total Checks: 50
Passed: 48
Failed: 0
Warnings: 2
Success Rate: 96%

✅ OVERALL STATUS: HEALTHY
Proxmox integration is fully operational!
```

### Failed Check

```
============================================================
2. Cluster API Components
============================================================

⏳ Checking: CAPI controller pods
❌ FAIL: No CAPI controller pods are running

============================================================
INTEGRITY CHECK SUMMARY
============================================================

Total Checks: 50
Passed: 35
Failed: 8
Warnings: 7
Success Rate: 70%

❌ OVERALL STATUS: CRITICAL
Proxmox integration has critical issues!
```

## 🔧 What Each Check Validates

### Kubernetes Checks (6)
1. ✅ API server responding
2. ✅ Authentication working
3. ✅ All nodes Ready
4. ✅ Control plane healthy
5. ✅ Worker nodes present
6. ✅ Proxmox worker identified

### CAPI Checks (8)
1. ✅ Namespace exists
2. ✅ Controller pods running
3. ✅ Core provider operational
4. ✅ Bootstrap provider working
5. ✅ Control plane provider running
6. ✅ Proxmox provider (capmox) operational
7. ✅ ProxmoxCluster resources Ready
8. ✅ CRDs installed

### Network Checks (7)
1. ✅ CoreDNS running
2. ✅ DNS resolution working
3. ✅ Cilium agents running
4. ✅ Kube-OVN controller operational
5. ✅ Pod IP allocation working
6. ✅ Service networking functional
7. ✅ MetalLB operational

### Storage Checks (5)
1. ✅ CSI drivers installed
2. ✅ Storage classes configured
3. ✅ LINSTOR operational
4. ✅ PV provisioning works
5. ✅ Volume mounting functional

### Proxmox Checks (10)
1. ✅ Host reachable
2. ✅ API port open (8006)
3. ✅ Authentication successful
4. ✅ API version retrievable
5. ✅ User permissions adequate
6. ✅ Nodes accessible
7. ✅ Storage pools configured
8. ✅ VMs manageable
9. ✅ Templates available
10. ✅ Network bridges configured

### Monitoring Checks (4)
1. ✅ Prometheus deployed
2. ✅ Grafana deployed
3. ✅ Metrics collection working
4. ✅ Dashboards available

### Security Checks (3)
1. ✅ Service accounts configured
2. ✅ Secrets properly stored
3. ✅ RBAC permissions set

### Health Checks (7)
1. ✅ No pods in error states
2. ✅ No critical events
3. ✅ Resource utilization healthy
4. ✅ No pending pods (>5 min)
5. ✅ No unknown pods
6. ✅ No crash loops
7. ✅ All deployments ready

**Total: 50+ comprehensive checks**

## 🎓 Troubleshooting Guide

### Issue: "kubectl: command not found"
**Solution**: Run on server with kubectl installed
```bash
ssh root@mgr.cp.if.ua
export KUBECONFIG=/root/cozy/mgr-cozy/kubeconfig
./run-integrity-checks.sh
```

### Issue: "Cannot connect to Kubernetes API"
**Solution**: Check kubeconfig path
```bash
echo $KUBECONFIG
cat $KUBECONFIG | head -10
kubectl cluster-info
```

### Issue: "Proxmox credentials not found"
**Solution**: Check secret exists
```bash
kubectl get secret capmox-credentials -n capmox-system
kubectl describe secret capmox-credentials -n capmox-system
```

### Issue: "Multiple checks failing"
**Solution**: Run recovery procedures
```bash
# See RECOVERY_SUCCESS.md for procedures
# Or run emergency fix:
cd /path/to/cozystack/Roadmap
cat RECOVERY_SUCCESS.md
```

## 📈 Continuous Monitoring

### Setup Cron Job

```bash
# On mgr.cp.if.ua
crontab -e

# Add daily check at 9 AM
0 9 * * * export KUBECONFIG=/root/cozy/mgr-cozy/kubeconfig && /path/to/run-integrity-checks.sh > /var/log/proxmox-daily-check.log 2>&1

# Add weekly comprehensive check Sunday 6 AM
0 6 * * 0 export KUBECONFIG=/root/cozy/mgr-cozy/kubeconfig && /path/to/run-integrity-checks.sh > /var/log/proxmox-weekly-check.log 2>&1
```

### Alert on Failure

```bash
# Send email on failure
0 9 * * * export KUBECONFIG=/root/cozy/mgr-cozy/kubeconfig && /path/to/run-integrity-checks.sh || echo "Integrity check failed at $(date)" | mail -s "Proxmox Integration Alert" admin@example.com
```

---

**Quick Command Reference**:

```bash
# Full check on server
ssh root@mgr.cp.if.ua "export KUBECONFIG=/root/cozy/mgr-cozy/kubeconfig && cd /opt/proxmox-integration && ./run-integrity-checks.sh"

# Quick check
ssh root@mgr.cp.if.ua "export KUBECONFIG=/root/cozy/mgr-cozy/kubeconfig && /opt/proxmox-integration/system-integrity-check.sh"

# Python check
ssh root@mgr.cp.if.ua "export KUBECONFIG=/root/cozy/mgr-cozy/kubeconfig && python3 /opt/proxmox-integration/integrity_checker.py"
```

---

**Last Updated**: 2025-10-24  
**Version**: 1.0.0
