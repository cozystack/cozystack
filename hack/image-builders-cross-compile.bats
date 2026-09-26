#!/usr/bin/env bats
# A builder stage that compiles something for the image must run on the build
# platform. `FROM --platform=$BUILDPLATFORM` keeps the toolchain native under a
# multi-platform build; without it the compiler itself runs under QEMU, which
# is slow and, for Go, unreliable (the migration-controller Dockerfile records
# a "found bad pointer in Go heap" crash from exactly that). A native builder
# must then be told which architecture to produce: a `go build` with no GOARCH
# silently emits a binary for the build host, and nothing fails until a node
# of the other architecture pulls it.
#
# A stage compiles when one of its RUN commands is go build/test/install, make,
# gradle, dpkg-buildpackage, a node package manager or a *build.sh script, at
# command position (after RUN and its --flag options, `&&`, `;` or `||`, past
# any VAR=value prefixes). Such a stage must sit on a BUILDPLATFORM FROM and
# use TARGETARCH or TARGETPLATFORM on a RUN or ENV line, unless its output
# carries no architecture at all (ARCH_NEUTRAL below). A bare
# `ARG TARGETARCH` does not count: it declares the variable, and a `go build`
# that never reads it still builds for the host. The check is per stage, not
# per command: a stage with `RUN echo $TARGETARCH` and a bare `go build` would
# pass.
#
# Written for POSIX sh: hack/cozytest.sh sources this file into /bin/sh.
#
# Requires: awk, sed.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"

# Outputs with no architecture: the console builds static JS; piraeus-server
# builds Java .debs that linstor-server's debian/control declares
# `Architecture: all`.
ARCH_NEUTRAL="packages/system/dashboard/images/console/Containerfile
packages/system/linstor/images/piraeus-server/Dockerfile"

# One line per stage: "<compiles 0|1> <targets 0|1> <FROM line>".
# Continuation lines are joined first so a command after `&& \` is seen at
# command position.
stages() {
  sed -e ':a' -e '/\\$/N; s/\\\n//; ta' "$1" | awk '
    function flush() { if (from != "") printf "%d %d %s\n", compiles, targets, from }
    /^FROM[[:space:]]/ { flush(); from = $0; compiles = 0; targets = 0; next }
    /^RUN[[:space:]]/ && /(^RUN([[:space:]]+--[^[:space:]]+)*|&&|;|[|][|])[[:space:]]+([A-Za-z_][A-Za-z0-9_]*=[^[:space:]]*[[:space:]]+)*(go[[:space:]]+(build|test|install)|make|gradle|[.]\/gradlew|dpkg-buildpackage|pnpm|npm|yarn|[^[:space:]]*build[.]sh)([[:space:]]|$)/ { compiles = 1 }
    /^(RUN|ENV)[[:space:]]/ && /TARGETARCH|TARGETPLATFORM/ { targets = 1 }
    END { flush() }
  '
}

@test "every compiling builder stage runs on BUILDPLATFORM and targets TARGETARCH" {
  failed=""
  checked=0
  stagelist="$(mktemp)"
  # Package paths carry no whitespace (the same assumption hack/build-matrix.sh
  # makes), so word-splitting find's output is safe.
  for f in $(find "$REPO_ROOT"/packages -path '*/charts' -prune -o \( -name Dockerfile -o -name Containerfile \) -type f -print | sort); do
    rel="${f#"$REPO_ROOT"/}"
    neutral=0
    case "$ARCH_NEUTRAL" in *"$rel"*) neutral=1 ;; esac
    stages "$f" >"$stagelist"
    while read -r compiles targets from; do
      [ "$compiles" = 1 ] || continue
      checked=$((checked + 1))
      case "$from" in
        *'--platform=$BUILDPLATFORM'*|*'--platform=${BUILDPLATFORM}'*) ;;
        *) failed="$failed
  $rel: $from (not on BUILDPLATFORM)"; continue ;;
      esac
      if [ "$neutral" = 0 ] && [ "$targets" = 0 ]; then
        failed="$failed
  $rel: $from (compiles without TARGETARCH)"
      fi
    done <"$stagelist"
  done
  rm -f "$stagelist"

  [ "$checked" -gt 0 ] # the find must reach compiling stages at all
  if [ -n "$failed" ]; then
    echo "builder stages that would compile under emulation or for the wrong arch:$failed" >&2
    exit 1
  fi
}

@test "a compile after RUN options still counts as a compile" {
  fixture="$(mktemp)"
  printf '%s\n' 'FROM golang AS builder' 'RUN --mount=type=cache,target=/root/.cache go build .' >"$fixture"
  out="$(stages "$fixture")"
  rm -f "$fixture"
  [ "$out" = "1 0 FROM golang AS builder" ]
}
