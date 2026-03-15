# Pre-Upgrade Health Check Report

**Date**: 2025-10-24 19:32 UTC  
**Cluster**: mgr.cp.if.ua  
**Current Version**: v0.28.0-54-g22cf18ff

## 🚨 CRITICAL ISSUES FOUND

**Status**: ❌ **CLUSTER NOT HEALTHY - UPGRADE BLOCKED**

### Summary

- ✅ **Nodes**: All 4 nodes Ready
- ❌ **Pods**: 19+ pods in Failed/Error states
- ✅ **PVCs**: All Bound (no storage issues)
- ❌ **Overall**: Multiple critical components failing

**Recommendation**: **FIX CRITICAL ISSUES BEFORE UPGRADE**

## Detailed Findings

### ✅ Nodes Status - HEALTHY

```
NAME           STATUS   ROLES           AGE    VERSION
mgr-cozy1      Ready    control-plane   219d   v1.32.3
mgr-cozy2      Ready    control-plane   219d   v1.32.3
mgr-cozy3      Ready    control-plane   219d   v1.32.3
mgr.cp.if.ua   Ready    <none>          179d   v1.32.3
```

**Status**: ✅ All nodes Ready  
**Kubernetes Version**: v1.32.3  
**Age**: 219 days (control plane), 179 days (worker)

### ❌ Pod Issues - CRITICAL

#### Issue 1: ImagePullBackOff (9 pods)

**Severity**: HIGH  
**Impact**: Components cannot start

**Affected Pods**:
```
capi-ipam-in-cluster-system/capi-ipam-in-cluster-controller-manager
capi-system/capi-controller-manager
cozy-cert-manager/cert-manager-cainjector
cozy-cert-manager/cert-manager-webhook
cozy-cluster-api/capi-operator-cluster-api-operator
cozy-cluster-api/capk-controller-manager
cozy-dashboard/dashboard-internal-kubeappsapis (multiple)
cozy-etcd-operator/etcd-operator-controller-manager
```

**Root Cause**: Cannot pull images from registry  
**Possible Reasons**:
- Registry connectivity issues
- Image pull secrets missing/expired
- Rate limiting
- DNS resolution problems
- Network policies blocking

**Action Required**:
```bash
# Check image pull errors
kubectl describe pod <pod-name> -n <namespace>

# Check if can reach registry
curl -I https://ghcr.io
curl -I https://registry.k8s.io

# Check DNS
nslookup ghcr.io
nslookup registry.k8s.io

# Check network policies
kubectl get networkpolicies -A
```

#### Issue 2: CrashLoopBackOff (5 pods)

**Severity**: HIGH  
**Impact**: Components restart continuously

**Affected Pods**:
```
cozy-cluster-api/capi-kubeadm-bootstrap-controller-manager
cozy-dashboard/dashboard (multiple replicas)
cozy-dashboard/dashboard-internal-kubeappsapis (1 pod)
```

**Root Cause**: Application crashes after start

**Action Required**:
```bash
# Check logs
kubectl logs <pod-name> -n <namespace> --previous

# Check events
kubectl describe pod <pod-name> -n <namespace>
```

#### Issue 3: OutOfCpu (4 pods)

**Severity**: MEDIUM  
**Impact**: Pods cannot be scheduled due to CPU limits

**Affected Pods**:
```
cozy-cluster-api/capi-kamaji-controller-manager
cozy-dashboard/dashboard (2 replicas)
cozy-dashboard/dashboard-internal-dashboard
cozy-dashboard/dashboard-internal-kubeappsapis
```

**Root Cause**: Insufficient CPU resources or quotas

**Action Required**:
```bash
# Check node resources
kubectl top nodes

# Check resource quotas
kubectl get resourcequota -A

# Check pod requests
kubectl describe pod <pod-name> -n <namespace> | grep -A 5 Requests
```

#### Issue 4: Init:0/5 (1 pod)

**Severity**: MEDIUM  
**Component**: Cilium CNI

**Affected Pod**:
```
cozy-cilium/cilium-dg9kr
```

**Root Cause**: Init containers not completing

**Impact**: Networking issues possible

**Action Required**:
```bash
# Check init container logs
kubectl logs cilium-dg9kr -n cozy-cilium -c <init-container-name>

# Check if other Cilium pods are healthy
kubectl get pods -n cozy-cilium
```

#### Issue 5: ContainerStatusUnknown (2 pods)

**Severity**: MEDIUM  
**Component**: Dashboard

**Affected Pods**:
```
cozy-dashboard/dashboard
cozy-dashboard/dashboard-internal-dashboard
```

**Root Cause**: Node communication issues

### ✅ Storage Status - HEALTHY

```
PVCs: All Bound
PVs: No issues detected
```

**Status**: ✅ No storage problems

### ✅ Backup Status - COMPLETED

**Location**: `/root/cozy-backup/20251024-1931/`

**Files Backed Up**:
```
✅ cozystack-configmap.yaml (853 bytes)
✅ all-configmaps.yaml (3.7M)
✅ all-helmreleases.yaml (144K)
✅ all-crds.yaml (24M)
✅ cozy-system-secrets.yaml (52K)
✅ all-resources-state.yaml (2.1M)
✅ all-namespaces.yaml (35K)
✅ proxmox-clusters.yaml (1.6K)
✅ checksums.md5
```

**Total Size**: 6.1M  
**Checksum**: Verified

**Missing**:
- ❌ ETCD snapshot (etcdctl not available on host)

## Impact Assessment for Upgrade

### 🛑 Blockers

1. **ImagePullBackOff Issues**
   - **Risk**: Upgrade will fail to pull new images
   - **Impact**: HIGH - Upgrade will fail
   - **Must Fix**: YES

2. **CrashLoopBackOff Issues**
   - **Risk**: Upgraded components may also crash
   - **Impact**: HIGH - Unstable platform
   - **Must Fix**: YES

3. **Resource Constraints**
   - **Risk**: New pods won't schedule
   - **Impact**: MEDIUM - Partial upgrade
   - **Must Fix**: RECOMMENDED

### ⚠️ Warnings

1. **Cilium Init Issue**
   - **Risk**: Networking problems during upgrade
   - **Impact**: MEDIUM
   - **Should Fix**: YES

2. **Dashboard Issues**
   - **Risk**: Dashboard unavailable post-upgrade
   - **Impact**: LOW (non-critical component)
   - **Can Fix**: After upgrade

## Recommendations

### 🚨 CRITICAL: Do NOT Proceed with Upgrade

**Reasons**:
1. Multiple ImagePullBackOff issues
2. CAPI components failing
3. Resource constraints present
4. Unstable platform state

**Risk**: Upgrade will likely fail or make situation worse

### ✅ Required Actions BEFORE Upgrade

#### Priority 1: Fix Image Pull Issues

**Steps**:
```bash
# 1. Check registry connectivity
curl -I https://ghcr.io
curl -I https://registry.k8s.io

# 2. Check DNS resolution
nslookup ghcr.io
nslookup registry.k8s.io

# 3. Check image pull secrets
kubectl get secrets -A | grep regcred

# 4. Test image pull manually
crictl pull ghcr.io/cozystack/cozystack/cozy-proxy:v0.1.4

# 5. Check network policies
kubectl get networkpolicies -A

# 6. Review logs
kubectl get events -A --sort-by='.lastTimestamp' | grep -i pull
```

#### Priority 2: Fix CrashLoopBackOff

**Steps**:
```bash
# 1. Get logs from crashed pods
kubectl logs -n cozy-cluster-api capi-kubeadm-bootstrap-controller-manager --previous

# 2. Check for configuration issues
kubectl describe pod -n cozy-cluster-api capi-kubeadm-bootstrap-controller-manager

# 3. Review HelmRelease status
kubectl get hr -A | grep -v True

# 4. Check dependencies
kubectl get hr -n cozy-cluster-api -o yaml
```

#### Priority 3: Address Resource Constraints

**Steps**:
```bash
# 1. Check current usage
kubectl top nodes
kubectl top pods -A

# 2. Review resource quotas
kubectl get resourcequota -A

# 3. Consider:
#    - Increase node resources
#    - Remove resource limits temporarily
#    - Scale down non-critical components
```

#### Priority 4: Fix Cilium

**Steps**:
```bash
# 1. Check Cilium status
kubectl get pods -n cozy-cilium

# 2. Review init container logs
kubectl logs -n cozy-cilium cilium-dg9kr -c <init-container>

# 3. May need to restart pod
kubectl delete pod -n cozy-cilium cilium-dg9kr
```

### Timeline Impact

**Original Plan**: Start upgrade today  
**Revised Plan**: 
1. Fix issues: 4-8 hours
2. Validate fixes: 2 hours
3. Then proceed with upgrade

**Total Delay**: 1-2 days

## Alternative: Fresh Install Approach

Given the cluster's current state, **reconsider Option C (Hybrid)**:

### Why Hybrid May Be Better Now

1. **Current cluster is unhealthy**
   - Multiple component failures
   - Resource constraints
   - Image pull issues

2. **Fixing may be harder than fresh install**
   - Unknown root causes
   - May uncover more issues
   - Time to fix uncertain

3. **Fresh install advantages**
   - Clean slate (v0.37.2)
   - No legacy issues
   - Known good state
   - Can test Proxmox properly

4. **Can keep unhealthy cluster as backup**
   - No risk
   - Fallback option
   - Study failure modes

### Recommendation: RECONSIDER APPROACH

**Current situation changes risk assessment**:

- **Option A (Incremental)**: Now VERY HIGH RISK
  - Must fix current issues first
  - Upgrade may fail anyway
  - Compound existing problems

- **Option C (Hybrid)**: Now MORE ATTRACTIVE
  - Avoid fixing complex issues
  - Clean environment for Proxmox
  - Keep problematic cluster isolated

**Question for User**: 
Given cluster's unhealthy state, should we:
1. **Spend 1-2 days fixing issues**, then upgrade? (Risk: may fail)
2. **Switch to fresh install** (Option C)? (Lower risk, similar timeline)

## Next Steps

### If Continuing with Option A

1. ⏳ Fix ImagePullBackOff issues
2. ⏳ Fix CrashLoopBackOff issues  
3. ⏳ Address resource constraints
4. ⏳ Re-run health check
5. ⏳ Only then proceed with upgrade

**Estimated Time**: 6-10 hours

### If Switching to Option C

1. ✅ Backup complete (already done)
2. ⏳ Plan fresh v0.37.2 installation
3. ⏳ Install on new VMs
4. ⏳ Test Proxmox integration
5. ⏳ Gradual migration

**Estimated Time**: 1 week (but cleaner result)

## Conclusion

**Status**: ❌ **UPGRADE BLOCKED**

**Critical Issues**: 19+ failing pods  
**Root Causes**: Image pull, crashes, resources  
**Fix Time**: 6-10 hours (uncertain)

**Recommendation**: **FIX ISSUES FIRST** or **SWITCH TO OPTION C**

**User Decision Required**: 
- Continue with fixes + Option A?
- Switch to Option C (fresh install)?

---

**Health Check Completed**: 2025-10-24 19:32 UTC  
**Next Action**: AWAITING USER DECISION  
**Backup Location**: `/root/cozy-backup/20251024-1931/`

