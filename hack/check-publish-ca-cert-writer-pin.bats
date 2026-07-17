#!/usr/bin/env bats
# Unit test: the cozystack-publish-ca-cert-writer-policy VAP pins the writer it
# allows as a literal ServiceAccount username, and this checks that literal
# against the cert-manager package it is supposed to name.
#
# The pin is the whole policy. A Secret labelled internal.cozystack.io/publish-
# ca-cert is a tenant trust-anchor SOURCE, and the policy's only rule is that
# nobody but cert-manager may write one. Get the identity wrong and BOTH failure
# modes are silent:
#
#   - too narrow / stale: cert-manager's own issuance is denied, and the entire
#     label leg breaks the moment an engine converges onto it;
#   - no longer matching the real writer: the policy allows nobody and forbids
#     nobody it needs to, so the hole it exists to close is quietly open again.
#
# Neither shows up in the rendered YAML, and neither shows up in a helm-unittest
# of the policy chart — that can only assert the template contains the constant
# the template defines, which is a tautology. The identity lives in a DIFFERENT
# chart (packages/system/cert-manager) installed by a THIRD file
# (packages/core/platform/sources/cert-manager.yaml), so the drift is
# cross-chart by construction and no single chart's tests can see it.
#
# Deriving the pin at render time instead is not available: the PackageSource is
# emitted with Files.Get and no tpl, so its coordinates are literals rather than
# values, and cozystack-basics receives only _cluster values. That is why the
# literal stays — and why it needs this test, which reads all three files and
# insists they agree. Rename cert-manager's release, move its namespace, or
# change its ServiceAccount, and this goes red instead of the platform going
# quietly insecure.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
POLICY="$REPO_ROOT/packages/system/cozystack-basics/templates/publish-ca-cert-writer-policy.yaml"
SOURCE="$REPO_ROOT/packages/core/platform/sources/cert-manager.yaml"
CHART="$REPO_ROOT/packages/system/cert-manager"

# pinned_writer returns the literal the policy template allows.
pinned_writer() {
  grep -oE '\$certManagerWriter := "[^"]+"' "$POLICY" | head -1 | sed -E 's/.*"([^"]+)"/\1/'
}

# installed_namespace / installed_release return the coordinates the platform
# actually installs the cert-manager component with.
installed_namespace() {
  yq eval '.spec.variants[] | select(.name == "default") | .components[] | select(.name == "cert-manager") | .install.namespace' "$SOURCE"
}

installed_release() {
  yq eval '.spec.variants[] | select(.name == "default") | .components[] | select(.name == "cert-manager") | .install.releaseName' "$SOURCE"
}

# controller_sa renders the cert-manager chart the way the platform installs it
# and returns the controller Deployment's ServiceAccount. The controller is
# selected by app.kubernetes.io/component=controller rather than by name: the
# chart also ships cainjector and webhook, each with its own ServiceAccount, and
# only the controller writes Certificate Secrets.
controller_sa() {
  local ns="$1" release="$2"
  helm template "$release" "$CHART" --namespace "$ns" 2>/dev/null |
    yq eval-all 'select(.kind == "Deployment" and .metadata.labels."app.kubernetes.io/component" == "controller") | .spec.template.spec.serviceAccountName' - |
    grep -v '^---$' | grep -v '^null$' | head -1
}

@test "the policy pins a writer at all" {
  local pin
  pin="$(pinned_writer)"
  if [ -z "$pin" ]; then
    echo "No \$certManagerWriter literal found in $POLICY." >&2
    echo "If the pin moved or was renamed, update this test with it — do not delete the check." >&2
    exit 1
  fi
  case "$pin" in
    system:serviceaccount:*) ;;
    *)
      echo "The pinned writer is not a ServiceAccount username: $pin" >&2
      echo "Expected it to begin with 'system:serviceaccount:'." >&2
      exit 1
      ;;
  esac
}

@test "the platform installs cert-manager with the namespace and release the pin assumes" {
  local ns release pin
  ns="$(installed_namespace)"
  release="$(installed_release)"
  pin="$(pinned_writer)"

  if [ -z "$ns" ] || [ "$ns" = "null" ] || [ -z "$release" ] || [ "$release" = "null" ]; then
    echo "Could not read the cert-manager component's install coordinates from $SOURCE." >&2
    exit 1
  fi

  case "$pin" in
    "system:serviceaccount:${ns}:"*) ;;
    *)
      echo "The publish-ca-cert writer policy allows: $pin" >&2
      echo "But the platform installs cert-manager into namespace: $ns (release $release)" >&2
      echo "" >&2
      echo "The policy would allow nobody, and every Secret labelled" >&2
      echo "internal.cozystack.io/publish-ca-cert would be denied — silently breaking" >&2
      echo "the CA-extraction controller's label leg. Update the pin in:" >&2
      echo "  $POLICY" >&2
      exit 1
      ;;
  esac
}

@test "the pinned ServiceAccount is the one cert-manager's controller actually runs as" {
  local ns release pin sa want
  ns="$(installed_namespace)"
  release="$(installed_release)"
  pin="$(pinned_writer)"
  sa="$(controller_sa "$ns" "$release")"

  if [ -z "$sa" ] || [ "$sa" = "null" ]; then
    echo "Could not resolve the cert-manager controller ServiceAccount by rendering $CHART." >&2
    echo "If the chart's structure changed, fix this test — the pin it guards is a" >&2
    echo "silent-failure seam and must not be left unchecked." >&2
    exit 1
  fi

  want="system:serviceaccount:${ns}:${sa}"
  if [ "$pin" != "$want" ]; then
    echo "The publish-ca-cert writer policy allows: $pin" >&2
    echo "cert-manager's controller actually runs as:  $want" >&2
    echo "" >&2
    echo "A wrong identity here fails silently in both directions: cert-manager's own" >&2
    echo "issuance gets denied (the label leg breaks), or the real writer no longer" >&2
    echo "matches (nothing is enforced and any namespace writer can forge a tenant" >&2
    echo "trust anchor). Update the pin in:" >&2
    echo "  $POLICY" >&2
    exit 1
  fi
}
