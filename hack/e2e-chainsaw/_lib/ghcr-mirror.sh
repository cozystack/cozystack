# shellcheck shell=bash
# Shared helper: point tenant Kubernetes e2e worker nodes at the in-sandbox
# ghcr.io pull-through registry (hack/e2e-ghcr-mirror.yaml) when it is up, via the
# chart's `talos.registryMirrors` knob, otherwise emit nothing so workers fall back
# to pulling ghcr.io directly.
#
# Why: tenant worker Talos nodes pull `ghcr.io/siderolabs/kubelet` directly; that
# egress is flaky/rate-limited from the CI runner and the pull times out with a TLS
# handshake timeout, so the kubelet service never starts and no tenant node joins.
# Diagnosed in cozystack/cozystack#3548 (in-guest Talos dmesg), tracked by #3513.
# Same flaky-public-egress class the talos-image-cache (#3231) fixed for the OS image.
# One variant of that flake, not the whole of it: #3513 also records failures that
# lose the budget before Talos ever reaches the kubelet pull, and those this cannot
# touch.
#
# WHAT THE GATE BELOW ESTABLISHES, precisely, because it is easy to overstate: the
# Deployment went Available, and the API accepted the egress allow. Neither observes
# the tenant-side datapath. If the allow selects an identity the worker's pull does
# not actually carry (see assumption 2 in hack/e2e-ghcr-mirror.yaml), workers are
# pointed at a ClusterIP whose SYNs are dropped, and Talos burns a TCP connect
# timeout per pull before falling back to ghcr.io -- slower than not mirroring at
# all, on exactly the runs this exists to protect. So: the fallback paths cannot
# make CI worse, the committed path is only as safe as the assumptions listed
# there.
#
# talos-image-cache.sh gates the equivalent decision on a byte-level 206 from a Pod
# carrying the consumer's own label, which is what turns that assumption into a
# measurement. Doing the same here is the hardening follow-up, and it is also what
# would let the suite assert the mirror served the pull rather than the worker
# quietly falling back.

GHCR_MIRROR_SVC_URL="${GHCR_MIRROR_SVC_URL:-http://ghcr-mirror.kube-system.svc}"
_GHCR_MIRROR_DECISION_FILE="${_GHCR_MIRROR_DECISION_FILE:-/tmp/e2e-ghcr-mirror-endpoint}"
GHCR_MIRROR_MANIFEST="${GHCR_MIRROR_MANIFEST:-hack/e2e-ghcr-mirror.yaml}"

# ghcr_registry_mirrors_block: pure string builder. Given a mirror endpoint URL
# ($1, may be empty), print the `registryMirrors:` sub-block for a tenant Kubernetes
# CR `spec.talos` (4-space indented, i.e. nested under `  talos:`), or nothing when
# the endpoint is empty. Kept separate so it can be unit-tested: an indentation or
# quoting slip here would emit an invalid CR and every kubernetes-* test would fail.
ghcr_registry_mirrors_block() {
  local endpoint="$1"
  [ -n "$endpoint" ] || return 0
  printf '    registryMirrors:\n      ghcr.io:\n        endpoints:\n          - %s\n' "$endpoint"
}

# _apply_ghcr_mirror_egress_policy: install the CiliumClusterwideNetworkPolicy that
# lets tenant worker VM (virt-launcher) Pods egress to the mirror. It ships in the
# manifest but is applied here, after Cilium's CRDs exist. Idempotent, best-effort.
#
# On failure it PRINTS the reason to stdout for the caller to surface. The causes
# are not all alike -- a missing yq, an apiVersion the cluster does not serve, an
# RBAC denial are permanent, while an apiserver blip is not -- and the exit code
# cannot tell them apart. Discarding the message would leave the caller announcing
# a cause it does not know, in the subsystem this exists to make legible.
_apply_ghcr_mirror_egress_policy() {
  if ! command -v yq >/dev/null 2>&1; then
    echo "yq is not installed, so the policy could not be extracted from ${GHCR_MIRROR_MANIFEST}"
    return 1
  fi
  # Braces, not a bare `2>&1 >/dev/null`: the goal is to keep stderr (the reason)
  # on stdout for the caller and drop kubectl's success chatter, and the braced
  # form says that unambiguously.
  { yq 'select(.kind == "CiliumClusterwideNetworkPolicy")' "$GHCR_MIRROR_MANIFEST" \
    | kubectl apply -f - >/dev/null; } 2>&1
}

# resolve_ghcr_mirror_endpoint: print the mirror endpoint URL to use, or an empty
# string to signal "pull ghcr.io directly". Resolved once and cached in a /tmp file
# that persists across bats files (same sandbox container), so only the first tenant
# test pays the readiness wait.
resolve_ghcr_mirror_endpoint() {
  if [ -f "$_GHCR_MIRROR_DECISION_FILE" ]; then
    cat "$_GHCR_MIRROR_DECISION_FILE"
    return 0
  fi
  local endpoint="" cache=1 out rc avail ready apply_err
  # One `get deploy` serves both the existence check and the not-deployed-vs-error
  # split: a genuine NotFound is a stable decision worth caching, but any other
  # failure (transient apiserver blip) must NOT be cached, or one blip would
  # disable the mirror for every later test in the shared sandbox.
  #
  # Capture inside `if` (not a bare `out=$(...); rc=$?`): the chainsaw suites run
  # this script under `sh -c` with `set -eu`, and dash inherits errexit into a
  # command substitution, so a non-zero kubectl in a bare assignment would kill
  # the script before either fallback branch runs. bash does not inherit it,
  # which is why a bare assignment survives locally but not in CI.
  if out=$(kubectl -n kube-system get deploy ghcr-mirror 2>&1); then
    rc=0
  else
    rc=$?
  fi
  if [ "$rc" -ne 0 ]; then
    case "$out" in
      # The API's own NotFound only. A bare "not found" also matches a shell
      # reporting a missing kubectl and a bad --context, neither of which says
      # anything about whether the mirror was applied -- and both would then be
      # cached as the stable answer for every later suite in the sandbox.
      *NotFound*)
        echo "ghcr-mirror not deployed -- tenant workers pull ghcr.io directly" >&2 ;;
      *)
        echo "WARNING: could not query ghcr-mirror Deployment (transient); not caching, will retry" >&2
        cache=0 ;;
    esac
  else
    # Readiness is decided once, then acted on once. The watch is the cheap way to
    # learn it, but `rollout status` also exits non-zero on a broken or aborted
    # watch, and its output goes to /dev/null -- so when it fails, ask the API what
    # the Deployment's own condition says instead of reading the watch's exit code
    # as the answer.
    #
    # 2m, not longer: the wait is serial with the rest of the 50m e2e step, whose
    # budget inventory has to stay above the sum of its legs, and spending more here
    # eats the margin the failure-path diagnostics need in order to be collected at
    # all. It is already generous -- the Deployment pulls one small digest-pinned
    # image and was applied at install time, tens of minutes earlier.
    if kubectl -n kube-system rollout status deploy/ghcr-mirror --timeout=2m >/dev/null 2>&1; then
      ready=yes
    else
      if avail=$(kubectl -n kube-system get deploy ghcr-mirror \
        -o jsonpath='{.status.conditions[?(@.type=="Available")].status}' 2>/dev/null); then
        :
      else
        avail=""
      fi
      case "$avail" in
        True)  ready=yes ;;
        False) ready=no ;;
        *)     ready=unknown ;;
      esac
    fi
    case "$ready" in
      yes)
        # Only commit the endpoint once the tenant egress allow is actually in
        # place. The mirror is a kube-system ClusterIP that the tenant default-deny
        # egress drops without this allow; pointing workers at it when the policy
        # did not apply just adds dial-timeout latency before Talos falls back.
        # Leaving the endpoint empty is what keeps that cost off the run -- it
        # removes the one failure mode this function can see, not every one of them
        # (see the header on what the gate does and does not establish).
        if apply_err=$(_apply_ghcr_mirror_egress_policy); then
          endpoint="$GHCR_MIRROR_SVC_URL"
          echo "ghcr-mirror ready and egress allow applied -- tenant workers mirror ghcr.io via ${endpoint}" >&2
        else
          # Not cached, for the same reason as the failed query above: a failed
          # apply says nothing about whether the next attempt would succeed. The
          # reason is printed rather than characterised -- some causes here are
          # permanent and retrying will not help, and only the message says which.
          echo "WARNING: ghcr-mirror ready but its egress allow failed to apply; not caching, will retry. Reason: ${apply_err:-none reported}" >&2
          cache=0
        fi
        ;;
      no)
        # Cached, and this is a trade rather than an invariant: a Deployment can
        # still go Available later on its own -- a kubelet image-pull retry or a
        # Pending Pod finally scheduling would both do it, with nothing re-applying
        # the manifest. What caching buys is not re-paying the wait in every later
        # suite, serial with the rest of the step; what it costs is missing a mirror
        # that recovers mid-run. For a best-effort accelerator that trade is worth
        # taking, and re-reading the condition cheaply on a cached miss would take
        # it back if it ever bites.
        echo "WARNING: ghcr-mirror not Available -- tenant workers pull ghcr.io directly" >&2
        ;;
      *)
        echo "WARNING: could not confirm ghcr-mirror readiness (transient); not caching, will retry" >&2
        cache=0
        ;;
    esac
  fi
  # Cache atomically (temp + mv) so a concurrent reader in the shared sandbox never
  # sees a half-written decision file.
  if [ "$cache" -eq 1 ]; then
    printf '%s' "$endpoint" > "${_GHCR_MIRROR_DECISION_FILE}.tmp" \
      && mv "${_GHCR_MIRROR_DECISION_FILE}.tmp" "$_GHCR_MIRROR_DECISION_FILE"
  fi
  printf '%s' "$endpoint"
}

# _ghcr_mirror_bounded_read <label> <command...>: run one diagnostic read under a
# wall-clock bound and name the outcome when it does not finish, so a read cut off
# mid-flight is not misread as the cluster being silent. Always returns 0: the only
# caller sits before the node-join block's `exit 1`, and a non-zero here would
# replace that exit under `set -e`.
#
# Bounded by the caller's own COZY_DIAG_READ_TIMEOUT/COZY_DIAG_READ_GRACE, the same
# knobs cozy_diag_read uses, so lowering the node-join block's budget lowers these
# too -- a private knob here would sit at its own default while the caller's budget
# dropped. The block validates those values before this runs; standalone use
# (hack/ghcr-mirror_test.bats exercises this without run-kubernetes.sh) falls back
# to the block's own 20s/5s defaults. The bound lives here rather than in a call to
# cozy_diag_read because that function is defined in run-kubernetes.sh and this file
# is sourced on its own, the same reason talos-image-cache.sh carries its own copy.
_ghcr_mirror_bounded_read() {
  local label="$1"
  shift
  local to="${COZY_DIAG_READ_TIMEOUT:-20}" grace="${COZY_DIAG_READ_GRACE:-5}" rc=0
  if command -v timeout >/dev/null 2>&1; then
    timeout -k "$grace" "$to" "$@" || rc=$?
  else
    "$@" || rc=$?
  fi
  case "$rc" in
    0) ;;
    124|137)
      echo "=== ${label}: read did not finish within ${to}s and was cut off; what it had not printed is absent from this log, not from the cluster ===" >&2 ;;
    *)
      echo "=== ${label}: read failed (exit ${rc}); what it would have shown was not observed ===" >&2 ;;
  esac
  return 0
}

# ghcr_mirror_diagnose: on-demand diagnostic for the node-join failure path.
#
# Pointing workers at the mirror and their nodes joining does not establish that
# the mirror served anything -- Talos falls back to public ghcr.io on its own, so
# a pass is consistent with the mirror never being touched, and a failure is
# consistent with it being unreachable. registry:2's access log is what tells the
# two apart: a `GET /v2/siderolabs/kubelet/...` line means a worker really came
# through here. Without it #3513 gains another run that has to be reasoned about
# from the outside, which is how it survived four attempts.
#
# All output to stderr; never fails the caller. Every read is wall-clock bounded
# by the caller's COZY_DIAG_READ_TIMEOUT/COZY_DIAG_READ_GRACE.
ghcr_mirror_diagnose() {
  local hits log gate_rc=0
  local to="${COZY_DIAG_READ_TIMEOUT:-20}" grace="${COZY_DIAG_READ_GRACE:-5}"
  # The existence gate is bounded too: a wedged apiserver hangs `get deploy` just
  # as it hangs the reads below.
  if command -v timeout >/dev/null 2>&1; then
    timeout -k "$grace" "$to" \
      kubectl -n kube-system get deploy ghcr-mirror >/dev/null 2>&1 || gate_rc=$?
  else
    kubectl -n kube-system get deploy ghcr-mirror >/dev/null 2>&1 || gate_rc=$?
  fi
  if [ "$gate_rc" -ne 0 ]; then
    echo "ghcr-mirror not deployed -- tenant workers pulled ghcr.io directly; nothing to diagnose" >&2
    return 0
  fi
  {
    echo "--- ghcr-mirror deploy/pod/svc/endpointslice ---"
    _ghcr_mirror_bounded_read 'ghcr-mirror deploy/pod/svc/endpointslice' \
      kubectl -n kube-system get deploy,pod,svc,endpointslice \
      -l app.kubernetes.io/name=ghcr-mirror -o wide
    echo "--- worker egress allow policy ---"
    _ghcr_mirror_bounded_read 'worker egress allow policy' \
      kubectl get ciliumclusterwidenetworkpolicy e2e-ghcr-mirror-worker-egress -o wide
    # The answer to "did a worker actually pull through the mirror".
    #
    # Filter the whole log rather than tailing it. The readinessProbe requests /v2/
    # every 5s and registry:2 writes an access line per request, so ~12 lines a
    # minute pile up after the worker's pull; by the time this runs -- past an 18m
    # node-join budget -- that is 200+ probe lines on top of the one request worth
    # finding, and any fixed tail window reports "no kubelet pull" for a mirror that
    # served it. --tail=-1 is explicit because kubectl defaults a label-selected
    # read to the last 10 lines, not to the whole log. It is the wall clock, not the
    # line count, that bounds this read: --tail=-1 stays, timeout caps the time.
    echo "--- did a worker pull the kubelet image through the mirror? ---"
    # Read and filter as separate steps. Piping them would make an unreadable log
    # and a log with no kubelet request produce the same 0, and the second is a
    # finding about the workers while the first is a statement about nothing.
    local log_rc=0
    if command -v timeout >/dev/null 2>&1; then
      log=$(timeout -k "$grace" "$to" \
        kubectl -n kube-system logs -l app.kubernetes.io/name=ghcr-mirror \
        --tail=-1 2>/dev/null) || log_rc=$?
    else
      log=$(kubectl -n kube-system logs -l app.kubernetes.io/name=ghcr-mirror \
        --tail=-1 2>/dev/null) || log_rc=$?
    fi
    if [ "$log_rc" -eq 0 ]; then
      hits=$(printf '%s\n' "$log" | grep -cF '/v2/siderolabs/kubelet') || hits=0
      if [ "${hits:-0}" -gt 0 ]; then
        echo "the mirror served ${hits} kubelet-image request(s), so workers did reach it"
      else
        echo "no kubelet-image request reached the mirror: workers either fell back to public ghcr.io or never got as far as the pull"
      fi
    else
      echo "could not read the mirror's log; this says nothing either way about whether workers used it"
    fi
    echo "--- registry access log (last 50 lines, for context) ---"
    _ghcr_mirror_bounded_read 'registry access log (last 50 lines)' \
      kubectl -n kube-system logs -l app.kubernetes.io/name=ghcr-mirror \
      --tail=50 --prefix
  } >&2
  return 0
}
