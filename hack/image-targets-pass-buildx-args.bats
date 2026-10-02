#!/usr/bin/env bats
# Every package image target builds through $(BUILDX_ARGS) from
# hack/common-envs.mk, so PLATFORM, BUILDER, SBOM and BUILDX_EXTRA_ARGS reach
# each `docker buildx build`. A target that spells its flags out instead builds
# for the host architecture when PLATFORM is set: on an arm64 workstation that
# is an arm64 image, which fails on amd64 nodes with "exec format error" while
# CI, running on amd64, never shows it.
#
# The check is a dry run of `make image` with a platform no target hardcodes
# and a marker passed through BUILDX_EXTRA_ARGS. Every buildx invocation it
# prints must carry the marker, which only $(BUILDX_ARGS) forwards, and that
# platform and no other, since a second hardcoded --platform turns the build
# into a multi-platform one.
#
# The packages in AMD64_ONLY pin `override PLATFORM := linux/amd64` because
# what they build is amd64 by nature; they must yield exactly that platform
# whatever the command line says.
#
# Written for POSIX sh: hack/cozytest.sh sources this file into /bin/sh.
#
# Requires: make.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
PROBE_PLATFORM="linux/riscv64"
PROBE_MARKER="--build-arg=BUILDX_ARGS_PROBE=1"
AMD64_ONLY="packages/core/testing"

@test "every package image target passes BUILDX_ARGS and PLATFORM to docker buildx" {
  command -v make >/dev/null || { echo "make is required" >&2; exit 1; }

  failed=""
  checked=0
  errlog="$(mktemp)"
  cmdlist="$(mktemp)"
  for makefile in "$REPO_ROOT"/packages/*/*/Makefile; do
    grep -q 'docker buildx build' "$makefile" || continue
    dir="$(dirname "$makefile")"
    # COZYSTACK_VERSION is set so common-envs.mk does not shell out to git.
    commands="$(make --no-print-directory -s -n -C "$dir" image \
      PLATFORM="$PROBE_PLATFORM" BUILDX_EXTRA_ARGS="$PROBE_MARKER" COZYSTACK_VERSION=0.0.0 2>"$errlog" \
      | sed -e ':a' -e '/\\$/N; s/\\\n//; ta' | grep 'docker buildx build')" || true
    if [ -z "$commands" ]; then
      failed="$failed ${dir#"$REPO_ROOT"/} (no buildx command: $(tr '\n' ' ' <"$errlog"))"
      continue
    fi
    want="$PROBE_PLATFORM"
    case " $AMD64_ONLY " in *" ${dir#"$REPO_ROOT"/} "*) want="linux/amd64" ;; esac
    printf '%s\n' "$commands" >"$cmdlist"
    while IFS= read -r cmd; do
      checked=$((checked + 1))
      case "$cmd" in
        *"$PROBE_MARKER"*) ;;
        *) failed="$failed ${dir#"$REPO_ROOT"/} (no \$(BUILDX_ARGS))"; continue ;;
      esac
      platforms="$(printf '%s\n' "$cmd" | grep -oE -- '--platform[= ][^ ]+' | tr '\n' ' ')"
      [ "$platforms" = "--platform=$want " ] || failed="$failed ${dir#"$REPO_ROOT"/} ($platforms)"
    done <"$cmdlist"
  done
  rm -f "$errlog" "$cmdlist"

  [ "$checked" -gt 0 ] # the glob must find image targets at all
  if [ -n "$failed" ]; then
    echo "image targets not building through BUILDX_ARGS for exactly PLATFORM:$failed" >&2
    exit 1
  fi
}
