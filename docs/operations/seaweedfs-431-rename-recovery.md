# SeaweedFS 4.31 rename — audit & recovery

This runbook covers clusters affected by the SeaweedFS chart-rename regression introduced when the vendored chart was bumped from `4.0.405` to `4.31.0` (Cozystack v1.5.0). Use it to classify each tenant, and to recover the ones that need an operator before they can be upgraded.

## Background

Before 4.31 the chart named workloads after the chart (`seaweedfs-*`), ignoring the release name. 4.31 names them after the release, and the data-plane HelmRelease is `<name>-system`, so every StatefulSet wanted to become `seaweedfs-system-*`. StatefulSet names are immutable, so the upgrade could not rename in place — Helm stood up a second, duplicate set beside the running one. Depending on cluster size the duplicate either deadlocks or splits:

- **D-wedged** — with as many nodes as master replicas, the new masters cannot schedule (hard pod anti-affinity against the old masters). The new set stays `Pending`/`CrashLoopBackOff`, the old set keeps serving. No data at risk.
- **D-split** — with more nodes than masters, the new (empty) set comes up. Both sets carry identical pod labels, so the `seaweedfs-s3` Service load-balances across them, and both filers write to the **same `seaweedfs-db` Postgres** metadata store while pointing at different volume servers. This is a data-integrity incident, not just a duplicate: reads of existing objects through the new endpoint miss, new writes land on empty volumes, and the two master sets hand out volume IDs from independent sequences into one shared metadata table.

The fix pins `fullnameOverride: seaweedfs` in `system/seaweedfs` values, so workloads are always named after the chart, exactly as they were before 4.31. Upgrading past the bump therefore **adopts the running set and its volumes in place**.

One class cannot be adopted that way: a tenant installed **fresh on 1.5.x**, whose data was written under the release-based names and lives on `data1-seaweedfs-system-volume-*` PVCs. Pinning the chart name there would rename the workloads *away* from that data. Helm cannot move data between PVCs, so the charts refuse to render for such a tenant and point here. The **enforcing** guard lives in `system/seaweedfs` (`templates/naming-guard.yaml`) — the `<name>-system` HelmRelease pulls that chart straight from a platform-managed ExternalArtifact, so a platform upgrade re-renders it directly and nothing else stands between the upgrade and the tenant's workloads; `extra/seaweedfs` carries a sibling copy so the refusal is also visible on the SeaweedFS application itself. Re-bind the tenant's volumes (below) before upgrading.

## Step 0 — `seaweedfs-db` ownership check (read-only, do this FIRST)

Unrelated to the rename, but it lands on the same upgrade and it destroys data rather than duplicating it, so clear it before anything else.

The v1.5.0 db split moved the CNPG `Cluster/seaweedfs-db` — the filer metadata store, i.e. the index for every object in the tenant's S3 — out of the `<name>-system` release into its own `<name>-db` release. Migration 43 performs the hand-over: it re-owns the Cluster to `<name>-db` and stamps `helm.sh/resource-policy: keep` so the `<name>-system` upgrade, whose chart no longer renders the Cluster, does not delete it as a removed resource. **Migration 43 shipped comparing the owning release name against the literal `seaweedfs-system`**, so it only ever fired for an instance named `seaweedfs`. `SeaweedFS` is a user-creatable kind: an instance named `foo` is owned by `foo-system`, was skipped, and had its Cluster pruned — CNPG takes the PVC with it.

The prune is not a one-shot. Helm computes deletions by diffing the **last deployed** revision against the new manifest, so a tenant whose `<name>-system` last succeeded on a pre-split revision recomputes the same deletion on *every* upgrade attempt — including attempts that fail for unrelated reasons and never become the new deployed revision. Such a tenant re-deletes the Cluster each time `<name>-db` recreates it.

Migration 43 is fixed to match the `-system` suffix, and migration 53 re-runs the hand-over for clusters that already ran the hardcoded version. Both are pre-upgrade hooks, so they land before `<name>-system` re-renders. Audit anyway — a Cluster already deleted cannot be recovered by either:

```sh
kubectl get cluster.postgresql.cnpg.io -A \
  -o custom-columns='NS:.metadata.namespace,NAME:.metadata.name,OWNER:.metadata.annotations.meta\.helm\.sh/release-name,KEEP:.metadata.annotations.helm\.sh/resource-policy'
```

Read the rows for `NAME=seaweedfs-db`:

| OWNER | KEEP | Meaning |
|---|---|---|
| `<name>-db` | `keep` | Handed over. Nothing to do. |
| `<name>-db` | *(none)* | Installed fresh on ≥1.5.0. Safe: `<name>-system` never rendered the Cluster, so it is not in that release's prune baseline. |
| `<name>-system` | *(none)* | **At risk.** Migration 53 hands it over on the next upgrade. Do not reconcile `<name>-system` before the migration runs. |
| *(no row at all)* | | **Already lost** — see below. |

A tenant with a SeaweedFS instance but **no `seaweedfs-db` row** has already had its metadata deleted. Its S3 returns 500 and its objects are unreachable even though the volume PVCs still hold the bytes. Nothing in this runbook or in any migration can rebuild that index: the Cluster and its PVC are gone. Restore the `seaweedfs-db` Postgres from a backup, or treat the tenant's object storage as lost. Note that `<name>-db` may report **Ready** while this is true — it rendered its Cluster successfully and a later `<name>-system` prune removed it; Flux has not re-checked. Trust the `kubectl get cluster` output, not the HelmRelease status.

## Step 1 — Audit the fleet (read-only)

Run per cluster (`KUBECONFIG` pointed at each). It mutates nothing. Classification is driven by the **PVCs**, because they hold the data and outlive any workload.

```sh
#!/usr/bin/env bash
set -euo pipefail
printf '%-28s %-10s %s\n' NAMESPACE CLASS NOTE
printf '%-28s %-10s %s\n' --------- ----- ----
# A volume PVC is data1-<fullname>-volume[-<pool|zone>]-N. Pre-4.31 <fullname> was
# always the chart name, so legacy data is data1-seaweedfs-volume-*. On 4.31 it is
# the release name, plus the chart name when the release does not contain it:
# seaweedfs-system-* for the default instance, <name>-system-seaweedfs-* otherwise.
nss=$(kubectl get pvc -A -o jsonpath='{range .items[*]}{.metadata.namespace}{" "}{.metadata.name}{"\n"}{end}' \
        | awk '$2 ~ /^data1-.*volume/ && $2 ~ /seaweedfs/ {print $1}' | sort -u)
[ -z "${nss}" ] && { echo "(no SeaweedFS volume PVCs found)"; exit 0; }
for ns in ${nss}; do
  # name<TAB>creationTimestamp — the AGE ordering below tells a genuine D tenant
  # from an S tenant that an unguarded upgrade already damaged.
  pvcs=$(kubectl get pvc -n "$ns" -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.metadata.creationTimestamp}{"\n"}{end}')
  legacy=$(echo "$pvcs" | grep -cE '^data1-seaweedfs-volume' || true)
  system=$(echo "$pvcs" | grep -E '^data1-.*volume' | grep -E 'seaweedfs' | grep -vE '^data1-seaweedfs-volume' | grep -c . || true)
  legacy_oldest=$(echo "$pvcs" | grep -E '^data1-seaweedfs-volume' | cut -f2 | sort | head -1)
  system_oldest=$(echo "$pvcs" | grep -E '^data1-.*volume' | grep -E 'seaweedfs' | grep -vE '^data1-seaweedfs-volume' | cut -f2 | sort | head -1)
  running_new=$(kubectl get pods -n "$ns" \
      -l app.kubernetes.io/name=seaweedfs,app.kubernetes.io/component=volume \
      --field-selector=status.phase=Running -o name 2>/dev/null \
      | grep -vc '/seaweedfs-volume-' || true)
  if [ "$legacy" -gt 0 ] && [ "$system" -gt 0 ]; then
    # PVCs are never recreated in place, so the OLDER generation is where the
    # data was born. Renamed older ⇒ a fresh-1.5.x tenant that an unguarded
    # 1.6.0 upgrade already hit: the chart-named set is the NEWER, EMPTY one.
    # That tenant is S (recover via Step 2a + Step 2), NEVER D-split — the
    # D-split procedure would quiesce the set that holds all the data.
    if [ "$system_oldest" \< "$legacy_oldest" ]; then
      printf '%-28s %-10s %s\n' "$ns" "S-damaged" "renamed volumes are OLDER: data born there, chart-named set is empty (Step 2a, then Step 2)"
    elif [ "${running_new:-0}" -gt 0 ]; then
      printf '%-28s %-10s %s\n' "$ns" "D-split" "DANGER: duplicate volume servers Running — audit before upgrading"
    else
      printf '%-28s %-10s %s\n' "$ns" "D-wedged" "duplicate set never served; upgrade adopts the legacy set"
    fi
  elif [ "$legacy" -gt 0 ]; then
    printf '%-28s %-10s %s\n' "$ns" "L" "pre-1.5 naming; upgrade adopts in place, nothing to do"
  else
    printf '%-28s %-10s %s\n' "$ns" "S" "fresh 1.5.x install; re-bind volumes (Step 2) BEFORE upgrading"
  fi
done
```

Act on the classes in this order: resolve every **D-split**, **S** and **S-damaged** tenant first (all need an operator), then upgrade. **L** needs nothing.

**D-wedged** normally needs nothing — but on a cluster with no spare nodes (nodes ≤ replicas) the adoption rollout can stall: the wedged duplicate pods are still *scheduled*, carry the same labels as the adopted set (including `app.kubernetes.io/instance`), and their hard pod anti-affinity blocks the adopted set's rolled pods from landing anywhere (observed as `seaweedfs-filer-1`/`seaweedfs-master-2` stuck Pending on `didn't match pod anti-affinity rules`, wedging the `<name>-system` HelmRelease in upgrade/rollback loops). If that happens, scale the duplicate down first — it never served, so this is safe:

```sh
ns=<tenant>
kubectl -n "$ns" get sts -l app.kubernetes.io/name=seaweedfs -o name | sed 's|statefulset.apps/||' \
  | grep -vE '^seaweedfs-(master|filer|volume)($|-)' \
  | xargs -r -I{} kubectl -n "$ns" scale sts {} --replicas=0
```

## Step 2 — `S` tenants: re-bind the volumes before upgrading

The data is on `data1-seaweedfs-system-volume-N`; the fixed chart expects `data1-seaweedfs-volume-N`. Rather than copying objects, re-point the same PersistentVolume at a PVC with the new name. Nothing is written or moved; only the claim is renamed. Expect downtime for this tenant's S3.

```sh
ns=<tenant>

# 1. Stop the operator from fighting the change.
app=<seaweedfs-instance-name>   # the SeaweedFS resource name; `seaweedfs` unless you renamed it
kubectl -n "$ns" patch helmrelease "${app}-system" --type merge -p '{"spec":{"suspend":true}}'
# Select the renamed workloads precisely. An S tenant has only the renamed set,
# but exclude the pre-4.31 chart-named seaweedfs-master/-filer/-volume[-<key>]
# anyway so the same selector is reused in Step 3, where both sets coexist.
# seaweedfs-system-* and <name>-system-seaweedfs-* are kept.
renamed_sts() {
  kubectl -n "$ns" get sts -l app.kubernetes.io/name=seaweedfs -o name \
    | sed 's|statefulset.apps/||' | grep -vE '^seaweedfs-(master|filer|volume)($|-)'
}
for sts in $(renamed_sts); do
  kubectl -n "$ns" scale sts "$sts" --replicas=0
done
kubectl -n "$ns" scale deploy -l app.kubernetes.io/name=seaweedfs,app.kubernetes.io/component=s3 --replicas=0

# 2. For every volume PVC: protect its PV, then re-bind it under the new name.
for pvc in $(kubectl -n "$ns" get pvc -o name | sed 's|persistentvolumeclaim/||' \
               | grep -E '^data1-.*volume' | grep seaweedfs | grep -vE '^data1-seaweedfs-volume'); do
  # data1-<renamed>-volume[-<key>]-N  ->  data1-seaweedfs-volume[-<key>]-N
  new_pvc=$(echo "$pvc" | sed -E 's/^data1-.*-volume-/data1-seaweedfs-volume-/')
  pv=$(kubectl -n "$ns" get pvc "$pvc" -o jsonpath='{.spec.volumeName}')
  sc=$(kubectl -n "$ns" get pvc "$pvc" -o jsonpath='{.spec.storageClassName}')
  size=$(kubectl -n "$ns" get pvc "$pvc" -o jsonpath='{.spec.resources.requests.storage}')
  # Remember the PV's own reclaim policy: it is restored verbatim below, so a volume
  # the cluster deliberately set to Retain does not silently come back as Delete.
  reclaim=$(kubectl get pv "$pv" -o jsonpath='{.spec.persistentVolumeReclaimPolicy}')

  # Keep the PV (and the data) when the claim goes away.
  kubectl patch pv "$pv" -p '{"spec":{"persistentVolumeReclaimPolicy":"Retain"}}'
  kubectl -n "$ns" delete pvc "$pvc"
  # A Released PV cannot be re-bound until its old claimRef is cleared.
  kubectl patch pv "$pv" --type json -p '[{"op":"remove","path":"/spec/claimRef"}]'

  kubectl -n "$ns" apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ${new_pvc}
  namespace: ${ns}
spec:
  accessModes: ["ReadWriteOnce"]
  storageClassName: ${sc}
  volumeName: ${pv}
  resources:
    requests:
      storage: ${size}
EOF
  # Restore the PV's original reclaim policy now that the new claim owns it.
  kubectl patch pv "$pv" -p "{\"spec\":{\"persistentVolumeReclaimPolicy\":\"${reclaim}\"}}"
done

# 3. Confirm every new claim is Bound to its original PV before going further.
kubectl -n "$ns" get pvc -o custom-columns=NAME:.metadata.name,STATUS:.status.phase,VOLUME:.spec.volumeName

# 4. Drop the old workloads; the fixed chart will recreate them under the chart name.
for sts in $(renamed_sts); do kubectl -n "$ns" delete sts "$sts" --ignore-not-found; done
kubectl -n "$ns" get deploy -l app.kubernetes.io/name=seaweedfs -o name | sed 's|deployment.apps/||' \
  | grep -vE '^seaweedfs-(s3|objectstorage-provisioner)$' \
  | xargs -r -I{} kubectl -n "$ns" delete deploy {} --ignore-not-found
kubectl -n "$ns" patch helmrelease "${app}-system" --type merge -p '{"spec":{"suspend":false}}'
```

Then upgrade. The filer metadata lives in the shared `seaweedfs-db` Postgres and is name-independent, and the volume IDs travel with the volumes themselves, so the adopted cluster comes back with its objects intact.

The loop handles pools and zones (their PVC names carry the pool/zone key) and both renamed shapes: `data1-seaweedfs-system-volume-*` for the default instance, and `data1-<name>-system-seaweedfs-volume-*` for an instance running under another name, because 4.31's name helper appends the chart name when the release name does not contain it.

## Step 2a — `S-damaged` tenants: remove the empty chart-named set first

An `S` tenant that an unguarded 1.6.0 upgrade already reached has an extra problem: the upgrade **created** chart-named workloads (`seaweedfs-master/-filer/-volume`, an s3 Deployment) and **empty** `data1-seaweedfs-volume-*` PVCs beside the live renamed set, and deleted the renamed `<fullname>-s3` Service (only `seaweedfs-s3` remains — its endpoints may still resolve to the renamed set's pods because both sets carry identical labels, which is luck, not design: if the chart-named pods ever become Ready, the Service splits reads across a live set and an empty one).

Verify the direction before touching anything — the chart-named PVCs must be the **newer** generation (compare `kubectl get pvc -o custom-columns=NAME:.metadata.name,CREATED:.metadata.creationTimestamp`), matching the `S-damaged` audit row. Then clear the empty chart-named set so Step 2 can re-bind onto those names:

```sh
ns=<tenant>
app=<seaweedfs-instance-name>
kubectl -n "$ns" patch helmrelease "${app}-system" --type merge -p '{"spec":{"suspend":true}}'
# The chart-named workloads were created by the aborted upgrade and never held
# data. Delete them so the re-bind can take over their names.
kubectl -n "$ns" delete sts seaweedfs-master seaweedfs-filer --ignore-not-found
kubectl -n "$ns" get sts -o name | sed 's|statefulset.apps/||' | grep -E '^seaweedfs-volume($|-)' \
  | xargs -r -I{} kubectl -n "$ns" delete sts {}
# The EMPTY chart-named claims block Step 2's re-bind (same names). Double-check
# each is the newer, never-served generation before deleting.
kubectl -n "$ns" get pvc -o name | sed 's|persistentvolumeclaim/||' | grep -E '^data1-seaweedfs-volume' \
  | xargs -r -I{} kubectl -n "$ns" delete pvc {}
```

Leave the HelmRelease suspended and continue with Step 2 (skip its suspend line; the renamed StatefulSets it scales down are the ones holding the data, exactly as in a plain `S` tenant). Do NOT scale down or delete anything named `*-system-*` before its volumes are re-bound.

## Step 3 — `D-split` tenants: stop the split before upgrading

Do **not** upgrade first and do not delete the duplicate PVCs — they may hold objects written through the split endpoint.

1. Take the live duplicate out of the `seaweedfs-s3` Service rotation so nothing else is written to it. Select the renamed set precisely — the pre-4.31 chart-named `seaweedfs-master/-filer/-volume[-<key>]` is the authoritative set and must keep serving:

   ```sh
   ns=<tenant>
   kubectl -n "$ns" get sts -l app.kubernetes.io/name=seaweedfs -o name | sed 's|statefulset.apps/||' \
     | grep -vE '^seaweedfs-(master|filer|volume)($|-)' \
     | xargs -r -I{} kubectl -n "$ns" scale sts {} --replicas=0
   # The renamed s3 Deployment is the one whose pod template is NOT the chart-named set;
   # scale every seaweedfs s3 Deployment except the adopted `seaweedfs-s3`.
   kubectl -n "$ns" get deploy -l app.kubernetes.io/name=seaweedfs,app.kubernetes.io/component=s3 -o name \
     | sed 's|deployment.apps/||' | grep -v '^seaweedfs-s3$' \
     | xargs -r -I{} kubectl -n "$ns" scale deploy {} --replicas=0
   ```

2. Confirm the legacy set (`seaweedfs-*`) is the authoritative one and is serving.
3. Audit what the split wrote: enumerate filer entries whose fids resolve only on the duplicate volume servers (objects PUT while both sets were live). Export anything that must survive, then re-upload it through the authoritative endpoint.
4. Only then upgrade, and clean up the duplicate as in Step 4.

## Step 4 — Upgrade, then clear the leftovers

Upgrade the cluster to a Cozystack version carrying the fix. On reconcile the `seaweedfs-system` release renders the chart-based names, adopts the running workloads and their volumes, and drops the duplicate `seaweedfs-system-*` StatefulSets and Deployments (they are no longer part of the release).

Helm does not delete PVCs it did not template, and cert-manager Secrets have no owner reference, so the duplicate's volumes and certificates survive the upgrade as inert leftovers. Remove them by hand once the tenant is verified healthy:

```sh
ns=<tenant>
# Duplicate volume PVCs, BY NAME. Never by label: the live data PVCs carry the
# same app.kubernetes.io/instance=seaweedfs-system label and would match too.
kubectl -n "$ns" get pvc -o name | sed 's|persistentvolumeclaim/||' | grep -E '^data1-.*volume' \
  | grep seaweedfs | grep -vE '^data1-seaweedfs-volume' | xargs -r -I{} kubectl -n "$ns" delete pvc {}
# Orphaned certificate/db secrets of the duplicate set. cert-manager names them
# <fullname>-<comp>-cert, so the renamed set's are everything matching the cert/db
# suffix EXCEPT the adopted chart-named seaweedfs-<comp>-cert / seaweedfs-db-secret.
# This covers the default (seaweedfs-system-*) and any non-default (<name>-system-
# seaweedfs-*) instance without hardcoding the name.
kubectl -n "$ns" get secret -o name | sed 's|secret/||' \
  | grep -E '(-cert|-db-secret)$' | grep seaweedfs \
  | grep -vE '^seaweedfs-(admin|ca|client|filer|master|volume|worker)-cert$|^seaweedfs-db-secret$' \
  | xargs -r -I{} kubectl -n "$ns" delete secret {} --ignore-not-found
```

Never delete `data1-seaweedfs-volume-*` — those are the live data PVCs of the adopted set.

The same 4.31 bump also renamed the tenant's four **cluster-scoped** RBAC objects, and those leftovers are cluster-wide rather than namespaced. Pre-4.31 they were named after the per-namespace service account (`<tenant>-seaweedfs-*`); 4.31 named them after the Helm release, which is identical in every tenant, so the fleet collided on one object. The fix puts them back on the service account name, which is where a 1.4.x tenant's objects already are — those are adopted in place and need no cleanup. A tenant that passed **through** 1.5.x, however, leaves behind whatever name that release used, and because more than one tenant claimed it, it may not be pruned by any release's manifest:

```sh
# Stale shared/renamed cluster-scoped RBAC. The go-forward names all start with a
# tenant namespace; anything on the release-based names is a leftover. The fourth
# pre-4.31 object is matched separately: 4.31 renamed the master-rw BINDING from
# upstream's username-shaped system:serviceaccount:<sa>:default to <sa>-rw-crb, so
# the old one carries neither suffix and no tenant prefix.
kubectl get clusterrole,clusterrolebinding \
  -o custom-columns='KIND:.kind,NAME:.metadata.name,OWNER:.metadata.annotations.meta\.helm\.sh/release-name,NS:.metadata.annotations.meta\.helm\.sh/release-namespace' \
  | grep -E 'objectstorage-provisioner|-rw-cr|^\S+\s+system:serviceaccount:.*:default' \
  | grep -vE '^\S+\s+tenant-'
```

A `system:serviceaccount:<tenant>-seaweedfs:default` row is the pre-4.31 master-rw binding. It is superseded by `<tenant>-seaweedfs-rw-crb` and is pruned automatically by the tenant's own upgrade (it is in that release's manifest), so it should not survive — if it does, the tenant has not upgraded yet.

Each surviving row is inert once every tenant is upgraded and verified — no release renders those names any more. Confirm the tenant listed in `OWNER`/`NS` is healthy on the go-forward names first, then delete by name. Do not delete anything named `<tenant>-seaweedfs-*`: those are live.

**During** a rolling fleet upgrade there is a window, and it is worth expecting rather than debugging: Helm prunes by name without checking ownership, so the first tenant to reconcile onto the fixed chart deletes the shared `seaweedfs-objectstorage-provisioner` / `seaweedfs-rw-cr` objects that a not-yet-upgraded tenant is still bound through. Those tenants' COSI provisioners get 403s on bucket operations until they reconcile onto their own per-namespace RBAC. Existing buckets keep serving — S3 traffic does not go through the provisioner — so this is a provisioning outage, not a data-plane one, and it closes on its own as the remaining tenants reconcile.

If a tenant's `seaweedfs-system` HelmRelease was suspended during triage, resume it so the fix can reconcile:

```sh
kubectl -n <tenant> patch helmrelease seaweedfs-system --type merge -p '{"spec":{"suspend":false}}'
kubectl -n <tenant> annotate helmrelease seaweedfs-system reconcile.fluxcd.io/requestedAt="$(date +%s)" --overwrite
```

## Step 5 — Verify

```sh
ns=<tenant>
# Exactly one set, named after the chart, healthy.
kubectl -n "$ns" get sts -l app.kubernetes.io/name=seaweedfs
# All three HelmReleases Ready.
kubectl -n "$ns" get helmrelease seaweedfs seaweedfs-system seaweedfs-db
# S3 endpoints resolve only to the adopted set's ready pods.
kubectl -n "$ns" get endpoints seaweedfs-s3
# Objects are readable through S3 (bucket list via any tenant bucket).
```

A recovered tenant has a single `seaweedfs-*` set, all three HelmReleases `Ready`, no `seaweedfs-system-*` StatefulSets, and no `data1-seaweedfs-system-volume-*` PVCs left behind.
