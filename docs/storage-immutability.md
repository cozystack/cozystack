# `storageClass` is immutable after creation

Every stateful Cozystack application that exposes a `storageClass` parameter declares it immutable in the chart schema (`x-kubernetes-validations: [{rule: "self == oldSelf"}]`). This document explains the contract and where it is enforced.

## Why

`storageClass` binds the StatefulSet's PVC template. Kubernetes fixes `PersistentVolumeClaim.spec.storageClassName` at PVC creation time — editing `volumeClaimTemplates[].spec.storageClassName` on an existing StatefulSet does not retroactively migrate any data. A user who edits `storageClass` on an existing resource may believe their data is being moved to the new class; it is not. Locking the field at the schema layer makes that contract explicit.

## What enforces it

| Consumer | Behavior |
| --- | --- |
| Cozystack UI (`packages/system/dashboard/images/console`) | Reads the CEL rule from the chart's `openAPISchema` and renders `storageClass` as a disabled, helper-text-annotated field on edit forms. Save-time overlay reinstates the original value before PUT. |
| `kubectl edit` / `kubectl patch` against the cozystack aggregated apiserver | **Currently accepted** — the apiserver does not yet evaluate CEL rules embedded in `ApplicationDefinition.openAPISchema`. The change passes through but does not propagate to existing PVCs (see "Why" above), so no data corruption is possible. Apiserver enforcement is tracked in cozystack/cozystack#2657. |
| `kubectl edit` / `kubectl patch` against native CRDs | Enforced today by the standard apiextensions apiserver. |

## Apps covered

The following charts annotate at least one `storageClass` field as immutable:

- `clickhouse`, `foundationdb`, `harbor`, `http-cache`, `kafka` (Kafka + ZooKeeper), `kubernetes`, `kubernetes-nodes`, `mariadb`, `mongodb`, `nats`, `openbao`, `opensearch`, `postgres`, `qdrant`, `rabbitmq`, `redis`, `valkey`, `vm-disk`.

The `kubernetes-nodes` chart annotates both its per-pool `storageClass` (the worker node system-disk class, defaulted `replicated`) and its `cluster` field immutable. `storageClass` binds the same PVC-template contract as every other chart in the table, so the plain `self == oldSelf` rule applies for the reason in "Why" above. `cluster` is immutable for a different reason: it wires the pool's CAPI objects to one parent cluster `kubernetes-<cluster>`, and repointing a live pool at another cluster would orphan its running worker VMs rather than migrate them.

## `vm-disk` also freezes `source`

The `vm-disk` chart annotates `source` immutable in addition to `storageClass`, for a mechanism distinct from the PVC-template reasoning above. A `vm-disk` renders a single `DataVolume`, and `DataVolume.spec` is immutable in CDI: once the DataVolume exists, editing `spec.source` (or `spec.storage.storageClassName`) has no effect. The chart therefore looks the existing DataVolume up and reuses its stored `spec`, so an edit to `.Values.source` would otherwise render as a silent no-op while the HelmRelease still reported success — the CR and the cluster diverge with no diagnostic (cozystack/cozystack#2985).

Because the aggregated apiserver does not yet evaluate the CEL rule (see the table above, cozystack/cozystack#2657), `vm-disk` adds a second, chart-level guard that is the substantive enforcement today: `templates/dv.yaml` fails the render when the requested `storageClass` or `source` differs from what the disk was created with. Both checks compare the operator's input recorded in an annotation at creation — `vm-disk.cozystack.io/storage-class` and `vm-disk.cozystack.io/source` — written only at creation and preserved verbatim afterward, never overwritten with an unapplied edit. The storage-class annotation is written even for the cluster default (`""`), and its key presence — not its value — marks a recorded class, so a disk created with `""` is still caught if later changed to a concrete class. Recording the input rather than reading the live object matters on both axes: the image-clone PVC name carries a chart-version prefix (`vm-image-` was renamed to `vm-default-images-`) that drifts across upgrades with no operator edit, and the live PVC's `storageClassName` on an adopted or foreign DataVolume may legitimately differ from the CR's default without being a change.

Disks created before these annotations existed (or out-of-band) are handled conservatively so a working release never deadlocks. For `source`, such a disk is compared against its stored `spec.source` directly, but only when that stored source is a shape the chart itself produces (`blank`, `http` with just a `url`, `upload`, or a `pvc` with just `name`/`namespace`). An adopted DataVolume using `spec.sourceRef`, or a source type the chart cannot render (`registry`, `snapshot`, `imageio`, `http` with a `secretRef`, a foreign-namespace `pvc`), has no comparable provenance and is left alone rather than failed on a mismatch. The image-clone exemption fires only when the stored source is a `cozy-public` `pvc` **and** the request is itself an image — the one case where the `vm-image-` → `vm-default-images-` prefix drift is indistinguishable from a real image-name change; a change away from an image source to another type is still caught. For `storageClass` there is no safe live fallback (the CR default need not reflect an adopted disk's real class), so an un-annotated disk is simply not guarded here; apiserver CEL (cozystack/cozystack#2657) is the eventual guard for those. To actually change `storageClass` or `source`, delete and recreate the disk; size growth is the one mutable dimension and is handled separately by `templates/pvc-resize-hook.yaml`.

The `storageClass` annotation records the effective value applied at creation, including when it came from the chart's `values.yaml` default (`replicated`) rather than an explicit operator value. Applications created through the aggregated apiserver have the default stamped into the CR, so it stays stable; but a direct HelmRelease consumer that omits `storageClass` records the chart default of the day, so changing that default in a future chart version would make every such untouched release start failing the render until its CR pins the old value or the disk is recreated. Treat the `storageClass` default as a frozen contract for the same reason the `source` serialization above is.

The `source` annotation is a byte-for-byte `toJson` of `.Values.source`, so it is coupled to the exact serialization of the source schema. Adding a subfield with a schema default (which the apiserver would stamp onto every CR) would make the recorded and requested JSON diverge on an untouched disk; such a change needs an annotation-migration story rather than a bare schema bump.

If you add a new stateful chart that exposes `storageClass`, annotate it the same way:

```yaml
## @param {string} storageClass - StorageClass used to store the data.
## @immutable
storageClass: ""
```

For fields declared inside a `@typedef`, place `## @immutable` on the line below the `## @field` it should attach to.
