# Proxmox Integration System Integrity Checks

## 🎯 Overview

Comprehensive system integrity checking tools for validating Proxmox VE integration with CozyStack. These tools verify all components, configurations, and functionality of the integration.

## 📋 Available Integrity Checks

### 1. Quick Integrity Check (Shell-based)
**File**: `system-integrity-check.sh`  
**Duration**: 2-3 minutes  
**Checks**: 30+ component checks

**What it checks**:
- Kubernetes cluster health
- Node status and readiness
- Proxmox API connectivity
- CAPI components
- Network stack (CoreDNS, Cilium, Kube-OVN)
- Storage components
- Proxmox-specific resources
- Workload health

**Usage**:
```bash
./system-integrity-check.sh
```

**Exit codes**:
- `0` - All checks passed (Healthy)
- `1` - Some warnings (Degraded but functional)
- `2` - Critical failures (Needs attention)

### 2. Comprehensive Integrity Check (Python)
**File**: `integrity_checker.py`  
**Duration**: 3-5 minutes  
**Checks**: 40+ detailed validations

**What it checks**:
- Kubernetes API and authentication
- All node statuses with details
- Proxmox worker node identification
- Full CAPI component validation
- Proxmox CAPI provider health
- CRD installation verification
- ProxmoxCluster resource status
- Network stack health (DNS, CNI, OVN)
- Storage stack (CSI, StorageClasses)
- Direct Proxmox API testing
- Monitoring stack (Prometheus, Grafana)
- Workload health across cluster

**Usage**:
```bash
export KUBECONFIG=/path/to/kubeconfig
python3 integrity_checker.py
```

### 3. Complete Integrity Suite
**File**: `run-integrity-checks.sh`  
**Duration**: 5-8 minutes  
**Checks**: All of the above + summary

**What it includes**:
- Runs shell-based checks
- Runs Python-based checks
- Direct Proxmox API validation
- Comprehensive component summary
- Detailed logging

**Usage**:
```bash
export KUBECONFIG=/path/to/kubeconfig
./run-integrity-checks.sh
```

**Output**:
- Console output with color-coded results
- Detailed log file in `logs/integrity-check-TIMESTAMP.log`
- Component status summary table
- Final health assessment

## 🔍 Check Categories

### Category 1: Kubernetes Cluster (6 checks)
1. ✅ API server connectivity
2. ✅ Node status (all nodes Ready)
3. ✅ Proxmox worker node presence
4. ✅ Control plane health
5. ✅ Cluster version compatibility
6. ✅ Basic cluster functionality

### Category 2: Cluster API (8 checks)
1. ✅ CAPI namespace existence
2. ✅ CAPI controller pods running
3. ✅ CAPI core provider health
4. ✅ CAPI bootstrap provider
5. ✅ CAPI control plane provider
6. ✅ Proxmox CAPI provider (capmox)
7. ✅ ProxmoxCluster resources
8. ✅ ProxmoxMachine resources

### Category 3: Proxmox CRDs (5 checks)
1. ✅ proxmoxclusters.infrastructure.cluster.x-k8s.io
2. ✅ proxmoxmachines.infrastructure.cluster.x-k8s.io
3. ✅ proxmoxclustertemplates.infrastructure.cluster.x-k8s.io
4. ✅ proxmoxmachinetemplates.infrastructure.cluster.x-k8s.io
5. ✅ CRD versions and status

### Category 4: Network Stack (7 checks)
1. ✅ CoreDNS running and functional
2. ✅ DNS resolution working
3. ✅ Cilium CNI operational
4. ✅ Kube-OVN controller running
5. ✅ Pod networking (IP allocation)
6. ✅ Service networking
7. ✅ MetalLB load balancer

### Category 5: Storage Stack (5 checks)
1. ✅ Proxmox CSI driver installed
2. ✅ CSI driver pods running
3. ✅ Storage classes configured
4. ✅ LINSTOR integration
5. ✅ PV/PVC functionality

### Category 6: Proxmox API (6 checks)
1. ✅ Proxmox host reachability
2. ✅ API port accessibility (8006)
3. ✅ API authentication
4. ✅ API version retrieval
5. ✅ Proxmox permissions
6. ✅ API response time

### Category 7: Proxmox Resources (4 checks)
1. ✅ Proxmox credentials secret
2. ✅ ProxmoxCluster status
3. ✅ Proxmox nodes
4. ✅ Proxmox storage pools

### Category 8: Monitoring (4 checks)
1. ✅ Prometheus deployment
2. ✅ Grafana deployment
3. ✅ Metrics collection
4. ✅ Alerting configuration

### Category 9: Security (3 checks)
1. ✅ Service accounts
2. ✅ Secrets management
3. ✅ RBAC configuration

### Category 10: Workload Health (2 checks)
1. ✅ Pods in error states
2. ✅ Recent events analysis

**Total**: 50+ comprehensive checks

## 🚀 Quick Start

### Run All Checks (Recommended)
```bash
cd /path/to/cozystack/tests/proxmox-integration

# Set kubeconfig
export KUBECONFIG=/root/cozy/mgr-cozy/kubeconfig

# Run complete suite
./run-integrity-checks.sh
```

### Run Quick Check
```bash
# Just the essential checks (2-3 minutes)
./system-integrity-check.sh
```

### Run Detailed Check
```bash
# Comprehensive validation (3-5 minutes)
python3 integrity_checker.py
```

## 📊 Understanding Results

### Exit Codes

#### 0 - HEALTHY ✅
- All critical components operational
- Fewer than 5 warnings
- Integration fully functional
- **Action**: None required

#### 1 - DEGRADED ⚠️
- Core functionality working
- Some non-critical issues
- 1-2 failed checks
- **Action**: Review warnings, plan fixes

#### 2 - CRITICAL ❌
- Multiple critical failures
- Integration may not work
- 3+ failed checks
- **Action**: Immediate attention required

### Status Indicators

- **✅ PASS** - Check passed successfully
- **⚠️ WARN** - Check passed with warnings
- **❌ FAIL** - Check failed
- **ℹ️ INFO** - Informational message

## 📝 Check Scheduling

### Daily Health Check
```bash
# Run basic check daily
0 9 * * * /path/to/system-integrity-check.sh > /var/log/proxmox-daily-check.log 2>&1
```

### Weekly Comprehensive Check
```bash
# Run full check weekly
0 6 * * 0 /path/to/run-integrity-checks.sh > /var/log/proxmox-weekly-check.log 2>&1
```

### Pre-Deployment Validation
```bash
# Run before any major changes
./run-integrity-checks.sh
if [ $? -eq 0 ]; then
    echo "Safe to proceed with deployment"
else
    echo "Fix issues before deploying"
    exit 1
fi
```

## 🔧 Customization

### Configuration File
Create `config.env` with your settings:

```bash
# Kubernetes
export KUBECONFIG="/path/to/kubeconfig"

# Proxmox (auto-detected from cluster secrets)
export PROXMOX_HOST="10.0.0.1"
export PROXMOX_PORT="8006"

# Thresholds
export MAX_ERROR_PODS="5"
export MAX_WARNING_EVENTS="10"
export MIN_READY_NODES="3"

# Features to check
export CHECK_MONITORING="true"
export CHECK_STORAGE="true"
export CHECK_CAPI="true"
```

### Adding Custom Checks

#### In shell script:
```bash
# Add to system-integrity-check.sh
print_check "My custom check"
if my_check_command; then
    print_success "Check passed"
else
    print_fail "Check failed"
fi
```

#### In Python script:
```python
# Add to integrity_checker.py
def check_my_feature(self):
    """Check my custom feature"""
    self.print_check("My custom check")
    
    rc, stdout, stderr = self.kubectl('get', 'resource')
    if rc == 0:
        self.print_success("Check passed")
    else:
        self.print_fail("Check failed")
```

## 📈 Monitoring Integration

### Prometheus Metrics
The integrity checker can export metrics to Prometheus:

```yaml
# Add to prometheus config
- job_name: 'proxmox-integrity'
  static_configs:
    - targets: ['localhost:9090']
  metrics_path: '/metrics'
  scrape_interval: 5m
```

### Alerting Rules
```yaml
# Alert if integrity check fails
- alert: ProxmoxIntegrityCheckFailed
  expr: proxmox_integrity_status != 0
  for: 10m
  annotations:
    summary: "Proxmox integration integrity check failed"
    description: "The integrity check returned non-zero exit code"
```

## 🐛 Troubleshooting

### Common Issues

#### "Cannot connect to Kubernetes API"
```bash
# Check kubeconfig
export KUBECONFIG=/path/to/your/kubeconfig
kubectl cluster-info

# Verify connectivity
ping <cluster-ip>
telnet <cluster-ip> 6443
```

#### "Proxmox credentials not found"
```bash
# Check secret exists
kubectl get secret capmox-credentials -n capmox-system

# Recreate if missing
kubectl create secret generic capmox-credentials \
  -n capmox-system \
  --from-literal=PROXMOX_ENDPOINT='https://10.0.0.1:8006' \
  --from-literal=PROXMOX_USER='capmox@pam' \
  --from-literal=PROXMOX_PASSWORD='<password>'
```

#### "Python dependencies missing"
```bash
# Install requirements
pip3 install -r requirements.txt

# Or install individually
pip3 install requests kubernetes
```

### Debug Mode

#### Shell script:
```bash
# Enable debug output
set -x
./system-integrity-check.sh
set +x
```

#### Python script:
```bash
# Verbose mode
python3 -v integrity_checker.py

# Debug specific section
python3 -c "
from integrity_checker import IntegrityChecker
checker = IntegrityChecker()
checker.check_proxmox_api()
"
```

## 📊 Expected Results

### Healthy System
```
Total Checks: 50
Passed: 48
Failed: 0
Warnings: 2
Success Rate: 96%
✅ OVERALL STATUS: HEALTHY
```

### Typical Warnings (Acceptable)
- ImagePullBackOff on some pods (if redundant)
- Some monitoring components not installed
- Optional features not enabled

### Critical Failures (Need Fix)
- CoreDNS not running
- Kube-OVN controller down
- CAPI controllers failing
- Proxmox API unreachable

## 🎯 Best Practices

### Regular Checks
1. **Daily**: Run quick check (system-integrity-check.sh)
2. **Weekly**: Run full suite (run-integrity-checks.sh)
3. **Before changes**: Always run integrity check
4. **After recovery**: Verify with full suite
5. **Monthly**: Review trends and patterns

### Log Management
```bash
# Keep last 30 days of logs
find logs/ -name "integrity-check-*.log" -mtime +30 -delete

# Compress old logs
find logs/ -name "*.log" -mtime +7 -exec gzip {} \;

# Archive monthly
tar -czf integrity-logs-$(date +%Y%m).tar.gz logs/*.log.gz
```

### Continuous Monitoring
```bash
# Run every hour and alert on failure
0 * * * * /path/to/run-integrity-checks.sh || echo "Integrity check failed!" | mail -s "Alert" admin@example.com
```

## 📚 Integration with CI/CD

### GitHub Actions
```yaml
name: Proxmox Integrity Check
on:
  schedule:
    - cron: '0 */6 * * *'  # Every 6 hours
  workflow_dispatch:

jobs:
  integrity-check:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      - name: Run Integrity Check
        run: |
          cd tests/proxmox-integration
          ./run-integrity-checks.sh
```

### GitLab CI
```yaml
proxmox-integrity:
  stage: test
  script:
    - cd tests/proxmox-integration
    - ./run-integrity-checks.sh
  only:
    - schedules
  artifacts:
    paths:
      - tests/proxmox-integration/logs/
    when: always
```

## 🔗 Related Documentation

- [PROXMOX_TESTING_PLAN.md](./PROXMOX_TESTING_PLAN.md) - Full testing plan
- [COMPLETE_INTEGRATION_GUIDE.md](./COMPLETE_INTEGRATION_GUIDE.md) - Integration guide
- [PROXMOX_INTEGRATION_RUNBOOK.md](../../Roadmap/PROXMOX_INTEGRATION_RUNBOOK.md) - Operational runbook

## 📞 Support

If integrity checks fail:
1. Review the log file for specific failures
2. Check [RECOVERY_SUCCESS.md](../../Roadmap/RECOVERY_SUCCESS.md) for recovery procedures
3. Consult [CRITICAL_CLUSTER_STATE.md](../../Roadmap/CRITICAL_CLUSTER_STATE.md) for emergency procedures
4. Contact team via Slack #proxmox-integration

---

**Last Updated**: 2025-10-24  
**Version**: 1.0.0  
**Author**: CozyStack Team

**Recommendation**: Run integrity checks daily for production systems!
