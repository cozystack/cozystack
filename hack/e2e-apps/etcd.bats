#!/usr/bin/env bats

# The etcd chart pins the Helm release name to 'etcd' via
# packages/extra/etcd/templates/check-release-name.yaml, so every test
# applies its Etcd with metadata.name: etcd. The tests run as an ordered
# sequence that reconfigures that singleton in place.
#
# The e2e runner (hack/cozytest.sh) is not real bats: it never calls
# setup()/teardown() and never opens fd 3. It recognises @test blocks and a
# cozy_cleanup() hook, which it invokes once at suite exit and on the first
# failing test. A dead setup() therefore did nothing — etcd leaked into the
# shared tenant-test namespace.
#
# Teardown lives in etcd_drain(). It is run two ways:
#   - inline at the end of the last @test, where set -e makes a teardown
#     failure fail the suite (e2e-testing.md §4 — don't mask teardown
#     failures); and
#   - as cozy_cleanup, the best-effort safety net cozytest runs at suite exit
#     and on the first failing test (the runner wraps it in `|| true`, so this
#     path alone cannot fail the suite — the inline call is what has teeth).
# The first @test also drains up front so a dirty namespace from a previous
# run cannot taint it (e2e-testing.md §3 — pre-cleanup at test start).
etcd_drain() {
  etcd_pvc_selector='app.kubernetes.io/name=etcd,app.kubernetes.io/managed-by=etcd-operator'
  kubectl -n tenant-test delete etcd.apps.cozystack.io etcd \
    --ignore-not-found --wait=false >/dev/null 2>&1 || true
  # The EtcdCluster StatefulSet's data-etcd-* PVCs are retained on uninstall
  # (they are not owned by the Etcd CR). They MUST go too: a later install
  # reuses the same-named PVCs, boots the previous data dir and member
  # identity, never forms a fresh first quorum, and the chart's helm --wait
  # times out (InstallFailed). kubernetes.io/pvc-protection holds them until
  # the HR uninstall removes the StatefulSet pods, so the loop below re-issues
  # the delete and waits for them to actually disappear.
  kubectl -n tenant-test delete pvc -l "$etcd_pvc_selector" \
    --ignore-not-found --wait=false >/dev/null 2>&1 || true
  # Wait until the Etcd CR, its HelmRelease, the cluster-scoped Kamaji
  # DataStore/tenant-test, and the data PVCs are ALL gone. The Etcd CR is
  # included because it can linger Terminating after the HR is gone and then
  # block the next apply of the singleton name. Kamaji leaks the DataStore
  # finalizer (the reconcile that would drop it needs the now-gone etcd),
  # wedging the DataStore in Terminating and hanging the HR uninstall; no
  # TenantControlPlane uses this per-namespace test DataStore, so clear the
  # finalizer each pass. Each remnant is probed by name: an absent object
  # gives empty output and exit 0, but a transient API error gives a non-zero
  # exit, which sets an "err" sentinel so a blip is never mistaken for
  # "deleted". The per-delete `|| true` is deliberate — the deletes are
  # fire-and-forget (--wait=false, re-issued each pass); the teeth are this
  # deadline-bounded poll, which returns non-zero (failing the inline
  # teardown, hence the suite) if anything is still present after 4m. A
  # blocking `delete --wait` would instead hang on the leaked DataStore
  # finalizer rather than fail.
  deadline=$(( $(date +%s) + 240 ))
  while :; do
    o_etcd=$(kubectl -n tenant-test get etcd.apps.cozystack.io/etcd --ignore-not-found -o name 2>/dev/null) || o_etcd=err
    o_hr=$(kubectl -n tenant-test get hr/etcd --ignore-not-found -o name 2>/dev/null) || o_hr=err
    o_ds=$(kubectl get datastore.kamaji.clastix.io/tenant-test --ignore-not-found -o name 2>/dev/null) || o_ds=err
    o_pvc=$(kubectl -n tenant-test get pvc -l "$etcd_pvc_selector" -o name 2>/dev/null) || o_pvc=err
    if [ -z "$o_etcd$o_hr$o_ds$o_pvc" ]; then break; fi
    kubectl patch datastore.kamaji.clastix.io tenant-test \
      --type=merge -p '{"metadata":{"finalizers":[]}}' >/dev/null 2>&1 || true
    kubectl -n tenant-test delete pvc -l "$etcd_pvc_selector" \
      --ignore-not-found --wait=false >/dev/null 2>&1 || true
    if [ "$(date +%s)" -ge "$deadline" ]; then
      echo "etcd/DataStore/PVC teardown did not settle within 4m"
      kubectl -n tenant-test get hr,etcd.apps.cozystack.io,pvc -l "$etcd_pvc_selector" 2>&1 || true
      kubectl get datastore.kamaji.clastix.io tenant-test -o yaml 2>&1 || true
      return 1
    fi
    sleep 5
  done
}

cozy_cleanup() {
  etcd_drain
}

dump_diagnostics() {
  # cozytest captures the test's stdout/stderr and prints it on failure, so
  # diagnostics go to stdout — fd 3 is never opened by the runner.
  echo "# --- diagnostics ---"
  kubectl -n tenant-test get etcdcluster,etcdbackupschedule,cronjob -o wide 2>&1 || true
  kubectl -n tenant-test describe etcdbackupschedule etcd 2>&1 || true
  kubectl -n cozy-etcd-operator logs -l app.kubernetes.io/name=etcd-operator --tail=100 2>&1 || true
}

# Wait until the etcd HelmRelease is reconciled by Flux and its
# condition=ready is set. kubectl wait fails immediately on a missing
# resource, so poll for existence first.
wait_etcd_hr_ready() {
  timeout 60 sh -ec 'until kubectl -n tenant-test get hr/etcd >/dev/null 2>&1; do sleep 2; done'
  kubectl -n tenant-test wait hr/etcd --timeout=5m --for=condition=ready
}

@test "Create Etcd" {
  # Pre-clean: drain any etcd left by a previous run so the singleton starts
  # from a clean slate (no stale data PVCs / wedged DataStore).
  etcd_drain
  kubectl apply -f- <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Etcd
metadata:
  name: etcd
  namespace: tenant-test
spec:
  size: 1Gi
  replicas: 3
  storageClass: ""
  resources:
    cpu: 100m
    memory: 128Mi
EOF
  wait_etcd_hr_ready || { dump_diagnostics; false; }
  kubectl -n tenant-test wait etcdcluster.etcd.aenix.io etcd --timeout=180s --for=jsonpath='{.status.conditions[?(@.type=="Ready")].status}'=True || { dump_diagnostics; false; }
}

@test "Create Etcd with empty backup block (disabled by default)" {
  kubectl apply -f- <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Etcd
metadata:
  name: etcd
  namespace: tenant-test
spec:
  size: 1Gi
  replicas: 3
  storageClass: ""
  resources:
    cpu: 100m
    memory: 128Mi
  backup: {}
EOF
  wait_etcd_hr_ready || { dump_diagnostics; false; }
  # With backup disabled, neither the schedule nor the secret should be created.
  ! kubectl -n tenant-test get etcdbackupschedule.etcd.aenix.io etcd 2>/dev/null
  ! kubectl -n tenant-test get secret etcd-s3-creds 2>/dev/null
}

@test "Create Etcd with backup schedule" {
  kubectl apply -f- <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Etcd
metadata:
  name: etcd
  namespace: tenant-test
spec:
  size: 1Gi
  replicas: 3
  storageClass: ""
  resources:
    cpu: 100m
    memory: 128Mi
  backup:
    enabled: true
    # Schedule is chosen far in the future so no Jobs fire during the
    # ~6-minute test window; this test only checks that the chart renders
    # EtcdBackupSchedule/Secret and that the etcd-operator materializes a
    # CronJob — it does NOT verify that backups reach S3. The endpoint
    # below intentionally resolves nowhere to keep the test self-contained.
    schedule: "0 0 1 1 *"
    destinationPath: "s3://test-bucket/etcd-backups/"
    endpointURL: "http://no-such-endpoint.invalid:9000"
    region: "us-east-1"
    forcePathStyle: true
    s3AccessKey: "e2e-access-key"
    s3SecretKey: "e2e-secret-key"
    successfulJobsHistoryLimit: 1
    failedJobsHistoryLimit: 1
EOF
  wait_etcd_hr_ready || { dump_diagnostics; false; }
  kubectl -n tenant-test wait etcdcluster.etcd.aenix.io etcd --timeout=180s --for=jsonpath='{.status.conditions[?(@.type=="Ready")].status}'=True || { dump_diagnostics; false; }
  # Reconfiguring the Etcd CR triggers a HelmRelease upgrade that renders the
  # EtcdBackupSchedule; hr/etcdcluster readiness can report ready on the prior
  # revision before that resource materializes, so poll for it instead of a
  # bare get (same pattern this test already uses for the CronJob below).
  timeout 120 sh -ec "until kubectl -n tenant-test get etcdbackupschedule.etcd.aenix.io etcd >/dev/null 2>&1; do sleep 2; done" || { dump_diagnostics; false; }
  # Verify the region field propagated to the EtcdBackupSchedule.
  REGION=$(kubectl -n tenant-test get etcdbackupschedule.etcd.aenix.io etcd -o jsonpath='{.spec.destination.s3.region}')
  [ "$REGION" = "us-east-1" ]
  kubectl -n tenant-test get secret etcd-s3-creds -o jsonpath='{.data.AWS_ACCESS_KEY_ID}' | base64 -d | grep -q '^e2e-access-key$'
  # The etcd-operator generates a CronJob from the EtcdBackupSchedule. Wait for it.
  timeout 120 sh -ec "until [ \"\$(kubectl -n tenant-test get cronjob -l etcd.aenix.io/etcdbackupschedule-name=etcd -o jsonpath='{.items[*].metadata.name}' 2>/dev/null)\" != '' ]; do sleep 5; done" || { dump_diagnostics; false; }
  # Inline teardown for the last scenario: drain etcd, its retained data PVCs,
  # and the Kamaji DataStore. Runs under set -e, so a teardown that does not
  # settle fails the suite instead of leaking into later app tests. This drain
  # MUST stay in the last @test to keep its teeth — if you append a scenario
  # after this one, move the inline drain there (cozy_cleanup alone is the
  # best-effort safety net cozytest `|| true`s, so it cannot fail the suite).
  etcd_drain
}
