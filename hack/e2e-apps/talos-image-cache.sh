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
      echo "talos-image-cache mirror ready — tenant workers import from ${url}" >&2
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
