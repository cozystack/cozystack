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
  expected_names=$(printf '%s\n' cozystack.cozystack-platform cozystack.linstor cozystack.kubevirt-cdi)
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
  if [ "$disabled" != "cozystack.linstor,cozystack.kubevirt-cdi" ]; then
    echo "platform-managed LINSTOR was not disabled: $disabled" >&2
    return 1
  fi
  if [ "$drbd" != "false" ]; then
    echo "replacement LINSTOR package did not disable DRBD: $drbd" >&2
    return 1
  fi
}

@test "container mode raises the CDI importer memory ceiling" {
  manifest=$(COZY_APISERVER_ENDPOINT=https://192.168.123.11:6443 COZY_LINSTOR_DRBD_ENABLED=false hack/e2e-platform-packages.sh)
  disabled=$(printf '%s\n' "$manifest" | yq 'select(.metadata.name == "cozystack.cozystack-platform") | .spec.components.platform.values.bundles.disabledPackages | join(",")')
  mem=$(printf '%s\n' "$manifest" | yq 'select(.metadata.name == "cozystack.kubevirt-cdi") | .spec.components.kubevirt-cdi.values.importerResources.limits.memory')
  cpu=$(printf '%s\n' "$manifest" | yq 'select(.metadata.name == "cozystack.kubevirt-cdi") | .spec.components.kubevirt-cdi.values.importerResources.limits.cpu')

  if [ "$disabled" != "cozystack.linstor,cozystack.kubevirt-cdi" ]; then
    echo "container mode did not replace both packages: $disabled" >&2
    return 1
  fi
  if [ "$mem" != "4Gi" ]; then
    echo "replacement CDI package did not raise the memory ceiling: $mem" >&2
    return 1
  fi
  # The CPU ceiling deliberately stays at CDI's own default: the failure this
  # override answers was memory, and widening CPU too would be an unmeasured
  # second change riding along.
  if [ "$cpu" != "750m" ]; then
    echo "replacement CDI package moved the CPU ceiling off CDI's default: $cpu" >&2
    return 1
  fi
}

@test "the CDI chart default does not move production behaviour" {
  # The override above is a lane concern. The chart itself must restate CDI's
  # built-in requirements, so an install that sets nothing behaves as it did
  # before importerResources existed.
  defaults=$(helm template packages/system/kubevirt-cdi --set _cluster.root-host=example.org \
    | yq -N 'select(.kind == "CDI") | .spec.config.podResourceRequirements | [.limits.cpu, .limits.memory, .requests.cpu, .requests.memory] | join(" ")' | grep .)

  if [ "$defaults" != "750m 600M 100m 60M" ]; then
    echo "kubevirt-cdi default podResourceRequirements drifted from CDI's own: $defaults" >&2
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

@test "both gating workflows forward the E2E-only override into the sandbox" {
  workflow_value=$(yq '.jobs.e2e.steps[] | select(.name == "Install Cozystack into sandbox") | .env.COZY_LINSTOR_DRBD_ENABLED' .github/workflows/pull-requests.yaml)
  fork_value=$(yq '.jobs.e2e.steps[] | select(.name == "Install Cozystack into sandbox") | .env.COZY_LINSTOR_DRBD_ENABLED' .github/workflows/e2e-fork.yaml)
  make_command=$(make -n -C packages/core/testing SANDBOX_NAME=test COZY_LINSTOR_DRBD_ENABLED=false install-cozystack)

  if [ "$workflow_value" != "false" ]; then
    echo "container workflow does not set COZY_LINSTOR_DRBD_ENABLED=false" >&2
    return 1
  fi
  if [ "$fork_value" != "false" ]; then
    echo "fork workflow does not set COZY_LINSTOR_DRBD_ENABLED=false" >&2
    return 1
  fi
  if ! printf '%s\n' "$make_command" | grep -Fq -- '-e COZY_LINSTOR_DRBD_ENABLED="false"'; then
    echo "testing Makefile does not forward COZY_LINSTOR_DRBD_ENABLED" >&2
    return 1
  fi
}
