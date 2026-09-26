#!/bin/sh
# Join the amd64 and arm64 builds of every first-party image into one
# multi-arch index, and repin the packages tree on the index digests.
#
# Usage: hack/stitch-multiarch.sh <packages-root> <registry> <image-tag>
#   <packages-root>  a tree stamped by the amd64 build (packages/, or an
#                    extracted artifact with the same <group>/<pkg> layout)
#   <registry>       the registry both builds pushed to
#   <image-tag>      the amd64 build's IMAGE_TAG; the arm64 build pushed each
#                    image as <image-tag>-arm64
#
# The amd64 build and the arm64 leg run on separate native runners, so each
# pushes a single-platform image. For every ref <registry>/<repo>[:<t>]@A in
# the tree, whatever tag <t> it carries, with <tag> the <image-tag>:
#
#   1. B is the digest of <repo>:<tag>-arm64. No such image is not an error:
#      talos and testing are amd64 only and have no arm64 build, so the ref is
#      skipped and named in the summary.
#   2. Every non-PR tag of <repo> that points at A is moved to an index of A
#      and B, so no tag is left naming the amd64 half alone: a versioned tag
#      pushed beside <image-tag> under PUBLISH_VERSIONED=1, or another line's
#      tag right after that line was cut from this commit.
#   3. A is replaced with the index digest in the files hack/lib/image-refs.sh
#      enumerates, and nowhere else.
#
# When <tag> already points at an index of A and an arm64 image, as it does on
# a rerun of a stitch that failed partway, or for a line whose tag main's
# stitch moved (step 2), that index is used as is and copied to any tag still
# left on A. <tag> pointing anywhere else means another build moved it and the
# tree no longer matches the registry, so the run fails.
#
# The rewrite is as narrow as the enumeration, so the check after it is not:
# any sha256:A left under the tree outside charts/ fails the run. A ref whose
# digest also sits in a file the rewrite does not reach is skipped before
# anything is pushed, since rewriting only some of its copies would split one
# image across two digests. The kamaji control-plane provider is such a ref
# today (files/control-plane-components.yaml); see docs/agents/image-refs.md.
#
# Requires: skopeo, docker buildx, jq, sha256sum, yq (mikefarah), and a login to
# <registry> that both skopeo and docker read (REGISTRY_AUTH_FILE for skopeo).
set -eu

ROOT="${1:?usage: stitch-multiarch.sh <packages-root> <registry> <image-tag>}"
REGISTRY="${2:?usage: stitch-multiarch.sh <packages-root> <registry> <image-tag>}"
IMAGE_TAG="${3:?usage: stitch-multiarch.sh <packages-root> <registry> <image-tag>}"

ROOT="${ROOT%/}"
[ -d "$ROOT" ] || { echo "packages-root '$ROOT' is not a directory" >&2; exit 1; }
for _tool in skopeo docker jq sha256sum yq; do
  command -v "$_tool" >/dev/null || { echo "$_tool is required" >&2; exit 1; }
done

# shellcheck source=hack/lib/image-refs.sh
. "$(dirname "$0")/lib/image-refs.sh"

# manifest_digest <ref> prints the digest of <ref>'s raw manifest, or nothing
# when the registry says it does not exist. Any other failure returns non-zero:
# a rate limit read as "no arm64 image" would quietly ship amd64 only.
manifest_digest() {
  _md_out="$(mktemp)"
  _md_err="$(mktemp)"
  if skopeo inspect --raw "docker://$1" >"$_md_out" 2>"$_md_err" && [ -s "$_md_out" ]; then
    printf 'sha256:%s' "$(sha256sum <"$_md_out" | cut -d' ' -f1)"
    rm -f "$_md_out" "$_md_err"
    return 0
  fi
  if grep -qiE 'manifest unknown|not found|404' "$_md_err"; then
    rm -f "$_md_out" "$_md_err"
    return 0
  fi
  echo "::error::cannot read the manifest of $1: $(tr '\n' ' ' <"$_md_err")" >&2
  rm -f "$_md_out" "$_md_err"
  return 1
}

# image_arch <repo@digest> prints the architecture of a single-platform image,
# or nothing for an index or a non-image artifact. A registry error returns
# non-zero, like manifest_digest.
image_arch() {
  _ia_raw="$(skopeo inspect --raw "docker://$1")" || return 1
  printf '%s' "$_ia_raw" | jq -r '.config.mediaType // empty' \
    | grep -qE 'image\.config|container\.image' || return 0
  _ia_cfg="$(skopeo inspect --config "docker://$1")" || return 1
  printf '%s' "$_ia_cfg" | jq -r '.architecture // empty'
}

# Every lookup below is assigned before it is compared: `set -e` aborts on a
# failed command substitution only in a plain assignment, and inside a test a
# registry error would read as a mismatch and turn into a skip.

skipped=""
skipped_n=0
skip() {
  skipped="${skipped}${skipped:+, }$1"
  skipped_n=$((skipped_n + 1))
  echo "::warning::stitch: skipping $1: $2" >&2
}

ref_files="$(image_ref_files "$ROOT")"
reg_re="$(printf '%s' "$REGISTRY" | sed -e 's/[].[^$*/\\]/\\&/g')"

# "<repo> <digest>" per owned ref, one line per image. The tag a ref carries is
# dropped: a component-versioned ref names its component version, but every
# build pushed the image under <image-tag>. The packages artifact is not an
# image; the caller republishes it from the rewritten tree. grep -o drops
# anything around the ref, such as a --migrate-image= prefix.
pairs="$(collect_image_refs "$ROOT" \
  | grep -oE "${reg_re}/[^@[:space:]\"']+@sha256:[0-9a-f]{64}" \
  | while IFS= read -r _ref; do
      _name="${_ref%@*}"
      _last="${_name##*/}"
      printf '%s/%s %s\n' "${_name%/*}" "${_last%%:*}" "${_ref##*@}"
    done \
  | grep -v "^${reg_re}/cozystack-packages " \
  | sort -u)" || true
[ -n "$pairs" ] || { echo "no ${REGISTRY}/ refs found under ${ROOT}; is this a tree the build stamped?" >&2; exit 1; }
tag="$IMAGE_TAG"

stitched=""
stitched_n=0
stitched_digests=""
tags_json="$(mktemp)"

# tags_at_a sets $move to a --tag flag for every tag of $repo on $a. pr-* tags
# are build-unique and never point at a main or release image, and skipping
# them keeps this from reading every PR manifest the repo holds.
tags_at_a() {
  skopeo list-tags "docker://${repo}" >"$tags_json"
  move=""
  for t in $(jq -r '.Tags[]' "$tags_json"); do
    case "$t" in
      pr-*|*-arm64|buildcache*|sha256-*) continue ;;
    esac
    d="$(manifest_digest "${repo}:${t}")"
    if [ "$d" = "$a" ]; then move="${move} --tag ${repo}:${t}"; fi
  done
}
# fd 3, so a registry client that reads stdin cannot swallow the list.
while read -r repo a <&3; do
  name="${repo##*/}"

  b="$(manifest_digest "${repo}:${tag}-arm64")"
  if [ -z "$b" ]; then skip "$name" "no ${tag}-arm64 image"; continue; fi
  elsewhere="$(grep -rIl --exclude-dir=charts "$a" "$ROOT" | grep -vxF "$ref_files" || true)"
  if [ -n "$elsewhere" ]; then
    skip "$name" "digest also pinned in $(echo "$elsewhere" | tr '\n' ' ')which the rewrite does not reach"; continue
  fi
  cur="$(manifest_digest "${repo}:${tag}")"

  if [ "$cur" = "$a" ]; then
    arch_a="$(image_arch "${repo}@${a}")"
    arch_b="$(image_arch "${repo}@${b}")"
    if [ "$arch_a" != amd64 ] || [ "$arch_b" != arm64 ]; then
      skip "$name" "${tag} and ${tag}-arm64 are not one amd64 and one arm64 image"; continue
    fi

    tags_at_a
    [ -n "$move" ] || { echo "::error::no tag of ${repo} left to move to the index" >&2; exit 1; }
    # $move is a list of --tag words; tags cannot contain whitespace.
    # shellcheck disable=SC2086
    docker buildx imagetools create $move "${repo}@${a}" "${repo}@${b}"
    index="$(manifest_digest "${repo}:${tag}")"
    [ -n "$index" ] && [ "$index" != "$a" ] || { echo "::error::${repo}:${tag} did not move to an index" >&2; exit 1; }
  else
    [ -n "$cur" ] || { echo "::error::${repo}:${tag} does not exist, though the tree pins it" >&2; exit 1; }
    raw="$(skopeo inspect --raw "docker://${repo}@${cur}")"
    # Its arm64 half need not be this run's B: main's stitch moves a line's
    # tag that sits on the same A to main's index, before the line's own
    # stitch runs.
    if ! printf '%s' "$raw" | jq -e --arg a "$a" \
      '(.manifests // []) as $m | any($m[]; .digest == $a) and any($m[]; .platform.architecture == "arm64")' >/dev/null; then
      echo "::error::${repo}:${tag} points at ${cur}, which is neither the digest the tree pins (${a}) nor an index of it and an arm64 image" >&2
      exit 1
    fi
    index="$cur"
    # An attempt that died inside imagetools create may have moved only some
    # of the tags; copy the index to any still left on A.
    tags_at_a
    # shellcheck disable=SC2086
    if [ -n "$move" ]; then docker buildx imagetools create $move "${repo}@${index}"; fi
  fi

  for f in $ref_files; do
    grep -q "$a" "$f" || continue
    sed "s|${a}|${index}|g" "$f" >"$f.stitch"
    cat "$f.stitch" >"$f"
    rm -f "$f.stitch"
  done
  stitched="${stitched}${stitched:+, }${name}"
  stitched_n=$((stitched_n + 1))
  stitched_digests="${stitched_digests} ${a}"
done 3<<EOF
$pairs
EOF
rm -f "$tags_json"

for a in $stitched_digests; do
  left="$(grep -rIl --exclude-dir=charts "$a" "$ROOT" || true)"
  if [ -n "$left" ]; then
    echo "::error::${a} is still pinned after the rewrite, in: $(echo "$left" | tr '\n' ' ')" >&2
    exit 1
  fi
done

echo "stitch: ${stitched_n} stitched (${stitched}); ${skipped_n} skipped (${skipped})"
