#!/usr/bin/env bats
# Unit coverage for the lane-specific E2E storage contract: LINSTOR pool
# registration, management StorageClasses, and downstream Chainsaw fixtures.
# The library guard keeps this suite cluster-free under hack/cozytest.sh.

load test_helper

HACK_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")" && pwd)"
POST_PREP="$HACK_DIR/e2e-post-install-prep.sh"
# shellcheck source=/dev/null
E2E_POST_INSTALL_PREP_LIB=true . "$POST_PREP"

kubectl() { printf '%s\n' "$*"; }

# The pool waits poll against a wall-clock deadline. Each `date` call moves this
# clock a minute on, so a wait that cannot succeed runs out within a few polls.
# The suite also runs kubectl through `timeout` and `sh -c`, which bypass the
# shell function, so a kubectl that refuses to run goes first on PATH.
fake_linstor_clock() {
  clock_file="_out/tmp/linstor-clock-$$"
  mkdir -p _out/tmp/linstor-bin
  printf '#!/bin/sh\necho "unstubbed kubectl $*" >&2\nexit 97\n' >_out/tmp/linstor-bin/kubectl
  chmod +x _out/tmp/linstor-bin/kubectl
  PATH="$PWD/_out/tmp/linstor-bin:$PATH"
  printf '0\n' >"$clock_file"
  date() {
    _now=$(cat "$clock_file")
    printf '%s\n' "$((_now + 60))" >"$clock_file"
    printf '%s\n' "$_now"
  }
  sleep() { :; }
}

# Three container-mode nodes, each with a satellite pod and a node-specific
# zpool. `zfs list` answers from $pool_dir/datasets.<poll>, where every pool
# listing moves <poll> on by one and the last file present keeps answering,
# so a test can script what each cleanup poll sees. The free-capacity probe
# keeps answering a short figure, which is what LINSTOR's cache serves for a
# thick ZFS pool after a destroy, and what the dataset comparison must ignore.
fake_linstor_pools() {
  pool_dir="_out/tmp/linstor-pools-$$"
  mkdir -p "$pool_dir/bin"
  printf '0\n' >"$pool_dir/poll"
  # Each node has its ZFS pool and LINSTOR's DISKLESS placeholder, which
  # carries no zpool. $pool_dir/srv2-pool replaces srv2's ZFS pool object.
  cat >"$pool_dir/bin/linstor" <<EOF_LINSTOR
#!/bin/sh
srv2_pool='{"storage_pool_name":"data","node_name":"srv2","provider_kind":"ZFS","props":{"StorDriver/StorPoolName":"data-srv2"}}'
[ -e "$PWD/$pool_dir/srv2-pool" ] && srv2_pool=\$(cat "$PWD/$pool_dir/srv2-pool")
printf '[[%s,%s,%s,%s]]\n' \\
  '{"storage_pool_name":"data","node_name":"srv1","provider_kind":"ZFS","props":{"StorDriver/StorPoolName":"data-srv1"}}' \\
  '{"storage_pool_name":"DfltDisklessStorPool","node_name":"srv1","provider_kind":"DISKLESS","props":{}}' \\
  "\$srv2_pool" \\
  '{"storage_pool_name":"data","node_name":"srv3","provider_kind":"ZFS","props":{"StorDriver/StorPoolName":"data-srv3"}}'
EOF_LINSTOR
  chmod +x "$pool_dir/bin/linstor"
  kubectl() {
    # A read made without a ceiling is one a hung pod would block for good;
    # a stub cannot block, so it leaves this marker instead.
    [ -n "${COZY_TEST_BOUNDED:-}" ] || : >"$pool_dir/unbounded-read"
    case "$*" in
      *free_capacity*) printf '87127923:srv2\n' ;;
      *'sp l'*)
        # Run the controller-side script for real against a machine-readable
        # listing shaped like LINSTOR's, so the jq filter is what reads it.
        printf '%s\n' "$(( $(cat "$pool_dir/poll") + 1 ))" >"$pool_dir/poll"
        for _script; do :; done
        PATH="$pool_dir/bin:$PATH" sh -c "$_script"
        return
        ;;
      *'get pods'*)
        [ -e "$pool_dir/no-satellite-srv2" ] || printf 'srv2 linstor-satellite.srv2-b\n'
        printf 'srv1 linstor-satellite.srv1-a\nsrv3 linstor-satellite.srv3-c\n'
        ;;
      *'zfs list'*)
        # kubectl refuses an exec without a pod name, as the real one does.
        [ -n "$4" ] || return 1
        # A satellite that never answers ends the read as timeout(1) ends it.
        if [ -e "$pool_dir/hang-srv2" ] && [ "$4" = linstor-satellite.srv2-b ]; then
          return 124
        fi
        for _zpool; do :; done
        _poll=$(cat "$pool_dir/poll")
        while [ "$_poll" -gt 0 ] && [ ! -e "$pool_dir/datasets.$_poll" ]; do _poll=$((_poll - 1)); done
        awk -v pool="$_zpool" '$0 == pool || index($0, pool "/") == 1 { print; found = 1 } END { exit !found }' "$pool_dir/datasets.$_poll"
        return
        ;;
    esac
    return 0
  }
  # The probe's reads go through timeout(1), which would bypass the kubectl
  # function above; this runs the command as the shell function instead, and
  # only under the `-k <grace> <limit>` form.
  timeout() {
    [ "${1:-}" = -k ] && [ "${3:-0}" -gt 0 ] || return 97
    shift 3
    COZY_TEST_BOUNDED=1 "$@"
  }
}

# The datasets of an idle sandbox: each pool root, and a platform volume that
# predates the suite on srv2.
idle_pool_datasets() {
  printf '%s\n' data-srv1 data-srv2 data-srv2/pvc-0a1b2c3d-platform_00000 data-srv3
}

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
  expected='patch storageprofile local --request-timeout=60s --type merge -p {"spec":{"claimPropertySets":[{"accessModes":["ReadWriteOnce"],"volumeMode":"Block"}]}}'

  if [ "$command" != "$expected" ]; then
    echo "unexpected CDI StorageProfile patch: $command" >&2
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

@test "replicated cleanup holds LINSTOR pools to the pre-suite baseline, not to an absolute floor" {
  # shellcheck source=/dev/null
  . "$HACK_DIR/e2e-chainsaw/_lib/run-kubernetes.sh"
  COZY_E2E_STORAGE_CLASS=replicated
  COZY_LINSTOR_POOL_BASELINE_FILE="_out/tmp/linstor-baseline-$$"
  mkdir -p _out/tmp
  fake_linstor_pools
  # srv2 reads 83 GiB free before and after the suite, below the 90 GiB that
  # used to be demanded of every node, because earlier suites hold the space.
  idle_pool_datasets >"$pool_dir/datasets.0"
  idle_pool_datasets | sed 's/^data-\(srv[0-9]\)/\1 &/' >"$COZY_LINSTOR_POOL_BASELINE_FILE"
  cozy_wait_tenant_drained() { return 0; }
  fake_linstor_clock

  rc=0
  cozy_cleanup test-latest-version || rc=$?
  rm -rf "$COZY_LINSTOR_POOL_BASELINE_FILE" "$clock_file" "$pool_dir"

  if [ "$rc" -ne 0 ]; then
    echo "replicated cleanup failed although every pool holds only its pre-suite datasets" >&2
    return 1
  fi
}

@test "pool baseline wait passes once a destroyed worker volume is gone, whatever the cached free capacity says" {
  # The shape of a cleanup on a thick ZFS pool: the worker zvol is still
  # listed on the first poll, gone on the next, and LINSTOR's free capacity
  # stays short of the baseline because it was cached mid-destroy.
  # shellcheck source=/dev/null
  . "$HACK_DIR/e2e-chainsaw/_lib/run-kubernetes.sh"
  COZY_LINSTOR_POOL_BASELINE_FILE="_out/tmp/linstor-baseline-$$"
  mkdir -p _out/tmp
  fake_linstor_pools
  idle_pool_datasets | sed 's/^data-\(srv[0-9]\)/\1 &/' >"$COZY_LINSTOR_POOL_BASELINE_FILE"
  { idle_pool_datasets; printf 'data-srv1/pvc-5e6f7a8b-worker_00000\n'; } >"$pool_dir/datasets.1"
  idle_pool_datasets >"$pool_dir/datasets.2"
  fake_linstor_clock

  rc=0
  output=$(cozy_wait_linstor_pool_baseline 300 2>&1) || rc=$?
  polls=$(cat "$pool_dir/poll")
  rm -rf "$COZY_LINSTOR_POOL_BASELINE_FILE" "$clock_file" "$pool_dir"

  if [ "$rc" -ne 0 ]; then
    echo "pool wait failed after the worker volume was destroyed: $output" >&2
    return 1
  fi
  if [ "$polls" -ne 2 ]; then
    echo "pool wait passed on poll $polls, expected it to wait out the in-flight destroy and pass on poll 2" >&2
    return 1
  fi
}

@test "pool baseline wait fails on a leaked volume and names it" {
  # The shape of a leaked 21 GiB CDI scratch volume on srv3: one dataset the
  # baseline does not have, still present at the deadline.
  # shellcheck source=/dev/null
  . "$HACK_DIR/e2e-chainsaw/_lib/run-kubernetes.sh"
  COZY_LINSTOR_POOL_BASELINE_FILE="_out/tmp/linstor-baseline-$$"
  mkdir -p _out/tmp
  fake_linstor_pools
  idle_pool_datasets | sed 's/^data-\(srv[0-9]\)/\1 &/' >"$COZY_LINSTOR_POOL_BASELINE_FILE"
  { idle_pool_datasets; printf 'data-srv3/pvc-9c0d1e2f-scratch_00000\n'; } >"$pool_dir/datasets.0"
  fake_linstor_clock

  rc=0
  output=$(cozy_wait_linstor_pool_baseline 300 2>&1) || rc=$?
  rm -rf "$COZY_LINSTOR_POOL_BASELINE_FILE" "$clock_file" "$pool_dir"

  if [ "$rc" -ne 1 ]; then
    echo "pool wait returned $rc for a leaked scratch volume, expected 1: $output" >&2
    return 1
  fi
  if ! printf '%s\n' "$output" | grep -Fxq '  linstor-pool: new srv3 data-srv3/pvc-9c0d1e2f-scratch_00000'; then
    echo "the leak report does not name the leaked volume and its node: $output" >&2
    return 1
  fi
}

@test "pool baseline wait fails when a node's pool cannot be read" {
  # shellcheck source=/dev/null
  . "$HACK_DIR/e2e-chainsaw/_lib/run-kubernetes.sh"
  COZY_LINSTOR_POOL_BASELINE_FILE="_out/tmp/linstor-baseline-$$"
  mkdir -p _out/tmp
  fake_linstor_pools
  idle_pool_datasets | sed 's/^data-\(srv[0-9]\)/\1 &/' >"$COZY_LINSTOR_POOL_BASELINE_FILE"
  idle_pool_datasets >"$pool_dir/datasets.0"
  : >"$pool_dir/no-satellite-srv2"
  fake_linstor_clock

  rc=0
  output=$(cozy_wait_linstor_pool_baseline 300 2>&1) || rc=$?
  rm -rf "$COZY_LINSTOR_POOL_BASELINE_FILE" "$clock_file" "$pool_dir"

  if [ "$rc" -ne 1 ]; then
    echo "pool wait returned $rc with srv2's satellite missing, expected 1: $output" >&2
    return 1
  fi
}

@test "pool baseline wait ends at its deadline when a satellite read hangs" {
  # shellcheck source=/dev/null
  . "$HACK_DIR/e2e-chainsaw/_lib/run-kubernetes.sh"
  COZY_LINSTOR_POOL_BASELINE_FILE="_out/tmp/linstor-baseline-$$"
  mkdir -p _out/tmp
  fake_linstor_pools
  idle_pool_datasets | sed 's/^data-\(srv[0-9]\)/\1 &/' >"$COZY_LINSTOR_POOL_BASELINE_FILE"
  idle_pool_datasets >"$pool_dir/datasets.0"
  : >"$pool_dir/hang-srv2"
  fake_linstor_clock

  rc=0
  output=$(cozy_wait_linstor_pool_baseline 300 2>&1) || rc=$?
  unbounded=no
  [ ! -e "$pool_dir/unbounded-read" ] || unbounded=yes
  rm -rf "$COZY_LINSTOR_POOL_BASELINE_FILE" "$clock_file" "$pool_dir"

  if [ "$unbounded" = yes ]; then
    echo "the probe made a LINSTOR read without a ceiling, so a hung exec would hold the wait past its deadline" >&2
    return 1
  fi
  if [ "$rc" -ne 1 ] || ! printf '%s\n' "$output" | grep -Fq 'did not return to their pre-suite baseline within 300s'; then
    echo "a timed-out satellite read did not end the wait as a failure at its deadline: rc=$rc $output" >&2
    return 1
  fi
}

@test "pool baseline wait without a recorded baseline returns 2" {
  # shellcheck source=/dev/null
  . "$HACK_DIR/e2e-chainsaw/_lib/run-kubernetes.sh"
  COZY_LINSTOR_POOL_BASELINE_FILE="_out/tmp/linstor-baseline-absent-$$"
  rm -f "$COZY_LINSTOR_POOL_BASELINE_FILE"
  fake_linstor_pools
  fake_linstor_clock

  rc=0
  cozy_wait_linstor_pool_baseline 300 >/dev/null 2>&1 || rc=$?
  rm -rf "$clock_file" "$pool_dir"

  if [ "$rc" -ne 2 ]; then
    echo "pool wait returned $rc without a baseline, expected 2" >&2
    return 1
  fi
}

@test "replicated Kubernetes suite records a LINSTOR baseline before creating the tenant" {
  # shellcheck source=/dev/null
  . "$HACK_DIR/e2e-chainsaw/_lib/run-kubernetes.sh"
  COZY_E2E_STORAGE_CLASS=replicated
  COZY_LINSTOR_POOL_BASELINE_FILE="_out/tmp/linstor-baseline-$$"
  yq() { printf 'v1.33.12\n'; }
  # The first apply creates the tenant. Exiting the command substitution there
  # also keeps the run away from the `sh -c` loops further on, which would
  # call the real kubectl.
  kubectl() {
    case "$*" in apply*) printf 'UNEXPECTED_TENANT_APPLY\n'; exit 99 ;; esac
    return 0
  }
  cozy_wait_tenant_drained() { return 0; }
  cozy_wait_linstor_pool_free() { return 0; }
  cozy_capture_linstor_pool_baseline() { printf 'BASELINE_CAPTURED\n'; return 42; }
  fake_linstor_clock

  rc=0
  output=$(run_kubernetes_test '.' test-latest-version 59991) || rc=$?
  rm -f "$clock_file"

  if ! printf '%s\n' "$output" | grep -Fq BASELINE_CAPTURED; then
    echo "replicated suite went on without recording a pre-suite LINSTOR baseline" >&2
    return 1
  fi
  if [ "$rc" -eq 0 ] || printf '%s\n' "$output" | grep -Fq UNEXPECTED_TENANT_APPLY; then
    echo "replicated suite continued after the baseline capture failed" >&2
    return 1
  fi
}

@test "replicated suite starting below the LINSTOR pool floor stops early and blames the environment" {
  # shellcheck source=/dev/null
  . "$HACK_DIR/e2e-chainsaw/_lib/run-kubernetes.sh"
  COZY_E2E_STORAGE_CLASS=replicated
  COZY_LINSTOR_POOL_BASELINE_FILE="_out/tmp/linstor-baseline-$$"
  yq() { printf 'v1.33.12\n'; }
  kubectl() {
    case "$*" in
      *machine-readable*) printf '87127923:srv2\n' ;;
      *'sp l'*) printf '| data | srv2 | ZFS | data | 83.09 GiB | 199 GiB |\n' ;;
      apply*) printf 'UNEXPECTED_TENANT_APPLY\n'; exit 99 ;;
    esac
    return 0
  }
  cozy_wait_tenant_drained() { return 0; }
  cozy_capture_linstor_pool_baseline() { return 0; }
  fake_linstor_clock

  rc=0
  output=$(run_kubernetes_test '.' test-latest-version 59991 2>&1) || rc=$?
  rm -f "$clock_file"

  if [ "$rc" -eq 0 ] || printf '%s\n' "$output" | grep -Fq UNEXPECTED_TENANT_APPLY; then
    echo "suite went on to create its tenant with srv2 below the pool floor" >&2
    return 1
  fi
  if ! printf '%s\n' "$output" | grep -Fq 'earlier suites'; then
    echo "the low-pool failure does not name the environment as its cause" >&2
    return 1
  fi
  if ! printf '%s\n' "$output" | grep -Fq '83.09 GiB'; then
    echo "the low-pool failure does not print the per-node pool table" >&2
    return 1
  fi
}

@test "local Kubernetes suite does not demand the replicated lane's pool floor" {
  # shellcheck source=/dev/null
  . "$HACK_DIR/e2e-chainsaw/_lib/run-kubernetes.sh"
  COZY_E2E_STORAGE_CLASS=local
  COZY_LINSTOR_POOL_BASELINE_FILE="_out/tmp/linstor-baseline-$$"
  yq() { printf 'v1.33.12\n'; }
  kubectl() {
    case "$*" in apply*) printf 'TENANT_APPLY_REACHED\n'; exit 0 ;; esac
    return 0
  }
  cozy_wait_tenant_drained() { return 0; }
  cozy_wait_linstor_pool_free() { printf 'UNEXPECTED_FLOOR\n'; return 99; }
  cozy_capture_linstor_pool_baseline() { return 0; }
  fake_linstor_clock

  output=$(run_kubernetes_test '.' test-latest-version 59991 2>&1) || true
  rm -f "$clock_file"

  if printf '%s\n' "$output" | grep -Fq UNEXPECTED_FLOOR; then
    echo "local suite waited for the 90 GiB floor that its platform volumes keep one pool below" >&2
    return 1
  fi
  if ! printf '%s\n' "$output" | grep -Fq TENANT_APPLY_REACHED; then
    echo "local suite stopped before creating its tenant: $output" >&2
    return 1
  fi
}

@test "a suite that fails before recording its baseline cannot reuse the previous suite's" {
  # shellcheck source=/dev/null
  . "$HACK_DIR/e2e-chainsaw/_lib/run-kubernetes.sh"
  COZY_LINSTOR_POOL_BASELINE_FILE="_out/tmp/linstor-baseline-$$"
  mkdir -p _out/tmp
  # The previous suite's baseline is valid and matches the pools, so only its
  # removal keeps this suite's cleanup from passing against it.
  idle_pool_datasets | sed 's/^data-\(srv[0-9]\)/\1 &/' >"$COZY_LINSTOR_POOL_BASELINE_FILE"
  yq() { printf 'v1.33.12\n'; }
  kubectl() { return 0; }
  cozy_wait_tenant_drained() { return 1; }
  run_kubernetes_test '.' test-latest-version 59991 >/dev/null 2>&1 || true
  fake_linstor_pools
  idle_pool_datasets >"$pool_dir/datasets.0"
  fake_linstor_clock

  rc=0
  cozy_wait_linstor_pool_baseline 300 >/dev/null 2>&1 || rc=$?
  rm -rf "$COZY_LINSTOR_POOL_BASELINE_FILE" "$clock_file" "$pool_dir"

  if [ "$rc" -ne 2 ]; then
    echo "cleanup returned $rc instead of reporting the baseline missing; it checked against the previous suite's" >&2
    return 1
  fi
}

@test "pool dataset probe fails on a ZFS pool that names no zpool" {
  # shellcheck source=/dev/null
  . "$HACK_DIR/e2e-chainsaw/_lib/run-kubernetes.sh"
  fake_linstor_pools
  idle_pool_datasets >"$pool_dir/datasets.0"

  listing=$(_cozy_linstor_pool_datasets)
  printf '%s\n' '{"storage_pool_name":"data","node_name":"srv2","provider_kind":"ZFS","props":{}}' >"$pool_dir/srv2-pool"
  rc=0
  _cozy_linstor_pool_datasets >/dev/null 2>&1 || rc=$?
  rm -rf "$pool_dir"

  if [ "$listing" != "$(printf 'srv1 data-srv1\nsrv2 data-srv2\nsrv2 data-srv2/pvc-0a1b2c3d-platform_00000\nsrv3 data-srv3')" ]; then
    echo "the probe did not read each node's zpool off its LINSTOR pool: $listing" >&2
    return 1
  fi
  if [ "$rc" -eq 0 ]; then
    echo "the probe accepted a ZFS pool with no zpool name" >&2
    return 1
  fi
}

@test "pool dataset comparison reports only new datasets and unread nodes" {
  # shellcheck source=/dev/null
  . "$HACK_DIR/e2e-chainsaw/_lib/run-kubernetes.sh"
  baseline=$(printf 'srv1 data\nsrv2 data\nsrv2 data/pvc-old_00000\nsrv3 data')
  reclaimed_old=$(printf 'srv1 data\nsrv2 data\nsrv3 data')
  same_name_other_node=$(printf 'srv1 data\nsrv1 data/pvc-old_00000\nsrv2 data\nsrv2 data/pvc-old_00000\nsrv3 data')
  missing_node=$(printf 'srv1 data\nsrv3 data')

  cozy_linstor_pool_new_datasets "$baseline" "$baseline"
  cozy_linstor_pool_new_datasets "$baseline" "$reclaimed_old"
  rc=0
  output=$(cozy_linstor_pool_new_datasets "$baseline" "$same_name_other_node") || rc=$?
  if [ "$rc" -eq 0 ] || [ "$output" != 'new srv1 data/pvc-old_00000' ]; then
    echo "a volume on a node that did not have it before was not reported as new: rc=$rc $output" >&2
    return 1
  fi
  rc=0
  output=$(cozy_linstor_pool_new_datasets "$baseline" "$missing_node") || rc=$?
  if [ "$rc" -eq 0 ] || [ "$output" != 'unread srv2' ]; then
    echo "a node missing from the current listing was not reported: rc=$rc $output" >&2
    return 1
  fi
}

@test "pool baseline capture persists every node's datasets" {
  # shellcheck source=/dev/null
  . "$HACK_DIR/e2e-chainsaw/_lib/run-kubernetes.sh"
  COZY_LINSTOR_POOL_BASELINE_FILE="_out/tmp/linstor-baseline-$$"
  fake_linstor_pools
  idle_pool_datasets >"$pool_dir/datasets.0"

  output=$(cozy_capture_linstor_pool_baseline)
  captured=$(cat "$COZY_LINSTOR_POOL_BASELINE_FILE")
  printf '%s\n' '{"storage_pool_name":"DfltDisklessStorPool","node_name":"srv2","provider_kind":"DISKLESS","props":{}}' >"$pool_dir/srv2-pool"
  rc=0
  cozy_capture_linstor_pool_baseline >/dev/null 2>&1 || rc=$?
  partial=$(cat "$COZY_LINSTOR_POOL_BASELINE_FILE")
  rm -rf "$COZY_LINSTOR_POOL_BASELINE_FILE" "$pool_dir"

  [ "$captured" = "$(printf 'srv1 data-srv1\nsrv2 data-srv2\nsrv2 data-srv2/pvc-0a1b2c3d-platform_00000\nsrv3 data-srv3')" ]
  printf '%s\n' "$output" | grep -Fq 'LINSTOR pool baseline recorded'
  if [ "$rc" -eq 0 ] || [ -n "$partial" ]; then
    echo "baseline capture accepted a listing without srv2: rc=$rc $partial" >&2
    return 1
  fi
}

@test "Kubernetes cleanup fails when LINSTOR capacity is not reclaimed" {
  # shellcheck source=/dev/null
  . "$HACK_DIR/e2e-chainsaw/_lib/run-kubernetes.sh"
  kubectl() { return 0; }
  cozy_wait_tenant_drained() { return 0; }
  cozy_wait_linstor_pool_baseline() { return 42; }

  if cozy_cleanup test-latest-version; then
    echo "cleanup hid the failed LINSTOR reclamation barrier" >&2
    return 1
  fi
}

@test "cleanup of a suite that never recorded a baseline does not report a leak" {
  # shellcheck source=/dev/null
  . "$HACK_DIR/e2e-chainsaw/_lib/run-kubernetes.sh"
  COZY_LINSTOR_POOL_BASELINE_FILE="_out/tmp/linstor-baseline-absent-$$"
  rm -f "$COZY_LINSTOR_POOL_BASELINE_FILE"
  kubectl() { return 0; }
  cozy_wait_tenant_drained() { return 0; }
  fake_linstor_clock

  rc=0
  output=$(cozy_cleanup test-latest-version 2>&1) || rc=$?
  rm -f "$clock_file"

  if [ "$rc" -eq 0 ]; then
    echo "cleanup passed with LINSTOR reclamation unchecked" >&2
    return 1
  fi
  if printf '%s\n' "$output" | grep -Fq 'did not return to the pre-suite level'; then
    echo "cleanup blamed the suite for a leak although it stopped before recording a baseline: $output" >&2
    return 1
  fi
}

@test "local Kubernetes suite cannot start without a LINSTOR baseline" {
  # shellcheck source=/dev/null
  . "$HACK_DIR/e2e-chainsaw/_lib/run-kubernetes.sh"
  COZY_E2E_STORAGE_CLASS=local
  COZY_LINSTOR_POOL_BASELINE_FILE="_out/tmp/linstor-baseline-$$"
  yq() { printf 'v1.33.12\n'; }
  kubectl() { return 0; }
  cozy_wait_tenant_drained() { return 0; }
  cozy_capture_linstor_pool_baseline() { return 42; }
  helm() { printf 'UNEXPECTED_HELM_CALL\n'; return 99; }
  fake_linstor_clock

  rc=0
  output=$(run_kubernetes_test '.' test-latest-version 59991) || rc=$?
  rm -f "$clock_file"

  if [ "$rc" -eq 0 ]; then
    echo "suite accepted a failed local LINSTOR baseline capture" >&2
    return 1
  fi
  if printf '%s\n' "$output" | grep -Fq UNEXPECTED_HELM_CALL; then
    echo "suite continued into Helm after the baseline capture failed" >&2
    return 1
  fi
}

@test "Kubernetes cleanup operations outlive their bounded reclamation stages" {
  for suite in latest previous; do
    timeout=$(yq "select(.metadata.name == \"kubernetes-${suite}\") | .spec.steps[0].finally[0].script.timeout" "hack/e2e-chainsaw/kubernetes-${suite}/chainsaw-test.yaml")
    if [ "$timeout" != 22m ]; then
      echo "kubernetes-${suite} cleanup timeout is $timeout, expected 22m" >&2
      return 1
    fi
  done
}

@test "both merge-gating lanes forward local storage through install and Chainsaw" {
  # Checked in both workflows rather than one: pull-requests.yaml gates same-repo
  # PRs and e2e-fork.yaml gates fork PRs, they carry the override separately, and
  # a lane that installs on `local` while templating Chainsaw with `replicated`
  # fails deep inside a suite rather than at the mismatch.
  install_command=$(make -n -C packages/core/testing SANDBOX_NAME=test COZY_E2E_STORAGE_CLASS=local install-cozystack)
  make_command=$(make -n -C packages/core/testing SANDBOX_NAME=test COZY_E2E_STORAGE_CLASS=local CHAINSAW_SUITES=vminstance test-chainsaw)

  for wf in .github/workflows/pull-requests.yaml .github/workflows/e2e-fork.yaml; do
    install_value=$(yq '.jobs.e2e.steps[] | select(.name == "Install Cozystack into sandbox") | .env.COZY_E2E_STORAGE_CLASS' "$wf")
    run_value=$(yq '.jobs.e2e.steps[] | select(.name == "Run E2E tests") | .env.COZY_E2E_STORAGE_CLASS' "$wf")
    if [ "$install_value" != local ] || [ "$run_value" != local ]; then
      echo "unexpected storage modes in $wf: install=$install_value chainsaw=$run_value" >&2
      return 1
    fi
  done
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
