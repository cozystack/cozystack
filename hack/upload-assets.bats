#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for hack/upload-assets.sh.
#
# gh, syft and cosign are replaced by stubs on PATH, and the real
# hack/sign-release-assets.sh runs between them, so the tests hold what the
# upload script owns: which files it uploads, in which order, that the
# checksums name what it uploads, and that nothing is uploaded when signing
# fails.
#
# Both scripts take paths relative to the repository root (hack/..., and
# _out/assets for the release assets), so each test runs from a throwaway git
# repository that commits copies of them, with placeholder assets.
#
# Expected failures are written as `if CMD; then ... exit 1; fi`, never as a
# negated command: see hack/bats-no-negated-assert.bats.
# -----------------------------------------------------------------------------

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"

# The fixture's release assets in upload order, with two cozypkg tarballs where
# a release has one per platform.
assets() {
    printf '%s\n' \
        cozystack-crds.yaml \
        cozystack-operator-talos.yaml \
        cozystack-operator-generic.yaml \
        cozystack-operator-hosted.yaml
    for arch in amd64 arm64; do
        printf '%s\n' \
            "metal-$arch.iso" \
            "metal-$arch.raw.xz" \
            "nocloud-$arch.raw.xz" \
            "kernel-$arch" \
            "initramfs-metal-$arch.xz"
    done
    printf '%s\n' \
        cozypkg-darwin-arm64.tar.gz \
        cozypkg-linux-amd64.tar.gz \
        cozypkg-checksums.txt \
        openapi.json
}

# What hack/upload-assets.sh uploads: the assets, then the three files
# hack/sign-release-assets.sh writes.
expected_uploads() {
    assets
    printf '%s\n' \
        cozystack-source-sbom.spdx.json \
        cozystack-checksums.txt \
        cozystack-checksums.txt.sigstore.json
}

# A repository (current directory) with both scripts committed, every asset
# under _out/assets, and the stubs. gh records one line per call in gh-calls;
# COSIGN_VERIFY_EXIT sets the exit status of `cosign verify-blob`.
make_fixture() {
    work="$(mktemp -d)"
    mkdir -p "$work/src/hack" "$work/src/_out/assets" "$work/stub" "$work/pkg"
    cp "$REPO_ROOT/hack/upload-assets.sh" "$REPO_ROOT/hack/sign-release-assets.sh" "$work/src/hack/"
    cd "$work/src"
    git init -q .
    git config user.email ci@example.invalid
    git config user.name CI
    git config commit.gpgsign false
    git config core.hooksPath /dev/null
    git add hack
    git commit -q -m seed

    echo binary > "$work/pkg/cozypkg"
    for name in $(assets); do
        case $name in
            cozypkg-*.tar.gz) tar -czf "_out/assets/$name" -C "$work/pkg" cozypkg ;;
            *) echo "$name" > "_out/assets/$name" ;;
        esac
    done

    cat > "$work/stub/gh" <<'STUB'
#!/bin/sh
echo "$*" >> "$STUB_LOG/gh-calls"
STUB
    cat > "$work/stub/syft" <<'STUB'
#!/bin/sh
for arg in "$@"; do
    case $arg in
        spdx-json=*) echo '{"spdxVersion":"SPDX-2.3"}' > "${arg#spdx-json=}" ;;
    esac
done
STUB
    cat > "$work/stub/cosign" <<'STUB'
#!/bin/sh
case $1 in
    sign-blob)
        while [ "$#" -gt 0 ]; do
            if [ "$1" = --bundle ]; then echo '{}' > "$2"; fi
            shift
        done
        ;;
    verify-blob) exit "${COSIGN_VERIFY_EXIT:-0}" ;;
esac
STUB
    chmod +x "$work/stub/gh" "$work/stub/syft" "$work/stub/cosign"
    export PATH="$work/stub:$PATH" STUB_LOG="$work" VERSION=v1.2.3
}

# The status is returned explicitly: cozytest.sh appends `return 0` before a
# closing brace at column zero, which would turn a failed run into a success.
upload() {
    hack/upload-assets.sh
    return $?
}

# Last line of a test body rather than a trap, which the runners forbid.
cleanup() {
    cd /
    rm -rf "$work"
}

@test "uploads every asset, then the SBOM, the checksums and the bundle" {
    make_fixture
    upload

    expected="$(expected_uploads | sed 's|^|release upload --clobber v1.2.3 _out/assets/|')"
    if [ "$(cat "$work/gh-calls")" != "$expected" ]; then
        printf 'gh was called with:\n%s\nexpected:\n%s\n' "$(cat "$work/gh-calls")" "$expected" >&2
        exit 1
    fi

    cleanup
}

@test "the checksums name every upload but the checksums and the bundle" {
    make_fixture
    upload

    names="$(awk '{ print $2 }' _out/assets/cozystack-checksums.txt)"
    expected="$(assets; echo cozystack-source-sbom.spdx.json)"
    if [ "$names" != "$expected" ]; then
        printf 'checksums name:\n%s\nexpected:\n%s\n' "$names" "$expected" >&2
        exit 1
    fi

    cleanup
}

@test "uploads nothing when the signature does not verify" {
    make_fixture
    export COSIGN_VERIFY_EXIT=1
    if upload; then
        echo "expected a failed verification to fail the upload" >&2
        exit 1
    fi
    if [ -e "$work/gh-calls" ]; then
        echo "expected no gh call; got:" >&2
        cat "$work/gh-calls" >&2
        exit 1
    fi
    cleanup
}

@test "uploads nothing when an asset is missing" {
    make_fixture
    rm _out/assets/nocloud-arm64.raw.xz
    if upload; then
        echo "expected a missing asset to fail the upload" >&2
        exit 1
    fi
    if [ -e "$work/gh-calls" ]; then
        echo "expected no gh call; got:" >&2
        cat "$work/gh-calls" >&2
        exit 1
    fi
    cleanup
}
