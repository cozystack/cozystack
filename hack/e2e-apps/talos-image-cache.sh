# Shared helper: choose the Talos Image Factory base URL for tenant Kubernetes
# e2e CRs.
#
# Prefer the in-sandbox caching mirror (hack/e2e-talos-image-cache.yaml, deployed
# by hack/e2e-install-cozystack.bats) when it is Available, otherwise fall back to
# the chart default (the public factory.talos.dev) by emitting nothing. The
# mirror rides out the public factory's flaky/range-less egress that otherwise
# stalls worker DataVolume imports past the 12-minute node-join deadline — see
# cozystack/cozystack#3231. Falling back to the default means the mirror can only
# help, never make CI worse.

TALOS_IMAGE_CACHE_SVC_URL="${TALOS_IMAGE_CACHE_SVC_URL:-http://talos-image-cache.kube-system.svc}"
_TALOS_IMAGE_FACTORY_DECISION_FILE="${_TALOS_IMAGE_FACTORY_DECISION_FILE:-/tmp/e2e-talos-image-factory-url}"

# _talos_image_cache_serves_ranges: succeed iff the mirror answers a byte-range
# request with 206 Partial Content. Runs curl inside the serve pod against
# localhost, range-probing the seeded image file it finds under /data, so the
# check works without the sandbox being able to reach the ClusterIP.
_talos_image_cache_serves_ranges() {
  local pod code
  pod=$(kubectl -n kube-system get pod \
    -l app.kubernetes.io/name=talos-image-cache \
    --field-selector=status.phase=Running \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null) || return 1
  [ -n "$pod" ] || return 1
  code=$(kubectl -n kube-system exec "$pod" -c serve -- sh -ec '
    f=$(find /data -name "*.raw.xz" 2>/dev/null | head -n1)
    [ -n "$f" ] || exit 1
    curl -s -o /dev/null -w "%{http_code}" -r 0-0 "http://127.0.0.1:8080/${f#/data/}"
  ' 2>/dev/null) || return 1
  [ "$code" = "206" ]
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
    # this is the signal that the mirror is warm and safe to point tenants at.
    if kubectl -n kube-system rollout status deploy/talos-image-cache --timeout=12m >/dev/null 2>&1; then
      url="$TALOS_IMAGE_CACHE_SVC_URL"
      # Available only proves the seed finished and the port is open. Verify the
      # server actually answers a byte-range request with 206 (not a range-less
      # 200): range support is what lets CDI skip the slow copy-to-scratch path,
      # and a future swap to a range-incapable server would otherwise regress it
      # silently. Probe from inside the serve pod so this does not depend on the
      # sandbox reaching a ClusterIP. A non-206 still imports correctly (just
      # without the fast path), so warn loudly rather than discard the mirror and
      # reintroduce the public factory's egress flakiness.
      if _talos_image_cache_serves_ranges; then
        echo "talos-image-cache mirror ready (byte-range verified) — tenant workers import from ${url}" >&2
      else
        echo "WARNING: talos-image-cache is Available but did not answer a range request with 206 — tenant workers still import from ${url}, but CDI will fall back to a full copy" >&2
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
