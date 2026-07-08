#!/bin/sh
# Overlay current-main image references onto packages a PR did NOT rebuild.
#
# Source of truth: the cozystack-packages:main OCI artifact build-main.yaml
# already publishes (`make build` -> packages/core/installer image-packages does
# `flux push artifact oci://$REGISTRY/cozystack-packages:main --path=packages`).
# That artifact is the ENTIRE current-main packages tree with every image
# reference digest-pinned to the images build-main just pushed.
#
# We walk EVERY ref-bearing file (values.yaml + images/*.tag) in that tree and
# overlay the repo's copy when it differs only in image-reference lines. This is
# deliberately driven by the artifact, NOT the per-package build matrix, so it
# covers the WHOLE first-party tree — apps/, system/, core/, AND extra/,
# library/, ... For any file build-main did not rebuild, the artifact copy is
# byte-identical to the committed copy and is skipped; only files carrying a
# rebuilt current-main digest differ and get overlaid. So a fix merged to ANY
# first-party image (e.g. the objectstorage-sidecar ref under extra/seaweedfs)
# takes effect in e2e and the installer artifact, instead of the frozen
# last-release refs committed in the repo (e.g. v1.5.0).
#
# Skipped:
#   - units the PR itself rebuilt (BUILT_JSON, the plan job's matrix): their
#     pr-<N>-<sha> refs, already applied in finalize, are authoritative.
#   - packages the PR EDITED (TOUCHED, the plan job's changed-package dirs): the
#     PR's committed refs are its intent (e.g. an upstream image bump in a
#     non-build-unit package), so they must win over the artifact — never overlay.
#   - packages/core/talos and packages/core/installer: rebuilt unconditionally
#     by their dedicated jobs (build-talos / the finalize installer build),
#     which own those files — never overlay them.
#   - vendored charts/ subtrees: upstream chart values we do not build.
#
# Surgical + self-validating: a file is overlaid only when every line that
# differs from the current-main version is image-reference-bearing. The repo's
# committed refs use the release registry (ghcr) while the artifact uses the CI
# registry (OCIR), so refs differ in registry host / tag / digest — and charts
# split those across separate lines (`repository:`, `tag:`, `digest:`), not all
# of which carry '@sha256:'. So a changed line counts as image-related if it
# carries '@sha256:' OR its key is image/repository/registry/tag/digest (or it
# is a `--…-image=` arg). If any OTHER line differs — the PR branched from a
# main whose config for that file differs from the artifact's base — the file is
# left on its committed ref (safe degradation, logged, never fatal). CI checks
# out the pull_request merge commit (current main + PR), so for an unbuilt file
# the config already matches current main and only ref lines differ.
#
# Usage: hack/overlay-main-images.sh <mainpkgs-dir> <built-matrix-json> [touched-pkg-dirs]
#        <mainpkgs-dir>     = extracted cozystack-packages:main tree; its root holds
#                             apps/ system/ core/ extra/ ... (the CONTENTS of
#                             packages/).
#        <built-matrix-json> = JSON array of package dirs the PR rebuilt (skipped).
#        [touched-pkg-dirs]  = whitespace-separated package dirs the PR edited
#                             (no trailing slash); their committed refs are kept.
#        (run from the repo root)
set -eu

MAINPKGS="${1:?usage: overlay-main-images.sh <mainpkgs-dir> <built-matrix-json> [touched-pkg-dirs]}"
MAINPKGS="${MAINPKGS%/}"
BUILT_JSON="${2:-[]}"
# Package dirs the PR itself EDITED (whitespace-separated, no trailing slash).
# Their committed refs ARE the PR's intent — e.g. an upstream image bump in a
# package that is NOT a build unit (keycloak, cert-manager, ingress-nginx, …) —
# so they must win over the artifact. Without this, such an edit differs from the
# artifact only on image-reference lines, the drift check passes, and the overlay
# would silently revert the PR's bump (e2e then tests the stale image). The plan
# job derives this from `git diff --name-only` and passes it via env, not from
# untrusted PR input.
TOUCHED="${3:-}"

if [ ! -d "$MAINPKGS" ]; then
  echo "No current-main packages tree at $MAINPKGS — unbuilt packages keep committed refs"
  exit 0
fi

# Dirs whose files must never be overlaid: the two units owned by dedicated
# jobs, every unit the PR rebuilt, and every package the PR edited (its committed
# refs are the PR's intent). Space-wrapped for whole-token matching.
skip=" packages/core/talos packages/core/installer $(echo "$BUILT_JSON" | tr -d '[]"' | tr ',' ' ') $TOUCHED "

# A changed line is image-reference-bearing if it carries a full ref (@sha256:),
# is a split ref key (image/repository/registry/tag/digest), or a `--…-image=` arg.
img_line='(@sha256:|^[[:space:]]*(- )?(image|repository|registry|tag|digest):|--[A-Za-z-]*image=)'

overlaid=0
same=0
skipped=0
drift=0
failed=0
# Walk the artifact's ref-bearing files, pruning vendored charts/ subtrees.
# `for … in $(find)` (not a pipe) keeps the counters in this shell; package
# paths and filenames carry no spaces (same assumption as hack/build-matrix.sh).
for new in $(find "$MAINPKGS" -type d -name charts -prune -o \
                  \( -name values.yaml -o -name '*.tag' \) -type f -print 2>/dev/null); do
  # Artifact root == contents of packages/, so map back by re-adding the prefix.
  cur="packages/${new#"$MAINPKGS"/}"

  in_skip=0
  for d in $skip; do
    case "$cur" in "$d"/*) in_skip=1; break ;; esac
  done
  if [ "$in_skip" -eq 1 ]; then
    skipped=$((skipped + 1))
    continue
  fi

  [ -f "$cur" ] || continue           # not in the PR tree -> don't introduce it
  cmp -s "$cur" "$new" && { same=$((same + 1)); continue; }

  if diff "$cur" "$new" | sed -n 's/^[<>] //p' | grep -qvE "$img_line"; then
    echo "drift (non-ref change) in $cur -> keeping committed ref"
    drift=$((drift + 1))
    continue
  fi

  if cp "$new" "$cur"; then
    echo "overlay: $cur -> current-main"
    overlaid=$((overlaid + 1))
  else
    echo "WARN: cp failed for $cur (keeping committed ref)"
    failed=$((failed + 1))
  fi
done

echo "Overlay current-main images: overlaid=$overlaid same=$same skipped(rebuilt/owned/edited)=$skipped drift=$drift failed=$failed"
