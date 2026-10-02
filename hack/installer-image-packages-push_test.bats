#!/usr/bin/env bats
# `make image-packages` in packages/core/installer publishes the packages
# artifact with `flux push artifact` and then stamps the pushed digest into
# values.yaml as platformSourceRef. The operator hands that ref to Flux, which
# pulls the artifact by digest from the registry, so a build that pushes nothing
# must leave the committed ref alone rather than stamp one that resolves nowhere.
#
# Run via hack/cozytest.sh (make bats-unit-tests). This is NOT real bats: no
# run/$status/$output, and a `!`-negated pipeline is suppressed under set -e, so
# negative checks use the `if grep -q ...; then ...; false; fi` idiom.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"

@test "image-packages pushes nothing and stamps nothing with PUSH=0" {
  out=$(env -u PUSH -u LOAD make --dry-run -C "$REPO_ROOT/packages/core/installer" image-packages PUSH=0)
  if echo "$out" | grep -q 'flux push artifact'; then echo "FAIL: PUSH=0 still pushes the packages artifact"; false; fi
  if echo "$out" | grep -q 'platformSourceRef = '; then echo "FAIL: PUSH=0 still stamps platformSourceRef"; false; fi
}

@test "image-packages pushes and stamps the digest when PUSH is left at its default" {
  out=$(env -u PUSH -u LOAD make --dry-run -C "$REPO_ROOT/packages/core/installer" image-packages)
  echo "$out" | grep -q 'flux push artifact'
  echo "$out" | grep -q "platformSourceUrl = strenv(REPO)"
  echo "$out" | grep -q 'platformSourceRef = "digest=" + strenv(DIGEST)'
}
