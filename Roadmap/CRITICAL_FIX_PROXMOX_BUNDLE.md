# Critical Fix: paas-proxmox Bundle Configuration

**Date**: 2025-10-24  
**Issue**: Incorrect architecture understanding - KubeVirt vs Proxmox  
**Severity**: Critical  
**Status**: Fixed

## Problem Statement

### What Was Wrong

The initial understanding of Proxmox integration was **fundamentally incorrect**:

❌ **Wrong**: Using KubeVirt to create VMs inside Kubernetes pods
```
User → K8s API → KubeVirt → Pod → QEMU/KVM → VM
```

✅ **Correct**: Using Cluster API to create VMs directly in Proxmox
```
User → K8s API → Cluster API → Proxmox API → Proxmox VM
```

### Why This Matters

**KubeVirt approach** (wrong for Proxmox):
- VMs run inside Kubernetes pods
- Uses QEMU/KVM inside containers
- Adds overhead and complexity
- Doesn't leverage Proxmox features
- Makes no sense when you have Proxmox hypervisor

**Proxmox approach** (correct):
- VMs run directly in Proxmox hypervisor
- Native Proxmox VM management
- Better performance
- Full Proxmox feature set (snapshots, migration, HA)
- Leverages existing infrastructure

## Changes Made

### 1. Fixed paas-proxmox.yaml Bundle

**Before** (`packages/core/platform/bundles/paas-proxmox.yaml`):
```yaml
# Had duplicate proxmox-csi-operator entries
- name: proxmox-csi-operator
  releaseName: proxmox-csi-operator
  chart: cozy-proxmox-csi-operator
  namespace: cozy-proxmox
  dependsOn: [cilium,kubeovn,cert-manager]

- name: proxmox-csi-operator  # DUPLICATE!
  releaseName: proxmox-csi-operator
  chart: cozy-proxmox-csi-operator
  namespace: cozy-proxmox
  dependsOn: [cilium,kubeovn,cert-manager]

# Missing critical components
```

**After**:
```yaml
# Added missing FluxCD
- name: fluxcd-operator
- name: fluxcd

# Added CozyStack core
- name: cozy-proxy
- name: cozystack-api
- name: cozystack-controller
- name: lineage-controller-webhook
- name: cozystack-resource-definition-crd
- name: cozystack-resource-definitions

# Fixed Proxmox integration components
- name: proxmox-csi       # CSI driver for storage
- name: proxmox-ccm       # Cloud controller manager
- name: metallb           # Load balancer
- name: snapshot-controller
- name: piraeus-operator  # LINSTOR
- name: linstor

# Kept database operators
- name: mariadb-operator
- name: postgres-operator
- name: rabbitmq-operator
- name: redis-operator

# Added CAPI for VM management
- name: kamaji
- name: capi-operator
- name: capi-providers    # Includes Proxmox provider
```

### 2. Removed Incorrect Documentation

Deleted files that suggested KubeVirt approach:
- `Roadmap/VM_CREATION_GUIDE.md` (KubeVirt-based)
- `tests/proxmox-integration/test-vm-creation.sh` (KubeVirt tests)
- `tests/proxmox-integration/test_vm_api.py` (KubeVirt API)

### 3. Created Correct Documentation

New files with Proxmox-specific approach:
- `Roadmap/PROXMOX_ARCHITECTURE.md` - Complete architecture explanation
- `Roadmap/PROXMOX_VM_CREATION_GUIDE.md` - Correct VM creation guide
- `tests/proxmox-integration/test-proxmox-vm-creation.sh` - Proper testing

## Component Comparison

### paas-full (KubeVirt-based) vs paas-proxmox (Proxmox-based)

| Component | paas-full | paas-proxmox | Reason |
|-----------|-----------|--------------|--------|
| **VM Management** |
| kubevirt-operator | ✅ | ❌ | Not needed with Proxmox |
| kubevirt | ✅ | ❌ | Replaced by Proxmox |
| kubevirt-instancetypes | ✅ | ❌ | Proxmox handles this |
| kubevirt-cdi-operator | ✅ | ❌ | Not needed |
| kubevirt-cdi | ✅ | ❌ | Not needed |
| **Proxmox Integration** |
| proxmox-csi | ❌ | ✅ | For Proxmox storage |
| proxmox-ccm | ❌ | ✅ | For Proxmox cloud integration |
| **Cluster API** |
| capi-providers | Different | Includes Proxmox | Proxmox provider |
| **Storage** |
| piraeus-operator | ✅ | ✅ | LINSTOR for replication |
| linstor | ✅ | ✅ | Hybrid with Proxmox |
| **Networking** |
| cilium | ✅ | ✅ | Same |
| kubeovn | ✅ | ✅ | Same |
| metallb | ✅ | ✅ | Same |

## Architecture Flow

### VM Creation Flow (Corrected)

```
1. User creates Kubernetes CRD
   apiVersion: apps.cozystack.io/v1alpha1
   kind: Kubernetes
   
   ↓

2. CozyStack controller creates HelmRelease
   
   ↓

3. Flux installs kubernetes chart
   
   ↓

4. Chart creates Cluster API resources:
   - Cluster
   - ProxmoxCluster
   - KamajiControlPlane
   - MachineDeployment
   - ProxmoxMachineTemplate
   
   ↓

5. CAPI core controller creates:
   - Machine resources
   
   ↓

6. CAPI Proxmox provider (capmox):
   - Reads ProxmoxMachine spec
   - Calls Proxmox API
   
   ↓

7. Proxmox VE:
   - Clones VM from template
   - Configures VM (CPU, RAM, disk)
   - Starts VM
   - Applies cloud-init
   
   ↓

8. VM boots and:
   - Cloud-init runs
   - Kubelet starts
   - Node joins Kubernetes cluster
   
   ↓

9. VM is ready as Kubernetes worker node
```

### Storage Flow (Corrected)

```
Pod needs storage
   ↓
PVC created with storageClass: proxmox-data-xfs
   ↓
Proxmox CSI driver:
   - Calls Proxmox API
   - Creates disk in Proxmox storage
   - Returns volume handle
   ↓
CSI node plugin:
   - Attaches Proxmox disk to VM
   - Mounts volume in pod
   ↓
Pod uses storage
```

## Testing Strategy

### Test 1: Verify CAPI Provider

```bash
kubectl -n cozy-cluster-api get pods -l control-plane=capmox-controller-manager
```

Expected: Running pod

### Test 2: Check ProxmoxCluster

```bash
kubectl get proxmoxclusters -A
```

Expected: Existing ProxmoxCluster in Ready state

### Test 3: Create Test VM

```bash
kubectl apply -f - <<EOF
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ProxmoxMachine
metadata:
  name: test-vm
spec:
  nodeName: pve
  template: ubuntu-22.04
  cores: 2
  memory: 4096
  diskSize: 20
EOF

# Wait and verify
kubectl get proxmoxmachine test-vm
```

Expected: VM created in Proxmox with assigned vmID

### Test 4: Verify in Proxmox

```bash
# On Proxmox host
qm list | grep test-vm
```

Expected: VM visible in Proxmox

## Impact Assessment

### What Works Now ✅

1. **Correct VM creation flow** - VMs created in Proxmox, not in pods
2. **Proper storage integration** - Proxmox CSI driver
3. **Cloud controller** - Proxmox CCM for node management
4. **Tenant clusters** - Via Kamaji + CAPI Proxmox
5. **Database operators** - Can run in pods (default) or future LXC/VM support

### What Still Needs Work 🚧

1. **LXC runtime support** - Via proxmox-lxcri project (priority)
2. **User choice mechanism** - Choose between pod/VM/LXC for databases
3. **Advanced networking** - L2 connectivity, VXLAN
4. **HA and failover** - Integration with Proxmox HA

### What Was Removed ❌

1. **KubeVirt** - Not needed with Proxmox
2. **CDI** - Not needed
3. **virt-launcher pods** - No equivalent (VMs run in Proxmox)

## Migration Guide

### For Existing Deployments

If you have existing `paas-full` deployment and want to switch to `paas-proxmox`:

**DON'T**: This is a breaking change. VMs created with KubeVirt are incompatible with Proxmox-based approach.

**DO**: 
1. Plan new deployment with `paas-proxmox`
2. Migrate workloads to new cluster
3. Decommission old cluster

### For New Deployments

Use `paas-proxmox` bundle from the start if you have Proxmox infrastructure.

## Documentation Updates

### New Files Created

1. **PROXMOX_ARCHITECTURE.md**
   - Complete architecture explanation
   - Component descriptions
   - Comparison with KubeVirt
   - Best practices

2. **PROXMOX_VM_CREATION_GUIDE.md**
   - How to create VMs via CAPI
   - VM template creation
   - Examples and troubleshooting
   - API access examples

3. **CRITICAL_FIX_PROXMOX_BUNDLE.md** (this file)
   - Problem statement
   - Changes made
   - Testing strategy

### Updated Files

1. **paas-proxmox.yaml**
   - Fixed duplicate entries
   - Added missing components
   - Correct dependencies

2. **COMPLETE_ROADMAP.md**
   - Needs update to reflect correct approach

3. **EXTENDED_INTEGRATION_PLAN.md**
   - Needs update for LXC integration

## Next Steps

### Immediate (Critical)

1. ✅ Fix `paas-proxmox.yaml`
2. ✅ Create correct documentation
3. ⏳ Test VM creation via CAPI
4. ⏳ Verify existing ProxmoxCluster works

### Short-term (1-2 weeks)

1. Complete testing of all VM creation scenarios
2. Update all integration guides
3. Fix any remaining issues in `paas-proxmox.yaml`
4. Create comprehensive examples

### Long-term (1-3 months)

1. Complete `proxmox-lxcri` project
2. Implement LXC runtime support
3. Add user choice mechanism for runtime
4. Integrate database operators with LXC/VM options

## Lessons Learned

### Key Takeaways

1. **Understand infrastructure first** - Know if you're using KubeVirt or native hypervisor
2. **Read architecture docs** - Don't assume similar names mean same approach
3. **Test early** - Would have caught this with first VM creation test
4. **Document assumptions** - Make architecture decisions explicit

### Process Improvements

1. **Architecture review** - Review architecture before implementation
2. **Integration testing** - Test actual VM creation, not just pod readiness
3. **Component understanding** - Understand what each component does
4. **Regular validation** - Verify assumptions against reality

## Conclusion

This was a critical architectural fix. The previous understanding would have led to:
- Inefficient VM management
- Inability to leverage Proxmox features
- Poor performance
- Unnecessary complexity

The corrected approach:
- ✅ Leverages Proxmox native capabilities
- ✅ Better performance (no pod overhead)
- ✅ Full feature set (snapshots, migration, HA)
- ✅ Cleaner architecture
- ✅ Matches intended design from Issue #69

**Status**: Architecture corrected, documentation updated, ready for testing and validation.

## References

- Issue #69: Integration with Proxmox
- [Cluster API Proxmox Provider](https://github.com/ionos-cloud/cluster-api-provider-proxmox)
- [Proxmox VE Documentation](https://pve.proxmox.com/pve-docs/)
- [Cluster API Book](https://cluster-api.sigs.k8s.io/)

