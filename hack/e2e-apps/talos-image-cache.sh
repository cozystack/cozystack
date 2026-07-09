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

TALOS_IMAGE_CACHE_SVC_URL="${TALOS_IMAGE_CACHE_SVC_URL:-http://talos-image-cache.kube-system.svc}"
_TALOS_IMAGE_FACTORY_DECISION_FILE="${_TALOS_IMAGE_FACTORY_DECISION_FILE:-/tmp/e2e-talos-image-factory-url}"
TALOS_IMAGE_CACHE_MANIFEST="${TALOS_IMAGE_CACHE_MANIFEST:-hack/e2e-talos-image-cache.yaml}"
# Tenant namespace the kubernetes-* CRs are created in (run-kubernetes.sh and the
# oidc bats all target tenant-test); the reachability probe runs here so it inherits
# that namespace's default-deny egress, exactly as a worker importer does.
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

# resolve_talos_image_factory_url: print the imageFactoryURL to use, or an empty
# string to signal "use the chart default". The decision is resolved once (with a
# bounded wait for the mirror to finish seeding) and cached in a /tmp file that
# persists across bats files (they all exec in the same sandbox container), so
# only the first tenant Kubernetes test pays the readiness wait.
resolve_talos_image_factory_url() {
  if [ -f "$_TALOS_IMAGE_FACTORY_DECISION_FILE" ]; then
    cat "$_TALOS_IMAGE_FACTORY_DECISION_FILE"
    return 0
  fi
  local url=""
  if kubectl -n kube-system get deploy talos-image-cache >/dev/null 2>&1; then
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

# talos_image_factory_spec_block: emit a two-line YAML block
#   talos:
#     imageFactoryURL: <url>
# indented for insertion directly under a tenant Kubernetes CR `spec:`, or
# nothing when the chart default should apply. Ends with a trailing newline when
# non-empty so it can prefix the next `spec` key in a heredoc.
talos_image_factory_spec_block() {
  local url
  url=$(resolve_talos_image_factory_url)
  if [ -n "$url" ]; then
    printf '  talos:\n    imageFactoryURL: %s\n' "$url"
  fi
}
