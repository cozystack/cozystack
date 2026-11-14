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
# left on its committed ref (safe degradation, logged, never fatal), except
# that a bare first-party ref in it, one with no digest, still takes the
# artifact's ref (see replace_bare_refs). CI checks out the pull_request merge
# commit (main + PR), so for an unbuilt file the config matches main as of
# that merge commit. It is fixed when the run starts, so a rerun, or a PR not
# pushed to since, can be older than the artifact and drift on files it never
# touched.
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

# ...and, in an images/*.tag file only, a bare reference: a whole line that is
# one ref with no YAML key around it, which is the entire format of that file
# (hack/lib/image-refs.sh: "a plain file holding one ref"). Every branch above
# needs either a key or an `@sha256:`, and a .tag file carries neither until a
# build stamps it, so an unstamped one is classified as a non-ref change, the
# drift guard keeps the committed ref, and the overlay never replaces the
# placeholder it exists to replace. 17 of the 19 .tag files in the tree pass on
# `@sha256:` alone, which is why this went unnoticed; the exception that proves
# it is packages/extra/seaweedfs/images/seaweedfs-cosi-driver.tag, digest-less
# but third-party, so build-main never rebuilds it, the artifact copy is
# byte-identical and this test is never reached.
#
# Scoped to *.tag because only there is a whole line known to be a reference and
# nothing else. An unkeyed line in a values.yaml is genuinely unclassifiable and
# must keep counting as drift. hack/promote-rewrite-tags.sh carries a second
# match branch for this same shape and for this same reason.
bare_ref_line='^[^[:space:]#][^[:space:]]*/[^[:space:]]+$'

# A drifted file keeps its committed refs. For a ref pinned by digest that is an
# older image which pulls and matches the PR's config, so it is the safe answer.
# A first-party ref with no digest is not: docs/agents/image-refs.md forbids it
# except as the first commit of a new image, whose tag was never pushed, and
# keeping that makes the component fail to pull. The merge commit CI checks out
# is fixed when the run starts, so a PR whose merge commit predates a config
# change to such a file drifts on it, and a new image fails every such PR until
# a release stamps its ref (#5328).
#
# replace_bare_refs <cur> <new> rewrites, in <cur> only, each line whose value is
# a bare first-party ref (ghcr.io/cozystack/cozystack/<name>:<tag>, no digest)
# under an image key, taking the line from <new> that has the same key and
# indentation, an @sha256: digest and the same first-party image, i.e. a path
# ending in /cozystack/<name>. Exactly one such line must exist; two are
# ambiguous and none means build-main did not stamp it. Every other line of
# <cur> stays as the PR has it.
#
# The trade-off is deliberate. A bare first-party ref that does exist (an older
# release tag or a moving `latest`, #4264) also takes main's image, so it runs
# with the PR's older config rather than as committed. It is the same component
# built from the tree, and a digest-less ref is not reproducible anyway; the
# alternative is a ref that may not pull at all. Third-party refs are never
# touched: main can bump one alongside its config, and the committed pair is
# the one known to work together. The split shape (`repository:` + `tag:`
# without `digest:`) is not handled, nor is a single-quoted value or one
# followed by a `# comment`; such a line stays as committed, and no package
# writes a bare first-party ref that way today. Prints nothing and fails when
# the file could not be rewritten, so the caller counts it as failed.
#
# Prints the number of lines replaced.
replace_bare_refs() {
  tmp="$1.overlay-bare.$$"
  n=$(awk -v newf="$2" -v out="$tmp" '
    function key_prefix(s) {
      if (match(s, /^[[:space:]]*(- )?([A-Za-z]+[Ii]mage|image):[[:space:]]*/)) return substr(s, 1, RLENGTH)
      return ""
    }
    function ref_value(s, p,   v) {
      v = substr(s, length(p) + 1)
      sub(/[[:space:]]+$/, "", v)
      gsub(/"/, "", v)
      return v
    }
    function image_name(v) {
      sub(/@.*/, "", v)
      sub(/.*\//, "", v)
      sub(/:.*/, "", v)
      return v
    }
    BEGIN {
      while ((getline l < newf) > 0) {
        p = key_prefix(l)
        if (p == "" || index(l, "@sha256:") == 0) continue
        v = ref_value(l, p)
        name = image_name(v)
        if (index(v, "/cozystack/" name ":") == 0) continue
        k = p SUBSEP name
        cnt[k]++
        line[k] = l
      }
      close(newf)
    }
    {
      p = key_prefix($0)
      if (p != "") {
        v = ref_value($0, p)
        if (v ~ /^ghcr\.io\/cozystack\/cozystack\/[^@[:space:]\/]+:[^@[:space:]\/]+$/) {
          k = p SUBSEP image_name(v)
          if (cnt[k] == 1) { print line[k] > out; r++; next }
        }
      }
      print > out
    }
    END { close(out); print r + 0 }
  ' "$1")
  case "$n" in
    ''|*[!0-9]*) rm -f "$tmp"; return 1 ;;
    0) rm -f "$tmp" ;;
    *) mv "$tmp" "$1" || { rm -f "$tmp"; return 1; } ;;
  esac
  echo "$n"
}

overlaid=0
same=0
bare=0
bare_files=""
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
  line_pat="$img_line"
  case "$cur" in *.tag) line_pat="($img_line)|($bare_ref_line)" ;; esac

  if diff "$cur" "$new" | sed -n 's/^[<>] //p' \
      | grep -vE '^[[:space:]]*$' | grep -qvE "$line_pat"; then
    drift=$((drift + 1))
    r=0
    case "$cur" in
      *.yaml)
        if ! r=$(replace_bare_refs "$cur" "$new"); then
          echo "WARN: rewriting bare refs failed for $cur (keeping committed refs)"
          failed=$((failed + 1))
          r=0
        fi
        ;;
    esac
    if [ "$r" -eq 0 ]; then
      echo "drift (non-ref change) in $cur -> keeping committed ref"
    else
      echo "drift (non-ref change) in $cur -> keeping committed refs except bare first-party ones"
      echo "bare first-party ref in $cur -> current-main ($r line(s)); its other refs stay committed"
      bare=$((bare + r))
      d=$(dirname "${cur#packages/}")
      bare_files="$bare_files $d"
    fi
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

echo "Overlay current-main images: overlaid=$overlaid same=$same skipped(rebuilt/owned/edited)=$skipped drift=$drift bare-refs=$bare failed=$failed"

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
# Among those, a package whose bare first-party ref took main's image runs that
# image with the PR's older config, not its committed ref; name it separately
# so the warning above does not read as the whole story for it.
if [ "$bare" -gt 0 ]; then
  bare_pkgs=$(echo "$bare_files" | tr ' ' '\n' | grep -v '^$' | sort -u)
  printf '::warning title=Image overlay bare refs::%s drifted package(s) took current-main images for bare first-party refs, with the config committed in this PR: %s\n' \
    "$(printf '%s\n' "$bare_pkgs" | wc -l | tr -d ' ')" \
    "$(printf '%s' "$bare_pkgs" | tr '\n' ' ' | sed 's/ $//')"
fi
