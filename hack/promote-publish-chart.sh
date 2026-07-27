#!/bin/sh
# Publish the stable cozy-installer OCI chart for a promoted release.
#
# The retag step (hack/promote-retag.sh) covers component images and the
# cozystack-packages artifact, but cozy-installer is a separate OCI artifact
# referenced from no values.yaml, so the documented install path
# (`helm upgrade --install cozystack oci://…/cozy-installer --version X.Y.Z`)
# 404s with "manifest unknown" unless the chart is published here.
#
# No rebuild: the chart is packaged from the CURRENT TREE, which is expected to
# be the promoted stable tree (the release-X.Y.Z merge commit, or the vX.Y.Z tag
# when recovering) — it already pins the retagged images and artifact by digest.
# Only the chart's version, appVersion and platformVersion default are stamped.
#
# Usage: hack/promote-publish-chart.sh <stable-version>
#   <stable-version>  e.g. v1.6.0 (the tag being promoted to; prereleases are
#                     rejected, matching finalize, which skips the registry
#                     side effects for an rc/alpha/beta)
#
# Environment:
#   REGISTRY      Release registry. Default mirrors hack/promote-retag.sh and
#                 hack/common-envs.mk — NOT the OCIR build registry.
#   MOVE_LATEST=1 Also repoint cozy-installer:latest. OFF by default, same
#                 contract as hack/promote-retag.sh: :latest tracks the newest
#                 stable, so a patch on an older line must not drag it back.
#   FORCE_CHART=1 Republish even though a chart is already published at this
#                 version with DIFFERENT contents. Break-glass only.
#
# Re-runnable by construction. `helm package` is not byte-reproducible — tar and
# gzip metadata differ between runs — so an unconditional re-push would mint a
# new digest for an already-published version and move the stable chart tag off
# the bytes users pulled. Instead an existing chart is pulled and compared by
# extracted file contents: identical is a no-op, different is a hard failure
# unless FORCE_CHART says otherwise. Together with promote-retag.sh's write-once
# image tags, that makes re-running the whole registry step after a partial
# failure safe.
#
# Requires: yq (mikefarah), helm, skopeo, and a registry login already done.
set -eu

TAG="${1:?usage: promote-publish-chart.sh <stable-version>}"

case "$TAG" in
  v*.*.*) ;;
  *) echo "'$TAG' must be a stable version tag of the form vX.Y.Z" >&2; exit 1 ;;
esac
# A prerelease never reaches the registry side effects (finalize gates them on a
# dash-free tag), so a chart published under an rc version here would be an
# artifact no release process produces. Refuse rather than invent one.
case "$TAG" in
  *-*) echo "'$TAG' is a prerelease; the stable chart is published only for vX.Y.Z" >&2; exit 1 ;;
esac

REGISTRY="${REGISTRY:-ghcr.io/cozystack/cozystack}"
MOVE_LATEST="${MOVE_LATEST:-0}"
FORCE_CHART="${FORCE_CHART:-0}"

command -v yq >/dev/null || { echo "yq (mikefarah) is required" >&2; exit 1; }
# The stamp below uses mikefarah syntax; reject python-yq and other variants
# (mirrors hack/promote-retag.sh and the build-deps check in the Makefile).
yq --version 2>&1 | grep -q mikefarah || { echo "yq (mikefarah) is required" >&2; exit 1; }
command -v helm >/dev/null   || { echo "helm is required" >&2; exit 1; }
command -v skopeo >/dev/null || { echo "skopeo is required" >&2; exit 1; }

STABLE_VERSION="${TAG#v}"
CHART_REF="${REGISTRY}/cozy-installer"

WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT
mkdir -p "$WORKDIR/fresh" "$WORKDIR/published" "$WORKDIR/fresh-tree" "$WORKDIR/published-tree"

# Bake the correct platform version into the chart's DEFAULT values so the
# primary `helm --install --version X.Y.Z` path reports the stable version in
# telemetry — the operator image itself still carries the rc version baked at
# build time, and this env override is what corrects it. Done on the working
# tree only (never committed): committing it would leak platformVersion=vX.Y.Z
# onto main/the release line when the promote PR merges, mis-stamping the next
# rc built from that branch.
export STABLE_VERSION
yq -i '.cozystackOperator.platformVersion = "v" + strenv(STABLE_VERSION)' \
  packages/core/installer/values.yaml

helm package packages/core/installer \
  --version "$STABLE_VERSION" --app-version "$STABLE_VERSION" \
  --destination "$WORKDIR/fresh" >/dev/null
PKG="$WORKDIR/fresh/cozy-installer-${STABLE_VERSION}.tgz"
[ -f "$PKG" ] || { echo "helm package did not produce $PKG" >&2; exit 1; }

# Is this version already published? `--raw` is load-bearing: a Helm OCI artifact
# is not a container image, and the image-shaped `skopeo inspect` rejects it on
# media type. Fetching the manifest raw is media-type agnostic, so this probe
# works for charts and would work for any other artifact type.
#
# The probe must fail CLOSED. skopeo exits 1 for everything — a genuinely absent
# manifest, a 403, an expired token, a DNS failure, a registry 5xx — so treating
# any non-zero exit as "not published yet" would turn a transient network error
# into an unconditional `helm push`, silently bypassing the content comparison
# below and moving the stable chart tag onto freshly packaged bytes. That is the
# opposite of what this script exists to guarantee, and it would happen under
# exactly the flaky conditions that cause a promotion to be re-run in the first
# place. So classify the failure: only an explicitly missing manifest (or a
# repository that does not exist yet, which is the first-ever push) counts as
# absent; anything else aborts with the registry's own diagnostic.
PROBE_LOG="$WORKDIR/probe.log"
PROBE_RC=0
skopeo inspect --raw "docker://${CHART_REF}:${STABLE_VERSION}" \
  >/dev/null 2>"$PROBE_LOG" || PROBE_RC=$?

if [ "$PROBE_RC" -eq 0 ]; then
  PUBLISHED=1
elif grep -qiE 'manifest unknown|manifest not found|name unknown|repository name not known' \
  "$PROBE_LOG"; then
  PUBLISHED=0
else
  echo "::error::Could not determine whether cozy-installer:${STABLE_VERSION} is already published (skopeo exit ${PROBE_RC})." >&2
  echo "::error::Refusing to push blind — a push here would overwrite a published chart. Fix the registry access and re-run." >&2
  cat "$PROBE_LOG" >&2
  exit 1
fi

if [ "$PUBLISHED" -eq 1 ]; then
  helm pull "oci://${CHART_REF}" \
    --version "$STABLE_VERSION" --destination "$WORKDIR/published" >/dev/null
  tar -xzf "$PKG" -C "$WORKDIR/fresh-tree"
  tar -xzf "$WORKDIR/published/cozy-installer-${STABLE_VERSION}.tgz" -C "$WORKDIR/published-tree"

  REPORT="$WORKDIR/chart.diff"
  if diff -qr "$WORKDIR/fresh-tree/cozy-installer" \
    "$WORKDIR/published-tree/cozy-installer" >"$REPORT" 2>&1; then
    echo "cozy-installer:${STABLE_VERSION} is already published with identical contents; skipping push."
  else
    DIFF_STATUS=$?
    # diff exits 1 for "files differ" and >1 for a real error (unreadable path,
    # missing tree). Only the former is a content decision; anything else means
    # the comparison itself did not happen and must not be waved through.
    if [ "$DIFF_STATUS" -ne 1 ]; then
      echo "::error::Unable to compare the published cozy-installer:${STABLE_VERSION} with the tree." >&2
      cat "$REPORT" >&2
      exit "$DIFF_STATUS"
    fi
    echo "Published cozy-installer:${STABLE_VERSION} differs from the chart packaged from this tree:" >&2
    cat "$REPORT" >&2
    if [ "$FORCE_CHART" = "1" ]; then
      echo "::warning::FORCE_CHART=1; overwriting the published cozy-installer:${STABLE_VERSION}." >&2
      helm push "$PKG" "oci://${REGISTRY}"
    else
      echo "::error::Refusing to overwrite published chart bytes. Review the files above, then re-run with FORCE_CHART=1 to republish deliberately." >&2
      exit 1
    fi
  fi
else
  echo "cozy-installer:${STABLE_VERSION} is not published; pushing."
  helm push "$PKG" "oci://${REGISTRY}"
fi

# cozy-installer:latest tracks the newest stable, matching the component images'
# :latest and the release's `latest` flag — never moved by a patch on an older
# line. Unlike the versioned tag this one is mutable by design, so it is simply
# re-pointed at the versioned tag rather than compared.
if [ "$MOVE_LATEST" = "1" ]; then
  skopeo copy --multi-arch all \
    "docker://${CHART_REF}:${STABLE_VERSION}" \
    "docker://${CHART_REF}:latest"
fi
