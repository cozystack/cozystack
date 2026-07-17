# SeaweedFS 4.31 rename — audit & recovery

This runbook covers clusters affected by the SeaweedFS chart-rename regression introduced when the vendored chart was bumped from `4.0.405` to `4.31.0` (Cozystack v1.5.0). Use it to classify each tenant, and to recover the ones that need an operator before they can be upgraded.

**Scope — the default instance name is assumed throughout.** The supported way to run SeaweedFS is the tenant module: the tenant chart creates the instance under the fixed name `seaweedfs` (`packages/apps/tenant/templates/seaweedfs.yaml` hardcodes it; a tenant only enables or disables the module). Every shell selector below assumes that name. The API does not yet enforce it, so an instance created directly against `seaweedfses.apps.cozystack.io` under another name can exist — the audit script still classifies it (its release is `<name>-system`), but **do not run the shell loops here against it: escalate instead**, because the name-based `grep seaweedfs` filters cannot see claims whose names the chart truncated (instance names of roughly 30+ characters), and the chart guard's reconstruction does not cover the zone/pool volume components of long-named instances. One accepted limit applies even to default-named instances: a zone or pool **key** of roughly 40+ characters pushes `seaweedfs-system-volume-<key>` past the chart's truncation limit and similarly out of the guard's reconstruction — do not use keys that long.

## Background

Before 4.31 the chart named workloads after the chart (`seaweedfs-*`), ignoring the release name. 4.31 names them after the release, and the data-plane HelmRelease is `<name>-system`, so every StatefulSet wanted to become `seaweedfs-system-*`. StatefulSet names are immutable, so the upgrade could not rename in place — Helm stood up a second, duplicate set beside the running one. Depending on cluster size the duplicate either deadlocks or splits:

- **D-wedged** — with as many nodes as master replicas, the new masters cannot schedule (hard pod anti-affinity against the old masters). The new set stays `Pending`/`CrashLoopBackOff`, the old set keeps serving. Usually no data at risk — but "usually" is not something a Helm render can verify (a duplicate that served writes and later crashed or was scaled down is indistinguishable from one that never started), so the chart refuses these too rather than adopt on an assumption.
- **D-split** — with more nodes than masters, the new (empty) set comes up. Both sets carry identical pod labels, so the `seaweedfs-s3` Service load-balances across them, and both filers write to the **same `seaweedfs-db` Postgres** metadata store while pointing at different volume servers. This is a data-integrity incident, not just a duplicate: reads of existing objects through the new endpoint miss, new writes land on empty volumes, and the two master sets hand out volume IDs from independent sequences into one shared metadata table.

The fix pins `fullnameOverride: seaweedfs` in `system/seaweedfs` values, so workloads are always named after the chart, exactly as they were before 4.31. Upgrading past the bump therefore **adopts the running set and its volumes in place**.

Two states cannot be adopted that way, and the charts refuse to render for both rather than guess:

- A tenant installed **fresh on 1.5.x**, whose data was written under the release-based names and lives on `data1-seaweedfs-system-volume-*` PVCs. Pinning the chart name there would rename the workloads *away* from that data, and Helm cannot move data between PVCs. Re-bind its volumes (Step 2) before upgrading.
- A tenant where **both** naming generations exist. One of them is an empty duplicate and one holds the data — but nothing durable in the object graph says which. Claim timestamps are not evidence: Step 2's own re-bind deletes and recreates claims, so a tenant interrupted mid-recovery has a brand-new claim holding real data. StatefulSets are recreated by the adoption hook. `readyReplicas: 0` does not prove a duplicate never served. Rendering would adopt the chart-named set, so a wrong guess strands or destroys data. Step 1 classifies these with signals a template does not have; once the empty generation is deleted, exactly one remains and the render proceeds on its own.

The **enforcing** guard lives in `system/seaweedfs` (`templates/naming-guard.yaml`) — the `<name>-system` HelmRelease pulls that chart straight from a platform-managed ExternalArtifact, so a platform upgrade re-renders it directly and nothing else stands between the upgrade and the tenant's workloads; `extra/seaweedfs` carries a sibling copy so the refusal is also visible on the SeaweedFS application itself.

A tenant upgrading **1.4.x straight to 1.6 never renames**, so it only ever has one generation and is unaffected by any of this. Duplicates exist only on tenants that passed through 1.5.x.

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

```sh
hack/seaweedfs-naming-audit.sh                 # whole cluster
hack/seaweedfs-naming-audit.sh tenant-foo      # or named namespaces
```

It mutates nothing. Earlier revisions of this runbook inlined the classification as a shell snippet here; it is a tested script now (`hack/seaweedfs-naming-audit.bats`), because it is what the chart's refusal hands you to and acting on it deletes PVCs. Two inline versions shipped wrong — one whose selector matched both generations at once and so inverted its own primary rule, one that could not see a long instance name at all — so it is not a snippet any more.

It reports one class per SeaweedFS instance, matching exactly what the chart's guard decides:

| CLASS | Meaning | Action |
|---|---|---|
| `L` | Only the chart-named generation. | None. The upgrade adopts it in place. |
| `S` | Only the release-named generation — installed fresh on 1.5.x, or a long instance name. | Step 2, before upgrading. |
| `MIXED` | Both generations. The chart **refuses**. | Below. |

For `MIXED` it also names which generation is **original**, from two independent durable signals: the naming scheme revision 1 of the `<name>-system` release was installed with, and each generation's **PersistentVolume** creation timestamps. It reads PV timestamps, never claim timestamps — Step 2 deletes each release-named claim and recreates it under the chart name against the same PV, so claim age is not durable and inverts for a tenant interrupted mid-re-bind. The direction rule is **relative, never a clock**: a generation is the candidate duplicate only when *every* one of its bound PVs is strictly newer than every bound PV of the other generation — the same precondition Step 2a enforces. A tenant interrupted mid-re-bind has both generations on original-vintage PVs, so their ranges **overlap** and the audit names no candidate: finish Step 2, do not run Step 2a.

**Read the audit's own warning.** "Original" is not "the other one is empty", and the gap is exactly where it matters. A duplicate that never scheduled (safe to delete) and one that served writes and later crashed or was scaled down (holds unique objects, deleting destroys them) are **identical on every durable signal** — same revision-1 scheme, same `first_deployed` deltas. The audit narrows the question to one generation; it does not answer it. Before deleting anything, establish that the candidate is empty:

Empty means: no volume files (`.dat`/`.idx`/`.vif`) in any data directory. Do **not** `kubectl exec` into the candidate's own pods to check — a wedged duplicate's pods never start, so a check that needs the pod running is unexecutable exactly for the class where it matters most. Mount the claims instead:

```sh
ns=<tenant>
# 1. Stop the candidate's workloads so its RWO claims can be mounted elsewhere.
#    Reversible, and it is Step 3's first action anyway (Step 2a's for S-damaged).
# 2. A candidate claim still Pending has no PV and therefore no data — empty by
#    construction; skip it.
# 3. Mount each BOUND candidate claim read-only in a scratch pod:
pvc=<candidate-volume-claim>     # e.g. data1-seaweedfs-system-volume-0; repeat per claim
kubectl -n "$ns" apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: swfs-emptiness-check
spec:
  restartPolicy: Never
  containers:
  - name: inspect
    image: busybox:1.37
    command: ["sh", "-c", "ls -laR /data; echo '--- volume file count:'; find /data \( -name '*.dat' -o -name '*.idx' -o -name '*.vif' \) | wc -l"]
    volumeMounts:
    - { name: data, mountPath: /data, readOnly: true }
  volumes:
  - name: data
    persistentVolumeClaim: { claimName: ${pvc}, readOnly: true }
EOF
kubectl -n "$ns" wait --for=jsonpath='{.status.phase}'=Succeeded pod/swfs-emptiness-check --timeout=120s
kubectl -n "$ns" logs swfs-emptiness-check      # empty = volume file count 0
kubectl -n "$ns" delete pod swfs-emptiness-check

# Cross-check against the cluster's own accounting, on the AUTHORITATIVE set's
# running master (exec is fine there — that set is serving): no volumes may be
# attributed to the candidate's servers.
kubectl -n "$ns" exec <authoritative-master-pod> -- weed shell -c "volume.list"
```

If a candidate claim cannot be inspected, or the two views disagree, **stop and escalate**. Both generations holding real data is recoverable; deleting the wrong one is not.

**D-wedged** — a duplicate that never scheduled — is `MIXED` too, and the chart refuses it like any other duplicate. On main it rendered through, because a duplicate reading `readyReplicas: 0` was taken as proof it never served. That is not proof: a duplicate that served writes and later crashed or was scaled down reads identically. Confirm emptiness as above, then remove it via Step 3 **before** upgrading.

On a cluster with no spare nodes (nodes ≤ replicas) a wedged duplicate also blocks the adoption rollout even once the render passes: its pods are still *scheduled*, carry the same labels as the adopted set (including `app.kubernetes.io/instance`), and their hard pod anti-affinity keeps the adopted set's rolled pods from landing anywhere (observed as `seaweedfs-filer-1`/`seaweedfs-master-2` stuck Pending on `didn't match pod anti-affinity rules`, wedging the `<name>-system` HelmRelease in upgrade/rollback loops). Step 3 removes it, which resolves that too.

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
  # Stash the PV's own reclaim policy ON THE PV before changing it, so a volume the
  # cluster deliberately set to Retain does not silently come back as Delete -- and
  # so the record survives this loop being interrupted, which a shell variable would
  # not. Step 5 restores it from the annotation once the tenant is verified healthy.
  reclaim=$(kubectl get pv "$pv" -o jsonpath='{.spec.persistentVolumeReclaimPolicy}')
  if [ -z "$(kubectl get pv "$pv" -o jsonpath='{.metadata.annotations.cozystack\.io/original-reclaim-policy}')" ]; then
    kubectl annotate pv "$pv" "cozystack.io/original-reclaim-policy=${reclaim}" || { echo "FAILED to record the original reclaim policy on $pv; aborting before any PVC is deleted" >&2; break; }
  fi

  # Keep the PV (and the data) when the claim goes away.
  kubectl patch pv "$pv" -p '{"spec":{"persistentVolumeReclaimPolicy":"Retain"}}' || { echo "FAILED to set Retain on $pv; aborting before deleting its PVC" >&2; break; }
  kubectl -n "$ns" delete pvc "$pvc"
  # A Released PV cannot be re-bound until its old claimRef is cleared.
  kubectl patch pv "$pv" --type json -p '[{"op":"remove","path":"/spec/claimRef"}]'

  # The labels matter: the post-delete cleanup hook selects volume PVCs by
  # app.kubernetes.io/name + instance, and StatefulSet reconciliation does NOT
  # retrofit claim-template labels onto an existing PVC. A claim recreated without
  # them is invisible to that hook forever, so a recovered tenant would leak its
  # volumes on a later app deletion.
  kubectl -n "$ns" apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ${new_pvc}
  namespace: ${ns}
  labels:
    app.kubernetes.io/name: seaweedfs
    app.kubernetes.io/instance: ${app}-system
spec:
  accessModes: ["ReadWriteOnce"]
  storageClassName: ${sc}
  volumeName: ${pv}
  resources:
    requests:
      storage: ${size}
EOF
  # NOTE: the reclaim policy stays Retain until Step 5. Restoring it here (as this
  # runbook used to) re-arms Delete while the tenant is still mid-recovery, which is
  # what turned a later mis-step into permanent data loss: on a Delete-policy
  # StorageClass, deleting the claim takes the PV and the bytes with it.
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

> **STOP if you are resuming an interrupted Step 2.** This step deletes every `data1-seaweedfs-volume-*` claim. If Step 2 already re-bound some of them, those claims hold your data and are bound to the original PVs — deleting them is the data-loss path this step used to be reachable through by misclassification. Finish Step 2 instead. The check below refuses in that case, but read the Step 1 classification first and be sure.

Verify the direction before touching anything. The chart-named generation must be the **newer** one, judged on the **PV** ages (claims are recreated by Step 2's re-bind, so their timestamps prove nothing). The precondition below refuses unless every chart-named claim is bound to a PV strictly newer than every release-named PV:

```sh
#!/usr/bin/env bash
set -euo pipefail
ns=<tenant>
app=<seaweedfs-instance-name>

# PRECONDITION. Every chart-named claim must be bound to a PV strictly newer than
# every release-named PV. A chart-named claim sitting on an OLD PV means Step 2
# already re-bound it: it holds data, and this step would delete it.
pv_age() { kubectl get pv "$(kubectl -n "$ns" get pvc "$1" -o jsonpath='{.spec.volumeName}')" \
             -o jsonpath='{.metadata.creationTimestamp}'; }
newest_release_pv=""
for pvc in $(kubectl -n "$ns" get pvc -o name | sed 's|persistentvolumeclaim/||' \
               | grep -E '^data1-.*volume' | grep seaweedfs | grep -vE '^data1-seaweedfs-volume'); do
  a=$(pv_age "$pvc"); [ "$a" \> "$newest_release_pv" ] && newest_release_pv="$a"
done
[ -n "$newest_release_pv" ] || { echo "REFUSING: no release-named volumes found; this is not an S-damaged tenant"; exit 1; }
for pvc in $(kubectl -n "$ns" get pvc -o name | sed 's|persistentvolumeclaim/||' | grep -E '^data1-seaweedfs-volume'); do
  a=$(pv_age "$pvc")
  if [ ! "$a" \> "$newest_release_pv" ]; then
    echo "REFUSING: $pvc is bound to a PV created $a, NOT newer than the newest release-named PV ($newest_release_pv)."
    echo "That claim is not an empty duplicate — Step 2 has most likely already re-bound it. Finish Step 2; do not run this step."
    exit 1
  fi
done
echo "ok: every chart-named claim is bound to a strictly newer PV — safe to clear the duplicate"

kubectl -n "$ns" patch helmrelease "${app}-system" --type merge -p '{"spec":{"suspend":true}}'
# The chart-named workloads were created by the aborted upgrade and never held
# data. Delete them so the re-bind can take over their names.
kubectl -n "$ns" delete sts seaweedfs-master seaweedfs-filer --ignore-not-found
kubectl -n "$ns" get sts -o name | sed 's|statefulset.apps/||' | grep -E '^seaweedfs-volume($|-)' \
  | xargs -r -I{} kubectl -n "$ns" delete sts {}
# The EMPTY chart-named claims block Step 2's re-bind (same names).
kubectl -n "$ns" get pvc -o name | sed 's|persistentvolumeclaim/||' | grep -E '^data1-seaweedfs-volume' \
  | xargs -r -I{} kubectl -n "$ns" delete pvc {}
```

Leave the HelmRelease suspended and continue with Step 2 (skip its suspend line; the renamed StatefulSets it scales down are the ones holding the data, exactly as in a plain `S` tenant). Do NOT scale down or delete anything named `*-system-*` before its volumes are re-bound.

## Step 3 — `MIXED` tenants: remove the duplicate before upgrading

The chart refuses while both generations exist, so the duplicate must be **gone before** the upgrade, not cleaned up after it. Do not skip to Step 4: it cannot run until this is done.

Do **not** start by deleting PVCs. If the duplicate ever served, they may hold objects written through the split endpoint.

1. **Take the duplicate out of service** so nothing else is written to it. Select it precisely — the generation Step 1 named as ORIGINAL is authoritative and must keep serving. For the common case (chart-named original, release-named duplicate):

   ```sh
   ns=<tenant>
   kubectl -n "$ns" get sts -l app.kubernetes.io/name=seaweedfs -o name | sed 's|statefulset.apps/||' \
     | grep -vE '^seaweedfs-(master|filer|volume)($|-)' \
     | xargs -r -I{} kubectl -n "$ns" scale sts {} --replicas=0
   # The renamed s3 Deployment is the one that is NOT the chart-named `seaweedfs-s3`.
   kubectl -n "$ns" get deploy -l app.kubernetes.io/name=seaweedfs,app.kubernetes.io/component=s3 -o name \
     | sed 's|deployment.apps/||' | grep -v '^seaweedfs-s3$' \
     | xargs -r -I{} kubectl -n "$ns" scale deploy {} --replicas=0
   ```

   If Step 1 named the **release-named** generation as original, this tenant is `S-damaged`: the duplicate is the chart-named set. Use **Step 2a** instead, then Step 2 — the selectors are inverted there.

2. **Confirm the authoritative set is serving**, and that the duplicate is out of the `seaweedfs-s3` endpoints:

   ```sh
   kubectl -n "$ns" get endpoints seaweedfs-s3 -o yaml
   ```

3. **If the duplicate ever served, escalate.** Step 1's emptiness check is what decides this. Recovering objects that exist only on a duplicate is not a procedure this runbook can give you: both master sets allocate volume IDs from independent sequences into one shared `seaweedfs-db`, so a fid written through the split endpoint can collide with a fid on the authoritative set, and there is no supported tool that reconciles two volume-ID spaces against one metadata store. Do not improvise it. Involve someone who can plan a per-object export, and treat the tenant as an incident.

4. **Delete the duplicate's StatefulSets, Deployments and PVCs.** Only once (3) is settled, and only for a duplicate confirmed empty (or whose contents have been exported):

   ```sh
   ns=<tenant>
   # StatefulSets and Deployments of the duplicate generation.
   kubectl -n "$ns" get sts -l app.kubernetes.io/name=seaweedfs -o name | sed 's|statefulset.apps/||' \
     | grep -vE '^seaweedfs-(master|filer|volume)($|-)' \
     | xargs -r -I{} kubectl -n "$ns" delete sts {}
   kubectl -n "$ns" get deploy -l app.kubernetes.io/name=seaweedfs -o name | sed 's|deployment.apps/||' \
     | grep -vE '^seaweedfs-(s3|objectstorage-provisioner)$' \
     | xargs -r -I{} kubectl -n "$ns" delete deploy {}
   # Duplicate volume PVCs, BY NAME. Never by label: the live data PVCs carry the
   # same app.kubernetes.io/instance=<name>-system label and would match too.
   kubectl -n "$ns" get pvc -o name | sed 's|persistentvolumeclaim/||' | grep -E '^data1-.*volume' \
     | grep seaweedfs | grep -vE '^data1-seaweedfs-volume' | xargs -r -I{} kubectl -n "$ns" delete pvc {}
   ```

   Never delete `data1-seaweedfs-volume-*` here — those are the live data PVCs of the authoritative set. (For an `S-damaged` tenant it is the other way round; that is Step 2a's job, and it has its own precondition check.)

5. **Re-run the audit.** The tenant must now read `L` (or `S`, if you are on the Step 2 path). Only then upgrade.

   ```sh
   hack/seaweedfs-naming-audit.sh "$ns"
   ```

## Step 4 — Upgrade, then clear the leftovers

Every tenant must read `L` or `S` in the audit before you start: the chart refuses to render while both generations exist, so a `MIXED` tenant does not upgrade at all — Steps 2/2a/3 come first, not after. Once the fleet is clean, upgrade to a Cozystack version carrying the fix. On reconcile the `<name>-system` release renders the chart-based names and adopts the running workloads and their volumes in place.

A duplicate's StatefulSets and PVCs are removed in Step 3, before the upgrade. What can still be left behind afterwards are objects no release templated and nothing owns: cert-manager Secrets have no owner reference, and PVCs Helm never templated are never GC'd. Remove those once the tenant is verified healthy:

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

Only once that is true, restore the reclaim policy Step 2 stashed on each PV. Until this runs the volumes are `Retain`, which is deliberate: it is what makes an accidental claim deletion during recovery survivable.

```sh
ns=<tenant>
for pvc in $(kubectl -n "$ns" get pvc -o name | sed 's|persistentvolumeclaim/||' | grep -E '^data1-seaweedfs-volume'); do
  pv=$(kubectl -n "$ns" get pvc "$pvc" -o jsonpath='{.spec.volumeName}')
  orig=$(kubectl get pv "$pv" -o jsonpath='{.metadata.annotations.cozystack\.io/original-reclaim-policy}')
  [ -n "$orig" ] || continue
  kubectl patch pv "$pv" -p "{\"spec\":{\"persistentVolumeReclaimPolicy\":\"${orig}\"}}" && kubectl annotate pv "$pv" cozystack.io/original-reclaim-policy-
done
```
