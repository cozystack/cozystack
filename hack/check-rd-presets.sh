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
jq_err=$(mktemp)
trap 'rm -f "$jq_err"' EXIT
for f in packages/system/*-rd/cozyrds/*.yaml; do
  schema=$(yq -r '.spec.application.openAPISchema // ""' "$f")
  if [ -z "$schema" ]; then
    continue
  fi
  # Pull every resourcesPreset enum out of the schema. Key on the JSON
  # path ending in "resourcesPreset" rather than a description heuristic,
  # so an unrelated field with "preset" in its description does not match.
  #
  # jq's status is read rather than discarded. `2>/dev/null || true` left a
  # read that failed indistinguishable from one that found nothing, and it goes
  # two ways: with no output the emptiness guard below turned it into a clean
  # skip, so a schema nobody could parse silently passed the check whose whole
  # job is to parse it, and with output already written before jq died $enums
  # held a truncated list that every preset past the cut was then judged
  # against. An errored read is now named as one and carries jq's own reason.
  # Fed by here-string for the same reason as the membership test below.
  if ! enums=$(jq -r '
    [paths(type == "object" and has("enum")) as $p
     | select($p[-1] == "resourcesPreset")
     | getpath($p).enum[]]
    | .[]
  ' <<<"$schema" 2>"$jq_err"); then
    echo "FAIL: $f openAPISchema could not be read, so its presets were not checked: $(tr '\n' ' ' <"$jq_err")" >&2
    fail=1
    continue
  fi
  # Empty stays a skip, not a failure: most RDs define no resourcesPreset at all,
  # and only a read that ERRORED is evidence of anything.
  if [ -z "$enums" ]; then
    continue
  fi
  missing=()
  for want in "${EXPECTED[@]}"; do
    # -F: literal match so the `.` in t1.nano does not match any character.
    # A here-string, not a pipe: `grep -q` exits on its first match, so a pipe
    # leaves the writer with unwritten output and kills it with SIGPIPE. Under
    # `set -o pipefail` that becomes exit 141 for the whole pipeline, which
    # reads exactly like "not found" — so a preset that IS present is reported
    # missing, on a different file and a different preset each run. Measured at
    # roughly one spurious failure per run of this script.
    if ! grep -Fqx -- "$want" <<<"$enums"; then
      missing+=("$want")
    fi
  done
  if [ ${#missing[@]} -gt 0 ]; then
    echo "FAIL: $f resourcesPreset enum missing: ${missing[*]}" >&2
    fail=1
  fi
done

if [ "$fail" -ne 0 ]; then
  echo "One or more RD schemas did not pass the preset check above." >&2
  echo "A missing preset is drift: run 'make generate' inside the affected chart" >&2
  echo "directory. A schema that could not be read is not drift and carries its" >&2
  echo "own reason on the line that names it." >&2
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
