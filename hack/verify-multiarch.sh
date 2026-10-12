#!/bin/sh
# Fail unless every image the packages tree pins is an index serving both
# linux/amd64 and linux/arm64.
#
# Usage: hack/verify-multiarch.sh [--report] [--allowlist <file>] <packages-root>
#        hack/verify-multiarch.sh [--report] [--allowlist <file>] --refs-file <file>
#   --report     print the same list but exit 0, for a tree that is not being
#                released (the nightly arm64 build runs it against main)
#   --allowlist  repositories allowed to stay single-arch; defaults to
#                hack/multiarch-allowlist
#   --refs-file  check the refs listed in <file>, one per line, instead of a
#                tree: what an e2e run's nodes actually pulled, which includes
#                images the tree does not pin by digest. A ref without a digest
#                is resolved by its tag.
#
# A release ships one ref per image to clusters of either architecture, so a
# ref that resolves to a single-platform manifest, or to an index missing one of
# the two, installs on one of them and fails to pull on the other. First- and
# third-party refs are checked alike: an arm64 node pulls both.
#
# The allowlist holds one repository per line with a mandatory `# reason`, and
# no tag or digest, so bumping an allowed image needs no edit. Allowed refs are
# still listed, under their own heading, and in tree mode an entry that
# matches no ref that failed the check is named as stale.
#
# A registry that answers a lookup with a rate limit or a 5xx is asked again,
# up to three times, VERIFY_MULTIARCH_RETRY_DELAY seconds (default 10) times
# the attempt apart; a ref still refused fails as rate-limited, never as
# single-arch.
#
# Exit: 0 all refs pass (or --report), 1 a ref failed, 2 usage or allowlist
# error. Requires skopeo, jq, and yq (mikefarah) to read a tree.
set -eu

usage() { echo "usage: verify-multiarch.sh [--report] [--allowlist <file>] <packages-root> | --refs-file <file>" >&2; exit 2; }

report=0
allowlist="$(dirname "$0")/multiarch-allowlist"
refs_file=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --report) report=1; shift ;;
    --allowlist) [ "$#" -ge 2 ] || usage; allowlist="$2"; shift 2 ;;
    --refs-file) [ "$#" -ge 2 ] || usage; refs_file="$2"; shift 2 ;;
    -*) usage ;;
    *) break ;;
  esac
done
if [ -n "$refs_file" ]; then
  [ "$#" -eq 0 ] || usage
  [ -f "$refs_file" ] || { echo "refs file '$refs_file' does not exist" >&2; exit 2; }
  tools="skopeo jq"
else
  [ "$#" -eq 1 ] || usage
  ROOT="${1%/}"
  [ -d "$ROOT" ] || { echo "packages-root '$ROOT' is not a directory" >&2; exit 2; }
  tools="skopeo jq yq"
fi
[ -f "$allowlist" ] || { echo "allowlist '$allowlist' does not exist" >&2; exit 2; }
for _tool in $tools; do
  command -v "$_tool" >/dev/null || { echo "$_tool is required" >&2; exit 2; }
done

# shellcheck source=hack/lib/image-refs.sh
. "$(dirname "$0")/lib/image-refs.sh"

# "<repo> <reason>" per entry.
allowed="$(sed -E '/^[[:space:]]*(#|$)/d' "$allowlist" | while IFS= read -r _line; do
  _repo="$(printf '%s' "$_line" | sed -E 's/[[:space:]]*#.*//')"
  _why="$(printf '%s' "$_line" | sed -nE 's/^[^#]*#[[:space:]]*//p')"
  if [ -z "$_why" ] || [ -z "$_repo" ]; then
    echo "::error::allowlist entry '$_line' needs a '# reason'" >&2
    exit 2
  fi
  printf '%s %s\n' "$_repo" "$_why"
done)" || exit 2

# A ref can sit inside a container argument, such as kamaji's
# --migrate-image=<ref>. The packages artifact is an OCI artifact with no
# platform, so it is left out. The enumeration also emits the bare
# "<tag>@<digest>" or "<repo>@<digest>" half of a split map next to the full
# ref; such a fragment is dropped when a ref with a host carries its digest, and
# kept otherwise, so a ref the gate cannot resolve still fails it.
if [ -n "$refs_file" ]; then
  refs="$(grep -vE '^[[:space:]]*(#|$)' "$refs_file" | sort -u)" || true
  [ -n "$refs" ] || { echo "no image refs in ${refs_file}" >&2; exit 2; }
else
  refs="$(collect_image_refs "$ROOT" \
    | sed -E 's/^--[A-Za-z0-9-]+=//' \
    | grep -vE '/cozystack-packages(:[^@]*)?@' \
    | sort -u \
    | awk -F@ '{ ref[NR] = $0; dig[NR] = $2; if ($1 ~ /\//) full[$2] = 1 }
        END { for (i = 1; i <= NR; i++) if (ref[i] ~ /^[^@]*\// || !(dig[i] in full)) print ref[i] }')" || true
  [ -n "$refs" ] || { echo "no image refs found under ${ROOT}" >&2; exit 2; }
fi

bad=""
ok_single=""
used=""
nl='
'
while IFS= read -r ref; do
  name="${ref%@*}"
  last="${name##*/}"
  repo="${name%/*}/${last%%:*}"
  case "$name" in */*) ;; *) repo="${last%%:*}" ;; esac
  # A registry client rejects a ref carrying both a tag and a digest, so a
  # digest ref drops its tag; a tag-only ref resolves as written.
  case "$ref" in
    *@*) target="${repo}@${ref##*@}" ;;
    *) target="$ref" ;;
  esac
  # A rate limit or a 5xx says nothing about the image, so that answer alone is
  # retried, a bounded number of times; any other error is deterministic and
  # fails at once.
  err="$(mktemp)"
  try=1
  while :; do
    if raw="$(skopeo inspect --raw "docker://${target}" 2>"$err")"; then
      missing="$(printf '%s' "$raw" | jq -r '
        if .manifests then
          [.manifests[].platform | select(.os == "linux") | .architecture] as $a
          | ["amd64", "arm64"] - $a | map("index without linux/" + .) | join(", ")
        else "single-platform manifest" end')"
      break
    fi
    if ! grep -qiE 'toomanyrequests|too many requests|status:? (429|5[0-9][0-9])|status code (429|5[0-9][0-9])' "$err"; then
      missing="cannot read the manifest: $(head -n 1 "$err")"
      break
    fi
    if [ "$try" -ge 3 ]; then
      missing="rate-limited or unavailable after 3 tries, not checked: $(head -n 1 "$err")"
      break
    fi
    sleep $((try * ${VERIFY_MULTIARCH_RETRY_DELAY:-10}))
    try=$((try + 1))
  done
  rm -f "$err"
  [ -n "$missing" ] || continue
  # A ref of an allowlisted repository that failed the check, whatever the
  # reason, keeps the entry from being stale; the entry excuses a platform
  # verdict only, since a ref that cannot be read or stays rate-limited is no
  # more usable on amd64 than on arm64.
  entry="$(printf '%s\n' "$allowed" | awk -v r="$repo" '$1 == r { $1 = ""; sub(/^ /, ""); print; exit }')"
  [ -z "$entry" ] || used="${used}${repo}${nl}"
  why=""
  case "$missing" in
    "single-platform manifest"|"index without"*) why="$entry" ;;
  esac
  if [ -n "$why" ]; then
    ok_single="${ok_single}  ${ref}: ${missing} (${why})${nl}"
  else
    bad="${bad}  ${ref}: ${missing}${nl}"
  fi
done <<EOF
$refs
EOF

# Only a tree says an entry is dead. A list of pulled images leaves out every
# image the run did not pull, allowed ones included.
stale=""
if [ -z "$refs_file" ]; then
  stale="$(printf '%s\n' "$allowed" | awk 'NF { print $1 }' | while IFS= read -r _repo; do
    printf '%s' "$used" | grep -qxF "$_repo" || printf '  %s\n' "$_repo"
  done)" || true
fi

n_bad="$(printf '%s' "$bad" | grep -c . || true)"
echo "refs that failed (${n_bad}):"
printf '%s\n' "$bad"
if [ -n "$ok_single" ]; then
  echo "allowed single-arch:"
  printf '%s\n' "$ok_single"
fi
if [ -n "$stale" ]; then
  echo "stale allowlist entries, matching no ref that failed the check:"
  printf '%s\n\n' "$stale"
fi

[ "$n_bad" -eq 0 ] || [ "$report" -eq 1 ] || exit 1
