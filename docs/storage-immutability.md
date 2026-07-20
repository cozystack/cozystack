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

- `clickhouse`, `foundationdb`, `harbor`, `http-cache`, `kafka` (Kafka + ZooKeeper), `kubernetes` (top-level only — see below), `mariadb`, `mongodb`, `nats`, `openbao`, `opensearch`, `postgres`, `qdrant`, `rabbitmq`, `redis`, `valkey`, `vm-disk`.

The per-node-group `kubernetes.nodeGroups[name].storageClass` field is intentionally NOT annotated immutable: it is optional and undefaulted, so a strict `self == oldSelf` rule would block any future attempt to set it on an existing node group. If a richer ratcheting form (`!has(oldSelf.storageClass) || self.storageClass == oldSelf.storageClass`) becomes available, the annotation should be added back.

If you add a new stateful chart that exposes `storageClass`, annotate it the same way:

```yaml
## @param {string} storageClass - StorageClass used to store the data.
## @immutable
storageClass: ""
```

For fields declared inside a `@typedef`, place `## @immutable` on the line below the `## @field` it should attach to.
