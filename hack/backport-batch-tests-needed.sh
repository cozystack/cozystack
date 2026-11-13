#!/bin/sh
# Print whether a changed-path list touches an input of
# `go test ./cmd/backport-batch/...`, as listed in
# cmd/backport-batch/test-inputs.txt. An entry ending in / matches every path
# below it, any other entry matches that file alone. The caller supplies the
# rename-blind list so both sides of a rename are classified.
#
# Usage: hack/backport-batch-tests-needed.sh <changed-files> [<inputs-file>]
set -eu

inputs="${2:-cmd/backport-batch/test-inputs.txt}"
if [ "$#" -lt 1 ] || [ "$#" -gt 2 ] || [ ! -r "$1" ] || [ ! -r "$inputs" ]; then
  echo "usage: backport-batch-tests-needed.sh <readable-changed-files> [<readable-inputs-file>]" >&2
  exit 2
fi

# An inputs file with no entry would answer false for every diff and switch the
# tests off without a word; refuse instead.
if ! grep -qvE '^[[:space:]]*(#|$)' "$inputs"; then
  echo "backport-batch-tests-needed: $inputs lists no path" >&2
  exit 2
fi

# Entries are trimmed the way TestInputsListed trims them, so a trailing space
# or a CR cannot make the Go test accept an entry this never matches.
awk '
  NR == FNR {
    e = $0
    sub(/^[[:space:]]+/, "", e)
    sub(/[[:space:]]+$/, "", e)
    if (e != "" && e !~ /^#/) entries[++n] = e
    next
  }
  {
    for (i = 1; i <= n; i++) {
      e = entries[i]
      if ((substr(e, length(e)) == "/" && index($0, e) == 1) || $0 == e) {
        found = 1
        exit
      }
    }
  }
  END { print (found ? "true" : "false") }
' "$inputs" "$1"
