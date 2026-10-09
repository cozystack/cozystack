#!/bin/sh
# Print the GHCR container packages the retention workflow sweeps, one per line.
#
# Usage: retention-packages.sh <published-tree-dir>
#
# GET /orgs/{org}/packages?package_type=container answers "400 Invalid
# argument" to the app installation token the workflow uses, while the
# per-package versions endpoints accept it, so the org cannot be listed. The
# names come instead from the published nightly packages tree: it is the tree
# hack/nightly-mirror.sh copied images for, so it names every component image
# that receives dated nightly tags. The committed tree is not a substitute — an
# image whose ref is digest-pinned only by the build is mirrored nightly yet
# invisible there.
#
# A wrong list fails quietly (a package left out is simply never pruned), so a
# tree that does not even reference the packages artifact is refused rather than
# read as "fewer packages". A component removed from the tree drops out too and
# keeps whatever dated nightlies it had; it receives no new ones.
set -eu

TREE="${1:?usage: retention-packages.sh <published-tree-dir>}"
DST_REGISTRY="ghcr.io/cozystack/cozystack"

[ -d "$TREE" ] || { echo "retention-packages: '$TREE' is not a directory" >&2; exit 1; }
yq --version 2>&1 | grep -q mikefarah || { echo "retention-packages: yq (mikefarah) is required" >&2; exit 1; }

# shellcheck source=hack/lib/image-refs.sh
. "$(dirname "$0")/lib/image-refs.sh"

# Package names keep the namespace under the org: ghcr.io/cozystack/cozystack/x
# is the package "cozystack/x".
package_names() {
  collect_image_refs "$TREE" | while IFS= read -r ref; do
    repo="${ref%@*}"
    case "$repo" in
      "${DST_REGISTRY}/"*) ;;
      *) continue ;;
    esac
    img="${repo##*/}"
    repo="${repo%/*}/${img%:*}"
    printf '%s\n' "${repo#"${DST_REGISTRY%/*}/"}"
  done
}
names="$(package_names)"

printf '%s\n' "$names" | grep -qx 'cozystack/cozystack-packages' || {
  echo "retention-packages: '$TREE' does not reference ${DST_REGISTRY}/cozystack-packages; not a published tree" >&2
  exit 1
}

# Published by the nightly workflow alongside the tree, never referenced by it.
printf '%s\n%s\n%s\n' "$names" cozystack/cozy-installer cozystack/cozystack-nocloud | sort -u
