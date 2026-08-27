#!/usr/bin/env bats
# Unit coverage for the lane-specific Package manifest emitted by
# hack/e2e-platform-packages.sh. This suite runs through hack/cozytest.sh, so it
# uses direct shell assertions rather than bats' run/status helpers.

@test "QEMU defaults keep the platform-managed LINSTOR package" {
  manifest=$(hack/e2e-platform-packages.sh)
  names=$(printf '%s\n' "$manifest" | yq -N '.metadata.name')
  endpoint=$(printf '%s\n' "$manifest" | yq '.spec.components.platform.values.publishing.apiServerEndpoint')

  if [ "$names" != "cozystack.cozystack-platform" ]; then
    echo "default manifest unexpectedly replaces a platform package: $names" >&2
    return 1
  fi
  if [ "$endpoint" != "https://192.168.123.10:6443" ]; then
    echo "unexpected default API endpoint: $endpoint" >&2
    return 1
  fi
}

@test "container mode replaces LINSTOR with DRBD disabled" {
  manifest=$(COZY_APISERVER_ENDPOINT=https://192.168.123.11:6443 COZY_LINSTOR_DRBD_ENABLED=false hack/e2e-platform-packages.sh)
  names=$(printf '%s\n' "$manifest" | yq -N '.metadata.name')
  expected_names=$(printf '%s\n' cozystack.cozystack-platform cozystack.linstor)
  endpoint=$(printf '%s\n' "$manifest" | yq 'select(.metadata.name == "cozystack.cozystack-platform") | .spec.components.platform.values.publishing.apiServerEndpoint')
  disabled=$(printf '%s\n' "$manifest" | yq 'select(.metadata.name == "cozystack.cozystack-platform") | .spec.components.platform.values.bundles.disabledPackages | join(",")')
  drbd=$(printf '%s\n' "$manifest" | yq 'select(.metadata.name == "cozystack.linstor") | .spec.components.linstor.values.drbd.enabled')

  if [ "$names" != "$expected_names" ]; then
    echo "container manifest has unexpected packages: $names" >&2
    return 1
  fi
  if [ "$endpoint" != "https://192.168.123.11:6443" ]; then
    echo "container API endpoint was not forwarded: $endpoint" >&2
    return 1
  fi
  if [ "$disabled" != "cozystack.linstor" ]; then
    echo "platform-managed LINSTOR was not disabled: $disabled" >&2
    return 1
  fi
  if [ "$drbd" != "false" ]; then
    echo "replacement LINSTOR package did not disable DRBD: $drbd" >&2
    return 1
  fi
}

@test "invalid DRBD mode fails before emitting YAML" {
  if manifest=$(COZY_LINSTOR_DRBD_ENABLED=unsupported hack/e2e-platform-packages.sh); then
    echo "invalid DRBD mode unexpectedly succeeded" >&2
    return 1
  fi
  if [ -n "$manifest" ]; then
    echo "invalid DRBD mode emitted a partial manifest" >&2
    return 1
  fi
}

@test "container workflow forwards the E2E-only override into the sandbox" {
  workflow_value=$(yq '.jobs.e2e-container.steps[] | select(.name == "Install Cozystack into sandbox") | .env.COZY_LINSTOR_DRBD_ENABLED' .github/workflows/pull-requests.yaml)
  make_command=$(make -n -C packages/core/testing SANDBOX_NAME=test COZY_LINSTOR_DRBD_ENABLED=false install-cozystack)

  if [ "$workflow_value" != "false" ]; then
    echo "container workflow does not set COZY_LINSTOR_DRBD_ENABLED=false" >&2
    return 1
  fi
  if ! printf '%s\n' "$make_command" | grep -Fq -- '-e COZY_LINSTOR_DRBD_ENABLED="false"'; then
    echo "testing Makefile does not forward COZY_LINSTOR_DRBD_ENABLED" >&2
    return 1
  fi
}
