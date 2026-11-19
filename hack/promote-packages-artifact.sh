#!/bin/sh
# Publish the tag-rewritten packages tree as a stable-candidate Flux OCI
# artifact, then pin the installer values to the candidate's immutable digest.
# Container images are not rebuilt here: their digests were baked by the rc and
# remain unchanged. Only the declarative packages artifact is re-serialized so
# the stable install consumes the stable tag strings committed by promotion.
#
# Usage: hack/promote-packages-artifact.sh <stable-version> <rc-tag> <source-sha> [root]
#   <stable-version>  X.Y.Z (without a leading v)
#   <rc-tag>          vX.Y.Z-rc.N
#   <source-sha>      commit SHA containing the rewritten tree being published
#   [root]            packages tree to publish; defaults to packages
#
# Environment:
#   REGISTRY          destination registry; defaults to the release GHCR path
#   SOURCE_URL        OCI source annotation; defaults to this repository
#   PROMOTION_ID      unique GitHub run-id/run-attempt pair, e.g. 123456789-2
#   EXPECTED_PACKAGES_REPOSITORY  exact trusted OCI repository the rc tree must
#                                 already pin; defaults to the public Cozystack
#                                 release repository
set -eu

STABLE_VERSION="${1:?usage: promote-packages-artifact.sh <stable-version> <rc-tag> <source-sha> [root]}"
RC_TAG="${2:?usage: promote-packages-artifact.sh <stable-version> <rc-tag> <source-sha> [root]}"
SOURCE_SHA="${3:?usage: promote-packages-artifact.sh <stable-version> <rc-tag> <source-sha> [root]}"
ROOT="${4:-packages}"
REGISTRY="${REGISTRY:-ghcr.io/cozystack/cozystack}"
SOURCE_URL="${SOURCE_URL:-https://github.com/cozystack/cozystack}"
PROMOTION_ID="${PROMOTION_ID:?PROMOTION_ID must be the GitHub run-id/run-attempt pair}"

# EXPECTED_PACKAGES_REPOSITORY and PACKAGES_DIGEST_REF_PATTERN, shared with
# hack/verify-promoted-packages.sh so the preflight below cannot drift from the
# guard it is bringing forward.
# shellcheck source=hack/lib/promoted-packages.sh
. "$(dirname "$0")/lib/promoted-packages.sh"

printf '%s\n' "$STABLE_VERSION" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' \
  || { echo "stable-version '$STABLE_VERSION' must match X.Y.Z" >&2; exit 1; }
printf '%s\n' "$RC_TAG" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$' \
  || { echo "rc-tag '$RC_TAG' must match v${STABLE_VERSION}-rc.N" >&2; exit 1; }
[ "${RC_TAG%-rc.*}" = "v${STABLE_VERSION}" ] \
  || { echo "rc-tag '$RC_TAG' must match v${STABLE_VERSION}-rc.N" >&2; exit 1; }
printf '%s\n' "$SOURCE_SHA" | grep -Eq '^[0-9a-f]{40}$' \
  || { echo "source-sha '$SOURCE_SHA' must be a 40-character lowercase hex SHA" >&2; exit 1; }
printf '%s\n' "$PROMOTION_ID" | grep -Eq '^[0-9]+-[0-9]+$' \
  || { echo "PROMOTION_ID '$PROMOTION_ID' must match <run-id>-<run-attempt>" >&2; exit 1; }
[ -d "$ROOT" ] || { echo "root '$ROOT' is not a directory" >&2; exit 1; }
VALUES="$ROOT/core/installer/values.yaml"
[ -f "$VALUES" ] \
  || { echo "root '$ROOT' has no core/installer/values.yaml" >&2; exit 1; }

command -v flux >/dev/null || { echo "flux is required" >&2; exit 1; }
command -v yq >/dev/null   || { echo "yq (mikefarah) is required" >&2; exit 1; }
yq --version 2>&1 | grep -q mikefarah \
  || { echo "yq (mikefarah) is required" >&2; exit 1; }

# Preflight the rc tree's own pin against the checks the candidate will face.
# The artifact is pushed BEFORE the rewrite at the bottom of this script, so it
# carries this values.yaml verbatim and verify-promoted-packages.sh reads these
# two fields back out of it as the candidate's rc baseline. Left unchecked, a
# tag-form ref or an untrusted repository is therefore not caught at dispatch
# but in the promote PR gate, once the artifact, the staging branch and the
# draft release all exist. Refuse now, while nothing has been written.
rc_source_url="$(yq -e -r '.cozystackOperator.platformSourceUrl' "$VALUES")"
rc_source_ref="$(yq -e -r '.cozystackOperator.platformSourceRef' "$VALUES")"
[ "$rc_source_url" = "$EXPECTED_PACKAGES_REPOSITORY" ] \
  || { echo "rc tree's platformSourceUrl '$rc_source_url' must equal trusted repository '$EXPECTED_PACKAGES_REPOSITORY'" >&2; exit 1; }
printf '%s\n' "$rc_source_ref" | grep -Eq "$PACKAGES_DIGEST_REF_PATTERN" \
  || { echo "rc tree's platformSourceRef '$rc_source_ref' is not an immutable digest" >&2; exit 1; }

REPOSITORY="oci://${REGISTRY}/cozystack-packages"
CANDIDATE_TAG="promotion-v${STABLE_VERSION}-from-${RC_TAG}-run-${PROMOTION_ID}"
REVISION="promotion-v${STABLE_VERSION}-from-${RC_TAG}@sha1:${SOURCE_SHA}"

# Every workflow run/attempt gets a new tag, so a retry can never move a tag or
# orphan the previous manifest. --reproducible and the source commit keep the
# artifact's own metadata deterministic and point at a tree that actually
# contains the rewritten packages content (before its impossible self-pin).
result="$(flux push artifact "${REPOSITORY}:${CANDIDATE_TAG}" \
  --path="$ROOT" \
  --source="$SOURCE_URL" \
  --revision="$REVISION" \
  --reproducible \
  --output json)"

digest="$(printf '%s\n' "$result" | yq -e -r '.digest')"
printf '%s\n' "$digest" | grep -Eq '^sha256:[0-9a-f]{64}$' \
  || { echo "flux returned invalid artifact digest '$digest'" >&2; exit 1; }

# The artifact cannot contain its own digest: writing the digest changes the
# artifact and therefore creates another digest. The separately published
# installer chart carries this pin; the embedded old pin is runtime-inert and
# is the sole normalized difference accepted by verify-promoted-packages.sh.
export REPOSITORY digest
yq -i '.cozystackOperator.platformSourceUrl = strenv(REPOSITORY) |
  .cozystackOperator.platformSourceRef = "digest=" + strenv(digest)' \
  "$VALUES"

echo "Published ${REPOSITORY}:${CANDIDATE_TAG}@${digest} and pinned installer values."
