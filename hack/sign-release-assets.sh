#!/bin/bash
# Write the SBOM, the checksums and the keyless signature of a release.
#
# Usage: hack/sign-release-assets.sh <version> <asset>...
#
# Every asset is a file in one directory (_out/assets in a release job), and the
# three files below are written next to them:
#
#   cozystack-sbom.spdx.json               SPDX SBOM, written by syft, of the
#                                          committed source tree and of the
#                                          cozypkg binaries in the cozypkg-*.tar.gz
#                                          assets. CLOMonitor's sbom check looks
#                                          for "sbom" in a release asset's name.
#   cozystack-checksums.txt                SHA-256 of every asset and of the SBOM,
#                                          in sha256sum format.
#   cozystack-checksums.txt.sigstore.json  Sigstore bundle of a keyless cosign
#                                          signature over the checksums, made with
#                                          the job's GitHub OIDC identity.
#
# One signature covers every asset through the checksums. The asset list comes
# from the caller, so the checksums name exactly what hack/upload-assets.sh
# uploads, and never one of these three files: promote-rc.yaml downloads the rc's
# copies along with its other assets, and a stable release gets its own.
#
# The bundle is verified before the script returns, with the same certificate
# identity users are told to check in docs/release.md, so a release whose
# signature that check would reject fails here instead of being uploaded.
#
# Needs syft and cosign on PATH, and `id-token: write` in the job.
set -euo pipefail

SBOM=cozystack-sbom.spdx.json
CHECKSUMS=cozystack-checksums.txt
BUNDLE=cozystack-checksums.txt.sigstore.json

# The workflows that publish releases: tags.yaml for a pre-release, promote-rc.yaml
# for a stable one. docs/release.md shows users the same pattern.
IDENTITY_REGEXP='^https://github\.com/cozystack/cozystack/\.github/workflows/(tags|promote-rc)\.yaml@'
OIDC_ISSUER=https://token.actions.githubusercontent.com

if [ "$#" -lt 2 ]; then
  echo "usage: $0 <version> <asset>..." >&2
  exit 2
fi
version=$1
shift

dir=$(dirname "$1")
names=()
for asset in "$@"; do
  if [ ! -f "$asset" ]; then
    echo "$0: asset not found: $asset" >&2
    exit 1
  fi
  if [ "$(dirname "$asset")" != "$dir" ]; then
    echo "$0: $asset is not in $dir, where the other assets are" >&2
    exit 1
  fi
  name=$(basename "$asset")
  case $name in
    "$SBOM" | "$CHECKSUMS" | "$BUNDLE")
      echo "$0: $name is written by this script and cannot be an input" >&2
      exit 1
      ;;
  esac
  names+=("$name")
done

stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT

# The tree being released, as committed: git archive leaves out _out/, the
# promote job's .release-tooling checkout and anything else the runner left in
# the work tree.
mkdir "$stage/source"
git archive HEAD | tar -x -C "$stage/source"
for name in "${names[@]}"; do
  case $name in
    cozypkg-*.tar.gz)
      mkdir "$stage/${name%.tar.gz}"
      tar -xzf "$dir/$name" -C "$stage/${name%.tar.gz}"
      ;;
  esac
done

syft scan "dir:$stage" \
  --quiet \
  --source-name cozystack \
  --source-version "$version" \
  --output "spdx-json=$dir/$SBOM"

(cd "$dir" && sha256sum -- "${names[@]}" "$SBOM") > "$dir/$CHECKSUMS"

cosign sign-blob --yes \
  --bundle "$dir/$BUNDLE" \
  "$dir/$CHECKSUMS"

cosign verify-blob \
  --bundle "$dir/$BUNDLE" \
  --certificate-identity-regexp "$IDENTITY_REGEXP" \
  --certificate-oidc-issuer "$OIDC_ISSUER" \
  "$dir/$CHECKSUMS"
