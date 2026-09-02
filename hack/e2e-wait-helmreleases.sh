#!/bin/bash
# Readiness gate for the platform HelmReleases created during E2E install.

cozy_wait_all_helmreleases_ready() {
  local timeout_seconds="${1:-900}"
  local minimum_count="${2:-11}"
  local quiet_seconds="${3:-5}"
  local poll_seconds="${4:-2}"
  local deadline
  local snapshot= count= stalled= not_ready= fingerprint=
  local stable_fingerprint= stable_since=0 now

  case "${timeout_seconds}:${minimum_count}:${quiet_seconds}:${poll_seconds}" in
    *[!0-9:]*)
      echo "HelmRelease readiness timing arguments must be integers" >&2
      return 1
      ;;
  esac
  if [ "${minimum_count}" -eq 0 ] || [ "${poll_seconds}" -eq 0 ]; then
    echo "HelmRelease readiness minimum count and poll interval must be positive" >&2
    return 1
  fi
  deadline=$(( $(date +%s) + timeout_seconds ))

  echo "Waiting for at least ${minimum_count} HelmReleases to become Ready and remain a stable set for ${quiet_seconds}s..."
  while :; do
    now=$(date +%s)
    if ! snapshot=$(kubectl get helmreleases.helm.toolkit.fluxcd.io -A -o json); then
      echo "HelmRelease readiness list failed; keeping the gate closed" >&2
      stable_fingerprint=
      stable_since=0
    else
      if ! count=$(printf '%s\n' "${snapshot}" | jq -r '.items | length'); then
        echo "Could not parse the HelmRelease readiness list" >&2
        return 1
      fi
      stalled=$(printf '%s\n' "${snapshot}" | jq -r '
        .items[] |
        select((.status.observedGeneration // 0) == .metadata.generation) |
        select([.status.conditions[]? | select(.type == "Stalled" and .status == "True")] | length > 0) |
        "\(.metadata.namespace)/\(.metadata.name): " +
        (([.status.conditions[]? | select(.type == "Stalled") | .message][0]) // "Stalled=True")
      ')
      if [ -n "${stalled}" ]; then
        echo "HelmRelease reconciliation reached a terminal Stalled condition:" >&2
        printf '%s\n' "${stalled}" | sed 's/^/  /' >&2
        return 1
      fi

      not_ready=$(printf '%s\n' "${snapshot}" | jq -r '
        .items[] |
        (.status.observedGeneration // 0) as $observed |
        .metadata.generation as $generation |
        ([.status.conditions[]? | select(.type == "Ready" and .status == "True")] | length) as $ready |
        select($observed != $generation or $ready == 0) |
        "\(.metadata.namespace)/\(.metadata.name): " +
        (if $observed != $generation then
           "generation \($generation) not observed (status.observedGeneration=\($observed))"
         else
           (([.status.conditions[]? | select(.type == "Ready") | "\(.reason): \(.message)"][0]) // "Ready condition absent")
         end)
      ')
      if [ "${count}" -ge "${minimum_count}" ] && [ -z "${not_ready}" ]; then
        fingerprint=$(printf '%s\n' "${snapshot}" | jq -r '
          .items | map("\(.metadata.namespace)/\(.metadata.name)") | sort | join("\n")
        ')
        if [ "${fingerprint}" != "${stable_fingerprint}" ]; then
          stable_fingerprint="${fingerprint}"
          stable_since="${now}"
        elif [ $(( now - stable_since )) -ge "${quiet_seconds}" ]; then
          echo "All ${count} HelmReleases are Ready; the release set stayed stable for ${quiet_seconds}s."
          return 0
        fi
      else
        stable_fingerprint=
        stable_since=0
      fi
    fi

    if [ "${now}" -ge "${deadline}" ]; then
      echo "HelmReleases did not all become Ready within ${timeout_seconds}s" >&2
      if [ -n "${not_ready}" ]; then
        printf '%s\n' "${not_ready}" | sed 's/^/  /' >&2
      fi
      if ! kubectl get helmreleases.helm.toolkit.fluxcd.io -A >&2; then
        echo "The final HelmRelease diagnostic list also failed" >&2
      fi
      return 1
    fi
    sleep "${poll_seconds}"
  done
}
