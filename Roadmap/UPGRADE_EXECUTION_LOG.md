# CozyStack Upgrade Execution Log

**Date Started**: 2025-10-24  
**Cluster**: mgr.cp.if.ua  
**Executor**: System Administrator

## Current State Assessment

### Version Detection

**Current Version**: `v0.28.0-54-g22cf18ff`
- Base version: v0.28.0
- Additional commits: 54
- Git hash: 22cf18ff
- Installation date: ~March 19, 2025

**Target Version**: v0.37.2 (latest stable)

**Version Gap**: 9 minor versions (0.28 → 0.37)

### Component Versions

```
cozy-proxy:           v0.1.4
cozystack-api:        v0.29.1
cozystack-controller: (checking)
```

### Upgrade Path Required

Due to large version gap (v0.28.0 → v0.37.2), we need careful incremental approach:

```
v0.28.0 (current)
    ↓
v0.31.0 (milestone 1)
    ↓
v0.32.1 (milestone 2)
    ↓
v0.33.2 (milestone 3)
    ↓
v0.34.8 (milestone 4)
    ↓
v0.35.5 (milestone 5)
    ↓
v0.36.2 (milestone 6)
    ↓
v0.37.2 (target)
```

**Total Steps**: 7 incremental upgrades

**Estimated Time**: 
- Conservative: 25-30 hours (4-5 days)
- Realistic: 15-20 hours (3 days)

### Available Changelogs

Located in `/home/moriarti/repo/cozystack/docs/changelogs/`:

```
✅ v0.31.0.md
✅ v0.31.1.md
✅ v0.31.2.md
✅ v0.32.0.md
✅ v0.32.1.md
✅ v0.33.0.md
✅ v0.33.1.md
✅ v0.33.2.md
✅ v0.34.0.md
✅ v0.34.1.md
✅ v0.34.2.md
✅ v0.34.3.md
✅ v0.34.4.md
✅ v0.34.5.md
✅ v0.34.6.md
✅ v0.34.7.md
✅ v0.34.8.md
✅ v0.35.0.md
✅ v0.35.1.md
✅ v0.35.2.md
❌ v0.35.3+ (need to check git)
❌ v0.36.x (need to check git)
❌ v0.37.x (need to check git)
```

### Critical Considerations

**WARNING**: Large version gap presents risks:

1. **Breaking Changes Accumulation**
   - 9 minor versions = high probability of breaking changes
   - API changes may be incompatible
   - CRD schema changes possible

2. **Database Migrations**
   - ETCD schema may have changed
   - Data migrations may be required
   - Backup is CRITICAL

3. **Component Compatibility**
   - Kubernetes version compatibility
   - Helm version requirements
   - Go version changes

4. **Network/Storage Changes**
   - CNI changes (Cilium, Kube-OVN)
   - CSI changes
   - NetworkPolicy changes

### Recommendation

**OPTION A: Incremental (Recommended for Production)**
- Go through each minor version
- Validate at each step
- Rollback possible at any point
- Timeline: 3-5 days

**OPTION B: Direct (Risky, for Testing Only)**
- Jump directly to v0.37.2
- High risk of failures
- Difficult rollback
- Timeline: 1 day (if successful)

**OPTION C: Fresh Install (Safest for Major Gap)**
- Install v0.37.2 on new cluster
- Migrate workloads
- Keep old cluster as backup
- Timeline: 1 week

**Selected Approach**: **OPTION A** (Incremental)

## Pre-Upgrade Checklist

### [Step 1] ✅ Version Detection - COMPLETED
- [x] Current version: v0.28.0-54-g22cf18ff
- [x] Target version: v0.37.2
- [x] Upgrade path: 7 steps
- [x] Changelogs available: v0.31.0 → v0.35.2

### [Step 2] ⏳ Changelog Review - IN PROGRESS
- [ ] Review v0.31.x changes
- [ ] Review v0.32.x changes
- [ ] Review v0.33.x changes
- [ ] Review v0.34.x changes
- [ ] Review v0.35.x changes
- [ ] Review v0.36.x changes (from git)
- [ ] Review v0.37.x changes (from git)
- [ ] Document breaking changes
- [ ] Document required migrations

### [Step 3] ⏳ Backup Creation - PENDING
- [ ] Backup ETCD
- [ ] Backup ConfigMaps
- [ ] Backup Secrets
- [ ] Backup PVCs
- [ ] Backup HelmReleases
- [ ] Backup CRDs
- [ ] Backup Proxmox configuration
- [ ] Verify backup integrity

### [Step 4] ⏳ Health Check - PENDING
- [ ] All nodes Ready
- [ ] All pods Running/Completed
- [ ] No PVCs Pending
- [ ] ETCD healthy
- [ ] API server accessible
- [ ] Run integrity checks
- [ ] Document current issues

### [Step 5] ⏳ Preparation - PENDING
- [ ] Schedule maintenance window
- [ ] Notify stakeholders
- [ ] Prepare rollback plan
- [ ] Setup monitoring
- [ ] Clone repository at each version

## Upgrade Execution Steps

### Milestone 1: v0.28.0 → v0.31.0

**Status**: PENDING

**Preparation**:
```bash
cd /tmp
git clone https://github.com/cozystack/cozystack.git cozystack-v0.31.0
cd cozystack-v0.31.0
git checkout v0.31.0
```

**Execution**: TBD

### Milestone 2: v0.31.0 → v0.32.1

**Status**: PENDING

### Milestone 3: v0.32.1 → v0.33.2

**Status**: PENDING

### Milestone 4: v0.33.2 → v0.34.8

**Status**: PENDING

### Milestone 5: v0.34.8 → v0.35.5

**Status**: PENDING

### Milestone 6: v0.35.5 → v0.36.2

**Status**: PENDING

### Milestone 7: v0.36.2 → v0.37.2

**Status**: PENDING

## Issues Encountered

### Issue Log

**Format**: [Timestamp] [Severity] [Component] Description

(To be filled during upgrade)

## Rollback Log

### Rollback Events

(To be filled if rollback needed)

## Timeline

### Planned Timeline

```
Day 1 (8 hours):
  - Preparation: 2h
  - v0.28 → v0.31: 2h
  - Validation: 1h
  - v0.31 → v0.32: 2h
  - Validation: 1h

Day 2 (8 hours):
  - v0.32 → v0.33: 2h
  - Validation: 1h
  - v0.33 → v0.34: 2h
  - Validation: 1h
  - Monitoring: 2h

Day 3 (8 hours):
  - v0.34 → v0.35: 2h
  - Validation: 1h
  - v0.35 → v0.36: 2h
  - Validation: 1h
  - v0.36 → v0.37: 2h

Day 4 (4 hours):
  - Final validation: 2h
  - Documentation: 2h
```

### Actual Timeline

(To be filled during execution)

## Next Steps

### Immediate Actions Needed

1. **Review ALL changelogs** (HIGH PRIORITY)
   - Identify breaking changes
   - Note migration requirements
   - Document API changes

2. **Create comprehensive backup** (CRITICAL)
   - ETCD snapshot
   - All configurations
   - Verify backup restoration

3. **Run health check** (HIGH PRIORITY)
   - Current cluster state
   - Identify existing issues
   - Fix critical problems before upgrade

4. **Schedule maintenance window** (HIGH PRIORITY)
   - 4-day window recommended
   - Off-peak hours
   - Stakeholder notification

5. **Prepare infrastructure** (MEDIUM PRIORITY)
   - Clone repo at each version
   - Setup monitoring
   - Prepare rollback procedures

### Decision Points

**CRITICAL DECISION**: Should we proceed with incremental upgrade or consider fresh install?

**Factors to consider**:
- Age of cluster (219 days)
- Version gap (9 minor versions)
- Risk tolerance
- Downtime tolerance
- Data migration complexity

**Recommendation**: 
- If cluster has critical data: **Incremental upgrade**
- If can afford downtime: Consider **fresh install** of v0.37.2

### Questions to Answer

1. What critical workloads are running?
2. Can we afford 4-day maintenance window?
3. Do we have backup/DR procedures?
4. Are there any blockers for upgrade?
5. What is rollback time expectation?

## Status Summary

**Overall Status**: PREPARATION PHASE

**Completed**:
- ✅ Version detection
- ✅ Upgrade path planning

**In Progress**:
- ⏳ Changelog review

**Pending**:
- ⏳ Backup creation
- ⏳ Health check
- ⏳ All upgrade milestones

**Blockers**:
- Need to review changelogs for breaking changes
- Need to create backups before proceeding
- Need to assess cluster health

**Estimated Completion**: 4-5 days from start

---

**Log Started**: 2025-10-24  
**Last Updated**: 2025-10-24  
**Next Update**: After changelog review

