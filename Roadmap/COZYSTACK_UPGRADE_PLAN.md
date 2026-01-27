# CozyStack Incremental Upgrade Plan

**Date Created**: 2025-10-24  
**Task**: Upgrade CozyStack to latest version with incremental approach  
**Priority**: High (before Proxmox integration completion)  
**Status**: Planning

## Overview

Upgrade CozyStack cluster from current version to latest stable release (v0.37.2) using incremental, version-by-version approach to minimize risks and ensure stability.

## Current State Assessment

### Cluster Information
- **Cluster**: mgr.cp.if.ua
- **Bundle**: paas-full (needs migration to paas-proxmox)
- **Namespace**: cozy-system
- **Age**: ~219 days (since March 2025)

### Version Detection Needed
```bash
# Check current version
kubectl get cm -n cozy-system cozystack -o yaml
kubectl get hr -n cozy-system -o wide
kubectl version --short

# Check installed components
kubectl get pods -n cozy-system
kubectl get hr -A
```

## Available Versions

### Latest Releases (from git tags)
```
v0.34.x series: v0.34.1 → v0.34.8
v0.35.x series: v0.35.0 → v0.35.5
v0.36.x series: v0.36.0 → v0.36.2
v0.37.x series: v0.37.0 → v0.37.2 (latest)
```

### Changelog Locations
- `/home/moriarti/repo/cozystack/docs/changelogs/`
- Available changelogs: v0.31.0 → v0.35.2
- Missing: v0.35.3+, v0.36.x, v0.37.x

## Upgrade Strategy

### Principles

1. **Incremental Upgrades**
   - Never skip major or minor versions
   - Go version by version: 0.34.x → 0.35.x → 0.36.x → 0.37.x
   - Within series, can skip patches (e.g., 0.35.0 → 0.35.5)

2. **Validation at Each Step**
   - Verify all components healthy before next upgrade
   - Run integrity checks
   - Test critical workloads

3. **Rollback Plan**
   - Backup before each upgrade
   - Document rollback procedure
   - Keep previous version available

4. **Testing Sequence**
   - Dev/Test environment first (if available)
   - Management cluster
   - Tenant clusters

### Upgrade Path

```
Current Version (TBD)
    ↓
v0.35.5 (if < 0.35.5)
    ↓
v0.36.2
    ↓
v0.37.2 (target)
```

## Pre-Upgrade Checklist

### 1. Backup Current State

```bash
# Backup CozyStack configuration
kubectl get cm -n cozy-system cozystack -o yaml > backup/cozystack-cm.yaml

# Backup all HelmReleases
kubectl get hr -A -o yaml > backup/helmreleases.yaml

# Backup CRDs
kubectl get crd -o yaml > backup/crds.yaml

# Backup critical namespaces
for ns in cozy-system cozy-cluster-api capmox-system; do
  kubectl get all,cm,secret,pvc -n $ns -o yaml > backup/$ns.yaml
done

# Backup ETCD (if accessible)
etcdctl snapshot save backup/etcd-snapshot.db

# Backup Proxmox configuration
pvesh get /cluster/backup --output-format=yaml > backup/proxmox-backup.yaml
```

### 2. Document Current State

```bash
# Component versions
kubectl get pods -A -o jsonpath='{range .items[*]}{.metadata.namespace}{"\t"}{.metadata.name}{"\t"}{.spec.containers[*].image}{"\n"}' > current-versions.txt

# Cluster resources
kubectl get nodes -o wide > current-nodes.txt
kubectl get pv,pvc -A > current-storage.txt
kubectl top nodes > current-resources.txt

# Network configuration
kubectl get svc,ep -A > current-network.txt
kubectl get networkpolicies -A > current-policies.txt
```

### 3. Health Check

```bash
# Run comprehensive health check
./tests/proxmox-integration/extended-integrity-check.sh

# Check critical pods
kubectl get pods -A | grep -v Running | grep -v Completed

# Check PVC status
kubectl get pvc -A | grep -v Bound

# Check nodes
kubectl get nodes
```

### 4. Notify Stakeholders

- Inform users about planned maintenance
- Schedule maintenance window
- Prepare communication channels

## Upgrade Procedure

### Phase 1: Preparation (2 hours)

**Step 1.1: Determine Current Version**
```bash
# Check multiple sources
kubectl get cm -n cozy-system cozystack -o jsonpath='{.data.version}'
kubectl get hr -n cozy-system cozy-proxy -o jsonpath='{.status.lastAppliedRevision}'
helm list -n cozy-system

# Document findings
echo "Current version: X.Y.Z" > upgrade-log.txt
```

**Step 1.2: Review Changelogs**
```bash
# Read changelogs for all versions between current and target
cd /home/moriarti/repo/cozystack/docs/changelogs
ls -la v0.*.md

# Check for:
# - Breaking changes
# - Migration steps
# - New features
# - Deprecated features
```

**Step 1.3: Create Backups**
```bash
# Execute backup script
./scripts/backup-cluster.sh

# Verify backups
ls -lh backup/
md5sum backup/* > backup/checksums.txt
```

**Step 1.4: Prepare Upgrade Environment**
```bash
# Clone repository at target version
cd /tmp
git clone https://github.com/cozystack/cozystack.git cozystack-upgrade
cd cozystack-upgrade

# Checkout first target version (e.g., v0.35.5)
git checkout v0.35.5
```

### Phase 2: Upgrade to v0.35.5 (if needed)

**Step 2.1: Review Changes**
```bash
# Compare configurations
diff -u /root/cozy/packages/core/platform /tmp/cozystack-upgrade/packages/core/platform

# Check for new CRDs
diff <(kubectl get crd -o name | sort) <(ls /tmp/cozystack-upgrade/packages/*/templates/*crd* | sort)
```

**Step 2.2: Apply CRD Updates**
```bash
# Update CRDs first (before controller)
kubectl apply -f /tmp/cozystack-upgrade/packages/system/*/crds/

# Wait for CRDs to be established
kubectl wait --for condition=established --timeout=60s crd --all
```

**Step 2.3: Update Platform Components**
```bash
# Update cozystack platform
cd /tmp/cozystack-upgrade/packages/core/platform

# Apply updates
kubectl apply -f Chart.yaml
kubectl apply -f values.yaml

# Or use Helm
helm upgrade cozystack-platform . \
  --namespace cozy-system \
  --values values.yaml \
  --wait --timeout=15m
```

**Step 2.4: Monitor Upgrade Progress**
```bash
# Watch HelmReleases
watch kubectl get hr -n cozy-system

# Check pod status
watch kubectl get pods -A

# Monitor events
kubectl get events -A --sort-by='.lastTimestamp' --watch
```

**Step 2.5: Validate Upgrade**
```bash
# Check all HelmReleases are ready
kubectl get hr -A | grep -v True

# Verify version
kubectl get cm -n cozy-system cozystack -o yaml | grep version

# Run health check
./tests/proxmox-integration/extended-integrity-check.sh

# Test critical functionality
kubectl create namespace test-upgrade
kubectl run test-pod --image=nginx -n test-upgrade
kubectl delete namespace test-upgrade
```

**Step 2.6: Stabilization Period**
- Wait 30-60 minutes
- Monitor logs for errors
- Check resource usage
- Verify no pod restarts

### Phase 3: Upgrade to v0.36.2

**Repeat Phase 2 steps with v0.36.2**

```bash
cd /tmp/cozystack-upgrade
git checkout v0.36.2

# Follow steps 2.1 through 2.6
```

**Additional checks for v0.36.x**:
- New features verification
- Deprecated API cleanup
- Network policy updates

### Phase 4: Upgrade to v0.37.2 (Latest)

**Repeat Phase 2 steps with v0.37.2**

```bash
cd /tmp/cozystack-upgrade
git checkout v0.37.2

# Follow steps 2.1 through 2.6
```

**Additional v0.37.x considerations**:
- Latest security patches
- Performance improvements
- Bug fixes

### Phase 5: Post-Upgrade Validation (2 hours)

**Step 5.1: Comprehensive Testing**
```bash
# Run all integrity checks
./tests/proxmox-integration/extended-integrity-check.sh
./tests/proxmox-integration/system-integrity-check.sh

# Test CAPI functionality
kubectl get proxmoxclusters -A
kubectl get proxmoxmachines -A

# Test storage
kubectl get pv,pvc -A
kubectl get storageclass

# Test networking
kubectl get svc,ep -A
kubectl get networkpolicies -A
```

**Step 5.2: Functional Testing**
```bash
# Create test tenant namespace
kubectl create namespace tenant-upgrade-test

# Test workload deployment
kubectl run test-nginx --image=nginx -n tenant-upgrade-test
kubectl expose pod test-nginx --port=80 -n tenant-upgrade-test

# Test storage provisioning
kubectl apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-pvc
  namespace: tenant-upgrade-test
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 1Gi
  storageClassName: replicated
EOF

# Verify and cleanup
kubectl get all,pvc -n tenant-upgrade-test
kubectl delete namespace tenant-upgrade-test
```

**Step 5.3: Performance Baseline**
```bash
# Collect metrics
kubectl top nodes
kubectl top pods -A

# Compare with pre-upgrade baseline
diff pre-upgrade-resources.txt current-resources.txt
```

**Step 5.4: Documentation**
```bash
# Document final state
kubectl get pods -A > post-upgrade-pods.txt
kubectl get hr -A > post-upgrade-hr.txt

# Create upgrade report
cat > UPGRADE_REPORT.md <<EOF
# Upgrade Report

Date: $(date)
From: X.Y.Z
To: v0.37.2

## Steps Executed
1. Backup created
2. Upgraded to v0.35.5
3. Upgraded to v0.36.2
4. Upgraded to v0.37.2

## Validation Results
- All HelmReleases: Ready
- All Pods: Running
- Integrity checks: Passed
- Functional tests: Passed

## Issues Encountered
(list any issues)

## Rollback Plan
(if needed)
EOF
```

## Rollback Procedure

### When to Rollback

Rollback if:
- Critical components fail
- Data corruption detected
- Performance degradation > 50%
- Security vulnerabilities introduced
- Business-critical workloads fail

### Rollback Steps

```bash
# 1. Restore previous version
cd /tmp/cozystack-upgrade
git checkout v<previous-version>

# 2. Restore CRDs
kubectl apply -f backup/crds.yaml

# 3. Restore HelmReleases
kubectl apply -f backup/helmreleases.yaml

# 4. Restore ConfigMaps
kubectl apply -f backup/cozystack-cm.yaml

# 5. Wait for stabilization
watch kubectl get hr -A

# 6. Restore ETCD snapshot (if needed)
etcdctl snapshot restore backup/etcd-snapshot.db

# 7. Verify rollback
./tests/proxmox-integration/extended-integrity-check.sh
```

## Risk Mitigation

### High-Risk Components

1. **ETCD**
   - Risk: Data loss
   - Mitigation: Snapshot before upgrade
   - Recovery: Restore from snapshot

2. **Kube-OVN**
   - Risk: Network disruption
   - Mitigation: Upgrade during maintenance window
   - Recovery: Restart affected pods

3. **Storage (LINSTOR)**
   - Risk: Data unavailability
   - Mitigation: Verify replication before upgrade
   - Recovery: DRBD recovery procedures

4. **CAPI Providers**
   - Risk: VM provisioning failure
   - Mitigation: Test in non-production first
   - Recovery: Reinstall provider

### Monitoring During Upgrade

```bash
# Terminal 1: Watch HelmReleases
watch kubectl get hr -A

# Terminal 2: Watch Pods
watch kubectl get pods -A

# Terminal 3: Monitor Events
kubectl get events -A --watch --sort-by='.lastTimestamp'

# Terminal 4: Monitor Logs
kubectl logs -f -n cozy-system deployment/cozystack-controller

# Terminal 5: Resource Usage
watch kubectl top nodes
```

## Timeline Estimate

### Conservative Estimate

```
Phase 1: Preparation               2 hours
Phase 2: Upgrade to v0.35.5        2 hours
  - Stabilization                  1 hour
Phase 3: Upgrade to v0.36.2        2 hours
  - Stabilization                  1 hour
Phase 4: Upgrade to v0.37.2        2 hours
  - Stabilization                  1 hour
Phase 5: Post-Upgrade Validation   2 hours
Buffer for issues                  2 hours
-------------------------------------------
Total:                            15 hours
```

### Aggressive Estimate (if no issues)

```
Preparation                        1 hour
Each upgrade                       1 hour × 3 = 3 hours
Validation                         1 hour
-------------------------------------------
Total:                             5 hours
```

**Recommended**: Schedule 2-day maintenance window (allows for buffer)

## Success Criteria

### Must Have (Blocking)

- ✅ All HelmReleases in Ready state
- ✅ All pods Running or Completed
- ✅ No PVCs in Pending state
- ✅ Kube-apiserver accessible
- ✅ Nodes in Ready state
- ✅ ETCD cluster healthy
- ✅ Core DNS functional

### Should Have (Warning)

- ✅ All integrity checks pass
- ✅ Monitoring operational
- ✅ Logging functional
- ✅ Backup systems working
- ✅ Network policies enforced

### Nice to Have (Info)

- ✅ Performance baseline met
- ✅ Resource usage optimized
- ✅ Latest features available
- ✅ Documentation updated

## Post-Upgrade Tasks

### Immediate (Same Day)

1. Update documentation with new version
2. Notify stakeholders of completion
3. Monitor for 24 hours
4. Keep backups for 7 days

### Short-term (1 Week)

1. Review and close upgrade tickets
2. Document lessons learned
3. Update upgrade procedures
4. Train team on new features

### Long-term (1 Month)

1. Evaluate new features for adoption
2. Plan for next upgrade cycle
3. Review and update monitoring
4. Optimize configurations

## Integration with Proxmox Project

### Sequencing

**Option A: Upgrade First** (Recommended)
```
1. Upgrade CozyStack to v0.37.2
2. Verify stability
3. Continue with Proxmox integration
4. Test VM creation with latest version
```

**Benefits**:
- Latest bug fixes
- Better compatibility
- Reduced technical debt

**Option B: Parallel Approach**
```
1. Branch current code
2. Test Proxmox integration on current version
3. Upgrade in separate branch
4. Merge and test combined
```

**Benefits**:
- Faster overall progress
- Isolated testing

### Recommendation

**Upgrade FIRST**, then continue Proxmox integration:
1. Latest CozyStack has better CAPI support
2. Reduces variables during Proxmox testing
3. Ensures we're working with supported version
4. May include Proxmox-specific improvements

## Automation Scripts

### Create Upgrade Automation

```bash
#!/bin/bash
# cozystack-upgrade.sh

set -e

VERSION=$1
if [ -z "$VERSION" ]; then
    echo "Usage: $0 <version>"
    exit 1
fi

echo "Upgrading to $VERSION..."

# Backup
./scripts/backup-cluster.sh

# Checkout version
cd /tmp/cozystack-upgrade
git checkout $VERSION

# Update CRDs
kubectl apply -f packages/system/*/crds/

# Update platform
helm upgrade cozystack-platform packages/core/platform \
    --namespace cozy-system \
    --wait --timeout=15m

# Validate
./tests/proxmox-integration/extended-integrity-check.sh

echo "Upgrade to $VERSION complete!"
```

## Resources

### Documentation
- CozyStack releases: https://github.com/cozystack/cozystack/releases
- Changelogs: `/home/moriarti/repo/cozystack/docs/changelogs/`
- Upgrade guide: TBD

### Tools
- kubectl: v1.28+
- helm: v3.12+
- etcdctl: v3.5+
- Integrity check scripts: `./tests/proxmox-integration/`

### Support
- GitHub Issues: https://github.com/cozystack/cozystack/issues
- Community: TBD
- Internal team: Available during upgrade

## Next Actions

### Before Upgrade

1. [ ] Determine current version
2. [ ] Review all changelogs
3. [ ] Schedule maintenance window
4. [ ] Notify stakeholders
5. [ ] Create backups
6. [ ] Run pre-upgrade health check

### During Upgrade

1. [ ] Execute upgrade procedure
2. [ ] Monitor all components
3. [ ] Validate each step
4. [ ] Document issues
5. [ ] Test critical workflows

### After Upgrade

1. [ ] Validate all systems
2. [ ] Update documentation
3. [ ] Create upgrade report
4. [ ] Resume Proxmox integration work
5. [ ] Plan next upgrade cycle

---

**Document Status**: Draft  
**Review Date**: TBD  
**Approved By**: TBD  
**Next Review**: After upgrade completion

