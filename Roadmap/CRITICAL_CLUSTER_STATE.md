# CRITICAL: Cluster Network Infrastructure Failure

**Date**: 2025-09-10 22:30  
**Severity**: CRITICAL - Production Impact  
**Status**: Cluster Partially Degraded

## 🚨 CRITICAL ISSUES IDENTIFIED

### 1. CoreDNS Complete Failure ⚠️⚠️⚠️
**Impact**: CRITICAL - DNS resolution not working

```
coredns-578d4f8ffc-5zhlt    ContainerStatusUnknown   138d
coredns-578d4f8ffc-wm6fz    ContainerCreating        17d
coredns-578d4f8ffc-wtc7l    ContainerStatusUnknown   53d
coredns-578d4f8ffc-zm4g4    ImagePullBackOff         189d
```

**Root Cause**: All CoreDNS pods are failing  
**Effect**: 
- No DNS resolution in cluster
- Services cannot communicate
- Helm releases cannot fetch charts
- New pods cannot start

### 2. Kube-OVN Controller Terminated
**Impact**: HIGH - Network IP allocation failing

```
kube-ovn-controller    Completed (not running)
kube-ovn-monitor       Completed (not running)  
kube-ovn-webhook       200+ pods in Failed/Unknown state
```

**Root Cause**: Controller pods exited and not restarting
**Effect**:
- Cannot allocate IPs to new pods
- New pods stuck in ContainerCreating
- Network connectivity failing

### 3. Cilium Dependency Failure
**Impact**: HIGH - CNI integration broken

```
cilium HelmRelease: False
Error: "dial udp 10.96.0.10:53: connect: operation not permitted"
```

**Root Cause**: Cannot reach DNS (10.96.0.10:53) due to network policy or firewall
**Effect**:
- Cannot update Cilium
- Kube-OVN waiting for Cilium
- Circular dependency deadlock

### 4. Cluster API Complete Failure
**Impact**: HIGH - Cannot manage infrastructure

```
All CAPI pods: 0/27 Running
Status: Error/ContainerCreating/Unknown
```

**Root Cause**: Cannot create pods due to network/DNS issues
**Effect**:
- Cannot provision VMs
- Cannot manage clusters
- Proxmox integration blocked

## 🔍 Root Cause Analysis

### Primary Issue: Network Bootstrap Failure

The cluster is in a **network bootstrap deadlock**:

1. **CoreDNS is down** → No DNS resolution
2. **Without DNS** → Helm cannot fetch charts
3. **Without Helm** → Cilium cannot update
4. **Without Cilium** → Kube-OVN waits
5. **Without Kube-OVN** → No IP allocation
6. **Without IPs** → New pods cannot start
7. **Without new pods** → CoreDNS cannot restart

### Secondary Issues:
- **Resource exhaustion** on mgr-cozy1 (97% CPU)
- **Pod accumulation** (hundreds of dead pods)
- **Long cluster uptime** (208 days) without maintenance

## 🛠️ EMERGENCY RECOVERY PLAN

### Phase 0: Emergency Stabilization (IMMEDIATE)

#### Step 1: Fix CoreDNS (Priority 1)
```bash
# Delete failed CoreDNS pods
kubectl delete pods -n kube-system --field-selector=status.phase=Failed --force --grace-period=0
kubectl delete pods -n kube-system -l k8s-app=kube-dns --field-selector=status.phase=Unknown --force --grace-period=0

# Force recreate CoreDNS deployment
kubectl rollout restart deployment/coredns -n kube-system

# If still failing, manually create a CoreDNS pod
kubectl run coredns-emergency -n kube-system \
  --image=registry.k8s.io/coredns/coredns:v1.11.1 \
  --command -- /coredns -conf /etc/coredns/Corefile
```

#### Step 2: Fix Kube-OVN Dependencies
```bash
# Delete old deployments
kubectl delete deployment kube-ovn-controller -n cozy-kubeovn
kubectl delete deployment kube-ovn-monitor -n cozy-kubeovn
kubectl delete deployment kube-ovn-webhook -n cozy-kubeovn

# Trigger Helm to recreate them
kubectl annotate helmrelease kubeovn -n cozy-kubeovn \
  reconcile.fluxcd.io/requestedAt="$(date +%s)"
```

#### Step 3: Manual Cilium Fix
```bash
# Check if Cilium pods can access DNS
kubectl exec -n cozy-cilium cilium-dn4tv -- nslookup kubernetes.default

# If DNS works from Cilium pods, fix network policy
kubectl get networkpolicy -A
kubectl describe networkpolicy -n cozy-fluxcd

# Temporarily disable problematic network policies
kubectl patch networkpolicy <name> -n <namespace> -p '{"spec":{"podSelector":{}}}'
```

### Phase 1: Network Recovery (1-2 hours)

#### Step 1: Verify DNS
```bash
# Test DNS from working pod
kubectl run -it --rm debug --image=busybox --restart=Never -- nslookup kubernetes.default

# Check CoreDNS logs
kubectl logs -n kube-system -l k8s-app=kube-dns --tail=100
```

#### Step 2: Verify Kube-OVN
```bash
# Check controller logs
kubectl logs -n cozy-kubeovn -l app=kube-ovn-controller --tail=100

# Verify IP allocation
kubectl get subnet
kubectl get ip
```

#### Step 3: Verify Cilium
```bash
# Check Cilium status
kubectl exec -n cozy-cilium cilium-dn4tv -- cilium status

# Check network connectivity
kubectl exec -n cozy-cilium cilium-dn4tv -- cilium connectivity test
```

### Phase 2: CAPI Recovery (2-4 hours)

#### Step 1: Clean CAPI Namespace
```bash
# Delete all failed pods
kubectl delete pods -n cozy-cluster-api --field-selector=status.phase!=Running --force --grace-period=0

# Restart CAPI operator
kubectl rollout restart deployment -n cozy-cluster-api capi-operator-cluster-api-operator
```

#### Step 2: Verify Providers
```bash
# Check infrastructure providers
kubectl get infrastructureproviders -A

# Check provider health
kubectl get pods -n cozy-cluster-api
```

## ⚠️ CRITICAL DECISION POINT

### Option A: Emergency Recovery (Recommended for Production)
**Time**: 2-4 hours  
**Risk**: Medium  
**Steps**: Follow emergency recovery plan above

**Pros**:
- Preserves existing workloads
- Minimal disruption
- Can recover incrementally

**Cons**:
- May take multiple iterations
- Some issues may resurface
- Requires deep troubleshooting

### Option B: Network Stack Reinstall (Nuclear Option)
**Time**: 4-8 hours  
**Risk**: HIGH  
**Impact**: ALL pods will restart

**Steps**:
1. Backup all critical data
2. Uninstall Kube-OVN
3. Uninstall Cilium  
4. Reinstall network stack
5. Restore workloads

**Pros**:
- Clean state
- Removes accumulated issues
- Long-term stability

**Cons**:
- Downtime for all services
- Risk of data loss
- Complex recovery

### Option C: Cluster Rebuild (Last Resort)
**Time**: 1-2 days  
**Risk**: VERY HIGH  
**Impact**: Complete cluster recreation

**When to consider**:
- If Options A & B fail
- If corruption is too extensive
- If downtime is acceptable

## 📊 Current Cluster State Summary

### Working Components ✅
- **Kubernetes API**: Operational
- **Node connectivity**: All nodes Ready
- **Some Cilium agents**: 3/4 running
- **OVN Central**: 2/3 running
- **Basic OVS**: 3/4 running

### Broken Components ❌
- **CoreDNS**: 0/4 pods running
- **Kube-OVN Controller**: Terminated
- **Kube-OVN Webhook**: 200+ failed pods
- **Cilium HelmRelease**: Cannot update
- **CAPI**: Complete failure
- **All infrastructure providers**: Not ready

### Resource Status
```
mgr-cozy1:  CPU 97% (CRITICAL), Memory 58%
mgr-cozy2:  CPU 70%, Memory 28%
mgr-cozy3:  CPU 27%, Memory 16%
mgr.cp.if.ua: CPU 4%, Memory 0%
```

## 🎯 Immediate Actions Required

### MUST DO NOW (Next 30 minutes):
1. ✅ Clean up failed pods in cozy-kubeovn (DONE - 250+ pods removed)
2. ⏳ Fix CoreDNS - CRITICAL BLOCKER
3. ⏳ Restart Kube-OVN controller
4. ⏳ Verify DNS resolution working

### SHOULD DO SOON (Next 2 hours):
1. Fix Cilium network connectivity
2. Clear CAPI namespace
3. Restart CAPI operators
4. Verify basic CAPI functionality

### CAN DO LATER (Next 24 hours):
1. Install Proxmox provider
2. Test Proxmox integration
3. Performance optimization
4. Documentation updates

## 🚫 BLOCKED ITEMS

### Cannot Proceed Until Fixed:
- ❌ Proxmox integration (blocked by CAPI failure)
- ❌ New VM provisioning (blocked by CAPI failure)
- ❌ Helm chart deployments (blocked by DNS failure)
- ❌ New workloads (blocked by IP allocation failure)

## 📝 Recommendations

### Immediate (Today):
1. **Focus on CoreDNS recovery** - This is the critical blocker
2. **Fix network bootstrap** - Get DNS → Helm → Cilium → Kube-OVN chain working
3. **Clean up resources** - Remove accumulated failed pods
4. **Monitor progress** - Check every 15 minutes

### Short Term (This Week):
1. **Stabilize CAPI** - Get basic functionality working
2. **Reduce resource usage** - Address CPU on mgr-cozy1
3. **Clean up old resources** - Remove 200+ day old pods
4. **Document recovery** - Record what worked

### Long Term (Next Month):
1. **Implement monitoring** - Prevent future failures
2. **Regular maintenance** - Clean up pods weekly
3. **Resource planning** - Address CPU constraints
4. **Backup strategy** - Ensure recoverability

## 🆘 If Emergency Recovery Fails

### Escalation Path:
1. **Contact CozyStack team** - Get expert help
2. **Check community** - Look for similar issues
3. **Consider Option B** - Network stack reinstall
4. **Last resort** - Cluster rebuild

### Emergency Contacts:
- CozyStack GitHub: https://github.com/cozystack/cozystack/issues
- Kubernetes Slack: #cozystack
- Emergency: Document current state and seek help

---

**Assessment Status**: ⚠️ CRITICAL  
**Recovery Status**: 🔄 IN PROGRESS  
**Next Check**: Every 15 minutes  
**Expected Recovery**: 2-4 hours (if emergency plan works)

**IMPORTANT**: Do NOT proceed with Proxmox integration until cluster network is stable!
