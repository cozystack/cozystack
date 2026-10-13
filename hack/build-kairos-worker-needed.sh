#!/bin/sh
# Print whether a changed-path list contains an input to the pull-request
# Kairos worker disk build. The caller supplies the rename-blind list so
# removals and both sides of a rename are classified.
set -eu

if [ "$#" -ne 1 ] || [ ! -f "$1" ] || [ ! -r "$1" ]; then
  echo "usage: build-kairos-worker-needed.sh <readable-changed-files>" >&2
  exit 2
fi

pattern='^(packages/apps/kubernetes-nodes/(images/kairos-worker(-disk)?/[^[:space:]]+|files/versions\.yaml|Makefile)|hack/common-envs\.mk|hack/buildkitd\.toml|\.github/workflows/pull-requests\.yaml|\.dockerignore)$'

if grep -qE "$pattern" "$1"; then
  printf 'true\n'
else
  status=$?
  if [ "$status" -ne 1 ]; then
    exit "$status"
  fi
  printf 'false\n'
fi
