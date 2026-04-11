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
# Tests are self-contained — no shared setup/teardown helpers, because
# cozytest.sh's awk parser only recognizes @test blocks and treats a bare `}`
# on its own line as the end of a test function.
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

  rm -rf "$STUB_DIR"
}

@test "both services active prints two warnings" {
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
  COZYSTACK_CONTAINERD_SOCKET="$STUB_DIR/missing-containerd.sock" \
  COZYSTACK_DOCKER_SOCKET_PATHS="$STUB_DIR/missing-docker.sock" \
  COZYSTACK_CONTAINERD_DIR="$STUB_DIR/var-lib-containerd" \
  COZYSTACK_DOCKER_DIR="$STUB_DIR/var-lib-docker" \
  PATH="$STUB_DIR:$PATH" \
    bash hack/check-host-runtime.sh 2>"$STDERR_FILE"

  grep -q 'standalone containerd.service' "$STDERR_FILE"
  grep -q 'standalone docker.service' "$STDERR_FILE"

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

@test "socket only fallback fires when systemctl is unavailable" {
  STUB_DIR=$(mktemp -d)
  SOCK="$STUB_DIR/containerd.sock"
  if ! command -v python3 >/dev/null 2>&1; then
    echo "python3 not available - skipping socket fallback test" >&2
    rm -rf "$STUB_DIR"
    return 0
  fi
  python3 -c 'import socket,sys; s=socket.socket(socket.AF_UNIX); s.bind(sys.argv[1])' "$SOCK"

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
