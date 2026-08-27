#!/usr/bin/env bats
# Unit coverage for lane-specific LINSTOR pool registration and
# StorageClasses in hack/e2e-post-install-prep.sh. The library guard keeps this
# suite cluster-free when it runs through hack/cozytest.sh.

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

@test "invalid DRBD mode is rejected before cluster access" {
  COZY_LINSTOR_DRBD_ENABLED=unsupported
  if validate_linstor_storage_mode; then
    echo "invalid DRBD mode unexpectedly passed validation" >&2
    return 1
  fi
}
