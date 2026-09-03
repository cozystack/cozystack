# shellcheck shell=bash
# Sourced by the chainsaw kubernetes-latest/previous Tests after cd to repo root.
. hack/e2e-chainsaw/_lib/remediation-guard.sh
. hack/e2e-chainsaw/_lib/talos-image-cache.sh
. hack/e2e-chainsaw/_lib/ghcr-mirror.sh

# talos_spec_block: emit the tenant Kubernetes CR `spec.talos` block combining the
# Talos OS image cache (imageFactoryURL) and the ghcr.io worker image-pull mirror
# (registryMirrors), each included only when its in-sandbox mirror is up. Prints
# nothing when both fall back to the public defaults, so the chart defaults apply.
# Indented for insertion directly under `spec:` in the heredoc below.
#
# No trailing newline, unlike the single-key helper this replaced: the caller reads
# it through `$(...)`, which strips one anyway, and `${talos_block}` sits on a line
# of its own in the heredoc, which supplies the break before the next `spec` key.
# An empty result therefore leaves a blank line, which is valid YAML.
talos_spec_block() {
  local url ghcr mirrors
  url=$(resolve_talos_image_factory_url)
  ghcr=$(resolve_ghcr_mirror_endpoint)
  mirrors=$(ghcr_registry_mirrors_block "$ghcr")
  [ -n "$url" ] || [ -n "$mirrors" ] || return 0
  printf '  talos:\n'
  [ -n "$url" ] && printf '    imageFactoryURL: %s\n' "$url"
  [ -n "$mirrors" ] && printf '%s' "$mirrors"
  return 0
}

# kubectl_wait_retry: wraps `kubectl wait` with retries against transient
# management-cluster apiserver/etcd errors.
#
# The e2e sandbox is a 3-node kind cluster on Talos VMs; the 3-instance
# etcd HA cluster can shed a leader under the accumulated CDI+DRBD IO +
# multiple back-to-back Kamaji tenant control-plane bringups this suite
# stacks. kubectl's watch-based `wait` exits non-zero on the FIRST server
# error it sees on the channel, even when the target is on the cusp of
# becoming Ready. Concretely, we have seen:
#   Error from server: etcdserver: leader changed
# fire mid-wait for `kubernetes-<test>-{cluster-autoscaler,kccm,kcsi-controller,base}`
# with 3 of 4 deployments already `condition met` and the 4th ~200ms
# from Ready. The snapshot-on-fail collector then showed all four at
# `readyReplicas: 2` — the wait exited early, not the target's fault.
#
# This wrapper retries a small number of times against a curated allowlist
# of transient server-side signatures. It does NOT swallow legitimate
# timeouts (`--timeout=... expired`) or NotFound; those still surface.
#
# The capture guards the assignment with `|| _rc=$?`: the chainsaw scripts run
# under `set -eu`, where a bare `_out=$(kubectl wait ...)` on a failing wait
# aborts the WHOLE sourcing script right there -- before the retry loop, before
# anything prints (stderr is captured into _out and dies with the shell). That
# exact shape killed the kubernetes-latest suite silently the moment a wait
# failed, with the transient-retry machinery never once reachable.
kubectl_wait_retry() {
  local _attempts=3
  local _i _out _rc
  for _i in $(seq 1 "${_attempts}"); do
    _rc=0
    _out=$(kubectl wait "$@" 2>&1) || _rc=$?
    if [ "${_rc}" = 0 ]; then
      printf '%s\n' "${_out}"
      return 0
    fi
    # Transient server-side signatures: etcd leader flap, etcd request
    # timeout, or apiserver watch channel closed without a clear reason.
    # Anything else (target NotFound, --timeout expired, permission
    # denied, etc.) is a real failure.
    if printf '%s' "${_out}" | grep --quiet --extended-regexp "etcdserver: leader changed|etcdserver: request timed out|the server was unable to return a response in the time allotted"; then
      printf 'kubectl_wait_retry: attempt %d/%d hit transient server error, retrying in 5s: %s\n' "${_i}" "${_attempts}" "${_out}" >&2
      sleep 5
      continue
    fi
    printf '%s\n' "${_out}"
    return "${_rc}"
  done
  printf 'kubectl_wait_retry: exhausted %d attempts on transient errors\n' "${_attempts}" >&2
  return 1
}

# Pure exit-condition for the inter-test drain loop (cozy_wait_tenant_drained).
# Each argument is one resource-probe capture: the stdout of a
# `kubectl get -o name` (empty once the resource is gone) or the literal "err"
# the loop substitutes when a probe itself fails. Returns 0 (drained) only when
# every capture holds nothing but whitespace; any capture with a non-whitespace
# character -- a resource name, or the "err" sentinel the loop injects on a
# probe failure -- yields non-zero, so a transient API blip is never misread as
# "the tenant has drained" (same guard as etcd_drain). Pure text logic,
# unit-tested in hack/run-kubernetes-drain_test.bats.
cozy_tenant_drained() {
  for _capture in "$@"; do
    case "$_capture" in
      *[![:space:]]*) return 1 ;;
    esac
  done
  return 0
}

# Block until the tenant cluster's KubeVirt compute and storage are actually
# released, not merely triggered for deletion. Deleting the Kubernetes CR
# returns as soon as its finalizers clear, but that only TRIGGERS teardown of
# the CAPK worker VMs and their DataVolume-backed disk PVCs. The virt-launcher
# pods keep their guest RAM reserved until the VMIs are gone, so without this
# barrier the next tenant test's worker VMs begin scheduling against a sandbox
# the previous tenant has not yet vacated -> memory starvation -> a worker VM
# misses the node-join budget and the test flakes on worker-node-join.
#
# Bounded and best-effort: cozytest runs cozy_cleanup wrapped in `|| true`, and
# this returns (loudly) on timeout, so a stuck teardown can never hang the job
# past the deadline -- it just leaves the sandbox no worse than before this
# wait existed. tenant-test is provisioned with etcd/monitoring/seaweedfs
# disabled (see the Tenant in hack/e2e-install-cozystack.bats), so it carries no
# baseline PVCs, and the e2e apps run sequentially each cleaning up after
# itself; at cleanup time the only VMs/VMIs/PVCs in the namespace belong to the
# tenant cluster being torn down, so a plain namespace-scoped probe is both safe
# and accurate (the worker-disk PVCs carry no cluster-scoping label to select on).
cozy_wait_tenant_drained() {
  _ns=tenant-test
  _timeout="${1:-300}"
  _deadline=$(( $(date +%s) + _timeout ))
  while :; do
    _vm=$(kubectl -n "$_ns" get virtualmachines.kubevirt.io -o name 2>/dev/null) || _vm=err
    _vmi=$(kubectl -n "$_ns" get virtualmachineinstances.kubevirt.io -o name 2>/dev/null) || _vmi=err
    _pvc=$(kubectl -n "$_ns" get pvc -o name 2>/dev/null) || _pvc=err
    if cozy_tenant_drained "$_vm" "$_vmi" "$_pvc"; then
      echo "» tenant VMs/VMIs/PVCs drained from $_ns"
      return 0
    fi
    if [ "$(date +%s)" -ge "$_deadline" ]; then
      echo "» WARNING: tenant teardown did not drain within ${_timeout}s; continuing (next test may face memory/storage pressure)" >&2
      kubectl -n "$_ns" get virtualmachines.kubevirt.io,virtualmachineinstances.kubevirt.io,pvc 2>&1 | sed 's/^/  drain-leftover: /' >&2 || true
      return 1
    fi
    sleep 5
  done
}

# Pure predicate for ONE node row of the capture cozy_has_schedulable_node
# scans. Encodes the scheduler's own admission rule for a Pod that tolerates
# nothing: the NodeUnschedulable plugin rejects a node whose
# .spec.unschedulable is set (what `kubectl get nodes` renders as
# SchedulingDisabled), and the TaintToleration plugin rejects one carrying any
# taint with effect NoSchedule or NoExecute. PreferNoSchedule only lowers the
# node's score, so it is deliberately not treated as blocking. Ready is checked
# explicitly rather than left to the not-ready taint, so the gate does not
# depend on how promptly the node-lifecycle controller applies that taint.
#
# The backend Pod is not literally toleration-free: DefaultTolerationSeconds
# admission gives every Pod a 300s NoExecute toleration for
# node.kubernetes.io/not-ready and node.kubernetes.io/unreachable. So for those
# two taints this predicate is stricter than the scheduler and would keep
# waiting where the Pod could in fact be placed. That errs toward waiting,
# never toward releasing the gate early, and requiring Ready makes the case
# nearly unreachable anyway.
cozy_node_accepts_pods() {
  # $1 Ready condition status, $2 .spec.unschedulable, $3 taint effects
  if [ "$1" != True ]; then
    return 1
  fi
  case "$2" in
    true | True) return 1 ;;
  esac
  case ",$3," in
    *,NoSchedule,* | *,NoExecute,*) return 1 ;;
  esac
  return 0
}

# Pure exit-condition for the tenant scheduling gate
# (cozy_wait_schedulable_node). The single argument is the capture of a
# `kubectl get nodes --no-headers -o custom-columns=NAME,READY,UNSCHEDULABLE,TAINTS`,
# one whitespace-separated row per node. custom-columns renders an absent field
# as the literal "<none>" and joins several taint effects with a comma, so both
# are matched as text. Returns 0 as soon as one row describes a node that would
# accept a Pod that tolerates nothing. A failed probe leaves the capture empty,
# which reports not-schedulable rather than schedulable, so an API blip can
# never release the gate (same guard as cozy_tenant_drained). The scan runs in
# the subshell on the right of the pipeline, so its `exit` ends that subshell
# and becomes the function's status -- it never leaves the caller. Pure text
# logic, unit-tested in hack/run-kubernetes-schedulable_test.bats.
cozy_has_schedulable_node() {
  printf '%s\n' "$1" | {
    while read -r _name _ready _unschedulable _taints _rest; do
      if [ -z "$_name" ]; then
        continue
      fi
      if cozy_node_accepts_pods "$_ready" "$_unschedulable" "$_taints"; then
        exit 0
      fi
    done
    exit 1
  }
}

# Block until the tenant cluster has at least one node that actually accepts a
# Pod, then print the node table it decided on (the same table the failure path
# prints, so the two outcomes are read the same way). The node-join gate in
# run_kubernetes_test establishes that two nodes are Ready, which is a weaker
# guarantee: a Ready node still carries `node.cilium.io/agent-not-ready` until
# the tenant's cilium agent claims it, and a node the bringup has not finished
# with is Ready,SchedulingDisabled.
# Scheduling against such a set is not stuck, only slow, and the time it takes
# is variable -- so it belongs in a budget of its own rather than inside the
# workload's readiness budget, where it is indistinguishable from a slow image
# pull or a failing probe.
cozy_wait_schedulable_node() {
  _kc="$1"
  _timeout="${2:-300}"
  _deadline=$(( $(date +%s) + _timeout ))
  while :; do
    _nodes=$(kubectl --kubeconfig "$_kc" get nodes --no-headers -o custom-columns='NAME:.metadata.name,READY:.status.conditions[?(@.type=="Ready")].status,UNSCHEDULABLE:.spec.unschedulable,TAINTS:.spec.taints[*].effect' 2>/dev/null) || _nodes=""
    if cozy_has_schedulable_node "$_nodes"; then
      echo "» tenant has a node that accepts Pods:"
      printf '%s\n' "$_nodes" | sed 's/^/  node: /'
      return 0
    fi
    if [ "$(date +%s)" -ge "$_deadline" ]; then
      echo "» no tenant node became schedulable (Ready, not cordoned, no NoSchedule/NoExecute taint) within ${_timeout}s" >&2
      printf '%s\n' "${_nodes:-<the node probe returned nothing>}" | sed 's/^/  node: /' >&2
      return 1
    fi
    sleep 5
  done
}

# Block until every ZFS storage pool on every LINSTOR satellite reports at
# least _min_free_gib of FreeCapacity. Motivation is proven from a
# cozyreport artefact captured by hack/cozyreport.sh (see PR #3044 run
# 28751310913, LINSTOR satellite ErrorReport 6A4AADFD-349B2-000000):
# tearing down a tenant Kubernetes worker with a `replicated` (autoPlace=3,
# DRBD) 20 GiB root disk removes the PVC from the API within seconds, but
# the ZFS `zvol destroy` on each satellite lags behind by tens of seconds
# as DRBD adjusts, unref counts drain and ZFS batch-destroys the datasets.
# cozy_wait_tenant_drained above only waits on the API-level PVC delete,
# not on the physical satellite space return; if the next tenant test
# starts inside that window it hits `zfs create -V ...` failing with
# `cannot create '...': out of space`, LINSTOR-CSI then retries autoplace,
# each retry racing the still-being-torn-down previous placement and
# stretching worker-Machine bringup past the MHC nodeStartupTimeout.
#
# Two 20 GiB replicated worker targets (60 GiB total per satellite, since
# autoPlace=3 places one replica per node) plus two 21 GiB CDI scratch
# PVCs (worst case both landing on the same node via the local
# storageClass) yields a ~82 GiB per-satellite peak footprint; 90 GiB
# default threshold covers that with margin. Bounded and best-effort like
# cozy_wait_tenant_drained: caller wraps in `|| true`, timeout returns
# loudly.
cozy_wait_linstor_pool_free() {
  _min_free_gib="${1:-90}"
  _timeout="${2:-300}"
  _min_free_kib=$(( _min_free_gib * 1024 * 1024 ))
  _deadline=$(( $(date +%s) + _timeout ))
  while :; do
    # jq lives inside the controller pod (Debian-bookworm base, `sh` is
    # dash — keep the heredoc POSIX-safe). LINSTOR's `--machine-readable`
    # output for `sp l` on LINSTOR 1.33.x is a one-element outer array
    # whose sole element is a flat array of storage-pool objects; each
    # pool object exposes free_capacity at the top level in KiB. Filter
    # to ZFS variants (both `ZFS` and `ZFS_THIN`) so DISKLESS
    # placeholders (whose free_capacity is a Long.MAX_VALUE sentinel)
    # and any future non-ZFS driver are skipped. Also guard against
    # OFFLINE satellites, whose pool objects omit free_capacity entirely
    # (StoragePool schema marks it optional) — without the null guard
    # `sort -n` would rank the string "null" ahead of real numbers and
    # the loop would silently poll to timeout. Emit
    # `<free_capacity_kib>:<node>` lines so a single sort yields the
    # smallest pool and its owner in one round-trip.
    _min_line=$(kubectl -n cozy-linstor exec deploy/linstor-controller -- sh -c '
      linstor --machine-readable sp l 2>/dev/null |
      jq -r "first | .[] | select((.provider_kind | test(\"^ZFS\")) and .free_capacity != null) | \"\(.free_capacity):\(.node_name)\"" |
      sort -n | head -n 1
    ' 2>/dev/null) || _min_line=""
    _min_kib="${_min_line%%:*}"
    _min_node="${_min_line#*:}"
    if [ -n "$_min_kib" ] && [ "$_min_kib" -ge "$_min_free_kib" ] 2>/dev/null; then
      echo "» LINSTOR ZFS pool free: smallest satellite ${_min_node} has $(( _min_kib / 1024 / 1024 )) GiB (>= ${_min_free_gib} GiB threshold)"
      return 0
    fi
    if [ "$(date +%s)" -ge "$_deadline" ]; then
      echo "» WARNING: LINSTOR ZFS pool free did not reach ${_min_free_gib} GiB on every satellite within ${_timeout}s (smallest observed: ${_min_kib:-unknown} KiB on ${_min_node:-unknown}); continuing (next test may face zfs create out-of-space)" >&2
      kubectl -n cozy-linstor exec deploy/linstor-controller -- linstor --no-color sp l 2>&1 | sed 's/^/  linstor-pool: /' >&2 || true
      return 1
    fi
    sleep 5
  done
}

# Unconditional cleanup hook, invoked from the kubernetes-* tests' Chainsaw
# `finally` block (which always runs, after any crust-gather `catch`). The tenant
# Kubernetes CR is applied imperatively (kubectl) inside run_kubernetes_test, so
# Chainsaw's auto-cleanup does not track it — `finally` is where it gets
# reclaimed. A failed run otherwise leaves the tenant cluster's worker-VM PVCs
# (tens of GiB) in tenant-test, exhausting the shared tenant-quota and
# cascade-failing every storage-heavy suite that runs afterwards. Best-effort
# (each delete is `|| true`) so a slow teardown never flips a passing test red.
cozy_cleanup() {
  # Delete any test-scoped tenant API LoadBalancer Services left by a failed run
  # so they don't leak MetalLB IPs from the shared host pool. Labeled by the
  # test so a single selector reaps them all.
  kubectl -n tenant-test delete service -l cozystack-e2e.io/tenant-api-lb --ignore-not-found --wait=false 2>/dev/null || true
  # A failed node-join capture creates a short-lived reader Certificate and a
  # hardened helper Pod. They are labelled separately from the tenant API LB:
  # the Secret contains a Talos client key and must be reaped even if the
  # diagnostic collector itself returned early.
  if ! kubectl -n tenant-test delete certificates.cert-manager.io -l cozystack-e2e.io/tenant-talos-diagnostics --ignore-not-found --wait=true --timeout=30s 2>/dev/null; then
    echo "» WARNING: failed to delete tenant Talos diagnostic Certificates" >&2
  fi
  if ! kubectl -n tenant-test delete pod,secret -l cozystack-e2e.io/tenant-talos-diagnostics --ignore-not-found --wait=false 2>/dev/null; then
    echo "» WARNING: failed to delete tenant Talos diagnostic Pod/Secret" >&2
  fi
  # Delete worker node pools (KubernetesNodes) before the parent Kubernetes CR:
  # the pool owns the workers and its pre-delete hook unpins the adopted objects
  # so uninstall is clean. Chainsaw does not track this imperatively applied CR,
  # so cozy_cleanup must reap it too.
  kubectl -n tenant-test delete kubernetesnodeses.apps.cozystack.io --all --ignore-not-found --wait=false 2>/dev/null || true
  kubectl -n tenant-test wait kubernetesnodeses.apps.cozystack.io --all --for=delete --timeout=5m 2>/dev/null || true
  kubectl -n tenant-test delete kuberneteses.apps.cozystack.io --all --ignore-not-found --wait=false 2>/dev/null || true
  kubectl -n tenant-test wait kuberneteses.apps.cozystack.io --all --for=delete --timeout=5m 2>/dev/null || true
  # The CR delete above finalizes once the Kubernetes CR is gone, which only
  # TRIGGERS KubeVirt VM teardown + PVC release. Block until the worker VMs,
  # VMIs (guest RAM) and disk PVCs are actually gone so the next tenant test
  # starts on a freed sandbox -- the root cause of the node-join flake.
  cozy_wait_tenant_drained 300 || true
  # PVC removal at the API level does not imply the satellite ZFS pool has
  # reclaimed the space (see comment on cozy_wait_linstor_pool_free above);
  # wait for FreeCapacity to return before yielding to the next tenant test.
  cozy_wait_linstor_pool_free 90 300 || true
}

# Snapshot the tenant cluster (its cilium/CSI/coredns internals) on a failed run.
# Registered as an EXIT trap INSIDE run_kubernetes_test so it fires during THIS
# test subshell's exit, before the success path (or cozy_cleanup) deletes the
# tenant API LoadBalancer. crust-gather reaches the tenant only through the
# kubeconfig's server URL (it connects directly — no host-proxy mode — and the
# in-cluster URL is unreachable from the runner), which is the LB IP and stays
# routable until teardown. CURRENT_TENANT_KC is a global so the handler can read
# it regardless of function scope at EXIT-trap time.
# Where a suite's captures land, spelled once. Two callers reach for the bare
# directory rather than a subdirectory of it -- this handler and the softened
# node-join path -- and a marker written beside a directory the snapshot no
# longer uses is a marker beside nothing.
_cozy_suite_snapshot_dir() {
  printf '%s\n' "${COZY_REPORT_DIR:-/workspace/_out/cozyreport}/snapshots/${COZY_SNAPSHOT_NAME:-kubernetes}"
}

_tenant_snapshot_on_fail() {
  _rc=$?
  [ "$_rc" -eq 0 ] && return 0
  command -v crust-gather >/dev/null 2>&1 || return 0
  [ -n "${CURRENT_TENANT_KC:-}" ] && [ -f "${CURRENT_TENANT_KC}" ] || return 0
  # COZY_SNAPSHOT_NAME is the Chainsaw test name (set in the kubernetes-* test's
  # script env), so the tenant snapshot co-locates with the host snapshot the
  # global .chainsaw.yaml catch writes under snapshots/<test>/. Falls back to a
  # generic name if sourced outside the Chainsaw harness.
  _snap=$(_cozy_suite_snapshot_dir)
  mkdir -p "$_snap" 2>/dev/null || true
  echo "» capturing tenant crust-gather snapshot (${CURRENT_TENANT_KC}) before teardown"
  # Bounded with a timeout for the same reason as the host snapshot in
  # cozytest.sh: an unbounded collect can hang for hours and wedge the job.
  # --duration is crust-gather's own collection budget (default 60s, which
  # silently truncates and skips its finish step on elapse); the outer
  # wall-clock has to exceed it because the API discovery that runs first is
  # not covered by that budget at all.
  # (timeout's own -k 30 / 360 are distinct from crust-gather's -k kubeconfig.)
  # Output goes to a log beside the snapshot instead of /dev/null so "complete
  # or truncated?" is answerable from the artifact, as for the host snapshot.
  _cg_rc=0
  # --disable-additional-logs: the host-log leg spawns a privileged debug pod per
  # node, which baseline PodSecurity rejects (403); its retry loop then burns the
  # whole --duration and crust-gather exits 1. Skip it — it collects nothing here.
  timeout -k 30 360 crust-gather collect -k "${CURRENT_TENANT_KC}" --disable-additional-logs --duration 180s \
    --exclude-kind Secret -f "$_snap/${CURRENT_TENANT_KC}" \
    >"$_snap/crust-gather-${CURRENT_TENANT_KC}.log" 2>&1 || _cg_rc=$?
  case "$_cg_rc" in
    0) echo "» tenant crust-gather snapshot complete (${CURRENT_TENANT_KC})" ;;
    124 | 137) echo "» tenant crust-gather snapshot TRUNCATED (wall-clock $_cg_rc); partial state kept, see $_snap/crust-gather-${CURRENT_TENANT_KC}.log" ;;
    *) echo "» tenant crust-gather snapshot FAILED (exit $_cg_rc); see $_snap/crust-gather-${CURRENT_TENANT_KC}.log" ;;
  esac
}

# Render name|IP rows for worker VMIs from a `kubectl get ... -o json` document.
# Kept pure so an absent default interface is represented by an empty IP instead
# of silently dropping the VMI from the diagnostic report.
cozy_tenant_worker_vmi_rows() {
  jq -r '.items[] | [.metadata.name, ([.status.interfaces[]? | select(.name == "default") | .ipAddress][0] // "")] | join("|")'
}

# Ask cert-manager for a short-lived, read-only Talos client certificate. The
# tenant chart deliberately uses TalosConfigTemplate generateType=none, so CABPT
# creates no client talosconfig for this worker-only Kamaji topology. Reusing the
# existing Talos CA Issuer avoids materialising an os:admin credential or copying
# the CA private key out of Kubernetes.
cozy_apply_tenant_talos_reader_certificate() {
  local test_name="$1"
  local release="kubernetes-${test_name}"
  local certificate="${release}-e2e-talos-reader"

  kubectl apply --request-timeout=30s -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${certificate}
  namespace: tenant-test
  labels:
    cozystack-e2e.io/tenant-talos-diagnostics: "${test_name}"
spec:
  duration: 1h
  renewBefore: 10m
  commonName: ${certificate}
  secretName: ${certificate}
  secretTemplate:
    labels:
      cozystack-e2e.io/tenant-talos-diagnostics: "${test_name}"
  subject:
    organizations:
    - os:reader
  privateKey:
    algorithm: Ed25519
    rotationPolicy: Always
  usages:
  - digital signature
  - client auth
  issuerRef:
    name: ${release}-talos-ca
    kind: Issuer
    group: cert-manager.io
EOF
}

# Build a local talosconfig from the reader Certificate. All private material
# stays below workdir, which the caller creates with mode 0700 and removes from
# a contained subshell EXIT trap; none of it is copied into cozyreport.
cozy_prepare_tenant_talosconfig() (
  local test_name="$1"
  local workdir="$2"
  local release="kubernetes-${test_name}"
  local certificate="${release}-e2e-talos-reader"

  umask 077
  # An interrupted prior run may have left a reader Secret signed by the old
  # tenant CA. Remove the Certificate first so cert-manager cannot recreate that
  # Secret between deletion and the new request, then wait for both objects to
  # disappear before waiting for issuance below.
  kubectl -n tenant-test delete certificate "${certificate}" \
    --ignore-not-found --wait=true --timeout=10s >/dev/null 2>&1 || return 1
  kubectl -n tenant-test delete secret "${certificate}" \
    --ignore-not-found --wait=true --timeout=10s >/dev/null 2>&1 || return 1
  cozy_apply_tenant_talos_reader_certificate "${test_name}" || return 1

  # cert-manager issuance is asynchronous. Wait on its actual Ready condition;
  # on expiry, preserve Certificate diagnostics without ever printing the
  # generated Secret.
  if ! kubectl_wait_retry -n tenant-test certificate "${certificate}" \
    --for=condition=Ready --timeout=30s; then
    echo "tenant Talos reader Certificate was not issued within 30s" >&2
    kubectl -n tenant-test describe certificate "${certificate}" --request-timeout=30s >&2 || true
    kubectl -n tenant-test get certificaterequests.cert-manager.io \
      -l cert-manager.io/certificate-name="${certificate}" -o wide --request-timeout=30s >&2 || true
    return 1
  fi

  kubectl -n tenant-test get secret "${release}-talos-ca" \
    -o go-template='{{index .data "tls.crt" | base64decode}}' --request-timeout=30s >"${workdir}/ca.crt" || return 1
  kubectl -n tenant-test get secret "${certificate}" \
    -o go-template='{{index .data "tls.crt" | base64decode}}' --request-timeout=30s >"${workdir}/client.crt" || return 1
  kubectl -n tenant-test get secret "${certificate}" \
    -o go-template='{{index .data "tls.key" | base64decode}}' --request-timeout=30s >"${workdir}/client.key" || return 1

  talosctl --talosconfig "${workdir}/talosconfig" config add "${release}" \
    --ca "${workdir}/ca.crt" --crt "${workdir}/client.crt" \
    --key "${workdir}/client.key" || return 1
  talosctl --talosconfig "${workdir}/talosconfig" config context "${release}" || return 1
  chmod 600 "${workdir}/talosconfig"
)

# Apply the helper Pod separately from its readiness/copy steps so its security
# invariants have focused unit coverage. The Pod runs on the management cluster
# in the same namespace as bridge-networked worker VMIs, which lets talosctl use
# each real VMI IP as both endpoint and node and keeps server-TLS verification
# valid. A MetalLB or localhost port-forward address would not be in the Talos
# serving certificate SANs.
cozy_apply_tenant_talos_diagnostics_pod() {
  local test_name="$1"
  local pod_name="kubernetes-${test_name}-talos-diagnostics"

  kubectl apply --request-timeout=30s -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: ${pod_name}
  namespace: tenant-test
  labels:
    cozystack-e2e.io/tenant-talos-diagnostics: "${test_name}"
spec:
  activeDeadlineSeconds: 900
  automountServiceAccountToken: false
  enableServiceLinks: false
  restartPolicy: Never
  terminationGracePeriodSeconds: 1
  securityContext:
    runAsNonRoot: true
    runAsUser: 1000
    runAsGroup: 1000
    fsGroup: 1000
    seccompProfile:
      type: RuntimeDefault
  containers:
  - name: diagnostics
    # The E2E sandbox ships the statically linked upstream talosctl release, so
    # reuse the digest-pinned Alpine helper already pulled by the test setup.
    image: docker.io/alpine/k8s:1.36.2@sha256:44ef4942e171939b9c665a4a84beb80e2dcdb9a24330d4651cfdfd2e9deecc47
    imagePullPolicy: IfNotPresent
    command: ["sh", "-c", "sleep 900"]
    securityContext:
      allowPrivilegeEscalation: false
      capabilities:
        drop:
        - ALL
      readOnlyRootFilesystem: true
    resources:
      requests:
        cpu: 10m
        memory: 16Mi
      limits:
        cpu: 200m
        memory: 256Mi
    volumeMounts:
    - name: tmp
      mountPath: /tmp
  volumes:
  - name: tmp
    emptyDir: {}
EOF
}

# Start the helper Pod and stream talosctl plus the reader talosconfig into its
# emptyDir. Streaming avoids a Secret volume and keeps the Pod independent of
# kubectl-cp/tar behavior. The final cleanup selector remains the authoritative
# backstop if any preparation step fails.
cozy_prepare_tenant_talos_diagnostics_pod() {
  local test_name="$1"
  local talosconfig="$2"
  local pod_name="kubernetes-${test_name}-talos-diagnostics"
  local talosctl_bin

  talosctl_bin=$(command -v talosctl) || return 1
  kubectl -n tenant-test delete pod "${pod_name}" --ignore-not-found \
    --wait=true --timeout=10s >/dev/null 2>&1 || return 1
  cozy_apply_tenant_talos_diagnostics_pod "${test_name}" || return 1

  if ! kubectl -n tenant-test wait pod "${pod_name}" \
    --for=condition=Ready --timeout=60s; then
    kubectl -n tenant-test describe pod "${pod_name}" --request-timeout=30s >&2 || true
    kubectl -n tenant-test get events \
      --field-selector involvedObject.name="${pod_name}" \
      --sort-by=.lastTimestamp --request-timeout=30s >&2 || true
    return 1
  fi

  if ! timeout -k 5 60 kubectl -n tenant-test exec -i "${pod_name}" \
    -c diagnostics -- sh -ec 'cat > /tmp/talosctl && chmod 500 /tmp/talosctl' \
    <"${talosctl_bin}"; then
    echo "failed to stream talosctl into tenant diagnostics Pod" >&2
    return 1
  fi
  if ! timeout -k 5 20 kubectl -n tenant-test exec -i "${pod_name}" \
    -c diagnostics -- sh -ec 'umask 077; cat > /tmp/talosconfig; chmod 600 /tmp/talosconfig' \
    <"${talosconfig}"; then
    echo "failed to stream tenant talosconfig into diagnostics Pod" >&2
    return 1
  fi
}

# Capture one bounded Talos command through the helper Pod and retain the exit
# status alongside partial output. A timeout or unreachable pre-apid worker is
# itself useful evidence and must not erase captures from the other worker.
#
# The bound is the caller's rather than a constant here because the reads differ
# in kind: two of them stream (a kernel ring buffer, five hundred lines of
# service log) and two are single tabular RPCs that a reachable apid answers at
# once. The whole walk sits inside the collector the diagnostics phase budget is
# sized against, so a second spent here is a second the collectors gated after
# it do not get -- and a bound picked for the streams, applied to the tabular
# reads, buys nothing and costs that.
cozy_capture_tenant_talos_command() {
  local pod_name="$1"
  local node_ip="$2"
  local output="$3"
  local bound="$4"
  shift 4
  local rc=0

  timeout -k 5 "${bound}" kubectl -n tenant-test exec "${pod_name}" -c diagnostics -- \
    /tmp/talosctl --talosconfig /tmp/talosconfig \
    -e "${node_ip}" -n "${node_ip}" "$@" >"${output}" 2>&1 || rc=$?
  printf '\n[capture exit code: %s]\n' "${rc}" >>"${output}"
  return "${rc}"
}

cozy_capture_tenant_talos_node() {
  local pod_name="$1"
  local node_ip="$2"
  local output_dir="$3"
  local answered=0

  mkdir -p "${output_dir}"
  printf 'endpoint: %s\nnode: %s\n' "${node_ip}" "${node_ip}" >"${output_dir}/target.txt"
  cozy_capture_tenant_talos_command "${pod_name}" "${node_ip}" \
    "${output_dir}/dmesg.log" 20 dmesg && answered=$((answered + 1))
  cozy_capture_tenant_talos_command "${pod_name}" "${node_ip}" \
    "${output_dir}/kubelet.log" 20 logs kubelet --tail=500 && answered=$((answered + 1))
  # The service state machine, which answers what the kubelet log cannot when
  # there is no kubelet log to read. Talos creates a service's log buffer when
  # its runner opens the log writer, which the runner does as it starts the
  # process -- so a kubelet still Waiting on its volumes, on time sync, or on
  # the container runtime reporting healthy has never reached its runner, has no
  # buffer, and `logs kubelet` fails with `log "kubelet" was not registered`.
  # That is a true statement about the log and says nothing about the service,
  # while the state and the condition being waited on are the finding. One call
  # covers every service, so the runtime the kubelet is waiting for arrives in
  # the same file.
  cozy_capture_tenant_talos_command "${pod_name}" "${node_ip}" \
    "${output_dir}/services.log" 10 services && answered=$((answered + 1))
  # Read as yaml because MTU lives in the LinkStatus spec and not among the
  # resource's print columns, so the default table omits the one field this is
  # read for: a guest at 1500 over an encapsulated fabric drops the large
  # segments and passes the small ones, which is the signature a stalled
  # transfer beside a healthy probe presents.
  cozy_capture_tenant_talos_command "${pod_name}" "${node_ip}" \
    "${output_dir}/links.yaml.log" 10 get links -o yaml && answered=$((answered + 1))
  # Stated for the worker rather than per read, because per read it is already
  # stated and still invisible: with apid refusing the connection each log holds
  # its own rpc error and an exit code, accurate one file at a time and adding
  # up to a directory that looks like a worker with little to report. Written
  # only when nothing completed, so a worker that answered one read of the four
  # is not written up as unreachable.
  #
  # `answered` counts reads that COMPLETED, and the wording below is bounded by
  # that rather than claiming more. A read killed at its bound has already
  # written what it managed to read, so the directory can hold real evidence
  # while nothing in it exited zero -- which is the slow-apid worker, the shape
  # this capture is most often reached on. A marker saying nothing was observed
  # would be the collector making exactly the unsupported claim the arms above
  # exist to prevent, one directory up.
  if [ "${answered}" -eq 0 ]; then
    # `|| true` on the write, not only `return 0` below: under a live errexit a
    # failing redirect aborts AT the redirect, so a trailing return never runs
    # and cannot be what carries the guarantee. A full scratch directory on the
    # runner is how this fails.
    printf '%s\n' \
      'no Talos read completed for this worker: all four failed or were cut short, so this directory holds no whole capture -- what is here is whatever arrived before a read was killed, or the error a read returned, and each log carries its own reason and exit code' \
      >"${output_dir}/CAPTURE-FAILED.txt" || true
  fi
  # Defensive rather than load-bearing today, and worth saying which: the one
  # call site reaches this through `cozy_capture_tenant_talos ... || true`, and
  # that suppression covers the whole subshell and everything it calls, so no
  # status returned from here can end the worker walk. What this pair buys is
  # that the guarantee survives a caller that drops the `|| true` -- the walk
  # continues to the next worker because nothing in here fails, rather than
  # because of how the call happens to be written.
  return 0
}

# Name the console experiment's own failure before the noise starts. Enabling
# logSerialConsole punches through the platform's cluster-wide disable, and if
# kubevirt/kubevirt#15989 still bites, guest-console-log wedges virt-launcher
# in Init: the workers never boot, no Node registers, and the suite reports
# "fewer than 2 tenant nodes Ready within 29m" -- byte-identical to the failure
# this instrumentation was added to study. A triager reading that line files it
# as the known flake and the experiment's answer is lost at the bottom of
# POD-STATE.txt. A diagnostic whose own worst case is disguised as the bug it
# investigates is worth less than none, so it says so itself, and says it
# first.
#
# The bit it reads is `ready` on the init container. This sidecar is built with
# no probes at all, and kubelet's UpdatePodStatus sets `ready = !exists` for a
# container with no readiness worker once it is running -- liveness is not an
# input to readiness anywhere. So the bit is exactly "the container is
# running", and false means not running: never-started, waiting between
# restarts, or already-terminated. Running-but-not-ready is not a shape this
# container can hold, which is why the taxonomy below does not claim it.
# Those are not one finding: the hang starts the container and then fails it,
# so with restartPolicy Always it settles into CrashLoopBackOff and passes
# through Error, while a superseded virt-launcher from a VM restart exits
# cleanly and terminates Completed. ContainerState is a union, so a waiting
# reason alone leaves the terminated case blank and cannot tell the two apart.
# Both reasons are carried so each shape arrives labelled.
#
# Even so this line points rather than concludes, and it says so out loud: the
# bit cannot separate every shape, the describe that follows can, and a
# headline that asserts more than the bit carries is the same mislabel this
# function exists to prevent, merely pointing the other way.
cozy_report_guest_console_wedge() {
  local rows read_rc=0
  local read_err

  if ! read_err=$(mktemp); then
    # Same rule as the read-failure branch below: saying nothing here would
    # read as "the console container is fine" when nothing was checked.
    echo "=== could not allocate a scratch file to read virt-launcher Pod state; whether guest-console-log started is unknown, not fine ==="
    return 0
  fi
  rows=$(timeout -k 5 30 kubectl -n tenant-test get pods \
    -l kubevirt.io=virt-launcher \
    -o jsonpath='{range .items[*]}{range .status.initContainerStatuses[?(@.name=="guest-console-log")]}{.ready}{" "}{.state.waiting.reason}{.state.terminated.reason}{" "}{end}{.metadata.name}{"\n"}{end}' \
    --request-timeout=30s 2>"${read_err}") || read_rc=$?
  if [ "${read_rc}" -ne 0 ]; then
    # Silence here would be the same mislabel inverted: the reader would take
    # the absent headline as "the console container is fine" when nothing was
    # actually checked.
    echo "=== could not read virt-launcher Pod state (exit ${read_rc}); whether guest-console-log started is unknown, not fine ==="
    cat "${read_err}" || true
    rm -f "${read_err}"
    return 0
  fi
  rm -f "${read_err}"

  case "${rows}" in
    *"false "*) ;;
    *) return 0 ;;
  esac

  echo "=== guest-console-log is not running on the workers below; if these are current Pods this is the console experiment failing rather than the node-join failure it instruments (kubevirt/kubevirt#15989) ==="
  echo "=== a reason of CrashLoopBackOff or Error below is the wedge shape (kubevirt/kubevirt#15989); Completed is a superseded Pod and not this bug; any other reason, or none, is not covered by either and the describe that follows settles it ==="
  echo "=== if it is the wedge: the cluster-wide disableSerialConsoleLog in packages/system/kubevirt exists for this, and dropping logSerialConsole from the e2e node group returns the suite to its prior behaviour ==="
  printf '%s\n' "${rows}" | grep '^false ' || true
}

# Check that KubeVirt actually attached the guest console container, on the
# path where the workers did boot. The capture below runs only when node-join
# fails, so without this the suite would first learn that the node group's
# logSerialConsole never took effect in the run that needed the console, when
# it can no longer be recovered. The platform disables console logging
# cluster-wide (see packages/system/kubevirt), so what this asks is whether
# the per-VM override beat that on the KubeVirt version actually shipping.
#
# The read's own status is kept separate from the answer it returns, and the
# two get different exit codes, because they are different claims. Piping
# kubectl into grep would collapse them: a read that failed produces no output,
# grep finds nothing, and an API blip would be reported as "the override did
# not take effect" -- a cause that was never established, sending the next
# reader at KubeVirt config instead of at the apiserver. A selector that
# matched no Pod at all is the same trap one step further on, and is kept
# apart from a Pod that matched and carries nothing -- the second is the
# finding, the first is not a statement about any Pod.
#
# What it establishes is that SOME virt-launcher Pod in the namespace carries
# the container, not that every one does. That is the direction worth erring
# in for a check whose only job is to catch an inert setting: it can miss, but
# it cannot fail a run over a Pod that is not this test's. The match runs over
# the whole name=containers line, so a Pod named after the container would
# satisfy it -- these names come from the Machine name, so that cannot happen
# here, but a reader changing the format should know the glob spans both.
#
#   0  a Pod carries the container
#   1  the read did not answer, or matched no Pod; not evidence either way
#   2  Pods matched and none carries the container
cozy_assert_guest_console_attached() {
  local init_names read_rc=0
  local attached total

  # The Pod name is in the output for a reason that is not cosmetic: without
  # it, a Pod whose initContainers list is absent emits a bare newline, command
  # substitution strips it, and "Pods matched, none carries the container" is
  # byte-identical to "no Pod matched". Those two must not collapse -- the
  # first is the finding this whole check exists for, and on these workers it
  # is also the only shape it can take, since guest-console-log is the only
  # init container a DataVolume-booted virt-launcher Pod ever has.
  init_names=$(timeout -k 5 30 kubectl -n tenant-test get pods \
    -l kubevirt.io=virt-launcher \
    -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.spec.initContainers[*].name}{" "}{.spec.containers[*].name}{"\n"}{end}' \
    --request-timeout=30s) || read_rc=$?
  if [ "${read_rc}" -ne 0 ]; then
    echo "could not read tenant virt-launcher Pods to verify the guest console container (exit ${read_rc})" >&2
    echo "this says nothing about logSerialConsole either way; continuing" >&2
    return 1
  fi

  if [ -z "${init_names}" ]; then
    echo "no tenant virt-launcher Pod matched; nothing to verify about the guest console container" >&2
    echo "this says nothing about logSerialConsole either way; continuing" >&2
    return 1
  fi

  case "${init_names}" in
    *guest-console-log*)
      # The positive outcome goes on the record too. This run doubles as the
      # revalidation of a platform-wide workaround, and a check that returns
      # 0 in silence leaves "the override worked" to be inferred from the
      # absence of a complaint -- the inference this collector refuses to
      # make everywhere else. Counted rather than asserted for every Pod: the
      # check is deliberately "some Pod carries it", so the ratio is the
      # honest way to say what was seen.
      attached=$(printf '%s\n' "${init_names}" | grep -c 'guest-console-log' || true)
      total=$(printf '%s\n' "${init_names}" | grep -c '=' || true)
      echo "» guest-console-log attached on ${attached} of ${total} virt-launcher Pods"
      return 0
      ;;
  esac

  echo "tenant worker virt-launcher Pods carry no guest-console-log container" >&2
  echo "the node group asked for logSerialConsole, so either the cluster-wide" >&2
  echo "disableSerialConsoleLog won the override or the field never reached the VM" >&2
  # Bounded like the read above, and for the same reason: these run on a path
  # that ends in exit 1, and a hang here would take the step's whole deadline
  # with it -- losing the tenant snapshot that the exit is supposed to reach.
  timeout -k 5 30 kubectl -n tenant-test get pods -l kubevirt.io=virt-launcher \
    -o jsonpath='{range .items[*]}{.metadata.name}{": "}{.spec.initContainers[*].name}{"\n"}{end}' \
    --request-timeout=30s || true
  timeout -k 5 30 kubectl -n tenant-test get virtualmachineinstances.kubevirt.io \
    -o jsonpath='{range .items[*]}{.metadata.name}{": logSerialConsole="}{.spec.domain.devices.logSerialConsole}{"\n"}{end}' \
    --request-timeout=30s || true
  return 2
}

# Capture each tenant worker's guest serial console from the management
# cluster. When a node group sets logSerialConsole, KubeVirt streams the
# console into a guest-console-log container beside virt-launcher, and reading
# it needs nothing from inside the guest: no talosctl, no client certificate,
# no helper Pod, no reachable apid. That is the whole reason it exists. The
# talosctl capture below can only describe a worker whose apid already answers,
# so a worker that stalls earlier -- the case where no Node registers and no
# certificate request is ever made -- is invisible to it by construction, and
# the console is the only remaining surface.
#
# Reads start at the beginning of the stream rather than tailing it, because a
# guest that repaints its console would push the boot output out of any tail
# window while --limit-bytes truncates from the far end instead. That covers
# only what kubectl returns: whatever the kubelet has already rotated out of
# the container log (containerLogMaxSize, 10Mi x 5 by default) is gone before
# the capture runs, and no read option recovers it.
#
# An absent guest-console-log container is the finding, not a gap: it says
# console logging did not take effect on this run, which is a different fact
# from a guest that printed nothing. kubectl's refusal is kept beside the
# capture rather than inside it so the two stay distinguishable, and the
# capture file says which of them happened.
#
# The Pod list is namespace-wide rather than scoped to one release. The two
# virt-launcher captures immediately above the call site already select that
# way, and suites run one at a time. Below the cap a Pod that does not belong
# to this test costs one extra file named after itself and nothing else; at
# the cap it can displace one of this suite's own, since kubectl returns the
# list name-sorted. COLLECTION-TRUNCATED.txt is what keeps that visible.
#
# Takes the context it was called in, because it is called from both paths and
# both write here. On the failure path the console is the evidence; on the
# passing path it is the baseline that evidence is read against, and the two are
# the same files in the same directory. Which one a tarball holds is otherwise
# recoverable only from the run's verdict somewhere else entirely, so the
# capture states it.
cozy_capture_tenant_serial_console() {
  local context="$1"
  # How many Pods the walk may cover, taken from the caller because the two
  # paths have different room. The failure path runs inside a phase whose budget
  # is derived against the whole op, so it can afford the pool at its maximum.
  # The passing path has no phase and no budget of its own: it runs inside the
  # same operation whose ceiling the suite has already spent most of, and a
  # diagnostic that turns a suite green-then-red for running long would be worse
  # than the missing baseline it exists to collect. So that caller asks for the
  # pool's minimum, which is what a boot baseline needs -- two workers booting
  # is the same measurement as ten -- and the cap that fires records what it
  # dropped either way.
  local max_pods="$2"
  local report_dir="${COZY_REPORT_DIR:-/workspace/_out/cozyreport}/snapshots/${COZY_SNAPSHOT_NAME:-kubernetes}/tenant-serial-console"
  local pods pod rc
  local list_rc=0
  local seen=0
  local walked=0
  local silent=0
  local cut_short=0
  local pod_err read_at read_done
  local timeline timeline_err timeline_why timeline_rc=0 timeline_err_kept=0
  local err_log="${report_dir}/setup-error.log"
  local warn_log="${report_dir}/READ-WARNINGS.txt"

  mkdir -p "${report_dir}"
  # Written before the first read, not after the walk: the outcome this most
  # needs to be legible in is the one where nothing was captured, and every
  # early return below leaves the directory holding a note alone.
  printf '%s\n' "${context}" >"${report_dir}/CAPTURE-CONTEXT.txt"
  # stderr goes to its own file rather than being folded into the captured
  # stdout: a warning kubectl prints on an otherwise successful call would
  # otherwise be read back as a Pod name. Which file it goes to is decided by
  # the exit status, not by the fact that something was written: an apiserver
  # Warning header or a deprecation notice arrives on stderr with a zero exit,
  # so a warning beside a healthy read belongs in READ-WARNINGS.txt as
  # elsewhere in this tree, while the stderr of a call that actually failed IS
  # the diagnosis and belongs in setup-error.log. The list is wrapped in
  # timeout for the same reason every read below it is: --request-timeout
  # bounds the HTTP request, not a client retrying against a wedged
  # apiserver, and a hang here would cost the whole
  # section rather than one Pod.
  pods=$(timeout -k 5 30 kubectl -n tenant-test get pods -l kubevirt.io=virt-launcher \
    -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' \
    --request-timeout=30s 2>"${warn_log}") || list_rc=$?
  if [ "${list_rc}" -ne 0 ]; then
    echo "failed to list tenant virt-launcher Pods for serial console capture" >&2
    mv "${warn_log}" "${err_log}" 2>/dev/null || true
    printf '%s\n' \
      "failed to list tenant virt-launcher Pods for serial console capture (exit ${list_rc})" \
      >>"${err_log}"
    return 1
  fi
  [ -s "${warn_log}" ] || rm -f "${warn_log}"
  if [ -z "${pods}" ]; then
    echo "no virt-launcher Pod found for serial console capture" >&2
    printf '%s\n' 'no virt-launcher Pod found in namespace tenant-test' \
      >"${err_log}"
    return 1
  fi

  # Cap the walk, at the number the caller asked for. Every read here is bounded,
  # but the pool can reach maxReplicas, so
  # an uncapped loop is a term whose size the cluster sets rather than this file --
  # inside a failure path that has to reach the tenant snapshot at the end of it.
  # The phase budget beside COZY_DIAG_PHASE_BUDGET is what stops the collectors as a
  # group from spending the time that snapshot needs; this cap is what stops this
  # walk from being the collector that spends it. A cap that fired is recorded with both
  # counts rather than leaving the shortfall implicit, since a short listing
  # otherwise reads as a small pool rather than a truncated walk.
  for pod in ${pods}; do
    seen=$((seen + 1))
    if [ "${seen}" -gt "${max_pods}" ]; then
      echo "--- guest serial console capture stopped at ${max_pods} Pods ---"
      printf 'capture stopped after %s Pods; %s matched in total\n' \
        "${max_pods}" "$(printf '%s\n' ${pods} | wc -l | tr -d ' ')" \
        >"${report_dir}/COLLECTION-TRUNCATED.txt"
      break
    fi
    # Counted after the cap test, not from it. `seen` is incremented before that
    # test so it can fire on the Pod past the cap, which leaves it one higher
    # than the number of Pods read whenever the walk truncates. Reporting a
    # count of silent consoles against it would state a denominator that is
    # neither what was read nor what matched -- and the truncation marker beside
    # the capture prints both correctly, so the job log and the artifact would
    # disagree on a run nobody opens the artifact for.
    walked=$((walked + 1))
    echo "--- capturing guest serial console: ${pod} ---"
    rc=0
    # stderr goes beside the capture, not into it. Folded in, kubectl's own
    # "container guest-console-log is not valid for pod ..." would make the
    # file non-empty and the console would look like it had said something.
    # That error is the headline case here -- it is what a container wedged in
    # Init looks like -- so it has to stay separable from console bytes. Which
    # name it lands under is decided by the exit status, the same way the list
    # read above decides it: kubectl writes warnings on stderr with a zero
    # status too, and a warning beside a healthy capture is not an error.
    pod_err="${report_dir}/${pod}.read-error.log"
    read_at=$(date +%s)
    timeout -k 5 30 kubectl -n tenant-test logs "${pod}" \
      -c guest-console-log --limit-bytes=1048576 \
      >"${report_dir}/${pod}.log" 2>"${pod_err}" || rc=$?
    read_done=$(date +%s)
    if [ ! -s "${pod_err}" ]; then
      rm -f "${pod_err}"
      pod_err=
    elif [ "${rc}" -eq 0 ]; then
      mv "${pod_err}" "${report_dir}/${pod}.READ-WARNINGS.txt" 2>/dev/null || true
      pod_err=
    fi
    # Tested before the exit-code line is appended, or nothing is ever empty.
    # Three outcomes, three labels, because only one of them is a statement
    # about what the guest did.
    if [ ! -s "${report_dir}/${pod}.log" ] && [ "${rc}" -ne 0 ]; then
      silent=$((silent + 1))
      # timeout kills the child without a word, so a read can fail having
      # written nothing to either stream. Naming a file that was never created
      # sends the reader looking for evidence that does not exist.
      if [ -n "${pod_err}" ]; then
        printf '%s\n' \
          'the read returned no console at all; see read-error.log and POD-STATE.txt' \
          >>"${report_dir}/${pod}.log"
      else
        printf '%s\n' \
          'the read failed silently and returned no console at all; see POD-STATE.txt' \
          >>"${report_dir}/${pod}.log"
      fi
    elif [ ! -s "${report_dir}/${pod}.log" ]; then
      silent=$((silent + 1))
      printf '%s\n' \
        'the read succeeded and returned nothing; see POD-STATE.txt for whether the container started' \
        >>"${report_dir}/${pod}.log"
    elif [ "${rc}" -ne 0 ]; then
      silent=$((silent + 1))
      # Counted apart as well as together. `silent` gates the Pod-state read
      # below, and a console cut off part way needs that read as much as an
      # empty one does; but it is not silence, and the summary line these feed
      # would otherwise report bytes that arrived as bytes that never did.
      cut_short=$((cut_short + 1))
      if [ -n "${pod_err}" ]; then
        printf '%s\n' \
          'console output is truncated; see read-error.log and POD-STATE.txt' \
          >>"${report_dir}/${pod}.log"
      else
        printf '%s\n' \
          'console output is truncated; see POD-STATE.txt for the Pod state' \
          >>"${report_dir}/${pod}.log"
      fi
    fi
    # The wall clock the log's own stamps have to be read against. Every line a
    # guest prints is stamped in seconds since that guest's kernel started, so
    # the file says how far the guest had got and nothing about when that was.
    # Paired with the compute container's start in CAPTURE-TIMELINE.txt beside
    # it, this turns the two questions a short console raises -- a guest that
    # started too late to reach a milestone, or one that reached it and then
    # went quiet -- into one subtraction. The read instant is otherwise nowhere
    # in the file: it survives as the artifact's mtime, which is metadata rather
    # than content, so a reader who opens the log sees nothing and anything that
    # regenerates or rewrites the file loses it.
    # Same bracket form the cAdvisor captures use, and for the same
    # reason: the stream is drained somewhere between the two instants, so an
    # age computed against either end is exact to the width of the bracket.
    printf '\n[console read started at %s and returned at %s epoch seconds]\n' \
      "${read_at}" "${read_done}" >>"${report_dir}/${pod}.log"
    printf '[capture exit code: %s]\n' "${rc}" >>"${report_dir}/${pod}.log"
  done

  # When each guest started, read once for the whole selector rather than per
  # Pod, like the Pod-state read below and for the same reason: several workers
  # are one finding and paying per Pod would put back the unbounded term the cap
  # removed. Unlike that read this one is unconditional, because the question it
  # answers is raised by a console that arrived intact just as much as by one
  # that did not -- a full log ending mid-boot is the shape this failure keeps
  # producing, and it is the shape that says nothing on its own.
  #
  # Both container starts, not just compute's: the guest's clock begins with
  # compute, and the console container starting later or restarting would mean
  # the stream missed a stretch the guest did print, which reads identically to
  # a guest that was quiet.
  #
  # The console container is asked for in both status lists, the way the attach
  # check asks for it in both spec lists. KubeVirt runs it as an init container
  # today, so its runtime state is under initContainerStatuses and reading only
  # containerStatuses returns empty for a container that started fine, which
  # costs the field its meaning. Missing keys are not an error for a jsonpath
  # template, so naming both costs an empty string on whichever list does not
  # hold it.
  #
  # Both fields read the RUNNING instance, so a container that has since
  # terminated reads empty here even though it ran. The legend says so rather
  # than the query chasing lastState as well: what this pairs with is a console
  # that was read from a live Pod, and a second start time for an instance whose
  # console is not the one in the file beside it would be read as the same
  # quantity.
  timeline="${report_dir}/CAPTURE-TIMELINE.txt"
  timeline_err="${report_dir}/CAPTURE-TIMELINE-warnings.txt"
  {
    printf '=== when each tenant worker guest started, against the reads above ===\n'
    printf '%s\n' \
      'each console log carries the instant its own read returned, in epoch seconds; the times below are RFC 3339, so convert before subtracting rather than comparing the two as printed. computeStartedAt subtracted from that instant is how long the guest had been alive when it was read; compare the result against the last [ ss.ssssss] stamp in the log, which counts from the same start. Close together means the guest was still printing when the read ran, so the log ends because the guest had not got further. Far apart means the console stopped growing for the difference, which is the guest going quiet or the stream no longer carrying it; consoleStartedAt is what separates those, since a console container that started late or restarted missed whatever the guest printed before it. Read that against CAPTURE-CONTEXT.txt before calling it a finding: on a capture taken after a suite passed, the guest finished booting long before the read and a large difference is what a healthy worker looks like, so what is worth comparing there is the guest seconds each milestone landed at rather than the size of the gap. These are the running instances only: a Pod whose compute has no start time has no compute container running now, which is not the same as never having had one, and phase beside it is what says which'
  } >"${timeline}"
  # Bounded like every read here, and its stderr kept out of the file for the
  # same reason the reads above keep theirs out: the success probe below matches
  # on the row prefix, and a warning kubectl writes beside a healthy read would
  # otherwise be sitting inside the rows it is supposed to be distinguished
  # from.
  timeout -k 5 30 kubectl -n tenant-test get pods -l kubevirt.io=virt-launcher \
    -o jsonpath='{range .items[*]}{"name="}{.metadata.name}{" phase="}{.status.phase}{" podStartTime="}{.status.startTime}{" computeStartedAt="}{.status.containerStatuses[?(@.name=="compute")].state.running.startedAt}{" consoleStartedAt="}{.status.initContainerStatuses[?(@.name=="guest-console-log")].state.running.startedAt}{.status.containerStatuses[?(@.name=="guest-console-log")].state.running.startedAt}{"\n"}{end}' \
    --request-timeout=30s >>"${timeline}" 2>>"${timeline_err}" || timeline_rc=$?
  timeline_err_kept=0
  if [ ! -s "${timeline_err}" ]; then
    rm -f "${timeline_err}"
  elif [ "${timeline_rc}" -ne 0 ]; then
    if mv "${timeline_err}" "${report_dir}/CAPTURE-TIMELINE-error.log" 2>/dev/null; then
      timeline_err_kept=1
    fi
  fi
  # Three outcomes, three labels, because only one of them says anything about
  # the guests. A read that failed is a statement about this machine's view of
  # the cluster; a read that answered with nothing is a statement about the
  # namespace; and neither may be left as an absent file, which reads as a
  # capture that had no reason to write one. The failed arm is tested for rows
  # as well, because a read cut off part way leaves both -- and a note saying
  # nothing was observed, sitting under rows that plainly were, is a file that
  # contradicts itself.
  # The error file is named only when there is one. `timeout` kills the child
  # without a word, so this read can fail having written to neither stream, and
  # a note pointing at a file that was never created sends the reader looking
  # for evidence that does not exist -- the same trap the per-Pod notes above
  # avoid the same way.
  if [ "${timeline_err_kept}" -eq 1 ]; then
    timeline_why='; the reason it gave is in CAPTURE-TIMELINE-error.log'
  else
    timeline_why='; it gave no reason on either stream'
  fi
  if [ "${timeline_rc}" -ne 0 ] && grep -q '^name=' "${timeline}"; then
    printf '%s\n' \
      "the start-time read was cut off (exit ${timeline_rc}); the rows above are a prefix and the Pods missing from them were not observed${timeline_why}" \
      >>"${timeline}"
  elif [ "${timeline_rc}" -ne 0 ]; then
    printf '%s\n' \
      "the start-time read failed (exit ${timeline_rc}); when these guests started was not observed, which is not the same as their having no start time${timeline_why}" \
      >>"${timeline}"
  elif ! grep -q '^name=' "${timeline}"; then
    printf '%s\n' \
      'the start-time read succeeded and returned no rows; the walk above read consoles from Pods this selector no longer matches' \
      >>"${timeline}"
  fi

  # An empty console log has two causes that are indistinguishable in the log
  # itself: the guest printed nothing, or the container never started and there
  # was no stream to read. The second is what kubevirt/kubevirt#15989 produces,
  # and it is the reading that would otherwise be filed as a quiet guest --
  # silence reported as a result is the failure this collector exists to
  # prevent, so it is not allowed to produce one. The Pod's own state and its
  # events are what separate the two, and they are read once for the whole
  # selector rather than per Pod: several silent workers are one finding, and
  # paying per Pod would put back the unbounded term the cap removed.
  if [ "${silent}" -gt 0 ]; then
    # Said on stdout as well as in the report, because the two callers read
    # different things. On the failure path the report is opened either way; on
    # the passing path it is not opened at all, and a baseline that came back
    # empty there would otherwise be visible only to whoever downloads a green
    # run's artifact -- which is nobody. The count is not a failure: on the
    # failure path a guest that printed nothing is itself a finding.
    echo "» of the ${walked} guest consoles read, $((silent - cut_short)) came back empty and ${cut_short} were cut short; see POD-STATE.txt beside the capture"
    {
      printf '=== describe pods -l kubevirt.io=virt-launcher ===\n'
      timeout -k 5 30 kubectl -n tenant-test describe pods \
        -l kubevirt.io=virt-launcher --request-timeout=30s 2>&1 || true
      printf '\n=== Pod events in tenant-test (not filtered to virt-launcher) ===\n'
      timeout -k 5 30 kubectl -n tenant-test get events \
        --field-selector involvedObject.kind=Pod \
        --sort-by=.lastTimestamp --request-timeout=30s 2>&1 || true
    } >"${report_dir}/POD-STATE.txt"
  fi
}

# One bounded read of a node's cAdvisor stream, shared by every worker counter
# capture and deliberately subject-agnostic: it fetches and reports a status,
# and says nothing about which series the caller is after. The reasoning about
# what each capture reads, and why from the kubelet rather than from inside the
# container, belongs to the captures and is written above each of them.
_cozy_cadvisor_node_stream() {
  local node="$1"
  local stream="$2"
  local node_err="$3"
  local rc=0

  # One read per capture, so a node's stream is fetched once per subject rather
  # than once in total, and once per SAMPLE where a subject is read twice. That
  # is a real cost -- bounded by the read bound and the node cap at up to 100s
  # per invocation, a ceiling those numbers impose rather than a measured
  # duration -- and it is declined for a reason that outlives any particular way
  # of sharing the read.
  #
  # Sharing it means the captures of one node becoming one walk, and that is not
  # done here for scope rather than for merit: one walk behind the earliest of
  # their gates would be cheaper AND collect more. The gate is a deadline checked
  # with date, not a per-collector allowance, so a run yields all of them, a
  # prefix of them, or none -- and a merged walk admitted at the earliest gate
  # turns every prefix into all of them while leaving none unchanged. Anyone
  # weighing this should read that as an argument for merging, not against it.
  #
  # A cross-phase cache avoids the merge and was tried; it is not what stands
  # here, and the reason is not the cost above but that a cache publishes a
  # local write failure as a statement about the cluster -- the one thing this
  # collector may not do.
  #
  # The timeout-absent fallback is the sibling collectors': bounding with a
  # binary that is not there turns every read into an exit 127 and every note
  # into "the kubelet refused".
  if command -v timeout >/dev/null 2>&1; then
    timeout -k "${COZY_DIAG_READ_GRACE}" "${COZY_DIAG_READ_TIMEOUT}" \
      kubectl get --raw "/api/v1/nodes/${node}/proxy/metrics/cadvisor" \
      "--request-timeout=${COZY_DIAG_READ_TIMEOUT}s" \
      >"${stream}" 2>"${node_err}" || rc=$?
  else
    kubectl get --raw "/api/v1/nodes/${node}/proxy/metrics/cadvisor" \
      "--request-timeout=${COZY_DIAG_READ_TIMEOUT}s" \
      >"${stream}" 2>"${node_err}" || rc=$?
  fi
  printf '%s\n' "${rc}"
}

# _cozy_capture_worker_cadvisor <subdir> <label> <subject> <series> <tmp-prefix> <tail-hook> <metric-re>
#
# The body every worker counter capture shares. It was two near-identical copies
# differing in a report subdirectory, a regex, a subject phrase and a trailing
# note, and the copies cost more than duplication normally does: the block-level
# bound audit pins each collector by something only it produces, and two
# collectors issuing identical reads left neither with anything of its own, so
# stubbing one out of the audit went unnoticed. One body removes that. It does
# not remove the second fetch -- each capture still reads the node itself, at
# the price the budget comment states -- and why that saving is declined is
# given where the read is.
#
# <subject> is the clause every "unknown" note opens with, and <series> the word
# naming what the kubelet did or did not report. Splitting them is what keeps
# each note a sentence about this collector's question rather than a generic one
# -- a note that says only "no series" leaves the reader to guess which.
_cozy_capture_worker_cadvisor() {
  local subdir="$1"
  local label="$2"
  local subject="$3"
  local series="$4"
  local tmp_prefix="$5"
  local tail_hook="$6"
  local metric_re="$7"
  # The label reads "<noun> capture" at both call sites, and three sentences
  # want the two halves differently: "failed to list ... for <label>" wants the
  # whole thing, while "capturing worker <noun>" and "worker <noun> capture
  # stopped" want the noun alone. Splitting it here keeps all three grammatical
  # without an eighth parameter that would have to agree with the seventh.
  local subject_noun="${label% capture}"
  local report_dir="${COZY_REPORT_DIR:-/workspace/_out/cozyreport}/snapshots/${COZY_SNAPSHOT_NAME:-kubernetes}/${subdir}"
  local nodes node rc
  local seen=0
  local node_err raw stream matched filter_err filter_rc tenant_seen had_series read_at read_done
  # The counters are per container but the endpoint is per node, so the walk is
  # over nodes and not over Pods: one read covers every worker the node hosts.
  # Three is the sandbox's management node count, so this cap is the whole
  # cluster rather than a sample of it.
  #
  # A literal and not a knob, unlike the time bound, and the difference is what
  # lowering would mean. Lowering a time bound costs detail inside a read that
  # still happened; lowering this one drops nodes silently, and the capture then
  # answers for part of the cluster while reading like it answered for all of it.
  local max_nodes=3

  mkdir -p "${report_dir}"
  # Re-validated here rather than trusted, for the reason cozy_diag_read gives
  # for doing the same: a value assigned after this file is sourced -- which is
  # how both a caller and a test set it -- would otherwise reach `timeout`
  # unchecked, and zero there disables the bound outright.
  COZY_DIAG_READ_TIMEOUT=$(_cozy_diag_seconds "${COZY_DIAG_READ_TIMEOUT-}" "$COZY_DIAG_READ_TIMEOUT_DEFAULT" COZY_DIAG_READ_TIMEOUT positive)
  COZY_DIAG_READ_GRACE=$(_cozy_diag_seconds "${COZY_DIAG_READ_GRACE-}" "$COZY_DIAG_READ_GRACE_DEFAULT" COZY_DIAG_READ_GRACE)

  nodes=$(_cozy_virt_launcher_listing "${report_dir}" "${label}" \
    '{range .items[*]}{.spec.nodeName}{"\n"}{end}' \
    'no virt-launcher Pod with a node assigned in namespace tenant-test; an unscheduled Pod has no kubelet to ask') || return 1

  for node in ${nodes}; do
    seen=$((seen + 1))
    if [ "${seen}" -gt "${max_nodes}" ]; then
      echo "--- worker ${subject_noun} capture stopped at ${max_nodes} nodes ---"
      printf 'capture stopped after %s nodes; %s carried a worker in total\n' \
        "${max_nodes}" "$(printf '%s\n' "${nodes}" | wc -l | tr -d ' ')" \
        >"${report_dir}/COLLECTION-TRUNCATED.txt"
      break
    fi
    echo "--- capturing worker ${subject_noun}: ${node} ---"
    node_err="${report_dir}/${node}.read-error.log"
    raw="${report_dir}/${node}.txt"
    # Outside report_dir on purpose. `rm -f` below clears it on every path this
    # function controls, but a hard kill during the read does not run it, and a
    # node's full cAdvisor dump is megabytes; left inside the report it would be
    # uploaded as though it were a capture. A scratch directory is the runner's,
    # not the artifact tree's, so a kill there costs nothing anyone reads.
    #
    # The template is explicit rather than a bare `mktemp`, and that is not
    # style: BSD mktemp with no template ignores TMPDIR outright and always
    # lands in the system directory, while GNU honours it. Given a template both
    # put the file where the template says, so this is the only spelling whose
    # location is the same on a developer's machine and on the runner.
    stream=$(mktemp "${TMPDIR:-/tmp}/${tmp_prefix}.XXXXXX") \
      || stream="${report_dir}/${node}.stream.tmp"
    matched=$(mktemp "${TMPDIR:-/tmp}/${tmp_prefix}-m.XXXXXX") \
      || matched="${report_dir}/${node}.matched.tmp"
    filter_err="${report_dir}/${node}.filter-error.log"
    # Captured whole and filtered afterwards. Piped straight into grep the
    # status would be grep's, `timeout`'s 124 could never reach this variable,
    # and a kubelet that never answered would arrive looking like a node with no
    # tenant container on it.
    #
    # A missing RBAC grant on nodes/proxy arrives as a non-zero exit with the
    # 403 on stderr, so it lands in the same branch as an unreachable kubelet,
    # and the error log is what says which of them happened.
    # Defaulted because the status now arrives through a command substitution
    # rather than from a local set ahead of the read: a subshell killed outright
    # returns nothing, and an empty rc turns every `[ "${rc}" -ne 0 ]` below into
    # a shell error instead of a branch.
    #
    # Defaulted to 137 rather than 0, and the direction is the whole point. The
    # case this covers is a read that was killed, and 0 is the one value that
    # means "the kubelet answered" -- so a zero default would take the single
    # outcome this collector may never manufacture and manufacture it. 137 is
    # what a kill produces anyway, and the note it selects says the read did not
    # finish, which is what was observed.
    # Stamped on both sides of the read, and this is what makes a pair of
    # captures subtractable. One stamp would leave the sampling instant
    # somewhere inside a read whose duration is exactly what goes wrong on the
    # run this collector exists for: a bound-length read stamped at its start
    # and one stamped at its end differ by that bound, and the error lands in
    # the interval a rate divides by. Two stamps make the uncertainty visible
    # rather than absent, and on a healthy read they are the same second.
    #
    # The knob names how long this collector WAITS between its two passes,
    # which is not the interval between two readings of
    # the same node: each pass also walks its nodes and the pass of the other
    # subject runs in between, so the real gap is the wait plus whatever those
    # cost -- seconds on a healthy run and up to the read bound per read on the
    # run this collector exists for, which is exactly the run where a rate
    # computed from the advertised number would be wrong. Reading it off the
    # captures needs no assumption about either.
    read_at=$(date -u +%s)
    rc=$(_cozy_cadvisor_node_stream "${node}" "${stream}" "${node_err}")
    read_done=$(date -u +%s)
    rc=${rc:-137}
    # The filter's own failure is kept apart from "matched nothing". grep exits
    # 1 when nothing matched and 2 when it could not read -- a full TMPDIR on
    # the runner reaches the second -- and folded together they land in the arm
    # that says the kubelet answered and carried no series, which is the one
    # conflation this collector exists to prevent.
    #
    # All three stages are run apart rather than piped for the same reason the
    # read above is: a pipeline carries only its LAST command's status, so a
    # stage that could not read its input hands an empty stream to the next one,
    # which then exits 1 for having matched nothing.
    filter_rc=0
    grep -E "${metric_re}" "${stream}" >"${matched}" 2>"${filter_err}" || filter_rc=$?
    if [ "${filter_rc}" -lt 2 ]; then
      grep 'namespace="tenant-test"' "${matched}" >"${stream}" 2>>"${filter_err}" || filter_rc=$?
    fi
    if [ "${filter_rc}" -lt 2 ]; then
      grep 'pod="virt-launcher-' "${stream}" >"${raw}" 2>>"${filter_err}" || filter_rc=$?
    fi
    rm -f "${matched}"
    # Read before the sink is removed, because an empty capture has two causes
    # here and only stage 2's output separates them: a node carrying nothing of
    # this namespace, and a namespace whose containers are none of them workers.
    tenant_seen=0
    if [ -s "${stream}" ]; then
      tenant_seen=1
    fi
    rm -f "${stream}"
    [ -s "${filter_err}" ] || rm -f "${filter_err}"
    if [ ! -s "${node_err}" ]; then
      rm -f "${node_err}"
      node_err=
    elif [ "${rc}" -eq 0 ]; then
      mv "${node_err}" "${report_dir}/${node}.READ-WARNINGS.txt" 2>/dev/null || true
      node_err=
    fi
    # Tested before anything is appended, or nothing is ever empty. An empty
    # capture here is the dangerous outcome rather than a neutral one: zero
    # bytes reads as a healthy worker, which is the very conclusion this
    # collector was added to stop a reader reaching by default.
    #
    # Which arm fires is decided by the STATUS, never by whether anything landed
    # on stderr. `timeout` kills its child without a word and kubectl dies on
    # SIGTERM the same way, so the dominant failure here -- a wedged kubelet --
    # arrives non-zero with an empty error log; keyed on stderr it would fall
    # through to the arm that says the kubelet answered, and the artifact would
    # state the opposite of what happened.
    had_series=0
    if [ -s "${raw}" ]; then
      had_series=1
    fi
    if [ ! -s "${raw}" ] && [ "${rc}" -ne 0 ]; then
      if [ -n "${node_err}" ]; then
        printf '%s\n' \
          "${subject} is unknown: the kubelet was not read; see read-error.log" \
          >>"${raw}"
      else
        printf '%s\n' \
          "${subject} is unknown: the kubelet was not read, and the read died without a word on either stream" \
          >>"${raw}"
      fi
    elif [ ! -s "${raw}" ] && [ "${filter_rc}" -ge 2 ]; then
      # Between the two kubelet rungs on purpose. The read may well have
      # succeeded -- this says nothing about it either way, because the failure
      # is local to this runner: grep could not read back the stream it was
      # given. Folded into the rung below, it would assert that the kubelet
      # answered and carried no series.
      if [ -s "${filter_err}" ]; then
        printf '%s\n' \
          "${subject} is unknown: the metric stream could not be read back on this runner; see filter-error.log" \
          >>"${raw}"
      else
        printf '%s\n' \
          "${subject} is unknown: the metric stream could not be read back on this runner, and the filter said nothing about why" \
          >>"${raw}"
      fi
    elif [ ! -s "${raw}" ] && [ "${tenant_seen}" -eq 1 ]; then
      # The namespace was there and the worker Pods were not, which is what an
      # upstream rename of the virt-launcher prefix looks like from inside. The
      # rung below is the scheduling reading instead, and the two are fixed by
      # looking in different places.
      printf '%s\n' \
        "${subject} is unknown: the kubelet answered and reported ${series} series for this namespace, none of them from a worker Pod named virt-launcher-*" \
        >>"${raw}"
    elif [ ! -s "${raw}" ]; then
      printf '%s\n' \
        "${subject} is unknown: the kubelet answered and reported no ${series} series for a tenant-test worker on this node" \
        >>"${raw}"
    elif [ "${rc}" -ne 0 ]; then
      if [ -n "${node_err}" ]; then
        printf '%s\n' \
          'these counters are incomplete: the read was cut short part way through the metric stream; see read-error.log' \
          >>"${raw}"
      else
        printf '%s\n' \
          'these counters are incomplete: the read was cut short part way through the metric stream, without a word on either stream' \
          >>"${raw}"
      fi
    elif [ "${filter_rc}" -ge 2 ]; then
      # The same truncation one layer down: series arrived, the read was fine,
      # and a filter stage stopped part way. A write error is where that happens
      # -- grep emits what it had flushed and exits 2, which is how a full disk
      # arrives. Reached only with something already in the file.
      if [ -s "${filter_err}" ]; then
        printf '%s\n' \
          'these counters are incomplete: the metric stream could not be read back past this point on this runner; see filter-error.log' \
          >>"${raw}"
      else
        printf '%s\n' \
          'these counters are incomplete: the metric stream could not be read back past this point on this runner, and the filter said nothing about why' \
          >>"${raw}"
      fi
    fi
    # kubectl exits 1 for a refused connection as readily as for a 403 or a
    # NotFound node, so the status alone names none of them and the error log is
    # the only thing that can.
    if [ "${rc}" -eq 124 ]; then
      printf '%s\n' \
        '[exit 124: this collector timing out and the read exiting 124 on its own cannot be told apart]' \
        >>"${raw}"
    fi
    # 128+SIGKILL. The grace signal produces it, and so does anything else that
    # kills the read -- an OOM killer on a loaded runner, a teardown signalling
    # the process group -- so the note says what the status establishes and
    # stops there. cozy_diag_read refuses the same wording for the same reason.
    if [ "${rc}" -eq 137 ]; then
      printf '%s\n' \
        '[exit 137: the read was killed rather than stopping on its own; the status does not say what killed it]' \
        >>"${raw}"
    fi
    # `|| true` because the hook is called bare inside the walk: a hook whose
    # last statement is a false test returns non-zero, and under a live errexit
    # that ends the walk part way through a node rather than at a boundary. The
    # hooks today end on an `if` that returns 0 when its condition is false, so
    # this changes nothing now and removes the requirement that they always will.
    "${tail_hook}" "${raw}" "${rc}" "${filter_rc}" "${had_series}" || true
    # Sample-agnostic on purpose. This body is shared, and only one of its
    # callers takes a second sample: telling the reader of a caller that does not
    # to subtract a sibling would name a file that does not exist and contradict
    # that capture's own tail note in the line above it. What is true for every
    # caller is when the read happened, so that is what this says, and the
    # instruction to pair it belongs to the capture that has a pair.
    # The bracket only, with no claim about what was sampled inside it. When the
    # read was attempted is true on every arm, including the ones that just said
    # nothing was read; that counters exist between the two instants is not, and
    # the arms above are what decide that. The note that does make the claim
    # sits behind the same clean-read gate as the pairing note.
    printf '[read attempted from %s to %s epoch seconds]\n' \
      "${read_at}" "${read_done}" >>"${raw}"
    printf '\n[capture exit code: %s]\n' "${rc}" >>"${raw}"
  done
}

# The virt-launcher listing, shared by the captures as code and issued fresh by
# each of them: they ask the same question of the same apiserver, and the answer
# is deliberately not carried between them, for the reason given at the read.
# Echoes the projected field and returns non-zero when there is no walk to make,
# with the reason written where the reader of that capture will look for it.
#
# The whole query is a parameter, not just the field inside it, because what a
# caller walks is not always what it reads: a capture that talks to a kubelet
# needs the node a worker landed on, one that talks to the container needs the
# Pod, and one that has to reach into a container needs to skip the Pods there
# is nothing to reach into. The sentence for an answer that named nothing
# travels with it, since an empty listing means something different under each
# query and a shared wording would be wrong for one of them.
_cozy_virt_launcher_listing() {
  local report_dir="$1"
  local label="$2"
  local jsonpath="$3"
  local empty_msg="$4"
  local err_log="${report_dir}/COLLECTION-FAILED.txt"
  local warn_log="${report_dir}/READ-WARNINGS.txt"
  local raw_values values
  local list_rc=0

  # stderr is kept out of the captured stdout so a warning on an otherwise
  # healthy call is never read back as a node name, and which file it lands in
  # is decided by the exit status rather than by something having been written:
  # kubectl writes deprecation and partial-result warnings with a zero exit.
  # Read first, filter after. A pipeline reports its LAST command's status, so
  # folding the sort into this assignment would hand list_rc to `sort` and make
  # a listing that never answered indistinguishable from a namespace with no
  # workers in it -- the one conflation these collectors must not make.
  if command -v timeout >/dev/null 2>&1; then
    raw_values=$(timeout -k "${COZY_DIAG_READ_GRACE}" "${COZY_DIAG_READ_TIMEOUT}" \
      kubectl -n tenant-test get pods -l kubevirt.io=virt-launcher \
      -o "jsonpath=${jsonpath}" \
      "--request-timeout=${COZY_DIAG_READ_TIMEOUT}s" 2>"${warn_log}") || list_rc=$?
  else
    raw_values=$(kubectl -n tenant-test get pods -l kubevirt.io=virt-launcher \
      -o "jsonpath=${jsonpath}" \
      "--request-timeout=${COZY_DIAG_READ_TIMEOUT}s" 2>"${warn_log}") || list_rc=$?
  fi
  if [ "${list_rc}" -ne 0 ]; then
    echo "failed to list tenant virt-launcher Pods for ${label}" >&2
    mv "${warn_log}" "${err_log}" 2>/dev/null || true
    printf '%s\n' \
      "failed to list tenant virt-launcher Pods for ${label} (exit ${list_rc})" \
      >>"${err_log}"
    return 1
  fi
  [ -s "${warn_log}" ] || rm -f "${warn_log}"
  values=$(printf '%s\n' "${raw_values}" | sort -u | grep -v '^$' || true)
  if [ -z "${values}" ]; then
    echo "${empty_msg} (${label})" >&2
    printf '%s\n' "${empty_msg}" >"${err_log}"
    return 1
  fi
  printf '%s\n' "${values}"
}

# The one answer here that is not a failure, and until it is written down the
# only one a reader has to derive. A capture carrying no quota, at a clean exit,
# is a container with no CPU limit -- but it is also the shape a truncated read
# leaves, and every other outcome gets a sentence precisely so
# that this one is not read as that one. Keyed on the quota's absence rather
# than on a line count, because that absence is the condition cAdvisor itself
# gates the whole capped set on. Tested for exactly 1, which is grep's "read it,
# no match": a bare `!` would also accept 2, and the artifact would then claim a
# ceiling that a failed local read had merely not found.
#
# Decided over the whole node file rather than per container, and the file holds
# every container of every worker Pod on that node. One quota line anywhere in
# it suppresses the note. That is deliberately the conservative direction: the
# note is only ever printed when nothing in the file is capped, so it cannot
# claim "uncapped" over a file that shows a ceiling.
_cozy_cpu_throttle_tail_note() {
  local raw="$1" rc="$2" filter_rc="$3" had_series="$4"
  local quota_rc=0

  grep -q '^container_spec_cpu_quota{' "${raw}" || quota_rc=$?
  if [ "${had_series}" -eq 1 ] && [ "${rc}" -eq 0 ] && [ "${filter_rc}" -lt 2 ] \
    && [ "${quota_rc}" -eq 1 ]; then
    printf '%s\n' \
      'these workers are running uncapped: cAdvisor publishes the quota and the CFS counters only for a container whose quota is non-zero, so a period without them beside it is a container with no CPU limit rather than a short read' \
      >>"${raw}"
  fi
  # Said here rather than in the shared walk, because this is the capture that
  # has a sibling to subtract from. Every family above is cumulative from the
  # container starting, so one file is an average over an uptime; the stamp on
  # the line below and the same node's file under the other sample directory are
  # what turn the pair into a rate.
  # Withheld from a read that did not finish, the way the sandbox capture
  # withholds its column legend from one. An instruction to subtract two files
  # is a statement that both hold a whole reading, and a capture already marked
  # incomplete does not; the two captures in this pair answer that question the
  # same way rather than each on its own terms.
  if [ "${had_series}" -eq 1 ] && [ "${rc}" -eq 0 ] && [ "${filter_rc}" -lt 2 ]; then
    printf '%s\n' \
      'these counters are cumulative since the container started: subtract this node file from its sibling under the other sample directory, and the stamps below from each other, to get a rate. The counters were sampled somewhere inside each read, so that interval is exact to the width of the two brackets' \
      >>"${raw}"
  fi
}

# Said in the file because the number invites the one reading it cannot support.
# Every family here is cumulative from the Pod's sandbox starting, so a drop
# count says nothing about the window the node-join deadline covered: a link
# that dropped nothing while the pull stalled and one that dropped throughout
# carry the same total whenever the earlier traffic matched. A rate needs a
# second capture of the same stream to subtract from, and the sample time each
# row carries as its third field is what makes two captures pairable.
_cozy_network_counters_tail_note() {
  local raw="$1" had_series="$4"

  if [ "${had_series}" -eq 1 ]; then
    printf '%s\n' \
      'these counters are cumulative since the Pod sandbox started, not a rate over the failure: a second capture of the same stream is what turns them into one' \
      >>"${raw}"
  fi
}

# Read each tenant worker's CPU throttling counters and its CFS ceiling from
# the kubelet's cAdvisor endpoint on the node the worker runs on.
#
# This exists because two different failures produce identical evidence from
# outside the Pod. A worker that misses the node-Ready deadline while the
# sandbox node it runs on sits at half its capacity, with a clean node dmesg,
# is either being held at its own CFS ceiling or losing host CPU it was
# entitled to. The ceiling itself is already in the artifact -- (a) dumps
# `describe pods -l kubevirt.io=virt-launcher`, which prints each container's
# Limits, and an absent Limits line is the uncapped case. What no other read
# here answers is whether the container ever REACHED that ceiling and for how
# long: node-level utilisation shows what the node used, and a limit shows what
# the container was allowed, but neither says whether the two ever met. The
# throttled counters are the only place that is recorded, and nothing else in
# this tree collects them for the tenant workers.
#
# The quota and period are captured beside the counters anyway, in cgroup units
# rather than Kubernetes ones, because a throttled-period count is unreadable
# without the ceiling it was measured against, and a reader should not have to
# carry a figure back from an earlier section to divide by it.
#
# Six families carry the answer, and cAdvisor publishes them ready to read, so
# no arithmetic is done here and none is needed:
#
#   container_cpu_usage_seconds_total          CPU time the group actually got
#   container_cpu_cfs_periods_total            periods the group was scheduled
#   container_cpu_cfs_throttled_periods_total  of those, periods it was stopped
#   container_cpu_cfs_throttled_seconds_total  how long it was stopped for
#   container_spec_cpu_period                  the ceiling's period
#   container_spec_cpu_quota                   the ceiling itself
#
# The throttled counters alone say a container hit some ceiling, not which one,
# and a VM capped at one core and a VM capped at eight are the same number
# without the quota beside them. Nor do they say what the container got: being
# stopped at a ceiling and never being scheduled onto a physical CPU are
# different failures that both leave throttled periods behind, and the usage
# counter is the only one of the six that separates them. It costs a wider
# filter rather than another read, since cAdvisor puts it on the stream this
# capture already fetches.
#
# Four of the six are gated, and on the same condition. cAdvisor emits the
# quota series and all three CFS counters only for a container whose quota is
# non-zero; container_spec_cpu_period and container_cpu_usage_seconds_total are
# the two it emits for any container with a CPU spec at all. So a container
# that reports anything puts two shapes on the wire and no third: all six when
# it is capped, the period and the usage when it is not. (A container cAdvisor
# knows nothing about contributes none of them, and reaches this capture as the
# same silence as a node with no worker on it.) The second is a reading rather
# than a gap -- it is the question this collector was added to answer, and it
# arrives without being computed.
#
# Worth stating because a short capture is also what a truncated read looks
# like, and this function spends its whole length making that difference legible.
# A reader who expected six families and found two would reach for the wrong
# conclusion with the artifact agreeing.
#
# Read from the kubelet rather than from inside the container on purpose. A
# reader that runs inside the container shares the cgroup it is measuring, so
# it slows down exactly when the answer matters and needs a budget sized for
# the pathological case; the kubelet is outside that cgroup and is not subject
# to it. It also removes any dependency on what the container image happens to
# ship and on how the host lays cgroups out, since cAdvisor reports the same
# series under either cgroup version.
#
# The in-container reading does exist in this tree -- hack/e2e-capture-dataplane.sh
# reads cpu.stat straight out of the ovs-ovn container -- and the size difference
# between the two is the two paragraphs above, not the reading itself. That one
# has a container it may assume (its own image, one namespace, one workload) and
# a caller that already knows the Pod. This one is handed a node and has to find
# the workers, tell a kubelet that never answered from a node carrying none, and
# survive a filter that fails half way, because the whole point is to be believed
# when it reports nothing.
#
# Takes the sample number it is writing, because it is called twice: the
# counters are cumulative since the container started, so one reading gives an
# average over the whole uptime and no reading of the failure window. Two
# readings a fixed interval apart give a rate, and a rate is what the burst
# profile needs -- a guest that freezes for tens of seconds and then runs at its
# ceiling produces a low average and a high instantaneous figure, and only the
# second of those distinguishes it from a guest that is simply capped.
cozy_capture_tenant_worker_cpu_throttle() {
  local sample="$1"
  # Anchored on the metric names so a series whose name merely contains one of
  # them cannot pass. The namespace alone is NOT a worker filter and must not be
  # used as one: tenant-test also carries the Kamaji control plane, whose
  # apiserver and etcd have CPU limits of their own and can be genuinely
  # throttled, plus the CDI importer Pods. Under a heading that says "worker", a
  # throttled apiserver answers the question with the wrong subject, so the Pod
  # name carries the second half of the filter.
  #
  # Note the asymmetry, since it is the one soft spot here: the listing selects
  # Pods by the authoritative label kubevirt.io=virt-launcher, while this matches
  # a name prefix, because cAdvisor publishes no such label and the name is all
  # the series carries. KubeVirt derives that prefix from the Pod it creates, so
  # it holds today; a rename upstream would silently empty this capture rather
  # than break it loudly.
  _cozy_capture_worker_cadvisor \
    "tenant-cpu-throttle/sample-${sample}" \
    'CPU throttling counters capture' \
    'what these workers got and whether they were throttled' \
    CPU \
    cozy-cpu-throttle \
    _cozy_cpu_throttle_tail_note \
    '^container_(cpu_(usage_seconds_total|cfs_(periods_total|throttled_periods_total|throttled_seconds_total))|spec_cpu_(period|quota))\{'
}

# Read each sandbox node's own CPU accounting, straight from its kernel.
#
# The captures above are about the tenant worker's cgroup: what it was allowed
# and what it got. Neither can see the layer under the sandbox. A worker whose
# vCPU thread is runnable and simply not scheduled looks, from the cgroup, like
# a worker that asked for nothing -- and the two candidate explanations for that
# are a sandbox node oversubscribed from inside and a sandbox node not given its
# own turn by the machine running the runner. Only the second leaves a trace,
# and it leaves it here: `steal` counts the time this kernel was runnable while
# the hypervisor beneath it ran somebody else.
#
# There is no kubelet or cAdvisor route to it. The kubelet's cAdvisor and
# summary endpoints publish container and node utilisation and neither carries a
# steal column, which cost one measurement window already; the node's own
# /proc/stat is the only surface with the number. crust-gather's node-shell Pod
# is refused by PodSecurity in this cluster, so the Talos API is what remains,
# and it is already how the report reaches these nodes for dmesg.
#
# Captured verbatim rather than reduced to a percentage, and the column legend
# is written beside it rather than folded into arithmetic here. In /proc/stat's
# cpu rows the eighth number after the label is steal and the ninth is guest,
# guest is enormous on a node running VMs and is already counted inside user,
# and reading the ninth as the eighth turns a fraction of a percent into
# twenty-odd. The legend states which is which where the numbers are, so the
# next reader does not repeat that.
cozy_capture_sandbox_node_cpu_time() {
  local sample="$1"
  local report_dir="${COZY_REPORT_DIR:-/workspace/_out/cozyreport}/snapshots/${COZY_SNAPSHOT_NAME:-kubernetes}/sandbox-host-cpu-time/sample-${sample}"
  local err_log="${report_dir}/COLLECTION-FAILED.txt"
  local warn_log="${report_dir}/READ-WARNINGS.txt"
  # The sandbox container carries TALOSCONFIG in its environment, so this is the
  # path talosctl would have used on its own; naming it is what lets an absent
  # config be reported as an absent config rather than as a node that refused.
  local talosconfig="${TALOSCONFIG:-talosconfig}"
  local rows node addr rc node_err raw read_at read_done
  local list_rc=0
  local seen=0
  # Three is the sandbox's node count, so this cap is the whole cluster rather
  # than a sample of it -- the same literal and the same reasoning as the walk
  # over worker-carrying nodes above.
  local max_nodes=3

  mkdir -p "${report_dir}"
  # Re-validated here for the reason every collector re-validates them: a value
  # assigned after this file is sourced never passed the assignment-time check,
  # and zero reaches `timeout` as no bound at all.
  COZY_DIAG_READ_TIMEOUT=$(_cozy_diag_seconds "${COZY_DIAG_READ_TIMEOUT-}" "$COZY_DIAG_READ_TIMEOUT_DEFAULT" COZY_DIAG_READ_TIMEOUT positive)
  COZY_DIAG_READ_GRACE=$(_cozy_diag_seconds "${COZY_DIAG_READ_GRACE-}" "$COZY_DIAG_READ_GRACE_DEFAULT" COZY_DIAG_READ_GRACE)

  # Both preconditions are reported rather than returned into silence. An empty
  # directory here would read as a sandbox that lost no time to its hypervisor,
  # which is a statement about the cluster this collector never made.
  if ! command -v talosctl >/dev/null 2>&1; then
    echo "talosctl is not on PATH, so the sandbox nodes' CPU time was not read" >&2
    printf '%s\n' \
      'talosctl is not on PATH on the machine running this suite; the sandbox nodes were never asked, which is not a reading that they lost no time' \
      >"${err_log}"
    return 1
  fi
  if [ ! -f "${talosconfig}" ]; then
    echo "no sandbox talosconfig at ${talosconfig}, so the sandbox nodes' CPU time was not read" >&2
    printf '%s\n' \
      "no sandbox talosconfig at ${talosconfig}; the sandbox nodes were never asked, which is not a reading that they lost no time" \
      >"${err_log}"
    return 1
  fi

  # Name and address in one row: the address is what the Talos API is reached
  # on, and the name is what pairs this file with the worker captures above.
  # stderr is kept out of the captured stdout so a warning is never read back as
  # an address, and which file it lands in is decided by the exit status, since
  # kubectl writes warnings with a zero exit.
  if command -v timeout >/dev/null 2>&1; then
    rows=$(timeout -k "${COZY_DIAG_READ_GRACE}" "${COZY_DIAG_READ_TIMEOUT}" \
      kubectl get nodes \
      -o jsonpath='{range .items[*]}{.metadata.name}{"|"}{.status.addresses[?(@.type=="InternalIP")].address}{"\n"}{end}' \
      "--request-timeout=${COZY_DIAG_READ_TIMEOUT}s" 2>"${warn_log}") || list_rc=$?
  else
    rows=$(kubectl get nodes \
      -o jsonpath='{range .items[*]}{.metadata.name}{"|"}{.status.addresses[?(@.type=="InternalIP")].address}{"\n"}{end}' \
      "--request-timeout=${COZY_DIAG_READ_TIMEOUT}s" 2>"${warn_log}") || list_rc=$?
  fi
  if [ "${list_rc}" -ne 0 ]; then
    echo "failed to list sandbox nodes for the CPU time capture" >&2
    mv "${warn_log}" "${err_log}" 2>/dev/null || true
    printf '%s\n' \
      "failed to list sandbox nodes for the CPU time capture (exit ${list_rc})" \
      >>"${err_log}"
    return 1
  fi
  [ -s "${warn_log}" ] || rm -f "${warn_log}"
  if [ -z "${rows}" ]; then
    echo "the sandbox node listing answered and named no node" >&2
    printf '%s\n' \
      'the node listing answered and named no node at all, so nothing was asked; a cluster with nodes cannot produce this' \
      >"${err_log}"
    return 1
  fi

  # Split on the separator rather than on whitespace. A node with more than one
  # InternalIP -- a dual-stack cluster -- puts both addresses in the same field,
  # space separated, and a whitespace walk would turn the second one into a row
  # of its own: a bogus per-node file, counted against the cap, displacing a real
  # node. The first address is the one used, and it is taken explicitly.
  while IFS='|' read -r node addr; do
    [ -n "${node}" ] || continue
    addr="${addr%% *}"
    seen=$((seen + 1))
    if [ "${seen}" -gt "${max_nodes}" ]; then
      echo "--- sandbox node CPU time capture stopped at ${max_nodes} nodes ---"
      # Counted over lines, with the expansion quoted, for the same reason the
      # walk above strips all but the first address: one row is one node, and a
      # dual-stack node carries two addresses in it. Unquoted this counts
      # addresses, so the marker that exists to say "truncated, not small"
      # would overstate the pool it truncated.
      printf 'capture stopped after %s nodes; %s were listed in total\n' \
        "${max_nodes}" "$(printf '%s\n' "${rows}" | grep -c . || true)" \
        >"${report_dir}/COLLECTION-TRUNCATED.txt"
      break
    fi
    raw="${report_dir}/${node}.txt"
    # A node with no InternalIP is a finding rather than a node to skip: the
    # Talos API is reached on that address, so its absence is why this node was
    # not read, and an absent file would say nothing at all.
    if [ -z "${addr}" ]; then
      printf '%s\n' \
        'this node reports no InternalIP, and the Talos API is reached on that address, so it was not asked' \
        >"${raw}"
      continue
    fi
    echo "--- capturing sandbox node CPU time: ${node} (${addr}) ---"
    node_err="${report_dir}/${node}.read-error.log"
    rc=0
    # Stamped before the read for the reason the sibling capture states at its
    # own stamp, and here it is the only timing there is: a cAdvisor row carries
    # its own sample time and a /proc/stat row carries none, so without this the
    # gap between two readings of this node is unrecoverable from the artifact.
    read_at=$(date -u +%s)
    # The endpoint and the node are both the address: the e2e talosconfig
    # carries no endpoints, which is why the report's own Talos reads pass -e
    # and -n together, and one without the other reaches nothing.
    if command -v timeout >/dev/null 2>&1; then
      # stdin closed explicitly: this walk is driven by a heredoc, so anything
      # here that read stdin would eat the remaining rows and the walk would
      # stop after one node with no truncation marker -- answering for part of
      # the cluster while reading like it answered for all of it, which is the
      # outcome the cap above exists to make impossible. talosctl does not read
      # stdin today; this costs nothing and does not depend on that staying true.
      timeout -k "${COZY_DIAG_READ_GRACE}" "${COZY_DIAG_READ_TIMEOUT}" \
        talosctl --talosconfig "${talosconfig}" -e "${addr}" -n "${addr}" \
        read /proc/stat >"${raw}" 2>"${node_err}" </dev/null || rc=$?
    else
      talosctl --talosconfig "${talosconfig}" -e "${addr}" -n "${addr}" \
        read /proc/stat >"${raw}" 2>"${node_err}" </dev/null || rc=$?
    fi
    read_done=$(date -u +%s)
    if [ ! -s "${node_err}" ]; then
      rm -f "${node_err}"
      node_err=
    elif [ "${rc}" -eq 0 ]; then
      mv "${node_err}" "${report_dir}/${node}.READ-WARNINGS.txt" 2>/dev/null || true
      node_err=
    fi
    # Tested before anything is appended, or nothing is ever empty. Which arm
    # fires is decided by the status and not by whether stderr holds anything:
    # `timeout` kills its child without a word, so the dominant failure arrives
    # non-zero with an empty error log, and keyed on stderr it would land in the
    # arm that says the node answered.
    if [ ! -s "${raw}" ] && [ "${rc}" -ne 0 ]; then
      if [ -n "${node_err}" ]; then
        printf '%s\n' \
          'this node CPU time is unknown: the Talos API was not read; see the read-error.log beside this file, named for the same node' \
          >>"${raw}"
      else
        printf '%s\n' \
          'this node CPU time is unknown: the Talos API was not read, and the read died without a word on either stream' \
          >>"${raw}"
      fi
    elif [ ! -s "${raw}" ]; then
      printf '%s\n' \
        'this node CPU time is unknown: the read succeeded and returned nothing, which /proc/stat on a running kernel cannot produce' \
        >>"${raw}"
    elif [ "${rc}" -ne 0 ]; then
      printf '%s\n' \
        'these counters are incomplete: the read was cut short part way through /proc/stat' \
        >>"${raw}"
    else
      printf '%s\n' \
        '[the cpu rows above are, after the label: user nice system idle iowait irq softirq steal guest guest_nice, in USER_HZ. The eighth number is steal and the ninth is guest; guest is large on a node running VMs and is already counted inside user, so reading the ninth as the eighth turns a fraction of a percent of steal into twenty-odd. A steal of zero here means this node was given every turn it asked for, not that the column is unavailable: the sandbox VMs are started with accel=kvm and -cpu host by hack/e2e-prepare-cluster.bats, where KVM exposes steal-time accounting to the guest]' \
        >>"${raw}"
      # Only here, beside the legend, and for the reason the legend is only
      # here. Telling a reader to subtract two files asserts that both hold a
      # whole reading. On the arm above it the file holds a prefix, and the
      # difference understates the counter by whatever the read did not return;
      # on the arms above that it holds no counters at all, and the difference
      # is the sibling's entire cumulative value read as a delta -- a steal
      # figure inflated to the node's whole uptime, which is the finding this
      # collector exists to look for. The cpu-throttle side withholds its own
      # pairing note on the same condition.
      printf '%s\n' \
        'subtracting this node file from its sibling under the other sample directory, and the stamps below from each other, gives a rate. The counters were sampled somewhere inside each read, so that interval is exact to the width of the two brackets' \
        >>"${raw}"
    fi
    printf '[read attempted from %s to %s epoch seconds]\n' \
      "${read_at}" "${read_done}" >>"${raw}"
    printf '\n[capture exit code: %s]\n' "${rc}" >>"${raw}"
  done <<EOF
${rows}
EOF
}

# The walk over a mounted debugfs that turns the kernel's KVM counters into
# lines, taking the mount point as its first argument.
#
# A function emitting a program rather than a literal at the call site, for the
# reason the thread walk beside it is written the same way: staging a subject
# worth reading needs a hypervisor, and the outcomes this has to get right --
# counters present, kvm directory absent, directory present and empty -- are
# reachable against a tree of files and against nothing else.
#
# Nothing is computed here. Each counter goes out under the level it was read
# at, because debugfs publishes the same names twice: once summed over every VM
# the kernel hosts and once per VM. Folded together they answer neither
# question.
_cozy_kvm_stats_probe() {
  cat <<'PROBE'
kvm_root="${1}/kvm"
globals=0
vm_lines=0
vms=0
skipped=0
if [ ! -d "${kvm_root}" ]; then
  printf '%s\n' "NO-KVM-DIR: debugfs is mounted and holds no kvm directory, so this kernel has no KVM in use. The sandbox VMs are started with accel=kvm by hack/e2e-prepare-cluster.bats, so this is a statement about how they are being run rather than a capture that came up short"
  exit 4
fi
printf '%s\n' '[counters follow, one per line, as: level name value; a file under these directories that is not a counter is named on a skipped line instead]'
# A counter is one integer and nothing else, and that shape is what decides
# whether a file here ends up in the numbers. It cannot decide whether the file
# gets read, which is a separate job done by name above, because for a seq_file
# the reading is the cost.
#
# `read` returning non-zero is not what decides whether a line arrived: it does
# that on a last line with no newline while still setting the variable. The
# kernel's own stat files do carry the newline, since it formats them through
# "%llu\n", but this walks whatever the directory holds rather than a list of
# names it was given.
# The files that must not be OPENED, as opposed to the ones that must not be
# emitted. Those are two different jobs and only one of them a shape test can
# do: mmu_rmaps_stat is registered with single_open() and seq_read, so the walk
# over every rmap head under the VM slots_lock and MMU write lock runs on the
# first read() rather than at open, and a reader that opens the file to find out
# what shape it is has already paid for it. Hence a name, checked before
# anything is opened, even though a name is a property of the kernel version
# where a shape is a property of the class. The shape test below stays as the
# general net: it keeps an unknown non-counter out of the artifact, which is all
# it can do, and this list keeps the one known expensive read from happening.
is_known_non_counter() {
  case "$1" in
    mmu_rmaps_stat) return 0 ;;
  esac
  return 1
}
read_counter() {
  counter_value=
  # Status 2 keeps "could not be read" apart from "read, wrong shape", and one
  # command carries both ways the first can happen: the open is refused in the
  # instant a VM's debugfs entry is being removed, and a file that did open
  # keeps refusing the read for the whole teardown once its entry is detached.
  # A file that fails either way may well be a counter whose VM is going away,
  # and saying "not a counter" about it would send the reader away from exactly
  # the per-VM files the legend tells them to consult when a guest disappears.
  _content=$(cat "$1" 2>/dev/null) || return 2
  case "${_content}" in
    '' | *[!0-9]*) return 1 ;;
  esac
  counter_value=${_content}
}
for stat_file in "${kvm_root}"/*; do
  [ -f "${stat_file}" ] || continue
  stat_name=${stat_file##*/}
  if is_known_non_counter "${stat_name}"; then
    skipped=$((skipped + 1))
    printf '[skipped global] %s: not opened, it is a known non-counter and reading it costs the guest\n' "${stat_name}"
  elif read_counter "${stat_file}"; then
    globals=$((globals + 1))
    printf '[global] %s %s\n' "${stat_name}" "${counter_value}"
  else
    case "$?" in
      2) skip_why='could not be read, so nothing about its shape is known' ;;
      *) skip_why='not a single integer, so not a counter' ;;
    esac
    skipped=$((skipped + 1))
    printf '[skipped global] %s: %s\n' "${stat_name}" "${skip_why}"
  fi
done 2>/dev/null
# On the loop as well as inside read_counter's own silencing: a counter that
# disappears between the glob and the read is routine -- a VM directory goes
# when its VM does -- and this probe's stderr is the collector's warning
# artifact, which must not name something that did not affect the reading.
for vm_dir in "${kvm_root}"/*/; do
  [ -d "${vm_dir}" ] || continue
  vm_name=${vm_dir%/}
  vm_name=${vm_name##*/}
  vms=$((vms + 1))
  for stat_file in "${vm_dir}"*; do
    [ -f "${stat_file}" ] || continue
    stat_name=${stat_file##*/}
    if is_known_non_counter "${stat_name}"; then
      skipped=$((skipped + 1))
      printf '[skipped vm %s] %s: not opened, it is a known non-counter and reading it costs the guest\n' "${vm_name}" "${stat_name}"
    elif read_counter "${stat_file}"; then
      vm_lines=$((vm_lines + 1))
      printf '[vm %s] %s %s\n' "${vm_name}" "${stat_name}" "${counter_value}"
    else
      case "$?" in
        2) skip_why='could not be read, so nothing about its shape is known' ;;
        *) skip_why='not a single integer, so not a counter' ;;
      esac
      skipped=$((skipped + 1))
      printf '[skipped vm %s] %s: %s\n' "${vm_name}" "${stat_name}" "${skip_why}"
    fi
  done
done 2>/dev/null
printf '[counters read: %s kernel-wide and %s per-VM, across %s per-VM director(ies); %s file(s) skipped, each with its reason beside it above]\n' \
  "${globals}" "${vm_lines}" "${vms}" "${skipped}"
# On counters rather than on directories. A VM that goes away under the walk
# leaves a directory whose files cannot be read, which is indistinguishable on
# disk from a VM that reported nothing -- and counted as a directory it would
# satisfy this test while the capture holds no number at all, then carry the
# legend telling a reader to difference it against its sibling.
if [ "${globals}" -eq 0 ] && [ "${vm_lines}" -eq 0 ]; then
  # A kvm directory that could be opened and held nothing readable. Without
  # this the probe exits clean carrying only a heading, a heading is enough to
  # make the capture non-empty, and the collector would pin a legend about
  # taking differences onto a file with nothing to difference.
  printf '%s\n' 'NO-COUNTER-FILES: the kvm directory was opened and not one counter under it could be read, so this says nothing about what the kernel paid'
  exit 5
fi
PROBE
}

# The KVM exit counters of the kernel the sandbox VMs run on.
#
# This exists because every other CPU reading in this tree measures a subject
# inside the sandbox and none of them can price a tick. The cgroup captures say
# what a tenant worker was allowed and what it consumed; the per-thread split
# says which QEMU thread consumed it; the node's own /proc/stat says whether the
# sandbox node was given its turn. All of them can be healthy at once -- quota
# untouched, steal at zero, the vCPU thread at its ceiling -- while the guest
# does six to ten times less work per core than the same image does on a run
# that passes. Ticks handed out and work not done is not a quantity any counter
# inside the sandbox holds.
#
# It is held one layer down. A tenant worker is a guest of a sandbox node, which
# is itself a guest of the runner, and the arrangement's cost is paid by the
# runner's kernel: every exit a sandbox node takes, every nested page fault, and
# every TLB flush that servicing its own guest provokes. That kernel publishes
# the totals in debugfs, and the suite runs in a container sharing it, so this
# is a local read rather than a walk over nodes -- which is also why it can be
# afforded at all. The reading it does NOT give is the other half: a tenant
# worker's exits into its sandbox node are counted by that node's kernel, and
# reaching those costs a bounded read per node per sample, which the phase
# budget in hack/run-kubernetes-node-join_test.bats does not have.
#
# Mounted in a mount namespace of this call's own rather than in the sandbox's.
# The suite keeps running for the length of a node-join wait after this returns,
# and a mount left behind would be a diagnostic editing the environment the rest
# of the run measures. The namespace also removes the unmount: there is nothing
# to clean up after a read the wrapper killed, which is the arm where a
# collector that unmounts itself would leak.
#
# Takes the sample number it is writing, because it is called twice and the two
# calls sit on either side of the node-join wait rather than inside one block.
# A counter here accumulates over the life of the VM it belongs to, so a single
# reading is an average over that life rather than a rate over anything; the
# pair spans the window the guest was failing to boot in, and the stamps in each
# file are what say how wide that window was. What the kernel-wide files add on
# top of that is stated in the legend they ship with rather than here, because
# it changes what a difference means rather than how wide the window is.
cozy_capture_sandbox_kvm_exits() {
  local sample="$1"
  local report_dir="${COZY_REPORT_DIR:-/workspace/_out/cozyreport}/snapshots/${COZY_SNAPSHOT_NAME:-kubernetes}/sandbox-kvm-exits/sample-${sample}"
  local raw="${report_dir}/kvm-stats.txt"
  local err_log="${report_dir}/COLLECTION-FAILED.txt"
  local read_log="${report_dir}/read-error.log"
  local rc=0
  local counters_present=0
  local program read_at read_done
  # The mount point, and the seam that lets the walk above be exercised against
  # a tree of files instead of against a kernel. Production never sets it: the
  # path is where debugfs belongs, and mounting there inside a private namespace
  # is invisible outside this call.
  local mount_point="${COZY_DIAG_KVM_DEBUGFS:-/sys/kernel/debug}"

  # Checked, unlike its neighbours, because this is the one failure the markers
  # below cannot describe: with no directory every write under it fails, the
  # collector becomes a silent no-op, and the artifact carries the empty space
  # this capture exists to refuse -- with nowhere to put a marker saying so.
  if ! mkdir -p "${report_dir}"; then
    echo "the report directory for the sandbox KVM counters could not be created, so nothing was read and nothing could be written to say so" >&2
    return 1
  fi
  # Re-validated here for the reason every collector re-validates them: a value
  # assigned after this file is sourced never passed the assignment-time check,
  # and zero reaches `timeout` as no bound at all.
  COZY_DIAG_READ_TIMEOUT=$(_cozy_diag_seconds "${COZY_DIAG_READ_TIMEOUT-}" "$COZY_DIAG_READ_TIMEOUT_DEFAULT" COZY_DIAG_READ_TIMEOUT positive)
  COZY_DIAG_READ_GRACE=$(_cozy_diag_seconds "${COZY_DIAG_READ_GRACE-}" "$COZY_DIAG_READ_GRACE_DEFAULT" COZY_DIAG_READ_GRACE)

  # Both preconditions are reported rather than returned into silence, and both
  # say the same thing in their own words: nothing was asked, which is not a
  # reading that the arrangement cost the kernel nothing.
  if ! command -v unshare >/dev/null 2>&1; then
    echo "unshare is not on PATH, so the sandbox kernel's KVM counters were not read" >&2
    printf '%s\n' \
      'unshare is not on PATH, so debugfs could not be mounted in a namespace of this capture own and the counters were never asked for; that is not a reading that nesting cost it nothing' \
      >"${err_log}"
    return 1
  fi
  if ! command -v mount >/dev/null 2>&1; then
    echo "mount is not on PATH, so the sandbox kernel's KVM counters were not read" >&2
    printf '%s\n' \
      'mount is not on PATH, so debugfs was never mounted and the counters were never asked for; that is not a reading that nesting cost it nothing' \
      >"${err_log}"
    return 1
  fi

  program="if ! mount -t debugfs none \"\${1}\"; then exit 3; fi
$(_cozy_kvm_stats_probe)"

  echo "--- capturing the sandbox kernel's KVM exit counters (sample ${sample}) ---"
  # Stamped around the read rather than inside it, and here the stamps are the
  # only timing there is: a debugfs counter carries no sample time, and the two
  # readings of this pair are separated by the node-join wait rather than by a
  # knob, so without these the interval the pair spans is unrecoverable from the
  # artifact.
  read_at=$(date -u +%s)
  # stdin closed explicitly for the reason the node walk above states: nothing
  # here reads it today, and a program that started to would consume whatever
  # the caller was iterating.
  if command -v timeout >/dev/null 2>&1; then
    timeout -k "${COZY_DIAG_READ_GRACE}" "${COZY_DIAG_READ_TIMEOUT}" \
      unshare --mount --propagation private \
      sh -c "${program}" cozy-kvm-exits "${mount_point}" \
      >"${raw}" 2>"${read_log}" </dev/null || rc=$?
  else
    unshare --mount --propagation private \
      sh -c "${program}" cozy-kvm-exits "${mount_point}" \
      >"${raw}" 2>"${read_log}" </dev/null || rc=$?
    # Said in the capture and not only in the phase log: the warning that
    # names unbounded collectors fires inside the diagnostics phase, and two
    # of the three readings this pair takes run outside it.
    printf '%s\n' '[bounds] timeout is not on PATH here, so the reads this capture takes ran with no ceiling]' >>"${raw}" 2>/dev/null || true
  fi
  read_done=$(date -u +%s)

  # Emptied rather than only removed, because the arms below decide what to tell
  # the reader by whether there is an error log to send them to, and a variable
  # still holding the path of a file that is gone points at nothing.
  if [ ! -s "${read_log}" ]; then
    rm -f "${read_log}"
    read_log=
  elif [ "${rc}" -eq 0 ] || [ "${rc}" -eq 4 ] || [ "${rc}" -eq 5 ]; then
    # mount writes hints to stderr and exits zero, the way kubectl and talosctl
    # do beside it. Which file the complaint lands in is decided by the status,
    # not by whether stderr holds anything, so a healthy capture keeps its
    # warning instead of shipping a failure marker next to a reading.
    mv "${read_log}" "${report_dir}/READ-WARNINGS.txt" 2>/dev/null || true
    read_log=
  fi

  # Whether a counter reached the file, which is a different question from
  # whether the file has bytes in it. The probe writes its heading before it
  # reads anything, so a walk killed on its first blocked read leaves a file
  # that is not empty and holds no number; keyed on size, the arms below would
  # call that a partial reading, and the caller would be told a pair was
  # collected. Same distinction the probe makes one level down when it counts
  # counters rather than the directories they sit in.
  if grep -q -e '^\[global\] ' -e '^\[vm ' "${raw}" 2>/dev/null; then
    counters_present=1
  fi

  # How to read the numbers goes on every capture that holds numbers, which is a
  # wider set than the captures holding a whole reading. The naming trap and the
  # anonymity of the per-VM split do not depend on the walk having finished, and
  # a capture cut short is exactly where a reader needs them. Only the
  # instruction to difference two files asserts completeness, and that one stays
  # on the arm below that has it.
  if [ "${counters_present}" -ne 0 ]; then
    printf '%s\n' \
      '[these counters belong to the kernel that hosts the sandbox VMs and not what runs inside them: this suite runs in a container sharing the runner kernel, which is the hypervisor for the Talos nodes hack/e2e-prepare-cluster.bats starts with accel=kvm. An exit counted here is a sandbox node leaving its guest mode, so the cost of running a tenant worker inside that node is paid here too -- while the worker own exits into its sandbox node are counted by that node kernel, which this file does not read]' \
      '[what a difference between the two samples means depends on the level. A per-VM file is cumulative since that VM was created. A kernel-wide file is not an accumulator at all: the kernel answers it by summing the same counter over the VMs alive at the moment of the read, so a VM that went away between the samples takes its whole contribution with it. What the survivors accumulated in the meantime competes with that loss, so the difference is understated by a whole VM life while its sign proves nothing when positive. When it does come out negative, that is a finding about the sandbox -- a guest disappeared, since no per-vCPU counter ever decreases -- and not a rate; read the per-VM files beside it to see which one went]' \
      '[and several of these are not counts at all, sitting in the same directory under the same kind of name: guest_mode and blocking are per-vCPU booleans that every file here sums, so a reading is the number of vCPUs in that state at that instant -- bounded by the vCPU count, not by 1 -- while pages_4k, pages_2m, pages_1g, mmu_unsync and nx_lpage_splits are instantaneous, and max_mmu_rmap_size and max_mmu_page_hash_collisions are running maxima. A difference of any of those is a number with no meaning. Which kind a counter is comes from the kernel own descriptor tables in arch/x86/kvm/x86.c and include/linux/kvm_host.h and from nothing visible here, so read this as the names present when it was written rather than as a closed set]' \
      '[not every file in these directories is a counter, and the ones that are not are named on a skipped line above. Two different reasons appear there. mmu_rmaps_stat, which the kernel puts in every per-VM directory, is skipped by name and is never opened: it is a seq_file whose contents are computed on the first read, under that VM slots_lock and its MMU write lock, walking every rmap head -- so a reader that opened it to see what shape it was would already have stalled the guest page faults of the VM this capture exists to observe. Anything else that turns out not to hold exactly one integer is skipped after being read, which keeps it out of the numbers but cannot make its read free]' \
      '[there is no ept_violation counter here to look for. The nested-paging faults are counted as pf_taken and pf_fixed, under those names on both vendors, so a reader who searches for the Intel spelling concludes the capture is missing what it holds]' \
      '[a per-VM directory is named <pid>-<fd> with the pid in the kernel initial namespace, which this container cannot map to its own processes: /proc/<pid>/sched and NSpid both answer with the container-local number. So the split is per-VM and anonymous -- it says how evenly the sandbox VMs paid, not which sandbox node paid what]' \
      >>"${raw}"
  fi

  case "${rc}" in
    0)
      # The pairing instruction is the one line that stays on this arm, for the
      # reason the node CPU time capture withholds its own: telling a reader to
      # subtract two files asserts that both hold a whole reading.
      #
      # Unconditional on this arm, and deliberately not also gated on
      # counters_present. A zero status means the probe read counters, because
      # the probe exits 4 or 5 when it did not -- so the second test would be
      # unreachable, and an unreachable guard here is not free: it would absorb
      # those exit codes, leaving nothing that notices if the probe ever stopped
      # honouring them. The codes are pinned directly instead, by the two tests
      # that read them out of the capture.
      printf '%s\n' \
        '[subtracting this file from its sibling under the other sample directory, and the stamps below from each other, gives a rate. The two readings are taken on either side of the node-join wait, so the interval is that window rather than a fixed knob, and it is exact to the width of the two brackets]' \
        >>"${raw}"
      ;;
    3)
      echo "debugfs could not be mounted, so the sandbox kernel's KVM counters were not read" >&2
      # Branched on whether there is a message, because the sentence below
      # points at one. mount almost always writes a reason and exits non-zero,
      # but a refusal that says nothing leaves this marker holding a single line
      # telling the reader to look above it at an empty file -- the same shape
      # of promise-without-a-referent that the arms further down avoid.
      if [ -n "${read_log}" ]; then
        mv "${read_log}" "${err_log}" 2>/dev/null || true
        read_log=
        printf '%s\n' \
          'debugfs could not be mounted, so the counters were never reached; the message above is what mount said. This is not a kernel that was asked and had nothing to report' \
          >>"${err_log}"
      else
        printf '%s\n' \
          'debugfs could not be mounted, so the counters were never reached, and mount refused without a word on either stream. This is not a kernel that was asked and had nothing to report' \
          >>"${err_log}"
      fi
      printf '%s\n' \
        'these counters are unavailable: debugfs could not be mounted, and the failure is recorded beside this file' \
        >>"${raw}"
      ;;
    4 | 5) : ;;
    *)
      # Four arms rather than two, and the pair of questions they answer are
      # independent: whether a counter reached the file, and whether the read
      # left a message anywhere. Told only the first, a reader goes looking for
      # an explanation in the job log of a run that may be days old; told only
      # the second, they cannot tell a capture worth differencing from one that
      # holds nothing.
      if [ "${counters_present}" -ne 0 ] && [ -n "${read_log}" ]; then
        printf '%s\n' \
          'these counters are incomplete: the walk was cut short part way through the kvm directory, so a difference against the sibling sample understates every counter it did not reach; what the read said before it stopped is in the read-error.log beside this file' \
          >>"${raw}"
      elif [ "${counters_present}" -ne 0 ]; then
        printf '%s\n' \
          'these counters are incomplete: the walk was cut short part way through the kvm directory, so a difference against the sibling sample understates every counter it did not reach, and it stopped without a word on either stream' \
          >>"${raw}"
      elif [ -n "${read_log}" ]; then
        printf '%s\n' \
          'these counters are unavailable: the read produced no counter at all; what it said before it stopped is in the read-error.log beside this file' \
          >>"${raw}"
      else
        printf '%s\n' \
          'these counters are unavailable: the read produced no counter at all and stopped without a word on either stream, so what the kernel paid is not recorded here either way' \
          >>"${raw}"
      fi
      ;;
  esac
  printf '[read attempted from %s to %s epoch seconds]\n' \
    "${read_at}" "${read_done}" >>"${raw}"
  printf '\n[capture exit code: %s]\n' "${rc}" >>"${raw}"
  # Non-zero when the capture holds no counter, and only then -- whether that is
  # a read that failed or a kernel that answered and had none. Both callers ask
  # this function whether it collected something to pair with, and one of them
  # runs on the passing path where the report is the artifact nobody downloads,
  # so a capture holding no number has to answer out loud or the warning saying
  # the pair is broken never prints. That covers the two findings as well by
  # design: a kernel with no kvm directory at all is the most consequential
  # thing this instrument can report, and reaching only the report would be the
  # quietest possible place to put it. A capture cut short after it wrote
  # counters is the case this does NOT cover: it left a usable reading, marked
  # as partial, and telling the caller nothing was collected would contradict
  # the file sitting beside it.
  if [ "${rc}" -eq 3 ] || [ "${counters_present}" -eq 0 ]; then
    return 1
  fi
}

# The CPU time of the kernel one layer ABOVE the sandbox nodes.
#
# Every other CPU reading in this file has a subject inside the sandbox: a
# cgroup, a thread, a sandbox node's own /proc/stat. None of them can price the
# layers above, and that is where this suite's own tax is paid -- the sandbox
# nodes' guest time is charged to the runner kernel as user time, and whatever
# the runner VM loses to the machine hosting IT is charged to nobody the sandbox
# can see.
#
# /proc/stat is not namespaced, so a container reading it reads the kernel it
# runs on. The suite runs in a container on the runner VM, so this is a local
# read of the runner VM's own kernel and costs one open.
#
# What it answers that nothing else here does is steal. A sandbox node reporting
# steal 0 says the runner kernel gave it every turn it asked for, and says
# nothing about whether the runner VM itself was given every turn: that number
# exists only here, and it is the one that separates "this suite is slow" from
# "this machine was sharing a core with somebody else".
cozy_capture_runner_kernel_cpu_time() {
  local sample="$1"
  local report_dir="${COZY_REPORT_DIR:-/workspace/_out/cozyreport}/snapshots/${COZY_SNAPSHOT_NAME:-kubernetes}/runner-kernel-cpu-time/sample-${sample}"
  local raw="${report_dir}/proc-stat.txt"
  local err_log="${report_dir}/COLLECTION-FAILED.txt"
  local read_log="${report_dir}/read-error.log"
  local rc=0
  local rows_present=0
  local read_at read_done
  # The file to read, and the seam that lets the arms below be exercised against
  # a staged file instead of against a kernel. Production never sets it.
  local stat_file="${COZY_DIAG_RUNNER_PROC_STAT:-/proc/stat}"

  # Checked, unlike its neighbours, because this is the one failure the markers
  # below cannot describe: with no directory every write under it fails, the
  # collector becomes a silent no-op, and the artifact carries the empty space
  # this capture exists to refuse -- with nowhere to put a marker saying so.
  if ! mkdir -p "${report_dir}"; then
    echo "the report directory for the runner kernel CPU time could not be created, so nothing was read and nothing could be written to say so" >&2
    return 1
  fi
  # Re-validated here for the reason every collector re-validates them: a value
  # assigned after this file is sourced never passed the assignment-time check,
  # and zero reaches `timeout` as no bound at all.
  COZY_DIAG_READ_TIMEOUT=$(_cozy_diag_seconds "${COZY_DIAG_READ_TIMEOUT-}" "$COZY_DIAG_READ_TIMEOUT_DEFAULT" COZY_DIAG_READ_TIMEOUT positive)
  COZY_DIAG_READ_GRACE=$(_cozy_diag_seconds "${COZY_DIAG_READ_GRACE-}" "$COZY_DIAG_READ_GRACE_DEFAULT" COZY_DIAG_READ_GRACE)

  echo "--- capturing the runner kernel's CPU time (sample ${sample}) ---"
  # Stamped around the read, and here the stamps are the only timing there is: a
  # /proc/stat row carries no sample time, and the two readings of this pair are
  # separated by the node-join wait rather than by a knob, so without these the
  # interval the pair spans is unrecoverable from the artifact.
  read_at=$(date -u +%s)
  # stdin closed explicitly for the reason the walks above state: nothing here
  # reads it today, and a program that started to would consume whatever the
  # caller was iterating.
  if command -v timeout >/dev/null 2>&1; then
    timeout -k "${COZY_DIAG_READ_GRACE}" "${COZY_DIAG_READ_TIMEOUT}" \
      cat "${stat_file}" >"${raw}" 2>"${read_log}" </dev/null || rc=$?
  else
    cat "${stat_file}" >"${raw}" 2>"${read_log}" </dev/null || rc=$?
    # Said in the capture and not only in the phase log: the warning that
    # names unbounded collectors fires inside the diagnostics phase, and two
    # of the three readings this pair takes run outside it.
    printf '%s\n' '[bounds] timeout is not on PATH here, so the reads this capture takes ran with no ceiling]' >>"${raw}" 2>/dev/null || true
  fi
  read_done=$(date -u +%s)

  # Emptied rather than only removed, because the arms below decide what to tell
  # the reader by whether there is an error log to send them to, and a variable
  # still holding the path of a file that is gone points at nothing.
  if [ ! -s "${read_log}" ]; then
    rm -f "${read_log}"
    read_log=
  elif [ "${rc}" -eq 0 ]; then
    # A read that succeeded and still said something keeps its message beside the
    # capture rather than inside it, the way every other reader here does.
    mv "${read_log}" "${report_dir}/READ-WARNINGS.txt" 2>/dev/null || true
    read_log=
  fi

  # Whether a CPU row reached the file, which is a different question from
  # whether the file has bytes in it: /proc/stat carries intr, ctxt and btime
  # rows as well, and a read cut off after those holds numbers and no CPU time
  # at all. Keyed on size, the arms below would call that a reading.
  if grep -q '^cpu' "${raw}" 2>/dev/null; then
    rows_present=1
  fi

  # How to read the numbers goes on every capture that holds numbers, which is a
  # wider set than the captures holding a whole reading: the column trap and the
  # layer these rows belong to do not depend on the read having finished, and a
  # capture cut short is exactly where a reader needs them.
  if [ "${rows_present}" -ne 0 ]; then
    printf '%s\n' \
      '[these rows belong to the RUNNER VM kernel, one layer above the sandbox nodes: /proc/stat is not namespaced, so this container reads the kernel it runs on, which is the hypervisor hack/e2e-prepare-cluster.bats starts the three Talos nodes on with accel=kvm. The sandbox nodes report their own /proc/stat one layer down, under sandbox-host-cpu-time, and no capture in this report reads /proc/stat inside a tenant worker at all -- what sits under tenant-cpu-throttle and tenant-thread-cpu is cgroup and per-thread accounting, not this file. Reading these rows as a sandbox node compares a hypervisor against its guest]' \
      '[the cpu rows above are, after the label: user nice system idle iowait irq softirq steal guest guest_nice, in USER_HZ. The eighth number is steal and the ninth is guest. guest is already counted inside user, here as everywhere, so adding the two double-counts whatever the guests spent. How much of the user time on this kernel is guest time is not something these rows are read against anything to establish -- the runner also runs the CI process tree, containerd and this suite -- so take the ninth column as the guest share and the eighth as what was taken away, and neither as a proportion of the other]' \
      '[steal on THIS layer is the number that exists nowhere else in this report. It is time the machine hosting this runner VM gave to somebody else. A sandbox node reporting steal 0 while this row climbs points the wait one layer up -- and read the two as directions rather than rates, because the sandbox rows under sandbox-host-cpu-time are a pair seconds apart inside the diagnostics block while this pair spans the whole join window]' \
      '[read that column in ONE direction only. A steal that climbs is proof this runner VM was preempted. A steal of zero is not proof of the opposite: the column is filled only where the hypervisor exposes a paravirt steal clock, and where it does not the guest kernel prints zero forever. Nothing in this capture observes which of the two it is, and unlike the sandbox nodes one layer down -- started with accel=kvm by hack/e2e-prepare-cluster.bats, where KVM exposes the accounting -- nobody in this repository launches the runner VM, so the answer for this lane is unknown until somebody reads a non-zero here]' \
      '[the first row is the sum over every CPU and the cpuN rows below it are per-CPU, one per ONLINE CPU -- the kernel sums the first row over every possible CPU, so an offline one is inside the total and absent below it. What the row count is not is a core count: a cpuN row is a CPU as this kernel sees it, which is a hardware thread wherever SMT is exposed to the guest. This capture reads no topology, so it cannot say which this lane is; /sys/devices/system/cpu/cpuN/topology/thread_siblings_list is where that answer lives, and it is not in this artifact]' \
      >>"${raw}"
  fi

  case "${rc}" in
    0)
      # Gated on a row having arrived, unlike the KVM capture beside this one,
      # and the difference is in what the read can promise. That probe exits 4 or
      # 5 when it found no counter, so a zero there already means counters were
      # read; `cat` exits zero for any file it could open, including one holding
      # only the intr and ctxt rows that /proc/stat carries after the CPU time.
      # Ungated, the pairing instruction would ride on a file with nothing to
      # difference, and following it turns the sibling's whole cumulative total
      # into a rate over the window -- the largest number this artifact could
      # produce, arrived at by doing what it says.
      if [ "${rows_present}" -ne 0 ]; then
        printf '%s\n' \
          '[subtracting this file from its sibling under the other sample directory, and the stamps below from each other, gives a rate. The two readings are taken on either side of the node-join wait, so the interval is that window plus the sibling readings taken between each sample and the wait, every one bounded by its own ceiling; the stamps below, not the window, are the exact divisor]' \
          >>"${raw}"
      else
        # Opened, read to the end, and carrying no CPU time. That is a statement
        # about the file rather than about the read, and it has to be said in the
        # file: an empty-looking capture with a clean exit code otherwise reads
        # as a kernel that was idle.
        printf '%s\n' \
          'this reading is unavailable: the read completed and returned no cpu row at all, so this file holds whatever else /proc/stat carried and no CPU time. That is not a reading that the runner kernel was idle' \
          >>"${raw}"
      fi
      ;;
    *)
      # Four arms, and the pair of questions they answer are independent:
      # whether a CPU row reached the file, and whether the read left a message
      # anywhere. Told only the first, a reader goes looking for an explanation
      # in the job log of a run that may be days old; told only the second, they
      # cannot tell a capture worth differencing from one that holds nothing.
      if [ "${rows_present}" -ne 0 ] && [ -n "${read_log}" ]; then
        printf '%s\n' \
          'this reading is incomplete: the read was cut short part way through the file, so a difference against the sibling sample understates every row it did not reach; what the read said before it stopped is in the read-error.log beside this file' \
          >>"${raw}"
      elif [ "${rows_present}" -ne 0 ]; then
        printf '%s\n' \
          'this reading is incomplete: the read was cut short part way through the file, so a difference against the sibling sample understates every row it did not reach, and it stopped without a word on either stream' \
          >>"${raw}"
      elif [ -n "${read_log}" ]; then
        mv "${read_log}" "${err_log}" 2>/dev/null || true
        read_log=
        printf '%s\n' \
          'this reading is unavailable: the read returned no cpu row at all; what it said is in the COLLECTION-FAILED.txt beside this file. That is not a reading that the runner kernel was idle' \
          >>"${raw}"
      else
        printf '%s\n' \
          'this reading is unavailable: the read returned no cpu row at all and said nothing on either stream, so what this kernel spent is not recorded here either way. That is not a reading that it was idle' \
          >>"${raw}"
      fi
      ;;
  esac
  printf '[read attempted from %s to %s epoch seconds]\n' \
    "${read_at}" "${read_done}" >>"${raw}"
  printf '\n[capture exit code: %s]\n' "${rc}" >>"${raw}"
  # Non-zero when the capture holds no row, and only then. Both callers ask this
  # function whether it collected something to pair with, and one of them runs on
  # the passing path where the report is the artifact nobody downloads, so a
  # capture holding no number has to answer out loud or the warning saying the
  # pair is broken never prints. A capture cut short AFTER it wrote rows is the
  # case this does not cover: it left a usable reading, marked as partial, and
  # telling the caller nothing was collected would contradict the file beside it.
  if [ "${rows_present}" -eq 0 ]; then
    return 1
  fi
}

# The sandbox VMs' own QEMU threads, from the namespace they run in.
#
# The three Talos nodes are QEMU processes started by hack/e2e-prepare-cluster.bats
# inside this container, so they share its PID namespace with the shell running
# this suite: the same probe the tenant worker capture runs through `kubectl exec`
# runs here as a local read, against this container's own /proc.
#
# What it answers is where the runner kernel's user time went. The row above says
# this kernel spent it; this says which sandbox vCPU thread spent it, and whether
# the three nodes spent it evenly. A node-join failure in which one node's vCPU
# threads are pinned while the other two idle is a different fault from one in
# which all twenty-four are busy -- three nodes at `-smp 8`.
#
# What is NOT the claim here, because the obvious version of it is false:
# `cozy_capture_sandbox_node_cpu_time` reads each sandbox node's own /proc/stat
# through talosctl and names the node, so that collector does tell an uneven split
# from an even one. What no reading in this report does is price the split ON THE
# NODE-JOIN WINDOW: that one is sampled twice across a twelve-second interval
# inside the diagnostics block, after the join has already failed. This pair
# brackets the wait instead. What it adds beyond the window is the host-side
# accounting of each vCPU thread -- what the runner actually granted it, which
# no counter inside the guest can report, and nothing in this report samples
# the guest's own view on the same window to compare it against -- and QEMU's
# non-vCPU IO and worker threads, which no in-guest counter sees at all.
cozy_capture_sandbox_qemu_thread_cpu() {
  local sample="$1"
  local report_dir="${COZY_REPORT_DIR:-/workspace/_out/cozyreport}/snapshots/${COZY_SNAPSHOT_NAME:-kubernetes}/sandbox-qemu-thread-cpu/sample-${sample}"
  local raw="${report_dir}/qemu-threads.txt"
  local read_log="${report_dir}/read-error.log"
  local rc=0
  local threads_present=0
  local probe read_at read_done
  local pid_map=
  local srv pid_file pid
  # The proc mount to walk, and the seam that lets the arms below be exercised
  # against a staged tree instead of against a hypervisor. Production never sets
  # it: the probe defaults to the real one and the default is what runs.
  local proc_root="${COZY_DIAG_SANDBOX_PROC:-/proc}"
  # Where hack/e2e-prepare-cluster.bats leaves each guest's pid file. It writes
  # `srv<N>/qemu.pid` relative to /workspace in this same container, and the
  # kubernetes suites `cd ../../..` to /workspace before sourcing this library, so
  # the default is that directory rather than a path assembled from the cwd of
  # whoever called.
  local sandbox_root="${COZY_DIAG_SANDBOX_VM_ROOT:-/workspace}"

  if ! mkdir -p "${report_dir}"; then
    echo "the report directory for the sandbox QEMU thread CPU time could not be created, so nothing was read and nothing could be written to say so" >&2
    return 1
  fi
  COZY_DIAG_READ_TIMEOUT=$(_cozy_diag_seconds "${COZY_DIAG_READ_TIMEOUT-}" "$COZY_DIAG_READ_TIMEOUT_DEFAULT" COZY_DIAG_READ_TIMEOUT positive)
  COZY_DIAG_READ_GRACE=$(_cozy_diag_seconds "${COZY_DIAG_READ_GRACE-}" "$COZY_DIAG_READ_GRACE_DEFAULT" COZY_DIAG_READ_GRACE)

  # The same probe the tenant worker capture runs, and deliberately the same one
  # rather than a copy: it already answers the two outcomes that matter here --
  # "found QEMU" and "QEMU is gone" -- and it already names the three ways it can
  # come up short. A second copy would drift from this one exactly where the
  # failure paths are, which is where nobody looks until it matters.
  probe=$(_cozy_thread_cpu_probe)

  # Which pid is which node, read from the files the bringup already wrote. The
  # probe names processes by their container pid, and a pid identifies nothing to
  # a reader: the question this capture exists to answer is whether the
  # control-plane node or a worker-carrying one is starving, because the tenant
  # workers sit on specific sandbox nodes. Three tiny local reads buy that.
  #
  # Through a bounded external cat, never the shell's own redirect: `[ -r ]`
  # passes on a FIFO and the redirect open then blocks forever, outside anything
  # a wrapper can cover. Three tiny spawns buy the same ceiling every other read
  # here carries; where `timeout` is absent they run unbounded like the rest,
  # which the capture's own bounds note declares. The whole map is optional:
  # when it cannot be built the capture says so and stays anonymous, which is
  # what its KVM sibling does about a mapping that is genuinely unavailable.
  for srv in 1 2 3; do
    pid_file="${sandbox_root}/srv${srv}/qemu.pid"
    pid=
    if command -v timeout >/dev/null 2>&1; then
      pid=$(timeout -k "${COZY_DIAG_READ_GRACE}" "${COZY_DIAG_READ_TIMEOUT}" cat "${pid_file}" 2>/dev/null) || continue
    else
      pid=$(cat "${pid_file}" 2>/dev/null) || continue
    fi
    case "${pid}" in
      '' | *[!0-9]*) continue ;;
    esac
    pid_map="${pid_map}${pid} srv${srv}
"
  done

  echo "--- capturing the sandbox VMs' QEMU thread CPU time (sample ${sample}) ---"
  read_at=$(date -u +%s)
  if command -v timeout >/dev/null 2>&1; then
    timeout -k "${COZY_DIAG_READ_GRACE}" "${COZY_DIAG_READ_TIMEOUT}" \
      sh -c "${probe}" cozy-sandbox-qemu-threads "${proc_root}" \
      >"${raw}" 2>"${read_log}" </dev/null || rc=$?
  else
    sh -c "${probe}" cozy-sandbox-qemu-threads "${proc_root}" \
      >"${raw}" 2>"${read_log}" </dev/null || rc=$?
    # Said in the capture and not only in the phase log: the warning that
    # names unbounded collectors fires inside the diagnostics phase, and two
    # of the three readings this pair takes run outside it.
    printf '%s\n' '[bounds] timeout is not on PATH here, so the reads this capture takes ran with no ceiling]' >>"${raw}" 2>/dev/null || true
  fi
  read_done=$(date -u +%s)

  if [ ! -s "${read_log}" ]; then
    rm -f "${read_log}"
    read_log=
  elif [ "${rc}" -eq 0 ]; then
    mv "${read_log}" "${report_dir}/READ-WARNINGS.txt" 2>/dev/null || true
    read_log=
  fi

  # Whether a thread line reached the file, which is a different question from
  # whether the file has bytes in it: the probe writes a clock-tick heading and
  # a `process` line before any thread, and it writes its own findings when it
  # has none. Keyed on size, a capture holding only a finding would be labelled
  # a reading and a legend about columns would be pinned to a file with none.
  if grep -q '^[0-9]' "${raw}" 2>/dev/null; then
    threads_present=1
  fi

  if [ "${threads_present}" -ne 0 ]; then
    printf '%s\n' \
      '[these threads belong to the SANDBOX VMs: the three Talos nodes hack/e2e-prepare-cluster.bats starts as QEMU processes inside this container, which shares its PID namespace with them. They are the guests of the runner kernel whose /proc/stat sits under runner-kernel-cpu-time, and the hosts of the tenant workers whose own QEMU threads sit under tenant-thread-cpu. Reading these as a tenant worker inverts the layer]' \
      '[each line above beginning with a number is /proc/<pid>/task/<tid>/stat verbatim: the first field is the thread id, the second is the thread name in parentheses, utime is the 14th field and stime the 15th]' \
      '[counting those fields over whitespace reads the wrong column for exactly the threads this capture exists to identify: a KVM vCPU thread is named CPU 0/KVM, that space sits inside the parentheses, and every field after it shifts by one. Parse from the last ) in the line instead, after which the state is the first field, utime the 12th and stime the 13th]' \
      "[the names: CPU N/KVM is that sandbox node's vCPU N, the thread whose id equals the process id is QEMU's main thread and its emulator thread, and the rest are QEMU's own IO and worker threads, which carry the process name unless QEMU renamed them. The vCPU names exist because hack/e2e-prepare-cluster.bats starts every sandbox QEMU with -name debug-threads=on; a QEMU started without it shows the bare process name on every thread, and the split below is then by thread id only]" \
      '[utime and stime are clock ticks, at the rate the first line above reports, and both are cumulative since the thread started]' \
      '[a node with its eight vCPU threads busy and its siblings idle is a different failure from three nodes evenly loaded, and the runner kernel row one layer up cannot tell them apart: it sums all three]' \
      >>"${raw}"
  fi
  # Which pid is which node, or a statement that this capture does not know.
  # Written on every path, not only under thread lines: on the walk that found
  # a QEMU and could not read its tasks, which nodes still had usable pid files
  # is exactly the question, and gating this on threads would blank the answer
  # there. The KVM capture beside it discloses its own anonymity the same way.
  if [ -n "${pid_map}" ]; then
    printf '%s\n' \
      '[which sandbox node each QEMU process id belongs to, read from the pid files hack/e2e-prepare-cluster.bats wrote when it started them -- stated on every path, since a walk that met a QEMU it could not read leaves no thread lines while the node behind that pid is exactly the question. A mapping says which node the file names, not that the process still runs: a QEMU that died keeps its pid file. A thread line belongs to the process heading above it -- the stat line carries the thread id, so the position under its heading is the whole link. A node absent from this list had no usable pid file at the moment of this reading -- absent, unreadable, cut off by the read ceiling, empty or not one process id -- which for a node-join failure is itself worth knowing]' \
      >>"${raw}"
    printf '%s' "${pid_map}" | while read -r _pm_pid _pm_srv; do
      [ -n "${_pm_pid}" ] || continue
      printf '[process %s is %s]\n' "${_pm_pid}" "${_pm_srv}" >>"${raw}"
    done
  else
    printf '%s\n' \
      "[this capture is anonymous by node: no usable pid file under ${sandbox_root}/srv{1,2,3}/qemu.pid, so a process id above identifies a QEMU and not which sandbox node it is. The split is still per-VM -- it says how evenly the three paid, not which of them paid what]" \
      >>"${raw}"
  fi

  case "${rc}" in
    0)
      # The pairing instruction waits for this arm, for the reason the KVM
      # capture states beside it: telling a reader to subtract two files
      # asserts that both hold a whole reading, and a walk cut off after the
      # first node would have the other two read as having spent nothing
      # across the window.
      if [ "${threads_present}" -ne 0 ]; then
        printf '%s\n' \
          '[subtracting this file from its sibling under the other sample directory, and the stamps below from each other, gives a rate per thread. The two readings are taken on either side of the node-join wait, so the interval is that window plus the sibling readings taken between each sample and the wait, every one bounded by its own ceiling; the stamps below, not the window, are the exact divisor]' \
          >>"${raw}"
      else
        # Walked to the end and found nothing to read. The probe has already
        # said which of the three shapes it met, in its own words; what it
        # cannot say is that this is a finding rather than a collector that
        # came up short, and a `[capture exit code: 0]` under it otherwise
        # reads as a healthy capture of an idle machine. The closing sentence
        # follows the probe's own marker: one phrase over all three would call
        # a sandbox with three named QEMUs "a sandbox running no QEMU" two
        # lines under the list of them.
        if grep -q '^NO-QEMU-PROCESS:' "${raw}" 2>/dev/null; then
          printf '%s\n' \
            'these thread readings are unavailable: the walk completed and produced no thread line at all, for the reason it states above. That is not a reading that the sandbox VMs used no CPU -- a sandbox running no QEMU is a finding about this run rather than a shortfall of this capture' \
            >>"${raw}"
        elif grep -q '^NO-THREAD-LINES:' "${raw}" 2>/dev/null; then
          printf '%s\n' \
            'these thread readings are unavailable: the walk completed and produced no thread line at all, for the reason it states above. That is not a reading that the sandbox VMs used no CPU -- a QEMU that was named while its task files could not be read is a guest going away under the walk, which for a node-join failure is itself a finding' \
            >>"${raw}"
        else
          printf '%s\n' \
            'these thread readings are unavailable: the walk completed and produced no thread line at all, for the reason it states above. That is not a reading that the sandbox VMs used no CPU, and it says nothing about what the container was running' \
            >>"${raw}"
        fi
      fi
      ;;
    *)
      if [ "${threads_present}" -ne 0 ] && [ -n "${read_log}" ]; then
        printf '%s\n' \
          'these thread readings are incomplete: the walk was cut short part way through, so a difference against the sibling sample is missing whichever threads it did not reach; what the read said before it stopped is in the read-error.log beside this file' \
          >>"${raw}"
      elif [ "${threads_present}" -ne 0 ]; then
        printf '%s\n' \
          'these thread readings are incomplete: the walk was cut short part way through, so a difference against the sibling sample is missing whichever threads it did not reach, and it stopped without a word on either stream' \
          >>"${raw}"
      elif [ -n "${read_log}" ]; then
        # Into COLLECTION-FAILED.txt, the name this report gives the
        # artifact-level marker for a read that never returned, and the name
        # its sibling collector writes for this same shape. A reader sweeping
        # the tarball for failure markers would otherwise miss this walk.
        mv "${read_log}" "${report_dir}/COLLECTION-FAILED.txt" 2>/dev/null || true
        read_log=
        printf '%s\n' \
          'these thread readings are unavailable: the walk produced no thread line at all; what it said is in the COLLECTION-FAILED.txt beside this file. That is not a reading that the sandbox VMs used no CPU' \
          >>"${raw}"
      else
        printf '%s\n' \
          'these thread readings are unavailable: the walk produced no thread line at all and said nothing on either stream, so what the sandbox VMs spent is not recorded here either way. That is not a reading that they used none' \
          >>"${raw}"
      fi
      ;;
  esac
  printf '[read attempted from %s to %s epoch seconds]\n' \
    "${read_at}" "${read_done}" >>"${raw}"
  printf '\n[capture exit code: %s]\n' "${rc}" >>"${raw}"
  # Non-zero when the capture holds no thread line, and only then -- whether that
  # is a walk that failed or a container that had no QEMU to read. The second is
  # the more consequential of the two on this failure path: the sandbox VMs are
  # what the whole run stands on, and a container reporting none of them is a
  # finding rather than a shortfall of this collector. The probe says which of
  # the three shapes it met in its own words, inside the file.
  if [ "${threads_present}" -eq 0 ]; then
    return 1
  fi
}

# How much work one canary arm does, and how long it may take doing it.
#
# Constants rather than knobs, and assigned unconditionally so they stay that
# way: the legend states an expected duration against exactly these figures, so
# a run that quietly used other ones would publish a range that was never true
# of it.
#
# Sized so each arm takes about a second on a fast core: short enough that the
# pair costs a few seconds of one core there, long enough that the 10ms clock
# below is about a percent of the figure. Slower cores take proportionally
# longer, up to the ceiling below, and the expected ranges the legend states are
# where that is written down.
COZY_CANARY_CPU_ITERATIONS=30000000
# Declared in MiB and passed to dd in bytes. A suffix would be read differently
# by the dd of the machine that happens to run this -- the sandbox image ships
# the GNU one, where M is 1048576 while MB is a million, and dds elsewhere
# disagree about which spellings they accept at all -- so the multiplication
# happens here, where the unit the rate is reported in is the unit the constant
# is written in.
COZY_CANARY_MEM_BLOCK_MIB=64
COZY_CANARY_MEM_BLOCKS=128
# The rate below which an arm is pathological rather than merely slow, in the
# unit that arm's rate is printed in. The legend prints both figures from these
# two constants, so the number a reader is given is the number the collector
# compares against. The sentences around them describe how each floor sits
# against its band in words, and those are written rather than derived, so they
# are the part that can go stale when a constant moves.
#
# Both sit under the healthy band rather than at its edge, and how far under is
# what the ceiling limits. A floor an arm cannot reach before the ceiling stops
# it never fires: the arm is cut off before its rate can fall that far, so every
# alert arrives as the ceiling and carries a bound instead of the slowdown the
# floor was meant to name. The memory arm crosses 500 MiB/s at 16.4s against the
# 20s ceiling, under that band's own bottom of 8192 MiB in about eight seconds
# rather than at it, so a core sitting at the bottom reads slow and raises
# nothing where a floor placed at the bottom would have called that core
# pathological on a green run. The compute arm has room for the full decade: a
# tenth of a healthy mawk needs 10s, which is half the ceiling. Both
# floors are set downwards on purpose -- the rate is printed in the capture
# either way, so an alert this collector fails to raise costs less than one it
# manufactures on a healthy machine. hack/run-kubernetes-runner-canary_test.bats
# derives both crossings from these constants and fails when either leaves that
# window.
COZY_CANARY_CPU_MIN_RATE=3000000
COZY_CANARY_MEM_MIN_RATE=500
# The canary is work rather than a read, so it takes a ceiling of its own
# instead of COZY_DIAG_READ_TIMEOUT. That bound is sized for an apiserver that
# answers in under a second and lowering it is what the knob is for, which would
# kill a canary meant to take seconds -- and unlike a read, a slow run here is
# the finding rather than a lost one. This number is therefore the largest
# slowdown the capture can still put a figure on: at the sizes above it stops an
# arm an order of magnitude or more past the expected duration, past the factor
# being hunted and short of spending the diagnostics budget on it.
COZY_CANARY_RUN_BOUND_DEFAULT=20

# The canary clock: /proc/uptime, in centiseconds, into _COZY_CANARY_CS.
#
# A global rather than a printed value, because the alternative is a command
# substitution and that is a fork inside the interval being measured, at both
# ends of every arm.
#
# /proc/uptime rather than `date`, and not for portability: the field is a
# monotonic count no clock adjustment moves, and `read` is a builtin, so taking
# the stamp costs no process. What it costs is resolution -- the kernel prints
# hundredths -- which is why every duration here is quantised to 10ms and why
# the legend says so.
_cozy_canary_stamp() {
  local raw='' rest='' secs='' frac=''

  # The suppression goes ahead of the redirect rather than after it: applied in
  # order, a missing file would otherwise put the shell error on the real stderr
  # before the suppression took effect, and this function reports its own
  # failures by returning rather than by printing.
  read -r raw rest 2>/dev/null <"${COZY_DIAG_RUNNER_PROC_UPTIME:-/proc/uptime}" || return 1
  # Refused unless the shape is exactly the one the arithmetic below is written
  # for. A field with no decimal point is what this catches, and the only shape
  # it catches: `${raw#*.}` hands back the whole string when there is no `.`, so
  # a two-digit whole-second uptime passes the hundredths guard below as its own
  # hundredths and ten seconds reads as 1010 centiseconds -- a duration off by a
  # percent, manufactured by the one clock every figure here divides by. A field
  # carrying three decimals never reaches the arithmetic at all: it is refused
  # by that hundredths guard rather than by this one.
  case "${raw}" in
    *.*) ;;
    *) return 1 ;;
  esac
  secs="${raw%%.*}"
  frac="${raw#*.}"
  case "${secs}" in
    0 | [1-9]*) ;;
    *) return 1 ;;
  esac
  case "${secs}" in
    *[!0-9]*) return 1 ;;
  esac
  case "${frac}" in
    [0-9][0-9]) ;;
    *) return 1 ;;
  esac
  # A leading zero is octal to `$(( ))`, and 09 is not a number there at all, so
  # the hundredths lose theirs before they are added.
  frac="${frac#0}"
  _COZY_CANARY_CS=$(( secs * 100 + frac ))
}

# One arm: run a fixed amount of work under a wall-clock ceiling and time it.
#
# The ceiling and its kill grace are read from the globals the collector
# re-validates, the way the shared cAdvisor stream reads the pair its own caller
# validates. Both also take a pass at source time, so this function finds them
# set however it is reached; what the collector's pass adds is a value assigned
# after sourcing, which is how a test sets one.
#
# Sets _COZY_CANARY_MS to the elapsed milliseconds and _COZY_CANARY_RC to the
# status the shell waited on: the work's own whenever the work ended by itself,
# which is what a healthy run does under a ceiling as much as without one, and
# timeout's when the ceiling ended it instead. Which of the two a number came
# from is not readable from the number, which is why the clock decides it below.
# Returns 1 when the clock could not be read at both ends and 2 when it read but
# went backwards, which the caller reports as different things -- neither is a
# duration, but the first is silence about this machine and the second is a
# broken instrument. Both are different again from work that failed, which is a
# statement about the machine.
_cozy_canary_run_arm() {
  local capture="$1"
  shift
  local start=0 end=0

  _COZY_CANARY_CS=0
  _COZY_CANARY_MS=0
  _COZY_CANARY_RC=0
  # Whether a ceiling was in play at all, for the reporter to read beside the
  # exit status: 124 and 137 are what the ceiling produces, and they are also
  # what a work command exiting on its own or an outside SIGKILL produces, so
  # the status alone cannot say the ceiling fired.
  _COZY_CANARY_BOUNDED=0

  _cozy_canary_stamp || return 1
  start="${_COZY_CANARY_CS}"
  # stdin closed for the reason every walk in this file closes it: nothing here
  # reads it, and a program that started to would consume whatever the caller
  # was iterating. stdout is discarded because an arm exists for its duration
  # and its exit status already says whether the work ran to the end; stderr is
  # kept, since that is where dd reports and where timeout names a command it
  # could not start. A ceiling that fires says nothing there -- without
  # --verbose, which this call does not pass, timeout is silent about it -- and
  # that silence is why the status below is read against the clock rather than
  # on its own.
  if command -v timeout >/dev/null 2>&1; then
    _COZY_CANARY_BOUNDED=1
    timeout -k "${COZY_DIAG_READ_GRACE}" "${COZY_CANARY_RUN_BOUND}" \
      "$@" >/dev/null 2>>"${capture}" </dev/null || _COZY_CANARY_RC=$?
  else
    "$@" >/dev/null 2>>"${capture}" </dev/null || _COZY_CANARY_RC=$?
  fi
  _cozy_canary_stamp || return 1
  end="${_COZY_CANARY_CS}"

  # A clock that went backwards is refused rather than published as a negative
  # duration. Every figure this capture prints is derived from it, and a wrong
  # duration produces a rate that reads exactly like a real one.
  [ "${end}" -ge "${start}" ] || return 2
  _COZY_CANARY_MS=$(( (end - start) * 10 ))
}

# One arm reported into the capture: what it ran, whatever it said on stderr,
# how long it took, and what that divides to. <quantity> is the work expressed
# in <unit>s, so the rate is <unit>s per second, and <floor> is the rate in that
# unit below which the legend calls the machine pathological rather than slow.
#
# Returns non-zero when the arm could not be measured at all, so the caller can
# tell a capture worth reading from one that holds nothing. An arm that finished
# inside one tick was measured: it carries the note instead of a rate and still
# returns zero. Sets _COZY_CANARY_ALERT when the figure it did produce is below
# <floor> or was cut off by the ceiling. The status cannot carry that: an arm
# that measured a starved machine produced a reading and returns zero like any
# arm that finished.
_cozy_canary_report_arm() {
  local capture="$1" label="$2" quantity="$3" unit="$4" floor="$5"
  shift 5
  local timed=0 ms=0 rate=0 bound_rate=0

  printf '\n=== arm: %s ===\n' "${label}" >>"${capture}"
  printf 'command: %s\n' "$*" >>"${capture}"
  # The work this arm was asked for, written down before it is divided by
  # anything. Every other figure here is derived, so a reader who doubts one of
  # them has the two inputs in front of them rather than in the source of the
  # release that produced the artifact.
  printf 'work: %s %s\n' "${quantity}" "${unit}" >>"${capture}"
  _cozy_canary_run_arm "${capture}" "$@" || timed=$?
  if [ "${timed}" -eq 2 ]; then
    printf '%s\n' \
      'this arm produced no reading: the canary clock read at both ends and the second stamp was the earlier one, so the instrument is wrong rather than the machine slow. Nothing is derived from it here' \
      >>"${capture}"
    return 1
  fi
  if [ "${timed}" -ne 0 ]; then
    printf '%s\n' \
      'this arm produced no reading: the canary clock could not be read at both ends, so how long the work took is not recorded here. That is silence about this machine rather than a finding about it' \
      >>"${capture}"
    return 1
  fi
  ms="${_COZY_CANARY_MS}"
  printf 'elapsed: %s ms\n' "${ms}" >>"${capture}"

  # Whether 124 or 137 means the ceiling is decided by the clock rather than by
  # the status: timeout exits 124 when its term signal ends the work and the
  # work exits 137 when a kill does, and an outside kill -- the OOM killer
  # taking the 64 MiB buffer dd allocates is the realistic one -- produces the
  # same 137 with the ceiling never involved. An arm that ended in either status
  # before the ceiling could have fired was ended by something else, and
  # reporting that as the ceiling manufactures the too-slow-to-finish finding
  # this collector exists to detect. No tolerance is given for the truncation,
  # because a ceiling that fired cannot read short: both stamps truncate
  # hundredths off the one clock and the bound is a whole number of seconds, so
  # the difference of the two floors is at least the bound whatever fraction the
  # first stamp landed on. A duration under it was produced by something else.
  local at_ceiling=0 ceiling_note=''
  case "${_COZY_CANARY_RC}" in
    124 | 137)
      if [ "${_COZY_CANARY_BOUNDED}" -eq 1 ] && [ "${ms}" -ge $(( COZY_CANARY_RUN_BOUND * 1000 )) ]; then
        at_ceiling=1
      fi
      ;;
  esac
  # Named apart the way the sibling collectors name a kill apart from a
  # deadline: 137 says a kill ended the arm where 124 says the term signal did,
  # and that is a different observation even though the status does not say
  # whose kill it was. Timeout's own follow-up and a kill from outside are the
  # same 137 here, the way they are before the ceiling above.
  if [ "${at_ceiling}" -eq 1 ] && [ "${_COZY_CANARY_RC}" -eq 137 ]; then
    ceiling_note=', on a status that says a kill ended the arm without saying whose'
  fi
  # The work's own failure is settled before its duration is, and the order is
  # the whole of it: a command this machine does not have fails in well under
  # one tick, so a duration-first reading would report the one arm that never
  # ran as the one arm too fast to measure.
  case "${_COZY_CANARY_RC}" in
    0) ;;
    124 | 137)
      if [ "${at_ceiling}" -ne 1 ]; then
        if [ "${_COZY_CANARY_BOUNDED}" -eq 1 ]; then
          printf 'this arm produced no reading: the work ended with status %s after %s ms, before the %ss ceiling could have fired, so something other than the canary bound ended it and the duration above is how long it survived rather than how long the work takes here\n' \
            "${_COZY_CANARY_RC}" "${ms}" "${COZY_CANARY_RUN_BOUND}" >>"${capture}"
        else
          printf 'this arm produced no reading: the work ended with status %s with no ceiling in play, so something on this machine ended it and the duration above is how long it survived rather than how long the work takes here\n' \
            "${_COZY_CANARY_RC}" >>"${capture}"
        fi
        return 1
      fi
      ;;
    *)
      # The exit status goes in the line rather than only in the stderr above
      # it: a 127 is a binary missing from this machine and a non-zero from the
      # work itself is something else, and the number is what tells them apart.
      printf 'this arm produced no reading: it ended %s, so the duration above is how long it took to fail rather than how long the work takes here\n' \
        "${_COZY_CANARY_RC}" >>"${capture}"
      return 1
      ;;
  esac
  # And a zero duration before anything divides by it. It is not a fast machine
  # reported badly, it is a duration this clock cannot express, and dividing by
  # it ends the shell. Only a finished arm can reach this with a zero: the
  # ceiling needs the whole bound on the clock, and a kill ahead of the ceiling
  # was returned above.
  if [ "${ms}" -eq 0 ]; then
    printf '%s\n' \
      'no rate: the arm finished inside one tick of the 10ms clock, so its duration is under the resolution rather than measured. At the sizes this canary runs, that is itself unexpected' \
      >>"${capture}"
    return 0
  fi
  # Integer division, so a rate under one unit per second lands on zero -- and a
  # capture saying `rate: 0` reads as a machine that did nothing rather than as
  # arithmetic that ran out of places.
  rate=$(( quantity * 1000 / ms ))
  # The figure the ceiling sentence prints is rounded up where the rate line is
  # truncated, because the two are different kinds of number: a rate is measured
  # and rounds toward what was seen, while this is an upper bound on work that
  # never finished, and truncating a bound downwards prints a strict inequality
  # the arm itself can sit inside.
  bound_rate=$(( (quantity * 1000 + ms - 1) / ms ))
  # The alert every call site turns into a job-log line: the two outside the
  # diagnostics block and the one inside it. Raised here because this is the one
  # place the figure and the floor it is read against are both in scope. An arm
  # the ceiling stopped and an arm that finished under the floor are the same
  # answer to the question this collector exists to ask -- the machine is not
  # getting the work done -- and what differs is whether the figure is labelled a
  # bound or a reading. An arm that finished inside one tick returned above and
  # raises nothing: too fast to measure is not slow.
  if [ "${at_ceiling}" -eq 1 ] || [ "${rate}" -lt "${floor}" ]; then
    _COZY_CANARY_ALERT=1
  fi
  if [ "${at_ceiling}" -eq 1 ]; then
    # The ceiling firing is the strongest reading this arm can produce, so it
    # is recorded as a bound rather than dropped: the work did not finish in
    # that many seconds, which is already a multiple of what it should take.
    # Not recorded as a rate, because a rate would average over work that did
    # not all happen. One sentence rather than two: rounding up makes the bound
    # at least one in the printed unit for any quantity of at least one, and the
    # sizes declared above are never zero, so there is no zero case left for a
    # second sentence to carry.
    printf 'this arm did not finish: it was stopped at the %ss ceiling%s, so its rate is BELOW %s %s per second and that figure is a bound rather than a reading\n' \
      "${COZY_CANARY_RUN_BOUND}" "${ceiling_note}" "${bound_rate}" "${unit}" >>"${capture}"
  else
    if [ "${rate}" -eq 0 ]; then
      printf 'rate: under one %s per second, which the work and elapsed lines above give exactly\n' \
        "${unit}" >>"${capture}"
    else
      printf 'rate: %s %s per second\n' "${rate}" "${unit}" >>"${capture}"
    fi
  fi
}

# The runner layer fixed-work canary.
#
# The counters this report already carries are read in whatever unit their
# source publishes -- time in the /proc/stat rows and the per-thread ticks,
# events in the KVM exits, throttled periods beside throttled seconds in the
# cgroup rows, and instantaneous values and running maxima in the files sitting
# beside those exits -- and none of them is a fixed quantity of work.
# So a machine that spent every tick and got a fraction of the work done reads
# as healthy in all of them. That is the shape the node-join failures on this
# lane have: the counters at every layer look normal while the work does not
# get done. This collector carries its own unit of work instead, which gives its
# reading a scale of its own and makes the pathology visible in a single red run
# with no green one beside it.
#
# Two arms, because the candidates the time counters cannot separate act on
# different resources: interference on the shared cache and the memory
# controller from whatever else the host is running, and a core doing less per
# cycle. The two arms differ in how much of each they feel rather than in being
# deaf to one: the compute arm's working set is small enough that pressure on
# the shared cache and the memory controller moves it little, while the memory
# arm is dominated by it. A large asymmetric shift between them therefore points
# at one of the two, to the factor of ten these figures resolve and no finer.
#
# What this is NOT is a measurement of the tenant workers. It runs at the runner
# layer, where the sandbox nodes are hosted. Two layers down, the same
# fixed-work question is already answered by how long the tenant workers take to
# unpack their initramfs, which the serial console capture records. One layer
# down no capture times it: the QEMU line that boots the sandbox nodes passes no
# serial backend (hack/e2e-prepare-cluster.bats), so nothing carries their
# console anywhere for a capture to read. What those nodes do put in this report
# is their kernel ring buffer, which hack/cozyreport.sh pulls over the Talos API
# into sandbox-host/talos-<node>-dmesg.txt -- read there for what their kernels
# said, and a canary here would still be measuring the layer above them.
cozy_capture_runner_canary() {
  local sample="$1"
  local report_dir="${COZY_REPORT_DIR:-/workspace/_out/cozyreport}/snapshots/${COZY_SNAPSHOT_NAME:-kubernetes}/runner-canary/sample-${sample}"
  local capture="${report_dir}/fixed-work.txt"
  local readings=0 mem_mib=0 cpu_program='' read_at='' read_done=''

  # Cleared before anything below can return, so a call site reads it after
  # every exit from here rather than only after the ones that reached an arm.
  _COZY_CANARY_ALERT=0
  # Checked, unlike most of what follows: with no directory every write below
  # fails, the collector becomes a silent no-op, and the artifact carries the
  # empty space this capture exists to refuse -- with nowhere to put a marker
  # saying so.
  if ! mkdir -p "${report_dir}"; then
    echo "the report directory for the runner canary could not be created, so nothing was measured and nothing could be written to say so" >&2
    return 1
  fi
  # Truncated once here and appended to from then on, the way every raw file
  # beside this one is written: the arms and the legend are written in pieces,
  # and a file left over from an earlier call would otherwise carry two runs of
  # one sample with nothing between them to say where the first ended. Opened
  # with `true` rather than `:` and checked: `:` is a POSIX special built-in,
  # so a redirection that fails on it exits a non-interactive shell -- dash,
  # which is /bin/sh in the sandbox image, honours that -- and on the failure
  # path that exit ends the whole diagnostics block, straight past the call
  # site's `|| true`. The tenant snapshot survives it -- it is an EXIT trap,
  # armed before this block runs -- so what the exit costs is the rest of the
  # diagnostics, not the snapshot behind them. A capture that cannot be opened
  # is the mkdir case again: nothing can be measured, and the report has nowhere
  # to say so.
  if ! true >"${capture}"; then
    echo "the capture file for the runner canary could not be opened for writing, so nothing was measured and nothing could be written to say so" >&2
    return 1
  fi
  # Re-validated here for the reason every collector re-validates: a value
  # assigned after this file is sourced never passed the assignment-time check,
  # and zero reaches `timeout` as no bound at all.
  COZY_CANARY_RUN_BOUND=$(_cozy_diag_seconds "${COZY_CANARY_RUN_BOUND-}" "$COZY_CANARY_RUN_BOUND_DEFAULT" COZY_CANARY_RUN_BOUND positive "zero is no ceiling at all, and an arm that never returns costs the phase whatever is gated behind it")
  COZY_DIAG_READ_GRACE=$(_cozy_diag_seconds "${COZY_DIAG_READ_GRACE-}" "$COZY_DIAG_READ_GRACE_DEFAULT" COZY_DIAG_READ_GRACE)

  mem_mib=$(( COZY_CANARY_MEM_BLOCK_MIB * COZY_CANARY_MEM_BLOCKS ))
  # `print s` is kept and its output discarded: the sum is not wanted, but an
  # awk able to see that the loop has no effect would be free to skip it, and a
  # canary optimised away reports a healthy machine at any speed.
  cpu_program="BEGIN { s = 0; for (i = 0; i < ${COZY_CANARY_CPU_ITERATIONS}; i++) s = (s + i) % 1000003; print s }"

  echo "--- running the runner fixed-work canary (sample ${sample}) ---"
  # Stamped around the pair for the reason the readings beside this one are
  # stamped: the capture carries no sample time of its own, and the two samples
  # are separated by the node-join wait rather than by a knob.
  read_at=$(date -u +%s)
  if _cozy_canary_report_arm "${capture}" \
    "compute, a loop over a handful of integer-valued scalars" \
    "${COZY_CANARY_CPU_ITERATIONS}" iterations "${COZY_CANARY_CPU_MIN_RATE}" \
    awk "${cpu_program}"; then
    readings=$(( readings + 1 ))
  fi
  if _cozy_canary_report_arm "${capture}" \
    "memory, a store stream over blocks sized against one core's cache" \
    "${mem_mib}" MiB "${COZY_CANARY_MEM_MIN_RATE}" \
    dd if=/dev/zero of=/dev/null "bs=$(( COZY_CANARY_MEM_BLOCK_MIB * 1048576 ))" "count=${COZY_CANARY_MEM_BLOCKS}"; then
    readings=$(( readings + 1 ))
  fi
  read_done=$(date -u +%s)

  if ! command -v timeout >/dev/null 2>&1; then
    # Said in the capture and not only in the phase log, for the reason the
    # readings beside this one say it there: the warning naming the unbounded
    # collectors fires inside the diagnostics phase, and one of this pair of
    # samples is taken outside that phase on every run.
    printf '\n%s\n' '[bounds] timeout is not on PATH here, so the arms above ran with no ceiling; each of them terminates on its own, so what was lost is the ceiling rather than the reading -- but on the slow machine this canary exists to catch, fixed work with no ceiling can take minutes rather than seconds, and everything that prices this collector at two bounded arms no longer holds]' \
      >>"${capture}"
  fi

  printf '\n' >>"${capture}"
  printf '%s\n' \
    '[this canary runs on the RUNNER VM, inside the sandbox container: one layer above the three Talos nodes and two above the tenant workers. It is the only reading in this report that carries its own unit of work at this layer; two layers down the serial console capture records the tenant workers unpacking their initramfs, and one layer down no capture times it, because the QEMU line that boots the sandbox nodes passes no serial backend for a capture to read; what those nodes put here instead is their kernel ring buffer, under sandbox-host/talos-<node>-dmesg.txt. The counters beside it are read in the units their sources publish -- time in the CPU rows, events in the KVM exits, instantaneous values and running maxima in the files beside those -- and a counter with no unit of work in it cannot see a machine that spent its ticks and got less done with them, which is the shape this lane keeps failing in]' \
    >>"${capture}"
  printf '[two arms, and what makes them answer the same interference differently is the working set, not the amount of work each does. The compute arm loops over a handful of scalars, which live in registers and the nearest cache, so pressure on the shared cache or the memory controller moves it little rather than not at all. The memory arm streams blocks: a read of /dev/zero zeroes the buffer the caller supplied and a write to /dev/null never looks at it, so each block is one pass of stores over the whole block. The block size is both the working set and the reuse distance -- %s MiB here, against the 32 MiB of last-level cache an EPYC core can allocate into on the parts this lane has run on -- and a block that exceeds that cache leaves no byte still cached by the time it is written again, so the traffic lands on the memory controller. Which part this run got is recorded in sandbox-host/runner-identity.txt at the root of this report, and that is what tells a reader of this artifact which case it is looking at: on a part whose per-core cache exceeds the block size this arm stops being memory-bound and reads high. On a machine in front of you the dd line above can be rerun with a block size that fits the cache, and the figure rises several times over]\n' \
    "${COZY_CANARY_MEM_BLOCK_MIB}" >>"${capture}"
  printf '[expected ranges, and what they are worth. These are what the construction can do on a healthy server core, not figures measured on this lane, and they are stated to the precision they have: a factor of ten is what they resolve and a factor of two is not. Memory arm: one core sustains single-digit to low-double-digit GB per second of streaming stores, because the ceiling is how many misses one core keeps outstanding rather than how many memory channels the socket has, so the %s MiB it writes land in about a second on a fast core and in about eight at the bottom of that band, while anything under %s in the MiB-per-second unit the rate lines above are printed in, about half a gigabyte a second, is pathological rather than merely slow. Compute arm: the rate depends on the awk implementation as much as on the core, and that implementation is a property of the sandbox image rather than of the run, pinned by digest in packages/core/testing/images/e2e-sandbox/Dockerfile, so on the mawk that image ships a simple loop over integer-valued scalars runs at tens of millions of iterations a second and the %s of them here land in about a second, so anything under %s iterations a second, one factor of ten below that, is pathological rather than merely slow]\n' \
    "${mem_mib}" "${COZY_CANARY_MEM_MIN_RATE}" "${COZY_CANARY_CPU_ITERATIONS}" "${COZY_CANARY_CPU_MIN_RATE}" >>"${capture}"
  printf '%s\n' \
    '[what this canary does NOT separate. It reads wall clock only, so an arm that took ten times as long could be a core doing less per cycle or a container that was not on a core at all. Where to look for the second is runner-kernel-cpu-time beside this capture and sandbox-host-cpu-time one layer down, and their steal columns do not read the same way. On the runner row a climb is proof this VM was preempted and a zero proves nothing, because nobody here launches that VM and the column is filled only where the hypervisor exposes the clock. One layer down the sandbox nodes are started with accel=kvm, which hands the guest that accounting, so a zero there means the node got every turn it asked for. Which is what makes the pair worth reading together: a sandbox zero under a climbing runner row puts the wait a layer above them. Read those first when an arm here is slow, and read each for what it covers: the runner-kernel rows are a pair bracketing the node-join wait, so they describe the whole join window next to these seconds rather than these seconds themselves, while the sandbox-host rows are a pair seconds apart inside the on-failure diagnostics block, taken minutes after the wait already failed and absent from a green run altogether]' \
    '[the two samples. Sample 1 is taken before the node-join wait and sample 2 after it, on the failing and the passing path alike, on the same binaries in the same container. A difference between them puts the change inside the interval the pair brackets, which is the wait together with the readings taken on either side of it; two equally slow samples say it was already there going into that interval, which is the case the expected ranges above are the only instrument for -- the pair speaks for the interval between its own readings and for nothing before them]' \
    '[this collector perturbs what it measures, which is why it is placed where it is. It occupies one core for the duration of each arm, twice per run. Sample 1 runs before the first reading of all three pairs that bracket the node-join wait, and sample 2 after the last of their second readings, so neither burn falls inside any interval those pairs divide by]' \
    '[durations here are read from /proc/uptime, which the kernel prints in hundredths of a second, so every figure is quantised to 10ms. Against arms sized to take about a second that is about a percent, and it is why an arm finishing inside one tick is reported as being under the resolution rather than as a rate]' \
    >>"${capture}"
  printf '[read attempted from %s to %s epoch seconds]\n' \
    "${read_at}" "${read_done}" >>"${capture}"

  # Non-zero when no arm reported at all, and only then. An arm that finished
  # inside one tick counts as having reported, so a capture holding that note
  # instead of a rate still returns zero. Both call sites outside the
  # diagnostics block consume this status to put a shortfall in the job log, and
  # both of them run on the passing path, where the report is the artifact
  # nobody downloads.
  if [ "${readings}" -eq 0 ]; then
    return 1
  fi
}

# The shell run inside a worker's compute container to list QEMU's threads.
#
# A function rather than a literal at the call site so it can be run against a
# tree of files instead of against a container: it takes the /proc to walk as
# its first argument and defaults to the real one, which is the only argument
# the caller passes. That seam is the whole reason the branches below can be
# exercised at all -- staging a process whose threads are worth reading needs a
# hypervisor, and the two outcomes this has to get right are "found QEMU" and
# "QEMU is gone", the second of which is a routine result on this failure path.
#
# Written for the container's shell rather than for the runner's: virt-launcher
# ships no bash, and every construct here is POSIX. It also shells out to
# nothing except `getconf`, whose absence it names: `read` is a builtin, and a
# probe that needed `cat` would report a container missing coreutils as a
# container whose QEMU had exited -- which is one of the two readings this
# collector exists to tell apart, manufactured rather than observed.
#
# For the same reason the walk counts what it managed to read. Having read no
# process at all and having read them all without finding QEMU are different
# findings, and only the second is about the guest.
_cozy_thread_cpu_probe() {
  cat <<'PROBE'
proc_root=${1:-/proc}
found=0
seen=0
lines=0
printf 'clock ticks per second: %s\n' "$(getconf CLK_TCK 2>/dev/null || echo unavailable)"
for comm_file in "${proc_root}"/[0-9]*/comm; do
  comm=
  read -r comm <"${comm_file}"
  [ -n "${comm}" ] || continue
  seen=$((seen + 1))
  case "${comm}" in
    qemu-*) ;;
    *) continue ;;
  esac
  pid_dir=${comm_file%/comm}
  found=1
  printf 'process %s comm %s\n' "${pid_dir##*/}" "${comm}"
  # One line per file, because that is all /proc/<pid>/task/<tid>/stat is, and
  # `read` returns non-zero on a last line with no newline while still setting
  # the variable -- so the status is not what decides whether anything arrived.
  for stat_file in "${pid_dir}"/task/*/stat; do
    stat_line=
    read -r stat_line <"${stat_file}"
    [ -n "${stat_line}" ] || continue
    lines=$((lines + 1))
    printf '%s\n' "${stat_line}"
  done
# On the loop rather than on either read, and that is not a style choice.
# Redirections are applied left to right, so a `2>/dev/null` written after the
# input redirect is not in place yet when opening the input fails -- and `read`
# is a builtin, so the complaint is the shell's own and there is no child whose
# stderr was ever covered. A thread that exits between the glob and the read is
# routine here, this probe's stderr is the collector's error artifact, and a
# healthy capture must not ship a warnings file naming something that did not
# affect the reading. On the loop the redirect is in place before the body runs,
# which is also the only spelling that holds in every shell.
done 2>/dev/null
if [ "${seen}" -eq 0 ]; then
  printf '%s\n' 'NO-PROC-READ: not one process could be read under the proc mount, so this says nothing about what the container was running'
elif [ "${found}" -eq 0 ]; then
  printf '%s\n' "NO-QEMU-PROCESS: ${seen} process(es) were read and none is named qemu-*, so this container had no QEMU threads to read"
elif [ "${lines}" -eq 0 ]; then
  # QEMU was named and then gone: its comm was read and not one of its task
  # stat files could be. Without this the probe exits clean carrying only a
  # heading, and a heading is enough to make the capture non-empty, so the
  # collector would pin a legend about columns onto a file that has none.
  printf '%s\n' 'NO-THREAD-LINES: a qemu-* process was named and not one of its task stat files could be read, so it exited between the two reads'
fi
PROBE
}

# Split the compute container's CPU time across QEMU's threads, from inside it.
#
# This exists because the collector beside it measures the wrong subject for the
# question it is usually read for. The throttling capture reads the compute
# container's cgroup, and that cgroup holds every thread QEMU runs: the guest's
# vCPUs, QEMU's main thread, and its IO and worker threads. So a container
# burning most of its quota while the guest reports no progress at all is two
# incompatible findings wearing one number -- a guest computing at its ceiling,
# and a guest standing still while something beside it spends the quota -- and
# no container-level counter can separate them. The per-thread split is the only
# reading that can, and nothing else in this tree collects it.
#
# Read from inside the container rather than from the kubelet, which is the
# opposite of what the throttling capture does and for a reason that does not
# apply here: cAdvisor publishes per-container series and no per-thread ones, so
# there is no outside surface carrying this at all. The cost of going inside is
# the one that capture's comment names -- a reader that shares the cgroup it
# measures slows down exactly when the answer matters -- which is why the read
# is bounded and why a container that never answers gets a sentence rather than
# an empty file. hack/e2e-capture-dataplane.sh reads a container's own cpu.stat
# the same way, and the exec here carries no --request-timeout for the same
# reason its execs do not: the flag bounds a request, and this is a streamed
# connection whose bound is the wall-clock wrapper.
#
# Nothing is computed here. The stat lines go into the artifact verbatim and the
# legend beside them says how to read the two fields that matter, because the
# field positions are a trap that produces a wrong answer rather than an error:
# the thread this capture exists to identify is the one whose name contains a
# space.
#
# Takes the sample number it is writing, because it is called twice, and it is
# called from the loop the other two subjects already share rather than from one
# of its own: every counter here is cumulative since the thread started, so a
# rate needs two readings, and the wait between them is the one already being
# paid.
cozy_capture_tenant_worker_thread_cpu() {
  local sample="$1"
  local report_dir="${COZY_REPORT_DIR:-/workspace/_out/cozyreport}/snapshots/${COZY_SNAPSHOT_NAME:-kubernetes}/tenant-thread-cpu/sample-${sample}"
  local pods pod rc raw pod_err probe read_at read_done
  local seen=0
  # The pool's declared minimum is two workers, and the listing below counts
  # only the Running ones, so this covers that pool scaled once over rather than
  # a sample of it. Raising it is not free, and at today's knobs it is not
  # possible: this walk is taken twice, the budget derivation in
  # hack/run-kubernetes-node-join_test.bats holds the whole sampling group
  # against the room the tenant snapshot needs behind it, and the next value up
  # fails that guard. Read the current margin off the guard, which computes both
  # sides, rather than off a number written here.
  local max_pods=4

  mkdir -p "${report_dir}"
  # Re-validated here for the reason every collector re-validates them: a value
  # assigned after this file is sourced never passed the assignment-time check,
  # and zero reaches `timeout` as no bound at all.
  COZY_DIAG_READ_TIMEOUT=$(_cozy_diag_seconds "${COZY_DIAG_READ_TIMEOUT-}" "$COZY_DIAG_READ_TIMEOUT_DEFAULT" COZY_DIAG_READ_TIMEOUT positive)
  COZY_DIAG_READ_GRACE=$(_cozy_diag_seconds "${COZY_DIAG_READ_GRACE-}" "$COZY_DIAG_READ_GRACE_DEFAULT" COZY_DIAG_READ_GRACE)

  # Running only, which the sibling caller does not ask for and does not need:
  # it reduces its answer to node names, so a launcher left over from an earlier
  # attempt costs it nothing. Here each Pod costs a bounded exec and a slot under
  # the cap, and a Pod that is not Running has no container to exec into at all,
  # so three dead launchers could push a live worker out of the walk and spend
  # the budget on tombstones. Filtered in the query rather than after it: a Pod
  # dropped here was never counted.
  pods=$(_cozy_virt_launcher_listing "${report_dir}" 'per-thread CPU time capture' \
    '{range .items[?(@.status.phase=="Running")]}{.metadata.name}{"\n"}{end}' \
    'no Running virt-launcher Pod in namespace tenant-test; a Pod that is not Running has no compute container to read threads from, and that it is not Running is itself a finding about this failure') || return 1

  probe=$(_cozy_thread_cpu_probe)

  for pod in ${pods}; do
    seen=$((seen + 1))
    if [ "${seen}" -gt "${max_pods}" ]; then
      echo "--- worker per-thread CPU capture stopped at ${max_pods} Pods ---"
      printf 'capture stopped after %s Pods; %s matched in total\n' \
        "${max_pods}" "$(printf '%s\n' "${pods}" | wc -l | tr -d ' ')" \
        >"${report_dir}/COLLECTION-TRUNCATED.txt"
      break
    fi
    echo "--- capturing worker per-thread CPU time: ${pod} ---"
    raw="${report_dir}/${pod}.txt"
    pod_err="${report_dir}/${pod}.exec-error.log"
    rc=0
    # Stamped on both sides of the read, which is what makes a pair of captures
    # subtractable. One stamp would leave the sampling instant somewhere inside
    # a read whose duration is exactly what goes wrong on the run this collector
    # exists for; on a healthy read the two are the same second.
    read_at=$(date -u +%s)
    # Guarded the way the other collectors in this pair guard it: without the
    # guard a runner with no `timeout` would exit 127 here and leave an empty
    # directory, which is the one outcome this collector may not produce.
    # stdin closed explicitly: `sh -c` inherits it, and this walk shares its
    # stdin with the caller.
    if command -v timeout >/dev/null 2>&1; then
      timeout -k "${COZY_DIAG_READ_GRACE}" "${COZY_DIAG_READ_TIMEOUT}" \
        kubectl -n tenant-test exec "${pod}" -c compute -- \
        sh -c "${probe}" >"${raw}" 2>"${pod_err}" </dev/null || rc=$?
    else
      kubectl -n tenant-test exec "${pod}" -c compute -- \
        sh -c "${probe}" >"${raw}" 2>"${pod_err}" </dev/null || rc=$?
    fi
    read_done=$(date -u +%s)
    if [ ! -s "${pod_err}" ]; then
      rm -f "${pod_err}"
      pod_err=
    elif [ "${rc}" -eq 0 ]; then
      mv "${pod_err}" "${report_dir}/${pod}.READ-WARNINGS.txt" 2>/dev/null || true
      pod_err=
    fi
    # Tested before anything is appended, or nothing is ever empty. Which arm
    # fires is decided by the STATUS and never by whether anything landed on
    # stderr: `timeout` kills its child without a word and kubectl dies on
    # SIGTERM the same way, so the dominant failure here -- a container on a
    # wedged node -- arrives non-zero with an empty error log, and keyed on
    # stderr the artifact would state the opposite of what happened.
    #
    # An empty file is the dangerous outcome rather than a neutral one. Every
    # thread of a stalled guest still accumulates a stat line, so zero bytes
    # here reads as a guest whose threads were all idle -- which is one of the
    # two readings this collector was added to tell apart, arrived at by
    # default.
    if [ ! -s "${raw}" ] && [ "${rc}" -ne 0 ]; then
      if [ -n "${pod_err}" ]; then
        printf '%s\n' \
          'the thread split is unknown: the compute container was not reached; see the exec-error.log beside this file, named for the same Pod' \
          >>"${raw}"
      else
        printf '%s\n' \
          'the thread split is unknown: the compute container was not reached, and the read died without a word on either stream' \
          >>"${raw}"
      fi
    elif [ ! -s "${raw}" ]; then
      printf '%s\n' \
        'the thread split is unknown: the compute container answered and returned nothing, which a shell that ran cannot produce -- the probe prints a line before it looks for a process' \
        >>"${raw}"
    elif [ "${rc}" -ne 0 ]; then
      printf '%s\n' \
        'these thread counters are incomplete: the read was cut short part way through the thread list, so the threads missing from it are the ones listed last rather than an arbitrary subset' \
        >>"${raw}"
    elif grep -q '^NO-QEMU-PROCESS:' "${raw}"; then
      # A clean read that found nothing to read, which on this path is a
      # finding: the guest's QEMU has already exited. Kept apart from the legend
      # below because that legend describes stat lines, and this file holds
      # none -- an explanation of a column layout over a file without columns is
      # the same overclaim as a pairing instruction over a partial read.
      printf '%s\n' \
        'there is nothing to split: the container answered and carried no QEMU process, so this Pod contributes no thread counters to the pair' \
        >>"${raw}"
    elif grep -q '^NO-THREAD-LINES:' "${raw}"; then
      # A heading and nothing under it. The file is not empty, so none of the
      # arms above fire, and the legend below would explain a column layout the
      # capture does not carry -- which is why the probe says this rather than
      # leaving it to be inferred from a line count nobody takes.
      printf '%s\n' \
        'the thread split is unknown: QEMU was still named when the probe looked and its threads were gone before they could be read, so this Pod contributes no thread counters to the pair' \
        >>"${raw}"
    elif grep -q '^NO-PROC-READ:' "${raw}"; then
      # Not the arm above, and the difference is the whole point of the probe
      # counting what it read. That one is a statement about the guest; this one
      # is a statement about the probe, and reading it as the guest's would put
      # "QEMU had already exited" in the artifact on the strength of a proc
      # mount nobody could open.
      printf '%s\n' \
        'the thread split is unknown: the shell ran and could not read a single process, so nothing here is a reading about what the guest was doing' \
        >>"${raw}"
    else
      printf '%s\n' \
        '[each line above beginning with a number is /proc/<pid>/task/<tid>/stat verbatim: the first field is the thread id, the second is the thread name in parentheses, utime is the 14th field and stime the 15th]' \
        '[counting those fields over whitespace reads the wrong column for exactly the threads this capture exists to identify: a KVM vCPU thread is named CPU 0/KVM, that space sits inside the parentheses, and every field after it shifts by one. Parse from the last ) in the line instead, after which the state is the first field, utime the 12th and stime the 13th]' \
        "[the names: CPU N/KVM is the guest's vCPU N, the thread whose id equals the process id is QEMU's main thread and its emulator thread, and the rest are QEMU's own IO and worker threads, which carry the process name unless QEMU renamed them]" \
        '[utime and stime are clock ticks, at the rate the first line above reports, and both are cumulative since the thread started: subtract this Pod file from its sibling under the other sample directory, and the stamps below from each other, to get a rate per thread]' \
        >>"${raw}"
    fi
    # kubectl exits 1 for a refused connection as readily as for a container
    # that is gone, so the status alone names none of them and the error log is
    # the only thing that can.
    if [ "${rc}" -eq 124 ]; then
      printf '%s\n' \
        '[exit 124: this collector timing out and the read exiting 124 on its own cannot be told apart]' \
        >>"${raw}"
    fi
    # 128+SIGKILL. The grace signal produces it, and so does anything else that
    # kills the read, so the note says what the status establishes and stops
    # there.
    if [ "${rc}" -eq 137 ]; then
      printf '%s\n' \
        '[exit 137: the read was killed rather than stopping on its own; the status does not say what killed it]' \
        >>"${raw}"
    fi
    printf '[read attempted from %s to %s epoch seconds]\n' \
      "${read_at}" "${read_done}" >>"${raw}"
    printf '\n[capture exit code: %s]\n' "${rc}" >>"${raw}"
  done
}

# Two properties of the network family are invisible to anyone writing this
# filter from the metric documentation, and both fail by capturing nothing:
#
#   - The dropped-packet families are spelled packets_dropped. There is no
#     *_drops_total, which is the spelling the shorter name suggests.
#   - Every row carries container="", including the per-Pod ones. cAdvisor
#     publishes network counters on the Pod's sandbox cgroup, so the Pod is
#     named by `pod` and the container label is empty on the whole family. A
#     filter borrowed from the CPU capture above, whose rows do carry a
#     container name, therefore matches nothing at all -- and nothing at all
#     reads as a worker that moved no bytes.
#
# The node's own interfaces arrive in these same families with pod="" and
# namespace="", which the namespace stage already excludes; worth knowing before
# that stage looks redundant next to the Pod-name one.
cozy_capture_tenant_worker_network_counters() {
  _cozy_capture_worker_cadvisor \
    tenant-network-counters \
    'network counters capture' \
    'how many bytes reached these workers' \
    network \
    cozy-net-counters \
    _cozy_network_counters_tail_note \
    '^container_network_(receive|transmit)_(bytes|packets|packets_dropped|errors)_total\{'
}

# How to read the device label, which is the one sentence here that holds on a
# partial file as well as on a whole one. A function because more than one arm of
# the note below prints it and a second copy is a scheduled divergence.
_cozy_block_io_device_legend() {
  printf '%s\n' \
    '[the device labels here name HOST devices, and which of them backs the guest system disk is decided by the major rather than by the name: the kernel registers block major 147 for DRBD, so a container_blkio_device_usage_total row at that major with operation="Read" is the replicated volume the guest reads its image from]' \
    >>"$1"
}

# Said in the file because which families arrive is decided by the kernel
# interface under them, and a family's absence is not a reading about the disk.
_cozy_block_io_tail_note() {
  local raw="$1" rc="$2" filter_rc="$3" had_series="$4"
  local start_rc=0 counter_rc=0

  [ "${had_series}" -eq 1 ] || return 0
  # Every sentence that reads the file AS A WHOLE reading is withheld from a read
  # that did not finish, the way the CPU note withholds its pairing sentence and
  # the sandbox capture withholds its column legend. On a truncated capture what
  # is here is a prefix: a total over the container's age understates it by
  # whatever never arrived, and a family missing from a prefix says nothing about
  # the kernel interface that would explain its absence.
  #
  # The device legend is not one of those and stays: it explains how to read a
  # label on the rows that did arrive, which is as true of a prefix as of a whole
  # file, and a reader is most in need of it exactly when the file is short.
  if [ "${rc}" -ne 0 ] || [ "${filter_rc}" -ge 2 ]; then
    printf '%s\n' \
      'these counters are cumulative since the container started, and this capture is marked incomplete above, so what is here may be a prefix of what the kubelet had: no averaging instruction and no reading of a missing family follows, because both would treat a prefix as the whole' \
      >>"${raw}"
    _cozy_block_io_device_legend "${raw}"
    return 0
  fi
  # The divisor family is not a counter, and keeping it in the filter costs this
  # sentence: the shared walk decides "the kubelet reported nothing for this
  # subject" from whether ANY row survived, so a container cAdvisor knows about
  # puts its start time in the file whether or not a single disk row arrived, and
  # the walk's own "no series" arm can no longer fire. Without this the file would
  # carry a start time, an instruction for dividing counters, and no counters --
  # which is the reading this capture exists to make impossible. Decided on the
  # subject families rather than on the row count, and on grep's exact status for
  # the reason the arms below are.
  grep -qE '^container_(blkio_device_usage_total|fs_|pressure_io_)' "${raw}" || counter_rc=$?
  if [ "${counter_rc}" -eq 1 ]; then
    printf '%s\n' \
      'no block IO counter reached this file: what is above is a container start time, which cAdvisor publishes for any container it knows and which this capture keeps as the divisor for counters that did arrive. So the kubelet answered and reported no block IO for a tenant-test worker on this node -- not a worker that touched no disk' \
      >>"${raw}"
    return 0
  fi
  if [ "${counter_rc}" -ne 0 ]; then
    printf '%s\n' \
      'whether any block IO counter reached this file could not be checked: the search for the subject families failed on this runner rather than answering, so read the rows above before taking any sentence here as a statement about the disk' \
      >>"${raw}"
    _cozy_block_io_device_legend "${raw}"
    return 0
  fi
  # Totals, and the note says so rather than calling them an average: an average
  # needs a divisor, and the only reason this file can offer one is that the
  # start-time row is kept in the filter.
  #
  # Two arms rather than the three the checks above take, because the check above
  # already read this file: reaching here means grep returned 0 or 1 on it, so a
  # third arm for "could not read it" would be unreachable by construction.
  grep -q '^container_start_time_seconds{' "${raw}" || start_rc=$?
  if [ "${start_rc}" -eq 0 ]; then
    printf '%s\n' \
      "these counters are cumulative since the container started and this is one reading of them, so what is above are totals and not a rate. container_start_time_seconds above is the divisor, one row per container rather than one per node, so take the row whose container and pod labels match the counters being divided: a worker Pod's container starts with the guest it carries and KubeVirt gives that Pod restartPolicy Never, so the container is never restarted under the guest, the read stamp below minus that start is the guest's own life, and a total over that span is the average across exactly the window the node-join deadline covers. A rate INSIDE that window needs a second capture of the same stream, which this collector does not take" \
      >>"${raw}"
  else
    printf '%s\n' \
      'these counters are cumulative since the container started and this is one reading of them, so what is above are totals and not a rate. The divisor is missing from this file: container_start_time_seconds carries it and no row of it survived here, so the container Started line in the virt-launcher describe dump is where the age has to come from' \
      >>"${raw}"
  fi
  printf '%s\n' \
    "[container_pressure_io_stalled_seconds_total and container_pressure_io_waiting_seconds_total, where they appear, answer this collector's question directly, since they count the time the group spent stalled on IO rather than the bytes it moved. Their absence is a setting rather than a quiet disk: cAdvisor publishes these families only when the kubelet turns its pressure metrics on, which sits behind a feature gate. The kernel is not the missing half on these nodes, since the sandbox kernel ships pressure accounting enabled]" \
    >>"${raw}"
  _cozy_block_io_device_legend "${raw}"
  printf '%s\n' \
    '[a missing service time or queue depth here is the kernel interface and not an idle disk: under cgroup v2 io.stat publishes transferred bytes and completed operations only, so container_fs_io_current and container_fs_io_time_seconds_total are either absent or carry the node filesystem counters cAdvisor falls back to, which are not this Pod]' \
    >>"${raw}"
}

# Read how much each tenant worker moved through its block devices, from the
# kubelet's cAdvisor endpoint on the node the worker runs on.
#
# This exists because a worker burning its whole vCPU while making no progress
# has two mechanisms behind one appearance. It is computing and getting nowhere,
# or it is waiting on blocks -- and a vCPU thread spinning in a wait on a nested
# guest with no idle exit looks like a busy vCPU either way, at a quota with room
# to spare and with the node under it half free. Nothing else in this tree counts
# the worker's IO: the CPU counters beside it measure what the group was allowed
# and what it got, the network counters measure what crossed into the Pod, and
# both are blind to blocks by construction.
#
# The guest's own /proc/diskstats would answer it from inside, and cannot be
# reached: the diagnostics credential minted for the guest carries the os:reader
# role, which Talos does not allow to read file contents at all, and Talos
# publishes no resource carrying disk statistics. The Pod's cgroup is the nearest
# surface that exists, and it sits outside the guest, so a wedged guest does not
# slow the reading down.
#
# Read once rather than twice, unlike its neighbours in the sampling loop, and
# the counter's own origin is why: it accumulates from the container starting,
# which for a worker Pod is when the guest was created. A single reading is
# therefore an average over the guest's whole life, and the guest's whole life IS
# the window the deadline covers on this failure. What one reading cannot give is
# a rate inside that window, which is what would separate a guest reading slowly
# throughout from one that read its image and then burned CPU; the file says so
# where the rows are.
cozy_capture_tenant_worker_block_io() {
  # Anchored on the metric names, and the namespace is not a worker filter: the
  # tenant-test namespace also carries the Kamaji control plane, whose apiserver
  # and etcd have disks of their own, so the Pod name carries the second half of
  # the filter as it does for the sibling captures.
  #
  # container_blkio_device_usage_total is the row that survives cgroup v2 with an
  # operation and a device major on it, so it is the one that attributes reads to
  # a volume; the fs_* families are kept beside it because a node that publishes
  # the service-time ones must not produce an artifact identical to a node that
  # cannot.
  #
  # container_start_time_seconds is in the filter for a different reason from
  # every other family here: it is not a counter but the divisor these counters
  # need. cAdvisor publishes it for any container it knows, outside the disk
  # group, and keeping it is what lets one reading of a cumulative total be read
  # as an average without leaving the file for a Started line in a describe dump.
  #
  # It costs two things, both paid rather than left implicit. The subject word
  # below names it, because the shared walk builds its "reported no <subject>
  # series" sentences from that word and a sentence naming only block IO would be
  # false about a file holding a start time. And the tail note decides "no
  # counter arrived" on the subject families rather than on the walk's row count,
  # which this family would otherwise make always true.
  _cozy_capture_worker_cadvisor \
    tenant-block-io \
    'block IO counters capture' \
    'how much these workers moved through their disks' \
    'block IO or container start-time' \
    cozy-block-io \
    _cozy_block_io_tail_note \
    '^container_(blkio_device_usage_total|start_time_seconds|fs_((reads|writes)(_bytes)?_total|(read|write)_seconds_total|io_(current|time_seconds_total|time_weighted_seconds_total))|pressure_io_(stalled|waiting)_seconds_total)\{'
}

# Collect the guest-side evidence requested by issue #3513. This runs only after
# the 18-minute Ready deadline has already failed. VMIs without a reported IP
# are recorded before any Certificate or Pod is created; when at least one IP is
# available, setup and each Talos command remain bounded. It runs before slower
# cache diagnostics so the requested guest evidence lands first.
cozy_capture_tenant_talos() (
  local test_name="$1"
  local report_dir="${COZY_REPORT_DIR:-/workspace/_out/cozyreport}/snapshots/${COZY_SNAPSHOT_NAME:-kubernetes}/tenant-talos"
  local selector="cluster.x-k8s.io/cluster-name=kubernetes-${test_name},cluster.x-k8s.io/role=worker"
  local pod_name="kubernetes-${test_name}-talos-diagnostics"
  local workdir vmi_name node_ip
  local has_node_ip=false

  umask 077
  workdir=$(mktemp -d) || return 1
  chmod 700 "${workdir}"
  trap 'rm -rf "${workdir}"' EXIT
  mkdir -p "${report_dir}"

  if ! kubectl -n tenant-test get virtualmachineinstances.kubevirt.io \
    -l "${selector}" -o json --request-timeout=30s >"${report_dir}/vmis.json" \
    2>"${report_dir}/vmis-error.log"; then
    echo "failed to list tenant worker VMIs for Talos diagnostics" >&2
    return 1
  fi
  cozy_tenant_worker_vmi_rows <"${report_dir}/vmis.json" >"${workdir}/vmis.rows"
  if [ ! -s "${workdir}/vmis.rows" ]; then
    echo "no tenant worker VMIs found for Talos diagnostics" >&2
    printf 'no tenant worker VMIs matched selector %s\n' "${selector}" \
      >"${report_dir}/setup-error.log"
    return 1
  fi

  # Record missing addresses before spending time on Certificate issuance or a
  # helper Pod. If no worker booted far enough for KubeVirt to report an IP,
  # guest-side Talos is unreachable and setup cannot produce more evidence.
  while IFS='|' read -r vmi_name node_ip; do
    if [ -z "${node_ip}" ]; then
      mkdir -p "${report_dir}/${vmi_name}"
      printf '%s\n' 'default VMI interface has no reported IP address' \
        >"${report_dir}/${vmi_name}/capture-error.log"
      continue
    fi
    has_node_ip=true
  done <"${workdir}/vmis.rows"
  if [ "${has_node_ip}" != true ]; then
    echo "no tenant worker VMI has a reported IP for Talos diagnostics" >&2
    printf '%s\n' 'no tenant worker VMI has a reported IP for Talos diagnostics' \
      >"${report_dir}/setup-error.log"
    return 1
  fi

  if ! cozy_prepare_tenant_talosconfig "${test_name}" "${workdir}"; then
    echo "failed to create short-lived tenant Talos reader config" >&2
    printf '%s\n' 'failed to create short-lived tenant Talos reader config' \
      >"${report_dir}/setup-error.log"
    return 1
  fi
  if ! cozy_prepare_tenant_talos_diagnostics_pod \
    "${test_name}" "${workdir}/talosconfig"; then
    echo "failed to prepare tenant Talos diagnostics Pod" >&2
    printf '%s\n' 'failed to prepare tenant Talos diagnostics Pod' \
      >"${report_dir}/setup-error.log"
    return 1
  fi

  while IFS='|' read -r vmi_name node_ip; do
    [ -n "${node_ip}" ] || continue
    echo "--- capturing tenant Talos dmesg/kubelet/services/links: ${vmi_name} (${node_ip}) ---"
    cozy_capture_tenant_talos_node "${pod_name}" "${node_ip}" \
      "${report_dir}/${vmi_name}"
  done <"${workdir}/vmis.rows"
)

# _cozy_diag_seconds <value> <default> <name>: echo <value> when it is a bare
# non-negative integer, else echo <default> and say on stderr that the value was
# rejected.
#
# The rejection names no unit, because three of its four callers are seconds and the
# fourth is a Pod count: telling someone their importer cap must be "integer seconds"
# describes the wrong quantity, and the whole point of these notes is that they say
# something true about what was seen.
#
# Every knob below is pasted somewhere that accepts digits and nothing else, and
# each fails differently and quietly:
#
#   COZY_DIAG_PHASE_BUDGET=8m  -- goes into $(( )) inside cozy_diag_phase_start,
#     which is the FIRST statement of the diagnostics block. The arithmetic error
#     unwinds the whole function before its own headline, so the failing run
#     produces no diagnostics at all, only the shell's complaint. The worst
#     outcome on this path, arrived at through a knob.
#   COZY_DIAG_READ_TIMEOUT=2m  -- becomes --request-timeout=2ms and every read
#     fails instantly, so every note blames the cluster for a value.
#   COZY_DIAG_MAX_IMPORTERS=3s -- `[ n -gt 3s ]` exits 2, the `if` reads false and
#     the cap silently stops existing, which is the term this block capped on
#     purpose.
#
# A leading zero is rejected along with a suffix, and it is the arm that is easy to
# leave out. `0480` is all digits, so a digits-only check passes it through, and
# then `$(( ))` reads it as octal and fails exactly as `8m` does -- same total loss
# of the block, one character less obvious. For the read bound the same value is
# quieter and worse: `timeout -k 5 0480` is 480 seconds, so the per-call bound this
# whole change exists to add would be 24 times wider than the number says.
#
# Rejecting and naming the fallback is what the rest of this tree does with its own
# knobs, including this arm: hack/e2e-capture-previous-logs.sh rejects a zero or
# leading-zero COZY_PREVLOG_TAIL for the same octal reason, and
# docs/agents/e2e-testing.md records that contract.
# A fourth argument, any non-empty value, rejects zero as well; it is a flag rather
# than a bound, and the call site spells it `positive`. Zero is all digits and
# passes every check above, and `timeout -k 5 0` disables the timeout outright while
# `--request-timeout=0s` means "no timeout" to kubectl -- the defect this change
# exists to remove, reachable through the knob it adds. The phase budget must keep
# accepting zero: the suite uses it to mean "already spent".
#
# A fifth argument replaces the parenthetical that says why zero is refused, and it
# exists because the flag now has more than one caller and they refuse zero for
# different reasons. For a bound, zero removes the ceiling. For the sampling
# interval it removes nothing and leaves two readings taken at the same instant --
# a pair that looks collected and divides to nothing. One sentence covering both
# would have to say neither.
_cozy_diag_seconds() {
  if [ -n "${4:-}" ] && [ "${1}" = 0 ]; then
    echo "» WARNING: ignoring ${3}='${1}' (${5:-zero disables the bound instead of tightening it}); using ${2}" >&2
    printf '%s\n' "${2}"
    return 0
  fi
  case "${1}" in
    '' | *[!0-9]*)
      echo "» WARNING: ignoring ${3}='${1}' (a bare integer, no unit suffix); using ${2}" >&2
      printf '%s\n' "${2}"
      ;;
    0?*)
      echo "» WARNING: ignoring ${3}='${1}' (a leading zero is read as octal in arithmetic and as decimal elsewhere); using ${2}" >&2
      printf '%s\n' "${2}"
      ;;
    *) printf '%s\n' "${1}" ;;
  esac
}

# Per-read wall-clock bound for the on-failure diagnostics in
# run_kubernetes_test, and the kill grace that follows it. Named once so a note
# reporting a cut-off cannot quote a number the read never used, and overridable
# so a test does not have to wait out the real one.
#
# Lower than the bound most of the collectors in this file use, and the difference is
# count rather than confidence: those bound a single read or a short walk, while the
# block below issues a dozen back to back, so the same per-read bound would put the
# block's own ceiling ahead of the tenant crust-gather snapshot it exists to reach.
# The worker counter captures are the exception and deliberately so: they
# share one body that is a short walk by that measure, and it still takes this
# knob rather than a higher literal, because a bound that does not follow the
# knob is a bound nobody can lower. Read the sentence above as describing the
# collectors that predate the knob, not as a rule for new ones.
# Every read here is one get/describe/logs against a small tenant or a handful of
# cluster-scoped objects, which an apiserver that answers at all answers in under a
# second.
#
# Integer seconds, no unit suffix. `timeout` would accept `2m`, but the same value
# is pasted into `--request-timeout=${...}s`, where `2m` becomes `2ms` and every
# read fails instantly -- a note about the cluster manufactured by a knob.
# Each default is named once. Passed as a literal at every call site instead, a
# changed default would leave the re-checks below still falling back to the old
# number -- and only on the error path, where it is hardest to notice.
COZY_DIAG_READ_TIMEOUT_DEFAULT=20
COZY_DIAG_READ_GRACE_DEFAULT=5
COZY_DIAG_MAX_IMPORTERS_DEFAULT=3
# The wait between the two passes over the counters that only mean something as
# a pair. Every counter those two collectors read is cumulative, so a single
# reading divides out to an average over the container's whole uptime and says
# nothing about the window the deadline covered; the difference between two
# readings does.
#
# It is the WAIT and not the interval those readings span: each pass also walks
# its nodes, and the other subject's pass falls between a subject's two
# readings. The captures stamp themselves for that reason, and a rate is
# computed from the stamps rather than from this number.
#
# Short on purpose, and this is the one number here chosen for what it measures
# rather than for what it costs. The profile being separated is a guest that
# freezes for tens of seconds and then runs flat out, and a window long enough
# to contain both halves averages them back into the figure that could not tell
# them apart in the first place. A window of this size lands inside one half or
# the other, which is what makes the pair a discriminator rather than a second
# copy of the average.
#
# It is also the whole wall-clock cost this pair adds to a failing run, since
# the two collectors share it: both take their first reading, the interval
# passes once, and both take their second.
COZY_DIAG_RATE_INTERVAL_DEFAULT=12
# What the diagnostics phase may spend. A whole number of minutes, because the
# guard in hack/run-kubernetes-node-join_test.bats holds it against the figure
# both kubernetes-*/chainsaw-test.yaml state in minutes, and a budget that is
# not one leaves that statement rounded rather than true.
#
# Two bounds fix it, and both are held in that file rather than here.
#
# From below: the collectors gated ahead of the serial console must not be able
# to spend the whole phase between them at their own ceilings. What sits ahead
# of the console bounds what the console can lose, and a budget under their sum
# turns "cut short" into "never started" for the one surface that survives a
# worker which never reached apid.
#
# From above: this budget, one overshoot by the heaviest collector, and the
# tenant crust-gather snapshot all have to fit in what the operation has left
# once the bringup ahead of them is spent. That is the inequality the number
# answers to, and raising the budget without raising the operation moves the
# snapshot past the end of the window.
#
# What a budget of this size gives up first is what is gated last --
# ghcr_mirror_diagnose and talos_image_cache_diagnose. Neither loses its subject
# entirely: the mirror's state is partly recoverable from the request counts and
# response durations the mirror capture records against its own log, and from
# the kube-system snapshot the host report takes. The image cache's re-probe has
# no substitute, and it is the collector this budget gives up first.
#
# Only part of what runs ahead of the console is held mechanically, and the
# boundary is worth naming rather than leaving a reader to trust the whole of
# it: the guard walks the collectors gated ahead of the console -- the ones
# called as `cozy_… || true` -- and fails on any it has no cost arm for, so a
# walk added there is priced before the suite goes green. A bounded read added
# ahead of the console, as (a3) is, is not in that enumeration and its 25s are
# not summed there. That gap is recorded where the other budget residuals are
# rather than fixed here.
#
# What is deliberately NOT ahead of the console is the two-sample CPU pair,
# whose own ceiling is several times this one; the ordering argument at the pair
# says the rest.
COZY_DIAG_PHASE_BUDGET_DEFAULT=480

# The operation both kubernetes-*/chainsaw-test.yaml give the script, and the
# room the passing-path console baseline needs inside it. Constants rather than
# knobs: neither is a budget a caller tunes, they are a restatement of a ceiling
# that lives in the suite files, and the guard in
# hack/run-kubernetes-serial-console_test.bats reads both sides so a suite that
# changes its `timeout:` fails there rather than silently leaving the reserve
# measured against a ceiling that moved.
#
# The reserve is the capture's own cost at the cap that caller asks for -- a
# listing, two console reads, the two Pod-state reads a silent console triggers
# and the start-time read, six at the 30s bound and 5s kill grace, so 210s --
# plus a minute. It buys one thing: a run that is already near the ceiling
# declines the baseline instead of being killed while collecting it, because a
# suite that proved everything it exists to prove must not go red over a
# debugging aid.
#
# The extra minute is slack, not a bound on what follows. The teardown between
# this capture and the end of the function carries no wall-clock ceiling of its
# own -- `--wait=false` bounds what the deletes wait for, not how long the
# request itself may hang -- so no finite reserve makes the tail of a run fit by
# arithmetic. That is pre-existing and tracked in cozystack/cozystack#3666; what
# the reserve bounds is this capture's own contribution to the ceiling, which is
# the part this file added.
COZY_OP_CEILING=4020
COZY_GREEN_CAPTURE_RESERVE=270
# Zero until run_kubernetes_test stamps it. Declared here so the arithmetic in
# cozy_green_capture_has_room cannot meet an unbound name: under the `set -eu`
# the chainsaw script runs with, that aborts the whole function, which is the
# very outcome -- a passing run ending red -- the reserve exists to prevent. A
# zero left in place fails closed rather than quietly: the elapsed time it
# implies is the whole epoch, so the capture is declined and says so.
_COZY_RUN_STARTED_AT=0
COZY_DIAG_READ_TIMEOUT=$(_cozy_diag_seconds "${COZY_DIAG_READ_TIMEOUT:-$COZY_DIAG_READ_TIMEOUT_DEFAULT}" "$COZY_DIAG_READ_TIMEOUT_DEFAULT" COZY_DIAG_READ_TIMEOUT positive)
# Validated too, though a suffix is not what breaks it: `timeout -k abc 20` exits 125
# before running the command at all, so a non-numeric grace makes every read in the
# block report exit 125 and collects nothing -- the same total loss the other knobs
# were validated against. Zero is allowed here: `-k 0` only skips the follow-up
# SIGKILL, which a TERM-respecting child like kubectl does not need.
COZY_DIAG_READ_GRACE=$(_cozy_diag_seconds "${COZY_DIAG_READ_GRACE:-$COZY_DIAG_READ_GRACE_DEFAULT}" "$COZY_DIAG_READ_GRACE_DEFAULT" COZY_DIAG_READ_GRACE)
# How many CDI importer Pods get their logs walked. Each costs two reads, so an
# uncapped walk is a term the cluster sizes rather than this file, inside a block
# that has to finish. The default covers the pool at its minimum with room to spare,
# and a walk that hits the cap says so with both counts rather than leaving the
# shortfall implicit.
COZY_DIAG_MAX_IMPORTERS=$(_cozy_diag_seconds "${COZY_DIAG_MAX_IMPORTERS:-$COZY_DIAG_MAX_IMPORTERS_DEFAULT}" "$COZY_DIAG_MAX_IMPORTERS_DEFAULT" COZY_DIAG_MAX_IMPORTERS)
# Rejected as `positive` for a reason the other bounds do not have: zero here is
# not a disabled bound but two readings taken at the same instant, which divide
# to nothing and leave a pair that looks collected and answers no question.
COZY_DIAG_RATE_INTERVAL=$(_cozy_diag_seconds "${COZY_DIAG_RATE_INTERVAL:-$COZY_DIAG_RATE_INTERVAL_DEFAULT}" "$COZY_DIAG_RATE_INTERVAL_DEFAULT" COZY_DIAG_RATE_INTERVAL positive "zero puts both readings at the same instant, so the pair divides to nothing")
# The canary ceiling takes the same source-time pass as its neighbours, and not
# only for symmetry: the collector re-validates with `${COZY_CANARY_RUN_BOUND-}`,
# so without this line a run that never set the knob hands the validator an
# empty string, and the job log opens with a warning about ignoring a value
# nobody supplied. Rejected as `positive` because zero reaches `timeout` as no
# bound at all.
COZY_CANARY_RUN_BOUND=$(_cozy_diag_seconds "${COZY_CANARY_RUN_BOUND:-$COZY_CANARY_RUN_BOUND_DEFAULT}" "$COZY_CANARY_RUN_BOUND_DEFAULT" COZY_CANARY_RUN_BOUND positive "zero is no ceiling at all, and an arm that never returns costs the phase whatever is gated behind it")

# Wall-clock budget for the on-failure diagnostics phase as a whole, on top of the
# per-read bounds above, because the two buy different things. A per-read bound
# buys forward progress: one hung call can no longer eat the step. It does not buy
# an end to the phase, and the collectors on this path can together spend more than
# what is left of the op by the time it runs. Bounding every read and stopping
# there would have made the arithmetic in kubernetes-*/chainsaw-test.yaml legible
# and still false.
#
# So the phase is bounded as a phase. Once the budget is spent, the collectors that
# have not started are declined out loud instead of attempted, which keeps the one
# artifact that must survive reachable: the tenant crust-gather snapshot the
# caller's `exit 1` triggers.
#
# The budget is derived rather than chosen, from
#
#   budget + largest overshoot + snapshot <= op - bringup
#
# Every term but the budget belongs to something else: the op ceiling lives in both
# kubernetes-*/chainsaw-test.yaml, the snapshot's bound is on the crust-gather call
# above, and bringup is an observed figure rather than a ceiling. The overshoot
# term exists because admission gates when a collector may START, not when it ends,
# so one admitted a moment before the deadline still runs its whole cost. The
# default below is the largest whole minute under the bound that inequality gives.
# hack/run-kubernetes-node-join_test.bats holds the inequality against the code, so
# a later change cannot raise the budget past what the collectors leave room for.
#
# `largest` is a literal in that guard, taken from the heaviest collector at the
# pool's MINIMUM size, which makes it a floor and not a ceiling: the guest-Talos
# walk carries no cap, so a bigger pool makes that collector cost more than the
# figure the budget was derived against. Nor does it cover the collectors with no
# wall-clock bound at all. On the guest-Talos path the applies, waits and deletes
# carry `--request-timeout`/`--timeout`, which bounds one HTTP request rather than
# a client retrying against a wedged apiserver, and the image-cache re-probe makes
# unbounded calls of its own. Both are tracked in cozystack/cozystack#3666. So no
# worst case is claimed here, and none can be stated while that holds.
#
# What the budget buys, exactly and only, is that no collector is STARTED once it
# is spent, so the snapshot is never queued behind work begun past the deadline.
# That is how the snapshot was being lost.
#
# Admission is "the budget is not spent yet" rather than "this collector fits in
# what is left", and that is a choice. Sizing admission per collector would hold
# the phase to its budget exactly, and would also decline the guest-Talos capture
# on a merely slow run, throwing away the only in-guest evidence to protect a
# snapshot that was not yet in danger. Start-gating leaves that to the cheap-first
# order below, and the image-cache diagnosis is the collector this budget gives up
# first, which is worth knowing before its dumps are relied on.
COZY_DIAG_PHASE_BUDGET=$(_cozy_diag_seconds "${COZY_DIAG_PHASE_BUDGET:-$COZY_DIAG_PHASE_BUDGET_DEFAULT}" "$COZY_DIAG_PHASE_BUDGET_DEFAULT" COZY_DIAG_PHASE_BUDGET)
# Zero until a phase opens, which is how the scheduling-gate branch -- two reads,
# no phase -- stays ungated.
_COZY_DIAG_PHASE_DEADLINE=0

# cozy_diag_phase_start: open the diagnostics phase and stamp its deadline.
#
# There is no closing counterpart, because both callers end in `exit 1` a few
# lines later and a function that only ever runs before an exit does not need
# one. A future caller that opens a phase and then carries on would gate every
# later read on a spent deadline, and would need to clear it.
cozy_diag_phase_start() {
  # Said once, here, rather than letting a dozen notes imply it, and said narrowly
  # because the phase is not uniform. The reads that go through cozy_diag_read, the
  # importer listing and the image-cache helper fall back to running unbounded, since
  # bounding with a binary that is not there would turn each of them into an exit 127
  # and each note into "read failed" -- a missing local dependency reported as the
  # cluster refusing. Unbounded is what this path had before any of this, so a partial
  # capture beats none, and what it costs is the guarantee.
  #
  # The collectors that call `timeout` directly -- the wedge check below, the
  # serial-console family and the guest-Talos capture -- have no such fallback and do
  # exit 127, collecting nothing. That is pre-existing and tracked in
  # cozystack/cozystack#3666; the warning names both halves rather than promising the
  # better one for all of them.
  #
  # A third group belongs to neither half: those collectors call `timeout`
  # directly AND carry the same fallback, so on a runner without the binary they
  # run unbounded rather than exiting 127. That is the opposite failure from the
  # one the sentence above would lead a reader to, and it is the half with the
  # snapshot behind it, so the warning below names them one at a time. Only
  # there: that list is derived from the source by
  # hack/run-kubernetes-node-join_test.bats, and a second copy up here would be
  # an unchecked list sitting beside a checked one, drifting from it at whatever
  # rate collectors are added.
  command -v timeout >/dev/null 2>&1 || \
    echo "» WARNING: timeout is not on PATH; the bounded reads below run UNBOUNDED, so one that hangs can still take the op and the tenant snapshot with it, and the collectors that call timeout directly (wedge check, serial console, guest Talos) exit 127 and collect nothing; the ones that guard the call with command -v -- the worker CPU throttling, worker network counter, worker block IO counter, sandbox node CPU time, sandbox kernel KVM counters, runner kernel CPU time, sandbox QEMU per-thread CPU time, runner fixed-work canary, worker per-thread CPU time, ghcr-mirror and talos-image-cache captures -- keep collecting instead, unbounded" >&2
  # Re-checked here, not only at assignment: a value set after this file is sourced
  # -- which is how a test sets it -- would otherwise reach the arithmetic below
  # unvalidated, and that is the one failure that costs the whole block.
  COZY_DIAG_PHASE_BUDGET=$(_cozy_diag_seconds "${COZY_DIAG_PHASE_BUDGET-}" "$COZY_DIAG_PHASE_BUDGET_DEFAULT" COZY_DIAG_PHASE_BUDGET)
  _COZY_DIAG_PHASE_DEADLINE=$(( $(date +%s) + COZY_DIAG_PHASE_BUDGET ))
}

# cozy_diag_phase_has_time <what>: 0 while the phase can still afford <what>, 1
# once it cannot -- and on 1 it says which collector was declined and why.
#
# Declining out loud is the whole point. A collector that silently stops running
# leaves a bundle that looks like one where nothing was wrong, which is the
# reading this failure path exists to prevent; and what is missing here is
# missing from the log, not from the cluster.
#
# The note goes to stderr for the same reason cozy_diag_read's do, and it is
# called from inside that function, so it inherits the trap exactly: two of its
# call sites pipe stdout through grep, and a note on stdout is dropped by the
# filter at precisely the reads that were declined.
cozy_diag_phase_has_time() {
  [ "${_COZY_DIAG_PHASE_DEADLINE}" -ne 0 ] || return 0
  [ "$(date +%s)" -ge "${_COZY_DIAG_PHASE_DEADLINE}" ] || return 0
  echo "=== ${1}: not collected — the diagnostics phase spent its ${COZY_DIAG_PHASE_BUDGET}s budget and the tenant crust-gather snapshot after it needs the rest of the op; nothing here was observed either way ===" >&2
  return 1
}

# cozy_green_capture_has_room <what>: 0 while the operation can still afford
# <what> on the PASSING path, 1 once it cannot -- and on 1 it says which
# collector was declined and why.
#
# The failure path's counterpart above bounds a phase against a budget of its
# own. This one has no phase to bound: it runs after everything the suite
# proves, inside an operation whose ceiling the bringup ahead of it has already
# spent an unknown share of. So the quantity is what is LEFT of that ceiling,
# and the only way to know it is the stamp taken when the function began.
#
# A function rather than two lines at the call site, because the direction of
# the comparison is the whole safety property and a predicate is something a
# test can drive both ways. Inlined, the only coverage available is a grep for
# the line, which passes just as well when the comparison is inverted -- and an
# inverted one runs the capture on exactly the runs that cannot afford it.
cozy_green_capture_has_room() {
  local elapsed=$(( $(date +%s) - _COZY_RUN_STARTED_AT ))

  [ "$(( COZY_OP_CEILING - elapsed ))" -lt "${COZY_GREEN_CAPTURE_RESERVE}" ] || return 0
  # States the room asked for rather than the cost, because those are different
  # numbers and the gate compares against the first: the reserve is the capture's
  # own worst case plus slack, so reporting it as what the capture costs would
  # overstate the collector by the slack every time this fires.
  echo "» ${1}: not collected — ${elapsed}s of the ${COZY_OP_CEILING}s operation are spent and this needs ${COZY_GREEN_CAPTURE_RESERVE}s of room to start; nothing here was observed either way" >&2
  return 1
}

# cozy_diag_read <label> <command...>: run one diagnostic read under a wall-clock
# bound, and name the outcome when it does not finish. Used by both on-failure
# blocks in run_kubernetes_test, which end in the same `exit 1` and so depend on
# the same snapshot staying reachable.
#
# Notes go to stderr rather than stdout because two call sites pipe the read's
# stdout through grep to keep the log readable, and a note on stdout would be
# filtered out by exactly the runs that need it.
#
# The note says what was observed and stops there. This helper is shared by every
# bounded read here and cannot know what any one of them was asking, so "the read
# did not finish" is the whole claim; the branch that owns a question is the only
# place a consequence could be drawn from it. That distinction is the point: a
# read that was cut off is silence about the cluster, not a finding about it, and
# an empty node table reported as a finding sends the next reader after a cluster
# that was never looked at.
#
# Always returns 0. Every call site sits on a path that ends in the caller's
# `exit 1`, and a non-zero return here would replace that exit under `set -e` —
# handing the suite's exit status, and the snapshot that hangs off it, to a
# collector.
cozy_diag_read() {
  local label="$1"
  shift
  local rc=0

  # Checked here rather than only at the section boundaries so the guarantee is
  # total: whatever order a future edit puts these reads in, none of them can be
  # issued after the phase is out of budget.
  cozy_diag_phase_has_time "${label}" || return 0
  # Re-checked here rather than only at assignment, and here rather than in
  # cozy_diag_phase_start, because the scheduling-gate branch calls this without
  # opening a phase. The value that makes it necessary is zero: `timeout -k 5 0`
  # disables the timeout and `--request-timeout=0s` means no timeout to kubectl, so
  # a zero assigned after sourcing -- which is how a caller adjusts this -- restores
  # the unbounded read on the wedged-apiserver path with no warning and no note. The
  # corrected value is written back so the warning is not repeated per read. That
  # write-back is discarded at the two call sites whose stdout is piped through
  # grep, since a pipeline runs its left side in a subshell, and it does not show:
  # both blocks validate the knob before composing the per-request string, so the
  # value is already corrected before any read here runs. A caller that reaches
  # this function with a bad value and no such validation would warn per read.
  # The node-join block validates it once more before composing its
  # `--request-timeout` string; this check is what covers the scheduling-gate branch,
  # which calls straight in here without opening a phase.
  COZY_DIAG_READ_TIMEOUT=$(_cozy_diag_seconds "${COZY_DIAG_READ_TIMEOUT-}" "$COZY_DIAG_READ_TIMEOUT_DEFAULT" COZY_DIAG_READ_TIMEOUT positive)
  COZY_DIAG_READ_GRACE=$(_cozy_diag_seconds "${COZY_DIAG_READ_GRACE-}" "$COZY_DIAG_READ_GRACE_DEFAULT" COZY_DIAG_READ_GRACE)
  # Unbounded when `timeout` is absent, rather than bounded-into-exit-127. Running
  # the read is what this path did before any of this, and a partial capture beats
  # eleven notes blaming the cluster for a missing local binary. Same shape the
  # previous-logs collector uses, and cozy_diag_phase_start says it once out loud.
  # `command -v` is a builtin, so asking per read costs nothing and keeps this
  # correct when a caller adjusts PATH mid-run.
  if command -v timeout >/dev/null 2>&1; then
    timeout -k "${COZY_DIAG_READ_GRACE}" "${COZY_DIAG_READ_TIMEOUT}" "$@" || rc=$?
  else
    "$@" || rc=$?
  fi
  case "${rc}" in
    0) ;;
    124)
      # "did not finish", not "did not answer": a `logs` or `describe` read can
      # print most of its output and still be cut off, so the claim is about the
      # read ending early rather than about it saying nothing.
      echo "=== ${label}: read did not finish within ${COZY_DIAG_READ_TIMEOUT}s and was cut off; whatever it had not printed is absent from this log, not absent from the cluster ===" >&2
      ;;
    137)
      # 128+SIGKILL. The -k grace above produces this, and so does anything else
      # that kills the read — a loaded runner's OOM killer, a teardown
      # signalling the process group. "timed out after 20s" would state a cause
      # this never observed, and a read killed at second two is not one that ran
      # the full twenty.
      echo "=== ${label}: read was killed before it finished (SIGKILL); whatever it had not printed is absent from this log, not absent from the cluster ===" >&2
      ;;
    *)
      echo "=== ${label}: read failed (exit ${rc}); what it would have shown was not observed ===" >&2
      ;;
  esac
  return 0
}

# Diagnostics for the node-join failure at the call site in run_kubernetes_test:
# fewer than 2 tenant nodes became Ready inside its 29m deadline.
#
# The tenant's cilium-operator HR reports "InProgress" here purely because zero
# worker Nodes joined, so the HelmRelease condition alone cannot tell apart
# (2a) the worker VM never booted (virt-launcher Pending/OOMKilled) from (2b) the
# VM booted fine but its kubelet never registered a Node (Talos/CSR/DNS/routing).
# The captures below make that distinction legible; (2b) is the failure mode a
# follow-up fix has to target, and it cannot be designed without this artifact.
#
# Every read is bounded and the one walk is capped, for the reason that makes
# this block worth having in the first place: it runs when the cluster is
# misbehaving, and its first read goes through the tenant kubeconfig to the
# tenant apiserver — the component least likely to answer in a node-join
# failure. An unbounded read here does not lose only itself. It holds the
# Chainsaw op until the op is killed, and everything scheduled after it is then
# lost rather than truncated, the tenant crust-gather snapshot above all, which
# is the largest artifact this whole path exists to produce.
#
# No capture is allowed to fail the function either, for the same reason the
# reads are bounded: the caller chooses the exit this returns into, and that
# choice is what the tenant snapshot keys on. A collector returning non-zero
# here would take the choice over under `set -e`.
cozy_report_node_join_failure() {
  local test_name="$1"
  local tenant_kc="tenantkubeconfig-${test_name}"
  # Validated before the per-request bound is built from it, not only inside
  # cozy_diag_read: that one corrects the wall clock, and this string is composed
  # once for every read below. Left to the later check, a zero assigned after
  # sourcing would give `timeout -k 5 20` paired with `--request-timeout=0s` -- the
  # two halves disagreeing, which is what the single value exists to prevent.
  COZY_DIAG_READ_TIMEOUT=$(_cozy_diag_seconds "${COZY_DIAG_READ_TIMEOUT-}" "$COZY_DIAG_READ_TIMEOUT_DEFAULT" COZY_DIAG_READ_TIMEOUT positive)
  local request_timeout="--request-timeout=${COZY_DIAG_READ_TIMEOUT}s"
  # importer_list is initialised, not just declared: the assignment below sits
  # inside the phase gate, and bash under `set -u` aborts on the read that
  # follows when the gate declined it.
  local importer_list='' importer_names importer_rc=0 importer_seen=0 importer_total=0
  local importer_listed=0 _p _sample

  cozy_diag_phase_start

  # The headline stays here rather than at the call site so it keeps its place
  # ahead of the wedge check: that check exists to name the console experiment's
  # own failure before this line's wording -- which is byte-identical to the bug
  # the instrumentation studies -- sends a triager to the known flake.
  echo "=== node-join failed: fewer than 2 tenant nodes Ready within 29m — diagnostics follow ==="
  cozy_report_guest_console_wedge || true
  # The second KVM counter reading, pairing with the one taken before the wait.
  # Ungated for the reason the wedge check above is, and for one of its own:
  # the phase gate decides what may START, and a collector it declines loses
  # only itself -- except this one, which would also retire a reading taken
  # a 29m node-join wait earlier and leave it an orphan. Its cost is a single
  # bounded read of a file on this machine, so it cannot meaningfully bound what
  # comes after it.
  cozy_capture_sandbox_kvm_exits 2 || true
  # The second half of the other two pairs, here for the same reason and with the
  # same standing: the phase gate decides what may START, and a collector it
  # declines loses only itself -- except these, which would also retire a reading
  # taken a 29m node-join wait earlier and leave it an orphan.
  #
  # What they cost, stated rather than waved at: each runs under one ceiling of
  # its own, so the pair adds two of those to whatever runs before the console.
  # The first is one file read; the second is a walk over this container's /proc,
  # bounded by the same ceiling but not by a single open. The guard in
  # hack/run-kubernetes-node-join_test.bats prices both at that ceiling and holds
  # the sum against the phase budget.
  cozy_capture_runner_kernel_cpu_time 2 || true
  cozy_capture_sandbox_qemu_thread_cpu 2 || true
  # The canary's second sample, last of the four that bracket the wait. Last
  # rather than beside them because it is the only one of the four that costs a
  # core rather than a read -- ahead of them, its burn would sit inside the
  # interval each of those pairs divides by.
  #
  # Here rather than further down the block, and ungated, for a reason of its
  # own: unlike the three beside it this sample is an absolute reading
  # and would still be a reading taken minutes later. What it would stop being
  # is comparable. The passing path takes its own sample within seconds of the
  # wait returning, and the whole use of the expected ranges is that a red
  # figure and a green figure describe the same moment in the run; a red sample
  # that drifted behind the diagnostics reads a machine several minutes past the
  # failure it is meant to characterise.
  #
  # What it costs, stated rather than waved at: two arms, each under a ceiling
  # of its own. The guard in hack/run-kubernetes-node-join_test.bats prices both
  # at that ceiling and holds the sum against the phase budget.
  cozy_capture_runner_canary 2 || true
  # The alert reaches the job log here as well, and for a sharper version of the
  # reason the two samples outside this block report theirs: this is the run a
  # triager opens, the tail of it is where they start, and sample 1's warning is
  # a 29m node-join wait and thousands of lines above.
  if [ "${_COZY_CANARY_ALERT:-0}" -eq 1 ]; then
    echo "» WARNING: the runner fixed-work canary did not read inside the range its own legend calls healthy on the failing run, so what follows was collected on a machine that was not getting the work done; which arm it was and what figure it gave is written into the capture" >&2
  fi
  cozy_diag_read 'tenant node table' \
    kubectl --kubeconfig "${tenant_kc}" describe nodes "${request_timeout}"
  cozy_diag_read 'tenant HelmReleases' \
    kubectl -n tenant-test get hr "${request_timeout}"

  # (a) Worker VM / VMI / virt-launcher state on the MANAGEMENT cluster. A VMI
  # stuck Pending or a virt-launcher pod OOMKilled/Pending is mode 2a; a
  # Running+Ready VMI with a healthy virt-launcher is mode 2b. This is the key
  # split. Full resource names (not the `vm` alias) to avoid short-name
  # ambiguity, matching cozy_wait_tenant_drained above.
  echo "=== (a) tenant worker VM/VMI/virt-launcher state (management cluster, ns tenant-test) ==="
  cozy_diag_read 'worker VM/VMI list' \
    kubectl -n tenant-test get virtualmachines.kubevirt.io,virtualmachineinstances.kubevirt.io -o wide "${request_timeout}"
  cozy_diag_read 'worker VMI detail' \
    kubectl -n tenant-test describe virtualmachineinstances.kubevirt.io "${request_timeout}"
  cozy_diag_read 'virt-launcher Pod list' \
    kubectl -n tenant-test get pods -l kubevirt.io=virt-launcher -o wide "${request_timeout}"
  cozy_diag_read 'virt-launcher Pod detail' \
    kubectl -n tenant-test describe pods -l kubevirt.io=virt-launcher "${request_timeout}"

  # (a2) Worker DataVolume IMPORT stage. A VM stuck "Provisioning" whose
  # DataVolume is ImportInProgress at N/A progress with the importer pod
  # looping on an HTTP error is a distinct sub-mode of 2a that the VM/VMI
  # state alone does not show: the OS image never finishes importing, so the
  # VM never boots. This is what took out PR #2826's CI — the CDI importer
  # could not reach the talos-image-cache ClusterIP (`dial tcp <svc>:80: i/o
  # timeout`) even though the cache pod was healthy. Show the DataVolume/PVC
  # phases and the importer pod logs here; the cache ClusterIP re-probe that tells
  # "cache path went dead mid-run" apart from "upstream factory slow/flaky" belongs
  # to this section too, but it creates a Pod and waits on curl, so it sits with
  # the other minute-scale collectors further down rather than here.
  echo "=== (a2) tenant worker DataVolume import stage (management cluster, ns tenant-test) ==="
  # The two greps keep these dumps readable. kubectl's stderr is no longer folded
  # into them: `2>&1 | grep` sent a refusal into the filter, which dropped it for
  # not matching, so a read that never happened looked the same as one that found
  # nothing. It goes to the log unfiltered instead, beside cozy_diag_read's note.
  cozy_diag_read 'worker DataVolume/PVC phases' \
    kubectl -n tenant-test get datavolume,pvc -o wide "${request_timeout}" \
    | grep -E 'NAME|md0|disk' || true
  cozy_diag_read 'worker DataVolume detail' \
    kubectl -n tenant-test describe datavolume "${request_timeout}" \
    | grep -Ei 'Name:|Phase:|Progress:|Restart|Reason:|Message:|Running Condition|Bound Condition' || true

  # The listing is read on its own rather than inline in the `for`, so a listing
  # that never answered stays distinguishable from a namespace with no importer
  # Pod in it. Folded together, a failed read produces no names, the walk skips,
  # and the log carries nothing at all — which reads as "no importer ran", the
  # opposite conclusion on the sub-mode this whole section is about.
  #
  # Reading it on its own is also why the phase gate is spelled out here: this is
  # the one read in the block that does not go through cozy_diag_read, so it is
  # the one that does not inherit the gate.
  #
  # kubectl's stderr is not discarded, for the reason the two grep-piped reads
  # above stopped discarding theirs: the note below can only quote an exit status,
  # and Unauthorized, a refused connection and an unrecognised kind are all exit 1.
  # Only stdout is captured here, so the reason lands in the log beside the note.
  if cozy_diag_phase_has_time 'CDI importer Pod listing'; then
    importer_listed=1
    if command -v timeout >/dev/null 2>&1; then
      importer_list=$(timeout -k "${COZY_DIAG_READ_GRACE}" "${COZY_DIAG_READ_TIMEOUT}" \
        kubectl -n tenant-test get pods -o name "${request_timeout}") || importer_rc=$?
    else
      importer_list=$(kubectl -n tenant-test get pods -o name "${request_timeout}") || importer_rc=$?
    fi
    if [ "${importer_rc}" -ne 0 ]; then
      echo "=== could not list Pods to find the CDI importers (exit ${importer_rc}); whether any importer Pod exists is unknown, not none ==="
      importer_list=
    fi
  fi
  importer_names=$(printf '%s\n' "${importer_list}" | grep -E '^pod/importer-' || true)
  # `|| true`: grep -c exits 1 on a count of zero, which under set -e would end
  # the collector on the ordinary "no importer Pod" case.
  importer_total=$(printf '%s\n' "${importer_names}" | grep -c . || true)
  # Re-checked here for the reason every one of these knobs is re-checked where it is
  # used: a value set after this file was sourced never passed the assignment-time
  # check. This one fails silently -- `[ n -gt 3s ]` exits 2, the comparison below
  # reads false, and the cap simply stops existing.
  COZY_DIAG_MAX_IMPORTERS=$(_cozy_diag_seconds "${COZY_DIAG_MAX_IMPORTERS-}" "$COZY_DIAG_MAX_IMPORTERS_DEFAULT" COZY_DIAG_MAX_IMPORTERS)
  # Three states, not two, and the third is why importer_listed exists. A listing
  # that answered and matched nothing is a real finding about the import stage and
  # is said out loud. A listing that failed said so above. A listing the phase
  # declined never ran, and reporting that as "matched none" would put the
  # conclusion this section exists to test -- no importer, so no import -- on the
  # record from a read that was never issued, one line under the note saying it was
  # not collected.
  if [ "${importer_listed}" -eq 1 ] && [ "${importer_rc}" -eq 0 ] \
    && [ "${importer_total}" -eq 0 ]; then
    echo "=== no CDI importer Pod in tenant-test; the listing answered and matched none ==="
  fi
  for _p in ${importer_names}; do
    importer_seen=$((importer_seen + 1))
    if [ "${importer_seen}" -gt "${COZY_DIAG_MAX_IMPORTERS}" ]; then
      echo "=== importer log walk stopped after ${COZY_DIAG_MAX_IMPORTERS} Pods; ${importer_total} matched in total ==="
      break
    fi
    echo "--- logs ${_p} (current) ---"
    # No --request-timeout on the log reads: it bounds an API request, and these
    # are a log stream. The wall-clock bound is what covers them.
    cozy_diag_read "importer log ${_p} (current)" \
      kubectl -n tenant-test logs "${_p}" --tail=40
    # An importer that never restarted has no previous instance and kubectl exits
    # 1, which cozy_diag_read reports as a read that failed -- true, but the same
    # shape as a refused call, and the ordinary outcome here. kubectl's own
    # "previous terminated container not found" lands in the log beside the note;
    # this line says so up front so the pair reads as one answer rather than two.
    echo "--- logs ${_p} (previous; exit 1 here is usually the container never having restarted, and kubectl's own message below says which) ---"
    cozy_diag_read "importer log ${_p} (previous)" \
      kubectl -n tenant-test logs "${_p}" --previous --tail=40
  done
  # (c) Tenant kubelet CSRs + the talos-csr-signer sidecar log. A mode-2b node
  # boots but blocks on a kubelet-serving/-client CSR that is never submitted
  # or never approved; the pending CSR list (tenant cluster) plus the signer
  # sidecar log (in the Kamaji apiserver pod on the management cluster) show
  # which side stalled.
  echo "=== (c) tenant CSRs + talos-csr-signer sidecar log ==="
  cozy_diag_read 'tenant CSR list' \
    kubectl --kubeconfig "${tenant_kc}" get csr "${request_timeout}"
  cozy_diag_read 'talos-csr-signer sidecar log' \
    kubectl -n tenant-test logs -l kamaji.clastix.io/name="kubernetes-${test_name}" \
    -c talos-csr-signer --tail=200 --prefix

  # (a3) State of the replicated volumes behind the worker disks. Numbered with
  # the import stage it belongs to and placed here instead, because position in
  # this block is cost order rather than subject: it is one read, and moving it up
  # would push the two reads at (c) a read further from the start of the budget.
  #
  # What it answers is a state rather than a rate, which is why one read settles
  # it. A worker reads its system disk over DRBD, and a replica that is
  # SyncTarget, Inconsistent or Diskless serves those reads from a peer over the
  # network instead of locally, which is slow in a way no counter in this block
  # attributes to storage. The state also disappears with the volume: the report
  # collects the same tables after the suite, by which time cleanup has usually
  # deleted the worker's volumes, so a red run leaves nothing about them there.
  #
  # Which rows are the worker's is decided through the PV name, which the (a2)
  # dump above prints beside each PVC; resource names here are those PV names.
  #
  # No --request-timeout: this is an exec stream rather than an API request, so
  # the wall clock around it is the whole bound. A controller that is absent and
  # one that is wedged both arrive as a failed read, and kubectl's own message
  # beside the note is what separates them.
  echo "=== (a3) replicated volume state behind the worker disks (management cluster) ==="
  cozy_diag_read 'LINSTOR resource state' \
    kubectl -n cozy-linstor exec deploy/linstor-controller \
    --container=linstor-controller -- linstor --no-color r l

  # Order below is load-bearing and the phase budget is why. The budget declines
  # whatever has not started when it runs out, so what runs last is what gets
  # declined -- and (c) is the discriminator for mode 2b, the failure this whole
  # artifact exists to let someone fix. Cheap reads first, then the collectors
  # that cost minutes. Everything ahead of this line is bounded reads and a capped
  # walk, which together fit inside the budget with room left, while the gated
  # collectors below can exhaust it between them on a slow run. Putting the
  # heavy ones first, as this block used to, spends the budget on them and
  # declines the two 25s reads that answer the question. The same rule decides
  # the order among the gated collectors themselves: each one's ceiling bounds
  # what everything after it can lose, so a collector goes above the console
  # only if its ceiling is small enough to be worth risking the console for.
  #
  # (d2) Worker network counters, first of the gated collectors and cheapest of
  # them: one listing and one read per node, four bounded reads against the
  # minutes every collector below can spend between them. Its answer also has no
  # other source in this artifact. What it settles is which side of the worker's image
  # pull is slow. The guest reports its own progress and cannot see the bytes
  # that never became progress, so a pull that keeps restarting and one that
  # crawls look identical from in there. The host side counts both, and the gap
  # between it and the guest's progress is the discriminator.
  #
  # WHICH ROW answers that is not obvious and getting it wrong inverts the
  # reading, so it is written here rather than left to the reader. cAdvisor
  # publishes one row per interface in the Pod's network namespace, and a
  # bridge-bound worker has four of them:
  #
  #   eth0      a DUMMY holding the Pod IP so the kubelet's check still passes.
  #             It carries no traffic, so its counters sit at zero -- and it is
  #             the row a reader reaches for first, by name, where zero reads as
  #             a worker that received nothing.
  #   eth0-nic  the Pod's real NIC, renamed and enslaved to the bridge. Its
  #             RECEIVE is what arrived from the cluster network, and that is
  #             the number this capture exists to produce.
  #   k6t-eth0  the bridge; the same bytes again as they cross it.
  #   tap0      the host side of the tap to the guest, whose counters are named
  #             from the host's end, so traffic toward the guest is its
  #             transmit rather than its receive.
  #
  # The filter deliberately carries no interface predicate: all four rows reach
  # the artifact so the reader can tell them apart, and dropping three of them
  # here would decide the question in the collector on the strength of names
  # that KubeVirt, not this file, chooses.
  # Placed after the guest captures it would be the first thing declined on
  # exactly the slow runs this failure comes from.
  if cozy_diag_phase_has_time '(d2) tenant worker network counters'; then
    echo "=== (d2) tenant worker network counters (management cluster, ns tenant-test) ==="
    cozy_capture_tenant_worker_network_counters || true
  fi

  # (d3) Worker block IO, and it sits here for the reason (d2) does: one listing
  # and one read per node, read once, against the minutes the collectors below can
  # spend between them. What it settles has no other answer in this artifact
  # either. A worker whose vCPU thread runs at its physical maximum while nothing
  # progresses is either computing without result or waiting on blocks, and every
  # other counter here is blind to the second: the CPU families say what the group
  # was allowed and what it got, (d2) says what crossed the network, and neither
  # counts a read. Placed below the guest captures it would be declined on exactly
  # the slow runs this failure comes from.
  if cozy_diag_phase_has_time '(d3) tenant worker block IO counters'; then
    echo "=== (d3) tenant worker block IO counters (management cluster, ns tenant-test) ==="
    cozy_capture_tenant_worker_block_io || true
  fi

  # (b1) Guest serial console, read from the management cluster. First of the
  # in-guest captures because it is the only one that survives a worker which
  # never reached apid — the dominant shape of this failure, where no Node
  # registers and no certificate request is ever made. (b) below needs that
  # same apid to answer, so it cannot describe this class at all.
  # Two shapes of empty result are worth telling apart from a silent guest.
  # A virt-launcher Pod still Pending has no containers at all yet, which is
  # the ordinary shape of a worker that never booted. A Pod wedged in Init on
  # guest-console-log is the opposite reading: the console container is what
  # stopped it booting, and then neither a console nor an apid exists to
  # explain anything else. The container being absent from a Running Pod is
  # the rarest of the three, since the per-VM field outranks the cluster-wide
  # disable by design and the green path asserts it attached.
  if cozy_diag_phase_has_time '(b1) tenant worker guest serial console'; then
    echo "=== (b1) tenant worker guest serial console (management cluster, ns tenant-test) ==="
    cozy_capture_tenant_serial_console 'node-join failed: captured after fewer than 2 tenant nodes became Ready inside the deadline' 6 || true
  fi

  # (d) What the workers got, what they were denied, and what the sandbox nodes
  # under them lost to their own hypervisor. Its question has no other answer
  # here: the ceiling is already carried by (a), but whether the guest ever met
  # it, and whether it was ever scheduled at all, are recorded nowhere else,
  # while (b1) above and (b) below describe a guest that at least reports
  # something on its own. (a) shows a Running VMI on a healthy virt-launcher,
  # which is where mode 2a stops being the answer and the question becomes why a
  # healthy VM made no progress -- a node sitting at half its capacity does not
  # settle that, and these counters do.
  #
  # It sits BELOW the console, and its ceiling rather than its worth is the
  # reason. Each collector spends a listing plus one read per subject it walks,
  # twice, and at the read bound and today's caps that ceiling is well past the
  # whole phase budget rather than a share of it. In practice it costs the
  # interval plus a handful of reads that an apiserver or an apid answering at
  # all answers in under a second -- but the run where
  # those reads DO approach their bound is a wedged kubelet, which is the
  # failure this whole block is written for. So the ceiling is not a remote
  # case here, and a collector carrying one that large must not sit ahead of the
  # single capture that survives a worker which never reached apid: ahead of the
  # console it could take the console with it, and below it the worst it can
  # spend is its own time and the legs after it. The gate's promise is the same
  # either way and is worth stating, since it reads like a fit check and is not
  # one: it admits a collector whose start is inside the budget, so one admitted
  # late still runs to completion and overruns.
  #
  # That overrun is already budgeted for, and the number is worth putting here
  # because it is the first question this pair invites. The phase budget is
  # derived as budget + largest overshoot + snapshot <= op - bringup, where the
  # overshoot term is exactly "one collector admitted a moment before the
  # deadline runs its whole cost". This group's ceiling is the sum of its walks
  # plus the interval; the term is the guest-Talos walk, which is still larger,
  # though no longer by much. So a group admitted at the last second finishes
  # inside the room already left for the tenant crust-gather snapshot rather
  # than pushing it past the op.
  # hack/run-kubernetes-node-join_test.bats holds that against both numbers, so
  # a later change to a cap, the read bound or the interval cannot quietly make
  # this group the binding term. The margin is not uniform across those knobs
  # and is not restated here: every walk enters the sum multiplied by its cap
  # and by two samples, while the interval enters it once, so a cap and the
  # interval are nowhere near equally expensive. The guard computes both sides
  # from the source, so it is where a reader finds out which knob has room, and
  # where the failure surfaces rather than in a timed-out run.
  #
  # Each collector is read twice with one wait between the passes, and the wait
  # is shared: every counter here is cumulative, so one reading of any of them
  # is an average over an uptime rather than a rate over the failure. Taking the
  # first reading of each, waiting once, and taking the second of each costs the
  # wait once rather than once per subject.
  #
  # What the knob does NOT give is the interval a subject's counters span. Each
  # subject's two readings are separated by the wait plus the other subjects'
  # passes, and on the run this exists for those passes are the slow part rather
  # than the wait. So each capture stamps the moment it was read and the reader
  # subtracts stamps rather than assuming the knob. The subjects' windows
  # overlap and are offset from one another; they are not identical, and
  # treating them as identical is the error the stamps remove.
  #
  # This `sleep` is not the fixed-timeout kind the e2e conventions rule out.
  # Those stand in for a condition nobody wrote a wait for; this one is the
  # measurement interval, and there is no event to wait for instead of it. The
  # doc carves it out by name rather than leaving the reader to judge.
  #
  # Re-validated here rather than trusted from the assignment, like every other
  # knob in this block, because a value set after this file is sourced never
  # passed that check -- and zero, the value the flag rejects, would put both
  # readings at the same instant and leave a pair that divides to nothing.
  COZY_DIAG_RATE_INTERVAL=$(_cozy_diag_seconds "${COZY_DIAG_RATE_INTERVAL-}" "$COZY_DIAG_RATE_INTERVAL_DEFAULT" COZY_DIAG_RATE_INTERVAL positive "zero puts both readings at the same instant, so the pair divides to nothing")
  # The label names all three subjects because the phase reuses it verbatim in
  # the decline line, the way the (b) gate below does and for the same reason: a
  # decline that names two of them reports the third as lost to nobody.
  if cozy_diag_phase_has_time '(d) tenant worker CPU counters, sandbox node CPU time and worker per-thread CPU time'; then
    echo "=== (d) tenant worker CPU counters + sandbox node CPU time + worker per-thread CPU time, two samples with a ${COZY_DIAG_RATE_INTERVAL}s wait between the passes; each capture carries the time it was read ==="
    for _sample in 1 2; do
      # Guarded like every other external here, and for a sharper reason than
      # the reads: this call is not wrapped in `|| true`, and the block runs
      # under `set -eu` inside the chainsaw script, so `sleep: command not
      # found` exits 127 and takes everything after it -- including two of the
      # five collectors the phase's own missing-timeout warning promises keep
      # collecting. Degrading to back-to-back readings is honest rather than
      # lossy: the interval a rate divides by is read off the two stamps in each
      # capture, not off this knob, so a wait that did not happen yields a
      # tighter window rather than an unreadable pair.
      if [ "${_sample}" != 1 ] && command -v sleep >/dev/null 2>&1; then
        sleep "${COZY_DIAG_RATE_INTERVAL}"
      elif [ "${_sample}" != 1 ]; then
        echo "» WARNING: sleep is not on PATH; the two readings are taken back to back, so the pair spans only what the first pass took -- the stamps inside each capture say how much" >&2
      fi
      cozy_capture_tenant_worker_cpu_throttle "${_sample}" || true
      cozy_capture_sandbox_node_cpu_time "${_sample}" || true
      cozy_capture_tenant_worker_thread_cpu "${_sample}" || true
    done
  fi

  # (b) In-guest Talos kernel and kubelet logs. The tenant chart intentionally
  # has no admin talosconfig, so mint a one-hour os:reader client from its
  # existing cert-manager Issuer and run talosctl from a hardened Pod that can
  # reach the bridge-networked VMI IPs without weakening TLS verification.
  # A worker that has not reached apid yet will produce a bounded connection
  # error while a later-stage worker remains capturable; both outcomes are
  # retained in cozyreport.
  # The label names all four reads because the phase reuses it verbatim in the
  # decline line, and a decline that names two of them reports the other two as
  # lost to nobody: the service states are the finding on the shape of this
  # failure where no kubelet log exists to read at all, so a note that does not
  # mention them loses them silently rather than out loud.
  if cozy_diag_phase_has_time '(b) in-guest Talos dmesg + kubelet logs + service states + links'; then
    echo "=== (b) in-guest Talos dmesg + kubelet logs + service states + links ==="
    cozy_capture_tenant_talos "${test_name}" || true
  fi

  # The OS-image cache and the ghcr.io mirror fail independently and produce the
  # same symptom from outside the guest, so both get dumped: this one answers
  # whether the worker's kubelet-image pull reached the mirror or fell back to
  # public ghcr.io, which the node-join failure alone cannot distinguish. Gated
  # like its neighbours, and after the guest captures. Bounded read by read
  # like the collector at (d2), but five of them at COZY_DIAG_READ_TIMEOUT plus
  # grace, so it can spend a quarter of the phase budget -- and time is the
  # only thing the gate rations, so a quarter spent here is a quarter the
  # guest captures do not get. Cost is not what settles the order, though, or
  # (d2) would sit here too: what settles it is whether the answer survives
  # being declined. The console evidence this would starve is irreplaceable
  # and (d2)'s question has no other answer in the tree, while the mirror's
  # state is partly recoverable from the reads above -- so those two go first
  # and this one waits, whichever of them is cheaper. Cheaper than the
  # talos-image-cache re-probe below, which creates a Pod and waits on curl
  # retries, so it goes ahead of it.
  if cozy_diag_phase_has_time 'ghcr-mirror state, access log and warm-up Job'; then
    echo "--- ghcr-mirror state, access log and warm-up Job ---"
    ghcr_mirror_diagnose || true
  fi

  # Last, and gated on the phase as well as bounded per read, because it is the
  # collector that most needs both: its reachability re-probe creates a Pod, waits
  # on curl retries, and makes seven unbounded management-cluster calls of its own,
  # so it has no ceiling here at all.
  if cozy_diag_phase_has_time 're-probe talos-image-cache ClusterIP + cacher debug bundle'; then
    echo "--- re-probe talos-image-cache ClusterIP + cacher debug bundle ---"
    talos_image_cache_diagnose || true
  fi
}

# Whether a non-zero from the node-join wait is the deadline expiring.
#
# Why 29m and not some other figure, since this is the number the answer is
# about. It is this test's own budget and only that. The clock starts where the
# wait starts, which is after the machinedeployment wait, the LoadBalancer
# assignment and the healthz probe, each with a ceiling of its own; the
# product's timers start elsewhere and are already running by then. The
# autoscaler's `maxNodeProvisionTime` runs from the Machine's creation, and the
# MachineHealthCheck's `nodeStartupTimeout` from the later of creation and
# InfrastructureReady, restarting at that transition. So the offset between this
# deadline and either of those is not fixed, and no ordering against them is
# claimed here.
#
# What the two figures do give is a scale to pick against. The chart ships 20m
# for `nodeStartupTimeout` and 30m for `maxNodeProvisionTime`, and the e2e
# tenant overrides neither. A budget over the first is wide enough for a
# remediation cycle to fit inside it; whether one does on a given run is not
# something this deadline decides, and a run that spends the window on a single
# worker that never comes back is as much a red as one that watched a
# replacement fail.
#
# That width has a price and it is paid in evidence: when the health check does
# replace a worker inside the window, the guest serial console of the first one
# goes with its virt-launcher Pod. What survives is the console of its
# replacement and, through Chainsaw's catch, the host snapshot carrying the
# Machine history that shows the replacement happened at all.
#
# 124 is what `timeout` returns when it fired, and it is the only status that
# means what the marker below is about. The wrapper's other failures are
# reported with the same non-zero shape and mean something else entirely: 125 is
# `timeout` failing, 126 and 127 are the shell it was asked to run being
# unusable or absent, and 128+n is that shell killed by a signal, the OOM killer
# on a loaded runner among them. Folding those into the softened branch would
# report "the workers were slow" for a run where the wait never ran, which is
# the one claim this change must not start making.
#
# No `-k` is given to that `timeout`, so a 137 here is a kill from outside the
# wait rather than its own escalation, and it stays hard for that reason.
cozy_node_join_deadline_expired() {
  [ "$1" -eq 124 ]
}

# Record that the node-join deadline, and not something else, is what ended this
# run. The suite still fails: the exit below this call is 1, so Chainsaw sees a
# failed test, its whole catch runs, and the JUnit report says what happened.
# What this function writes is the evidence one layer up needs to tell this
# failure apart from every other way the suite can go red.
#
# The deadline stopped discriminating. Nested virtualisation on these runners
# degrades far enough that the guest a worker boots in can miss any deadline
# this test can afford, which is what the four readings that bracket the wait
# were added to measure. So a red on this one wait no longer separates a product
# regression from the machine the run landed on, and a signal that cannot
# separate those teaches a reader to re-run the job -- which is how the reds
# that do mean something get re-run too. The lanes that block a merge therefore
# tolerate this one failure and only this one, and they decide that from the
# marker below rather than from anything they could infer about the run.
#
# Two things are written, and each is for a different reader.
#
# The annotation is for the reader who never opens the log: a job-level warning
# is the one surface that reaches them. It is `::warning` rather than `::error`
# because an error annotation is what a run that actually broke should carry,
# and this line has to be distinguishable from those at a glance.
#
# It survives the two layers between here and the job log, which is worth
# knowing before anyone moves it: Chainsaw does not print a script's stdout
# through, it collects it and re-prints it under an `=== STDOUT` heading with
# eight spaces of indentation on every line. The Actions runner trims leading
# whitespace before it looks for the `::` (ActionCommand.TryParseV2), so an
# indented workflow command still registers -- but a change that put anything
# else before the `::` on this line, a prefix or a timestamp, would silently
# turn the annotation into a log line nobody sees.
#
# The marker is what the lane reads, and it is also what a reader with the
# report open finds beside the diagnostics. Both uses want the same file, and
# the lane's use is why it has to be written before the exit rather than derived
# from the log afterwards. It is written before the diagnostics rather than
# after them for a reason of the same kind: the diagnostics phase spends minutes
# and the job cap can end the run inside it, and a marker written after that
# point is a marker the lane never sees on exactly the runs that spent longest.
# What it does not give is a cheap count across runs: it lives inside
# cozyreport.tgz, and no API lists the contents of an artifact, so answering
# "how often" means fetching and unpacking every run -- and artifacts expire on
# a retention of their own, the same as logs do.
#
# Nothing in here may fail the caller, and the reason is sharper than tidiness:
# a marker that could not be written turns this run into an ordinary hard red
# one layer up, which is the safe direction, and it must not also break the
# failing exit that gets the diagnostics collected. So the writes are guarded
# and the function returns zero on every path.
cozy_soft_red_node_join() {
  local test_name="$1"
  local report_dir marker
  report_dir=$(_cozy_suite_snapshot_dir)
  marker="${report_dir}/SOFT-RED-node-join.txt"

  echo "::warning title=node-join::${COZY_SNAPSHOT_NAME:-kubernetes}: fewer than 2 tenant nodes Ready within 29m — this test fails and its diagnostics follow below; whether the job blocks over it is decided one layer up, and only the lanes that block a merge tolerate it, because the deadline cannot separate a product regression from a degraded runner"

  if ! mkdir -p "${report_dir}" 2>/dev/null; then
    echo "» WARNING: could not create ${report_dir}, so this run carries no soft-red marker; the warning annotation above is the only record that the node-join deadline expired" >&2
    return 0
  fi
  # The write is tested rather than left to stand on its own: under a live
  # errexit a bare redirect that fails aborts AT the redirect, and the report
  # this function exists to leave would then be missing without a word about it.
  if ! printf '%s\n' \
    "the node-join wait for ${test_name} ended with fewer than 2 tenant nodes Ready within 29m" \
    'this test FAILED, which is what gets the rest of the evidence collected: the in-process diagnostics and then the Chainsaw catch both run after this file is written -- previous-instance container logs, the host snapshot, the data-plane capture, the event dump -- and the report records a failure' \
    'the tenant crust-gather snapshot is attempted after this marker is written; when it ran it lands in this directory with a log saying whether it completed or was truncated, and when it could not start at all -- no crust-gather on PATH, no live kubeconfig -- it leaves nothing here and says nothing, so an absent snapshot beside this marker is unexplained rather than explained' \
    'what the run did NOT reach is everything after the join: the suite stops at this wait, so it asserted nothing about storage, LoadBalancer, addon or ouroboros hairpin-NAT behaviour' \
    'what this file is for is the layer above: a lane that tolerates this failure reads it to tell this red apart from every other way the suite can go red, and tolerates only this one; not every lane does, so a job that finished green while this file exists is a job that decided not to block on a deadline it cannot attribute, not a job whose suite passed' \
    >"${marker}" 2>/dev/null; then
    echo "» WARNING: could not write ${marker}, so this run carries no soft-red marker; the warning annotation above is the only record that the node-join deadline expired" >&2
  fi
  return 0
}

run_kubernetes_test() {
    local version_expr="$1"
    local test_name="$2"
    local port="$3"
    # Optional: when "true", enable the ouroboros addon on the Kubernetes CR
    # and run the hairpin-NAT reconciliation assertions after the cluster is
    # Ready. Folded in here so we don't pay a second ~25m Kamaji bringup just
    # to flip one addon flag — kubernetes-latest passes "true", kubernetes-
    # previous leaves it empty.
    local enable_ouroboros="${4:-}"
    local k8s_version
    k8s_version=$(yq "$version_expr" packages/apps/kubernetes/files/versions.yaml)

  # Stamped here because the passing path below spends wall clock on a
  # diagnostic after everything the suite proves has already passed, and the
  # operation it runs inside has a fixed ceiling. What that costs is bounded;
  # what is LEFT of the ceiling by then is not, so the only way the capture can
  # be prevented from turning a green run red is to know how long this has been
  # running. Nothing else reads it.
  _COZY_RUN_STARTED_AT=$(date +%s)

  # Clean up stale resources from a previous failed retry. Delete the worker
  # pool (KubernetesNodes) before the parent Kubernetes CR so a rerun re-creates
  # the MachineDeployment instead of no-op'ing on a surviving child HelmRelease.
  kubectl -n tenant-test delete kubernetesnodeses.apps.cozystack.io "${test_name}-md0" --ignore-not-found --wait=false 2>/dev/null || true
  kubectl -n tenant-test wait kubernetesnodeses.apps.cozystack.io "${test_name}-md0" --for=delete --timeout=2m 2>/dev/null || true
  kubectl -n tenant-test delete kuberneteses.apps.cozystack.io "${test_name}" --ignore-not-found --wait=false 2>/dev/null || true
  kubectl -n tenant-test wait kuberneteses.apps.cozystack.io "${test_name}" --for=delete --timeout=2m 2>/dev/null || true

  # Compose the optional ouroboros addon block. Indentation matches the
  # surrounding addons map (4 spaces).
  local ouroboros_addon=""
  if [ "${enable_ouroboros}" = "true" ]; then
    ouroboros_addon=$(cat <<'YAML'
    ouroboros:
      enabled: true
      # logLevel=debug surfaces controller informer events for failure
      # diagnosis; scoped to the e2e fixture only, production tenants stay
      # on the upstream chart default (info).
      valuesOverride:
        ouroboros:
          controller:
            logLevel: debug
YAML
)
  fi

  # Point worker DataVolume imports at the in-sandbox Talos OS image cache and
  # worker image pulls at the in-sandbox ghcr.io mirror when each is up (falls back
  # to the public defaults otherwise). Emitted right under spec: as
  # `talos: { imageFactoryURL: ..., registryMirrors: {...} }`, or nothing when both
  # defaults apply.
  local talos_block
  talos_block=$(talos_spec_block)

  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Kubernetes
metadata:
  name: "${test_name}"
  namespace: tenant-test
spec:
${talos_block}
  addons:
    certManager:
      enabled: false
      valuesOverride: {}
    cilium:
      valuesOverride: {}
    gatewayAPI:
      enabled: false
    gpuOperator:
      enabled: false
      valuesOverride: {}
    ingressNginx:
      enabled: true
      hosts: []
      valuesOverride: {}
    monitoringAgents:
      enabled: false
      valuesOverride: {}
${ouroboros_addon}
    verticalPodAutoscaler:
      valuesOverride: {}
  controlPlane:
    apiServer:
      resources: {}
      # Chart default (2 CPU / 2Gi), not a smaller override. The legacy "small"
      # preset caps the tenant apiserver at 512Mi, and a two-node tenant cluster
      # running the full addon set (cilium, coredns, metrics-server, csi,
      # ingress-nginx, VPA) opens enough watches to exceed that: the apiserver
      # is OOMKilled once the workers join, and every in-flight tenant
      # HelmRelease that is waiting on a DaemonSet rollout burns its whole
      # timeout while the control plane is restarting.
      resourcesPreset: c1.medium
    controllerManager:
      resources: {}
      resourcesPreset: micro
    konnectivity:
      server:
        resources: {}
        resourcesPreset: micro
    replicas: 2
    scheduler:
      resources: {}
      resourcesPreset: micro
  host: ""
  storageClass: replicated
  version: "${k8s_version}"
EOF

  # Worker node pool is a separate KubernetesNodes resource since the Phase 2
  # split. Named "<cluster>-md0" so it produces MachineDeployment
  # kubernetes-${test_name}-md0 — the same object the pre-split
  # spec.nodeGroups.md0 produced, which the waits below still key on. Carries
  # roles: [ingress-nginx] so the ingress-nginx addon has a node to schedule on.
  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: KubernetesNodes
metadata:
  name: "${test_name}-md0"
  namespace: tenant-test
spec:
  cluster: "${test_name}"
${talos_block}
  diskSize: 20Gi
  gpus: []
  # Sizing comes from resources below; this is here because the values
  # schema requires the field, and the chart drops the instancetype from
  # the VM whenever a pool sets both resources.cpu and resources.memory.
  instanceType: u1.medium
  # The failure this suite keeps hitting is a worker that stalls in the guest
  # before Talos apid answers, which leaves nothing to read on the management
  # side and nothing for talosctl to connect to. Turning this on attaches
  # KubeVirt's guest-console-log container, the only artifact that covers that
  # window.
  logSerialConsole: true
  maxReplicas: 10
  minReplicas: 2
  # Headroom above the vCPU count, where KubeVirt would otherwise put the
  # ceiling. Workers failing to join (cozystack/cozystack#3513) have been
  # measured burning their vCPU threads flat out at a ceiling equal to their
  # vCPU count while the guest kernel made no progress, which is what a vCPU
  # spinning on a lock looks like. Spare quota above the pair did not change
  # it: under a ceiling of 3 the pair kept burning the same 1.4 to 1.8 cores
  # with no progress, so the spin is not a quota artefact. A single vCPU
  # cannot spin on its sibling, which is what this shape is here to answer,
  # and the ceiling above the vCPU count keeps the emulator and IO threads
  # from eating into the one core the guest computes with. The request is
  # what keeps scheduling where it was: setDefaultResourceRequests, in
  # KubeVirt's VMI mutating webhook, copies a declared CPU limit into an
  # undeclared CPU request, so the limit alone would have every worker ask
  # the scheduler for its whole ceiling and sit Pending on Insufficient cpu.
  # Its value is the one KubeVirt derived for these workers before, from the
  # vCPU count and the cluster CPU allocation ratio.
  podCpuLimit: 2
  podCpuRequest: 100m
  # One vCPU at the memory the workers had before. Sized by resources
  # rather than by instanceType, which cannot carry the pod CPU pair above.
  resources:
    cpu: 1
    memory: 4Gi
  roles:
  - ingress-nginx
  storageClass: replicated
  version: "${k8s_version}"
EOF
  # Wait for the tenant-test namespace to be active
  kubectl wait namespace tenant-test --timeout=20s --for=jsonpath='{.status.phase}'=Active

  # Wait for the Kamaji control plane to be created. Under Flux v2.8
  # kstatus-based health checks helm-controller can take 20-30s to dispatch
  # the new Kubernetes HR before it renders the KamajiControlPlane CR; the
  # old 10s budget was tight on v2.7 and consistently fails on v2.8.
  timeout 2m sh -ec 'until kubectl get kamajicontrolplane -n tenant-test kubernetes-'"${test_name}"'; do sleep 1; done'

  # Wait for the tenant control plane to be fully created. Pre-Talos this
  # only spun up Kamaji core; after PR #2610 the apiserver pod also pulls
  # and starts the talos-csr-signer sidecar and cert-manager has to issue
  # the Talos PKI Certificates that gate the wait-for-kubeconfig init
  # container, so cold-start times in a fresh sandbox crossed the original
  # 4m budget. The 10m wait below sits well inside the
  # helm-install-timeout: 20m annotation that cozystack-api copies from
  # cozyrds onto the HR.
  kubectl_wait_retry --for=condition=TenantControlPlaneCreated kamajicontrolplane -n tenant-test kubernetes-${test_name} --timeout=10m

  # Wait for Kubernetes resources to be ready. Same rationale as the
  # TenantControlPlaneCreated wait above — Talos PKI issuing + sidecar
  # readiness probes shift the steady-state Ready point.
  kubectl_wait_retry tcp -n tenant-test kubernetes-${test_name} --timeout=10m --for=jsonpath='{.status.kubernetesResources.version.status}'=Ready

  # Wait for all required deployments to be available (timeout after 4 minutes)
  kubectl_wait_retry deploy --timeout=4m --for=condition=available -n tenant-test kubernetes-${test_name} kubernetes-${test_name}-cluster-autoscaler kubernetes-${test_name}-kccm kubernetes-${test_name}-kcsi-controller

  # Since the Phase 2 split the MachineDeployment is rendered by the CHILD
  # kubernetes-nodes-<cluster>-md0 HelmRelease -- a separately-dispatched
  # reconcile with its own 20-30s helm-controller latency (same figure as the
  # KCP note above), not by the parent install that produced the deployments
  # just waited on. A fast parent bringup therefore reaches this point BEFORE
  # the child HR has applied anything, and `kubectl wait` on an absent object
  # exits NotFound immediately instead of polling. Event-driven existence
  # backstop first, same sanctioned pattern as the kamajicontrolplane loop
  # above; the faster the parent, the more certain the race is lost without it.
  timeout 3m sh -ec 'until kubectl get machinedeployment -n tenant-test kubernetes-'"${test_name}"'-md0; do sleep 2; done'

  # Wait for the machine deployment to scale to 2 replicas. Pre-Talos this
  # was effectively instant because KubeadmConfigTemplate had no async
  # dependencies and CAPI/CAPK could create Machine + KubevirtMachine
  # immediately. Post-Talos the MD bootstrap.configRef gates on the
  # TalosConfigTemplate, which only renders once the lookup-gated Talos PKI
  # Secrets (talos-secrets, talos-ca, k8s ca, apiserver Service ClusterIP)
  # all exist; cold-start in a fresh CI sandbox pushes the time to first
  # MachineSet scale-up past the old 1m budget.
  kubectl_wait_retry machinedeployment kubernetes-${test_name}-md0 -n tenant-test --timeout=5m --for=jsonpath='{.status.replicas}'=2
  # Get the admin kubeconfig and save it to a file
  kubectl get secret kubernetes-${test_name}-admin-kubeconfig -ojsonpath='{.data.super-admin\.conf}' -n tenant-test | base64 -d > "tenantkubeconfig-${test_name}"

  # Expose the tenant Kubernetes API via a test-scoped LoadBalancer instead of
  # `kubectl port-forward`. The host cluster runs MetalLB on the same /24 as the
  # sandbox nodes (pool 192.168.123.200-250), so an LB IP is directly routable
  # from the test — the in-tenant LB test below already curls such an address.
  # Crucially, a LoadBalancer Service load-balances across ALL ready apiserver
  # endpoints (both Kamaji control-plane pods), so a single apiserver pod restart
  # is routed around transparently. `kubectl port-forward` instead pins to one
  # pod and dies when that pod blips: a lone kube-apiserver restart was observed
  # leaving localhost refusing connections for the whole node-Ready wait while
  # the cluster was in fact healthy (CAPI NodeHealthy=True on both nodes),
  # failing the test on a dead tunnel. The LB endpoint is also stable until
  # teardown, so the failure snapshot can still reach the tenant. Test-scoped and
  # additive — no change to the product Kamaji/Kubernetes chart.
  #
  # Clean up a stale LB from a previous failed retry of this same test first.
  kubectl -n tenant-test delete service "kubernetes-${test_name}-e2e-lb" --ignore-not-found --wait=false 2>/dev/null || true
  kubectl apply -n tenant-test -f - <<EOF
apiVersion: v1
kind: Service
metadata:
  name: kubernetes-${test_name}-e2e-lb
  labels:
    cozystack-e2e.io/tenant-api-lb: "${test_name}"
spec:
  type: LoadBalancer
  selector:
    kamaji.clastix.io/name: kubernetes-${test_name}
  ports:
  - name: kube-apiserver
    port: 6443
    targetPort: 6443
EOF
  # Wait for MetalLB to assign an external IP.
  timeout 90 sh -ec 'until [ -n "$(kubectl get svc -n tenant-test kubernetes-'"${test_name}"'-e2e-lb -o jsonpath="{.status.loadBalancer.ingress[0].ip}" 2>/dev/null)" ]; do sleep 2; done'
  TENANT_API_LB_IP=$(kubectl get svc -n tenant-test "kubernetes-${test_name}-e2e-lb" -o jsonpath='{.status.loadBalancer.ingress[0].ip}')
  if [ -z "${TENANT_API_LB_IP}" ]; then
    echo "tenant API LoadBalancer did not receive an IP" >&2
    exit 1
  fi

  # Point the kubeconfig at the LB IP. The MetalLB IP is not in the apiserver
  # serving-cert SANs, so skip TLS verification (e2e only — we functionally test
  # the cluster, not its serving identity) and drop the now-mismatched CA data
  # (kubectl rejects insecure-skip-tls-verify alongside certificate-authority).
  yq -i ".clusters[0].cluster.server = \"https://${TENANT_API_LB_IP}:6443\" | .clusters[0].cluster.\"insecure-skip-tls-verify\" = true | del(.clusters[0].cluster.\"certificate-authority-data\")" "tenantkubeconfig-${test_name}"

  # Wait for the API to answer through the LB before using it.
  timeout 60 sh -ec 'until kubectl --kubeconfig tenantkubeconfig-'"${test_name}"' get --raw /healthz >/dev/null 2>&1; do sleep 2; done'
  # The kubeconfig + LB are live now. Arm the tenant snapshot: any failure from
  # here on captures the tenant cluster (the LB endpoint stays up until teardown,
  # so crust-gather can reach it). Cleared on the success path below.
  CURRENT_TENANT_KC="tenantkubeconfig-${test_name}"
  trap '_tenant_snapshot_on_fail' EXIT
  # Verify the Kubernetes version matches what we expect (retry for up to 20 seconds)
  timeout 20 sh -ec 'until kubectl --kubeconfig tenantkubeconfig-'"${test_name}"' version 2>/dev/null | grep -Fq "Server Version: ${k8s_version}"; do sleep 1; done'

  # The canary goes first, outside the three counter pairs below rather than
  # among them, and it is a different kind of instrument from all three: it does
  # not read a running total twice, it runs a fixed amount of work and times it,
  # so each sample stands on its own and there is no interval for anything to
  # widen. What it does have is a cost, since it occupies a core for a couple of
  # seconds. That is why it sits out here. Taken between any of those first
  # readings and the wait, its burn would land inside the interval that pair
  # divides by: runner-kernel CPU time the pair would then charge to the join
  # window, and for the two counting the guests, a core taken away from what
  # they count; ahead of all three, it lands on neither side of any of them, and
  # the second sample sits behind all three second readings for the same reason.
  #
  # Both outcomes reach the job log rather than only the capture, and the second
  # for the same reason as the first: a run that took a reading the legend calls
  # pathological is the loudest thing this collector can say, and on the passing
  # path the report holding it is the artifact nobody downloads.
  if ! cozy_capture_runner_canary 1; then
    echo "» WARNING: the runner fixed-work canary produced no reading before the node-join wait, so this run has no measure of what one core got done at this layer going into it; if a capture was written it says whether the clock could not be read or the work itself failed, and if it could not be written the line above this one says so" >&2
  elif [ "${_COZY_CANARY_ALERT:-0}" -eq 1 ]; then
    echo "» WARNING: the runner fixed-work canary did not read inside the range its own legend calls healthy before the node-join wait, so this run went into the wait on a machine that was already not getting the work done; which arm it was and what figure it gave is written into the capture" >&2
  fi
  # The first readings of the three pairs that bracket the wait, taken here
  # rather than inside the failure block and taken on every run rather than on
  # the failing ones. All three read running totals, so one reading is an
  # average over a whole life and a rate needs two -- and the interval worth
  # measuring is the window the guest is failing to boot in, which is the wait
  # below. A pair taken a few seconds apart after the deadline has already
  # expired would price the tail instead of the fault.
  #
  # The KVM reading goes LAST here and FIRST after the wait, mirrored on
  # purpose: nothing then runs between its two samples except the wait itself,
  # which is what lets its legend call the interval the window and mean it
  # exactly. The other two name the sibling readings inside their intervals.
  #
  # Ungated and not fatal. Each is a bounded local read against a wait measured
  # in minutes, and a run that could not take them must still be allowed to go
  # on and fail for its own reasons.
  if ! cozy_capture_sandbox_qemu_thread_cpu 1; then
    echo "» WARNING: the sandbox VMs' QEMU threads yielded no reading before the node-join wait, so the reading taken after it will have nothing to pair with; whether the walk failed or this container had no QEMU process to read is written into the capture itself" >&2
  fi
  if ! cozy_capture_runner_kernel_cpu_time 1; then
    echo "» WARNING: the runner kernel's CPU time yielded no reading before the node-join wait, so the reading taken after it will have nothing to pair with; whether the read failed or returned no cpu row is written into the capture itself" >&2
  fi
  if ! cozy_capture_sandbox_kvm_exits 1; then
    echo "» WARNING: the sandbox kernel's KVM counters yielded no reading before the node-join wait, so the reading taken after it will have nothing to pair with; whether the read failed or the kernel had no counters to give is written into the capture itself, and any message the read left is in the log beside it" >&2
  fi
  # The status is kept rather than tested in place, because what this branch
  # does next depends on which non-zero it was and `if !` throws that away.
  local _join_rc=0
  timeout 29m bash -c '
    until [ "$(kubectl --kubeconfig "tenantkubeconfig-'"${test_name}"'" get nodes --no-headers 2>/dev/null | grep -cw Ready)" -ge 2 ]; do
      sleep 5
    done
  ' || _join_rc=$?
  if [ "${_join_rc}" -ne 0 ]; then
    # Node-join failed: fewer than 2 tenant nodes became Ready inside the 29m
    # deadline, or the wait itself could not run. Record which of the two it was,
    # dump scoped diagnostics that split the failure sub-modes, then fail — the
    # checks below need Ready nodes and would report their absence as their own
    # failure.
    #
    # Both cases exit 1, and that is the point: the suite stays red, so Chainsaw
    # runs its whole catch and the report says a test failed. What differs is
    # only whether a marker is left behind, and whether a lane reads that marker
    # is what decides if this particular red blocks a merge. The marker is written
    # ahead of the diagnostics rather than behind them because the diagnostics
    # spend minutes under a job cap that can end the run inside them.
    if cozy_node_join_deadline_expired "${_join_rc}"; then
      cozy_soft_red_node_join "${test_name}"
    else
      echo "the node-join wait did not run to its deadline (exit ${_join_rc}); this is the wait failing rather than the nodes, and it is not what the lane tolerates" >&2
    fi
    cozy_report_node_join_failure "${test_name}"
    exit 1
  fi
  # The second KVM counter reading on the path where the join succeeded, and it
  # belongs here rather than beside the console baseline at the end of the run.
  # The pair is only a measurement of the join window if both readings bracket
  # it, and the tail below this point is another ten to fifteen minutes of a
  # tenant cluster doing storage and LoadBalancer work: a sample taken after it
  # would price a different window over a different workload while the legend in
  # the capture says it priced the join. That would corrupt exactly the
  # comparison these counters exist for, since the green run is what the red
  # ones are read against. Placing it here also removes an orphan: a failure
  # between the wait and the end of the suite would otherwise leave the first
  # reading alone in the report, carrying an instruction to difference it
  # against a sibling that was never written.
  if ! cozy_capture_sandbox_kvm_exits 2; then
    echo "» WARNING: the sandbox kernel's KVM counters yielded no reading after the node-join wait, so this report carries no green baseline for the exit rate; whether the read failed or the kernel had no counters to give is written into the capture itself, and any message the read left is in the log beside it" >&2
  fi
  # The second half of each pair on the path where the join succeeded, beside the
  # KVM reading and for its reason: the pair is a measurement of the join window
  # only if both readings bracket it, and the tail below this point is another
  # ten to fifteen minutes of storage and LoadBalancer work. A sample taken after
  # that would price a different window over a different workload while the
  # legend in the capture says it priced the join -- which would corrupt exactly
  # the comparison these readings exist for, since the green run is what the red
  # ones are read against.
  if ! cozy_capture_runner_kernel_cpu_time 2; then
    echo "» WARNING: the runner kernel's CPU time yielded no reading after the node-join wait, so this report carries no green baseline for what the layer above the sandbox spent; whether the read failed or returned no cpu row is written into the capture itself" >&2
  fi
  if ! cozy_capture_sandbox_qemu_thread_cpu 2; then
    echo "» WARNING: the sandbox VMs' QEMU threads yielded no reading after the node-join wait, so this report carries no green baseline for how that time split across the three nodes; whether the walk failed or this container had no QEMU process to read is written into the capture itself" >&2
  fi
  # The canary's second sample, last on this path as it was first before the
  # wait. Behind all three second readings deliberately: it burns a core, and
  # taken ahead of any of them that burn would sit inside the interval the pair
  # divides by. It is also the half of the pair with the green figure in it --
  # the expected ranges the capture states are what a red run is read against
  # when there is no green run to compare it with, and a green run that records
  # its own numbers is what says whether those ranges describe this lane.
  #
  # Ungated, unlike the console capture further down this path, and the reason is
  # not that the rule there does not apply. That rule is for a collector costing
  # minutes; both arms here sit under one ceiling apiece and the pair is seconds.
  if ! cozy_capture_runner_canary 2; then
    echo "» WARNING: the runner fixed-work canary produced no reading after the node-join wait, so this report carries no green figure for what one core gets done at this layer; if a capture was written it says whether the clock could not be read or the work itself failed, and if it could not be written the line above this one says so" >&2
  elif [ "${_COZY_CANARY_ALERT:-0}" -eq 1 ]; then
    echo "» WARNING: the runner fixed-work canary did not read inside the range its own legend calls healthy after the node-join wait, so this run passed on a machine that was not getting the work done at this layer; which arm it was and what figure it gave is written into the capture" >&2
  fi
  kubectl --kubeconfig "tenantkubeconfig-${test_name}" get nodes -o wide

  # Verify the kubelet version matches what we expect
  versions=$(kubectl --kubeconfig "tenantkubeconfig-${test_name}" \
    get nodes -o jsonpath='{.items[*].status.nodeInfo.kubeletVersion}')

  node_ok=true

  for v in $versions; do
    case "$v" in
      "${k8s_version}" | "${k8s_version}".* | "${k8s_version}"-*)
        # acceptable
        ;;
      *)
        node_ok=false
        break
        ;;
    esac
  done

  if [ "$node_ok" != true ]; then
    echo "Kubelet versions did not match expected ${k8s_version}" >&2
    exit 1
  fi


  kubectl --kubeconfig "tenantkubeconfig-${test_name}" apply -f - <<EOF
apiVersion: v1
kind: Namespace
metadata:
  name: tenant-test
EOF

  # Clean up backend resources from any previous failed attempt
  kubectl delete deployment --kubeconfig "tenantkubeconfig-${test_name}" "${test_name}-backend" \
    -n tenant-test --ignore-not-found --timeout=60s || true
  kubectl delete service --kubeconfig "tenantkubeconfig-${test_name}" "${test_name}-backend" \
    -n tenant-test --ignore-not-found --timeout=60s || true

  # Start the workload's clock from a node that accepts Pods, not from a node
  # that is merely Ready. Both instances of run 31020254620 spent 2m18s and
  # 1m57s of the 300s readiness budget below on FailedScheduling, against nodes
  # that were Ready but carried `node.cilium.io/agent-not-ready` or were
  # SchedulingDisabled, and then ran out while the image was still being
  # pulled. This gate is not extra waiting on the happy path: it spends the
  # seconds the Pod would otherwise spend Pending (plus at most one 5s poll
  # interval) and moves them out of a budget that has a different job. 300s is
  # more than twice the longest scheduling delay observed, and the gate prints
  # the node table on both outcomes so a timeout names the taint that held it.
  # The two budgets do stack on a failing run: one that spends the full 300s
  # here and then overruns the readiness wait gives up at ~600s where it used
  # to give up at 300s. That sits inside the enclosing 40m Chainsaw script op,
  # which the kubernetes-* suites document as a ~25m bringup.
  if ! cozy_wait_schedulable_node "tenantkubeconfig-${test_name}" 300; then
    echo "=== tenant scheduling gate failed: no node became schedulable within 300s — diagnostics follow ==="
    # Bounded like the node-join reads, and for the same reason: this branch also
    # ends in the `exit 1` that triggers the tenant snapshot, and the node table
    # is read through the tenant kubeconfig — a nodes-Ready-but-unschedulable
    # tenant is one whose apiserver may or may not answer.
    #
    # Validated here for the same reason cozy_report_node_join_failure validates
    # before composing its own string: the `--request-timeout` below is interpolated
    # when these arguments are expanded, which is before cozy_diag_read gets to
    # re-check the global. A post-source `8m` would otherwise send
    # `--request-timeout=8ms` and lose the node table -- the one thing this branch
    # exists to print -- while the note blamed the cluster.
    COZY_DIAG_READ_TIMEOUT=$(_cozy_diag_seconds "${COZY_DIAG_READ_TIMEOUT-}" "$COZY_DIAG_READ_TIMEOUT_DEFAULT" COZY_DIAG_READ_TIMEOUT positive)
    cozy_diag_read 'tenant node table' \
      kubectl --kubeconfig "tenantkubeconfig-${test_name}" describe nodes \
      "--request-timeout=${COZY_DIAG_READ_TIMEOUT}s"
    cozy_diag_read 'tenant HelmReleases' \
      kubectl -n tenant-test get hr "--request-timeout=${COZY_DIAG_READ_TIMEOUT}s"
    exit 1
  fi

  # Backend 1
  #
  # nginx is pinned by digest. The tenant workers reach no registry mirror --
  # hack/e2e-talos-image-cache.yaml serves the Talos worker OS disk image over
  # HTTP and is not one, and nothing else in the tree mirrors container images
  # for a tenant -- so this is pulled from Docker Hub on every run either way.
  # The digest does not remove that pull, it fixes what the pull returns: a
  # floating `nginx:alpine` silently changes size and layer count under the
  # readiness budget below, and supplies whatever content the tag points at on
  # the day. The digest is the OCI index, not a per-architecture manifest, so
  # the kubelet still selects the image for the worker's own architecture.
  # Nothing will bump it: renovate's enabledManagers are gomod, dockerfile,
  # github-actions and custom.regex, and both custom managers match packages/
  # paths only, so no manager reads this file. That is the intent rather than an
  # oversight -- the point of the pin is that the bytes stay the same run to
  # run, and this is a throwaway test workload, not an image the platform ships.
  kubectl apply --kubeconfig "tenantkubeconfig-${test_name}" -f- <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: "${test_name}-backend"
  namespace: tenant-test
spec:
  replicas: 1
  selector:
    matchLabels:
      app: backend
      backend: "${test_name}-backend"
  template:
    metadata:
      labels:
        app: backend
        backend: "${test_name}-backend"
    spec:
      containers:
      - name: nginx
        image: nginx:1.31.3-alpine@sha256:4a73073bd557c65b759505da037898b61f1be6cbcc3c2c3aeac22d2a470c1752
        ports:
        - containerPort: 80
        readinessProbe:
          httpGet:
            path: /
            port: 80
          initialDelaySeconds: 2
          periodSeconds: 2
EOF

  # LoadBalancer Service
  kubectl apply --kubeconfig "tenantkubeconfig-${test_name}" -f- <<EOF
apiVersion: v1
kind: Service
metadata:
  name: "${test_name}-backend"
  namespace: tenant-test
spec:
  type: LoadBalancer
  selector:
    app: backend
    backend: "${test_name}-backend"
  ports:
  - port: 80
    targetPort: 80
EOF

  # Wait for pods readiness. With scheduling gated above, these 300s start from
  # a node that accepts Pods, so they cover placement, sandbox setup, the image
  # pull and the probe -- and no longer the wait for a node to stop rejecting
  # the Pod, which now has a budget and a message of its own. The events in the
  # diagnostics below tell the remaining consumers apart. The number is
  # unchanged from when it also had to absorb scheduling; the pull alone
  # measured 1m42s in run 31020254620.
  if ! kubectl wait deployment --kubeconfig "tenantkubeconfig-${test_name}" "${test_name}-backend" -n tenant-test --for=condition=Available --timeout=300s; then
    echo "=== backend readiness failed: the Pod was schedulable but did not become Available within 300s — diagnostics follow ==="
    kubectl --kubeconfig "tenantkubeconfig-${test_name}" -n tenant-test describe deployment "${test_name}-backend" || true
    kubectl --kubeconfig "tenantkubeconfig-${test_name}" -n tenant-test describe pods -l "backend=${test_name}-backend" || true
    kubectl --kubeconfig "tenantkubeconfig-${test_name}" -n tenant-test get events --sort-by=.lastTimestamp || true
    exit 1
  fi

  # Wait for LoadBalancer to be provisioned (IP or hostname)
  timeout 90 sh -ec "
    until kubectl get svc ${test_name}-backend --kubeconfig tenantkubeconfig-${test_name} -n tenant-test \
      -o jsonpath='{.status.loadBalancer.ingress[0]}' | grep -q .; do
      sleep 5
    done
  "

  LB_ADDR=$(
    kubectl get svc --kubeconfig "tenantkubeconfig-${test_name}" "${test_name}-backend" \
      -n tenant-test \
      -o jsonpath='{.status.loadBalancer.ingress[0].ip}{.status.loadBalancer.ingress[0].hostname}'
  )

  if [ -z "$LB_ADDR" ]; then
    echo "LoadBalancer address is empty" >&2
    exit 1
  fi

  # TODO(e2e-replace-fixed-timeouts): genuine retry loop. This validates an
  # external HTTP path (MetalLB-advertised LB IP -> in-tenant ingress ->
  # backend pod) which is not visible to the Kubernetes API as a single
  # condition, so kubectl wait cannot replace it. The 20x3s = 60s budget is
  # capped with `lb_ok=false` then asserted below.
  lb_ok=false
  for i in $(seq 1 20); do
    echo "Attempt $i"
    if curl --silent --fail "http://${LB_ADDR}"; then
      lb_ok=true
      break
    fi
    sleep 3
  done

  if [ "$lb_ok" != true ]; then
    echo "LoadBalancer not reachable" >&2
    exit 1
  fi

  # Cleanup
  kubectl delete deployment --kubeconfig "tenantkubeconfig-${test_name}" "${test_name}-backend" -n tenant-test
  kubectl delete service --kubeconfig "tenantkubeconfig-${test_name}" "${test_name}-backend" -n tenant-test

  # Block until csi.kubevirt.io is registered on the tenant worker CSINode.
  # Otherwise the NFS pod schedules while kubevirt-csi-node DaemonSet is
  # still rolling out, eats ~1m on FailedAttachVolume retries, and trips
  # the 5m pod-Succeeded budget when containerd's CreateContainer stalls.
  kubectl wait hr -n tenant-test "kubernetes-${test_name}-csi" --timeout=10m --for=condition=ready

  # ----------------------------------------------------------------------
  # StorageClass propagation (issue #2094). Remote-accessible LINSTOR infra
  # classes propagate to the tenant under the same name; node-local classes
  # ("local", allowRemoteVolumeAccess=false) are filtered out; the legacy
  # "kubevirt" alias is retained for backward compatibility. The e2e infra
  # cluster ships both "replicated" (remote) and "local" (node-local).
  # ----------------------------------------------------------------------
  echo "Verifying StorageClass propagation to tenant..."
  timeout 2m bash -c '
    until kubectl --kubeconfig tenantkubeconfig-'"${test_name}"' get sc replicated >/dev/null 2>&1; do
      sleep 5
    done
  '

  rep_prov=$(kubectl --kubeconfig "tenantkubeconfig-${test_name}" get sc replicated -o jsonpath='{.provisioner}')
  rep_infra=$(kubectl --kubeconfig "tenantkubeconfig-${test_name}" get sc replicated -o jsonpath='{.parameters.infraStorageClassName}')
  if [ "$rep_prov" != "csi.kubevirt.io" ] || [ "$rep_infra" != "replicated" ]; then
    echo "replicated SC misconfigured: provisioner=$rep_prov infraStorageClassName=$rep_infra" >&2
    kubectl --kubeconfig "tenantkubeconfig-${test_name}" get sc >&2
    exit 1
  fi

  # Legacy kubevirt alias must still exist (existing PVCs depend on it).
  if ! kubectl --kubeconfig "tenantkubeconfig-${test_name}" get sc kubevirt >/dev/null 2>&1; then
    echo "legacy kubevirt StorageClass alias is missing" >&2
    exit 1
  fi

  # Node-local "local" class must NOT be propagated (allowRemoteVolumeAccess=false).
  if kubectl --kubeconfig "tenantkubeconfig-${test_name}" get sc local >/dev/null 2>&1; then
    echo "node-local StorageClass 'local' should not be propagated to the tenant" >&2
    exit 1
  fi

  # Exactly one default StorageClass, and it must be "replicated".
  default_scs=$(kubectl --kubeconfig "tenantkubeconfig-${test_name}" get sc \
    -o jsonpath='{range .items[?(@.metadata.annotations.storageclass\.kubernetes\.io/is-default-class=="true")]}{.metadata.name}{"\n"}{end}')
  default_count=$(printf '%s' "$default_scs" | grep -c .)
  if [ "$default_count" -ne 1 ] || [ "$default_scs" != "replicated" ]; then
    echo "expected exactly one default StorageClass 'replicated', got: ${default_scs:-<none>} (count=$default_count)" >&2
    exit 1
  fi
  echo "StorageClass propagation OK (replicated default, kubevirt alias present, local filtered)"

  # Clean up NFS test resources from any previous failed attempt
  kubectl --kubeconfig "tenantkubeconfig-${test_name}" delete pod nfs-test-pod \
    -n tenant-test --ignore-not-found --timeout=60s || true
  kubectl --kubeconfig "tenantkubeconfig-${test_name}" delete pvc nfs-test-pvc \
    -n tenant-test --ignore-not-found --timeout=60s || true

  # Test RWX NFS mount in tenant cluster (uses kubevirt CSI driver with RWX support)
  kubectl --kubeconfig "tenantkubeconfig-${test_name}" apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: nfs-test-pvc
  namespace: tenant-test
spec:
  accessModes:
  - ReadWriteMany
  storageClassName: kubevirt
  resources:
    requests:
      storage: 1Gi
EOF

  # Wait for PVC to be bound (RWX via kubevirt CSI provisions an NFS server pod, needs time)
  kubectl --kubeconfig "tenantkubeconfig-${test_name}" wait pvc nfs-test-pvc -n tenant-test --timeout=3m --for=jsonpath='{.status.phase}'=Bound

  # Create Pod that writes and reads data from NFS volume
  kubectl --kubeconfig "tenantkubeconfig-${test_name}" apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: nfs-test-pod
  namespace: tenant-test
spec:
  containers:
  - name: test
    image: busybox
    command: ["sh", "-c", "echo 'nfs-mount-ok' > /data/test.txt && cat /data/test.txt"]
    volumeMounts:
    - name: nfs-vol
      mountPath: /data
  volumes:
  - name: nfs-vol
    persistentVolumeClaim:
      claimName: nfs-test-pvc
  restartPolicy: Never
EOF

  # 10m, not 5m: host CDI prime PVC + tenant CSI mount + busybox pull worst-case bursts past 5m.
  if ! kubectl --kubeconfig "tenantkubeconfig-${test_name}" wait pod nfs-test-pod -n tenant-test --timeout=10m --for=jsonpath='{.status.phase}'=Succeeded; then
    echo "=== NFS test pod did not complete ===" >&2
    kubectl --kubeconfig "tenantkubeconfig-${test_name}" describe pod nfs-test-pod -n tenant-test >&2 || true
    kubectl --kubeconfig "tenantkubeconfig-${test_name}" get events -n tenant-test --sort-by='.lastTimestamp' >&2 || true
    exit 1
  fi

  # Verify NFS data integrity
  nfs_result=$(kubectl --kubeconfig "tenantkubeconfig-${test_name}" logs nfs-test-pod -n tenant-test)
  if [ "$nfs_result" != "nfs-mount-ok" ]; then
    echo "NFS mount test failed: expected 'nfs-mount-ok', got '$nfs_result'" >&2
    kubectl --kubeconfig "tenantkubeconfig-${test_name}" delete pod nfs-test-pod -n tenant-test --wait=false 2>/dev/null || true
    kubectl --kubeconfig "tenantkubeconfig-${test_name}" delete pvc nfs-test-pvc -n tenant-test --wait=false 2>/dev/null || true
    exit 1
  fi

  # Cleanup NFS test resources in tenant cluster
  kubectl --kubeconfig "tenantkubeconfig-${test_name}" delete pod nfs-test-pod -n tenant-test --wait
  kubectl --kubeconfig "tenantkubeconfig-${test_name}" delete pvc nfs-test-pvc -n tenant-test

  # Wait for all machine deployment replicas to be ready (timeout after 10 minutes)
  kubectl wait machinedeployment kubernetes-${test_name}-md0 -n tenant-test --timeout=10m --for=jsonpath='{.status.v1beta2.readyReplicas}'=2

  for component in cilium coredns csi vsnap-crd; do
      kubectl wait hr "kubernetes-${test_name}-${component}" -n tenant-test --timeout=5m --for=condition=ready
    done
    kubectl wait hr "kubernetes-${test_name}-ingress-nginx" -n tenant-test --timeout=5m --for=condition=ready

  # Optional ouroboros addon assertions. Folded in from the standalone
  # ouroboros.bats so the test reuses this cluster instead of spinning up a
  # second ~25m Kamaji bringup. The assertions cover: HR Ready, controller
  # pod Running, Ingress->coredns-custom rewrite line injection, and the
  # end-to-end DNS resolution proof from inside the tenant cluster.
  if [ "${enable_ouroboros}" = "true" ]; then
    kubectl wait hr "kubernetes-${test_name}-ouroboros" -n tenant-test \
      --timeout=10m --for=condition=ready

    # cozystack coredns wrapper renders an empty coredns-custom ConfigMap in
    # kube-system; the ouroboros controller writes the rewrite snippet into
    # its ouroboros.override key.
    kubectl --kubeconfig "tenantkubeconfig-${test_name}" -n kube-system \
      get configmap coredns-custom

    # Upstream chart ships no readiness probe — wait covers pod Running only;
    # the rewrite-snippet check below is the real reconciliation assertion.
    kubectl --kubeconfig "tenantkubeconfig-${test_name}" -n cozy-ouroboros \
      wait pod --selector=app.kubernetes.io/component=controller \
      --timeout=5m --for=condition=ready

    local hairpin_host=hairpin-cozystack-e2e.example.invalid
    kubectl --kubeconfig "tenantkubeconfig-${test_name}" -n default apply -f - <<EOF
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: hairpin-probe
spec:
  ingressClassName: nginx
  tls:
    - hosts:
        - ${hairpin_host}
      secretName: hairpin-probe-tls
  rules:
    - host: ${hairpin_host}
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: hairpin-probe
                port:
                  number: 80
EOF

    # Poll the import ConfigMap for the rewrite line. Dump-the-whole-map
    # form avoids the silent-empty kubectl jsonpath bracket-notation trap
    # on ConfigMap keys with dots (e.g. ouroboros.override).
    local deadline=$(( $(date +%s) + 300 ))
    local snippet=
    while [ "$(date +%s)" -lt "${deadline}" ]; do
      snippet=$(kubectl --kubeconfig "tenantkubeconfig-${test_name}" -n kube-system \
        get configmap coredns coredns-custom \
        -o 'jsonpath={range .items[*]}{.metadata.name}{"\n"}{.data}{"\n---\n"}{end}' \
        2>/dev/null || true)
      if echo "${snippet}" | grep -q "rewrite name ${hairpin_host}"; then break; fi
      sleep 5
    done
    if ! echo "${snippet}" | grep -q "rewrite name ${hairpin_host}"; then
      echo "ouroboros rewrite snippet for ${hairpin_host} not written to coredns-custom within 5m" >&2
      kubectl --kubeconfig "tenantkubeconfig-${test_name}" -n cozy-ouroboros \
        logs --selector=app.kubernetes.io/component=controller --tail=200 --all-containers || true
      exit 1
    fi

    # End-to-end proof: resolve the hairpin host from inside the tenant.
    # CoreDNS reload-period default is 30s, so the in-pod loop is needed.
    local proxy_ip
    proxy_ip=$(kubectl --kubeconfig "tenantkubeconfig-${test_name}" -n cozy-ouroboros \
      get service ouroboros-proxy -o jsonpath='{.spec.clusterIP}' 2>/dev/null || true)
    if [ -z "${proxy_ip}" ]; then
      echo "ouroboros-proxy Service has no ClusterIP" >&2
      exit 1
    fi
    # The DNS resolution itself is asserted EXACTLY ONCE and fail-fast, per the
    # e2e no-retry rule (docs/agents/e2e-testing.md #1: never retry a step that
    # carries product/test logic): a probe that runs to phase Failed means the
    # in-Pod dig loop (120s, with its own retries -- the right place for CoreDNS
    # eventual-consistency tolerance) never resolved the hairpin host to the
    # proxy, i.e. the reconciliation regression this assertion exists to catch,
    # and it fails the test immediately.
    #
    # The one thing recreated is the *vehicle*, and only for the pure-infra event
    # the same rule carves out (worker-VM boot/recycle). On the single-node
    # sandbox a tenant worker node can lose its kubelet heartbeat, and the CAPI
    # MachineHealthCheck deletes its Machine/KubeVirt-VM/Node after 30s and
    # provisions a replacement. A `--restart=Never` probe bound to that node is
    # removed by the node controller and, being a bare Pod, never recreated -- so
    # its verdict is destroyed by infrastructure before it is produced (typically
    # while its image is still pulling and it has never left Pending). A single-
    # shot probe reported that as a DNS failure ("last seen: <empty>"), which is
    # the observed flake (it hits PRs that don't touch virt at all). Recreate the
    # probe only when it vanished AND the node it was on is confirmed recycled
    # (gone or no longer Ready). A probe that disappears while its node is still
    # Ready is NOT infra churn -- it is an unexpected deletion -- and fails loud
    # rather than being retried, so no pod-churn regression is masked.
    local hairpin_deadline=$(( $(date +%s) + 420 ))
    local phase=
    local probe_node=
    local attempt=0
    local exists=
    local raw=
    local node=
    local node_ready=
    while [ "$(date +%s)" -lt "${hairpin_deadline}" ]; do
      attempt=$(( attempt + 1 ))
      # delete defaults to --wait=true, so it returns only once any stale Pod is
      # fully gone; the subsequent run cannot race an AlreadyExists.
      kubectl --kubeconfig "tenantkubeconfig-${test_name}" -n default \
        delete pod dnscheck --ignore-not-found 2>/dev/null || true
      kubectl --kubeconfig "tenantkubeconfig-${test_name}" -n default \
        run dnscheck --image=nicolaka/netshoot:v0.13 --restart=Never \
        --command -- sh -c "
          deadline=\$(( \$(date +%s) + 120 ))
          while [ \"\$(date +%s)\" -lt \"\${deadline}\" ]; do
            addr=\$(dig +short +tries=2 +time=5 ${hairpin_host} | head -n 1)
            echo \"resolved: \${addr:-<empty>}\"
            if [ \"\${addr}\" = \"${proxy_ip}\" ]; then
              exit 0
            fi
            sleep 5
          done
          echo \"timed out waiting for ${hairpin_host} to resolve to ${proxy_ip}\"
          exit 1
        "
      # Wait for THIS Pod to reach a terminal phase or vanish, remembering the
      # node it landed on so a later disappearance can be attributed (or not) to
      # that node being recycled. One get returns both fields; on NotFound it
      # errors and yields an empty phase.
      phase=
      probe_node=
      while [ "$(date +%s)" -lt "${hairpin_deadline}" ]; do
        raw=$(kubectl --kubeconfig "tenantkubeconfig-${test_name}" -n default \
          get pod dnscheck -o jsonpath='{.status.phase}@{.spec.nodeName}' 2>/dev/null || true)
        phase=${raw%%@*}
        node=${raw##*@}
        [ -n "${node}" ] && probe_node=${node}
        case "${phase}" in
          Succeeded|Failed) break ;;
        esac
        # Empty phase: either the Pod is gone or the tenant API had a transient
        # error. Only a clean query that definitively reports no such Pod (rc 0
        # under --ignore-not-found, empty output) is a candidate node recycle; a
        # transient API error exits nonzero and must NOT be read as a deleted
        # Pod, so keep polling. The `if var=$(...)` form keeps the nonzero rc
        # from tripping errexit (the whole test runs under `set -eu`).
        if [ -z "${phase}" ] \
          && exists=$(kubectl --kubeconfig "tenantkubeconfig-${test_name}" -n default \
               get pod dnscheck --ignore-not-found -o name 2>/dev/null) \
          && [ -z "${exists}" ]; then
          phase=Gone
          break
        fi
        sleep 3
      done

      case "${phase}" in
        Succeeded)
          break
          ;;
        Failed)
          # The Pod ran and its dig loop exhausted 120s without resolving the
          # hairpin host to the proxy: a genuine DNS/reconciliation failure.
          echo "dnscheck ran but ${hairpin_host} never resolved to ${proxy_ip} (attempt ${attempt})" >&2
          kubectl --kubeconfig "tenantkubeconfig-${test_name}" -n default \
            logs dnscheck 2>&1 | sed 's/^/  dnscheck: /' || true
          exit 1
          ;;
        Gone)
          # The probe vanished before producing a verdict. Recreate it only if
          # this was the pure-infra node recycle: its node must be gone or no
          # longer Ready. `get node` erroring (node deleted) short-circuits the
          # && so we fall through to retry; a still-Ready node means an
          # unexpected deletion, which fails loud rather than being retried.
          if [ -z "${probe_node}" ]; then
            echo "dnscheck vanished before it was scheduled to any node -- not a node recycle" >&2
            exit 1
          fi
          if node_ready=$(kubectl --kubeconfig "tenantkubeconfig-${test_name}" \
               get node "${probe_node}" \
               -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null) \
             && [ "${node_ready}" = "True" ]; then
            echo "dnscheck disappeared while its node ${probe_node} was still Ready -- unexpected pod deletion, not a node recycle" >&2
            exit 1
          fi
          echo "» dnscheck attempt ${attempt}: node ${probe_node} was recycled (gone/NotReady) before the probe completed -- retrying on a surviving node" >&2
          ;;
        *)
          # Deadline reached with the Pod still Pending (never ran, never gone):
          # the outer loop exits; the post-loop check reports it.
          :
          ;;
      esac
    done
    if [ "${phase}" != "Succeeded" ]; then
      echo "dnscheck did not resolve ${hairpin_host} to ${proxy_ip} within the deadline (last phase: ${phase:-<empty>}, attempts: ${attempt})" >&2
      kubectl --kubeconfig "tenantkubeconfig-${test_name}" -n default \
        logs dnscheck 2>&1 | sed 's/^/  dnscheck: /' || true
      exit 1
    fi

    kubectl --kubeconfig "tenantkubeconfig-${test_name}" -n default \
      delete pod dnscheck --ignore-not-found 2>/dev/null || true
    kubectl --kubeconfig "tenantkubeconfig-${test_name}" -n default \
      delete ingress hairpin-probe --ignore-not-found 2>/dev/null || true
  fi

  # Wait for the parent kubernetes-${test_name} HR to be Ready before the
  # remediation guard runs. The guard reads `.status.history`, which is empty
  # until the helm install action completes — under Flux v2.8 kstatus the
  # parent's helm install can still be "Running 'install'" after every child
  # HR (cilium, coredns, csi, vsnap-crd, ingress-nginx) is already Ready,
  # because kstatus walks all applied resources before flipping the parent
  # Ready.
  kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=5m --for=condition=ready

  # Guard: parent HelmRelease must not have entered an install/upgrade remediation cycle.
  # A non-zero installFailures/upgradeFailures indicates the helm-wait budget expired while
  # admin-kubeconfig was still being provisioned, which would trigger uninstall remediation
  # and churn the Cluster CR.
  # Flux helm-controller v2 retains per-revision release Snapshots in
  # .status.history; each Snapshot's .status reflects the Helm release
  # state (deployed/superseded/failed/uninstalled). A remediation cycle
  # leaves a "failed" or "uninstalled" entry behind that survives a later
  # successful reinstall, unlike the installFailures/upgradeFailures
  # counters (which ClearFailures zeroes on every successful reconcile).
  # The shape is pinned by hack/remediation-guard.bats; the upstream
  # types are github.com/fluxcd/helm-controller/api v2 Snapshot.
  history_statuses=$(kubectl get hr -n tenant-test "kubernetes-${test_name}" \
    -ojsonpath='{range .status.history[*]}{.status}{"\n"}{end}')
  # Always emit the raw value so a silent future-Flux field rename shows
  # up as "empty history on a Ready HR" in CI logs rather than vanishing.
  echo "Parent HelmRelease history statuses:"
  printf '%s\n' "${history_statuses:-<empty>}"
  if [ -z "${history_statuses}" ]; then
    echo "Unexpected empty .status.history on a Ready HelmRelease - Flux API shape may have changed." >&2
    kubectl -n tenant-test describe hr "kubernetes-${test_name}" >&2
    exit 1
  fi
  if helmrelease_has_remediation_cycle "${history_statuses}"; then
    echo "Parent HelmRelease entered remediation cycle." >&2
    kubectl -n tenant-test describe hr "kubernetes-${test_name}" >&2
    exit 1
  fi

  # Last, after everything the suite exists to prove. This checks a debugging
  # aid, not the product: a worker boots and serves either way, so letting it
  # run first would let a KubeVirt-side change to a diagnostic preempt the
  # assertions that actually cover the tenant cluster. It still runs on the
  # passing path rather than beside the collector, because the collector only
  # runs when node-join fails and would learn the setting was inert in the one
  # run where the console can no longer be recovered. Exit 2 alone fails the
  # suite; see cozy_assert_guest_console_attached for why its three outcomes
  # are not interchangeable.
  echo "» verifying the tenant worker guest console container attached"
  attach_rc=0
  cozy_assert_guest_console_attached || attach_rc=$?
  if [ "${attach_rc}" -eq 2 ]; then
    exit 1
  fi

  # The same capture the failure path takes, on the run that succeeded. Every
  # console this tree has collected came from a failure, so what a healthy
  # worker's boot looks like -- which stages it goes through and how long each
  # takes -- has never been measured, and "slower than usual" is the claim the
  # node-join failure keeps resting on. This is where the value it is compared
  # against comes from, and it exists only until the tenant below is deleted:
  # the container holding the stream goes with the Pod, and no read recovers it
  # afterwards.
  #
  # Not gated by the diagnostics phase budget, which covers the failure path and
  # the snapshot behind it. Every read here is bounded individually and none of
  # them as a group, so the honest ceiling is the listing, one read per Pod at
  # the cap this caller asks for, the two Pod-state reads a silent console
  # triggers and the start-time read -- six reads at the 30s bound and 5s kill
  # grace, so 210s, against a run that has finished everything it exists to
  # prove and typically has minutes of its operation left. The wedge check is
  # not in that count: it belongs to the failure path, and this caller reaches
  # here only after the attach check above has already answered the question it
  # asks. Asking for the pool's minimum rather than the pool is what keeps that
  # number small.
  #
  # It is still declined rather than attempted when the operation no longer has
  # room for it, which is what keeps "must not fail the suite" true of a
  # collector whose cost is real: everything above has passed by here, so a run
  # killed at the op ceiling while collecting a debugging aid would report the
  # suite as broken on the strength of the aid. Where the reserve comes from is
  # written beside the constant rather than repeated here.
  if cozy_green_capture_has_room 'the tenant worker guest console baseline'; then
    echo "» capturing the tenant worker guest console before teardown"
    if ! cozy_capture_tenant_serial_console 'the suite passed: captured after the last assertion and before tenant teardown' 2; then
      # Said in the job log rather than only in the report, because a green run
      # is the one nobody opens the report for. This branch covers the two setup
      # failures the capture returns non-zero for, the Pod listing failing and
      # no virt-launcher Pod matching; a walk that ran and came back with empty
      # consoles returns zero and is reported by the capture's own line about
      # how many said nothing. It does not fail the suite: this captures a
      # debugging aid, and everything the suite exists to prove has already
      # passed by here.
      echo "» WARNING: the guest console capture failed on a passing run; the boot baseline is missing from this report, see the notes beside the capture" >&2
    fi
  fi

  # Success: disarm the tenant-snapshot trap so it doesn't fire on the clean exit.
  trap - EXIT
  # Clean up: delete the test-scoped tenant API LoadBalancer (frees its MetalLB
  # IP) and the local kubeconfig.
  kubectl -n tenant-test delete service "kubernetes-${test_name}-e2e-lb" --ignore-not-found --wait=false 2>/dev/null || true
  rm -f "tenantkubeconfig-${test_name}"
  # Delete the worker pool (KubernetesNodes) before the parent Kubernetes CR so
  # its pre-delete unpin hook runs and the child HelmRelease does not leak.
  kubectl -n tenant-test delete kubernetesnodeses.apps.cozystack.io "${test_name}-md0" --ignore-not-found --wait=false 2>/dev/null || true
  kubectl -n tenant-test delete kuberneteses.apps.cozystack.io "${test_name}" --ignore-not-found --wait=false 2>/dev/null || true

}

# B1 regression coverage (PR #2872 review). The tenant's default StorageClass
# must be chosen among the *propagated* classes and must never be the legacy
# "kubevirt" alias -- even when the management cluster exposes only remote
# LINSTOR classes whose names sort alphabetically after "kubevirt" and none is
# named the configured storageClass (default "replicated"). That is the
# feature's own multi-tier target configuration. A regressed `sortAlpha | first`
# over a candidate set that still contained the inserted "kubevirt" alias would
# pick it, pointing the tenant default at an infra class absent on the
# management cluster -> default PVCs stay Pending with no error surfaced.
#
# helm-unittest cannot reach this branch: with no live cluster Helm `lookup`
# returns empty, so the storageClasses map always collapses to the "replicated"
# fallback (see packages/apps/kubernetes/tests/csi_test.yaml). It is therefore
# exercised here against the live management cluster with a single server-side
# dry-run render (helm v4 executes `lookup` against the API): add two remote
# LINSTOR classes that sort after "kubevirt", remove "replicated" for the one
# render, restore it immediately, then assert on the rendered -csi HelmRelease's
# storageClasses map.
verify_storageclass_fallback_default() {
  echo "Verifying tenant default StorageClass selection with no 'replicated' class (PR #2872 B1 regression)..."

  # Pre-cleanup: drop probe classes leaked by a previous failed run.
  kubectl delete sc nvme ssd --ignore-not-found

  # Two remote-accessible LINSTOR classes whose names sort AFTER "kubevirt".
  kubectl apply -f - <<'EOF'
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: nvme
provisioner: linstor.csi.linbit.com
parameters:
  linstor.csi.linbit.com/storagePool: "data"
  linstor.csi.linbit.com/allowRemoteVolumeAccess: "true"
volumeBindingMode: Immediate
allowVolumeExpansion: true
---
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: ssd
provisioner: linstor.csi.linbit.com
parameters:
  linstor.csi.linbit.com/storagePool: "data"
  linstor.csi.linbit.com/allowRemoteVolumeAccess: "true"
volumeBindingMode: Immediate
allowVolumeExpansion: true
EOF

  # Remove "replicated" only for the duration of the render below, so that
  # neither the configured storageClass (default "replicated") nor "replicated"
  # is in the propagated set -- forcing the `sortAlpha | first` selection branch.
  kubectl delete sc replicated --ignore-not-found

  # Server-side dry-run executes Helm `lookup` against the live cluster and
  # renders the real storageClasses map. rc is captured separately (no pipe) so
  # the management-cluster state is always restored before any assertion exits.
  # The release namespace must be a valid tenant identifier (the chart's
  # dashboard-resourcemap template enforces this), so render under tenant-test.
  local raw rc
  raw=$(timeout 120 helm install scprobe packages/apps/kubernetes \
    --dry-run=server -n tenant-test \
    -f packages/apps/kubernetes/tests/values/common.yaml -o json 2>/tmp/sc-fallback-render.err)
  rc=$?

  # Restore management-cluster StorageClasses (inline, unconditional). This MUST
  # run before any assertion `exit 1` below, so no EXIT/RETURN trap is used
  # (per docs/agents/e2e-testing.md). The "replicated" manifest mirrors
  # hack/e2e-post-install-prep.sh.
  kubectl apply -f - <<'EOF'
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: replicated
provisioner: linstor.csi.linbit.com
parameters:
  linstor.csi.linbit.com/storagePool: "data"
  linstor.csi.linbit.com/autoPlace: "3"
  linstor.csi.linbit.com/layerList: "drbd storage"
  linstor.csi.linbit.com/allowRemoteVolumeAccess: "true"
  property.linstor.csi.linbit.com/DrbdOptions/auto-quorum: suspend-io
  property.linstor.csi.linbit.com/DrbdOptions/Resource/on-no-data-accessible: suspend-io
  property.linstor.csi.linbit.com/DrbdOptions/Resource/on-suspended-primary-outdated: force-secondary
  property.linstor.csi.linbit.com/DrbdOptions/Net/rr-conflict: retry-connect
volumeBindingMode: Immediate
allowVolumeExpansion: true
EOF
  kubectl delete sc nvme ssd --ignore-not-found

  if [ "$rc" -ne 0 ] || [ -z "$raw" ]; then
    echo "server-side dry-run render of the kubernetes chart failed (rc=$rc)" >&2
    cat /tmp/sc-fallback-render.err >&2 || true
    exit 1
  fi

  # Isolate the rendered -csi HelmRelease's storageClasses map.
  local sc
  sc=$(printf '%s' "$raw" | yq -p=json '.manifest' \
    | yq 'select(.kind == "HelmRelease" and .metadata.name == "scprobe-csi") | .spec.values.storageClasses')
  if [ -z "$sc" ] || [ "$sc" = "null" ]; then
    echo "rendered scprobe-csi HelmRelease carries no storageClasses map" >&2
    printf '%s' "$raw" | yq -p=json '.manifest' >&2
    exit 1
  fi

  local default_count default_key kubevirt_present kubevirt_default
  default_count=$(printf '%s' "$sc" | yq '[to_entries | .[] | select(.value.default == true)] | length')
  default_key=$(printf '%s' "$sc" | yq 'to_entries | map(select(.value.default == true)) | .[0].key')
  kubevirt_present=$(printf '%s' "$sc" | yq 'has("kubevirt")')
  kubevirt_default=$(printf '%s' "$sc" | yq '.kubevirt.default')

  # 1. Exactly one default. 2. The default is a propagated class (nvme/ssd),
  # never the kubevirt alias. 3. The kubevirt alias still exists, non-default.
  if [ "$default_count" != "1" ] \
    || { [ "$default_key" != "nvme" ] && [ "$default_key" != "ssd" ]; } \
    || [ "$kubevirt_present" != "true" ] \
    || [ "$kubevirt_default" != "false" ]; then
    echo "tenant default StorageClass selection regressed (PR #2872 B1):" >&2
    echo "  default_count=$default_count default_key=$default_key kubevirt_present=$kubevirt_present kubevirt_default=$kubevirt_default" >&2
    echo "  expected exactly one default among {nvme,ssd}; kubevirt present and non-default" >&2
    printf 'rendered storageClasses:\n%s\n' "$sc" >&2
    exit 1
  fi
  echo "StorageClass fallback-default OK (default='$default_key' among propagated classes; kubevirt alias non-default)"
}
