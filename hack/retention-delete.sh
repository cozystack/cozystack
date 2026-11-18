#!/bin/sh
# Delete GHCR container-package versions for the retention workflow.
#
# Usage: APPLY=true|false ORG=<org> [DELETE_TOKEN=<token>] \
#          retention-delete.sh <package> <label> <version-id>...
#
# Deletes with DELETE_TOKEN, required under APPLY=true, rather than the GH_TOKEN
# that listed the versions: the app installation token can list org package
# versions but has its DELETE answered with 404, while the workflow's
# GITHUB_TOKEN deleted a version of a package linked to its repository.
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
if [ "${APPLY:-false}" = "true" ]; then
  : "${DELETE_TOKEN:?DELETE_TOKEN is required to delete}"
fi

enc="$(printf '%s' "$PKG" | sed 's|/|%2F|g')"
failed=0
for id in "$@"; do
  if [ "${APPLY:-false}" != "true" ]; then
    echo "  • would delete ${PKG} ${LABEL} ${id}"
    continue
  fi
  echo "  ✗ deleting ${PKG} ${LABEL} ${id}"
  if ! err="$(GH_TOKEN="$DELETE_TOKEN" gh api -X DELETE "/orgs/${ORG}/packages/container/${enc}/versions/${id}" 2>&1 >/dev/null)"; then
    echo "::error::failed to delete ${PKG} ${LABEL} ${id}: ${err}"
    failed=1
  fi
done
exit "$failed"
