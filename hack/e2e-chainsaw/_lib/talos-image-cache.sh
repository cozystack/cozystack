# shellcheck shell=bash
# Shared helper: choose the Talos Image Factory base URL for tenant Kubernetes
# e2e CRs.
#
# Prefer the in-sandbox caching mirror (hack/e2e-talos-image-cache.yaml, deployed
# by hack/e2e-install-cozystack.bats) when a worker's CDI importer can actually
# reach it, otherwise fall back to the chart default (the public factory.talos.dev)
# by emitting nothing. The mirror rides out the public factory's flaky/range-less
# egress that otherwise stalls worker DataVolume imports past the 12-minute
# node-join deadline — see cozystack/cozystack#3231. Falling back to the default
# means the mirror can only help, never make CI worse.
#
# Reachability is not assumed. Tenant namespaces run under a default-deny Cilium
# egress installed by the tenant chart: a worker's importer may reach the `world`
# entity (hence the public factory) and the tenant's own tree, but not an
# arbitrary kube-system Service. Without an explicit allow, the importer resolves
# talos-image-cache.kube-system.svc yet its TCP connect to the ClusterIP is
# silently dropped and the disk import never starts. This helper installs that
# allow (the CiliumClusterwideNetworkPolicy shipped in the mirror manifest) and
# then verifies the path end to end from a Pod that faces the exact same egress
# rules as a real importer, so the mirror is used only when it genuinely works.
#
# Root cause + fix are from cozystack/cozystack#3254 (@lexfrei), ported here to
# the Chainsaw layout (hack/e2e-chainsaw/_lib/).

TALOS_IMAGE_CACHE_SVC_URL="${TALOS_IMAGE_CACHE_SVC_URL:-http://talos-image-cache.kube-system.svc}"
_TALOS_IMAGE_FACTORY_DECISION_FILE="${_TALOS_IMAGE_FACTORY_DECISION_FILE:-/tmp/e2e-talos-image-factory-url}"
TALOS_IMAGE_CACHE_MANIFEST="${TALOS_IMAGE_CACHE_MANIFEST:-hack/e2e-talos-image-cache.yaml}"
# Tenant namespace the kubernetes-* CRs are created in (run-kubernetes.sh and the
# oidc suites all target tenant-test); the reachability probe runs here so it
# inherits that namespace's default-deny egress, exactly as a worker importer does.
TALOS_IMAGE_CACHE_PROBE_NS="${TALOS_IMAGE_CACHE_PROBE_NS:-tenant-test}"
TALOS_IMAGE_CACHE_PROBE_POD="${TALOS_IMAGE_CACHE_PROBE_POD:-talos-image-cache-probe}"

# _apply_talos_image_cache_egress_policy: install the CiliumClusterwideNetworkPolicy
# that lets tenant CDI importer Pods egress to the mirror. It lives in the mirror
# manifest but is applied here, not in hack/e2e-install-cozystack.bats, because that
# manifest is applied before Cozystack (and thus before Cilium's CRDs) exists.
# Idempotent; best-effort — a failure just leaves the probe below to fall back.
_apply_talos_image_cache_egress_policy() {
  command -v yq >/dev/null 2>&1 || return 1
  yq 'select(.kind == "CiliumClusterwideNetworkPolicy")' "$TALOS_IMAGE_CACHE_MANIFEST" 2>/dev/null \
    | kubectl apply -f - >/dev/null 2>&1
}

# _talos_image_cache_probe_overrides: emit the kubectl --overrides JSON for the
# probe Pod. $1 is the container image, $2 the shell command it runs. The single
# container replaces the one `kubectl run` generates (merge patch semantics), and
# the security context satisfies the tenant namespace's restricted PSA.
#
# Pure string builder, kept separate so it can be unit-tested: an edit that
# unbalances a quote here would emit invalid JSON, every probe would fail, and CI
# would silently fall back to the public factory instead of erroring.
_talos_image_cache_probe_overrides() {
  printf '{"spec":{"securityContext":{"runAsNonRoot":true,"runAsUser":1000,"seccompProfile":{"type":"RuntimeDefault"}},"containers":[{"name":"c","image":"%s","command":["sh","-ec","%s"],"securityContext":{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]}}}]}}' \
    "$1" "$2"
}

# _talos_image_cache_probe_succeeded: succeed iff the probe output ($1) carries the
# strict 206 marker.
#
# Gate strictly on 206. The old localhost check kept a reachable-but-range-less
# mirror and only warned; here any non-206 — unreachable (code=000) or a plain 200 —
# falls back to the public factory instead. serve.py always answers a valid range
# with 206, so the 200 case is unreachable in practice; the strict gate keeps the
# "use the mirror only when it is fully working" rule simple.
#
# Pure predicate, kept separate so it can be unit-tested: this single comparison is
# what decides whether tenant workers are pointed at the mirror at all.
_talos_image_cache_probe_succeeded() {
  printf '%s' "$1" | grep -q 'code=206'
}

# _talos_image_cache_reachable_from_tenant: succeed iff a Pod that faces the exact
# same tenant egress restrictions and network path as a worker's CDI importer can
# reach the mirror Service ClusterIP with a byte-range (206) response.
#
# This is the load-bearing gate: a green result guarantees a real importer will
# reach the mirror. It runs a throwaway Pod in the tenant namespace, labelled
# cdi.kubevirt.io=importer (so the tenant egress default-deny plus the allow
# policy above apply to it just as they do to a real importer), and has it curl
# the ClusterIP Service for the seeded image with a Range header — exercising DNS,
# the egress policy, ClusterIP translation, and range support together, not merely
# that the server answers on localhost.
#
# cdi.kubevirt.io=importer is CDI's own importer-pod label. The probe and the
# egress policy's endpointSelector must both use it; if a future CDI renames it,
# they would still agree with each other but no longer with reality, and the probe
# would pass while real importers stayed blocked. It is correct as of the CDI
# version this platform ships (packages/system/kubevirt-cdi-operator).
_talos_image_cache_reachable_from_tenant() {
  local pod rel img curl_cmd overrides out
  pod=$(kubectl -n kube-system get pod \
    -l app.kubernetes.io/name=talos-image-cache \
    --field-selector=status.phase=Running \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null) || return 1
  [ -n "$pod" ] || return 1
  # Discover the seeded file (its presence also confirms the seed finished) and
  # probe that exact path, so the probe requests what a worker actually requests.
  rel=$(kubectl -n kube-system exec "$pod" -c serve -- sh -ec \
    'f=$(find /data -name "*.raw.xz" 2>/dev/null | head -n1); [ -n "$f" ] || exit 1; printf "%s" "${f#/data/}"' \
    2>/dev/null) || return 1
  [ -n "$rel" ] || return 1
  # Reuse the mirror's own digest-pinned image for the probe Pod. It is the same
  # image many CI hooks share, so by this late stage of the suite it is usually
  # already cached on whatever node the probe lands on and no pull is needed; if
  # the probe does land on a node without it, the pull is bounded by the Pod's
  # --pod-running-timeout below and a timeout just falls back to the public factory.
  img=$(kubectl -n kube-system get deploy talos-image-cache \
    -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null) || return 1
  [ -n "$img" ] || return 1

  # --retry rides out the seconds Cilium needs to program the freshly-applied allow
  # policy; a persistently unreachable ClusterIP still fails fast per attempt
  # (connect-timeout) and returns a non-206, falling back to the public factory.
  # The total budget is deliberately generous: this decision is cached once for the
  # whole suite, so a single slow policy-programming window must not permanently
  # disable the mirror. --max-time still bounds a hung attempt (a genuinely
  # unreachable ClusterIP resolves well within it and falls back).
  curl_cmd="curl -s -o /dev/null -w code=%{http_code} --retry 8 --retry-all-errors --retry-delay 5 --connect-timeout 5 --max-time 45 -r 0-0 ${TALOS_IMAGE_CACHE_SVC_URL}/${rel}"
  overrides=$(_talos_image_cache_probe_overrides "$img" "$curl_cmd")

  kubectl -n "$TALOS_IMAGE_CACHE_PROBE_NS" delete pod "$TALOS_IMAGE_CACHE_PROBE_POD" \
    --ignore-not-found >/dev/null 2>&1
  # No --rm: with --rm the Pod is gone the instant it exits, and --attach can lose
  # the tail of a fast-completing Pod's stream, leaving $out empty — a false negative
  # that would fall back to the flaky public factory this mirror exists to avoid.
  # Keep the Pod so its logs can be re-read if the attach stream was lost, then
  # delete it explicitly below.
  out=$(kubectl -n "$TALOS_IMAGE_CACHE_PROBE_NS" run "$TALOS_IMAGE_CACHE_PROBE_POD" \
    --restart=Never --attach --pod-running-timeout=90s \
    --image="$img" --labels='cdi.kubevirt.io=importer' \
    --overrides="$overrides" 2>&1) || true
  # --attach returns only after the container exits, so its logs are ready now;
  # fall back to them when the attach stream carried no result marker.
  if ! printf '%s' "$out" | grep -q 'code='; then
    out=$(kubectl -n "$TALOS_IMAGE_CACHE_PROBE_NS" logs "$TALOS_IMAGE_CACHE_PROBE_POD" 2>/dev/null || true)
  fi
  # Fire-and-forget: the result is already captured in $out, so nothing depends on
  # the Pod actually being gone. The pre-run delete above must NOT do this — there,
  # `kubectl run` would race a still-terminating Pod of the same name.
  kubectl -n "$TALOS_IMAGE_CACHE_PROBE_NS" delete pod "$TALOS_IMAGE_CACHE_PROBE_POD" \
    --ignore-not-found --wait=false >/dev/null 2>&1
  _talos_image_cache_probe_succeeded "$out"
}

# _talos_image_cache_deploy_state: answer "is the mirror Deployment there?" as
# exactly one of present, absent or unknown.
#
# --ignore-not-found is what makes a three-way answer possible: a genuine absence
# becomes exit 0 with no output, which leaves a non-zero exit meaning only "the
# question was never answered" — connection refused, Unauthorized, timed out.
# A plain `get` collapses those two into one exit code, and the caller below then
# reads a blip as an absence, caches it, and sends every tenant worker in the
# suite to the public factory over the live egress the mirror exists to avoid.
# Same idiom as the teardown poll in hack/e2e-chainsaw/_lib/etcd-cleanup.sh.
#
# talos_image_cache_diagnose asks the same question and answers it its own way,
# and the two are separate on purpose. That one runs on the node-join failure
# path under a phase budget, where re-asking spends what the crust-gather
# snapshot after it needs, and it gives up after one attempt. This one runs on
# the happy path, where a re-ask costs a suite nothing and buys it the mirror.
#
# The re-ask is small, and each attempt has to be bounded twice over for the
# re-ask to be worth anything. kubectl's --request-timeout defaults to 0 — no
# deadline on the request at all — and the case this loop exists for is a
# connection that establishes and then stalls, which client-go's dial timeout
# does not bound. The flag on its own is still not a wall-clock bound: against
# an unreachable apiserver kubectl retries discovery several times before it
# gives up, so a call carrying only the flag costs a multiple of it, which
# hack/e2e-chainsaw/_lib/pod-label-census.sh measured and sized its caller's op
# timeout around. Asking three times would inherit that multiple, inside a
# Chainsaw op the rest of the tenant test has to fit in. So each attempt takes
# both bounds, the pairing the reads in run-kubernetes.sh use and explain beside
# its pod list: --request-timeout bounds the HTTP request, timeout bounds the
# client retrying against a wedged apiserver. A kill by timeout exits non-zero
# and is simply another attempt that did not answer. Past that, an apiserver
# that is down does not become reachable by asking longer, and a run whose
# apiserver is down has bigger problems than which factory it pulls from. A
# real absence answers on the first try and waits for nothing.
#
# The wrapper is skipped rather than depended on when `timeout` is not installed,
# for the reason _talos_image_cache_bounded_read gives below and one more that is
# specific to a loop: its exit 127 would otherwise count as an attempt that did
# not answer, three times over, and a missing local binary would pin the whole
# suite to the public factory while every message blamed the apiserver.
#
# The outer bound sits ABOVE the inner one rather than level with it, and that
# gap is the whole reason kubectl ever gets to explain itself. Measured against a
# stub apiserver, the three ways this call fails are not alike. A refused port
# answers in under a second and names the refusal. One that completes TCP but
# stalls in TLS gets kubectl's own handshake deadline, which is shorter than
# either bound and fires twice inside the window, so there is text even though
# the wrapper ends up killing the call. A blackholed address — SYN dropped, no
# RST, which is what a wedged node or a netpol looks like — produces nothing at
# all until some deadline expires, and with the two bounds equal the expiry that
# arrives is the wall-clock kill, at the same instant as the request deadline it
# beats to it. `--request-timeout` then cannot fire in any of the three, which
# makes it decoration. Ten seconds of headroom hands that case back to kubectl:
# the request deadline ends the attempt and says why, and the wrapper stays as
# the backstop for a client that overruns its own deadline. The cost is ten more
# seconds per attempt on a path where the apiserver is already unreachable,
# against a 67m op.
#
# kubectl's stderr is therefore deliberately not swallowed: it is the only thing
# that separates refused from unauthorized from stalled, and suppressing it would
# replace one silent failure with another. It is still not guaranteed — a kill
# can always land before the client has said anything — so the last attempt's
# exit status is named too, in the vocabulary the diagnostic reads below use for
# 124 and 137. Between the two the caller's warning always has something real to
# point at.
_TALOS_IMAGE_CACHE_QUERY_TRIES="${_TALOS_IMAGE_CACHE_QUERY_TRIES:-3}"
_TALOS_IMAGE_CACHE_QUERY_DELAY="${_TALOS_IMAGE_CACHE_QUERY_DELAY:-5}"
_talos_image_cache_deploy_state() {
  local out rc try=1
  while :; do
    rc=0
    if command -v timeout >/dev/null 2>&1; then
      out=$(timeout -k 5 40 kubectl -n kube-system get deploy talos-image-cache \
        --request-timeout=30s --ignore-not-found -o name) || rc=$?
    else
      out=$(kubectl -n kube-system get deploy talos-image-cache \
        --request-timeout=30s --ignore-not-found -o name) || rc=$?
    fi
    if [ "$rc" -eq 0 ]; then
      if [ -n "$out" ]; then
        printf 'present'
      else
        printf 'absent'
      fi
      return 0
    fi
    if [ "$try" -ge "$_TALOS_IMAGE_CACHE_QUERY_TRIES" ]; then
      echo "WARNING: the talos-image-cache lookup gave up after ${_TALOS_IMAGE_CACHE_QUERY_TRIES} attempts; the last one exited ${rc} (124 = cut off at the wall-clock bound, 137 = SIGKILL after it, and both of those print nothing of their own; any other status is kubectl's, and its message is above)" >&2
      printf 'unknown'
      return 0
    fi
    try=$((try + 1))
    sleep "$_TALOS_IMAGE_CACHE_QUERY_DELAY"
  done
}

# resolve_talos_image_factory_url: print the imageFactoryURL to use, or an empty
# string to signal "use the chart default". A decision that was actually reached
# — the mirror is there, or it demonstrably is not — is taken once (with a
# bounded wait for the mirror to finish seeding) and cached in a /tmp file that
# persists across bats files (they all exec in the same sandbox container), so
# only the first tenant Kubernetes test pays the readiness wait. A lookup that
# never got an answer decides nothing and caches nothing; the next tenant test
# asks again.
resolve_talos_image_factory_url() {
  if [ -f "$_TALOS_IMAGE_FACTORY_DECISION_FILE" ]; then
    cat "$_TALOS_IMAGE_FACTORY_DECISION_FILE"
    return 0
  fi
  local url="" state
  state=$(_talos_image_cache_deploy_state)
  if [ "$state" = unknown ]; then
    # Nothing was established, so nothing is cached: this caller falls back for
    # itself and the next tenant test asks again.
    echo "WARNING: could not determine whether the talos-image-cache mirror is deployed (the lookup's own account of why is above) — this test falls back to public factory.talos.dev; nothing is cached, so the next tenant test asks again" >&2
    return 0
  fi
  if [ "$state" = present ]; then
    # Available only after the seed initContainer has fully fetched the image, so
    # this is the signal that the mirror is warm.
    if kubectl -n kube-system rollout status deploy/talos-image-cache --timeout=12m >/dev/null 2>&1; then
      # Available proves only that the seed finished and the server answers on
      # localhost — NOT that a worker's importer, penned in by the tenant egress
      # default-deny, can reach the ClusterIP. Install the allow policy (Cilium is
      # up by now, unlike at seed-deploy time) and verify the whole importer path
      # end to end. Point tenants at the mirror only when that check passes;
      # otherwise fall back to the public factory so the mirror can only help,
      # never point workers at an unreachable ClusterIP and make CI worse.
      _apply_talos_image_cache_egress_policy || true
      if _talos_image_cache_reachable_from_tenant; then
        url="$TALOS_IMAGE_CACHE_SVC_URL"
        echo "talos-image-cache mirror reachable from tenant namespace (byte-range 206 verified) — tenant workers import from ${url}" >&2
      else
        echo "WARNING: talos-image-cache is Available but a tenant-scoped probe could not reach its ClusterIP with a 206 — tenant workers fall back to public factory.talos.dev" >&2
      fi
    else
      echo "WARNING: talos-image-cache mirror not Available in time — tenant workers fall back to public factory.talos.dev" >&2
    fi
  else
    echo "talos-image-cache mirror not deployed — tenant workers use public factory.talos.dev" >&2
  fi
  printf '%s' "$url" > "$_TALOS_IMAGE_FACTORY_DECISION_FILE"
  printf '%s' "$url"
}

# One value, so the wall-clock bound and the per-request bound cannot drift apart
# into a pair where the inner one outlasts the outer. Overridable for the same
# reason the caller's bounds are: a test that turns the caller's budgets down and
# leaves these at the real twenty seconds waits out a bound it thought it had
# lowered. Not read from the caller's COZY_DIAG_* -- this file is sourced before
# those exist and is also used on its own.
#
# Spelled out at each call site rather than precomputed into one prefix string,
# because a prefix built here is frozen at source time while the paired
# `--request-timeout` beside it expands at call time. Overriding the knob after
# sourcing -- which is the only moment a test can do it, and how the caller's own
# bounds are overridden -- would then move the inner bound and leave the outer at
# twenty seconds: exactly the drift the single value exists to prevent, arriving
# through the mechanism meant to make it adjustable.
#
# _talos_image_cache_seconds <value> <default> <name> [positive]: echo <value> when
# it is bare integer seconds, else echo <default> and say why on stderr, naming the
# knob the caller passed. `positive` additionally rejects zero.
#
# The name and the flag are parameters rather than constants because this validates
# two knobs with different rules, and the first version hardcoded one knob's name
# into all three messages -- so a bad grace was reported as a bad timeout, with the
# grace's default quoted beside the timeout's name. A note that sends the reader to
# the wrong knob is the same defect class this file exists to remove.
#
# Its own copy of the check rather than a call to run-kubernetes.sh's: that file
# sources this one, so nothing of its is defined yet here, and this file is also used
# on its own. A suffix would reach `--request-timeout=${...}s` as `2ms` and make every
# dump fail instantly; a leading zero is all digits and reads as octal in arithmetic
# and decimal elsewhere; and zero disables `timeout` outright while
# `--request-timeout=0s` means no timeout to kubectl, which is the unbounded read this
# file just stopped having.
_talos_image_cache_seconds() {
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
      echo "» WARNING: ignoring ${3}='${1}' (a leading zero reads as octal in arithmetic and as decimal elsewhere); using ${2}" >&2
      printf '%s\n' "${2}"
      ;;
    *) printf '%s\n' "${1}" ;;
  esac
}
# Named once, for the reason its twin's are: a default passed as a literal at every
# call site leaves the re-checks falling back to a stale number when it changes, and
# only on the error path.
_TALOS_IMAGE_CACHE_READ_TIMEOUT_DEFAULT=20
_TALOS_IMAGE_CACHE_READ_GRACE_DEFAULT=5
_TALOS_IMAGE_CACHE_READ_TIMEOUT=$(_talos_image_cache_seconds "${_TALOS_IMAGE_CACHE_READ_TIMEOUT:-$_TALOS_IMAGE_CACHE_READ_TIMEOUT_DEFAULT}" "$_TALOS_IMAGE_CACHE_READ_TIMEOUT_DEFAULT" _TALOS_IMAGE_CACHE_READ_TIMEOUT positive)

# Validated too. A suffix really is legal for `timeout -k`, but that was never the
# hazard: `timeout -k abc 20` exits 125 before running the command, so a non-numeric
# grace makes every dump report 125 and collects nothing. Zero stays legal -- it only
# skips the follow-up SIGKILL, which kubectl does not need.
_TALOS_IMAGE_CACHE_READ_GRACE=$(_talos_image_cache_seconds "${_TALOS_IMAGE_CACHE_READ_GRACE:-$_TALOS_IMAGE_CACHE_READ_GRACE_DEFAULT}" "$_TALOS_IMAGE_CACHE_READ_GRACE_DEFAULT" _TALOS_IMAGE_CACHE_READ_GRACE)

# _talos_image_cache_bounded_read <label> <command...>: one bounded diagnostic
# dump, with the outcome named whenever it does not finish.
#
# A local twin of run-kubernetes.sh's cozy_diag_read rather than a call to it,
# for the reason given above: this file is sourced from the top of that one, about
# a thousand lines before COZY_DIAG_* exists, and it is also used on its own.
#
# The note is what makes bounding these dumps safe rather than a new way to
# mislead. `timeout` prints nothing when it fires, so a cut-off dump would leave a
# bare section header and nothing else -- and an empty
# `get deploy,pod,svc,endpointslice` section reads as a cache with no Pod, no
# Service and no EndpointSlice. That is a finding a triager acts on, drawn from a
# read that never reached the apiserver, which is the same mistake the gate above
# was rewritten to stop making. Callers run inside a `{ … } >&2` group, so a plain
# echo lands beside the dump it describes.
#
# Always returns 0: this whole function is best-effort and never fails its caller.
_talos_image_cache_bounded_read() {
  local label="$1"
  shift
  local rc=0

  # Unbounded when `timeout` is absent rather than bounded into exit 127, which
  # would drop every dump and blame the cluster for a missing local binary. The
  # caller says so once; see cozy_diag_read for the same shape and reason.
  # Re-checked here as well as at assignment: a value set after this file was sourced
  # -- which is how a test lowers it -- never passed that check, and zero there would
  # hand `timeout` a duration that disables it.
  _TALOS_IMAGE_CACHE_READ_TIMEOUT=$(_talos_image_cache_seconds "${_TALOS_IMAGE_CACHE_READ_TIMEOUT-}" "$_TALOS_IMAGE_CACHE_READ_TIMEOUT_DEFAULT" _TALOS_IMAGE_CACHE_READ_TIMEOUT positive)
  _TALOS_IMAGE_CACHE_READ_GRACE=$(_talos_image_cache_seconds "${_TALOS_IMAGE_CACHE_READ_GRACE-}" "$_TALOS_IMAGE_CACHE_READ_GRACE_DEFAULT" _TALOS_IMAGE_CACHE_READ_GRACE)
  if command -v timeout >/dev/null 2>&1; then
    timeout -k "$_TALOS_IMAGE_CACHE_READ_GRACE" "$_TALOS_IMAGE_CACHE_READ_TIMEOUT" "$@" || rc=$?
  else
    "$@" || rc=$?
  fi
  case "$rc" in
    0) ;;
    124)
      echo "=== ${label}: read did not finish within ${_TALOS_IMAGE_CACHE_READ_TIMEOUT}s and was cut off; whatever it had not printed is absent from this log, not absent from the cluster ==="
      ;;
    137)
      echo "=== ${label}: read was killed before it finished (SIGKILL); whatever it had not printed is absent from this log, not absent from the cluster ==="
      ;;
    *)
      echo "=== ${label}: read failed (exit ${rc}); what it would have shown was not observed ==="
      ;;
  esac
  return 0
}

# talos_image_cache_diagnose: on-demand diagnostic for the node-join failure path
# in run-kubernetes.sh (Chainsaw-branch addition, not in #3254). Re-probes the
# mirror from the tenant namespace — the same path a real importer faces — and
# dumps the cacher + egress-policy state, so an operator can tell "the egress
# allow / ClusterIP path is broken" apart from "the upstream factory was slow".
# All output to stderr; never fails the caller.
#
# Its own reads are bounded because its only caller is the node-join failure path
# in run-kubernetes.sh, which ends in the `exit 1` that triggers the tenant
# crust-gather snapshot: a read that hangs here holds the Chainsaw op until the
# op is killed and the snapshot after it is lost rather than truncated.
#
# The reachability re-probe is the exception, and the exception is wider than the
# curl budget it is easy to mistake it for. _talos_image_cache_reachable_from_tenant
# issues seven unbounded management-cluster calls around that curl -- the Running-pod
# jsonpath get, the `exec` that finds the seeded file, the deploy-image jsonpath
# get, the pre-run `delete pod` that waits for deletion, the `run --attach` whose
# --pod-running-timeout bounds the wait for Running and not the attach stream, and
# the `logs` re-read and the fire-and-forget `delete pod` after it, whose
# --wait=false drops the wait for deletion and not the DELETE request -- and any one of them can hang on a wedged apiserver exactly
# as the reads below could before they were bounded.
#
# They are left alone here rather than bounded, and the reason is not that they
# are safe. That function also runs on the happy path from
# resolve_talos_image_factory_url, where its non-zero return disables the mirror
# for the whole suite, so changing when it gives up is a decision about which
# image factory tenant workers use and does not belong in a diagnostic's commit.
# What covers them meanwhile is the caller's phase budget, and only partly: the
# gate stops the re-probe from being STARTED once the phase is out of budget, and
# nothing stops it running long once started. Closing that surface is its own
# change, tracked in cozystack/cozystack#3666.
talos_image_cache_diagnose() {
  # The gate is bounded like everything under it, and its outcomes are three
  # rather than two: deployed, absent, and a read that did not answer.
  #
  # --ignore-not-found is what separates the last two. Without it "absent" and
  # "the call failed" are the same non-zero status -- `kubectl get` exits 1 for a
  # refused connection, for Unauthorized and for an unrecognised kind exactly as
  # it does for NotFound -- so a read that never reached the apiserver was
  # announced as "not deployed", a claim about the cluster drawn from a call that
  # failed to make one, and it retired the cache hypothesis on the one path that
  # exists to test it. With the flag, absent is exit 0 with empty output and
  # every other non-zero is unknown.
  # Validated once here, before the first read composes a `--request-timeout` string
  # from it. The re-check inside _talos_image_cache_bounded_read corrects the wall
  # clock, but each call site interpolates the value itself, so the first dump would
  # otherwise pair a corrected `timeout` with a stale `--request-timeout=0s`.
  _TALOS_IMAGE_CACHE_READ_TIMEOUT=$(_talos_image_cache_seconds "${_TALOS_IMAGE_CACHE_READ_TIMEOUT-}" "$_TALOS_IMAGE_CACHE_READ_TIMEOUT_DEFAULT" _TALOS_IMAGE_CACHE_READ_TIMEOUT positive)
  _TALOS_IMAGE_CACHE_READ_GRACE=$(_talos_image_cache_seconds "${_TALOS_IMAGE_CACHE_READ_GRACE-}" "$_TALOS_IMAGE_CACHE_READ_GRACE_DEFAULT" _TALOS_IMAGE_CACHE_READ_GRACE)
  local gate_rc=0 gate_out
  if command -v timeout >/dev/null 2>&1; then
    gate_out=$(timeout -k "$_TALOS_IMAGE_CACHE_READ_GRACE" "$_TALOS_IMAGE_CACHE_READ_TIMEOUT" kubectl -n kube-system get deploy talos-image-cache \
      --ignore-not-found -o name \
      "--request-timeout=${_TALOS_IMAGE_CACHE_READ_TIMEOUT}s") || gate_rc=$?
  else
    gate_out=$(kubectl -n kube-system get deploy talos-image-cache --ignore-not-found -o name \
      "--request-timeout=${_TALOS_IMAGE_CACHE_READ_TIMEOUT}s") || gate_rc=$?
  fi
  if [ "$gate_rc" -ne 0 ]; then
    echo "could not read whether talos-image-cache is deployed (exit $gate_rc); whether the mirror was in play is unknown, not no" >&2
    return 0
  fi
  if [ -z "$gate_out" ]; then
    echo "talos-image-cache not deployed — tenant workers used the public factory; nothing to diagnose" >&2
    return 0
  fi
  # The whole block goes to stderr, which is also where the notes belong, so the
  # reads below need no redirect of their own: fd1 is already stderr in here and
  # kubectl's own stderr is inherited to the same place.
  {
    if _talos_image_cache_reachable_from_tenant; then
      echo "talos-image-cache reachable from ${TALOS_IMAGE_CACHE_PROBE_NS} at diagnose time (byte-range 206)"
    else
      echo "talos-image-cache NOT reachable from ${TALOS_IMAGE_CACHE_PROBE_NS} at diagnose time (tenant egress allow missing/unprogrammed, or cache down)"
    fi
    echo "--- cache deploy/pod/svc/endpointslice ---"
    _talos_image_cache_bounded_read 'cache deploy/pod/svc/endpointslice' \
      kubectl -n kube-system get deploy,pod,svc,endpointslice \
      -l app.kubernetes.io/name=talos-image-cache -o wide "--request-timeout=${_TALOS_IMAGE_CACHE_READ_TIMEOUT}s"
    echo "--- importer egress allow policy ---"
    _talos_image_cache_bounded_read 'importer egress allow policy' \
      kubectl get ciliumclusterwidenetworkpolicy \
      e2e-talos-image-cache-importer-egress -o wide "--request-timeout=${_TALOS_IMAGE_CACHE_READ_TIMEOUT}s"
    echo "--- serve container log (tail) ---"
    _talos_image_cache_bounded_read 'serve container log' \
      kubectl -n kube-system logs \
      -l app.kubernetes.io/name=talos-image-cache -c serve --tail=50 --prefix
  } >&2
}
