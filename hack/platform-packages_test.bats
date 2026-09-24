#!/usr/bin/env bats
# Unit coverage for the Package manifest emitted by hack/e2e-platform-packages.sh.
# This suite runs through hack/cozytest.sh, so it uses direct shell assertions
# rather than bats' run/status helpers.

@test "QEMU defaults keep the platform package and its VIP endpoint" {
  manifest=$(hack/e2e-platform-packages.sh)
  names=$(printf '%s\n' "$manifest" | yq -N '.metadata.name')
  endpoint=$(printf '%s\n' "$manifest" | yq '.spec.components.platform.values.publishing.apiServerEndpoint')

  if [ "$names" != "cozystack.cozystack-platform" ]; then
    echo "default manifest unexpectedly carries other packages: $names" >&2
    return 1
  fi
  if [ "$endpoint" != "https://192.168.123.10:6443" ]; then
    echo "unexpected default API endpoint: $endpoint" >&2
    return 1
  fi
}

@test "container mode forwards its endpoint and replaces no package" {
  manifest=$(COZY_APISERVER_ENDPOINT=https://192.168.123.11:6443 COZY_LINSTOR_DRBD_ENABLED=false hack/e2e-platform-packages.sh)
  names=$(printf '%s\n' "$manifest" | yq -N '.metadata.name')
  endpoint=$(printf '%s\n' "$manifest" | yq '.spec.components.platform.values.publishing.apiServerEndpoint')
  disabled=$(printf '%s\n' "$manifest" | yq '.spec.components.platform.values.bundles.disabledPackages')

  if [ "$names" != "cozystack.cozystack-platform" ]; then
    echo "container manifest has unexpected packages: $names" >&2
    return 1
  fi
  if [ "$endpoint" != "https://192.168.123.11:6443" ]; then
    echo "container API endpoint was not forwarded: $endpoint" >&2
    return 1
  fi
  if [ "$disabled" != "null" ]; then
    echo "container manifest disables platform packages: $disabled" >&2
    return 1
  fi
}

@test "the gating workflow forwards the container-lane settings into the sandbox" {
  endpoint_value=$(yq '.jobs.e2e.steps[] | select(.name == "Install Cozystack into sandbox") | .env.COZY_APISERVER_ENDPOINT' .github/workflows/pull-requests.yaml)
  drbd_value=$(yq '.jobs.e2e.steps[] | select(.name == "Install Cozystack into sandbox") | .env.COZY_LINSTOR_DRBD_ENABLED' .github/workflows/pull-requests.yaml)
  make_command=$(make -n -C packages/core/testing SANDBOX_NAME=test COZY_APISERVER_ENDPOINT=https://192.168.123.11:6443 COZY_LINSTOR_DRBD_ENABLED=false install-cozystack)

  if [ "$endpoint_value" != "https://192.168.123.11:6443" ]; then
    echo "container workflow does not set the node API endpoint: $endpoint_value" >&2
    return 1
  fi
  if [ "$drbd_value" != "false" ]; then
    echo "container workflow does not set COZY_LINSTOR_DRBD_ENABLED=false" >&2
    return 1
  fi
  if ! printf '%s\n' "$make_command" | grep -Fq -- '-e COZY_APISERVER_ENDPOINT="https://192.168.123.11:6443"'; then
    echo "testing Makefile does not forward COZY_APISERVER_ENDPOINT" >&2
    return 1
  fi
  if ! printf '%s\n' "$make_command" | grep -Fq -- '-e COZY_LINSTOR_DRBD_ENABLED="false"'; then
    echo "testing Makefile does not forward COZY_LINSTOR_DRBD_ENABLED" >&2
    return 1
  fi
}
