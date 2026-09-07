#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# published-appliance-digest.sh decides whether `make build` spends ~15 minutes
# rebuilding the VyOS appliance from a floating apt mirror, or stamps a digest
# that is already published. Both wrong answers are expensive in different ways,
# so both directions are pinned here:
#
#   a false MISS  costs one rebuild (annoying, self-correcting)
#   a false HIT   stamps a reference nothing can pull, and the failure surfaces
#                 much later as a CDI DataVolume that never imports
#
# So the script is required to answer "build it" for everything it is not
# certain about, and this suite drives the uncertain cases: an unreachable
# registry, a proxy answering with something that is not a digest, and a
# truncated digest.
#
# The OCI_EXPORT_DIR case is not an optimisation detail. A fork PR's build
# exports OCI archives and its upload uses `if-no-files-found: error`; reusing a
# published digest there would skip buildx, write no archive, and fail the job
# on a missing file rather than on anything real (#3257). That case must answer
# "build it" even when the manifest is sitting right there in the registry,
# which is what the first test pins.
#
# Harness note: the CI path is hack/cozytest.sh, NOT real bats. There is no
# `run`, `$status`, `$output`, `skip`, or setup()/teardown(); each test runs as a
# shell function under `set -eu -x`, so a non-zero exit is the failure, and a
# test that wants scratch space makes its own. A command expected to FAIL is
# therefore wrapped in an `if`, never asserted on `$status`.
#
# Run with: hack/cozytest.sh hack/vyos-appliance-cache_test.bats
# -----------------------------------------------------------------------------

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
SCRIPT="$REPO_ROOT/packages/system/vyos-router-image/hack/published-appliance-digest.sh"

REF="registry.example/site-router/vyos-router-disk:appliance-1.5-rolling-20260904"
GOOD="sha256:deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"

# Put a fake `docker` on PATH that prints $FAKE_DOCKER_OUT and exits
# $FAKE_DOCKER_RC. No real registry is contacted by this suite.
fake_docker() {
    FAKE_BIN=$(mktemp -d)
    cat > "$FAKE_BIN/docker" <<SH
#!/bin/sh
printf '%s' "\${FAKE_DOCKER_OUT:-}"
exit \${FAKE_DOCKER_RC:-0}
SH
    chmod +x "$FAKE_BIN/docker"
    PATH="$FAKE_BIN:$PATH"
    export PATH
}

@test "an export build declines the cache even when the manifest is published" {
    fake_docker
    FAKE_DOCKER_OUT="$GOOD"
    FAKE_DOCKER_RC=0
    OCI_EXPORT_DIR="$FAKE_BIN/oci"
    export FAKE_DOCKER_OUT FAKE_DOCKER_RC OCI_EXPORT_DIR

    # Would be a hit if OCI_EXPORT_DIR were unset; must still say "build it".
    if out="$("$SCRIPT" "$REF")"; then
        echo "expected a decline under OCI_EXPORT_DIR, got: $out"
        false
    fi
}

@test "a published manifest is reported as its digest" {
    fake_docker
    FAKE_DOCKER_OUT="$GOOD"
    FAKE_DOCKER_RC=0
    unset OCI_EXPORT_DIR || true
    export FAKE_DOCKER_OUT FAKE_DOCKER_RC

    out="$("$SCRIPT" "$REF")"
    [ "$out" = "$GOOD" ]
}

@test "an unreachable registry is a miss, not a hit" {
    fake_docker
    FAKE_DOCKER_OUT=""
    FAKE_DOCKER_RC=1
    unset OCI_EXPORT_DIR || true
    export FAKE_DOCKER_OUT FAKE_DOCKER_RC

    if out="$("$SCRIPT" "$REF")"; then
        echo "expected a miss on registry failure, got: $out"
        false
    fi
}

@test "a non-digest answer is a miss, not a hit" {
    fake_docker
    # A proxy or login wall answering 200 with a body instead of a manifest.
    FAKE_DOCKER_OUT="<html>401 Unauthorized</html>"
    FAKE_DOCKER_RC=0
    unset OCI_EXPORT_DIR || true
    export FAKE_DOCKER_OUT FAKE_DOCKER_RC

    if out="$("$SCRIPT" "$REF")"; then
        echo "expected a miss on a non-digest answer, got: $out"
        false
    fi
}

@test "a truncated digest is a miss, not a hit" {
    fake_docker
    # Right prefix, wrong length: the case a prefix-only check would let through
    # and then stamp as if it were a full digest.
    FAKE_DOCKER_OUT="sha256:deadbeef"
    FAKE_DOCKER_RC=0
    unset OCI_EXPORT_DIR || true
    export FAKE_DOCKER_OUT FAKE_DOCKER_RC

    if out="$("$SCRIPT" "$REF")"; then
        echo "expected a miss on a truncated digest, got: $out"
        false
    fi
}

@test "the cache tag is keyed on VYOS_VERSION and suppressed for export builds" {
    MK="$REPO_ROOT/packages/system/vyos-router-image/Makefile"

    # The key must be the pin-derived version, not the build-unique IMAGE_TAG:
    # keying on IMAGE_TAG would miss on every build and the cache would never
    # hit, which is the failure mode that looks like "it just still rebuilds".
    grep -q '^APPLIANCE_TAG = appliance-\$(VYOS_VERSION)$' "$MK"

    # And it must drop out under OCI_EXPORT_DIR, or a multi-tag oci-output build
    # writes two refs into index.json and skopeo refuses the archive (#3257).
    grep -q '^APPLIANCE_CACHE_TAG = \$(if \$(strip \$(OCI_EXPORT_DIR)),,--tag \$(APPLIANCE_REF))$' "$MK"

    # The live-build must hang off the miss branch only. If `qcow2` were still a
    # prerequisite of image-vyos-router-disk, every build would run it and the
    # whole change would be inert.
    grep -q '^image-vyos-router-disk:$' "$MK"
    grep -q '^appliance-publish: qcow2$' "$MK"
}
