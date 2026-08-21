# migration-controller

Reconciles `migration.cozystack.io`: `VMImportSource` connections and the `VMImportTask` operations that run over them. Konveyor Forklift is the transfer engine underneath; this controller owns the tenant-facing API, drives Forklift's objects, and turns each finished transfer into a Cozystack `VMDisk` and `VMInstance`.

Opt-in. Add all three packages to `bundles.enabledPackages`:

```yaml
bundles:
  enabledPackages:
  - cozystack.forklift-operator
  - cozystack.forklift
  - cozystack.migration-controller
```

## Platform configuration

One key, and only for VMware:

```yaml
vmImport:
  vddkImage: registry.example.com/vddk:8.0.3
```

Cozystack neither ships nor mirrors that image: it is built from VMware's proprietary Virtual Disk Development Kit, which we have no licence to redistribute. An operator who holds one builds it, pushes it where the cluster can pull from, and names it here. Only the reference travels to the controller — never a credential.

Leaving it empty is a normal, supported state. A vSphere `VMImportSource` then reports `Ready=False` with reason `VDDKNotConfigured` at the moment it is created, so a tenant learns the path is unavailable at registration rather than watching a transfer die halfway through. Nothing is materialized for such a source — not even its credentials Secret.

## What a tenant creates

A connection, reusable across imports:

```yaml
apiVersion: migration.cozystack.io/v1alpha1
kind: VMImportSource
metadata:
  name: vcenter-prod
  namespace: tenant-foo
spec:
  type: vsphere
  url: https://vcenter.example.com/sdk
  credentials:
    username: mtv-migration@vsphere.local
    password: "..."
    caCert: |
      -----BEGIN CERTIFICATE-----
      ...
      -----END CERTIFICATE-----
```

Then an import, which is one-shot:

```yaml
apiVersion: migration.cozystack.io/v1alpha1
kind: VMImportTask
metadata:
  name: import-web-tier
  namespace: tenant-foo
spec:
  sourceRef:
    name: vcenter-prod
  vms:
  - id: vm-1234          # managed-object reference, from the vSphere client URL or `govc ls -i`
    name: web-01
  storageClass: replicated
```

Deleting the task removes the migration machinery and leaves `web-01` and its disks in place: they are ordinary Cozystack objects from the moment they exist, and carry no owner reference back to the task. Deleting the source deregisters the connection — its Forklift Provider and projected Secret go with it, and nothing already imported is touched.

Created VMs start `Halted`, not running. A freshly imported guest usually needs its network reconfigured, and booting it the instant the transfer finishes can put a second copy of a still-running production machine on the network.

## TLS: caCert or insecureSkipVerify, not a thumbprint

Exactly one of these must be set, and the API rejects the object at admission otherwise.

`caCert` is the PEM certificate authority that signed the endpoint's certificate. `insecureSkipVerify: true` turns verification off — convenient in a lab and a poor idea anywhere else, since the credentials above travel over that same connection.

A SHA-1 **thumbprint does not work here**, and this is worth stating plainly because the engine's own error is misleading. Forklift's vSphere provider validation requires `user`, `password`, and either a `cacert` key or `insecureSkipVerify`; given a thumbprint instead it marks the provider `SecretNotValid` with the message "The secret is not valid", which reads like a credentials problem and is not. A thumbprint is what the engine wants for a *direct ESXi host* connection, a path this version does not expose.

## vCenter privileges

Neither `Administrator` nor a plain read-only role is right. Read-only cannot do the job, and Administrator hands the cluster a credential whose blast radius is the entire vCenter — and that credential lands in a Secret in the tenant's own namespace.

Clone the read-only role, add the privileges Forklift documents, and assign it to a service account created for the migration campaign and disabled afterwards. Scope it to the datacenter being migrated, propagated to children — not the whole vCenter.

Three of the required operations are writes rather than reads, which is why read-only is not enough:

- `VirtualMachine.Provisioning.DiskRandomRead` — how the VDDK reads disk data.
- Powering off the source VM at cutover.
- Snapshots and changed-block tracking, for warm migration (not offered in this version, but the same role usually covers both).

Note that the VDDK opens NFC connections to the **ESXi hosts** on TCP 443 and 902, not only to vCenter. That is ordinary client egress from the tenant's namespace and the existing tenant network policy permits it, but it does mean the hosts themselves must be reachable from the cluster.

*The privilege detail above comes from the operational work on the original VMware import implementation, validated against a real vCenter.*

## Storage

Every disk of a task lands on `spec.storageClass`, or the cluster default when it is omitted. Either way the class must bind `Immediate`.

A `WaitForFirstConsumer` class does not fail an import, it **hangs** it: nothing consumes the claim while CDI populates it, so the transfer waits forever. The controller checks the binding mode before creating anything and fails the task with a message naming the class. On a stock Cozystack install the default class is `local`, which is `WaitForFirstConsumer` — so a task that names no class fails immediately with that explanation. `replicated` qualifies.

The transferred volume is handed to its `VMDisk` without being copied: the `PersistentVolume` is retained and re-bound into the claim the disk expects. That volume stays on `Retain` permanently and this is part of the contract, not an implementation detail — CDI takes a controller owner reference on an adopted claim, so deleting the DataVolume garbage-collects the claim, and only the reclaim policy keeps the data.

## Privilege model

Nothing on the transfer path is privileged. With guest conversion off — the only mode this version offers — no Forklift pod moves the bytes at all: Forklift emits a CDI DataVolume with a VDDK source and CDI's own importer does the work, and that pod satisfies the `restricted` Pod Security Standard. Verified against Forklift v2.11.5 and CDI v1.64.0, and confirmed on a live cluster by importing into a namespace enforcing `restricted` with no admission denial.

Guest conversion is a separate matter: it needs a node-level seccomp profile, because libguestfs starts `passt`, which unconditionally creates its own namespaces. It is deliberately not part of this version.

Tenants get read and write on both kinds through the standard aggregation labels, and no access at all to `forklift.konveyor.io` — the Forklift objects a task builds are internal machinery.

## Uninstalling

Remove the `ForkliftController` operand before the operator, or CRD deletion deadlocks on the operand's finalizer with nothing left running to clear it:

```sh
kubectl patch forkliftcontroller -n cozy-forklift forklift-controller \
  --type merge -p '{"metadata":{"finalizers":null}}'
```
