#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Catches silent regressions in the gpu-operator package's three variants:
#
#   1. The base values.yaml pins ccManager.enabled=false and
#      vgpuDeviceManager.enabled=false. Both upstream defaults are now true
#      (chart v26.x). If the next chart bump silently un-pins either one,
#      the `default` (passthrough) variant would gain confidential-computing
#      auto-enable on Hopper hardware (ccManager) or render an mdev DaemonSet
#      that crashloops on Ada+/Blackwell (vgpuDeviceManager). The same union
#      must hold for the `container` variant; the `vgpu` variant re-affirms
#      vgpuDeviceManager=false in its own overlay for the same reason.
#
#   2. The sandbox variants must set sandboxWorkloads.defaultWorkload to
#      their own enum value (vm-passthrough / vm-vgpu) — without it, GPU
#      nodes that lack the per-node label nvidia.com/gpu.workload.config
#      fall back to the upstream default 'container' workload, no DaemonSet
#      renders, and the variant is silently a no-op. The container variant
#      relies on the same upstream default but in the opposite direction:
#      with sandboxWorkloads.enabled=false the chart picks defaultWorkload
#      'container', which is what that variant wants — pinned here so an
#      upstream rename does not silently break it.
#
#   3. The vgpu variant must enable the vGPU manager DaemonSet
#      (vgpuManager.enabled=true), the passthrough variant must keep
#      driver.enabled / devicePlugin.enabled at false, and the container
#      variant must keep driver / toolkit / vfioManager at false (host
#      already provides them) while keeping devicePlugin enabled (publishes
#      nvidia.com/gpu to the kubelet). The container variant must also pin
#      cdi.enabled=false: upstream defaults it true, but CDI specs are
#      serviced by the toolkit DaemonSet this variant disables, so leaving
#      it on points the device plugin at CDI injection with no specs on an
#      apt host and silently breaks allocation.
#
# Implementation: render the chart with the merged values for each variant
# and grep for the relevant ClusterPolicy keys. We strip the package's
# 'gpu-operator:' Helm-subchart prefix because we render the subchart
# directly here — the prefix is meaningful only when the parent wrapper
# chart is in play.
#
# Compatible with both `bats` directly and the in-repo cozytest.sh runner.
# cozytest.sh runs each @test in a fresh subshell with `set -u` and does
# not honor bats setup()/teardown(), so we provision TMP inline per test.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
CHART="$REPO_ROOT/packages/system/gpu-operator/charts/gpu-operator"
VALUES_DIR="$REPO_ROOT/packages/system/gpu-operator"

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
  variant="$1"
  unwrap "$VALUES_DIR/values.yaml"             > "$TMP/values.yaml"
  unwrap "$VALUES_DIR/values-${variant}.yaml"  > "$TMP/variant.yaml"
  helm template t "$CHART" \
    --values "$TMP/values.yaml" \
    --values "$TMP/variant.yaml" \
    > "$TMP/rendered.yaml" 2>"$TMP/err"
  [ -s "$TMP/rendered.yaml" ] || { cat "$TMP/err" >&2; return 1; }
}

@test "default variant: ccManager and vgpuDeviceManager pinned off" {
  TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
  render_variant passthrough
  grep -A 1 '^  ccManager:'         "$TMP/rendered.yaml" | grep -q 'enabled: false'
  grep -A 1 '^  vgpuDeviceManager:' "$TMP/rendered.yaml" | grep -q 'enabled: false'
}

@test "default variant: defaultWorkload is vm-passthrough" {
  TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
  render_variant passthrough
  grep -A 2 '^  sandboxWorkloads:' "$TMP/rendered.yaml" | grep -q 'defaultWorkload: vm-passthrough'
}

@test "default variant: driver and devicePlugin disabled, vfioManager left on" {
  TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
  render_variant passthrough
  grep -A 1 '^  driver:'       "$TMP/rendered.yaml" | grep -q 'enabled: false'
  grep -A 1 '^  devicePlugin:' "$TMP/rendered.yaml" | grep -q 'enabled: false'
  grep -A 1 '^  vfioManager:'  "$TMP/rendered.yaml" | grep -q 'enabled: true'
}

@test "vgpu variant: ccManager and vgpuDeviceManager pinned off" {
  TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
  render_variant vgpu
  grep -A 1 '^  ccManager:'         "$TMP/rendered.yaml" | grep -q 'enabled: false'
  grep -A 1 '^  vgpuDeviceManager:' "$TMP/rendered.yaml" | grep -q 'enabled: false'
}

@test "vgpu variant: defaultWorkload is vm-vgpu" {
  TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
  render_variant vgpu
  grep -A 2 '^  sandboxWorkloads:' "$TMP/rendered.yaml" | grep -q 'defaultWorkload: vm-vgpu'
}

@test "vgpu variant: vgpuManager enabled, driver and devicePlugin disabled" {
  TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
  render_variant vgpu
  grep -A 1 '^  vgpuManager:'  "$TMP/rendered.yaml" | grep -q 'enabled: true'
  grep -A 1 '^  driver:'       "$TMP/rendered.yaml" | grep -q 'enabled: false'
  grep -A 1 '^  devicePlugin:' "$TMP/rendered.yaml" | grep -q 'enabled: false'
}

@test "container variant: ccManager and vgpuDeviceManager pinned off" {
  TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
  render_variant container
  grep -A 1 '^  ccManager:'         "$TMP/rendered.yaml" | grep -q 'enabled: false'
  grep -A 1 '^  vgpuDeviceManager:' "$TMP/rendered.yaml" | grep -q 'enabled: false'
}

@test "container variant: sandboxWorkloads off, defaultWorkload stays upstream 'container'" {
  TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
  render_variant container
  grep -A 2 '^  sandboxWorkloads:' "$TMP/rendered.yaml" | grep -q 'enabled: false'
  grep -A 2 '^  sandboxWorkloads:' "$TMP/rendered.yaml" | grep -q 'defaultWorkload: container'
}

@test "container variant: driver, toolkit, vfioManager off; devicePlugin on" {
  TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
  render_variant container
  grep -A 1 '^  driver:'       "$TMP/rendered.yaml" | grep -q 'enabled: false'
  grep -A 1 '^  toolkit:'      "$TMP/rendered.yaml" | grep -q 'enabled: false'
  grep -A 1 '^  vfioManager:'  "$TMP/rendered.yaml" | grep -q 'enabled: false'
  grep -A 1 '^  devicePlugin:' "$TMP/rendered.yaml" | grep -q 'enabled: true'
}

@test "container variant: cdi pinned off (toolkit disabled cannot service CDI specs)" {
  TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
  render_variant container
  grep -A 1 '^  cdi:' "$TMP/rendered.yaml" | grep -q 'enabled: false'
}
