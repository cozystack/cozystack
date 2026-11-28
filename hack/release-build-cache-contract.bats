#!/usr/bin/env bats
# The cache settings of the release-line image builds. build-release.yaml runs
# only on a push to a line and tags.yaml only on a tag, so no PR lane would
# notice a change to them.
#
# Test-level EXIT traps replace Bats' own handler and hide failing TAP results.
# Cleanup follows aborting assertions; see docs/agents/e2e-testing.md.

load test_helper

@test "CACHE_FROM_TAGS imports every cache it lists and the build still writes only CACHE_TAG" {
  pkg=packages/system/cozystack-controller
  out=$(make -n -C "$pkg" image IMAGE_TAG=pr-1-abc COZYSTACK_VERSION=0 WRITE_CACHE=1 CACHE_TAG=buildcache-release-1.6 'CACHE_FROM_TAGS=buildcache-release-1.6 buildcache')
  echo "$out" | grep -q -- '--cache-from type=registry,ref=[^ ]*/cozystack-controller:buildcache-release-1.6 '
  echo "$out" | grep -q -- '--cache-from type=registry,ref=[^ ]*/cozystack-controller:buildcache '
  echo "$out" | grep -q -- '--cache-to type=registry,ref=[^ ]*/cozystack-controller:buildcache-release-1.6,[^ ]*ignore-error=true'
  [ "$(echo "$out" | grep -o -- '--cache-to' | wc -l)" -eq 1 ]
  for write in '' WRITE_CACHE=0; do
    out=$(make -n -C "$pkg" image IMAGE_TAG=pr-1-abc COZYSTACK_VERSION=0 $write 'CACHE_FROM_TAGS=buildcache-release-1.6 buildcache')
    [ "$(echo "$out" | grep -o -- '--cache-from' | wc -l)" -eq 2 ]
    [ "$(echo "$out" | grep -o -- '--cache-to' | wc -l)" -eq 0 ]
  done
  out=$(make -n -C "$pkg" image IMAGE_TAG=pr-1-abc COZYSTACK_VERSION=0)
  [ "$(echo "$out" | grep -o -- '--cache-from' | wc -l)" -eq 1 ]
  echo "$out" | grep -q -- '--cache-from type=registry,ref=[^ ]*/cozystack-controller:buildcache '
}

@test "a push to a release line writes the line's cache and imports main's too" {
  step='.jobs["build-line"].steps[] | select(.name == "Build line images and publish the packages artifact")'
  wf=.github/workflows/build-release.yaml
  [ "$(yq "$step | .run" "$wf")" = 'make build-parallel' ]
  [ "$(yq "$step | .env.WRITE_CACHE" "$wf")" = 1 ]
  [ "$(yq "$step | .env.CACHE_TAG" "$wf")" = 'buildcache-${{ github.ref_name }}' ]
  [ "$(yq "$step | .env.CACHE_FROM_TAGS" "$wf")" = 'buildcache-${{ github.ref_name }} buildcache' ]
}

@test "an rc build imports its line's cache and main's, and writes none" {
  wf=.github/workflows/tags.yaml
  build='.jobs["prepare-release"].steps[] | select(.name == "Build")'
  [ "$(yq "$build | .run" "$wf")" = 'make build-parallel' ]
  [ "$(yq "$build | .env.CACHE_FROM_TAGS" "$wf")" = 'buildcache-release-${{ steps.tag.outputs.line }} buildcache' ]
  [ "$(yq "$build | .env.WRITE_CACHE" "$wf")" = null ]
}
