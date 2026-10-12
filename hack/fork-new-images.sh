#!/usr/bin/env bash
# Usage: hack/fork-new-images.sh BASE_TREE HEAD_TREE
#
# Fails, naming them, when HEAD_TREE exports image archives that BASE_TREE does
# not. e2e-fork.yaml pushes a fork PR's archives only under names the base tree
# exports, so such a PR would otherwise build everything and then be refused at
# the push, with nothing the contributor can do about it.
#
# image_names mirrors the "Derive trusted image allowlist" step of
# e2e-fork.yaml. That step cannot call this script, because it runs from the
# default branch over an untrusted checkout; hack/fork-new-images.bats keeps the
# two derivations in step.
set -eu

[ "$#" -eq 2 ] || { echo "usage: $0 BASE_TREE HEAD_TREE" >&2; exit 2; }

image_names() {
  git -C "$1" ls-files 'packages/*/*/Makefile' | while IFS= read -r mk; do
    grep -qE '^image:' "$1/$mk" || continue
    make -n -C "$1/$(dirname "$mk")" image \
      OCI_EXPORT_DIR=/oci REGISTRY=r COZYSTACK_VERSION=0 2>/dev/null || true
  done \
    | grep -oE '(dest=/oci/|oci-archive:/oci/)[a-z0-9._-]+\.oci\.tar' \
    | sed -E 's#.*/##; s#\.oci\.tar$##' | sort -u
}

base="$(image_names "$1")"
head="$(image_names "$2")"
# An empty base set would report every head image as new and blame the PR for
# a derivation that broke on its own.
[ -n "$base" ] || { echo "::error::image name derivation produced no names for the base tree" >&2; exit 1; }

new="$(comm -13 <(printf '%s\n' "$base") <(printf '%s\n' "$head"))"
[ -z "$new" ] && exit 0

names="$(printf '%s' "$new" | tr '\n' ' ')"
echo "::error title=New image in a fork PR::This PR adds images the base branch does not build: ${names% }. The fork e2e lane pushes only images whose names the base branch already exports, so it would refuse these after the whole build. A maintainer has to land the new image on the base branch first, or the change has to run from a branch in the upstream repository. See docs/agents/e2e-testing.md, section 10."
exit 1
