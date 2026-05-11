#!/usr/bin/env bash
# Verify every ApplicationDefinition in packages/system/*-rd/cozyrds/*.yaml
# that defines a resourcesPreset enum carries the full set of 40
# instance-type names plus the 7 legacy aliases. Catches the regression
# where one chart's Makefile forgets to invoke hack/update-crd.sh and the
# RD schema drifts from the chart's values.schema.json.
set -euo pipefail

EXPECTED=(
  t1.nano t1.micro t1.small t1.medium t1.large t1.xlarge t1.2xlarge t1.4xlarge
  c1.nano c1.micro c1.small c1.medium c1.large c1.xlarge c1.2xlarge c1.4xlarge
  s1.nano s1.micro s1.small s1.medium s1.large s1.xlarge s1.2xlarge s1.4xlarge
  u1.nano u1.micro u1.small u1.medium u1.large u1.xlarge u1.2xlarge u1.4xlarge
  m1.nano m1.micro m1.small m1.medium m1.large m1.xlarge m1.2xlarge m1.4xlarge
  nano micro small medium large xlarge 2xlarge
)

fail=0
for f in packages/system/*-rd/cozyrds/*.yaml; do
  schema=$(yq -r '.spec.application.openAPISchema // ""' "$f")
  if [ -z "$schema" ]; then
    continue
  fi
  # The schema is a JSON blob. Pull every resourcesPreset enum out of it.
  enums=$(printf '%s' "$schema" | jq -r '
    [.. | objects | select(has("enum") and (.description? // "" | test("preset"; "i")))]
    | .[].enum[]
  ' 2>/dev/null || true)
  if [ -z "$enums" ]; then
    continue
  fi
  missing=()
  for want in "${EXPECTED[@]}"; do
    if ! printf '%s\n' "$enums" | grep -qx "$want"; then
      missing+=("$want")
    fi
  done
  if [ ${#missing[@]} -gt 0 ]; then
    echo "FAIL: $f resourcesPreset enum missing: ${missing[*]}" >&2
    fail=1
  fi
done

if [ "$fail" -ne 0 ]; then
  echo "Some RD schemas are out of sync with the canonical preset set." >&2
  echo "Run 'make generate' inside the affected chart directory." >&2
  exit 1
fi
echo "All RD schemas carry the full 47-preset enum."

# Also reject hardcoded legacy preset literals in chart template files.
# New code should use instance-type names; legacy aliases exist only for
# user-supplied values in existing HelmRelease and app CR specs.
echo "Checking chart templates for hardcoded legacy preset literals..."
legacy_hits=$(grep -rEn 'defaultingSanitize.*\(list "(nano|micro|small|medium|large|xlarge|2xlarge)"' \
  packages/apps packages/extra packages/system 2>/dev/null || true)
if [ -n "$legacy_hits" ]; then
  echo "FAIL: hardcoded legacy preset literals in chart templates:" >&2
  echo "$legacy_hits" >&2
  echo "Replace with the instance-type form from docs/operations/resource-presets.md." >&2
  exit 1
fi
echo "No hardcoded legacy preset literals in chart templates."
