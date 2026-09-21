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
#   - packages/core/talos and packages/core/installer: owned by dedicated jobs
#     (the input-gated build-talos job / the finalize installer build), so their
#     own files are never overlaid. A successful Talos job also adds its actual
#     output package, packages/extra/bootbox, to BUILT_JSON at the call site.
#   - vendored charts/ subtrees: upstream chart values we do not build.
#
# Surgical + self-validating: a file is overlaid only when every line that
# differs from the current-main version is image-reference-bearing. The repo's
# committed refs use the release registry (ghcr) while the artifact uses the CI
# registry (OCIR), so refs differ in registry host / tag / digest — and charts
# split those across separate lines (`repository:`, `tag:`, `digest:`), not all
# of which carry '@sha256:'. So a changed line counts as image-related if it
# carries '@sha256:', its key is image/repository/registry/tag/digest, its key
# ends in `image`/`Image` and its value holds a repository path, or it is a
# `--…-image=` arg (see the img_line comment for why each). A blank line is not
# a change: `yq -i`, which every `make image` uses to stamp the ref, drops one.
# If any OTHER line differs — the PR branched from a
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
# is a split ref key (image/repository/registry/tag/digest), a prefixed *Image
# key holding a repository path, or a `--…-image=` arg.
#
# The prefixed-key branch exists because a chart that renders several images
# names the extra ones after what they are for — chBackupClientImage,
# rabbitmqBackupClientImage, redisBackupClientImage. Those used to pass only
# through the `@sha256:` branch, so one pinned by tag alone read as a config
# change and cost its WHOLE file the overlay: that is how
# backupstrategy-controller kept serving a release image two months older than
# the tree in every lane that did not rebuild it (#4257).
#
# Two things keep that branch narrow, and neither is the trailing `:`. The
# `[A-Za-z]+` demands a letter BEFORE `image`, which a key that begins with it
# has nothing to put there — that, not the colon, is why `imagePullPolicy:` and
# `imagePullSecrets:` do not match, with the colon or without it. The colon
# earns its place on the other side: it is where the value scan starts, so
# `clientImage: "ghcr.io/…"` stops matching if it goes, which is what the test
# named "a suffixed image key is a ref, not a config change" holds. And the value has to contain a `/`, which a repository path always
# does and an operator-settable knob named after an image does not: `vddkImage: ""`
# (core/platform, migration-controller) is configuration the build never
# stamps, and taking it from the artifact in silence is the opposite of what
# this script is for. The exact-key branch keeps no such requirement, because
# `tag:` and `digest:` legitimately hold values with no slash in them.
img_line='(@sha256:|^[[:space:]]*(- )?(image|repository|registry|tag|digest):|^[[:space:]]*(- )?[A-Za-z]+[Ii]mage:[[:space:]]*"?[^"[:space:]]*/|--[A-Za-z-]*image=)'

overlaid=0
same=0
skipped=0
drift=0
drift_files=""
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

  # Blank lines are dropped before the test. `yq -i` does not preserve them:
  # stamping a digest re-emits the document, and a blank line separating two
  # top-level keys does not come back. The stamp therefore changes a file the
  # PR never touched, on a line carrying no configuration at all, and the whole
  # file loses its overlay for it. That is not a corner case -- on the run
  # behind #4265 it was the entire remaining drift list: core/platform,
  # cozystack-api, dashboard, kubeovn-webhook and linstor-gui each differed
  # from the artifact by one blank line and their own ref, nothing else.
  if diff "$cur" "$new" | sed -n 's/^[<>] //p' \
      | grep -vE '^[[:space:]]*$' | grep -qvE "$img_line"; then
    echo "drift (non-ref change) in $cur -> keeping committed ref"
    drift=$((drift + 1))
    # Name the PACKAGE, not the directory the file happens to sit in: an
    # images/*.tag drift would otherwise read as "<pkg>/images", which is not
    # something anyone can go and look at.
    d=$(dirname "${cur#packages/}")
    drift_files="$drift_files ${d%/images}"
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

# Keeping the committed ref is the safe answer to a config that does not match
# the artifact, but it is not a neutral one: that component then runs its last
# RELEASE image in a lane whose job is to exercise the tree under review. Said
# once per file among hundreds of overlay lines, it reads as bookkeeping. Said
# once at the end, with the packages named, it is the first thing to check when
# a suite fails on behaviour the diff plainly contains — which is what #4257
# cost three unrelated PRs, one round each.
if [ "$drift" -gt 0 ]; then
  drift_pkgs=$(echo "$drift_files" | tr ' ' '\n' | grep -v '^$' | sort -u)
  printf '::warning title=Image overlay drift::%s package(s) kept committed release refs, so they run release images in this lane rather than current main: %s\n' \
    "$(printf '%s\n' "$drift_pkgs" | wc -l | tr -d ' ')" \
    "$(printf '%s' "$drift_pkgs" | tr '\n' ' ' | sed 's/ $//')"
fi
