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
# gradle, dpkg-buildpackage, a node package manager or a script whose file name
# contains "build" and ends in .sh, at command position (after RUN and its
# --flag options, `&&`, `;` or `||`, past any VAR=value prefixes). Such a stage
# must run on the build platform (see stages() below) and use TARGETARCH or
# TARGETPLATFORM on a RUN or ENV line, unless its output carries no
# architecture at all (ARCH_NEUTRAL below). A bare
# `ARG TARGETARCH` does not count: it declares the variable, and a `go build`
# that never reads it still builds for the host. The check is per stage, not
# per command: a stage with `RUN echo $TARGETARCH` and a bare `go build` would
# pass.
#
# Requires: awk, sed.

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"

# Outputs with no architecture, one entry per line: a whole file, or
# "<file>:<stage>" when only that stage is neutral and the file's other
# stages still have to target TARGETARCH. The console, the Harbor portal and
# shadowbox's webpack stage build static JS; piraeus-server builds Java .debs
# that linstor-server's debian/control declares `Architecture: all`.
ARCH_NEUTRAL="packages/apps/vpn/images/shadowbox/Dockerfile:app
packages/system/dashboard/images/console/Containerfile
packages/system/harbor/images/harbor-portal/Dockerfile
packages/system/linstor/images/piraeus-server/Dockerfile"

# is_neutral <file> <stage> <list>: 1 when the list names the file or that stage of it.
is_neutral() {
  printf '%s\n' "$3" | awk -v f="$1" -v s="$1:$2" '$0 == f || $0 == s { hit = 1 } END { print hit + 0 }'
}

# One line per stage: "<compiles 0|1> <targets 0|1> <native 0|1> <name> <FROM line>".
# A stage is native when its FROM carries --platform=$BUILDPLATFORM, or when it
# builds FROM an earlier native stage: a stage inherits the platform of the
# stage it starts from. An unnamed stage is reported as "-".
# Continuation lines are joined first so a command after `&& \` is seen at
# command position.
stages() {
  sed -e ':a' -e '/\\$/N; s/\\\n//; ta' "$1" | awk '
    function flush() { if (from != "") printf "%d %d %d %s %s\n", compiles, targets, native, name, from }
    /^FROM[[:space:]]/ {
      flush(); from = $0; compiles = 0; targets = 0; native = 0; name = "-"; base = ""
      for (i = 2; i <= NF; i++) {
        if ($i ~ /^--platform=(\$BUILDPLATFORM|\$\{BUILDPLATFORM\})$/) native = 1
        else if ($i !~ /^--/ && base == "") base = $i
        else if (tolower($i) == "as" && i < NF) name = $(i + 1)
      }
      if (base in nativestage) native = 1
      if (native && name != "-") nativestage[name] = 1
      next
    }
    /^RUN[[:space:]]/ && /(^RUN([[:space:]]+--[^[:space:]]+)*|&&|;|[|][|])[[:space:]]+([A-Za-z_][A-Za-z0-9_]*=[^[:space:]]*[[:space:]]+)*(go[[:space:]]+(build|test|install)|make|gradle|[.]\/gradlew|dpkg-buildpackage|pnpm|npm|yarn|[^[:space:]]*build[^[:space:]\/]*[.]sh)([[:space:]]|$)/ { compiles = 1 }
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
    stages "$f" >"$stagelist"
    while read -r compiles targets native name from; do
      [ "$compiles" = 1 ] || continue
      checked=$((checked + 1))
      if [ "$native" = 0 ]; then
        failed="$failed
  $rel: $from (not on BUILDPLATFORM)"
        continue
      fi
      if [ "$targets" = 0 ] && [ "$(is_neutral "$rel" "$name" "$ARCH_NEUTRAL")" != 1 ]; then
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
  [ "$out" = "1 0 0 builder FROM golang AS builder" ]
}

@test "a build script with a suffix after build still counts as a compile" {
  fixture="$(mktemp)"
  printf '%s\n' 'FROM --platform=$BUILDPLATFORM golang AS build' 'RUN ./hack/build-go.sh' >"$fixture"
  out="$(stages "$fixture")"
  rm -f "$fixture"
  [ "$out" = '1 0 1 build FROM --platform=$BUILDPLATFORM golang AS build' ]
}

@test "a script under a build/ directory is not a build script" {
  fixture="$(mktemp)"
  printf '%s\n' 'FROM golang AS x' 'RUN ./build/install.sh' >"$fixture"
  out="$(stages "$fixture")"
  rm -f "$fixture"
  [ "$out" = "0 0 0 x FROM golang AS x" ]
}

@test "a stage built FROM an earlier BUILDPLATFORM stage runs on the build platform too" {
  fixture="$(mktemp)"
  printf '%s\n' 'FROM --platform=$BUILDPLATFORM node AS source' 'RUN true' 'FROM source AS app' 'RUN npm ci' 'FROM node AS emulated' 'RUN npm ci' 'FROM emulated AS child' 'RUN go build .' >"$fixture"
  out="$(stages "$fixture")"
  rm -f "$fixture"
  [ "$(printf '%s\n' "$out" | sed -n 2p)" = "1 0 1 app FROM source AS app" ]
  [ "$(printf '%s\n' "$out" | sed -n 3p)" = "1 0 0 emulated FROM node AS emulated" ]
  # A stage built from an emulated stage stays emulated.
  [ "$(printf '%s\n' "$out" | sed -n 4p)" = "1 0 0 child FROM emulated AS child" ]
}

@test "an arch-neutral stage entry excuses only that stage" {
  [ "$(is_neutral packages/x/Dockerfile app 'packages/x/Dockerfile:app')" = 1 ]
  [ "$(is_neutral packages/x/Dockerfile ss 'packages/x/Dockerfile:app')" = 0 ]
  [ "$(is_neutral packages/y/Dockerfile any 'packages/y/Dockerfile')" = 1 ]
}
