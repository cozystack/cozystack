#!/bin/sh
# Delete GHCR container-package versions for the retention workflow.
#
# Usage: APPLY=true|false ORG=<org> retention-delete.sh <package> <label> <version-id>...
#
# <label> names what is deleted in the log ("version", "promotion candidate
# version"). Without APPLY=true it only reports. A refused delete is an error,
# not a warning: the remaining ids are still tried, then the exit is non-zero,
# so a token that cannot delete turns the run red instead of leaving it green
# with nothing removed.
set -eu

PKG="${1:?usage: retention-delete.sh <package> <label> <version-id>...}"
LABEL="${2:?usage: retention-delete.sh <package> <label> <version-id>...}"
shift 2
: "${ORG:?ORG is required}"

enc="$(printf '%s' "$PKG" | sed 's|/|%2F|g')"
failed=0
for id in "$@"; do
  if [ "${APPLY:-false}" != "true" ]; then
    echo "  • would delete ${PKG} ${LABEL} ${id}"
    continue
  fi
  echo "  ✗ deleting ${PKG} ${LABEL} ${id}"
  if ! err="$(gh api -X DELETE "/orgs/${ORG}/packages/container/${enc}/versions/${id}" 2>&1 >/dev/null)"; then
    echo "::error::failed to delete ${PKG} ${LABEL} ${id}: ${err}"
    failed=1
  fi
done
exit "$failed"
