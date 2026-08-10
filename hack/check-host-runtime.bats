#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for hack/check-host-runtime.sh
#
# The script warns when a standalone containerd.service or docker.service is
# active alongside the embedded k3s runtime on Ubuntu hosts running the
# cozystack "generic" variant. Warnings go to stderr; exit code is always 0.
#
# Test strategy: each test builds its own temporary stub directory and prepends
# it to PATH to inject a fake `systemctl` (and optionally `du`) binary. The
# script itself honors a small set of COZYSTACK_PREFLIGHT_* environment
# variables to redirect socket/dir probes into the stub tree, so tests do not
# need root privileges or a real systemd host.
#
# Each test removes its stub dir on the last line of its body rather than from
# an EXIT trap: a trap inside an @test replaces the one the bats binary installs
# for its own bookkeeping, and a test that then fails prints no TAP line at all.
# Both runners set -e, so a failed test leaves its stub dir behind, which is what
# you want to look at anyway.
#
# Tests are otherwise self-contained — no shared setup/teardown helpers,
# because cozytest.sh's awk parser only recognizes @test blocks and treats a
# bare `}` on its own line as the end of a test function.
#
# Title syntax constraints (inherited from cozytest.sh's awk parser):
#   - Titles must be delimited by ASCII double quotes; embedded literal
#     double quotes are NOT escaped and will silently truncate the title.
#   - Only alphanumeric characters from the title survive into the shell
#     function name (everything else becomes '_'), so titles that differ
#     only in punctuation collapse to the same function name. Keep titles
#     distinctive in their alphanumeric run.
#
# Run with: hack/cozytest.sh hack/check-host-runtime.bats
#           (or `bats hack/check-host-runtime.bats` if the bats binary is
#           installed; cozytest.sh is the CI path.)
# -----------------------------------------------------------------------------

@test "clean host with no runtime services exits silently" {
  STUB_DIR=$(mktemp -d)

  cat >"$STUB_DIR/systemctl" <<'STUBEOF'
#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "systemd stub"
  exit 0
fi
exit 1
STUBEOF
  chmod +x "$STUB_DIR/systemctl"

  STDERR_FILE="$STUB_DIR/stderr"
  COZYSTACK_CONTAINERD_SOCKET="$STUB_DIR/missing-containerd.sock" \
  COZYSTACK_DOCKER_SOCKET_PATHS="$STUB_DIR/missing-docker1.sock $STUB_DIR/missing-docker2.sock" \
  COZYSTACK_CONTAINERD_DIR="$STUB_DIR/missing-containerd-dir" \
  COZYSTACK_DOCKER_DIR="$STUB_DIR/missing-docker-dir" \
  PATH="$STUB_DIR:$PATH" \
    bash hack/check-host-runtime.sh >"$STUB_DIR/stdout" 2>"$STDERR_FILE"

  [ ! -s "$STDERR_FILE" ]
  [ ! -s "$STUB_DIR/stdout" ]
  rm -rf "$STUB_DIR"
}

@test "standalone containerd service active prints warning" {
  STUB_DIR=$(mktemp -d)

  cat >"$STUB_DIR/systemctl" <<'STUBEOF'
#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "systemd stub"
  exit 0
fi
if [ "$1" = "is-active" ] && [ "$2" = "containerd.service" ]; then
  echo active
  exit 0
fi
exit 1
STUBEOF
  chmod +x "$STUB_DIR/systemctl"

  mkdir -p "$STUB_DIR/var-lib-containerd"
  echo dummy >"$STUB_DIR/var-lib-containerd/dummy"

  STDERR_FILE="$STUB_DIR/stderr"
  COZYSTACK_CONTAINERD_SOCKET="$STUB_DIR/missing-containerd.sock" \
  COZYSTACK_DOCKER_SOCKET_PATHS="$STUB_DIR/missing-docker.sock" \
  COZYSTACK_CONTAINERD_DIR="$STUB_DIR/var-lib-containerd" \
  COZYSTACK_DOCKER_DIR="$STUB_DIR/missing-docker-dir" \
  PATH="$STUB_DIR:$PATH" \
    bash hack/check-host-runtime.sh 2>"$STDERR_FILE"

  grep -q 'standalone containerd.service' "$STDERR_FILE"
  if grep -q 'standalone docker.service' "$STDERR_FILE"; then
    echo "unexpected docker warning found:" >&2
    cat "$STDERR_FILE" >&2
    exit 1
  fi
  # HINT line must name only the detected service, not advise disabling
  # docker.service when only containerd.service is running. The sudo
  # prefix is also required — without it the command silently no-ops
  # for a non-root operator, so the prefix is part of the contract.
  grep -q 'sudo systemctl disable --now containerd.service' "$STDERR_FILE"
  if grep -q 'systemctl disable --now.*docker' "$STDERR_FILE"; then
    echo "HINT unexpectedly mentions docker:" >&2
    cat "$STDERR_FILE" >&2
    exit 1
  fi
  rm -rf "$STUB_DIR"
}

@test "standalone docker service active prints warning" {
  STUB_DIR=$(mktemp -d)

  cat >"$STUB_DIR/systemctl" <<'STUBEOF'
#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "systemd stub"
  exit 0
fi
if [ "$1" = "is-active" ] && [ "$2" = "docker.service" ]; then
  echo active
  exit 0
fi
exit 1
STUBEOF
  chmod +x "$STUB_DIR/systemctl"

  mkdir -p "$STUB_DIR/var-lib-docker"
  echo dummy >"$STUB_DIR/var-lib-docker/dummy"

  STDERR_FILE="$STUB_DIR/stderr"
  COZYSTACK_CONTAINERD_SOCKET="$STUB_DIR/missing-containerd.sock" \
  COZYSTACK_DOCKER_SOCKET_PATHS="$STUB_DIR/missing-docker.sock" \
  COZYSTACK_CONTAINERD_DIR="$STUB_DIR/missing-containerd-dir" \
  COZYSTACK_DOCKER_DIR="$STUB_DIR/var-lib-docker" \
  PATH="$STUB_DIR:$PATH" \
    bash hack/check-host-runtime.sh 2>"$STDERR_FILE"

  grep -q 'standalone docker.service' "$STDERR_FILE"
  if grep -q 'standalone containerd.service' "$STDERR_FILE"; then
    echo "unexpected containerd warning found:" >&2
    cat "$STDERR_FILE" >&2
    exit 1
  fi
  # HINT line must name only the detected service. As in the
  # containerd test, the sudo prefix is part of the contract.
  grep -q 'sudo systemctl disable --now docker.service' "$STDERR_FILE"
  if grep -q 'systemctl disable --now.*containerd' "$STDERR_FILE"; then
    echo "HINT unexpectedly mentions containerd:" >&2
    cat "$STDERR_FILE" >&2
    exit 1
  fi
  rm -rf "$STUB_DIR"
}

@test "both services active prints two warnings and the HINT block" {
  STUB_DIR=$(mktemp -d)

  cat >"$STUB_DIR/systemctl" <<'STUBEOF'
#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "systemd stub"
  exit 0
fi
if [ "$1" = "is-active" ]; then
  case "$2" in
    containerd.service|docker.service) echo active; exit 0 ;;
  esac
fi
exit 1
STUBEOF
  chmod +x "$STUB_DIR/systemctl"

  mkdir -p "$STUB_DIR/var-lib-containerd" "$STUB_DIR/var-lib-docker"

  STDERR_FILE="$STUB_DIR/stderr"
  # Capture exit code explicitly: the script contract says exit 0
  # unconditionally (warning, not blocker). `set -e` in the test
  # function body would already fail on a nonzero exit, but an
  # explicit status check locks in the contract and makes a
  # regression show up as "expected 0, got N" rather than as a
  # generic test failure.
  status=0
  COZYSTACK_CONTAINERD_SOCKET="$STUB_DIR/missing-containerd.sock" \
  COZYSTACK_DOCKER_SOCKET_PATHS="$STUB_DIR/missing-docker.sock" \
  COZYSTACK_CONTAINERD_DIR="$STUB_DIR/var-lib-containerd" \
  COZYSTACK_DOCKER_DIR="$STUB_DIR/var-lib-docker" \
  PATH="$STUB_DIR:$PATH" \
    bash hack/check-host-runtime.sh 2>"$STDERR_FILE" || status=$?

  [ "$status" -eq 0 ]
  grep -q 'standalone containerd.service' "$STDERR_FILE"
  grep -q 'standalone docker.service' "$STDERR_FILE"
  # HINT block must fire whenever warnings exist; otherwise a future silent
  # removal of the HINT would go unnoticed. When both services fire the HINT
  # must list both in a single sudo systemctl disable invocation — the sudo
  # prefix is as important as the systemctl verb, otherwise the operator
  # would be told to run it as a non-root user and quietly fail.
  grep -q 'HINT:' "$STDERR_FILE"
  grep -q 'sudo systemctl disable --now containerd.service docker.service' "$STDERR_FILE"
  rm -rf "$STUB_DIR"
}

@test "failing du does not suppress the containerd warning" {
  STUB_DIR=$(mktemp -d)

  cat >"$STUB_DIR/systemctl" <<'STUBEOF'
#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "systemd stub"
  exit 0
fi
if [ "$1" = "is-active" ] && [ "$2" = "containerd.service" ]; then
  echo active
  exit 0
fi
exit 1
STUBEOF
  chmod +x "$STUB_DIR/systemctl"
  cat >"$STUB_DIR/du" <<'DUEOF'
#!/bin/sh
exit 1
DUEOF
  chmod +x "$STUB_DIR/du"

  mkdir -p "$STUB_DIR/var-lib-containerd"

  STDERR_FILE="$STUB_DIR/stderr"
  COZYSTACK_CONTAINERD_SOCKET="$STUB_DIR/missing-containerd.sock" \
  COZYSTACK_DOCKER_SOCKET_PATHS="$STUB_DIR/missing-docker.sock" \
  COZYSTACK_CONTAINERD_DIR="$STUB_DIR/var-lib-containerd" \
  COZYSTACK_DOCKER_DIR="$STUB_DIR/missing-docker-dir" \
  PATH="$STUB_DIR:$PATH" \
    bash hack/check-host-runtime.sh 2>"$STDERR_FILE"

  grep -q 'standalone containerd.service' "$STDERR_FILE"
  rm -rf "$STUB_DIR"
}

@test "containerd socket fallback fires when systemctl is unavailable" {
  STUB_DIR=$(mktemp -d)

  # The script uses `[ -e "$sock" ]`, not `[ -S ... ]`, so a regular
  # file is a valid stand-in for a unix socket in tests. This also
  # removes any optional runtime dependency on python3 and makes the
  # test unconditional on every CI runner.
  SOCK="$STUB_DIR/containerd.sock"
  touch "$SOCK"

  STDERR_FILE="$STUB_DIR/stderr"
  COZYSTACK_PREFLIGHT_FORCE_NO_SYSTEMCTL=1 \
  COZYSTACK_CONTAINERD_SOCKET="$SOCK" \
  COZYSTACK_DOCKER_SOCKET_PATHS="$STUB_DIR/missing-docker.sock" \
  COZYSTACK_CONTAINERD_DIR="$STUB_DIR/missing-containerd-dir" \
  COZYSTACK_DOCKER_DIR="$STUB_DIR/missing-docker-dir" \
    bash hack/check-host-runtime.sh 2>"$STDERR_FILE"

  grep -q 'standalone containerd.service' "$STDERR_FILE"
  rm -rf "$STUB_DIR"
}

@test "docker socket fallback fires when systemctl is unavailable" {
  STUB_DIR=$(mktemp -d)

  SOCK="$STUB_DIR/docker.sock"
  touch "$SOCK"

  STDERR_FILE="$STUB_DIR/stderr"
  COZYSTACK_PREFLIGHT_FORCE_NO_SYSTEMCTL=1 \
  COZYSTACK_CONTAINERD_SOCKET="$STUB_DIR/missing-containerd.sock" \
  COZYSTACK_DOCKER_SOCKET_PATHS="$SOCK" \
  COZYSTACK_CONTAINERD_DIR="$STUB_DIR/missing-containerd-dir" \
  COZYSTACK_DOCKER_DIR="$STUB_DIR/missing-docker-dir" \
    bash hack/check-host-runtime.sh 2>"$STDERR_FILE"

  grep -q 'standalone docker.service' "$STDERR_FILE"
  if grep -q 'standalone containerd.service' "$STDERR_FILE"; then
    echo "unexpected containerd warning found:" >&2
    cat "$STDERR_FILE" >&2
    exit 1
  fi
  rm -rf "$STUB_DIR"
}

@test "clean host without systemctl exits silently" {
  STUB_DIR=$(mktemp -d)

  STDERR_FILE="$STUB_DIR/stderr"
  COZYSTACK_PREFLIGHT_FORCE_NO_SYSTEMCTL=1 \
  COZYSTACK_CONTAINERD_SOCKET="$STUB_DIR/missing-containerd.sock" \
  COZYSTACK_DOCKER_SOCKET_PATHS="$STUB_DIR/missing-docker1.sock $STUB_DIR/missing-docker2.sock" \
  COZYSTACK_CONTAINERD_DIR="$STUB_DIR/missing-containerd-dir" \
  COZYSTACK_DOCKER_DIR="$STUB_DIR/missing-docker-dir" \
    bash hack/check-host-runtime.sh >"$STUB_DIR/stdout" 2>"$STDERR_FILE"

  [ ! -s "$STDERR_FILE" ]
  [ ! -s "$STUB_DIR/stdout" ]
  rm -rf "$STUB_DIR"
}

@test "docker service plus socket still emits exactly one warning" {
  STUB_DIR=$(mktemp -d)

  cat >"$STUB_DIR/systemctl" <<'STUBEOF'
#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "systemd stub"
  exit 0
fi
if [ "$1" = "is-active" ] && [ "$2" = "docker.service" ]; then
  echo active
  exit 0
fi
exit 1
STUBEOF
  chmod +x "$STUB_DIR/systemctl"

  SOCK="$STUB_DIR/docker.sock"
  touch "$SOCK"

  mkdir -p "$STUB_DIR/var-lib-docker"

  STDERR_FILE="$STUB_DIR/stderr"
  COZYSTACK_CONTAINERD_SOCKET="$STUB_DIR/missing-containerd.sock" \
  COZYSTACK_DOCKER_SOCKET_PATHS="$SOCK" \
  COZYSTACK_CONTAINERD_DIR="$STUB_DIR/missing-containerd-dir" \
  COZYSTACK_DOCKER_DIR="$STUB_DIR/var-lib-docker" \
  PATH="$STUB_DIR:$PATH" \
    bash hack/check-host-runtime.sh 2>"$STDERR_FILE"

  count=$(grep -c 'standalone docker.service' "$STDERR_FILE")
  if [ "$count" != "1" ]; then
    echo "expected exactly one docker warning, got $count" >&2
    cat "$STDERR_FILE" >&2
    exit 1
  fi
  rm -rf "$STUB_DIR"
}

@test "docker socket paths with glob chars do not expand" {
  STUB_DIR=$(mktemp -d)

  # Create two directories that a naive `for sock in $PATHS` loop
  # would glob-expand and treat as existing "sockets". With the
  # array-based parsing the literal path "$STUB_DIR/var-lib-*" does
  # not exist and no warning must fire.
  mkdir -p "$STUB_DIR/var-lib-docker" "$STUB_DIR/var-lib-containerd"

  cat >"$STUB_DIR/systemctl" <<'STUBEOF'
#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "systemd stub"
  exit 0
fi
exit 1
STUBEOF
  chmod +x "$STUB_DIR/systemctl"

  STDERR_FILE="$STUB_DIR/stderr"
  COZYSTACK_CONTAINERD_SOCKET="$STUB_DIR/missing-containerd.sock" \
  COZYSTACK_DOCKER_SOCKET_PATHS="$STUB_DIR/var-lib-*" \
  COZYSTACK_CONTAINERD_DIR="$STUB_DIR/missing-containerd-dir" \
  COZYSTACK_DOCKER_DIR="$STUB_DIR/missing-docker-dir" \
  PATH="$STUB_DIR:$PATH" \
    bash hack/check-host-runtime.sh 2>"$STDERR_FILE"

  if grep -q 'standalone docker.service' "$STDERR_FILE"; then
    echo "glob pattern expanded — docker warning should not fire:" >&2
    cat "$STDERR_FILE" >&2
    exit 1
  fi
  rm -rf "$STUB_DIR"
}

@test "containerd service plus socket still emits exactly one warning" {
  STUB_DIR=$(mktemp -d)

  cat >"$STUB_DIR/systemctl" <<'STUBEOF'
#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "systemd stub"
  exit 0
fi
if [ "$1" = "is-active" ] && [ "$2" = "containerd.service" ]; then
  echo active
  exit 0
fi
exit 1
STUBEOF
  chmod +x "$STUB_DIR/systemctl"

  # The script uses `[ -e "$sock" ]`, not `[ -S ... ]`, so a regular
  # file is a valid stand-in for a unix socket in tests. This also
  # removes any optional runtime dependency on python3 and makes the
  # test unconditional on every CI runner.
  SOCK="$STUB_DIR/containerd.sock"
  touch "$SOCK"

  mkdir -p "$STUB_DIR/var-lib-containerd"

  STDERR_FILE="$STUB_DIR/stderr"
  COZYSTACK_CONTAINERD_SOCKET="$SOCK" \
  COZYSTACK_DOCKER_SOCKET_PATHS="$STUB_DIR/missing-docker.sock" \
  COZYSTACK_CONTAINERD_DIR="$STUB_DIR/var-lib-containerd" \
  COZYSTACK_DOCKER_DIR="$STUB_DIR/missing-docker-dir" \
  PATH="$STUB_DIR:$PATH" \
    bash hack/check-host-runtime.sh 2>"$STDERR_FILE"

  count=$(grep -c 'standalone containerd.service' "$STDERR_FILE")
  if [ "$count" != "1" ]; then
    echo "expected exactly one containerd warning, got $count" >&2
    cat "$STDERR_FILE" >&2
    exit 1
  fi
  rm -rf "$STUB_DIR"
}
