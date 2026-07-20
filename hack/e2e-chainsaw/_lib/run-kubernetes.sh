# shellcheck shell=bash
# Sourced by the chainsaw kubernetes-latest/previous Tests after cd to repo root.
. hack/e2e-chainsaw/_lib/remediation-guard.sh
. hack/e2e-chainsaw/_lib/talos-image-cache.sh

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
  # (timeout's own -k 30 / 300 are distinct from crust-gather's -k kubeconfig.)
  timeout -k 30 300 crust-gather collect -k "${CURRENT_TENANT_KC}" --exclude-kind Secret -f "$_snap/${CURRENT_TENANT_KC}" >/dev/null 2>&1 || true
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
  # leaving localhost refusing connections for the entire 12m node-Ready wait
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
  # Ready window even though the CNI converges fine. One 12m deadline that polls
  # for ">=2 nodes Ready" is robust to wherever the time goes.
  if ! timeout 12m bash -c '
    until [ "$(kubectl --kubeconfig tenantkubeconfig-'"${test_name}"' get nodes --no-headers 2>/dev/null | grep -cw Ready)" -ge 2 ]; do
      sleep 5
    done
  '; then
    # Node-join failed: fewer than 2 tenant nodes became Ready inside the 12m
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
    echo "=== node-join failed: fewer than 2 tenant nodes Ready within 12m — diagnostics follow ==="
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

  # Backend 1
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
        image: nginx:alpine
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

  # Wait for pods readiness
  kubectl wait deployment --kubeconfig "tenantkubeconfig-${test_name}" "${test_name}-backend" -n tenant-test --for=condition=Available --timeout=300s

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
