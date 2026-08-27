#!/usr/bin/env bats
# Unit coverage for the lane-specific E2E storage contract: LINSTOR pool
# registration, management StorageClasses, and downstream Chainsaw fixtures.
# The library guard keeps this suite cluster-free under hack/cozytest.sh.

HACK_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")" && pwd)"
POST_PREP="$HACK_DIR/e2e-post-install-prep.sh"
# shellcheck source=/dev/null
E2E_POST_INSTALL_PREP_LIB=true . "$POST_PREP"

kubectl() { printf '%s\n' "$*"; }

@test "QEMU mode creates data from the satellite block device" {
  unset COZY_LINSTOR_DRBD_ENABLED
  command=$(create_linstor_storage_pool srv2)
  expected='exec -n cozy-linstor deploy/linstor-controller -- linstor physical-storage create-device-pool zfs srv2 /dev/vdc --pool-name data --storage-pool data'

  if [ "$command" != "$expected" ]; then
    echo "unexpected QEMU pool command: $command" >&2
    return 1
  fi
}

@test "container mode registers the node-specific pre-created zpool" {
  COZY_LINSTOR_DRBD_ENABLED=false
  command=$(create_linstor_storage_pool srv2)
  expected='exec -n cozy-linstor deploy/linstor-controller -- linstor storage-pool create zfs srv2 data data-srv2'

  if [ "$command" != "$expected" ]; then
    echo "unexpected container pool command: $command" >&2
    return 1
  fi
}

@test "QEMU mode renders local and replicated StorageClasses" {
  unset COZY_LINSTOR_DRBD_ENABLED
  manifest=$(render_linstor_storageclasses)
  names=$(printf '%s\n' "$manifest" | yq -N '.metadata.name')
  expected_names=$(printf '%s\n' local replicated)
  replicated_layers=$(printf '%s\n' "$manifest" | yq 'select(.metadata.name == "replicated") | .parameters."linstor.csi.linbit.com/layerList"')

  if [ "$names" != "$expected_names" ]; then
    echo "unexpected QEMU StorageClasses: $names" >&2
    return 1
  fi
  if [ "$replicated_layers" != "drbd storage" ]; then
    echo "replicated StorageClass lost its DRBD layer: $replicated_layers" >&2
    return 1
  fi
}

@test "container mode renders only the production local StorageClass" {
  COZY_LINSTOR_DRBD_ENABLED=false
  manifest=$(render_linstor_storageclasses)
  names=$(printf '%s\n' "$manifest" | yq -N '.metadata.name')
  local_layers=$(printf '%s\n' "$manifest" | yq '.parameters."linstor.csi.linbit.com/layerList"')
  remote=$(printf '%s\n' "$manifest" | yq '.parameters."linstor.csi.linbit.com/allowRemoteVolumeAccess"')

  if [ "$names" != "local" ]; then
    echo "container mode emitted unexpected StorageClasses: $names" >&2
    return 1
  fi
  if [ "$local_layers" != "storage" ] || [ "$remote" != "false" ]; then
    echo "container local StorageClass has unexpected parameters: layerList=$local_layers allowRemoteVolumeAccess=$remote" >&2
    return 1
  fi
}

@test "container mode pins CDI local StorageProfile to RWO Block" {
  command=$(patch_local_cdi_storage_profile)
  expected='patch storageprofile local --type merge -p {"spec":{"claimPropertySets":[{"accessModes":["ReadWriteOnce"],"volumeMode":"Block"}]}}'

  if [ "$command" != "$expected" ]; then
    echo "unexpected CDI StorageProfile patch: $command" >&2
    return 1
  fi
  if ! grep -Fq 'timeout 600 sh -ec '\''until kubectl get storageprofile local' "$POST_PREP"; then
    echo "container prep does not wait for CDI to create StorageProfile/local" >&2
    return 1
  fi
}

@test "invalid DRBD mode is rejected before cluster access" {
  COZY_LINSTOR_DRBD_ENABLED=unsupported
  if validate_linstor_storage_mode; then
    echo "invalid DRBD mode unexpectedly passed validation" >&2
    return 1
  fi
}

@test "tenant Kubernetes storage defaults to replicated and accepts local only explicitly" {
  # Sourcing inside the test keeps run-kubernetes.sh's live-cluster
  # cozy_cleanup helper out of cozytest.sh's file-level cleanup discovery.
  # shellcheck source=/dev/null
  . "$HACK_DIR/e2e-chainsaw/_lib/run-kubernetes.sh"
  unset COZY_E2E_STORAGE_CLASS
  default_class=$(cozy_e2e_storage_class)
  COZY_E2E_STORAGE_CLASS=local
  container_class=$(cozy_e2e_storage_class)

  if [ "$default_class" != replicated ] || [ "$container_class" != local ]; then
    echo "unexpected tenant storage classes: default=$default_class container=$container_class" >&2
    return 1
  fi

  COZY_E2E_STORAGE_CLASS=unsupported
  if cozy_e2e_storage_class; then
    echo "invalid tenant storage class unexpectedly passed validation" >&2
    return 1
  fi
}

@test "container workflow forwards local storage through install and Chainsaw" {
  install_value=$(yq '.jobs.e2e-container.steps[] | select(.name == "Install Cozystack into sandbox") | .env.COZY_E2E_STORAGE_CLASS' .github/workflows/pull-requests.yaml)
  workflow_value=$(yq '.jobs.e2e-container.steps[] | select(.name == "Run E2E tests") | .env.COZY_E2E_STORAGE_CLASS' .github/workflows/pull-requests.yaml)
  qemu_value=$(yq '.jobs.e2e.steps[] | select(.name == "Run E2E tests") | .env.COZY_E2E_STORAGE_CLASS // ""' .github/workflows/pull-requests.yaml)
  install_command=$(make -n -C packages/core/testing SANDBOX_NAME=test COZY_E2E_STORAGE_CLASS=local install-cozystack)
  make_command=$(make -n -C packages/core/testing SANDBOX_NAME=test COZY_E2E_STORAGE_CLASS=local CHAINSAW_SUITES=vminstance test-chainsaw)

  if [ "$install_value" != local ] || [ "$workflow_value" != local ] || [ -n "$qemu_value" ]; then
    echo "unexpected workflow storage modes: install=$install_value chainsaw=$workflow_value qemu=${qemu_value:-<default>}" >&2
    return 1
  fi
  if ! printf '%s\n' "$install_command" | grep -Fq -- '-e COZY_E2E_STORAGE_CLASS="local"'; then
    echo "testing Makefile does not forward COZY_E2E_STORAGE_CLASS into installation" >&2
    return 1
  fi
  if ! printf '%s\n' "$make_command" | grep -Fq -- '-e COZY_E2E_STORAGE_CLASS="local"'; then
    echo "testing Makefile does not forward COZY_E2E_STORAGE_CLASS into the sandbox" >&2
    return 1
  fi
  if ! printf '%s\n' "$make_command" | grep -Fq -- '--set-string storageClass="${COZY_E2E_STORAGE_CLASS:-replicated}" vminstance'; then
    echo "testing Makefile does not pass the lane storage class to Chainsaw values" >&2
    return 1
  fi
}

@test "container install supplies VictoriaLogs local storage before monitoring exists" {
  install_suite=hack/e2e-install-cozystack.bats

  if ! grep -Fq 'storage_class="${COZY_E2E_STORAGE_CLASS:-replicated}"' "$install_suite"; then
    echo "install suite does not retain the replicated QEMU default" >&2
    return 1
  fi
  if ! grep -Fq 'kubectl patch secret cozystack-values -n tenant-root --type merge --patch-file "$root_values_patch_file"' "$install_suite"; then
    echo "install suite does not put the monitoring override in root values" >&2
    return 1
  fi
  if grep -Fq 'kubectl patch hr/monitoring' "$install_suite"; then
    echo "install suite still races monitoring reconciliation with a HelmRelease patch" >&2
    return 1
  fi
  if ! grep -Fq '.logsStorages = [{"name":"generic","retentionPeriod":"1","storage":"10Gi","storageClassName":strenv(COZY_E2E_STORAGE_CLASS)}]' "$install_suite"; then
    echo "monitoring override does not preserve the required VictoriaLogs values" >&2
    return 1
  fi

  secret_patch_line=$(grep -nF 'kubectl patch secret cozystack-values' "$install_suite" | cut -d: -f1)
  tenant_patch_line=$(grep -nF 'kubectl patch tenants/root' "$install_suite" | cut -d: -f1)
  if [ -z "$secret_patch_line" ] || [ -z "$tenant_patch_line" ] || [ "$secret_patch_line" -ge "$tenant_patch_line" ]; then
    echo "root values must be patched before monitoring is enabled: secret=$secret_patch_line tenant=$tenant_patch_line" >&2
    return 1
  fi

  rendered=$(mktemp)
  helm template monitoring packages/extra/monitoring --namespace tenant-root \
    --set-string 'logsStorages[0].name=generic' \
    --set-string 'logsStorages[0].retentionPeriod=1' \
    --set-string 'logsStorages[0].storage=10Gi' \
    --set-string 'logsStorages[0].storageClassName=local' > "$rendered"
  rendered_class=$(yq 'select(.kind == "HelmRelease" and .metadata.name == "monitoring-system") | .spec.values.logsStorages[0].storageClassName' "$rendered")
  rm -f "$rendered"
  if [ "$rendered_class" != local ]; then
    echo "monitoring chart did not carry the root values override into its child release: $rendered_class" >&2
    return 1
  fi
}

@test "DRBD-dependent fixtures and Kubernetes checks consume the lane storage mode" {
  for manifest in hack/e2e-chainsaw/vminstance/vmdisk.yaml hack/e2e-chainsaw/vminstance/vmdisk-vmi.yaml; do
    storage_class=$(yq '.spec.storageClass' "$manifest")
    if [ "$storage_class" != '($values.storageClass)' ]; then
      echo "$manifest does not consume the Chainsaw storageClass value: $storage_class" >&2
      return 1
    fi
  done

  kubernetes_script=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  rendered_uses=$(grep -Fc 'storageClass: "${storage_class}"' "$kubernetes_script")
  if [ "$rendered_uses" -ne 2 ]; then
    echo "expected both tenant Kubernetes resources to consume storage_class, found $rendered_uses" >&2
    return 1
  fi
  if ! grep -Fq 'if [ "$storage_class" = local ]; then' "$kubernetes_script"; then
    echo "local-only Kubernetes suites do not omit DRBD/RWX assertions" >&2
    return 1
  fi
  if ! grep -Fq 'if [ "$storage_class" = replicated ]; then' "$kubernetes_script"; then
    echo "fallback regression does not limit replicated StorageClass restoration to QEMU" >&2
    return 1
  fi
}
