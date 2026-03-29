#!/usr/bin/env bash
# Check readiness of Packages, ArtifactGenerators, ExternalArtifacts, and HelmReleases
# Shows only non-ready resources
#
# Usage: check-readiness.sh [-w [INTERVAL]]
#   -w [INTERVAL]  Watch mode: refresh continuously every INTERVAL seconds (default: 5)

set -euo pipefail

KUBECTL="kubectl"
if [[ -n "${KUBECONFIG:-}" ]]; then
  KUBECTL="kubectl --kubeconfig=${KUBECONFIG}"
fi

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BOLD='\033[1m'
RESET='\033[0m'

WATCH=0
INTERVAL=5

while [[ $# -gt 0 ]]; do
  case "$1" in
    -w|--watch)
      WATCH=1
      if [[ $# -gt 1 && "$2" =~ ^[0-9]+$ ]]; then
        INTERVAL="$2"
        shift
      fi
      shift
      ;;
    *) echo "Unknown argument: $1"; exit 2 ;;
  esac
done

check_resource() {
  local kind="$1"
  local header_printed=0

  while IFS= read -r line; do
    if [[ "$line" =~ ^(NAME|NAMESPACE) ]]; then
      continue
    fi
    if ! echo "$line" | awk '{for(i=1;i<=NF;i++) if($i=="True") exit 0; exit 1}'; then
      if [[ $header_printed -eq 0 ]]; then
        echo -e "${BOLD}${YELLOW}=== ${kind} (not ready) ===${RESET}"
        header_printed=1
      fi
      echo -e "${RED}${line}${RESET}"
      found_issues=1
    fi
  done < <($KUBECTL get "$kind" -A 2>/dev/null)

  return 0
}

run_once() {
  found_issues=0

  check_resource packages.cozystack.io
  check_resource artifactgenerators.source.extensions.fluxcd.io
  check_resource externalartifacts.source.toolkit.fluxcd.io
  check_resource helmreleases.helm.toolkit.fluxcd.io

  if [[ $found_issues -eq 0 ]]; then
    echo -e "${GREEN}${BOLD}All resources are ready.${RESET}"
  fi
}

if [[ $WATCH -eq 1 ]]; then
  while true; do
    output=$(run_once 2>&1)
    clear
    echo -e "${BOLD}Last updated: $(date)  (refreshing every ${INTERVAL}s, Ctrl+C to stop)${RESET}\n"
    echo -e "$output"
    sleep "$INTERVAL"
  done
else
  run_once
  [[ $found_issues -eq 0 ]] || exit 1
fi
