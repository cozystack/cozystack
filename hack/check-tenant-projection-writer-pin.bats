#!/usr/bin/env bats
# Unit test: the cozystack-tenant-projection-writer-policy VAP pins the writer it
# allows as a literal ServiceAccount username, and this checks that literal is
# well-formed and names the flux namespace the platform installs helm-controller
# into.
#
# The pin is the whole policy. A TenantProjection is a tenant trust-anchor
# DECLARATION, and the policy's only rule is that nobody but helm-controller may
# write one. Get the identity wrong and BOTH failure modes are silent:
#
#   - too narrow / stale: helm-controller's own render is denied, and NO CA is
#     ever published for any application — the whole feature is dead;
#   - no longer matching the real writer: the policy allows nobody it must and
#     forbids nobody it needs to, so the hole it exists to close is quietly open.
#
# cozystack runs all its flux controllers, helm-controller included, under one
# shared ServiceAccount "flux" in cozy-fluxcd (the flux-aio Deployment sets it and
# the flux-shard-operator inherits it — pinned by its provisioner test). The flux
# identity is generated at RUNTIME, not rendered by any chart this repo can
# `helm template`, so it cannot be cross-checked statically the way a chart-shipped
# ServiceAccount can. What this test pins: the literal is a well-formed
# ServiceAccount username, its namespace is the flux namespace the platform installs
# its controllers into, and its name is "flux" — so a stray flip back to a
# per-controller name reddens CI.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
POLICY="$REPO_ROOT/packages/system/cozystack-basics/templates/tenant-projection-writer-policy.yaml"
SHARD_SOURCE="$REPO_ROOT/packages/core/platform/sources/flux-shard-operator.yaml"

# pinned_writer returns the literal the policy template allows.
pinned_writer() {
  grep -oE '\$fluxWriter := "[^"]+"' "$POLICY" | head -1 | sed -E 's/.*"([^"]+)"/\1/'
}

# pinned_namespace extracts the namespace from a system:serviceaccount:<ns>:<sa>
# username.
pinned_namespace() {
  pinned_writer | sed -E 's/^system:serviceaccount:([^:]+):.*/\1/'
}

# flux_namespace returns the namespace the platform installs the flux
# controllers (helm-controller shards) into.
flux_namespace() {
  yq eval '.spec.variants[] | select(.name == "default") | .components[] | select(.name == "flux-shard-operator") | .install.namespace' "$SHARD_SOURCE"
}

@test "the policy pins a writer at all" {
  local pin
  pin="$(pinned_writer)"
  if [ -z "$pin" ]; then
    echo "No \$fluxWriter literal found in $POLICY." >&2
    echo "If the pin moved or was renamed, update this test with it — do not delete the check." >&2
    exit 1
  fi
  case "$pin" in
    system:serviceaccount:*:*) ;;
    *)
      echo "The pinned writer is not a ServiceAccount username: $pin" >&2
      echo "Expected 'system:serviceaccount:<namespace>:<name>'." >&2
      exit 1
      ;;
  esac
}

@test "the pinned writer is the shared flux ServiceAccount" {
  local sa
  sa="$(pinned_writer | sed -E 's/^system:serviceaccount:[^:]+:(.*)$/\1/')"
  # cozystack runs helm-controller under the shared "flux" ServiceAccount, not a
  # per-controller one. Pin the name so a flip back to "helm-controller" (which
  # would deny every sentinel write) reddens here.
  if [ "$sa" != "flux" ]; then
    echo "The pinned writer names ServiceAccount '$sa', want 'flux'." >&2
    echo "helm-controller runs under the shared cozy-fluxcd:flux identity; a" >&2
    echo "per-controller name would deny every TenantProjection write." >&2
    exit 1
  fi
}

@test "the pin names the flux namespace the platform installs helm-controller into" {
  local ns fluxns
  ns="$(pinned_namespace)"
  fluxns="$(flux_namespace)"

  if [ -z "$fluxns" ] || [ "$fluxns" = "null" ]; then
    echo "Could not read the flux namespace from $SHARD_SOURCE." >&2
    exit 1
  fi

  if [ "$ns" != "$fluxns" ]; then
    echo "The tenant-projection writer policy allows a writer in namespace: $ns" >&2
    echo "But the platform installs the flux controllers into namespace: $fluxns" >&2
    echo "" >&2
    echo "The policy would allow nobody, and every TenantProjection write would be" >&2
    echo "denied — no CA would ever be published. Update the pin in:" >&2
    echo "  $POLICY" >&2
    exit 1
  fi
}
