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

- `clickhouse`, `foundationdb`, `harbor`, `http-cache`, `kafka` (Kafka + ZooKeeper), `kubernetes`, `kubernetes-nodes`, `mariadb`, `mongodb`, `nats`, `openbao`, `opensearch`, `postgres`, `qdrant`, `rabbitmq`, `redis`, `vm-disk`.

The `kubernetes-nodes` chart annotates both its per-pool `storageClass` (the worker node system-disk class, defaulted `replicated`) and its `cluster` field immutable. `storageClass` binds the same PVC-template contract as every other chart in the table, so the plain `self == oldSelf` rule applies for the reason in "Why" above. `cluster` is immutable for a different reason: it wires the pool's CAPI objects to one parent cluster `kubernetes-<cluster>`, and repointing a live pool at another cluster would orphan its running worker VMs rather than migrate them.

If you add a new stateful chart that exposes `storageClass`, annotate it the same way:

```yaml
## @param {string} storageClass - StorageClass used to store the data.
## @immutable
storageClass: ""
```

For fields declared inside a `@typedef`, place `## @immutable` on the line below the `## @field` it should attach to.
