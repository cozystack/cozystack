#!/usr/bin/env bats
# Unit coverage for the lane-specific Package manifest emitted by
# hack/e2e-platform-packages.sh. This suite runs through hack/cozytest.sh, so it
# uses direct shell assertions rather than bats' run/status helpers.

load test_helper

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

@test "every e2e lane forwards the container-lane overrides into the sandbox" {
  make_command=$(make -n -C packages/core/testing SANDBOX_NAME=test COZY_LINSTOR_DRBD_ENABLED=false install-cozystack)
  if ! printf '%s\n' "$make_command" | grep -Fq -- '-e COZY_LINSTOR_DRBD_ENABLED="false"'; then
    echo "testing Makefile does not forward COZY_LINSTOR_DRBD_ENABLED" >&2
    return 1
  fi

  # Install and run must agree on the class: a mismatch fails deep inside a suite.
  for wf in .github/workflows/pull-requests.yaml .github/workflows/e2e-fork.yaml .github/workflows/nightly.yaml .github/workflows/e2e-tag.yaml; do
    install='.jobs.e2e.steps[] | select(.name == "Install Cozystack into sandbox")'
    tests='.jobs.e2e.steps[] | select(.name | test("^Run E2E tests"))'
    if [ "$(yq "$install | .env.COZY_LINSTOR_DRBD_ENABLED" "$wf")" != "false" ]; then
      echo "$wf: install does not set COZY_LINSTOR_DRBD_ENABLED=false" >&2
      return 1
    fi
    if [ "$(yq "$install | .env.COZY_E2E_STORAGE_CLASS" "$wf")" != "local" ]; then
      echo "$wf: install does not set COZY_E2E_STORAGE_CLASS=local" >&2
      return 1
    fi
    if [ "$(yq "$tests | .env.COZY_E2E_STORAGE_CLASS" "$wf")" != "local" ]; then
      echo "$wf: the suite run does not set COZY_E2E_STORAGE_CLASS=local" >&2
      return 1
    fi
  done
}

@test "a selector list is emitted under one disabledPackages key, merged with the container pair" {
  manifest=$(COZY_LINSTOR_DRBD_ENABLED=false COZY_DISABLED_PACKAGES="cozystack.goldpinger cozystack.linstor cozystack.keda" hack/e2e-platform-packages.sh)
  disabled=$(printf '%s\n' "$manifest" | yq 'select(.metadata.name == "cozystack.cozystack-platform") | .spec.components.platform.values.bundles.disabledPackages | join(",")')

  if [ "$disabled" != "cozystack.linstor,cozystack.kubevirt-cdi,cozystack.goldpinger,cozystack.keda" ]; then
    echo "selector list was not merged once with the container pair: $disabled" >&2
    return 1
  fi
}

@test "a selector list alone leaves the platform-managed packages in place" {
  manifest=$(COZY_DISABLED_PACKAGES="cozystack.goldpinger cozystack.keda" hack/e2e-platform-packages.sh)
  names=$(printf '%s\n' "$manifest" | yq -N '.metadata.name')
  disabled=$(printf '%s\n' "$manifest" | yq '.spec.components.platform.values.bundles.disabledPackages | join(",")')

  if [ "$names" != "cozystack.cozystack-platform" ]; then
    echo "a selector list unexpectedly replaced a platform package: $names" >&2
    return 1
  fi
  if [ "$disabled" != "cozystack.goldpinger,cozystack.keda" ]; then
    echo "selector list was not emitted as given: $disabled" >&2
    return 1
  fi
}

@test "no selector list emits no disabledPackages key" {
  manifest=$(hack/e2e-platform-packages.sh)
  key=$(printf '%s\n' "$manifest" | yq '.spec.components.platform.values.bundles | has("disabledPackages")')

  if [ "$key" != "false" ]; then
    echo "default manifest carries disabledPackages: $key" >&2
    return 1
  fi
}

@test "root monitoring redirects stay off, including for a narrow suite selection" {
  for disabled in "" "$(hack/select-install.sh --disabled vminstance)"; do
    manifest=$(COZY_DISABLED_PACKAGES="$disabled" hack/e2e-platform-packages.sh)
    root=$(printf '%s\n' "$manifest" | yq 'select(.metadata.name == "cozystack.cozystack-platform") | .spec.components.platform.values.monitoring.rootEnabled')

    if [ "$root" != "false" ]; then
      echo "monitoring.rootEnabled is '$root' with disabledPackages '$disabled', but Tenant/root monitoring is off" >&2
      return 1
    fi
  done
}

@test "a malformed package name fails before emitting YAML" {
  for bad in "goldpinger" "cozystack." "cozystack.Gold" "cozystack.a;b"; do
    if manifest=$(COZY_DISABLED_PACKAGES="cozystack.keda $bad" hack/e2e-platform-packages.sh 2>/dev/null); then
      echo "malformed package name '$bad' unexpectedly succeeded" >&2
      return 1
    fi
    if [ -n "$manifest" ]; then
      echo "malformed package name '$bad' emitted a partial manifest" >&2
      return 1
    fi
  done
}

@test "both gating workflows select before installing and forward the list" {
  make_command=$(make -n -C packages/core/testing SANDBOX_NAME=test COZY_DISABLED_PACKAGES="cozystack.keda" install-cozystack)
  if ! printf '%s\n' "$make_command" | grep -Fq -- '-e COZY_DISABLED_PACKAGES="cozystack.keda"'; then
    echo "testing Makefile does not forward COZY_DISABLED_PACKAGES" >&2
    return 1
  fi

  for wf in .github/workflows/pull-requests.yaml .github/workflows/e2e-fork.yaml; do
    select_at=$(yq '.jobs.e2e.steps | to_entries | .[] | select(.value.name == "Select E2E tests") | .key' "$wf")
    install_at=$(yq '.jobs.e2e.steps | to_entries | .[] | select(.value.name == "Install Cozystack into sandbox") | .key' "$wf")
    forwarded=$(yq '.jobs.e2e.steps[] | select(.name == "Install Cozystack into sandbox") | .env.COZY_DISABLED_PACKAGES' "$wf")

    if [ -z "$select_at" ] || [ -z "$install_at" ] || [ "$select_at" -ge "$install_at" ]; then
      echo "$wf: selection (step $select_at) does not precede the install (step $install_at)" >&2
      return 1
    fi
    if [ "$forwarded" != '${{ steps.select.outputs.disabled }}' ]; then
      echo "$wf: install does not take the selector list: $forwarded" >&2
      return 1
    fi
  done
}

@test "the proxmox-network package is enabled for its suite and dropped from every other run" {
  # It is opt-in in the iaas bundle, so the install has to ask for it; the
  # selector's complement then takes it out of any run that does not select
  # the proxmox-network suite, which is what keeps it off the other lanes.
  manifest=$(hack/e2e-platform-packages.sh)
  enabled=$(printf '%s\n' "$manifest" | yq '.spec.components.platform.values.bundles.enabledPackages | join(",")')
  case ",$enabled," in
    *,cozystack.proxmox-network,*) ;;
    *) echo "enabledPackages lacks cozystack.proxmox-network: $enabled" >&2; return 1 ;;
  esac

  other=$(hack/select-install.sh --disabled postgres)
  case " $other " in
    *" cozystack.proxmox-network "*) ;;
    *) echo "a postgres-only run keeps cozystack.proxmox-network installed" >&2; return 1 ;;
  esac
  own=$(hack/select-install.sh --disabled proxmox-network)
  for pkg in cozystack.proxmox-network cozystack.capi-provider-ipam-in-cluster; do
    case " $own " in
      *" $pkg "*) echo "the proxmox-network run disables $pkg, which it needs" >&2; return 1 ;;
    esac
  done
}
