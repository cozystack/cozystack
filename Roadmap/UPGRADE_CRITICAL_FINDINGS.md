# CozyStack Upgrade - Critical Findings

**Date**: 2025-10-24  
**Current Version**: v0.28.0-54-g22cf18ff  
**Target Version**: v0.37.2  
**Status**: ⚠️ **CRITICAL REVIEW REQUIRED**

## 🚨 Executive Summary

**CRITICAL**: Large version gap detected (9 minor versions)

- Current: v0.28.0 (March 2025)
- Target: v0.37.2 (latest)
- Gap: 9 minor versions
- Upgrade steps required: 7 milestones
- Estimated time: **15-20 hours** (3-4 days)

**Breaking changes detected**: **3 major breaking changes**

## 📊 Version Gap Analysis

### Current Installation

```
Base Version: v0.28.0
Commits ahead: 54
Git hash: 22cf18ff
Installation date: ~March 19, 2025
Age: ~219 days

Components:
- cozy-proxy: v0.1.4
- cozystack-api: v0.29.1
```

### Required Upgrade Path

```
v0.28.0 (current)
    ↓ [STEP 1] - v0.29.x, v0.30.x accumulated
v0.31.0
    ↓ [STEP 2] - v0.31.x patches
v0.32.1
    ↓ [STEP 3] - v0.32.x patches, v0.33.x accumulated
v0.33.2
    ↓ [STEP 4] - v0.33.x patches, v0.34.x accumulated
v0.34.8
    ↓ [STEP 5] - v0.34.x patches, v0.35.x accumulated
v0.35.5
    ↓ [STEP 6] - v0.35.x patches, v0.36.x accumulated
v0.36.2
    ↓ [STEP 7] - v0.36.x patches, v0.37.x accumulated
v0.37.2 (target)
```

**Total milestones**: 7  
**Cannot skip**: Must go through each milestone

## 🔴 Critical Breaking Changes

### 1. FerretDB v1 → v2 (v0.34.0)

**Impact**: HIGH  
**Component**: FerretDB  
**Change**: Major version upgrade v1 → v2.4.0

**Required Actions**:
```
⚠️ BEFORE upgrading to v0.34.0:
1. Backup ALL FerretDB data
2. Follow migration guide: https://docs.ferretdb.io/migration/migrating-from-v1/
3. Test migration in non-production environment
4. Verify data integrity after migration
```

**Risk**: Data loss if not migrated correctly

**Check if affected**:
```bash
kubectl get pods -A | grep ferretdb
kubectl get applications.apps.cozystack.io -A | grep ferretdb
```

### 2. SeaweedFS Breaking Change (v0.36.0)

**Impact**: MEDIUM-HIGH  
**Component**: SeaweedFS  
**Change**: Service specification changes

**Description**: Upon updating to v0.36.0, SeaweedFS will be updated to a newer version with service specification changes.

**Required Actions**:
```
⚠️ BEFORE upgrading to v0.36.0:
1. Check current SeaweedFS usage
2. Review SeaweedFS endpoints/configuration
3. Update dependent services
4. Test connectivity after upgrade
```

**Check if affected**:
```bash
kubectl get pods -A | grep seaweedfs
kubectl get svc -A | grep seaweedfs
```

### 3. Resource Configuration Migration (v0.33.0)

**Impact**: MEDIUM  
**Component**: All managed applications  
**Change**: Automatic migration of resource definitions

**Description**: Resource configuration format changes:
- Old: `resources.requests.[cpu,memory]` and `resources.limits.[cpu,memory]`
- New: `resources.[cpu,memory]`

**Actions**: 
- Migration is AUTOMATIC
- Verify after upgrade that resources are correctly configured
- Check for any custom resource configurations

## 📋 Migration Scripts

Each major version includes migration scripts that run automatically:

### v0.31.0 Migrations
- Update Kubernetes ConfigMap with stack version
- Refactor monitoring config
- Update kube-rbac-proxy daemonset

### v0.32.0 Migrations
- Tenant Kubernetes resource fixes
- CAPI providers migration

### v0.33.0 Migrations
- Automatic resource definition format migration

### v0.34.0 Migrations
- **FerretDB data migration** (manual step required!)

### v0.35.0 Migrations
- Snapshot CRD dependency
- Kamaji migration fixes

### v0.36.0 Migrations
- Virtual machine app version references
- SeaweedFS service spec changes

### v0.37.0 Migrations
- Installer hardening (migration #20)
- CRD decoupling
- Platform resource definitions protection

## ⚠️ Pre-Upgrade Requirements

### Must Complete Before Any Upgrade

1. **Create Complete Backup**
   ```bash
   # ETCD backup
   etcdctl snapshot save /backup/etcd-$(date +%Y%m%d).db
   
   # ConfigMaps backup
   kubectl get cm -A -o yaml > /backup/configmaps.yaml
   
   # FerretDB data backup (if used)
   # Follow FerretDB backup procedures
   
   # SeaweedFS data backup (if used)
   # Follow SeaweedFS backup procedures
   ```

2. **Health Check Current State**
   ```bash
   # Check all pods
   kubectl get pods -A | grep -v Running | grep -v Completed
   
   # Check PVCs
   kubectl get pvc -A | grep -v Bound
   
   # Check nodes
   kubectl get nodes
   
   # Check ETCD
   etcdctl endpoint health
   ```

3. **Document Current State**
   ```bash
   # All resources
   kubectl get all -A > /backup/all-resources-before.txt
   
   # Application list
   kubectl get applications.apps.cozystack.io -A
   
   # Tenant list
   kubectl get tenants -A
   ```

4. **Identify Usage of Affected Components**
   ```bash
   # FerretDB
   kubectl get pods -A | grep ferretdb
   
   # SeaweedFS
   kubectl get pods -A | grep seaweedfs
   
   # Custom applications
   kubectl get applications.apps.cozystack.io -A
   ```

## 🚨 Risks Assessment

### High Risk Factors

1. **Large Version Gap** (9 versions)
   - Risk: Accumulated breaking changes
   - Mitigation: Incremental upgrade, validation at each step

2. **FerretDB Data Migration**
   - Risk: Data loss
   - Mitigation: Complete backup, test migration first

3. **SeaweedFS Changes**
   - Risk: Storage access issues
   - Mitigation: Backup data, verify endpoints

4. **Age of Installation** (219 days)
   - Risk: Configuration drift
   - Mitigation: Document current state, compare with defaults

5. **Production Cluster**
   - Risk: Service disruption
   - Mitigation: Maintenance window, rollback plan

### Medium Risk Factors

1. **Migration Scripts**
   - Risk: Script failures
   - Mitigation: Manual verification after each step

2. **CRD Changes**
   - Risk: API compatibility
   - Mitigation: Review CRD changes, update clients

3. **Network/Storage Changes**
   - Risk: Connectivity issues
   - Mitigation: Monitor during upgrade

## 🎯 Recommendations

### Option A: Incremental Upgrade (Recommended)

**Pros**:
- ✅ Safest approach
- ✅ Rollback possible at each step
- ✅ Breaking changes handled individually
- ✅ Validation at each milestone

**Cons**:
- ⏱️ Time-consuming (3-4 days)
- 🔧 Requires careful monitoring
- 📚 Complex execution

**Timeline**: 15-20 hours across 3-4 days

### Option B: Fresh Install (Alternative)

**Pros**:
- ✅ Clean slate (v0.37.2 from start)
- ✅ No migration issues
- ✅ Latest features immediately
- ✅ Parallel migration possible

**Cons**:
- ⏱️ Longer total time (1 week)
- 🔄 Data migration needed
- 🔧 Workload migration complex

**Timeline**: 1 week total (install + migrate)

### Option C: Hybrid Approach (Recommended for Production)

**Strategy**:
1. Install fresh v0.37.2 cluster (parallel to existing)
2. Test Proxmox integration on new cluster
3. Migrate workloads gradually
4. Keep old cluster as backup
5. Decommission old after validation

**Pros**:
- ✅ Safest for production
- ✅ No downtime during migration
- ✅ Easy rollback (keep both)
- ✅ Test new version thoroughly

**Cons**:
- 💰 Requires additional resources temporarily
- ⏱️ Longest timeline (1-2 weeks)
- 🔧 Most complex coordination

**Timeline**: 1-2 weeks

## 📊 Decision Matrix

| Factor | Incremental (A) | Fresh Install (B) | Hybrid (C) |
|--------|----------------|-------------------|------------|
| **Risk** | Medium | Low | Very Low |
| **Time** | 3-4 days | 1 week | 1-2 weeks |
| **Complexity** | High | Medium | Very High |
| **Downtime** | Yes (4 days) | Minimal | None |
| **Rollback** | At each step | To old cluster | Keep both |
| **Cost** | Low | Low | High (2x resources) |
| **Data Safety** | Requires care | Full migration | Safest |

## 💡 Our Recommendation

**For Proxmox Integration Project**: **Option C (Hybrid)**

### Rationale

1. **Current cluster is 219 days old** with large version gap
2. **Proxmox integration needs testing** on latest version anyway
3. **Production stability** is critical (mgr.cp.if.ua)
4. **Time available** due to proxmox-lxcri project priority
5. **Risk mitigation** - can keep both clusters

### Proposed Plan

```
Week 1: Install fresh v0.37.2 cluster
  Day 1-2: Install CozyStack v0.37.2 on new VMs
  Day 3-4: Configure paas-proxmox bundle
  Day 5: Test basic functionality

Week 2: Proxmox Integration Testing
  Day 1-3: Test VM creation via CAPI
  Day 4-5: Test storage, networking

Week 3: Migration Planning
  Day 1-2: Document migration plan
  Day 3-5: Prepare migration scripts

Week 4+: Gradual Migration
  - Migrate non-critical workloads
  - Test thoroughly
  - Keep old cluster as backup
  - Decommission after 30 days
```

### Benefits for Proxmox Project

- ✅ Test integration on latest version (v0.37.2)
- ✅ No risk to existing cluster
- ✅ Clean baseline for testing
- ✅ Can validate paas-proxmox bundle properly
- ✅ Avoids FerretDB/SeaweedFS migration issues

## 🚦 Go/No-Go Decision

### Prerequisites for ANY Upgrade Approach

**MUST HAVE** ✅:
- [ ] Complete ETCD backup
- [ ] All critical data backed up
- [ ] FerretDB migration plan (if used)
- [ ] SeaweedFS backup (if used)
- [ ] Maintenance window scheduled
- [ ] Stakeholders notified
- [ ] Rollback plan documented
- [ ] Health check passed

**SHOULD HAVE** ⚠️:
- [ ] Test environment available
- [ ] Migration tested in dev
- [ ] Team trained on procedures
- [ ] Monitoring setup ready

**BLOCKERS** 🛑:
- ❌ No backup available
- ❌ Critical pods failing
- ❌ Storage issues present
- ❌ No rollback plan
- ❌ FerretDB in use without migration plan

## 📝 Next Steps

### Immediate Actions (TODAY)

1. **DECIDE**: Which approach to take (A, B, or C)
2. **ASSESS**: Check if FerretDB/SeaweedFS are in use
3. **BACKUP**: Create complete backup regardless of approach
4. **PLAN**: Schedule detailed timeline

### This Week

1. If Option A: Begin incremental upgrade
2. If Option B/C: Plan fresh installation
3. Continue with approach based on decision

### User Decision Required

**QUESTION**: Which approach do you prefer?

- **Option A**: Incremental upgrade (3-4 days, medium risk)
- **Option B**: Fresh install (1 week, low risk)
- **Option C**: Hybrid (1-2 weeks, lowest risk) - **RECOMMENDED**

Please advise which path to take before proceeding.

---

**Status**: AWAITING DECISION  
**Priority**: CRITICAL  
**Blocker**: Must decide approach before proceeding  
**Next Update**: After decision made

