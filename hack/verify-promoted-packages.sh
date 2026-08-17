#!/bin/sh
# Verify that the packages artifact pinned by a stable promotion is exactly the
# packages tree in the merge commit, apart from the artifact's impossible
# self-reference. This runs before finalize creates any stable git/release tag.
#
# Usage: hack/verify-promoted-packages.sh <stable-version> [root]
#   <stable-version>  X.Y.Z (without a leading v)
#   [root]            release packages tree; defaults to packages
#
# Environment:
#   EXPECTED_PACKAGES_REPOSITORY  exact trusted OCI repository; defaults to
#                                 the public Cozystack release repository
#   VERIFY_PACKAGES_WORKDIR       caller-owned work directory; when set, the
#                                 verifier neither creates nor removes it
set -eu

STABLE_VERSION="${1:?usage: verify-promoted-packages.sh <stable-version> [root]}"
ROOT="${2:-packages}"

# EXPECTED_PACKAGES_REPOSITORY and PACKAGES_DIGEST_REF_PATTERN, shared with the
# publisher so its dispatch-time preflight cannot drift from this guard.
# shellcheck source=hack/lib/promoted-packages.sh
. "$(dirname "$0")/lib/promoted-packages.sh"

printf '%s\n' "$STABLE_VERSION" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' \
  || { echo "stable-version '$STABLE_VERSION' must match X.Y.Z" >&2; exit 1; }
[ -d "$ROOT" ] || { echo "root '$ROOT' is not a directory" >&2; exit 1; }
VALUES="$ROOT/core/installer/values.yaml"
[ -f "$VALUES" ] || { echo "root '$ROOT' has no core/installer/values.yaml" >&2; exit 1; }

command -v flux >/dev/null || { echo "flux is required" >&2; exit 1; }
command -v yq >/dev/null   || { echo "yq (mikefarah) is required" >&2; exit 1; }
yq --version 2>&1 | grep -q mikefarah \
  || { echo "yq (mikefarah) is required" >&2; exit 1; }

source_url="$(yq -e -r '.cozystackOperator.platformSourceUrl' "$VALUES")"
source_ref="$(yq -e -r '.cozystackOperator.platformSourceRef' "$VALUES")"
[ "$source_url" = "$EXPECTED_PACKAGES_REPOSITORY" ] \
  || { echo "platformSourceUrl '$source_url' must equal trusted repository '$EXPECTED_PACKAGES_REPOSITORY'" >&2; exit 1; }
printf '%s\n' "$source_ref" | grep -Eq "$PACKAGES_DIGEST_REF_PATTERN" \
  || { echo "platformSourceRef '$source_ref' is not an immutable digest" >&2; exit 1; }

if [ -n "${VERIFY_PACKAGES_WORKDIR:-}" ]; then
  tmp="$VERIFY_PACKAGES_WORKDIR"
  [ ! -e "$tmp" ] || { echo "VERIFY_PACKAGES_WORKDIR '$tmp' already exists" >&2; exit 1; }
  mkdir -p "$tmp"
else
  tmp="$(mktemp -d)"
  cleanup() {
    case "$tmp" in
      "${TMPDIR:-/tmp}"/*) rm -r -- "$tmp" ;;
      *) echo "refusing to clean unexpected work directory '$tmp'" >&2 ;;
    esac
  }
  trap cleanup EXIT HUP INT TERM
fi
artifact="$tmp/artifact"
mkdir -p "$artifact"
flux pull artifact "${source_url}@${source_ref#digest=}" --output "$artifact"

ARTIFACT_VALUES="$artifact/core/installer/values.yaml"
[ -f "$ARTIFACT_VALUES" ] \
  || { echo "promoted artifact has no core/installer/values.yaml" >&2; exit 1; }

# The candidate was pushed before its own digest was written into installer
# values, so its embedded platformSourceRef is the immutable rc artifact it was
# derived from. Pull that baseline now; comparing its normalized refs below is
# what proves promotion changed tag strings rather than container bytes.
rc_source_url="$(yq -e -r '.cozystackOperator.platformSourceUrl' "$ARTIFACT_VALUES")"
rc_source_ref="$(yq -e -r '.cozystackOperator.platformSourceRef' "$ARTIFACT_VALUES")"
[ "$rc_source_url" = "$EXPECTED_PACKAGES_REPOSITORY" ] \
  || { echo "candidate's original platformSourceUrl '$rc_source_url' must equal trusted repository '$EXPECTED_PACKAGES_REPOSITORY'" >&2; exit 1; }
printf '%s\n' "$rc_source_ref" | grep -Eq "$PACKAGES_DIGEST_REF_PATTERN" \
  || { echo "candidate's original platformSourceRef '$rc_source_ref' is not an immutable digest" >&2; exit 1; }

# Promotion must have rewritten every version-line image tag in the artifact,
# not merely in git. Keep the match scoped to image-reference positions so an
# unrelated dependency version or historical prose cannot abort a release.
stable_esc="$(printf '%s' "$STABLE_VERSION" | sed 's/\./\\./g')"
scan_err="$tmp/rc-scan-err"
leftovers="$(find "$artifact" -type f ! -path '*/charts/*' ! -name '*.md' \
  -exec grep -lE -- \
  "(image|repository|tag)[\"']?:[^#]*${stable_esc}-rc\.[0-9]+|${stable_esc}-rc\.[0-9]+@sha256:" \
  {} + 2>"$scan_err" || true)"
# An unreadable file is NOT "a file with no match" — the silent-skip shape
# hack/promote-rewrite-tags.sh refuses by name, and the `|| true` here has to
# stay because `-exec … +` reports the legitimate "nothing matched" as failure
# too. That leaves grep's own diagnostics as the only signal that a file went
# unscanned, so treat any of them as a failed scan. Defence in depth: the
# per-file `cmp` below would catch a leftover rc string anyway, since the
# release tree has none. Cheap enough to not depend on that.
if [ -s "$scan_err" ]; then
  echo "::error::could not scan the promoted artifact for rc image references:" >&2
  cat "$scan_err" >&2
  exit 1
fi
if [ -n "$leftovers" ]; then
  echo "::error::promoted packages artifact still carries rc image references:" >&2
  printf '%s\n' "$leftovers" >&2
  exit 1
fi

# Flux's default archive excludes these file classes and omits symlinks rather
# than following or storing them. Match that established RC artifact view here;
# changing it would be a separate packages-format migration. .build-revision is
# an ignored build nonce generated only by the rc image-packages target; it is
# not consumed at runtime. The installer values are compared separately below
# after normalizing the one impossible self-reference.
#
# This list is a RESTATEMENT of flux's built-in `excludeOCI` set, not a
# derivation of it — the CLI does not expose it — so it is coupled to a flux
# version. That version is pinned as FLUX_VERSION (2.8.6) in all three workflow
# steps that install the toolchain, and the promote-packages contract test pins
# that they agree. If flux ever changes the set, the symptom is a "contain
# different files" failure naming the newly included or excluded class, which
# is loud and lands before any stable name exists; the fix is to update this
# list and FLUX_VERSION together, in that order.
artifact_entries() {
  (
    cd "$1"
    find . -type f \
      ! -name '.gitignore' \
      ! -name '.gitmodules' \
      ! -name '.gitattributes' \
      ! -name '*.jpg' \
      ! -name '*.jpeg' \
      ! -name '*.gif' \
      ! -name '*.png' \
      ! -name '*.wmv' \
      ! -name '*.flv' \
      ! -name '*.tar.gz' \
      ! -name '*.zip' \
      ! -name '.build-revision' \
      ! -path './core/installer/values.yaml' \
      | LC_ALL=C sort
  )
}

artifact_entries "$artifact" > "$tmp/artifact-files"
artifact_entries "$ROOT" > "$tmp/release-files"
if ! cmp -s "$tmp/artifact-files" "$tmp/release-files"; then
  echo "::error::promoted artifact and release tree contain different files:" >&2
  diff -u "$tmp/artifact-files" "$tmp/release-files" >&2 || true
  exit 1
fi

while IFS= read -r rel; do
  rel="${rel#./}"
  artifact_file="$artifact/$rel"
  release_file="$ROOT/$rel"
  if ! cmp -s "$artifact_file" "$release_file"; then
    echo "::error::promoted artifact file differs from release tree: $rel" >&2
    exit 1
  fi
  # Resolve each side to a value first. Written as one `&&`/`||` chain this
  # reads like a symmetric difference and is not one: POSIX gives the two
  # operators equal precedence and left associativity, so `A && B || C && D`
  # groups as `((A && B) || C) && D` and the candidate-executable direction —
  # the one that makes content executable inside the artifact the operator
  # installs — is accepted silently.
  if [ -x "$artifact_file" ]; then artifact_exec=1; else artifact_exec=0; fi
  if [ -x "$release_file" ]; then release_exec=1; else release_exec=0; fi
  if [ "$artifact_exec" -ne "$release_exec" ]; then
    echo "::error::promoted artifact executable bit differs from release tree: $rel" >&2
    exit 1
  fi
done < "$tmp/artifact-files"

normalize_installer() {
  sed -E 's|^([[:space:]]*platformSourceRef:[[:space:]]*).*$|\1digest=sha256:SELF|' "$1"
}
normalize_installer "$ARTIFACT_VALUES" > "$tmp/artifact-installer"
normalize_installer "$VALUES" > "$tmp/release-installer"
if ! cmp -s "$tmp/artifact-installer" "$tmp/release-installer"; then
  echo "::error::promoted artifact installer values differ beyond their self-reference" >&2
  diff -u "$tmp/artifact-installer" "$tmp/release-installer" >&2 || true
  exit 1
fi

# Compare normalized repo@digest sets against the rc artifact as an explicit
# proof that promotion did not change any container bytes. The packages
# artifact's own digest is omitted because that is the intentional declarative
# content change this verification is proving.
# shellcheck source=hack/lib/image-refs.sh
. "$(dirname "$0")/lib/image-refs.sh"
normalized_refs() {
  # Collect into a file rather than piping straight into the loop. As the head
  # of a pipeline the collector's exit status is thrown away — the pipeline
  # reports `sort -u`'s, POSIX sh has no `pipefail` and this script has to stay
  # POSIX — so `set -e` never sees it and a failed or truncated collection
  # becomes a smaller set that the comparison below happily matches. Same
  # fail-open class as the rc-reference scan above, on the one guard that is
  # supposed to prove the container bytes did not move across promotion.
  _nr_raw="$tmp/refs-raw"
  collect_image_refs "$1" > "$_nr_raw"
  # A collection that comes back empty is that same failure wearing a zero exit
  # status: two empty sets compare equal, so the proof passes having examined
  # nothing at all. Every packages tree carries image references — none means
  # the collector understood nothing it was given, not that there was nothing
  # to find.
  [ -s "$_nr_raw" ] \
    || { echo "::error::collected no image references from '$1'; cannot prove the container digests are unchanged" >&2; exit 1; }
  while IFS= read -r raw; do
    [ -n "$raw" ] || continue
    without_digest="${raw%@*}"
    digest="${raw##*@}"
    image="${without_digest##*/}"
    if [ "$without_digest" = "$image" ]; then
      repo="${image%:*}"
    else
      repo="${without_digest%/*}/${image%:*}"
    fi
    case "$repo" in
      */cozystack-packages) continue ;;
    esac
    printf '%s@%s\n' "$repo" "$digest"
  done < "$_nr_raw" | LC_ALL=C sort -u
}
rc_artifact="$tmp/rc-artifact"
mkdir -p "$rc_artifact"
flux pull artifact "${rc_source_url}@${rc_source_ref#digest=}" --output "$rc_artifact"
normalized_refs "$rc_artifact" > "$tmp/rc-refs"
normalized_refs "$artifact" > "$tmp/artifact-refs"
if ! cmp -s "$tmp/rc-refs" "$tmp/artifact-refs"; then
  echo "::error::promotion changed the container repository/digest set" >&2
  diff -u "$tmp/rc-refs" "$tmp/artifact-refs" >&2 || true
  exit 1
fi

echo "Verified ${source_url}@${source_ref#digest=}: stable tags, identical package tree, and the rc artifact's container digests."
