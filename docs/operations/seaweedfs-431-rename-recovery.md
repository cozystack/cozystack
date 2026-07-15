# SeaweedFS 4.31 rename — audit & recovery

This runbook covers clusters affected by the SeaweedFS chart-rename regression introduced when the vendored chart was bumped from `4.0.405` to `4.31.0` (Cozystack v1.5.0). Use it to classify each tenant, and to recover the ones that need an operator before they can be upgraded.

## Background

Before 4.31 the chart named workloads after the chart (`seaweedfs-*`), ignoring the release name. 4.31 names them after the release, and the data-plane HelmRelease is `<name>-system`, so every StatefulSet wanted to become `seaweedfs-system-*`. StatefulSet names are immutable, so the upgrade could not rename in place — Helm stood up a second, duplicate set beside the running one. Depending on cluster size the duplicate either deadlocks or splits:

- **D-wedged** — with as many nodes as master replicas, the new masters cannot schedule (hard pod anti-affinity against the old masters). The new set stays `Pending`/`CrashLoopBackOff`, the old set keeps serving. No data at risk.
- **D-split** — with more nodes than masters, the new (empty) set comes up. Both sets carry identical pod labels, so the `seaweedfs-s3` Service load-balances across them, and both filers write to the **same `seaweedfs-db` Postgres** metadata store while pointing at different volume servers. This is a data-integrity incident, not just a duplicate: reads of existing objects through the new endpoint miss, new writes land on empty volumes, and the two master sets hand out volume IDs from independent sequences into one shared metadata table.

The fix pins `fullnameOverride: seaweedfs` in `system/seaweedfs` values, so workloads are always named after the chart, exactly as they were before 4.31. Upgrading past the bump therefore **adopts the running set and its volumes in place**.

One class cannot be adopted that way: a tenant installed **fresh on 1.5.x**, whose data was written under the release-based names and lives on `data1-seaweedfs-system-volume-*` PVCs. Pinning the chart name there would rename the workloads *away* from that data. Helm cannot move data between PVCs, so `extra/seaweedfs` refuses to render for such a tenant and points here. Re-bind its volumes (below) before upgrading.

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
  pvcs=$(kubectl get pvc -n "$ns" -o name | sed 's|persistentvolumeclaim/||')
  legacy=$(echo "$pvcs" | grep -cE '^data1-seaweedfs-volume' || true)
  system=$(echo "$pvcs" | grep -E '^data1-.*volume' | grep -E 'seaweedfs' | grep -vE '^data1-seaweedfs-volume' | grep -c . || true)
  running_new=$(kubectl get pods -n "$ns" \
      -l app.kubernetes.io/name=seaweedfs,app.kubernetes.io/component=volume \
      --field-selector=status.phase=Running -o name 2>/dev/null \
      | grep -vc '/seaweedfs-volume-' || true)
  if [ "$legacy" -gt 0 ] && [ "$system" -gt 0 ]; then
    if [ "${running_new:-0}" -gt 0 ]; then
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

Act on the classes in this order: resolve every **D-split** and every **S** tenant first (both need an operator), then upgrade. **L** and **D-wedged** need nothing.

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
