#!/usr/bin/env bash
# Render every packages/system/*-rd chart the way each of its variants is
# installed, values.yaml alone and with each values-<variant>.yaml beside it,
# and check the consumption declarations the rendered ApplicationDefinitions
# carry. Instance types resolve against the rendered kubevirt-instancetypes.
set -euo pipefail
shopt -s nullglob

cd "$(dirname "$0")/.."
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

go build -o "$work/check-consumption" ./cmd/check-consumption
helm template instancetypes packages/system/kubevirt-instancetypes >"$work/instancetypes.yaml"

fail=0
for chart in packages/system/*-rd; do
  for values in "" "$chart"/values-*.yaml; do
    args=(template rd "$chart")
    label=$chart
    if [ -n "$values" ]; then
      args+=(--values "$values")
      label="$chart with $(basename "$values")"
    fi
    if ! helm "${args[@]}" >"$work/rendered.yaml"; then
      echo "FAIL: $label does not render" >&2
      fail=1
      continue
    fi
    if ! cat "$work/instancetypes.yaml" "$work/rendered.yaml" | "$work/check-consumption"; then
      echo "FAIL: $label" >&2
      fail=1
    fi
  done
done
exit "$fail"
