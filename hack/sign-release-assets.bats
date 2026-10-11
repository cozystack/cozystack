#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for hack/sign-release-assets.sh.
#
# syft and cosign are replaced by stubs on PATH: a keyless signature needs a
# GitHub OIDC token, which no unit runner has. The stubs record their arguments
# and write placeholder outputs, so the tests hold what the script owns: which
# files it reads and writes, what the checksums cover, which certificate
# identity it verifies against, and that it refuses input it would mis-sign.
#
# The script archives HEAD of the repository it runs in, so each test runs it
# from a throwaway git repository with one commit.
#
# Expected failures are written as `if CMD; then ... exit 1; fi`, never as a
# negated command: see hack/bats-no-negated-assert.bats.
# -----------------------------------------------------------------------------

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
SCRIPT="$REPO_ROOT/hack/sign-release-assets.sh"

# A source repository (current directory), an asset directory with a manifest,
# an ISO stand-in and a cozypkg tarball, and the stubs. COSIGN_VERIFY_EXIT sets
# the exit status of `cosign verify-blob`.
make_fixture() {
    work="$(mktemp -d)"
    mkdir -p "$work/src" "$work/assets" "$work/stub" "$work/pkg"
    cd "$work/src"
    git init -q .
    git config user.email ci@example.invalid
    git config user.name CI
    git config commit.gpgsign false
    git config core.hooksPath /dev/null
    echo 'module example.invalid/fixture' > go.mod
    git add go.mod
    git commit -q -m seed

    echo crds > "$work/assets/cozystack-crds.yaml"
    echo iso > "$work/assets/metal-amd64.iso"
    echo binary > "$work/pkg/cozypkg"
    tar -czf "$work/assets/cozypkg-linux-amd64.tar.gz" -C "$work/pkg" cozypkg

    cat > "$work/stub/syft" <<'STUB'
#!/bin/sh
out=
for arg in "$@"; do
    case $arg in
        dir:*) (cd "${arg#dir:}" && find . -mindepth 1 -maxdepth 2 | sort) > "$STUB_LOG/syft-tree" ;;
        spdx-json=*) out=${arg#spdx-json=} ;;
    esac
done
printf '%s\n' "$@" > "$STUB_LOG/syft-args"
echo '{"spdxVersion":"SPDX-2.3"}' > "$out"
STUB
    cat > "$work/stub/cosign" <<'STUB'
#!/bin/sh
printf '%s\n' "$@" > "$STUB_LOG/cosign-$1"
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
    chmod +x "$work/stub/syft" "$work/stub/cosign"
    export PATH="$work/stub:$PATH" STUB_LOG="$work"
}

# The status is returned explicitly: cozytest.sh appends `return 0` before a
# closing brace at column zero, which would turn a failed run into a success.
sign() {
    "$SCRIPT" v1.2.3 \
        "$work/assets/cozystack-crds.yaml" \
        "$work/assets/metal-amd64.iso" \
        "$work/assets/cozypkg-linux-amd64.tar.gz"
    return $?
}

# Last line of a test body rather than a trap, which the runners forbid.
cleanup() {
    cd /
    rm -rf "$work"
}

@test "writes the SBOM, the checksums over every asset and the SBOM, and the bundle" {
    make_fixture
    sign

    for f in cozystack-sbom.spdx.json cozystack-checksums.txt cozystack-checksums.txt.sigstore.json; do
        if [ ! -s "$work/assets/$f" ]; then
            echo "expected $f to be written" >&2
            exit 1
        fi
    done
    names="$(awk '{ print $2 }' "$work/assets/cozystack-checksums.txt")"
    expected="$(printf '%s\n' cozystack-crds.yaml metal-amd64.iso cozypkg-linux-amd64.tar.gz cozystack-sbom.spdx.json)"
    if [ "$names" != "$expected" ]; then
        printf 'checksums name:\n%s\nexpected:\n%s\n' "$names" "$expected" >&2
        exit 1
    fi
    (cd "$work/assets" && sha256sum --check --quiet cozystack-checksums.txt)

    cleanup
}

@test "the SBOM covers the committed tree and the unpacked cozypkg binaries" {
    make_fixture
    echo 'not committed' > untracked.txt
    sign

    for path in ./source/go.mod ./cozypkg-linux-amd64/cozypkg; do
        if ! grep -qxF "$path" "$work/syft-tree"; then
            echo "expected syft to scan $path; it saw:" >&2
            cat "$work/syft-tree" >&2
            exit 1
        fi
    done
    if grep -qF untracked.txt "$work/syft-tree"; then
        echo "syft scanned a file that is not committed" >&2
        exit 1
    fi
    grep -qxF v1.2.3 "$work/syft-args"

    cleanup
}

@test "signs the checksums and verifies them against the release workflows" {
    make_fixture
    sign

    grep -qxF "$work/assets/cozystack-checksums.txt" "$work/cosign-sign-blob"
    grep -qxF "$work/assets/cozystack-checksums.txt.sigstore.json" "$work/cosign-sign-blob"
    grep -qxF https://token.actions.githubusercontent.com "$work/cosign-verify-blob"
    regexp="$(grep -A1 -xF -- --certificate-identity-regexp "$work/cosign-verify-blob" | tail -n1)"
    for identity in \
        https://github.com/cozystack/cozystack/.github/workflows/tags.yaml@refs/tags/v1.2.3-rc.1 \
        https://github.com/cozystack/cozystack/.github/workflows/promote-rc.yaml@refs/heads/main; do
        if ! printf '%s\n' "$identity" | grep -qE "$regexp"; then
            echo "expected $regexp to accept $identity" >&2
            exit 1
        fi
    done
    for identity in \
        https://github.com/fork/cozystack/.github/workflows/tags.yaml@refs/tags/v1.2.3 \
        https://github.com/cozystack/cozystack/.github/workflows/nightly.yaml@refs/heads/main \
        https://github.com/cozystack/cozystack/.github/workflows/tags.yaml.evil@refs/heads/main; do
        if printf '%s\n' "$identity" | grep -qE "$regexp"; then
            echo "expected $regexp to reject $identity" >&2
            exit 1
        fi
    done

    cleanup
}

@test "docs/release.md tells users to verify the identity the script verifies" {
    regexp="$(sed -n "s/^IDENTITY_REGEXP='\(.*\)'\$/\1/p" "$SCRIPT")"
    if [ -z "$regexp" ]; then
        echo "IDENTITY_REGEXP not found in $SCRIPT" >&2
        exit 1
    fi
    grep -qF -- "--certificate-identity-regexp '$regexp'" "$REPO_ROOT/docs/release.md"
}

@test "fails when the signature does not verify" {
    make_fixture
    export COSIGN_VERIFY_EXIT=1
    if sign; then
        echo "expected a failed verification to fail the script" >&2
        exit 1
    fi
    cleanup
}

@test "refuses a missing asset" {
    make_fixture
    if "$SCRIPT" v1.2.3 "$work/assets/cozystack-crds.yaml" "$work/assets/missing.yaml"; then
        echo "expected a missing asset to be refused" >&2
        exit 1
    fi
    if [ -e "$work/assets/cozystack-checksums.txt" ]; then
        echo "expected nothing to be written" >&2
        exit 1
    fi
    cleanup
}

@test "refuses assets spread over two directories" {
    make_fixture
    mkdir "$work/other"
    echo other > "$work/other/openapi.json"
    if "$SCRIPT" v1.2.3 "$work/assets/cozystack-crds.yaml" "$work/other/openapi.json"; then
        echo "expected assets outside the first asset's directory to be refused" >&2
        exit 1
    fi
    cleanup
}

# promote-rc.yaml downloads every asset of the rc, its signature files included.
@test "refuses one of its own outputs as an input" {
    make_fixture
    echo stale > "$work/assets/cozystack-checksums.txt"
    if "$SCRIPT" v1.2.3 "$work/assets/cozystack-crds.yaml" "$work/assets/cozystack-checksums.txt"; then
        echo "expected the rc's checksums to be refused as an asset" >&2
        exit 1
    fi
    cleanup
}
