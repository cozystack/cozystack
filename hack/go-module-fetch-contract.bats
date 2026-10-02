#!/usr/bin/env bats
# Contract for surviving a transient proxy.golang.org failure (an HTTP/2
# stream reset, "stream error ... INTERNAL_ERROR"), which otherwise fails an
# image build or the unit test job outright. Three measures, each pinned here:
#
# 1. GOPROXY ends in "|direct", not Go's default ",direct": after a comma Go
#    falls back to direct only on a 404 or 410, after a pipe on any error. Image
#    builds get it as a build arg from hack/common-envs.mk, and a build arg
#    reaches a stage only when that stage declares it, so every Go builder
#    stage must say `ARG GOPROXY`. Direct fetching needs git, which the alpine
#    golang images lack, so there the retry below is the only cover. Runner
#    jobs that run Go get it as env.
# 2. Every explicit `go mod download` is retried a bounded number of times.
#    Runner jobs call hack/go-mod-download.sh. Dockerfiles carry the loop
#    inline, in the one form RETRY_LOOP below pins: their build contexts
#    differ, so no single script can be copied in, and a named build context
#    passed through the shared BUILDX_ARGS makes buildx refuse every Dockerfile
#    pinned to a frontend older than dockerfile:1.4.
# 3. The unit test job restores the module cache, and the main-branch warmer
#    writes it under the same key, so a PR's first run starts warm.
#
# Written for POSIX sh: hack/cozytest.sh sources this file into /bin/sh.
#
# Requires: awk, make, sed.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
RETRY="$REPO_ROOT/hack/go-mod-download.sh"
WORKFLOWS="$REPO_ROOT/.github/workflows"
GOPROXY_ENV='GOPROXY: "https://proxy.golang.org|direct"'
RETRY_LOOP='^RUN for attempt in 1 2 3; do ([A-Z_]+=[^ ]+ )*go mod download && break; \[ "\$attempt" = 3 \] && exit 1; sleep \$\(\(attempt \* 10\)\); done$'

# Runner jobs that run Go without a module cache by decision: release jobs,
# run a handful of times per release, get GOPROXY only.
NO_CACHE="tags.yaml:build-amd64
promote-rc.yaml:promote"

dockerfiles() {
  find "$REPO_ROOT"/packages -path '*/charts' -prune -o \( -name Dockerfile -o -name Containerfile \) -type f -print | sort
}

# One line per Go builder stage: "<declares ARG GOPROXY 0|1> <FROM line>". A
# stage is a Go builder when its base is a golang image, an ARG defaulting to
# one, or an earlier Go builder stage.
go_stages() {
  awk '
    function flush() { if (from != "" && gostage) printf "%d %s\n", declared, from }
    /^ARG[[:space:]]+[A-Za-z_][A-Za-z0-9_]*=/ && from == "" {
      kv = $2; eq = index(kv, "="); argdefault[substr(kv, 1, eq - 1)] = substr(kv, eq + 1)
    }
    /^FROM[[:space:]]/ {
      flush(); from = $0; declared = 0; gostage = 0; name = ""; base = ""
      for (i = 2; i <= NF; i++) {
        if ($i !~ /^--/ && base == "") base = $i
        else if (tolower($i) == "as" && i < NF) name = $(i + 1)
      }
      ref = base; gsub(/^\$\{?|\}$/, "", ref)
      if (base ~ /^\$/ && ref in argdefault) base = argdefault[ref]
      if (base ~ /(^|\/)golang[:@]/ || base in gonames) gostage = 1
      if (gostage && name != "") gonames[name] = 1
      next
    }
    /^ARG[[:space:]]+GOPROXY([[:space:]]|$)/ { declared = 1 }
    END { flush() }
  ' "$1"
}

# One line per alpine golang stage that runs `go build` with neither git nor a
# prior `go mod download`: the `|direct` fallback needs git, so such a stage
# has no cover at all when the proxy drops a stream mid-build.
uncovered_alpine_builds() {
  sed -e ':a' -e '/\\$/N; s/\\\n//; ta' "$1" | awk '
    function flush() { if (alpine && build && !git && !download) print from }
    /^FROM[[:space:]]/ { flush(); from = $0; alpine = ($0 ~ /golang:[^ ]*-alpine/); build = 0; git = 0; download = 0; next }
    /^RUN[[:space:]]/ && /apk add[^&;]*[[:space:]]git([[:space:]]|$)/ { git = 1 }
    /^RUN[[:space:]]/ && /go mod download/ { download = 1 }
    /^RUN[[:space:]]/ && /go build/ && !download { build = 1 }
    END { flush() }
  '
}

# RUN instructions with continuation lines joined, one per line.
run_lines() {
  sed -e ':a' -e '/\\$/N; s/\\\n//; ta' "$1" | grep '^RUN[[:space:]]' || true
}

# Lines of one job's block, comment lines dropped so a commented-out step never
# satisfies a pin.
job_block() {
  awk -v job="  $1:" '
    $0 == job { inside = 1; next }
    /^  [a-zA-Z0-9_-]+:$/ { inside = 0 }
    inside' "$2" | grep -v '^[[:space:]]*#' || true
}

job_names() {
  awk '/^jobs:$/ { j = 1; next } j && /^[^ ]/ { j = 0 } j && /^  [a-zA-Z0-9_-]+:$/ { sub(/:$/, ""); sub(/^  /, ""); print }' "$1"
}

# A job runs Go on the runner when it sets Go up, calls the go tool, or runs a
# root make target that does. A pattern rather than a helper function:
# hack/cozytest.sh appends `return 0` to every line that is exactly `}`, so a
# helper's exit status never reaches the caller there.
GO_JOB_RE='actions/setup-go@|(^|[^A-Za-z0-9_-])go (build|test|run|mod|generate|install)[[:space:]]|(^|[^A-Za-z0-9_-])make[^#]*[[:space:]](unit-tests|test-controllers|assets|assets-cozypkg|openapi-json|generate)([[:space:]]|$)'

@test "every Go builder stage declares ARG GOPROXY" {
  failed=""
  checked=0
  stagelist="$(mktemp)"
  for f in $(dockerfiles); do
    go_stages "$f" >"$stagelist"
    while read -r declared from; do
      checked=$((checked + 1))
      [ "$declared" = 1 ] || failed="$failed
  ${f#"$REPO_ROOT"/}: $from"
    done <"$stagelist"
  done
  rm -f "$stagelist"
  [ "$checked" -gt 0 ]
  if [ -n "$failed" ]; then
    echo "Go builder stages that never receive the GOPROXY build arg:$failed" >&2
    exit 1
  fi
}

@test "a Go stage built FROM an ARG or an earlier Go stage is still a Go stage" {
  fixture="$(mktemp)"
  printf '%s\n' 'ARG builder_image=docker.io/library/golang:1.27' 'FROM ${builder_image} AS a' 'ARG GOPROXY' 'FROM a AS b' 'FROM alpine' >"$fixture"
  out="$(go_stages "$fixture")"
  rm -f "$fixture"
  [ "$out" = '1 FROM ${builder_image} AS a
0 FROM a AS b' ]
}

@test "image builds pass the pipe GOPROXY through BUILDX_ARGS" {
  out="$(make --no-print-directory -s -n -C "$REPO_ROOT/packages/system/cozystack-controller" image COZYSTACK_VERSION=0.0.0)"
  echo "$out" | grep -qF -- "--build-arg 'GOPROXY=https://proxy.golang.org|direct'"
}

@test "a host GOPROXY does not reach image builds, IMAGE_GOPROXY does" {
  # A host mirror on localhost or a file:// proxy is unreachable from inside
  # buildkit, so the host's GOPROXY must not leak into the build.
  dir="$REPO_ROOT/packages/system/cozystack-controller"
  out="$(GOPROXY=http://localhost:3000 make --no-print-directory -s -n -C "$dir" image COZYSTACK_VERSION=0.0.0)"
  echo "$out" | grep -qF -- "--build-arg 'GOPROXY=https://proxy.golang.org|direct'"
  out="$(IMAGE_GOPROXY=https://mirror.example.com make --no-print-directory -s -n -C "$dir" image COZYSTACK_VERSION=0.0.0)"
  echo "$out" | grep -qF -- "--build-arg 'GOPROXY=https://mirror.example.com'"
}

@test "the shared buildx args carry no named build context" {
  # metallb is one of the images pinned to dockerfile:1.2, which buildx refuses
  # to build at all once --build-context is on the command line.
  out="$(make --no-print-directory -s -n -C "$REPO_ROOT/packages/system/metallb" image COZYSTACK_VERSION=0.0.0)"
  echo "$out" | grep -q 'docker buildx build'
  if echo "$out" | grep -q -- '--build-context'; then
    echo "BUILDX_ARGS reaches dockerfile:1.2 images with a named build context" >&2
    false
  fi
}

@test "every go mod download in a Dockerfile runs inside the bounded retry loop" {
  failed=""
  checked=0
  runlist="$(mktemp)"
  for f in $(dockerfiles); do
    run_lines "$f" | grep 'go mod download' >"$runlist" || true
    while IFS= read -r line; do
      checked=$((checked + 1))
      printf '%s\n' "$line" | grep -qE -- "$RETRY_LOOP" || failed="$failed
  ${f#"$REPO_ROOT"/}: $line"
    done <"$runlist"
  done
  rm -f "$runlist"
  [ "$checked" -gt 0 ]
  if [ -n "$failed" ]; then
    echo "go mod download outside the bounded retry loop:$failed" >&2
    exit 1
  fi
}

@test "every alpine golang stage downloads its modules before go build" {
  failed=""
  for f in $(dockerfiles); do
    out="$(uncovered_alpine_builds "$f")"
    [ -z "$out" ] || failed="$failed
  ${f#"$REPO_ROOT"/}: $out"
  done
  if [ -n "$failed" ]; then
    echo "alpine stages where a proxy error mid-build has neither fallback nor retry:$failed" >&2
    exit 1
  fi
}

@test "an alpine stage is covered by git or by a download before the build" {
  fixture="$(mktemp)"
  printf '%s\n' 'FROM golang:1.27-alpine AS a' 'RUN go build .' \
    'FROM golang:1.27-alpine AS b' 'RUN apk add --no-cache make git' 'RUN go build .' \
    'FROM golang:1.27-alpine AS c' 'RUN go mod download' 'RUN go build .' \
    'FROM golang:1.27 AS d' 'RUN go build .' >"$fixture"
  out="$(uncovered_alpine_builds "$fixture")"
  rm -f "$fixture"
  [ "$out" = 'FROM golang:1.27-alpine AS a' ]
}

@test "the retry loop pattern accepts the loop and rejects a bare or unbounded download" {
  printf '%s\n' 'RUN for attempt in 1 2 3; do GOOS=$TARGETOS go mod download && break; [ "$attempt" = 3 ] && exit 1; sleep $((attempt * 10)); done' | grep -qE -- "$RETRY_LOOP"
  for bad in 'RUN go mod download' \
    'RUN for attempt in 1 2 3; do go mod download && break; sleep $((attempt * 10)); done' \
    'RUN for attempt in 1 2 3; do go mod download && break; [ "$attempt" = 3 ] && exit 1; sleep $((attempt * 10)); done && go mod download'; do
    if printf '%s\n' "$bad" | grep -qE -- "$RETRY_LOOP"; then
      echo "pattern accepts: $bad" >&2
      false
    fi
  done
}

@test "the retry script succeeds once go mod download does, within its attempts" {
  work="$(mktemp -d)"
  printf '%s\n' '#!/bin/sh' 'n=$(cat "$CALLS" 2>/dev/null || echo 0); n=$((n + 1)); echo "$n" >"$CALLS"' '[ "$n" -ge "$SUCCEED_ON" ]' >"$work/go"
  chmod +x "$work/go"
  rc=0
  PATH="$work:$PATH" CALLS="$work/calls" SUCCEED_ON=3 GO_MOD_DOWNLOAD_BACKOFF=0 sh "$RETRY" >/dev/null 2>&1 || rc=$?
  calls="$(cat "$work/calls")"
  rm -rf "$work"
  [ "$rc" -eq 0 ]
  [ "$calls" -eq 3 ]
}

@test "the retry script gives up after its attempts and fails" {
  work="$(mktemp -d)"
  printf '%s\n' '#!/bin/sh' 'n=$(cat "$CALLS" 2>/dev/null || echo 0); echo $((n + 1)) >"$CALLS"' 'exit 1' >"$work/go"
  chmod +x "$work/go"
  rc=0
  PATH="$work:$PATH" CALLS="$work/calls" GO_MOD_DOWNLOAD_BACKOFF=0 sh "$RETRY" >/dev/null 2>&1 || rc=$?
  calls="$(cat "$work/calls")"
  rm -rf "$work"
  [ "$rc" -ne 0 ]
  [ "$calls" -eq 3 ]
}

@test "the retry script refuses a non-numeric backoff instead of looping on it" {
  work="$(mktemp -d)"
  printf '%s\n' '#!/bin/sh' 'echo x >>"$CALLS"' 'exit 1' >"$work/go"
  chmod +x "$work/go"
  rc=0
  PATH="$work:$PATH" CALLS="$work/calls" GO_MOD_DOWNLOAD_BACKOFF=soon sh "$RETRY" >/dev/null 2>&1 || rc=$?
  called=0
  [ -f "$work/calls" ] && called=1
  rm -rf "$work"
  [ "$rc" -ne 0 ]
  [ "$called" -eq 0 ]
}

@test "every runner job that runs Go sets the pipe GOPROXY" {
  failed=""
  checked=0
  for wf in "$WORKFLOWS"/*.yaml "$WORKFLOWS"/*.yml; do
    for job in $(job_names "$wf"); do
      block="$(job_block "$job" "$wf")"
      echo "$block" | grep -qE -- "$GO_JOB_RE" || continue
      checked=$((checked + 1))
      echo "$block" | grep -qF -- "$GOPROXY_ENV" || failed="$failed $(basename "$wf"):$job"
    done
  done
  [ "$checked" -gt 0 ]
  if [ -n "$failed" ]; then
    echo "jobs running Go without GOPROXY=https://proxy.golang.org|direct:$failed" >&2
    exit 1
  fi
}

@test "every runner job that runs Go without setup-go restores the module cache" {
  failed=""
  checked=0
  for wf in "$WORKFLOWS"/*.yaml "$WORKFLOWS"/*.yml; do
    for job in $(job_names "$wf"); do
      block="$(job_block "$job" "$wf")"
      echo "$block" | grep -qE -- "$GO_JOB_RE" || continue
      echo "$block" | grep -q 'actions/setup-go@' && continue
      case "
$NO_CACHE
" in *"
$(basename "$wf"):$job
"*) continue ;; esac
      checked=$((checked + 1))
      echo "$block" | grep -q 'uses: actions/cache@' || failed="$failed $(basename "$wf"):$job"
    done
  done
  [ "$checked" -gt 0 ]
  if [ -n "$failed" ]; then
    echo "jobs running Go with neither setup-go nor a module cache:$failed" >&2
    exit 1
  fi
}

# The distinct go-mod cache keys one job uses, so a restore and a save that
# disagree show up as two lines.
go_mod_key() {
  job_block "$1" "$WORKFLOWS/$2" | grep -E '^[[:space:]]+key: go-mod-' | sed -e 's/^[[:space:]]*key: //' | sort -u
}

@test "the unit test job and the main warmer share one module cache key over every go.sum" {
  checks="$(go_mod_key checks pull-requests.yaml)"
  warm="$(go_mod_key warm-cache build-main.yaml)"
  [ -n "$checks" ]
  [ "$checks" = "go-mod-\${{ runner.os }}-\${{ hashFiles('**/go.sum') }}" ]
  [ "$warm" = "$checks" ]
}

@test "the main warmer saves the module cache and both jobs prefetch every module through the retry" {
  job_block warm-cache "$WORKFLOWS/build-main.yaml" | grep -q 'uses: actions/cache/save@'
  if job_block warm-cache "$WORKFLOWS/build-main.yaml" | grep -q 'restore-keys:'; then
    echo "the warmer must not seed a new key from a partial match" >&2
    false
  fi
  for pair in pull-requests.yaml:checks build-main.yaml:warm-cache; do
    block="$(job_block "${pair#*:}" "$WORKFLOWS/${pair%%:*}")"
    echo "$block" | grep -qF "git ls-files go.mod '*/go.mod'"
    echo "$block" | grep -qF 'hack/go-mod-download.sh'
  done
}
