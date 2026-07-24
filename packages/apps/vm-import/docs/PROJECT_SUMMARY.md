# VMware → Cozystack VM Import — Project Summary

End-to-end import of VMware vSphere virtual machines into Cozystack, built on
[Forklift / Konveyor](https://github.com/kubev2v/forklift) and a dedicated
`vm-adoption-controller` that wraps each migrated VM as a managed Cozystack
`VMInstance`.

This document summarizes the whole effort: architecture, the two migration
paths, the API, the fixes made, what was validated on a live cluster, and the
remaining work. For step-by-step operator instructions see
[`MIGRATION_GUIDE.md`](./MIGRATION_GUIDE.md).

---

## 1. Goal

Let a Cozystack tenant import an existing vSphere VM through a single
declarative `VMImport` resource and end up with a dashboard-managed
`VMInstance` — disk data, firmware (BIOS/UEFI), instance type and networks
carried over, with no manual `kubectl` plumbing.

---

## 2. Components

| Component | Location | Role |
|-----------|----------|------|
| **forklift-operator** | `packages/system/forklift-operator` | Deploys Forklift (controller, API, validation, volume-populator) into `cozy-forklift`. |
| **vm-import** (app) | `packages/apps/vm-import` | User-facing chart. Renders the Forklift `Provider`/`NetworkMap`/`StorageMap`/`Plan`/`Migration` (+ optional `Host`) from a `VMImport`. |
| **vm-import-rd** | `packages/system/vm-import-rd` | `ApplicationDefinition` that exposes `VMImport` as a Cozystack CRD. |
| **vm-adoption-controller** | `packages/system/vm-adoption-controller` | Watches Forklift-created VMs and turns each into a Cozystack `VMInstance`. |
| **vm-instance** (app) | `packages/apps/vm-instance` | Existing managed-VM chart; extended to accept imported (PVC-backed) disks. |

### Flow

```
VMImport (tenant)
   │  vm-import chart
   ▼
Forklift Provider + NetworkMap + StorageMap + Plan + Migration  (+ Host override)
   │  Forklift
   ▼
Disk transfer from vSphere ──► PVC in the target namespace
   │  vm-adoption-controller
   ▼
VMInstance  ──► HelmRelease (vm-instance)  ──► running KubeVirt VM (dashboard-managed)
```

---

## 3. Two migration paths

The disk transfer mode determines almost everything downstream.

| | **virt-v2v** (guest conversion) | **VDDK raw-copy** |
|---|---|---|
| `skipGuestConversion` | `false` (default) | `true` |
| How the disk is read | through **vCenter** HTTP | **directly from the ESXi host** (NBD/NFC) |
| Guest drivers | injected by virt-v2v (virtio) | must already be present in the guest |
| Privileged pod | **yes** — libguestfs/`passt` needs a user namespace | **no** |
| Network requirement | reach vCenter only | reach vCenter **and** each ESXi host on **443 + 902** |
| Extra image | — | a **VDDK init image** (`vddkInitImage`) |
| Adoption | conversion in a privileged ns → **cross-namespace clone** into the tenant | Forklift copies **straight into the tenant** → **in-place** adoption |
| Relative speed | slower (conversion + clone) | ~2× faster (single copy) |

**Why the difference matters.** virt-v2v runs a privileged conversion pod that a
`baseline` tenant forbids, so conversion must happen in a privileged namespace
(`cozy-vm-import`) and the result is then cloned into the tenant. VDDK needs no
privileged pod, so Forklift can write the disk directly into the tenant and the
controller adopts it in place — avoiding the second full-disk copy.

---

## 4. The `VMImport` API

```yaml
apiVersion: apps.cozystack.io/v1alpha1
kind: VMImport
metadata:
  name: my-vm
  namespace: tenant-acme
spec:
  # --- VMware source ---
  sourceUrl: "https://vcenter.example.com/sdk"
  sourceSecretName: "vcenter-credentials"     # user / password / thumbprint

  # --- migration plan ---
  skipGuestConversion: true                   # VDDK raw-copy (vs virt-v2v)
  vddkInitImage: "registry.example.com/forklift-vddk:8.0.3"
  virtV2vImage: ""                            # escape hatch for image mismatches
  xfsCompatibility: false                     # work around the --no-fstrim bug
  warm: false

  vms:
    - id: vm-52                               # vSphere managed object ref
      name: my-vm                             # optional target name

  # --- adoption ---
  enableAdoption: true
  tenantNamespace: ""                         # tenant to adopt into (default: own ns)
  instanceType: "u1.medium"                   # optional sizing preset for the VMInstance
  instanceProfile: "centos.stream9"           # optional guest-OS preference

  # --- mappings ---
  networkMap:
    - sourceId: network-20
      destinationType: pod                    # or multus (+ destinationName/Namespace)
  storageMap:
    - sourceId: datastore-14
      storageClass: replicated-async

  # --- migration network (VDDK only) ---
  migrationHosts:                             # per-ESXi-host transfer IP override
    - id: host-10
      ipAddress: 10.31.0.29
      secretName: esxi-host-10                # user / password / thumbprint / insecureSkipVerify
```

Required: `sourceUrl`, `sourceSecretName`. Per-entry required: `vms[].id`,
`migrationHosts[].{id,ipAddress,secretName}`, `storageMap[].{sourceId,storageClass}`,
`networkMap[].{sourceId,destinationType}`.

---

## 5. Adoption controller

`vm-adoption-controller` polls Forklift-labelled VMs (`plan: <Plan-UID>`) every
15 s and, once the migration has succeeded, creates a `VMInstance`:

- **Plan resolution is cluster-wide** — the Plan may live in a different
  namespace than the migrated VM (the direct-target VDDK case). It is matched
  by UID, and the Plan's namespace is used for the migration-complete and
  adoption-enabled checks.
- **Target namespace** comes from the Plan annotation
  `vm-import.cozystack.io/target-namespace`.
  - target == VM namespace → **in-place** adoption (label the existing VM/DV for
    Helm take-over, create the `VMInstance` with `fullnameOverride`).
  - target != VM namespace → **cross-namespace clone** (CDI clone the disk into
    the tenant, then create the `VMInstance` there).
- Carries over **firmware** (`bootloader: bios|uefi`, `secureBoot`), instance
  type, preference, run strategy, disks and Multus networks. Disks are extracted
  from both `dataVolume` and `persistentVolumeClaim` volumes.
- **Instance type / preference precedence**: the `VMImport` preset
  (`instanceType` / `instanceProfile`, passed via Plan annotations) wins over the
  value detected on the source VM, which in turn wins over the controller's
  default flags.

The Helm release / RBAC for the controller lives in
`packages/system/vm-adoption-controller` (note the `.helmignore` that keeps the
build artifacts in `images/` out of the chart package).

---

## 6. Performance (16 GiB AlmaLinux 9 VM, `replicated-async` / LINSTOR-DRBD)

Measured end-to-end from a clean reinstall on a live lab:

| Step | virt-v2v + clone | **VDDK direct-target** |
|------|------------------|------------------------|
| Scheduling + Initialize | ~33 s | ~30 s |
| Disk transfer (16 GiB) | 2 m 02 s | 2 m 23 s |
| VM creation (Forklift) | 4 s | 3 s |
| Cross-namespace clone (16 GiB) | **+2 m 52 s** | — (none) |
| **Total → data in tenant** | **≈ 5 m 46 s** | **≈ 2 m 55 s** |

The clone re-copies the whole disk a second time across the 3-replica DRBD
storage, so eliminating it (VDDK direct-target) roughly halves wall-clock.

---

## 7. Fixes made

**forklift-operator**
- Inject `WATCH_NAMESPACE=cozy-forklift` (otherwise the Ansible role defaults to
  `konveyor-forklift` and the controller lands outside its RBAC, breaking install).
- Substitute the unresolved `${VIRT_V2V_DONT_REQUEST_KVM}` placeholder.

**vm-import chart**
- Destination `openshift` provider needs `secret: {}`.
- Source provider conditionally sets `settings.vddkInitImage`.
- New fields: `vddkInitImage`, `skipGuestConversion`, `virtV2vImage`,
  `xfsCompatibility`, `tenantNamespace`, `migrationHosts`.
- `xfsCompatibility` works around an upstream Forklift bug where the conversion
  entrypoint passes `--no-fstrim` to a `virt-v2v-inspector` that rejects it,
  failing the migration after a successful disk copy.
- `migrationHosts` renders a Forklift `Host` to override the disk-transfer IP
  when the vCenter-advertised ESXi IP is not routable from the cluster.
- `Plan.spec.targetNamespace` points straight at `tenantNamespace` for the VDDK
  path (single copy), and stays in the privileged namespace for virt-v2v.

**vm-adoption-controller**
- Match the real Forklift label `plan: <Plan-UID>` (release-2.11+) instead of
  `forklift.konveyor.io/plan`, resolving the UID to the Plan **cluster-wide**.
- Extract disks from `persistentVolumeClaim` volumes (Forklift-created VMs), not
  only `dataVolume`.
- Cross-namespace clone + in-place adoption; firmware mapping; RBAC for CDI
  cross-namespace clone (`datavolumes`, `datavolumes/source`, `pvc` read).
- `.helmignore` so the chart package excludes `images/` (50 MB Go binary).

**vm-instance chart**
- Accept **PVC-backed disks**: CDI garbage-collects the DataVolume after import,
  leaving only a PVC, so the chart now falls back to a `persistentVolumeClaim`
  reference (device + volume sections) when no DataVolume of that name exists.

---

## 8. Validated on a live cluster (Hikube-Lab)

- Real vSphere VM (`test-matthieu`, AlmaLinux 9, 16 GiB, UEFI) imported end to end.
- **virt-v2v path**: conversion in privileged `cozy-vm-import` (with a seccomp
  `Unconfined` webhook for the conversion pod) → cross-namespace clone → VMInstance.
- **VDDK path**: routable ESXi IP + `Host` override → single copy directly into
  `tenant-vmlab` → in-place adoption. No vCenter/ESXi credentials leaked into the
  tenant; the importer runs fine under the tenant's PodSecurity.
- vm-instance PVC fallback validated with `helm install --dry-run=server`
  against the real imported PVC (renders `persistentVolumeClaim` + bootable disk).

---

## 9. Known limitations & dependencies

- **UEFI firmware on the managed VMInstance** depends on upstream PR
  [#3002](https://github.com/cozystack/cozystack/pull/3002) (kvaps) which adds
  `firmware.bootloader` support to `vm-instance`. The controller already emits
  the firmware; it is a no-op until that lands.
- **Lab platform chart**: the test cluster runs a released `vm-instance` chart
  that predates `dvName`/PVC support, so the final `HelmRelease` wrap fails there
  (`Specified disk not exists`). Repo HEAD is correct (validated via dry-run).
- **VDDK requires cluster→ESXi connectivity** on 443 + 902, and the ESXi host
  must be healthy (`green`) in vCenter for the `Host` override to be honored
  (restart `forklift-controller` after clearing a stale `yellow` state).
- **Custom cluster DNS**: Forklift hardcodes `svc.cluster.local`; clusters with a
  different domain need a `hostAliases` workaround on `forklift-controller`.

---

## 10. Remaining productization

- Merge / rebase on #3002 for UEFI boot of adopted VMs.
- Ship the seccomp-`Unconfined` mechanism for virt-v2v conversion pods (webhook,
  or an upstream Forklift securityContext knob) so the virt-v2v path works
  without manual setup.
- Helm resource ordering so `Migration` is never applied before its
  `Plan`/`Provider` (benign under Flux retries, breaks plain `helm install`).
- Upstream the `--no-fstrim` / `xfsCompatibility` fix to Forklift.
