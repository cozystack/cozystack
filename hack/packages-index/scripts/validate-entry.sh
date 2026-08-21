#!/usr/bin/env bash
# Publication gate for the community package index.
#
# Validates every changed entry with cozypkg, including MANDATORY keyless cosign
# verification against the entry's recorded signing identity, then classifies
# the change into the auto-merge lane or the maintainer-review lane. It fails
# closed: any validation error, parse error, or signature failure blocks; a
# security-relevant edit is routed to review, never auto-merged.
set -euo pipefail

BASE_REF="${BASE_REF:-origin/main}"
ENTRIES_DIR="${ENTRIES_DIR:-entries}"

fail() { echo "::error::$*" >&2; exit 1; }

# field <yaml-file|-> <path>: extract a scalar, defaulting to empty. Reads stdin
# when the file argument is "-". A yq parse error propagates (fails closed).
field() {
  local src="$1" path="$2"
  if [ "$src" = "-" ]; then
    yq -r "${path} // \"\""
  else
    yq -r "${path} // \"\"" "$src"
  fi
}

# git diff failing (e.g. a bad base ref) must abort, not silently report "no
# changes" and pass — so there is no "|| true" here.
changed="$(git diff --name-only --diff-filter=AM "${BASE_REF}...HEAD" -- "$ENTRIES_DIR")"
if [ -z "$changed" ]; then
  echo "no entry changes to validate"
  echo "lane=none" >>"${GITHUB_OUTPUT:-/dev/null}"
  exit 0
fi

lane="auto" # downgraded to "review" by any security-relevant change

while IFS= read -r file; do
  [ -n "$file" ] || continue
  echo "== validating ${file} =="

  name="$(field "$file" '.name')"
  ociRef="$(field "$file" '.ociRef')"
  version="$(field "$file" '.version')"
  identity="$(field "$file" '.signing.identity')"
  issuer="$(field "$file" '.signing.issuer')"

  [ -n "$name" ] || fail "${file}: name is required"
  [ -n "$ociRef" ] || fail "${file}: ociRef is required"
  [ -n "$version" ] || fail "${file}: version is required"
  { [ -n "$identity" ] && [ -n "$issuer" ]; } || fail "${file}: signing.identity and signing.issuer are required"

  # Lane classification. A new entry, or any change to a security-relevant field
  # (ociRef, maintainer, or signing identity/issuer), requires maintainer
  # review. Only a pure version/description bump of an already-listed entry
  # stays on the auto lane. An unreadable base revision errs toward review.
  if git cat-file -e "${BASE_REF}:${file}" 2>/dev/null; then
    base_ociRef="$(git show "${BASE_REF}:${file}" | field - '.ociRef')"
    base_identity="$(git show "${BASE_REF}:${file}" | field - '.signing.identity')"
    base_issuer="$(git show "${BASE_REF}:${file}" | field - '.signing.issuer')"
    base_maintainer="$(git show "${BASE_REF}:${file}" | field - '.maintainer')"
    maintainer="$(field "$file" '.maintainer')"
    if [ "$ociRef" != "$base_ociRef" ] ||
      [ "$identity" != "$base_identity" ] ||
      [ "$issuer" != "$base_issuer" ] ||
      [ "$maintainer" != "$base_maintainer" ]; then
      echo "${file}: security-relevant field changed -> review lane"
      lane="review"
    fi
  else
    echo "${file}: new entry -> review lane"
    lane="review"
  fi

  # Mandatory validation plus cosign verification against the RECORDED identity.
  cozypkg validate "${ociRef}:${version}" \
    --require-signature \
    --certificate-identity "$identity" \
    --certificate-oidc-issuer "$issuer"
done <<<"$changed"

echo "lane=${lane}" >>"${GITHUB_OUTPUT:-/dev/null}"
echo "all changed entries validated; lane=${lane}"
