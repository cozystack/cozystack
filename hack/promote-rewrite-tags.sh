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
# Requires nothing but a POSIX shell and sed.
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

# The loop body runs in a subshell (it is the right-hand side of a pipe), so a
# counter incremented here would not survive it. The file writes do, and the
# postcondition below is what actually asserts completeness, so nothing needs
# to be carried out of the loop.
image_ref_files "$ROOT" | while IFS= read -r f; do
  grep -q "$RC_ESC" "$f" 2>/dev/null || continue
  sed -i "s/${RC_ESC}/${STABLE_VERSION}/g" "$f"
  echo "  ~ $f"
done

# Postcondition: scan WIDER than the rewrite. The rewrite is deliberately
# narrow — a blind walk of every file under $ROOT would rewrite vendored chart
# defaults that `make update` regenerates — but a narrow rewrite is exactly
# what shipped a half-promoted release, so the check that follows it must not
# share its blind spot. Anything still carrying the rc string here is a ref in
# a storage location the enumeration does not know about: fail the promotion
# loudly rather than publish a release whose images claim to be a release
# candidate.
#
# charts/ is excluded because a vendored upstream chart may legitimately pin an
# unrelated component whose own version happens to contain the substring, and
# the build never stamps into one. Markdown is excluded because documentation
# legitimately names release candidates — an upgrade note or a README example
# citing the rc being promoted is correct prose, not an unrewritten ref, and
# failing the promotion over one would be a false positive that teaches people
# to bypass this check.
leftovers=$(grep -rIl --exclude-dir=charts --exclude='*.md' -- "$RC_VERSION" "$ROOT" 2>/dev/null || true)
if [ -n "$leftovers" ]; then
  echo "::error::the following files still carry the rc version '${RC_VERSION}' after the rewrite:" >&2
  printf '%s\n' "$leftovers" >&2
  echo "Each is a first-party image ref in a storage location hack/lib/image-refs.sh does not enumerate." >&2
  echo "Add it to IMAGE_REF_EXTRA_FILES (or to a glob) there — see docs/agents/image-refs.md." >&2
  exit 1
fi

echo "Rewrote ${RC_VERSION} -> ${STABLE_VERSION} across ${ROOT}; no rc references remain."
