#!/usr/bin/env bats
# The cache settings of the release-line image builds. build-release.yaml runs
# only on a push to a line and tags.yaml only on a tag, so no PR lane would
# notice a change to them.
#
# Test-level EXIT traps replace Bats' own handler and hide failing TAP results.
# Cleanup follows aborting assertions; see docs/agents/e2e-testing.md.

load test_helper

@test "a push to a release line writes the line's cache and imports main's too" {
  step='.jobs["build-line"].steps[] | select(.name == "Build line images and publish the packages artifact")'
  wf=.github/workflows/build-release.yaml
  [ "$(yq "$step | .run" "$wf")" = 'make build-parallel' ]
  [ "$(yq "$step | .env.WRITE_CACHE" "$wf")" = 1 ]
  [ "$(yq "$step | .env.CACHE_TAG" "$wf")" = 'buildcache-${{ github.ref_name }}' ]
  [ "$(yq "$step | .env.CACHE_FROM_TAGS" "$wf")" = 'buildcache-${{ github.ref_name }} buildcache' ]
}

@test "an rc build imports its line's cache and main's, and only arm64 writes one" {
  wf=.github/workflows/tags.yaml
  amd64='.jobs["build-amd64"].steps[] | select(.name == "Build")'
  [ "$(yq "$amd64 | .env.CACHE_FROM_TAGS" "$wf")" = 'buildcache-release-${{ steps.tag.outputs.line }} buildcache' ]
  [ "$(yq "$amd64 | .env.WRITE_CACHE" "$wf")" = null ]
  arm64='.jobs["build-arm64"].steps[] | select(.name == "Build arm64 images")'
  [ "$(yq "$arm64 | .env.WRITE_CACHE" "$wf")" = 1 ]
  yq "$arm64 | .run" "$wf" | grep -qxF 'export CACHE_TAG="buildcache-arm64-$line"'
  yq "$arm64 | .run" "$wf" | grep -qxF 'export CACHE_FROM_TAGS="buildcache-arm64-$line buildcache-arm64"'
}

@test "the rc build jobs leave their buildx builder to the throwaway runner" {
  wf=.github/workflows/tags.yaml
  setup='[(.jobs["build-arm64"].steps[], .jobs["build-amd64"].steps[]) | select((.uses // "") | test("^docker/setup-buildx-action@"))]'
  [ "$(yq "$setup | length" "$wf")" -eq 2 ]
  [ "$(yq "$setup | map(.with.cleanup == false) | all" "$wf")" = true ]
}
