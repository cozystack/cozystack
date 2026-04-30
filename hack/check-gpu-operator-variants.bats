#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Catches silent regressions in the gpu-operator package's two variants:
#
#   1. The base values.yaml pins ccManager.enabled=false and
#      vgpuDeviceManager.enabled=false. Both upstream defaults are now true
#      (chart v26.x). If the next chart bump silently un-pins either one,
#      the `default` (passthrough) variant would gain confidential-computing
#      auto-enable on Hopper hardware (ccManager) or render an mdev DaemonSet
#      that crashloops on Ada+/Blackwell (vgpuDeviceManager).
#
#   2. Each variant's overlay must set sandboxWorkloads.defaultWorkload to its
#      own enum value (vm-passthrough / vm-vgpu) — without it, GPU nodes that
#      lack the per-node label nvidia.com/gpu.workload.config fall back to
#      the upstream default 'container' workload, no DaemonSet renders, and
#      the variant is silently a no-op.
#
#   3. The vgpu variant must enable the vGPU manager DaemonSet
#      (vgpuManager.enabled=true) and the passthrough variant must keep
#      driver.enabled / devicePlugin.enabled at false.
#
# Implementation: render the chart with the merged values for each variant
# and grep for the relevant ClusterPolicy keys. We strip the package's
# 'gpu-operator:' Helm-subchart prefix because we render the subchart
# directly here — the prefix is meaningful only when the parent wrapper
# chart is in play.

CHART="${BATS_TEST_DIRNAME}/../packages/system/gpu-operator/charts/gpu-operator"
VALUES_DIR="${BATS_TEST_DIRNAME}/../packages/system/gpu-operator"

setup() {
  TMP=$(mktemp -d)
}

teardown() {
  rm -rf "$TMP"
}

# Strip the 'gpu-operator:' wrapper-prefix so the values can be applied
# directly to the subchart in `helm template`.
unwrap() {
  awk '
    /^gpu-operator:[[:space:]]*$/ { in_block=1; next }
    in_block && /^[[:space:]]/    { sub(/^  /, ""); print; next }
    in_block && /^[^[:space:]]/   { in_block=0 }
    !in_block                     { print }
  ' "$1"
}

render_variant() {
  local variant="$1"
  unwrap "$VALUES_DIR/values.yaml"             > "$TMP/values.yaml"
  unwrap "$VALUES_DIR/values-${variant}.yaml"  > "$TMP/variant.yaml"
  helm template t "$CHART" \
    --values "$TMP/values.yaml" \
    --values "$TMP/variant.yaml" \
    > "$TMP/rendered.yaml" 2>"$TMP/err"
  [ -s "$TMP/rendered.yaml" ] || {
    cat "$TMP/err" >&2
    return 1
  }
}

@test "default variant: ccManager and vgpuDeviceManager pinned off" {
  render_variant passthrough
  grep -A 1 '^  ccManager:'         "$TMP/rendered.yaml" | grep -q 'enabled: false'
  grep -A 1 '^  vgpuDeviceManager:' "$TMP/rendered.yaml" | grep -q 'enabled: false'
}

@test "default variant: defaultWorkload is vm-passthrough" {
  render_variant passthrough
  grep -A 2 '^  sandboxWorkloads:' "$TMP/rendered.yaml" | grep -q 'defaultWorkload: vm-passthrough'
}

@test "default variant: driver and devicePlugin disabled, vfioManager left on" {
  render_variant passthrough
  grep -A 1 '^  driver:'       "$TMP/rendered.yaml" | grep -q 'enabled: false'
  grep -A 1 '^  devicePlugin:' "$TMP/rendered.yaml" | grep -q 'enabled: false'
  grep -A 1 '^  vfioManager:'  "$TMP/rendered.yaml" | grep -q 'enabled: true'
}

@test "vgpu variant: ccManager and vgpuDeviceManager pinned off" {
  render_variant vgpu
  grep -A 1 '^  ccManager:'         "$TMP/rendered.yaml" | grep -q 'enabled: false'
  grep -A 1 '^  vgpuDeviceManager:' "$TMP/rendered.yaml" | grep -q 'enabled: false'
}

@test "vgpu variant: defaultWorkload is vm-vgpu" {
  render_variant vgpu
  grep -A 2 '^  sandboxWorkloads:' "$TMP/rendered.yaml" | grep -q 'defaultWorkload: vm-vgpu'
}

@test "vgpu variant: vgpuManager enabled, driver and devicePlugin disabled" {
  render_variant vgpu
  grep -A 1 '^  vgpuManager:'  "$TMP/rendered.yaml" | grep -q 'enabled: true'
  grep -A 1 '^  driver:'       "$TMP/rendered.yaml" | grep -q 'enabled: false'
  grep -A 1 '^  devicePlugin:' "$TMP/rendered.yaml" | grep -q 'enabled: false'
}
