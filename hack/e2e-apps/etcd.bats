#!/usr/bin/env bats

# The etcd chart pins the Helm release name to 'etcd' via
# packages/extra/etcd/templates/check-release-name.yaml, so every test
# applies its Etcd with metadata.name: etcd. The tests run as an ordered
# sequence that reconfigures that singleton in place.
#
# The e2e runner (hack/cozytest.sh) is not real bats: it never calls
# setup()/teardown(), never opens fd 3, and knows no `run`/$status/$output —
# only @test blocks and bash. Assertions are direct shell tests; diagnostics
# go to stdout (cozytest captures and prints it on failure). The suite defines
# a cozy_cleanup() hook, which cozytest invokes once at suite exit and on the
# first failing test.
#
# Teardown lives in etcd_drain() and backup_cleanup(). They run two ways:
#   - inline at the end of the last @test, where set -e makes a teardown
#     failure fail the suite (e2e-testing.md §4 — don't mask teardown
#     failures); and
#   - as cozy_cleanup, the best-effort safety net cozytest runs at suite exit
#     and on the first failing test (the runner wraps it in `|| true`, so this
#     path alone cannot fail the suite — the inline call is what has teeth).
# The first @test also drains up front so a dirty namespace from a previous
# run cannot taint it (e2e-testing.md §3 — pre-cleanup at test start).

# Repo-root-relative: hack/cozytest.sh sources this file under `set -u` without
# ever setting BATS_TEST_DIRNAME (it is not real bats), so an interpolation of
# that unbound var would abort the whole suite at load time. cozytest's CWD is
# the repo root, so the plain relative path resolves — same convention
# hack/migration-50-etcd-adopt.bats uses with $PWD/...
ETCD_EXAMPLES="examples/backups/etcd"

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

# Teardown of the backup round-trip's resources (bucket, BackupJob/Backup,
# RestoreJob, etcdctl pod, creds Secret, BackupClass, strategy). cleanup.sh
# resumes the source Etcd HelmRelease first, so a round-trip aborted mid-restore
# does not strand the app suspended. Idempotent and safe even when the
# round-trip test never ran (deletes nothing else owns).
#
# Has teeth, mirroring etcd_drain: cleanup.sh is `set -e` with --ignore-not-found
# on every delete, so it exits 0 on a clean namespace and non-zero only on a real
# failure (stuck finalizer, RBAC error, wait timeout). We deliberately do NOT
# `|| true` it — inline at the end of the last @test (under set -e) a stuck
# cleanup must fail the suite rather than leak a wedged bucket/BackupClass into
# the next app test. The cozy_cleanup safety-net path stays best-effort because
# cozytest wraps cozy_cleanup in `|| true` (same split as etcd_drain).
backup_cleanup() {
  [ -x "${ETCD_EXAMPLES}/cleanup.sh" ] || return 0
  NAMESPACE=tenant-test "${ETCD_EXAMPLES}/cleanup.sh" 2>&1
}

cozy_cleanup() {
  backup_cleanup
  etcd_drain
}

dump_diagnostics() {
  # cozytest captures the test's stdout/stderr and prints it on failure, so
  # diagnostics go to stdout — fd 3 is never opened by the runner.
  echo "# --- diagnostics ---"
  kubectl -n tenant-test get etcdcluster.etcd-operator.cozystack.io,etcdmember.etcd-operator.cozystack.io,etcdsnapshot.etcd-operator.cozystack.io -o wide 2>&1 || true
  kubectl -n tenant-test describe etcdcluster.etcd-operator.cozystack.io etcd 2>&1 || true
  kubectl -n cozy-etcd-operator logs -l app.kubernetes.io/name=etcd-operator --tail=100 2>&1 || true

  # Backup round-trip chain. The snapshot agent runs in its OWN Job Pod (not the
  # operator), so a snapshot that wedges on the etcd read or the S3 upload leaves
  # its error ONLY in that Pod's log and in the BackupJob/EtcdSnapshot status —
  # none of which the operator log above captures. Dump the whole chain so a
  # failure is diagnosable from the CI log alone instead of a bare phase=Started.
  # The operator labels every snapshot Job (and its Pods) with
  # LabelCluster=etcd-operator.cozystack.io/cluster=<cluster>; chart-created
  # objects (members, the defrag Job) don't carry it, so the selector isolates
  # the snapshot Jobs. All best-effort — the contracts test creates none of
  # these resources, so the gets simply come back empty there.
  kubectl -n tenant-test get backupjobs.backups.cozystack.io,backups.backups.cozystack.io -o wide 2>&1 || true
  kubectl -n tenant-test describe backupjobs.backups.cozystack.io 2>&1 || true
  kubectl -n tenant-test describe etcdsnapshot.etcd-operator.cozystack.io 2>&1 || true
  for j in $(kubectl -n tenant-test get jobs -l etcd-operator.cozystack.io/cluster=etcd -o name 2>/dev/null); do
    kubectl -n tenant-test describe "$j" 2>&1 || true
    # --prefix tags each line with its Pod, so retried Job attempts (backoffLimit)
    # are distinguishable; a hung upload logs nothing, but describe + events show
    # the Pod still Running, and a fast failure prints the x509/dial error here.
    kubectl -n tenant-test logs "$j" --all-containers --prefix --tail=200 2>&1 || true
  done
  # Namespace events are the primary signal when a snapshot Pod hangs with an
  # empty log: they surface Pod scheduling/pull/network failures and the
  # BackupJob controller's own events that describe-job doesn't show.
  kubectl -n tenant-test get events --sort-by=.lastTimestamp 2>&1 | tail -40 || true
}

# Wait until the etcd HelmRelease is reconciled by Flux and its
# condition=ready is set. kubectl wait fails immediately on a missing
# resource, so poll for existence first.
wait_etcd_hr_ready() {
  timeout 60 sh -ec 'until kubectl -n tenant-test get hr/etcd >/dev/null 2>&1; do sleep 2; done'
  kubectl -n tenant-test wait hr/etcd --timeout=5m --for=condition=ready
}

@test "Create Etcd and verify v1alpha2 operator runtime contracts" {
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
  # Gate on FULL rollout, not mere discovery. The v1alpha2 operator flips
  # Available=True early (reason ClusterDiscovered) the instant the single seed
  # member forms the cluster, then scales the remaining members up one at a time
  # ("waiting for existing members to become Ready before next scale-up step").
  # Gating on Available=True therefore races the count assertion below and sees
  # only 1/3 Pods. status.readyMembers is the count of members whose EtcdMember
  # Ready condition is True (and the /scale subresource's current replicas), so
  # it only reaches 3 once every member Pod is Running and serving — the correct
  # precondition for every replica-count contract asserted below.
  kubectl -n tenant-test wait etcdcluster.etcd-operator.cozystack.io etcd --timeout=300s --for=jsonpath='{.status.readyMembers}'=3 || { dump_diagnostics; false; }

  # --- Runtime contracts the chart depends on but the operator owns. Each is a
  # silent failure if a future operator bump changes it, so assert against the
  # LIVE cluster (not the manifests). ---

  # podscrape.yaml + the WorkloadMonitor in etcd-cluster.yaml select member Pods
  # by app.kubernetes.io/{name=etcd,instance=etcd,managed-by=etcd-operator}.
  # If the operator labels members differently, metrics scraping and the
  # reported replica count silently break. Assert the selector matches all 3.
  running=$(kubectl -n tenant-test get pods \
    -l app.kubernetes.io/name=etcd,app.kubernetes.io/instance=etcd,app.kubernetes.io/managed-by=etcd-operator \
    --field-selector=status.phase=Running -o name) || { dump_diagnostics; false; }
  [ "$(printf '%s\n' "$running" | grep -c .)" -eq 3 ] || { echo "# WorkloadMonitor selector matched $(printf '%s\n' "$running" | grep -c .)/3 member Pods"; dump_diagnostics; false; }

  # vpa.yaml drives the EtcdCluster through its scale subresource; if the CRD
  # stops exposing /scale the VPA target silently fails to resolve.
  scale_replicas=$(kubectl -n tenant-test get etcdcluster.etcd-operator.cozystack.io etcd \
    --subresource=scale -o jsonpath='{.spec.replicas}') || { dump_diagnostics; false; }
  [ "$scale_replicas" = "3" ] || { echo "# /scale reported replicas='$scale_replicas' (want 3)"; dump_diagnostics; false; }

  # The WorkloadMonitor (etcd-cluster.yaml) selects member Pods by the same
  # operator-set labels and drives the app's dashboard health indicator. Assert
  # the controller actually matched them and flipped operational - a label
  # mismatch leaves it operational=false / wrong replica count, a silent break.
  kubectl -n tenant-test wait workloadmonitor.cozystack.io/etcd \
    --for=jsonpath='{.status.operational}'=true --timeout=180s \
    || { kubectl -n tenant-test get workloadmonitor.cozystack.io/etcd -o yaml 2>&1 || true; dump_diagnostics; false; }
  avail=$(kubectl -n tenant-test get workloadmonitor.cozystack.io/etcd -o jsonpath='{.status.availableReplicas}')
  [ "$avail" = "3" ] || { echo "# WorkloadMonitor availableReplicas=$avail (want 3)"; dump_diagnostics; false; }

  # vpa.yaml pins containerPolicies[].containerName=etcd; if the operator names
  # the member container anything else the VPA min/max bounds silently no-op.
  # Reuse the strict member selector + running-phase filter (as above) so the
  # reads below never dereference .items[0] on a non-member or not-yet-running
  # Pod.
  member_selector='app.kubernetes.io/name=etcd,app.kubernetes.io/instance=etcd,app.kubernetes.io/managed-by=etcd-operator'
  cname=$(kubectl -n tenant-test get pods -l "$member_selector" \
    --field-selector=status.phase=Running -o jsonpath='{.items[0].spec.containers[0].name}')
  [ "$cname" = "etcd" ] || { echo "# member container is '$cname' (want 'etcd' to match vpa.yaml containerName)"; dump_diagnostics; false; }

  # podscrape.yaml (VMPodScrape) scrapes the port named 'metrics' over http on
  # the member Pods. Assert the operator exposes that named port AND it actually
  # serves metrics on plaintext http (etcd 3.6 can be made to serve metrics on
  # the TLS client port, which would silently yield empty dashboards). Probe the
  # Pod IP directly - the scrape targets Pods, and the client Service is headless
  # and does not publish the metrics port.
  mport=$(kubectl -n tenant-test get pods -l "$member_selector" \
    --field-selector=status.phase=Running -o jsonpath='{.items[0].spec.containers[0].ports[?(@.name=="metrics")].containerPort}')
  [ -n "$mport" ] || { echo "# no container port named 'metrics' on member Pod (VMPodScrape would scrape nothing)"; dump_diagnostics; false; }
  pod_ip=$(kubectl -n tenant-test get pods -l "$member_selector" \
    --field-selector=status.phase=Running -o jsonpath='{.items[0].status.podIP}')
  [ -n "$pod_ip" ] || { echo "# no running member Pod IP found for metrics probe"; dump_diagnostics; false; }
  kubectl -n tenant-test delete pod etcd-metrics-probe --ignore-not-found >/dev/null 2>&1
  probe_out=$(kubectl -n tenant-test run etcd-metrics-probe --rm --restart=Never --attach \
    --image=curlimages/curl:8.10.1 \
    --overrides="{\"spec\":{\"securityContext\":{\"runAsNonRoot\":true,\"runAsUser\":1000,\"seccompProfile\":{\"type\":\"RuntimeDefault\"}},\"containers\":[{\"name\":\"c\",\"image\":\"curlimages/curl:8.10.1\",\"command\":[\"sh\",\"-c\",\"curl -fsS -o /dev/null -w code=%{http_code} http://${pod_ip}:${mport}/metrics\"],\"securityContext\":{\"allowPrivilegeEscalation\":false,\"capabilities\":{\"drop\":[\"ALL\"]}}}]}}" 2>&1) || true
  echo "$probe_out" | grep -q "code=200" || { echo "# metrics endpoint did not return 200 over http on the 'metrics' port; got: $probe_out"; dump_diagnostics; false; }

  # etcd-defrag.yaml's hourly CronJob targets the client Service
  # https://etcd.<ns>.svc:2379 with --cluster + the etcd-client-tls cert. Run it
  # on demand: a clean completion proves the Service name resolves to every
  # member and the client cert authenticates (the whole defrag contract). It
  # fails forever and silently if the operator names the Service differently.
  kubectl -n tenant-test delete job etcd-defrag-e2e --ignore-not-found
  kubectl -n tenant-test create job etcd-defrag-e2e --from=cronjob/etcd-defrag
  kubectl -n tenant-test wait job/etcd-defrag-e2e --for=condition=complete --timeout=180s \
    || { kubectl -n tenant-test logs job/etcd-defrag-e2e --tail=50 2>&1 || true; dump_diagnostics; false; }
  kubectl -n tenant-test delete job etcd-defrag-e2e --ignore-not-found
}

# Full backup -> EtcdSnapshot -> in-place RestoreJob round-trip against the
# v1alpha2 operator, driving the validated example scripts (examples/backups/etcd)
# as the harness so the test and the documented flow can never drift. The scripts
# run 01 strategy -> 02 bucket+BackupClass -> 03 source Etcd + sentinel write ->
# 04 BackupJob (waits Succeeded) -> 05 mutate sentinel + RestoreJob (waits
# Succeeded) and assert the sentinel reverts to its pre-mutation value -- the
# in-cluster witness that the snapshot round-tripped through S3.
#
# GATED OUT OF CI (opt in with ETCD_E2E_S3_ROUNDTRIP=1). The etcd S3 backup path
# cannot succeed in the kind e2e environment with etcd-operator v0.5.2:
#   - step 02 bakes the bucket's EXTERNAL ingress endpoint (.spec.secretS3.endpoint
#     from the BucketInfo) into the BackupClass; in CI that is the s3.example.org
#     placeholder, which the in-cluster snapshot Job can neither route to nor
#     TLS-validate (cert-manager rejects *.example.org);
#   - the in-cluster seaweedfs-s3.<ns>.svc:8333 endpoint serves HTTPS with a
#     self-signed cert, and the operator's snapshot agent (AWS SDK v2) verifies
#     against the image's system trust store -- the EtcdSnapshot CRD exposes no
#     InsecureSkipVerify / AWS_CA_BUNDLE / CA-mount surface, and there is no
#     plaintext-http S3 port -- so it cannot trust that cert;
#   - the strategy driver intentionally forbids a PVC destination
#     (api/backups/strategy/v1alpha1/etcd_types.go, CEL has(self.s3)), so there
#     is no in-cluster no-TLS fallback either.
# The round-trip therefore only works against a publicly-trusted S3 endpoint (a
# real cluster); the example scripts stay as the living documentation of that
# flow, and cozytest.sh has no working `skip`, so gate the flow with a plain
# conditional that still returns 0 in CI. Set ETCD_E2E_S3_ROUNDTRIP=1 to exercise
# it here on such a cluster.
#
# 03 re-applies the singleton Etcd 'etcd' in place, so this scenario reuses the
# cluster the previous @test created. NAMESPACE is overridden to the e2e tenant;
# run-all.sh is `set -e` so any step failing fails the test.
@test "Backup and in-place restore round-trip (EtcdSnapshot driver)" {
  if [ "${ETCD_E2E_S3_ROUNDTRIP:-0}" = "1" ]; then
    [ -x "${ETCD_EXAMPLES}/run-all.sh" ] || { echo "etcd backup example scripts not found at ${ETCD_EXAMPLES}"; false; }
    NAMESPACE=tenant-test "${ETCD_EXAMPLES}/run-all.sh" || { dump_diagnostics; false; }
  else
    echo "# S3 backup/restore round-trip gated out of CI: no publicly-trusted S3 endpoint in kind (see comment above). Set ETCD_E2E_S3_ROUNDTRIP=1 on a real cluster to run it."
  fi
  # Inline teardown for the last scenario: clean up the backup-flow resources
  # (idempotent -- a no-op when the round-trip was gated out) then drain etcd, its
  # retained data PVCs, and the Kamaji DataStore left by the contracts test. Both
  # run under set -e, so a teardown that does not settle fails the suite instead of
  # leaking into later app tests. This drain MUST stay in the last @test to keep
  # its teeth — if you append a scenario after this one, move the inline drain
  # there (cozy_cleanup alone is the best-effort safety net cozytest `|| true`s, so
  # it cannot fail the suite).
  backup_cleanup
  etcd_drain
}
