#!/bin/bash
set -euo pipefail

# shellcheck source=hack/e2e-chainsaw/_lib/remediation-guard.sh
source "$(dirname -- "${BASH_SOURCE[0]}")/../_lib/remediation-guard.sh"

context=${COZY_E2E_CONTEXT:-$(kubectl --context="" config current-context)}
exec 3< <(exec kubectl --context "$context" --namespace "$1" get helmreleases \
  --field-selector "metadata.name=$2" --watch --output='jsonpath={@}{"\n"}')
watcher=$!
trap 'kill "$watcher" 2>/dev/null || :; wait "$watcher" 2>/dev/null || :' EXIT

while IFS= read -r event <&3; do
  statuses=$(printf '%s\n' "$event" | jq --raw-output '(.items // [.])[] | .status.history[]?.status // empty')
  if helmrelease_has_remediation_cycle "$statuses"; then
    printf 'HelmRelease %s/%s has failed history: %s\n' "$1" "$2" "$statuses" >&2
    exit 1
  fi
  verdict=$(printf '%s\n' "$event" | jq --raw-output '
    (.items // [.])[] |
    if .spec.install.strategy.name != "RetryOnFailure" then
      error("Application HelmRelease must use RetryOnFailure")
    elif any(.status.conditions[]?; (.type == "Stalled" and .status == "True") or
        (.type == "Ready" and .status == "False" and (.reason | endswith("Failed")))) then
      error("HelmRelease failed: " + (.status | tojson))
    elif any(.status.conditions[]?; .type == "Ready" and .status == "True") then
      if (.status.history | type) != "array" or (.status.history | length) == 0 or
          any(.status.history[]; (.status | type) != "string" or .status == "") then
        error("Ready HelmRelease has no readable action history")
      elif .status.observedGeneration == .metadata.generation then "ready"
      else empty end
    else empty end')
  if [ "$verdict" = ready ]; then
    exit 0
  fi
done

echo "HelmRelease $1/$2 watch ended before observed Ready" >&2
exit 1
