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
#      matchbox is built for both arches by the amd64 job and the e2e
#      sandbox for amd64 only, so neither has an arm64 twin; the ref is
#      skipped and named in the summary.
#   2. Every tag this build pushed for <repo> is moved to an index of A and B
#      if it points at A, so none is left naming the amd64 half alone: <tag>,
#      the tag <t> the ref carries, and every <repo>:<tag> line in the file
#      PUSHED_TAGS_LOG names. image-tags in hack/common-envs.mk writes that
#      log when the variable is set and PUSH=1, and it is how the component
#      version pushed beside <image-tag> under PUBLISH_VERSIONED=1 is found,
#      since most refs name only <image-tag>. These are the only tags read;
#      a tag of another build that sits on the same bytes is not this build's
#      to move.
#      PUSHED_TAGS_LOG set to a file that does not exist fails the run.
#   3. A is replaced with the index digest in the files hack/lib/image-refs.sh
#      enumerates, and nowhere else.
#
# When <tag> already points at an index of A and an arm64 image, as it does on
# a rerun of a stitch that failed partway, that index is used as is and copied
# to the build's other tags still left on A. <tag> pointing anywhere else means
# another build moved it and the tree no longer matches the registry, so the
# run fails.
#
# A registry read answered with a rate limit or a 5xx is retried, three tries
# in all, STITCH_RETRY_DELAY seconds (default 10) times the attempt apart; any
# other registry error fails the run at once.
#
# The rewrite is as narrow as the enumeration, so the check after it is not:
# any sha256:A left under the tree outside charts/, gzip payloads included,
# fails the run. A ref whose digest also sits in a file the rewrite does not
# reach is skipped before anything is pushed, since rewriting only some of its
# copies would split one image across two digests. The one gzip it rewrites is
# declared in GZIP_COPIES below; see docs/agents/image-refs.md.
#
# Requires: skopeo, docker buildx, jq, sha256sum, gzip, yq (mikefarah), and a
# login to <registry> that both skopeo and docker read (REGISTRY_AUTH_FILE for
# skopeo).
set -eu

ROOT="${1:?usage: stitch-multiarch.sh <packages-root> <registry> <image-tag>}"
REGISTRY="${2:?usage: stitch-multiarch.sh <packages-root> <registry> <image-tag>}"
IMAGE_TAG="${3:?usage: stitch-multiarch.sh <packages-root> <registry> <image-tag>}"

ROOT="${ROOT%/}"
[ -d "$ROOT" ] || { echo "packages-root '$ROOT' is not a directory" >&2; exit 1; }
for _tool in skopeo docker jq sha256sum gzip yq; do
  command -v "$_tool" >/dev/null || { echo "$_tool is required" >&2; exit 1; }
done
if [ -n "${PUSHED_TAGS_LOG:-}" ] && [ ! -f "$PUSHED_TAGS_LOG" ]; then
  echo "PUSHED_TAGS_LOG '$PUSHED_TAGS_LOG' does not exist; the build recorded no tags" >&2
  exit 1
fi

# shellcheck source=hack/lib/image-refs.sh
. "$(dirname "$0")/lib/image-refs.sh"

# sk runs skopeo and asks again on a rate limit or a 5xx, which say nothing
# about the image; any other failure, or one that outlasts the tries, is
# returned with skopeo's message.
sk() {
  _sk_err="$(mktemp)"
  _sk_try=1
  while :; do
    if skopeo "$@" 2>"$_sk_err"; then rm -f "$_sk_err"; return 0; fi
    if [ "$_sk_try" -ge 3 ] \
      || ! grep -qiE 'toomanyrequests|too many requests|status:? (429|5[0-9][0-9])|status code (429|5[0-9][0-9])' "$_sk_err"; then
      cat "$_sk_err" >&2
      rm -f "$_sk_err"
      return 1
    fi
    sleep $((_sk_try * ${STITCH_RETRY_DELAY:-10}))
    _sk_try=$((_sk_try + 1))
  done
}

# manifest_digest <ref> prints the digest of <ref>'s raw manifest, or nothing
# when the registry says it does not exist. Any other failure returns non-zero:
# a rate limit read as "no arm64 image" would quietly ship amd64 only.
manifest_digest() {
  _md_out="$(mktemp)"
  _md_err="$(mktemp)"
  if sk inspect --raw "docker://$1" >"$_md_out" 2>"$_md_err" && [ -s "$_md_out" ]; then
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
  _ia_raw="$(sk inspect --raw "docker://$1")" || return 1
  printf '%s' "$_ia_raw" | jq -r '.config.mediaType // empty' \
    | grep -qE 'image\.config|container\.image' || return 0
  _ia_cfg="$(sk inspect --config "docker://$1")" || return 1
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

# "<gzip>=<plain file>" pairs, root-relative. The kamaji provider's chart ships
# its components only as files/components.gz, which the package's image target
# builds from control-plane-components.yaml with `gzip -nc`. Both copies are
# rewritten, the gzip by decompressing, rewriting and recompressing with -n,
# which is byte for byte what that target writes as long as both run GNU gzip,
# as CI does (Apple's gzip compresses differently). The list is not in
# image-refs.sh: the mirror's host rewrite there cannot reach inside a gzip, and
# declaring only the plain copy to it would split the two.
GZIP_COPIES="system/capi-providers-cpprovider/files/components.gz=system/capi-providers-cpprovider/files/control-plane-components.yaml"

ref_files="$(image_ref_files "$ROOT")"
gz_files=""
for _pair in $GZIP_COPIES; do
  [ -f "$ROOT/${_pair%%=*}" ] || continue
  gz_files="${gz_files}${gz_files:+
}${ROOT}/${_pair%%=*}"
  ref_files="${ref_files}
${ROOT}/${_pair#*=}"
done

# pinned_in <digest> lists every file outside charts/ that pins it, looking
# inside gzips too: grep -I skips them as binary.
pinned_in() {
  grep -rIl --exclude-dir=charts "$1" "$ROOT" || true
  find "$ROOT" -name charts -prune -o -type f -name '*.gz' -print | while IFS= read -r _gz; do
    if gzip -dc "$_gz" 2>/dev/null | grep -q "$1"; then printf '%s\n' "$_gz"; fi
  done
}
reg_re="$(printf '%s' "$REGISTRY" | sed -e 's/[].[^$*/\\]/\\&/g')"

# "<repo> <digest> <tags>" per owned image, <tags> the comma-joined tags its
# refs carry, or "-". Every build pushed the image under <image-tag>; a
# component-versioned ref names its component version, pushed beside it. The
# packages artifact is not an image; the caller republishes it from the
# rewritten tree. grep -o drops anything around the ref, such as a
# --migrate-image= prefix.
pairs="$(collect_image_refs "$ROOT" \
  | grep -oE "${reg_re}/[^@[:space:]\"']+@sha256:[0-9a-f]{64}" \
  | while IFS= read -r _ref; do
      _name="${_ref%@*}"
      _last="${_name##*/}"
      _t=-
      if [ "${_last#*:}" != "$_last" ]; then _t="${_last#*:}"; fi
      printf '%s/%s %s %s\n' "${_name%/*}" "${_last%%:*}" "${_ref##*@}" "$_t"
    done \
  | grep -v "^${reg_re}/cozystack-packages " \
  | sort -u \
  | awk '{ k = $1 " " $2; if (!(k in t)) { order[++n] = k; t[k] = "" } if ($3 != "-") t[k] = t[k] "," $3 }
      END { for (i = 1; i <= n; i++) { v = t[order[i]]; sub(/^,/, "", v); print order[i], (v == "" ? "-" : v) } }')" || true
[ -n "$pairs" ] || { echo "no ${REGISTRY}/ refs found under ${ROOT}; is this a tree the build stamped?" >&2; exit 1; }
tag="$IMAGE_TAG"

stitched=""
stitched_n=0
stitched_digests=""

# tags_at_a sets $move to a --tag flag for each tag this build pushed for
# $repo that still points at $a.
tags_at_a() {
  move=""
  logged=""
  if [ -n "${PUSHED_TAGS_LOG:-}" ]; then
    logged="$(awk -v r="$repo" 'index($0, r ":") == 1 { t = substr($0, length(r) + 2); if (t !~ /[\/:]/) print t }' "$PUSHED_TAGS_LOG")"
  fi
  for t in $tag $(printf '%s' "$reftags" | tr ',' ' ') $logged; do
    [ "$t" != - ] || continue
    case "$move " in *" --tag ${repo}:${t} "*) continue ;; esac
    d="$(manifest_digest "${repo}:${t}")"
    if [ "$d" = "$a" ]; then move="${move} --tag ${repo}:${t}"; fi
  done
}
# fd 3, so a registry client that reads stdin cannot swallow the list.
while read -r repo a reftags <&3; do
  name="${repo##*/}"

  b="$(manifest_digest "${repo}:${tag}-arm64")"
  if [ -z "$b" ]; then skip "$name" "no ${tag}-arm64 image"; continue; fi
  elsewhere="$(pinned_in "$a" | grep -vxF "${ref_files}${gz_files:+
}${gz_files}" || true)"
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
    raw="$(sk inspect --raw "docker://${repo}@${cur}")"
    # Its arm64 half need not be this run's B: a rerun rebuilds the arm64
    # image, which need not reproduce it byte for byte.
    if ! printf '%s' "$raw" | jq -e --arg a "$a" \
      '(.manifests // []) as $m | any($m[]; .digest == $a) and any($m[]; .platform.architecture == "arm64")' >/dev/null; then
      echo "::error::${repo}:${tag} points at ${cur}, which is neither the digest the tree pins (${a}) nor an index of it and an arm64 image" >&2
      exit 1
    fi
    index="$cur"
    # An attempt that died inside imagetools create may have moved <tag> and
    # not the others; copy the index to any still left on A.
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
  for f in $gz_files; do
    gzip -dc "$f" | grep -q "$a" || continue
    gzip -dc "$f" | sed "s|${a}|${index}|g" | gzip -n >"$f.stitch"
    cat "$f.stitch" >"$f"
    rm -f "$f.stitch"
  done
  stitched="${stitched}${stitched:+, }${name}"
  stitched_n=$((stitched_n + 1))
  stitched_digests="${stitched_digests} ${a}"
done 3<<EOF
$pairs
EOF

for a in $stitched_digests; do
  left="$(pinned_in "$a")"
  if [ -n "$left" ]; then
    echo "::error::${a} is still pinned after the rewrite, in: $(echo "$left" | tr '\n' ' ')" >&2
    exit 1
  fi
done

echo "stitch: ${stitched_n} stitched (${stitched}); ${skipped_n} skipped (${skipped})"
