#!/bin/sh
# Prints the image digest docker buildx wrote to the metadata file <file>.
# A build that exported nothing leaves no digest, and the image targets would
# otherwise stamp an empty or "null" reference into the files they ship.
set -eu
metadata=${1:?expected the buildx metadata file}
digest=$(yq e '."containerimage.digest"' "$metadata" --output-format json --unwrapScalar 2> /dev/null) || digest=
if [ "${#digest}" -ne 71 ] || ! printf '%s\n' "$digest" | grep -Eqx 'sha256:[0-9a-f]{64}'; then
  echo "no sha256 image digest in $metadata (got '$digest'); was the image exported?" >&2
  exit 1
fi
printf '%s\n' "$digest"
