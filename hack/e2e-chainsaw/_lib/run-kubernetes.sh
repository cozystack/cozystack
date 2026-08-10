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
kubectl_wait_retry() {
  local _attempts=3
  local _i _out _rc
  for _i in $(seq 1 "${_attempts}"); do
    _out=$(kubectl wait "$@" 2>&1)
    _rc=$?
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
_tenant_snapshot_on_fail() {
  _rc=$?
  [ "$_rc" -eq 0 ] && return 0
  command -v crust-gather >/dev/null 2>&1 || return 0
  [ -n "${CURRENT_TENANT_KC:-}" ] && [ -f "${CURRENT_TENANT_KC}" ] || return 0
  # COZY_SNAPSHOT_NAME is the Chainsaw test name (set in the kubernetes-* test's
  # script env), so the tenant snapshot co-locates with the host snapshot the
  # global .chainsaw.yaml catch writes under snapshots/<test>/. Falls back to a
  # generic name if sourced outside the Chainsaw harness.
  _snap="${COZY_REPORT_DIR:-/workspace/_out/cozyreport}/snapshots/${COZY_SNAPSHOT_NAME:-kubernetes}"
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
cozy_capture_tenant_talos_command() {
  local pod_name="$1"
  local node_ip="$2"
  local output="$3"
  shift 3
  local rc=0

  timeout -k 5 20 kubectl -n tenant-test exec "${pod_name}" -c diagnostics -- \
    /tmp/talosctl --talosconfig /tmp/talosconfig \
    -e "${node_ip}" -n "${node_ip}" "$@" >"${output}" 2>&1 || rc=$?
  printf '\n[capture exit code: %s]\n' "${rc}" >>"${output}"
  return "${rc}"
}

cozy_capture_tenant_talos_node() {
  local pod_name="$1"
  local node_ip="$2"
  local output_dir="$3"

  mkdir -p "${output_dir}"
  printf 'endpoint: %s\nnode: %s\n' "${node_ip}" "${node_ip}" >"${output_dir}/target.txt"
  cozy_capture_tenant_talos_command "${pod_name}" "${node_ip}" \
    "${output_dir}/dmesg.log" dmesg || true
  cozy_capture_tenant_talos_command "${pod_name}" "${node_ip}" \
    "${output_dir}/kubelet.log" logs kubelet --tail=500 || true
}

# Name the console experiment's own failure before the noise starts. Enabling
# logSerialConsole punches through the platform's cluster-wide disable, and if
# kubevirt/kubevirt#15989 still bites, guest-console-log wedges virt-launcher
# in Init: the workers never boot, no Node registers, and the suite reports
# "fewer than 2 tenant nodes Ready within 18m" -- byte-identical to the failure
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
cozy_capture_tenant_serial_console() {
  local report_dir="${COZY_REPORT_DIR:-/workspace/_out/cozyreport}/snapshots/${COZY_SNAPSHOT_NAME:-kubernetes}/tenant-serial-console"
  local pods pod rc
  local list_rc=0
  local seen=0
  local silent=0
  local pod_err
  local max_pods=6
  local err_log="${report_dir}/setup-error.log"
  local warn_log="${report_dir}/READ-WARNINGS.txt"

  mkdir -p "${report_dir}"
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

  # Cap the walk. Every read here is bounded, but the pool can reach maxReplicas, so
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
    timeout -k 5 30 kubectl -n tenant-test logs "${pod}" \
      -c guest-console-log --limit-bytes=1048576 \
      >"${report_dir}/${pod}.log" 2>"${pod_err}" || rc=$?
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
    printf '\n[capture exit code: %s]\n' "${rc}" >>"${report_dir}/${pod}.log"
  done

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
    echo "--- capturing tenant Talos dmesg/kubelet: ${vmi_name} (${node_ip}) ---"
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
_cozy_diag_seconds() {
  if [ -n "${4:-}" ] && [ "${1}" = 0 ]; then
    echo "» WARNING: ignoring ${3}='${1}' (zero disables the bound instead of tightening it); using ${2}" >&2
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
# Lower than the bound the newer collectors in this file use, and the difference is
# count rather than confidence: those bound a single read or a short walk, while the
# block below issues a dozen back to back, so the same per-read bound would put the
# block's own ceiling ahead of the tenant crust-gather snapshot it exists to reach.
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
COZY_DIAG_PHASE_BUDGET_DEFAULT=480
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
  command -v timeout >/dev/null 2>&1 || \
    echo "» WARNING: timeout is not on PATH; the bounded reads below run UNBOUNDED, so one that hangs can still take the op and the tenant snapshot with it, and the collectors that call timeout directly (wedge check, serial console, guest Talos) exit 127 and collect nothing" >&2
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
# fewer than 2 tenant nodes became Ready inside its 18m deadline.
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
# reads are bounded: the caller's `exit 1` is what fails the suite and what
# triggers the snapshot.
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
  local importer_listed=0 _p

  cozy_diag_phase_start

  # The headline stays here rather than at the call site so it keeps its place
  # ahead of the wedge check: that check exists to name the console experiment's
  # own failure before this line's wording -- which is byte-identical to the bug
  # the instrumentation studies -- sends a triager to the known flake.
  echo "=== node-join failed: fewer than 2 tenant nodes Ready within 18m — diagnostics follow ==="
  cozy_report_guest_console_wedge || true
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

  # Order below is load-bearing and the phase budget is why. The budget declines
  # whatever has not started when it runs out, so what runs last is what gets
  # declined -- and (c) is the discriminator for mode 2b, the failure this whole
  # artifact exists to let someone fix. Cheap reads first, then the collectors
  # that cost minutes. Everything ahead of this line is bounded reads and a capped
  # walk, which together fit inside the budget with room left, while the two
  # collectors below can exhaust it between them on a slow run. Putting the
  # heavy pair first, as this block used to, spends the budget on them and
  # declines the two 25s reads that answer the question.
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
    cozy_capture_tenant_serial_console || true
  fi

  # (b) In-guest Talos kernel and kubelet logs. The tenant chart intentionally
  # has no admin talosconfig, so mint a one-hour os:reader client from its
  # existing cert-manager Issuer and run talosctl from a hardened Pod that can
  # reach the bridge-networked VMI IPs without weakening TLS verification.
  # A worker that has not reached apid yet will produce a bounded connection
  # error while a later-stage worker remains capturable; both outcomes are
  # retained in cozyreport.
  if cozy_diag_phase_has_time '(b) in-guest Talos dmesg + kubelet logs'; then
    echo "=== (b) in-guest Talos dmesg + kubelet logs ==="
    cozy_capture_tenant_talos "${test_name}" || true
  fi

  # The OS-image cache and the ghcr.io mirror fail independently and produce the
  # same symptom from outside the guest, so both get dumped: this one answers
  # whether the worker's kubelet-image pull reached the mirror or fell back to
  # public ghcr.io, which the node-join failure alone cannot distinguish. Gated
  # like its neighbours, and after the guest captures: its whole-log read carries
  # no bound of its own, the console evidence it would otherwise starve is
  # irreplaceable, and the mirror's state is partly recoverable from the reads
  # above. Cheaper than the talos-image-cache re-probe below, which creates a
  # Pod and waits on curl retries, so it goes ahead of it.
  if cozy_diag_phase_has_time 'ghcr-mirror state + access log'; then
    echo "--- ghcr-mirror state + access log ---"
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

  # Clean up stale resources from a previous failed retry
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
    fluxcd:
      enabled: false
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
  nodeGroups:
    md0:
      diskSize: 20Gi
      gpus: []
      instanceType: u1.medium
      # The failure this suite keeps hitting is a worker that stalls in the
      # guest before Talos apid answers, which leaves nothing to read on the
      # management side and nothing for talosctl to connect to. Turning this on
      # attaches KubeVirt's guest-console-log container, the only artifact that
      # covers that window.
      logSerialConsole: true
      maxReplicas: 10
      minReplicas: 2
      resources: {}
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
  # leaving localhost refusing connections for the entire 18m node-Ready wait
  # while the cluster was in fact healthy (CAPI NodeHealthy=True on both nodes),
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

  # Wait until at least 2 worker nodes have joined AND become Ready, on a single
  # deadline. This used to be split (8m to join + 3m to become Ready), but the
  # two budgets starve each other under load: a slow KubeVirt VM boot consumes
  # the join budget, then the tenant cluster's cilium CNI needs several more
  # minutes to make the freshly-joined nodes Ready — overflowing the fixed 3m
  # Ready window even though the CNI converges fine. One deadline that polls
  # for ">=2 nodes Ready" is robust to wherever the time goes.
  #
  # 18m, not the earlier 12m: this single budget has to absorb the *entire*
  # worker bring-up, and the `machinedeployment .status.replicas=2` gate above
  # clears while the KubeVirt VMs are still only Machine objects — the clock
  # here starts ~2m before the guest VMIs even exist. Under host storage
  # pressure that margin evaporates: in run 30260770694 a transient
  # `drbd.linbit.com/lost-quorum` taint delayed the worker DataVolume imports,
  # the worker VMIs were created ~2m into this wait, and the guests then had
  # only ~10m to import Talos, boot, register a kubelet and let cilium turn the
  # nodes Ready. They did not make it: both kubernetes-latest and
  # kubernetes-previous failed here at exactly 12m with zero Nodes registered
  # and the tenant cilium HR still mid-install. A less-loaded fleet run passed
  # the same suites unchanged, so this is load-induced slowness, not a stuck
  # bring-up; 18m restores margin and still sits well inside the 50m step
  # timeout (the downstream LB/NFS/ouroboros checks add ~10-15m on the happy
  # path).
  if ! timeout 18m bash -c '
    until [ "$(kubectl --kubeconfig tenantkubeconfig-'"${test_name}"' get nodes --no-headers 2>/dev/null | grep -cw Ready)" -ge 2 ]; do
      sleep 5
    done
  '; then
    # Node-join failed: fewer than 2 tenant nodes became Ready inside the 18m
    # deadline. Dump scoped diagnostics that split the failure sub-modes, then
    # fail fast — no point running LB/NFS tests without Ready nodes. The `exit 1`
    # is also what triggers the tenant crust-gather snapshot (the EXIT trap
    # registered above), so it has to be reached.
    cozy_report_node_join_failure "${test_name}"
    exit 1
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

  # Success: disarm the tenant-snapshot trap so it doesn't fire on the clean exit.
  trap - EXIT
  # Clean up: delete the test-scoped tenant API LoadBalancer (frees its MetalLB
  # IP) and the local kubeconfig.
  kubectl -n tenant-test delete service "kubernetes-${test_name}-e2e-lb" --ignore-not-found --wait=false 2>/dev/null || true
  rm -f "tenantkubeconfig-${test_name}"
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
