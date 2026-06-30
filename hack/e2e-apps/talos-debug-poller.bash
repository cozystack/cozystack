# TEMPORARY DIAGNOSTIC — debug/talos-bootstrap-timing — DO NOT MERGE.
#
# Background poller that timestamps tenant Talos worker-bootstrap timing against
# the MANAGEMENT cluster (default kubectl context). Sourced by the kubernetes
# e2e bats files (kubernetes-latest.bats / kubernetes-previous.bats) and wrapped
# around the run_kubernetes_test call: started just before the install, stopped
# after the test returns (with a post-failure capture tail). Every emitted line
# is tagged [TALOS-DEBUG ...] for grep.
#
# It lives as a .bash (not .sh) file and is wired in from the two .bats files
# rather than run-kubernetes.sh on purpose: the e2e Test-Impact-Analysis
# (hack/select-e2e.sh) escalates any hack/e2e-apps/*.sh change to the FULL
# suite, while editing the per-app .bats selects only that app and a non-.sh
# helper is ignored. So this wiring runs ONLY kubernetes-latest +
# kubernetes-previous, not every app.
#
# Goal: classify why the TalosConfigTemplate is late so md0 can't scale within
# its wait budget — slow input / slow-or-stuck reconcile Job pod / pod-churn
# (Job recreated) / TCT-eventually-lands-after-timeout. It dumps regardless of a
# fast or slow run so even a fast sample gives the happy-path baseline.
#
# This whole file plus its two .bats hooks are reverted once the timeline is
# captured. Nothing here ships.

# One timestamped snapshot. All queries hit the management cluster, namespace
# tenant-test, and are individually guarded so a transient API error or a
# not-yet-created object never aborts the loop.
_talos_debug_dump() {
  tn="$1"; rel="kubernetes-${tn}"; ns="tenant-test"
  now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  echo "[TALOS-DEBUG ts=${now} test=${tn}] ---- poll ----"

  # Parent kubernetes HelmRelease: Ready/reason, attempted revision, remediation
  # counters, and per-revision history statuses (a remediation cycle leaves
  # failed/uninstalled entries). The churn axis at the HelmRelease level.
  echo "[TALOS-DEBUG hr] $(kubectl get hr "${rel}" -n "${ns}" -o jsonpath='ready={.status.conditions[?(@.type=="Ready")].status} reason={.status.conditions[?(@.type=="Ready")].reason} rev={.status.lastAttemptedRevision} instFail={.status.installFailures} upgFail={.status.upgradeFailures} hist=[{range .status.history[*]}{.status},{end}]' 2>/dev/null || echo absent)"

  # talos-reconcile Job(s): creationTimestamp + uid (a new uid over time = the
  # pod-churn axis), active/succeeded/failed, backoffLimit. The Job itself is not
  # labelled with the reconcile selector (only its pods are), so filter by name.
  kubectl get jobs -n "${ns}" -o jsonpath='{range .items[*]}{.metadata.name}|uid={.metadata.uid}|created={.metadata.creationTimestamp}|active={.status.active}|succeeded={.status.succeeded}|failed={.status.failed}|backoffLimit={.spec.backoffLimit}{"\n"}{end}' 2>/dev/null \
    | grep talos-reconcile | sed 's/^/[TALOS-DEBUG job] /' || true

  # talos-reconcile pod(s): phase + waiting reason (Pending / ContainerCreating /
  # ImagePullBackOff / CrashLoopBackOff) + running start + restartCount. The
  # slow-or-stuck-pod axis. Pods carry the reconcile label.
  kubectl get pods -n "${ns}" -l "cozystack.io/talos-reconcile=${rel}" -o jsonpath='{range .items[*]}{.metadata.name}|uid={.metadata.uid}|created={.metadata.creationTimestamp}|phase={.status.phase}|start={.status.startTime}|waiting={.status.containerStatuses[0].state.waiting.reason}|running={.status.containerStatuses[0].state.running.startedAt}|restarts={.status.containerStatuses[0].restartCount}{"\n"}{end}' 2>/dev/null \
    | sed 's/^/[TALOS-DEBUG pod] /' || true

  # The product of the Job: does the TalosConfigTemplate exist yet, and when did
  # it first appear? (md0 status.replicas=2 is gated on this.)
  echo "[TALOS-DEBUG tct] $(kubectl get talosconfigtemplate "${rel}-md0" -n "${ns}" -o jsonpath='present created={.metadata.creationTimestamp}' 2>/dev/null || echo absent)"

  # The four runtime-wait inputs the Job blocks on.
  echo "[TALOS-DEBUG in.svc] clusterIP=$(kubectl get svc "${rel}" -n "${ns}" -o jsonpath='{.spec.clusterIP}' 2>/dev/null || echo none)"
  echo "[TALOS-DEBUG in.talos-ca-cert] ready=$(kubectl get certificate "${rel}-talos-ca" -n "${ns}" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || echo none)"
  echo "[TALOS-DEBUG in.talos-ca-secret] created=$(kubectl get secret "${rel}-talos-ca" -n "${ns}" -o jsonpath='{.metadata.creationTimestamp}' 2>/dev/null || echo absent)"
  echo "[TALOS-DEBUG in.k8s-ca-secret] created=$(kubectl get secret "${rel}-ca" -n "${ns}" -o jsonpath='{.metadata.creationTimestamp}' 2>/dev/null || echo absent)"
  echo "[TALOS-DEBUG in.coredns] clusterIP=$(kubectl get svc kube-dns -n kube-system -o jsonpath='{.spec.clusterIP}' 2>/dev/null || echo none)"

  # The consumer: MachineDeployment status, its MachineSet (CAPI blocks
  # MachineSet creation until the TCT exists), and MD/MS events (the
  # "cannot create a new MachineSet when templates do not exist" line lands here).
  echo "[TALOS-DEBUG md] $(kubectl get machinedeployment "${rel}-md0" -n "${ns}" -o jsonpath='replicas={.status.replicas} ready={.status.readyReplicas} phase={.status.phase}' 2>/dev/null || echo absent)"
  kubectl get machineset -n "${ns}" -o jsonpath='{range .items[*]}{.metadata.name}|created={.metadata.creationTimestamp}|replicas={.status.replicas}{"\n"}{end}' 2>/dev/null \
    | grep "${rel}-md0" | sed 's/^/[TALOS-DEBUG ms] /' || true
  kubectl get events -n "${ns}" -o jsonpath='{range .items[*]}{.lastTimestamp}|{.involvedObject.kind}/{.involvedObject.name}|{.reason}|{.message}{"\n"}{end}' 2>/dev/null \
    | grep -E "talos-reconcile|${rel}-md0" | sed 's/^/[TALOS-DEBUG ev] /' || true
}

# Start the poller in the background. Writes to a per-test artifact log under the
# cozyreport dir AND to stdout (tee), so the timeline survives in both the
# uploaded artifact and the raw job log.
_talos_debug_poller_start() {
  tn="$1"
  _talos_debug_log="${COZY_REPORT_DIR:-_out/cozyreport}/talos-debug-${tn}.log"
  mkdir -p "$(dirname "${_talos_debug_log}")" 2>/dev/null || true
  rm -f "/tmp/talos-debug-stop-${tn}"
  (
    # cozytest.sh runs the test body under `set -eux`, which this background
    # subshell inherits. Drop it: a best-effort diagnostic must never abort the
    # loop on a transient query failure, and the command trace would bury the
    # [TALOS-DEBUG] lines. Correctness is held by the per-query guards instead.
    set +eux
    i=0
    # Self-cap (~40 min) so an orphaned poller — an upstream wait failing before
    # the md0 stop-point is reached — terminates on its own well inside the CI
    # job budget.
    while [ ! -f "/tmp/talos-debug-stop-${tn}" ] && [ "${i}" -lt 160 ]; do
      _talos_debug_dump "${tn}" 2>&1 | tee -a "${_talos_debug_log}" || true
      i=$((i + 1))
      sleep 15
    done
  ) &
  TALOS_DEBUG_PID=$!
  echo "[TALOS-DEBUG] poller started pid=${TALOS_DEBUG_PID} log=${_talos_debug_log}"
}

# Stop the poller and reap it.
_talos_debug_poller_stop() {
  tn="$1"
  touch "/tmp/talos-debug-stop-${tn}" 2>/dev/null || true
  if [ -n "${TALOS_DEBUG_PID:-}" ]; then
    wait "${TALOS_DEBUG_PID}" 2>/dev/null || true
  fi
  echo "[TALOS-DEBUG] poller stopped"
}
