#!/bin/sh
# Rewrite the rc version substring to the stable version in every vendored
# first-party image reference. Promotion does not rebuild: the digests stay
# exactly as the rc built them, and only the cosmetic tag string moves from
# "1.6.0-rc.4" to "1.6.0" so the shipped tree reads as the release it is.
#
# Usage: hack/promote-rewrite-tags.sh <rc-version> <stable-version> [root]
#   <rc-version>      e.g. 1.6.0-rc.4   (no leading v — the substring as it
#                     appears inside an image tag)
#   <stable-version>  e.g. 1.6.0
#   [root]            tree to rewrite; defaults to "packages"
#
# This lived inline in .github/workflows/promote-rc.yaml until it shipped a
# release whose tags were half-rewritten. Its glob covered the depth-2
# values.yaml plus packages/apps/kubernetes/images/*.tag alone, on the premise
# that the kubernetes app was the only one whose .tag files carry the cozystack
# version — nine other .tag files and one stamped template do. Because the step
# was workflow-inline there was nothing to unit test, so the miss was only
# observable by cutting a release. It is a script so that
# hack/promote-rewrite-tags_test.bats can round-trip it against the real tree.
#
# Requires a POSIX shell plus GNU sed and GNU grep: `sed -i` and grep's
# -r/--exclude/--exclude-dir are GNU extensions, not POSIX. Both are present on
# the CI runners and in the build image; this is not portable to BSD userland
# as written.
set -eu

RC_VERSION="${1:?usage: promote-rewrite-tags.sh <rc-version> <stable-version> [root]}"
STABLE_VERSION="${2:?usage: promote-rewrite-tags.sh <rc-version> <stable-version> [root]}"
ROOT="${3:-packages}"

# Validate both versions before touching a file. RC_VERSION becomes a sed
# pattern and STABLE_VERSION its replacement, so an unvalidated argument is
# both a correctness and an injection concern: a stray "/" or "&" in the
# replacement would corrupt every file this walks.
echo "$RC_VERSION" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$' \
  || { echo "rc-version '$RC_VERSION' must match X.Y.Z-rc.N" >&2; exit 1; }
echo "$STABLE_VERSION" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' \
  || { echo "stable-version '$STABLE_VERSION' must match X.Y.Z" >&2; exit 1; }
[ -d "$ROOT" ] || { echo "root '$ROOT' is not a directory" >&2; exit 1; }

# shellcheck source=hack/lib/image-refs.sh
. "$(dirname "$0")/lib/image-refs.sh"

# The dots are the only regex metacharacter a validated version can contain;
# escaping them keeps sed matching the literal string rather than any char.
RC_ESC=$(printf '%s' "$RC_VERSION" | sed -e 's/\./\\./g')

# Materialize the file list rather than piping into the loop: a `while` on the
# right of a pipe runs in a subshell, so it cannot carry a count out and cannot
# abort the script from inside. Both matter here — the count is the operator's
# only signal that the rewrite did anything, and an unreadable file must stop
# the promotion rather than be skipped.
files_list=$(mktemp)
image_ref_files "$ROOT" > "$files_list"

rewritten=0
while IFS= read -r f; do
  # An unreadable file is NOT "a file with no match". Treating the two alike is
  # how a ref goes unrewritten while the run still reports success — the same
  # silent-skip shape as the enumeration bug this script exists to fix. Fail.
  if [ ! -r "$f" ]; then
    echo "::error::${f} is not readable; refusing to promote a tree that cannot be fully scanned" >&2
    rm -f "$files_list"
    exit 1
  fi
  if grep -q "$RC_ESC" "$f"; then
    sed -i "s/${RC_ESC}/${STABLE_VERSION}/g" "$f"
    echo "  ~ $f"
    rewritten=$((rewritten + 1))
  else
    # grep exits 1 for "no match" and >1 for a real error; only the former is
    # an ordinary outcome.
    grep_rc=$?
    if [ "$grep_rc" -gt 1 ]; then
      echo "::error::failed to scan ${f} (grep exit ${grep_rc})" >&2
      rm -f "$files_list"
      exit 1
    fi
  fi
done < "$files_list"
rm -f "$files_list"

# Postcondition: scan WIDER than the rewrite. The rewrite is deliberately
# narrow — a blind walk of every file under $ROOT would rewrite vendored chart
# defaults that `make update` regenerates — but a narrow rewrite is exactly
# what shipped a half-promoted release, so the check that follows it must not
# share its blind spot. Anything still carrying the rc string here is a ref in
# a storage location the enumeration does not know about: fail the promotion
# loudly rather than publish a release whose images claim to be a release
# candidate.
#
# It must, however, only flag strings that could actually BE an image ref.
# "X.Y.Z-rc.N" is an ordinary version string that other things legitimately
# contain, and a false positive here fails a workflow that runs once per
# release, on release day. Three live examples, each of which aborted the
# promotion before this was shape-filtered:
#   - packages/apps/kubernetes/images/kubevirt-csi-driver/go.sum pins
#     github.com/golang/protobuf v1.4.0-rc.2 — and v1.4.0-rc.2 is a cozystack
#     rc that was actually cut, so this was not hypothetical
#   - packages/system/metallb/tests/metallb_test.yaml has a comment naming
#     v1.5.0-rc.2 as an example of what release-prep stamps
#   - the console's pnpm-lock.yaml carries rolldown@1.0.0-rc.15 and friends
#
# So match the version only where it appears in ref position: after an
# image/repository/tag key (with no `#` in between, which excludes prose in a
# trailing comment), or immediately before an @sha256 digest. The second branch
# is what catches a bare .tag file, whose content has no YAML key at all. The
# first is required for the split shape where the version and the digest live
# under separate keys — packages/system/cilium/values.yaml has `tag: v1.5.0`
# and `digest:` on the next line — so narrowing this to "${RC}@sha256:" alone
# would silently stop covering it.
#
# charts/ is excluded because a vendored upstream chart may legitimately pin an
# unrelated component whose own version happens to contain the substring, and
# the build never stamps into one. Markdown is excluded because documentation
# legitimately names release candidates — an upgrade note or a README example
# citing the rc being promoted is correct prose, not an unrewritten ref.
leftovers=$(grep -rIlE --exclude-dir=charts --exclude='*.md' \
  -- "(image|repository|tag)[\"']?:[^#]*${RC_ESC}|${RC_ESC}@sha256:" "$ROOT" 2>/dev/null || true)
if [ -n "$leftovers" ]; then
  echo "::error::the following files still carry the rc version '${RC_VERSION}' in image-reference position after the rewrite:" >&2
  printf '%s\n' "$leftovers" >&2
  echo "Most likely each is a first-party image ref in a storage location hack/lib/image-refs.sh does not enumerate;" >&2
  echo "if so, declare it in IMAGE_REF_EXTRA_FILES there — see docs/agents/image-refs.md." >&2
  echo "If instead the match is an unrelated version string that merely looks like a ref, narrow the pattern above" >&2
  echo "or add an --exclude for that file; do NOT declare a non-image file as a ref source." >&2
  exit 1
fi

echo "Rewrote ${RC_VERSION} -> ${STABLE_VERSION} in ${rewritten} file(s) under ${ROOT}; no rc references remain in image-reference position."
