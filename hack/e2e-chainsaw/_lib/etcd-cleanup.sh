# Sourced by the chainsaw etcd Tests after `cd` to the repo root. Provides:
#   etcd_cleanup        — drain of the singleton etcd release (details below)
#   etcd_backup_cleanup — teardown of the S3 backup round-trip's resources
# Both are used to pre-clean a scenario and as its guaranteed `finally`
# teardown (clean-after-each), so the singleton-named etcd release never leaks
# across scenarios — see hack/e2e-chainsaw/etcd/chainsaw-test.yaml.
#
# The etcd chart pins the Helm release name to 'etcd'
# (packages/extra/etcd/templates/check-release-name.yaml) and provisions a
# cluster-scoped DataStore/<namespace> for Kamaji. On teardown Kamaji leaks its
# kamaji.clastix.io/TenantControlPlane finalizer (the reconcile that would drop
# it needs the now-gone etcd), wedging the DataStore in Terminating and hanging
# the etcd HelmRelease uninstall — which is what errored every etcd case when
# Chainsaw auto-cleanup waited on it. No TenantControlPlane uses this
# per-namespace test DataStore, so clear the finalizer each pass until the HR
# and DataStore are both gone.
#
# The EtcdCluster's data-etcd-* PVCs are retained on uninstall (they are not
# owned by the Etcd CR). They MUST be deleted too: a later scenario reuses the
# same-named PVCs, the new etcd pods boot the previous scenario's data dir and
# member identity, and the cluster never forms a fresh first quorum — it sticks
# at EtcdCluster `WaitingForFirstQuorum` and the chart's helm --wait times out
# (InstallFailed). That is why the first etcd scenario passes and every later
# one failed until this drain reclaimed the volumes. Verified from the failed
# run's crust-gather snapshot: data-etcd-{0,1,2} shared one set of PV UIDs and a
# single creationTimestamp across all three scenarios.
#
# Requires $NAMESPACE (set by Chainsaw script steps). Returns non-zero (failing
# the step, per e2e-testing.md §4) if teardown does not settle within 4m.
etcd_cleanup() {
  etcd_pvc_selector='app.kubernetes.io/name=etcd,app.kubernetes.io/managed-by=etcd-operator'
  kubectl -n "$NAMESPACE" delete etcd.apps.cozystack.io etcd \
    --ignore-not-found --wait=false >/dev/null 2>&1 || true
  # Queue the retained data PVCs for deletion now; kubernetes.io/pvc-protection
  # holds them until the HR uninstall removes the member pods, after which
  # they actually go. The loop below waits for that.
  kubectl -n "$NAMESPACE" delete pvc -l "$etcd_pvc_selector" \
    --ignore-not-found --wait=false >/dev/null 2>&1 || true
  # Wait until the Etcd CR, its HelmRelease, the cluster-scoped Kamaji
  # DataStore/<namespace>, and the data PVCs are ALL gone. The Etcd CR is
  # included because it can linger Terminating after the HR is gone and then
  # block the next apply of the singleton name. Each remnant is probed by
  # name: an absent object gives empty output and exit 0, but a transient API
  # error gives a non-zero exit, which sets an "err" sentinel so a blip is
  # never mistaken for "deleted". The per-delete `|| true` is deliberate — the
  # deletes are fire-and-forget (--wait=false, re-issued each pass); the teeth
  # are this deadline-bounded poll, which returns non-zero (failing the step)
  # if anything is still present after 4m. A blocking `delete --wait` would
  # instead hang on the leaked DataStore finalizer rather than fail.
  deadline=$(( $(date +%s) + 240 ))
  while :; do
    o_etcd=$(kubectl -n "$NAMESPACE" get etcd.apps.cozystack.io/etcd --ignore-not-found -o name 2>/dev/null) || o_etcd=err
    o_hr=$(kubectl -n "$NAMESPACE" get hr/etcd --ignore-not-found -o name 2>/dev/null) || o_hr=err
    o_ds=$(kubectl get "datastore.kamaji.clastix.io/$NAMESPACE" --ignore-not-found -o name 2>/dev/null) || o_ds=err
    o_pvc=$(kubectl -n "$NAMESPACE" get pvc -l "$etcd_pvc_selector" -o name 2>/dev/null) || o_pvc=err
    if [ -z "$o_etcd$o_hr$o_ds$o_pvc" ]; then break; fi
    kubectl patch datastore.kamaji.clastix.io "$NAMESPACE" \
      --type=merge -p '{"metadata":{"finalizers":[]}}' >/dev/null 2>&1 || true
    # Re-issue the PVC delete each pass: the first call can race ahead of the
    # operator recreating them, and it is a no-op once they are gone.
    kubectl -n "$NAMESPACE" delete pvc -l "$etcd_pvc_selector" \
      --ignore-not-found --wait=false >/dev/null 2>&1 || true
    if [ "$(date +%s)" -ge "$deadline" ]; then
      echo "etcd/DataStore/PVC teardown did not settle within 4m" >&2
      kubectl -n "$NAMESPACE" get hr,etcd.apps.cozystack.io,pvc -l "$etcd_pvc_selector" >&2 2>&1 || true
      kubectl get datastore.kamaji.clastix.io "$NAMESPACE" -o yaml >&2 2>&1 || true
      return 1
    fi
    sleep 5
  done
}

# Teardown of the backup round-trip's resources (bucket, BackupJob/Backup,
# RestoreJob, etcdctl pod, creds Secret, BackupClass, strategy). cleanup.sh
# resumes the source Etcd HelmRelease first, so a round-trip aborted
# mid-restore does not strand the app suspended. Idempotent and safe even when
# the round-trip test never ran (deletes nothing else owns).
#
# Has teeth, mirroring etcd_cleanup: cleanup.sh is `set -e` with
# --ignore-not-found on every delete, so it exits 0 on a clean namespace and
# non-zero only on a real failure (stuck finalizer, RBAC error, wait timeout).
# Deliberately NOT `|| true`d here — inside a Chainsaw `finally` a stuck
# cleanup must fail the step rather than leak a wedged bucket/BackupClass into
# the next app test.
etcd_backup_cleanup() {
  [ -x examples/backups/etcd/cleanup.sh ] || return 0
  NAMESPACE="$NAMESPACE" examples/backups/etcd/cleanup.sh 2>&1
}
