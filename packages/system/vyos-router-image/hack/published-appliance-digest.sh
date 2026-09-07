#!/usr/bin/env bash
# Print the digest of an already-published appliance manifest, or exit 1.
#
# This is the cache lookup that keeps the VyOS live-build off the critical path
# of `make build`. The appliance is content-keyed by VYOS_VERSION, which the
# pins in ../Makefile make a function of VYOS_BUILD_REF, so a hit means "this
# exact pin was already built and pushed" and the caller can stamp the digest
# instead of spending ~15 minutes rebuilding it from a floating apt mirror.
#
# Exit 1 (with nothing on stdout) is the "build it" answer. Two cases produce it
# and they are deliberately indistinguishable to the caller:
#
#   1. No such manifest — a new or moved pin. This is the case the whole
#      mechanism exists for: the first build after a pin bump pays for it once.
#   2. OCI_EXPORT_DIR is set — a fork PR's artifact-export build. Reusing a
#      published digest there would write NO oci archive, and the fork build's
#      upload uses `if-no-files-found: error`, so the job would fail on a
#      missing file rather than on anything real (#3257). An export build must
#      always produce its archive, so it always builds.
#
# A registry that cannot be reached is also case 1. That is the safe direction:
# a false miss costs one rebuild, a false hit would stamp a digest nothing can
# pull.
set -euo pipefail

REF="${1:?usage: published-appliance-digest.sh <image-ref>}"

# An export build must produce an archive; see case 2 above.
if [ -n "${OCI_EXPORT_DIR:-}" ]; then
  exit 1
fi

# `imagetools inspect` resolves a tag to its manifest digest and needs only
# registry READ, which is anonymous on the registry this publishes to. buildx is
# already set up in every job that runs this recipe, so this adds no tooling.
digest="$(docker buildx imagetools inspect --format '{{.Manifest.Digest}}' "$REF" 2>/dev/null || true)"

# Guard the shape rather than trusting a zero exit: a proxy or a misconfigured
# registry can answer 200 with something that is not a digest, and stamping that
# would produce an unpullable reference that only fails at CDI import time.
case "$digest" in
  sha256:[0-9a-f]*)
    [ "${#digest}" -eq 71 ] || exit 1
    printf '%s\n' "$digest"
    ;;
  *)
    exit 1
    ;;
esac
