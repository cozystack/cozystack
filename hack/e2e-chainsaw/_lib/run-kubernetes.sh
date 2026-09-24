# shellcheck shell=bash
# Sourced by the chainsaw kubernetes-latest/previous Tests after cd to repo root.
. hack/e2e-chainsaw/_lib/remediation-guard.sh
. hack/e2e-chainsaw/_lib/talos-image-cache.sh

# The QEMU lane exercises replicated DRBD storage. The container lane cannot
# represent three DRBD nodes on one shared kernel, so its otherwise-identical
# suites use the production local class instead. Keep the accepted values
# narrow because this result is interpolated into Kubernetes YAML below.
cozy_e2e_storage_class() {
  case "${COZY_E2E_STORAGE_CLASS:-replicated}" in
    local | replicated) printf '%s\n' "${COZY_E2E_STORAGE_CLASS:-replicated}" ;;
    *)
      echo "COZY_E2E_STORAGE_CLASS must be local or replicated, got '${COZY_E2E_STORAGE_CLASS}'" >&2
      return 1
      ;;
  esac
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

# Wait for a HelmRelease upgrade that has not started yet at the call site.
# `kubectl wait --for=condition=Ready` alone is unsafe here: the old Ready=True
# condition remains visible until helm-controller observes the changed spec, so
# it can return before the upgrade even begins. Require a newer generation and
# require status.observedGeneration to catch up to it before accepting Ready.
# A terminal Stalled condition fails immediately instead of burning the whole
# timeout on work Flux has already declared impossible.
cozy_wait_helmrelease_upgrade() {
  local namespace="$1"
  local name="$2"
  local previous_generation="$3"
  local timeout_seconds="${4:-600}"
  local deadline=$(( $(date +%s) + timeout_seconds ))
  local state generation observed ready stalled

  while [ "$(date +%s)" -lt "${deadline}" ]; do
    state=$(kubectl -n "${namespace}" get helmrelease "${name}" \
      -o 'jsonpath={.metadata.generation}{"|"}{.status.observedGeneration}{"|"}{.status.conditions[?(@.type=="Ready")].status}{"|"}{.status.conditions[?(@.type=="Stalled")].status}' \
      2>/dev/null) || state=
    IFS='|' read -r generation observed ready stalled <<EOF
${state}
EOF

    if [ "${stalled}" = True ]; then
      echo "HelmRelease ${namespace}/${name} stalled while applying generation ${generation:-<unknown>}" >&2
      if ! kubectl -n "${namespace}" describe helmrelease "${name}" >&2; then
        echo "The HelmRelease failure diagnostic could not be collected" >&2
      fi
      return 1
    fi
    case "${generation}:${observed}" in
      *[!0-9:]* | :* | *:) ;;
      *)
        if [ "${generation}" -gt "${previous_generation}" ] \
          && [ "${observed}" -eq "${generation}" ] \
          && [ "${ready}" = True ]; then
          echo "HelmRelease ${namespace}/${name} reconciled generation ${generation}"
          return 0
        fi
        ;;
    esac
    sleep 5
  done

  echo "HelmRelease ${namespace}/${name} did not reconcile a generation newer than ${previous_generation} within ${timeout_seconds}s" >&2
  if ! kubectl -n "${namespace}" describe helmrelease "${name}" >&2; then
    echo "The HelmRelease timeout diagnostic could not be collected" >&2
  fi
  return 1
}

cozy_oidc_bindings() {
  local test_name="$1"
  kubectl --kubeconfig "tenantkubeconfig-${test_name}" get clusterrolebindings \
    --selector="app.kubernetes.io/managed-by=cozystack-oidc,app.kubernetes.io/instance=kubernetes-${test_name}" \
    -o 'jsonpath={range .items[*]}{.subjects[0].name}{"\t"}{.roleRef.name}{"\n"}{end}' |
    sort
}

cozy_assert_oidc_system() {
  local test_name="$1"
  local release="kubernetes-${test_name}"
  local audience="tenant-test-${release}"
  local authn_config oidc_kubeconfig bindings

  kubectl -n tenant-test wait job "${release}-oidc-bootstrap" \
    --for=condition=complete --timeout=1m

  kubectl -n tenant-test get kamajicontrolplane "${release}" \
    -o jsonpath='{.spec.apiServer.extraArgs}' |
    grep -qF -- '--authentication-config=/etc/kubernetes/authentication-config/config.yaml'

  authn_config=$(kubectl -n tenant-test get secret "${release}-oidc-authn-config" \
    -o jsonpath='{.data.config\.yaml}' | base64 -d)
  printf '%s\n' "${authn_config}" | grep -qE 'url: https://keycloak\.[^/]+/realms/cozy'
  printf '%s\n' "${authn_config}" | grep -qF -- "- ${audience}"

  [ "$(kubectl -n tenant-test get keycloakclient.v1.edp.epam.com "${audience}" \
    -o jsonpath='{.spec.public}')" = true ]
  [ "$(kubectl -n tenant-test get keycloakclientscope.v1.edp.epam.com "${audience}-audience" \
    -o jsonpath='{.spec.protocolMappers[0].protocolMapper}')" = oidc-audience-mapper ]

  oidc_kubeconfig=$(kubectl -n tenant-test get secret "${release}-oidc-kubeconfig" \
    -o jsonpath='{.data.kubeconfig}' | base64 -d)
  printf '%s\n' "${oidc_kubeconfig}" | grep -qF -- '- oidc-login'
  printf '%s\n' "${oidc_kubeconfig}" | grep -qF -- "--oidc-client-id=${audience}"

  bindings=$(cozy_oidc_bindings "${test_name}")
  [ "${bindings}" = "$(printf 'e2e-admin@example.test\tcluster-admin\ne2e-viewer@example.test\tview\n' | sort)" ]
}

cozy_switch_and_assert_oidc_custom_config() {
  local test_name="$1"
  local probe=
  local release="kubernetes-${test_name}"
  local audience="cozystack-byo-${test_name}"
  local previous_generation authn_config bindings

  previous_generation=$(kubectl -n tenant-test get helmrelease "${release}" \
    -o jsonpath='{.metadata.generation}')
  kubectl -n tenant-test patch kuberneteses.apps.cozystack.io "${test_name}" \
    --type=merge --patch "$(printf '%s' '{
      "spec": {
        "oidc": {
          "mode": "CustomConfig",
          "customConfig": {
            "config": "apiVersion: apiserver.config.k8s.io/v1beta1\nkind: AuthenticationConfiguration\njwt:\n- issuer:\n    url: https://idp.byo.example.test\n    audiences:\n    - '"${audience}"'\n  claimMappings:\n    username:\n      claim: preferred_username\n      prefix: \"\"\n    groups:\n      claim: groups\n      prefix: \"\"\n"
          },
          "users": [
            {"email": "byo-admin@example.test", "role": "admin"}
          ]
        }
      }
    }')"

  cozy_wait_helmrelease_upgrade tenant-test "${release}" \
    "${previous_generation}" 600
  kubectl -n tenant-test wait job "${release}-oidc-bootstrap" \
    --for=condition=complete --timeout=1m

  kubectl -n tenant-test get kamajicontrolplane "${release}" \
    -o jsonpath='{.spec.apiServer.extraArgs}' |
    grep -qF -- '--authentication-config=/etc/kubernetes/authentication-config/config.yaml'

  authn_config=$(kubectl -n tenant-test get secret "${release}-oidc-authn-config" \
    -o jsonpath='{.data.config\.yaml}' | base64 -d)
  printf '%s\n' "${authn_config}" | grep -qF 'url: https://idp.byo.example.test'
  printf '%s\n' "${authn_config}" | grep -qF -- "- ${audience}"
  if printf '%s\n' "${authn_config}" | grep -qE 'url: https://keycloak\.[^/]+/realms/cozy'; then
    echo "CustomConfig AuthenticationConfiguration still carries the System issuer" >&2
    return 1
  fi

  bindings=$(cozy_oidc_bindings "${test_name}")
  [ "${bindings}" = "$(printf 'byo-admin@example.test\tcluster-admin')" ]

  # `--ignore-not-found` is what separates "the object is gone" from "could not
  # ask": absent is exit 0 with empty output, while an RBAC denial, a timeout or
  # a missing CRD is still non-zero. A bare `if kubectl get ... >/dev/null 2>&1`
  # reads all of those as gone, so the teardown assertions used to report
  # success for never having observed anything.
  if ! probe=$(kubectl -n tenant-test get keycloakclient.v1.edp.epam.com "tenant-test-${release}" \
    --ignore-not-found -o name); then
    echo "could not determine whether the System-mode KeycloakClient is gone" >&2
    return 1
  fi
  if [ -n "${probe}" ]; then
    echo "System-mode KeycloakClient survived the CustomConfig upgrade" >&2
    return 1
  fi
  if ! probe=$(kubectl -n tenant-test get keycloakclientscope.v1.edp.epam.com "tenant-test-${release}-audience" \
    --ignore-not-found -o name); then
    echo "could not determine whether the System-mode KeycloakClientScope is gone" >&2
    return 1
  fi
  if [ -n "${probe}" ]; then
    echo "System-mode KeycloakClientScope survived the CustomConfig upgrade" >&2
    return 1
  fi
  if ! probe=$(kubectl -n tenant-test get secret "${release}-oidc-kubeconfig" \
    --ignore-not-found -o name); then
    echo "could not determine whether the System-mode OIDC kubeconfig is gone" >&2
    return 1
  fi
  if [ -n "${probe}" ]; then
    echo "System-mode OIDC kubeconfig survived the CustomConfig upgrade" >&2
    return 1
  fi
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

# Keep only PVC resource names that belong to one Kubernetes worker pool. The
# worker-disk PVCs carry no cluster label, but CDI preserves the owning
# MachineDeployment name inside every one of their generated names.
cozy_filter_cluster_pvcs() {
  local test_name="$1"
  local capture="$2"
  printf '%s\n' "${capture}" |
    awk -v cluster="kubernetes-${test_name}-md0" 'index($0, cluster) != 0'
}

# Block until one tenant cluster's KubeVirt compute and storage are actually
# released, not merely triggered for deletion. Deleting the Kubernetes CR
# returns as soon as its finalizers clear, but that only TRIGGERS teardown of
# the CAPK worker VMs and their DataVolume-backed disk PVCs. The virt-launcher
# pods keep their guest RAM reserved until the VMIs are gone, so without this
# barrier the next tenant test's worker VMs begin scheduling against a sandbox
# the previous tenant has not yet vacated -> memory starvation -> a worker VM
# misses the node-join budget and the test flakes on worker-node-join.
#
# Bounded and loud: on timeout this returns non-zero and names what is left, and
# each caller decides whether that stops the run. Other suites use the same
# tenant-test namespace and can legitimately leave their own PVCs there while
# this runs; VM/VMI probes therefore select the CAPI cluster label, and the
# unlabeled worker-disk PVCs are filtered by their MachineDeployment name
# component.
cozy_wait_tenant_drained() {
  local _test_name="$1"
  local _ns=tenant-test
  local _timeout="${2:-300}"
  local _deadline=$(( $(date +%s) + _timeout ))
  local _cluster="kubernetes-${_test_name}"
  local _vm _vmi _pvc _pvc_all
  while :; do
    _vm=$(kubectl -n "$_ns" get virtualmachines.kubevirt.io \
      -l "cluster.x-k8s.io/cluster-name=${_cluster}" -o name 2>/dev/null) || _vm=err
    _vmi=$(kubectl -n "$_ns" get virtualmachineinstances.kubevirt.io \
      -l "cluster.x-k8s.io/cluster-name=${_cluster}" -o name 2>/dev/null) || _vmi=err
    if _pvc_all=$(kubectl -n "$_ns" get pvc -o name 2>/dev/null); then
      _pvc=$(cozy_filter_cluster_pvcs "$_test_name" "$_pvc_all")
    else
      _pvc=err
    fi
    if cozy_tenant_drained "$_vm" "$_vmi" "$_pvc"; then
      echo "» ${_cluster} VMs/VMIs/PVCs drained from $_ns"
      return 0
    fi
    if [ "$(date +%s)" -ge "$_deadline" ]; then
      echo "» WARNING: ${_cluster} teardown did not drain within ${_timeout}s (next test may face memory/storage pressure)" >&2
      printf '%s\n' "$_vm" "$_vmi" "$_pvc" |
        awk 'NF { print "  drain-leftover: " $0 }' >&2
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

# Capture the local lane's per-node free-capacity baseline after stale tenant
# cleanup and before creating this suite's workers. Chainsaw executes `try` and
# `finally` in separate shells, so the small state file is the hand-off between
# them. An absolute threshold cannot describe this lane: persistent platform
# PVCs legitimately leave one pool far below 90 GiB, while all three sparse
# pools also share one runner filesystem beneath ZFS.
cozy_capture_linstor_pool_baseline() {
  _baseline_file="${COZY_LINSTOR_POOL_BASELINE_FILE:-_out/e2e-kubernetes-linstor-pool-baseline}"
  _baseline_dir=${_baseline_file%/*}
  [ "$_baseline_dir" != "$_baseline_file" ] || _baseline_dir=.
  mkdir -p "$_baseline_dir"
  : >"$_baseline_file"

  _baseline=$(kubectl -n cozy-linstor exec deploy/linstor-controller -- sh -c '
    linstor --machine-readable sp l 2>/dev/null |
    jq -r "first | .[] | select((.provider_kind | test(\"^ZFS\")) and .free_capacity != null) | \"\(.node_name):\(.free_capacity)\"" |
    sort
  ' 2>/dev/null) || _baseline=""
  if [ -z "$_baseline" ]; then
    echo "» ERROR: could not record the local LINSTOR pool baseline; physical ZFS reclamation would be unverifiable" >&2
    return 1
  fi
  _baseline_nodes=$(printf '%s\n' "$_baseline" | awk -F: 'NF == 2 { print $1 }' | sort | tr '\n' ' ')
  if [ "$_baseline_nodes" != "srv1 srv2 srv3 " ] \
      || ! cozy_linstor_pools_at_baseline "$_baseline" "$_baseline" 0; then
    echo "» ERROR: local LINSTOR pool baseline is incomplete or malformed (expected numeric rows for srv1, srv2 and srv3): ${_baseline:-<empty>}" >&2
    return 1
  fi
  printf '%s\n' "$_baseline" >"$_baseline_file"
  echo "» local LINSTOR pool baseline recorded:"
  printf '%s\n' "$_baseline" | sed 's/^/  baseline-free-kib: /'
}

# Pure comparison used by the local-pool reclamation loop. Both captures contain
# `node:free_capacity_kib` rows. Every node present in the baseline must still be
# present and may fall short only by the small caller-provided metadata tolerance.
# The 512 MiB default covers the ~0.27 GiB free-capacity drift measured between
# two otherwise-clean container suites, while still rejecting the smallest known
# leaked test volume (the 1 GiB ClickHouse keeper PVC).
cozy_linstor_pools_at_baseline() {
  _baseline_rows="$1"
  _current_rows="$2"
  _tolerance_kib="${3:-524288}"
  [ -n "$_baseline_rows" ] && [ -n "$_current_rows" ] || return 1
  case "$_tolerance_kib" in
    '' | *[!0-9]*) return 1 ;;
  esac

  while IFS=: read -r _baseline_node _baseline_kib; do
    [ -n "$_baseline_node" ] && [ -n "$_baseline_kib" ] || return 1
    _current_kib=$(printf '%s\n' "$_current_rows" | awk -F: -v node="$_baseline_node" '$1 == node { print $2; exit }')
    [ -n "$_current_kib" ] || return 1
    case "$_baseline_kib" in '' | *[!0-9]*) return 1 ;; esac
    case "$_current_kib" in '' | *[!0-9]*) return 1 ;; esac
    if [ "$(( _current_kib + _tolerance_kib ))" -lt "$_baseline_kib" ]; then
      return 1
    fi
  done <<EOF
$_baseline_rows
EOF
  return 0
}

cozy_wait_linstor_pool_baseline() {
  _timeout="${1:-300}"
  _tolerance_kib="${2:-524288}"
  _baseline_file="${COZY_LINSTOR_POOL_BASELINE_FILE:-_out/e2e-kubernetes-linstor-pool-baseline}"
  if [ ! -s "$_baseline_file" ]; then
    echo "» ERROR: no local LINSTOR pool baseline was recorded; physical ZFS reclamation is unknown" >&2
    return 1
  fi
  _baseline=$(cat "$_baseline_file")
  _deadline=$(( $(date +%s) + _timeout ))
  _current=""
  while :; do
    _current=$(kubectl -n cozy-linstor exec deploy/linstor-controller -- sh -c '
      linstor --machine-readable sp l 2>/dev/null |
      jq -r "first | .[] | select((.provider_kind | test(\"^ZFS\")) and .free_capacity != null) | \"\(.node_name):\(.free_capacity)\"" |
      sort
    ' 2>/dev/null) || _current=""
    if cozy_linstor_pools_at_baseline "$_baseline" "$_current" "$_tolerance_kib"; then
      echo "» local LINSTOR pools returned to their pre-suite baseline"
      return 0
    fi
    if [ "$(date +%s)" -ge "$_deadline" ]; then
      echo "» ERROR: local LINSTOR pools did not return to their pre-suite baseline within ${_timeout}s; a worker or CDI scratch volume may still occupy ZFS space" >&2
      printf '%s\n' "$_baseline" | sed 's/^/  baseline-free-kib: /' >&2
      printf '%s\n' "${_current:-<the LINSTOR pool probe returned nothing>}" | sed 's/^/  current-free-kib: /' >&2
      return 1
    fi
    sleep 5
  done
}

# The absolute 90 GiB threshold describes only the replicated lane's DRBD
# teardown. The local lane instead waits for the exact capacity it had before
# this Kubernetes suite, which detects its own leaked worker/scratch volumes
# without requiring persistent platform volumes to disappear.
cozy_wait_linstor_pool_reclaimed() {
  _storage_class=$(cozy_e2e_storage_class) || return 1
  if [ "$_storage_class" = local ]; then
    cozy_wait_linstor_pool_baseline "${2:-300}" 524288
    return $?
  fi
  cozy_wait_linstor_pool_free "${1:-90}" "${2:-300}"
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
  local test_name="${1:-}"
  # Delete any test-scoped tenant API LoadBalancer Services left by a failed run
  # so they don't leak MetalLB IPs from the shared host pool. Labeled by the
  # test so a single selector reaps them all.
  kubectl -n tenant-test delete service -l cozystack-e2e.io/tenant-api-lb --ignore-not-found --wait=false 2>/dev/null || true
  kubectl -n tenant-test delete kuberneteses.apps.cozystack.io --all --ignore-not-found --wait=false 2>/dev/null || true
  kubectl -n tenant-test wait kuberneteses.apps.cozystack.io --all --for=delete --timeout=5m 2>/dev/null || true
  # The CR delete above finalizes once the Kubernetes CR is gone, which only
  # TRIGGERS KubeVirt VM teardown + PVC release. Block until the worker VMs,
  # VMIs (guest RAM) and disk PVCs are actually gone so the next tenant test
  # starts on a freed sandbox -- the root cause of the node-join flake. Scoped to
  # this suite's cluster, so without its name there is nothing to wait for.
  if [ -n "${test_name}" ]; then
    cozy_wait_tenant_drained "${test_name}" 300 || true
  else
    echo "» WARNING: cozy_cleanup was given no test name; the tenant drain was skipped" >&2
  fi
  # PVC removal at the API level does not imply the satellite ZFS pool has
  # reclaimed the space (see comment on cozy_wait_linstor_pool_free above);
  # wait for it to return before yielding to the next tenant test. The local
  # lane compares against the baseline this suite recorded instead of the
  # replicated lane's absolute threshold.
  cozy_wait_linstor_pool_reclaimed 90 300 || true
}

# Without --strict-topology, external-provisioner passes every topology segment
# as `requisite` and only prefers the node the scheduler chose, so linstor-csi
# may place a node-pinned volume elsewhere when that node is full. A CDI
# importer mounts two such volumes on `local`, and the scratch one is created
# only after the pod is scheduled, so it can land on another node and leave the
# importer unschedulable forever against a node-affinity conflict. The linstor
# chart has no switch for the flag, so the container lane patches it onto the
# live LinstorCluster at the end of install (hack/e2e-install-cozystack.bats).
#
# `args` has no patchMergeKey, so the operator's strategic merge replaces the
# list rather than appending to it: the whole list is restated as
# piraeus-operator v2.10.2 renders it, and must be re-checked when that
# operator is bumped or an upstream flag is silently dropped.
cozy_csi_strict_topology_patch() {
  cat <<'EOF'
[{"op":"add","path":"/spec/csiController/podTemplate/spec/containers/-","value":{"name":"csi-provisioner","args":["--v=$(VERBOSE)","--csi-address=$(ADDRESS)","--timeout=$(TIMEOUT)","--leader-election=$(LEADER_ELECTION)","--leader-election-namespace=$(NAMESPACE)","--worker-threads=$(WORKER_THREADS)","--http-endpoint=$(HTTP_ENDPOINT)","--default-fstype=$(DEFAULT_FS_TYPE)","--enable-capacity=$(ENABLE_CAPACITY)","--extra-create-metadata=$(EXTRA_CREATE_METADATA)","--capacity-ownerref-level=$(CAPACITY_OWNERREF_LEVEL)","--strict-topology"]}}]
EOF
}

# CDI's own 600M worker memory ceiling was seen to OOM the decompress+convert of
# a tenant worker disk near 100% on the container lane, after which CDI retries
# from scratch and the DataVolume cycles forever instead of failing. The same
# import completes at 600M on QEMU nodes, so the substrate needs the headroom
# and the chart default stays. Merged into the CDI CR rather than CDIConfig:
# the operator reconciles CDIConfig.spec from the CR, so a direct CDIConfig
# patch is reverted. It applies to every worker pod CDI creates, not only the
# importer. The CPU ceiling stays at CDI's default, because the failure was
# memory.
cozy_cdi_worker_resources_patch() {
  printf '%s\n' '{"spec":{"config":{"podResourceRequirements":{"requests":{"cpu":"100m","memory":"256Mi"},"limits":{"cpu":"750m","memory":"4Gi"}}}}}'
}

# The override lives only in the live LinstorCluster, and a linstor upgrade
# re-renders that object from a chart that does not carry it. Called before
# every suite that imports onto `local`, so an override that went missing
# fails there, by name, instead of as an importer stuck at ImportScheduled.
cozy_check_csi_strict_topology() {
  local args
  if ! args=$(kubectl -n cozy-linstor get deployment linstor-csi-controller \
    -o jsonpath='{.spec.template.spec.containers[?(@.name=="csi-provisioner")].args}'); then
    echo "» ERROR: could not read the linstor-csi-controller csi-provisioner arguments" >&2
    return 1
  fi
  case "${args}" in
    *--strict-topology*) return 0 ;;
  esac
  echo "» ERROR: linstor-csi-controller's csi-provisioner runs without --strict-topology (args: ${args:-<none>}); the LinstorCluster override applied at install is gone" >&2
  return 1
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
  timeout -k 30 360 crust-gather collect -k "${CURRENT_TENANT_KC}" --duration 180s \
    --exclude-kind Secret -f "$_snap/${CURRENT_TENANT_KC}" \
    >"$_snap/crust-gather-${CURRENT_TENANT_KC}.log" 2>&1 || _cg_rc=$?
  case "$_cg_rc" in
    0) echo "» tenant crust-gather snapshot complete (${CURRENT_TENANT_KC})" ;;
    124 | 137) echo "» tenant crust-gather snapshot TRUNCATED (wall-clock $_cg_rc); partial state kept, see $_snap/crust-gather-${CURRENT_TENANT_KC}.log" ;;
    *) echo "» tenant crust-gather snapshot FAILED (exit $_cg_rc); see $_snap/crust-gather-${CURRENT_TENANT_KC}.log" ;;
  esac
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
    # Optional: exercise both OIDC modes on the already-running latest cluster.
    # The previous-version suite leaves it empty because OIDC is feature
    # coverage, not a compatibility matrix that justifies another upgrade.
    local enable_oidc="${5:-}"
    local storage_class
    storage_class=$(cozy_e2e_storage_class) || return 1
    if [ "$storage_class" = local ]; then
      cozy_check_csi_strict_topology || return 1
    fi
    local k8s_version
    k8s_version=$(yq "$version_expr" packages/apps/kubernetes/files/versions.yaml)

  # Clean up stale resources from a previous failed retry
  kubectl -n tenant-test delete kuberneteses.apps.cozystack.io "${test_name}" --ignore-not-found --wait=false 2>/dev/null || true
  kubectl -n tenant-test wait kuberneteses.apps.cozystack.io "${test_name}" --for=delete --timeout=2m 2>/dev/null || true

  # The local lane cannot use the replicated lane's absolute free-capacity
  # threshold because persistent platform volumes already put one pool below it.
  # Save the post-stale-cleanup state for the separate Chainsaw `finally` shell;
  # cleanup later verifies that this suite returned every node to these figures.
  # A baseline taken while old workers still own their disks would bless the
  # leftover capacity as the new normal, so the drain is required first.
  if [ "$storage_class" = local ]; then
    if ! cozy_wait_tenant_drained "${test_name}" 300; then
      echo "stale tenant compute or storage remains for ${test_name}" >&2
      return 1
    fi
    if ! cozy_capture_linstor_pool_baseline; then
      echo "cannot verify local LINSTOR reclamation without a complete baseline" >&2
      return 1
    fi
  fi

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

  local oidc_block=""
  if [ "${enable_oidc}" = "true" ]; then
    oidc_block=$(cat <<'YAML'
  oidc:
    mode: System
    users:
    - email: e2e-admin@example.test
      role: admin
    - email: e2e-viewer@example.test
      role: view
YAML
)
  fi

  # Point worker DataVolume imports at the in-sandbox Talos image cache when it
  # is up (falls back to the public factory otherwise). Emitted right under spec:
  # as `talos: { imageFactoryURL: ... }`, or an empty line when the default applies.
  local talos_block
  talos_block=$(talos_image_factory_spec_block)

  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Kubernetes
metadata:
  name: "${test_name}"
  namespace: tenant-test
spec:
${talos_block}
${oidc_block}
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
      maxReplicas: 10
      minReplicas: 2
      resources: {}
      roles:
      - ingress-nginx
  storageClass: "${storage_class}"
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
  # bring-up; 18m restores margin and still sits well inside the 40m step
  # timeout (the downstream LB/NFS/ouroboros checks add ~10-15m on the happy
  # path).
  if ! timeout 18m bash -c '
    until [ "$(kubectl --kubeconfig tenantkubeconfig-'"${test_name}"' get nodes --no-headers 2>/dev/null | grep -cw Ready)" -ge 2 ]; do
      sleep 5
    done
  '; then
    # Node-join failed: fewer than 2 tenant nodes became Ready inside the 18m
    # deadline. Dump scoped diagnostics that split the failure sub-modes, then
    # fail fast — no point running LB/NFS tests without Ready nodes.
    #
    # The tenant's cilium-operator HR reports "InProgress" here purely because
    # zero worker Nodes joined, so the HelmRelease condition alone cannot tell
    # apart (2a) the worker VM never booted (virt-launcher Pending/OOMKilled)
    # from (2b) the VM booted fine but its kubelet never registered a Node
    # (Talos/CSR/DNS/routing). The captures below make that distinction legible;
    # (2b) is the failure mode a follow-up fix has to target, and it cannot be
    # designed without this artifact. Every capture is guarded with `|| true`
    # so a capture failure never masks the real `exit 1`.
    echo "=== node-join failed: fewer than 2 tenant nodes Ready within 18m — diagnostics follow ==="
    kubectl --kubeconfig "tenantkubeconfig-${test_name}" describe nodes || true
    kubectl -n tenant-test get hr || true

    # (a) Worker VM / VMI / virt-launcher state on the MANAGEMENT cluster. A VMI
    # stuck Pending or a virt-launcher pod OOMKilled/Pending is mode 2a; a
    # Running+Ready VMI with a healthy virt-launcher is mode 2b. This is the key
    # split. Full resource names (not the `vm` alias) to avoid short-name
    # ambiguity, matching cozy_wait_tenant_drained above.
    echo "=== (a) tenant worker VM/VMI/virt-launcher state (management cluster, ns tenant-test) ==="
    kubectl -n tenant-test get virtualmachines.kubevirt.io,virtualmachineinstances.kubevirt.io -o wide || true
    kubectl -n tenant-test describe virtualmachineinstances.kubevirt.io || true
    kubectl -n tenant-test get pods -l kubevirt.io=virt-launcher -o wide || true
    kubectl -n tenant-test describe pods -l kubevirt.io=virt-launcher || true

    # (a2) Worker DataVolume IMPORT stage. A VM stuck "Provisioning" whose
    # DataVolume is ImportInProgress at N/A progress with the importer pod
    # looping on an HTTP error is a distinct sub-mode of 2a that the VM/VMI
    # state alone does not show: the OS image never finishes importing, so the
    # VM never boots. This is what took out PR #2826's CI — the CDI importer
    # could not reach the talos-image-cache ClusterIP (`dial tcp <svc>:80: i/o
    # timeout`) even though the cache pod was healthy. Show the DataVolume/PVC
    # phases and the importer pod logs, then re-probe the cache ClusterIP from a
    # throwaway pod (talos_image_cache_diagnose) to tell "cache path went dead
    # mid-run" apart from "upstream factory slow/flaky".
    echo "=== (a2) tenant worker DataVolume import stage (management cluster, ns tenant-test) ==="
    kubectl -n tenant-test get datavolume,pvc -o wide 2>&1 | grep -E 'NAME|md0|disk' || true
    kubectl -n tenant-test describe datavolume 2>&1 | grep -Ei 'Name:|Phase:|Progress:|Restart|Reason:|Message:|Running Condition|Bound Condition' || true
    for _p in $(kubectl -n tenant-test get pods -o name 2>/dev/null | grep -E '^pod/importer-'); do
      echo "--- logs ${_p} (current) ---"
      kubectl -n tenant-test logs "${_p}" --tail=40 2>&1 || true
      echo "--- logs ${_p} (previous) ---"
      kubectl -n tenant-test logs "${_p}" --previous --tail=40 2>&1 || true
    done
    echo "--- re-probe talos-image-cache ClusterIP + cacher debug bundle ---"
    talos_image_cache_diagnose || true

    # (c) Tenant kubelet CSRs + the talos-csr-signer sidecar log. A mode-2b node
    # boots but blocks on a kubelet-serving/-client CSR that is never submitted
    # or never approved; the pending CSR list (tenant cluster) plus the signer
    # sidecar log (in the Kamaji apiserver pod on the management cluster) show
    # which side stalled.
    echo "=== (c) tenant CSRs + talos-csr-signer sidecar log ==="
    kubectl --kubeconfig "tenantkubeconfig-${test_name}" get csr || true
    kubectl -n tenant-test logs -l kamaji.clastix.io/name="kubernetes-${test_name}" \
      -c talos-csr-signer --tail=200 --prefix || true

    # (b) In-guest Talos/kubelet state from the worker VMs is intentionally NOT
    # captured here. talosctl needs a client talosconfig for the TENANT cluster,
    # and the runner has none: the tenant workers are provisioned with their own
    # Talos PKI whose CA differs from the sandbox's /workspace/talosconfig (which
    # cozyreport.sh uses to reach the MANAGEMENT nodes only), and the chart
    # materialises no tenant client talosconfig Secret. Pointing talosctl at the
    # worker IPs with the management talosconfig would just fail mTLS and capture
    # nothing, so it is skipped rather than shipped as a misleading no-op. (a) +
    # (c) carry the 2a-vs-2b split; adding real in-guest capture later requires
    # wiring a tenant talosconfig into the runner first.
    echo "=== (b) in-guest Talos/kubelet capture skipped: no tenant talosconfig on the runner (a/c cover the 2a-vs-2b split) ==="
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
    kubectl --kubeconfig "tenantkubeconfig-${test_name}" describe nodes || true
    kubectl -n tenant-test get hr || true
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

  if [ "$storage_class" = local ]; then
    echo "Skipping remote StorageClass propagation and RWX NFS assertions for local-only E2E storage"
  else
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
  fi

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

  # The two old OIDC suites created control-plane-only clusters whose useful
  # assertions took seconds and whose pre-delete hooks then spent two full
  # 120s waits trying to uninstall child releases without a worker. Reuse the
  # real latest-version cluster instead: prove System mode after its bootstrap
  # hook has reached the tenant API, then exercise an actual System ->
  # CustomConfig upgrade and prove the System-only objects are reaped.
  if [ "${enable_oidc}" = "true" ]; then
    echo "Verifying OIDC System mode on the running tenant cluster..."
    cozy_assert_oidc_system "${test_name}"
    echo "Switching the running tenant cluster to OIDC CustomConfig mode..."
    cozy_switch_and_assert_oidc_custom_config "${test_name}"
  fi

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

  local storage_class
  storage_class=$(cozy_e2e_storage_class) || return 1

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
  #
  # `|| rc=$?` rather than a bare assignment followed by `rc=$?`: the caller runs
  # under `set -e`, so a failed render would exit the function on the assignment
  # itself -- before rc is read and before the restore below puts `replicated`
  # back, leaving the management cluster with no default StorageClass for every
  # later suite. That is the opposite of what the restore comment promises.
  local raw rc=0
  raw=$(timeout 120 helm install scprobe packages/apps/kubernetes \
    --dry-run=server -n tenant-test \
    -f packages/apps/kubernetes/tests/values/common.yaml -o json 2>/tmp/sc-fallback-render.err) || rc=$?

  # Restore the QEMU lane's management-cluster StorageClass inline before any
  # assertion exit. The local-only container lane had no replicated class to
  # restore. No EXIT/RETURN trap is used (docs/agents/e2e-testing.md), and the
  # manifest mirrors hack/e2e-post-install-prep.sh.
  if [ "$storage_class" = replicated ]; then
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
  fi
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
