# Sourced by the chainsaw etcd Tests after `cd` to the repo root. Provides
# etcd_cleanup, the single drain used both to pre-clean a scenario and as its
# guaranteed `finally` teardown (clean-after-each), so the singleton-named etcd
# release never leaks across scenarios — see hack/e2e-chainsaw/etcd/chainsaw-test.yaml.
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
# Requires $NAMESPACE (set by Chainsaw script steps). Returns non-zero (failing
# the step, per e2e-testing.md §4) if teardown does not settle within 4m.
etcd_cleanup() {
  kubectl -n "$NAMESPACE" delete etcd.apps.cozystack.io etcd \
    --ignore-not-found --wait=false >/dev/null 2>&1 || true
  deadline=$(( $(date +%s) + 240 ))
  while kubectl -n "$NAMESPACE" get hr etcd >/dev/null 2>&1 \
     || kubectl get datastore.kamaji.clastix.io "$NAMESPACE" >/dev/null 2>&1; do
    kubectl patch datastore.kamaji.clastix.io "$NAMESPACE" \
      --type=merge -p '{"metadata":{"finalizers":[]}}' >/dev/null 2>&1 || true
    if [ "$(date +%s)" -ge "$deadline" ]; then
      echo "etcd/DataStore teardown did not settle within 4m" >&2
      kubectl -n "$NAMESPACE" get hr,etcd.apps.cozystack.io >&2 2>&1 || true
      kubectl get datastore.kamaji.clastix.io "$NAMESPACE" -o yaml >&2 2>&1 || true
      return 1
    fi
    sleep 5
  done
}
