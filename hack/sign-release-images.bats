#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for hack/sign-release-images.sh, and for the two workflow steps
# that run it.
#
# cosign is a stub on PATH: a keyless signature needs a GitHub OIDC token,
# which no unit runner has. The stub records what it is asked to sign and
# answers `verify` from that record, so the tests hold what the script owns:
# which refs it signs, that it signs each one once, which identity it verifies
# against, what --verify-only and --list do, and that a signature which does
# not verify fails the run.
#
# Expected failures are written as `if CMD; then ... exit 1; fi`, never as a
# negated command: see hack/bats-no-negated-assert.bats.
# -----------------------------------------------------------------------------

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
SCRIPT="$REPO_ROOT/hack/sign-release-images.sh"
REG=registry.example/cozy

D_API=sha256:1111111111111111111111111111111111111111111111111111111111111111
D_FOO=sha256:2222222222222222222222222222222222222222222222222222222222222222
D_OTHER=sha256:3333333333333333333333333333333333333333333333333333333333333333
D_PKGS=sha256:4444444444444444444444444444444444444444444444444444444444444444

# A tree pinning two first-party images, one of them in two storage shapes, a
# third-party image and the packages artifact, and the cosign stub. `verify`
# succeeds for a ref the stub has signed or that PRESIGNED names;
# COSIGN_VERIFY_EXIT overrides every verify.
make_fixture() {
    work="$(mktemp -d)"
    mkdir -p "$work/tree/system/api" "$work/tree/apps/kube/images" "$work/tree/core/installer" "$work/stub"
    cat >"$work/tree/system/api/values.yaml" <<EOF
image: $REG/api:v1.2.3-rc.1@$D_API
sidecar:
  registry: docker.io
  repository: other/thing
  tag: 1.0@$D_OTHER
EOF
    printf '%s/foo:v1.2.3-rc.1@%s\n' "$REG" "$D_FOO" >"$work/tree/apps/kube/images/foo.tag"
    printf 'args:\n  - --migrate-image=%s/api:v1.2.3-rc.1@%s\n' "$REG" "$D_API" >"$work/tree/apps/kube/values.yaml"
    cat >"$work/tree/core/installer/values.yaml" <<EOF
cozystack:
  platformSourceUrl: 'oci://$REG/cozystack-packages'
  platformSourceRef: 'digest=$D_PKGS'
EOF

    cat >"$work/stub/cosign" <<'STUB'
#!/bin/sh
cmd=$1
shift
case $cmd in
    sign)
        echo "$*" >> "$STUB_LOG/cosign-sign"
        for arg in "$@"; do
            case $arg in *@sha256:*) echo "$arg" >> "$STUB_LOG/signed" ;; esac
        done
        ;;
    verify)
        printf '%s\n' "$@" > "$STUB_LOG/cosign-verify-last"
        [ -z "${COSIGN_VERIFY_EXIT:-}" ] || exit "$COSIGN_VERIFY_EXIT"
        ref=
        for arg in "$@"; do
            case $arg in *@sha256:*) ref=$arg ;; esac
        done
        if [ -f "$STUB_LOG/signed" ] && grep -qxF "$ref" "$STUB_LOG/signed"; then exit 0; fi
        case " ${PRESIGNED:-} " in *" $ref "*) exit 0 ;; esac
        echo "Error: no matching signatures" >&2
        exit 1
        ;;
    *) exit 2 ;;
esac
STUB
    chmod +x "$work/stub/cosign"
    export PATH="$work/stub:$PATH" STUB_LOG="$work"
}

# The status is returned explicitly: cozytest.sh appends `return 0` before a
# closing brace at column zero, which would turn a failed run into a success.
sign() {
    "$SCRIPT" "$@" "$work/tree" "$REG" >"$work/out" 2>"$work/err"
    return $?
}

cleanup() {
    cd /
    rm -rf "$work"
}

@test "signs every unsigned first-party ref by digest in one call, skipping third-party refs and the packages artifact" {
    make_fixture
    sign

    [ "$(grep -c . "$work/cosign-sign")" -eq 1 ]
    [ "$(sort "$work/signed")" = "$(printf '%s/api@%s\n%s/foo@%s\n' "$REG" "$D_API" "$REG" "$D_FOO")" ]
    if grep -q 'other/thing' "$work/cosign-sign"; then echo "FAIL: a third-party ref was signed" >&2; exit 1; fi
    if grep -q 'cozystack-packages' "$work/cosign-sign"; then echo "FAIL: the packages artifact was signed" >&2; exit 1; fi
    if grep -q ':v1.2.3-rc.1@' "$work/cosign-sign"; then echo "FAIL: a ref was signed with its tag" >&2; exit 1; fi
    grep -qx 'sign-images: 2 signed, 0 already signed' "$work/out"

    cleanup
}

@test "does not sign a ref that already verifies" {
    make_fixture
    export PRESIGNED="$REG/api@$D_API"
    sign

    [ "$(cat "$work/signed")" = "$REG/foo@$D_FOO" ]
    grep -qx 'sign-images: 1 signed, 1 already signed' "$work/out"

    cleanup
}

@test "verifies each signature against the release workflows, with the identity hack/sign-release-assets.sh verifies" {
    make_fixture
    sign

    grep -qxF https://token.actions.githubusercontent.com "$work/cosign-verify-last"
    regexp="$(grep -A1 -xF -- --certificate-identity-regexp "$work/cosign-verify-last" | tail -n1)"
    printf '%s\n' https://github.com/cozystack/cozystack/.github/workflows/tags.yaml@refs/tags/v1.2.3-rc.1 | grep -qE "$regexp"
    for identity in \
        https://github.com/fork/cozystack/.github/workflows/tags.yaml@refs/tags/v1.2.3 \
        https://github.com/cozystack/cozystack/.github/workflows/tags.yaml@refs/heads/main \
        https://github.com/cozystack/cozystack/.github/workflows/nightly.yaml@refs/heads/main; do
        if printf '%s\n' "$identity" | grep -qE "$regexp"; then
            echo "expected $regexp to reject $identity" >&2
            exit 1
        fi
    done
    assets_regexp="$(sed -n "s/^IDENTITY_REGEXP='\(.*\)'\$/\1/p" "$REPO_ROOT/hack/sign-release-assets.sh")"
    [ -n "$assets_regexp" ]
    [ "$regexp" = "$assets_regexp" ]

    cleanup
}

@test "docs/release.md tells users to verify the identity the script verifies" {
    regexp="$(sed -n "s/^IDENTITY_REGEXP='\(.*\)'\$/\1/p" "$SCRIPT")"
    [ -n "$regexp" ]
    grep -qF -- "--certificate-identity-regexp '$regexp'" "$REPO_ROOT/docs/release.md"
}

# Runs the fenced block of "How to verify a release" in docs/release.md against
# a release of two images, with a cosign stub that reads stdin, as a client
# may, and rejects the image REJECT names.
verify_recipe() {
    rwork="$(mktemp -d)"
    mkdir -p "$rwork/stub" "$rwork/rel" "$rwork/sbom"
    awk '/^### How to verify a release/ { s = 1 } s && /^```sh$/ { b = 1; next } b && /^```$/ { exit } b' \
        "$REPO_ROOT/docs/release.md" >"$rwork/recipe.sh"
    grep -q 'cosign verify "$image"' "$rwork/recipe.sh"
    cat >"$rwork/stub/cosign" <<'STUB'
#!/bin/sh
cmd=$1
shift
case $cmd in
    verify-blob) exit 0 ;;
    verify)
        cat >/dev/null
        echo "$1" >> "$RWORK/verified"
        [ "$1" != "${REJECT:-}" ] || exit 1
        ;;
    *) exit 2 ;;
esac
STUB
    chmod +x "$rwork/stub/cosign"
    cd "$rwork/rel"
    printf '%s/api@%s\n%s/foo@%s\n' "$REG" "$D_API" "$REG" "$D_FOO" >cozystack-images.txt
    echo '{}' >"$rwork/sbom/api.linux-amd64.spdx.json"
    tar -C "$rwork/sbom" -czf cozystack-images-sbom.tar.gz .
    echo bundle >cozystack-checksums.txt.sigstore.json
    sha256sum cozystack-images.txt cozystack-images-sbom.tar.gz >cozystack-checksums.txt
    PATH="$rwork/stub:$PATH" RWORK="$rwork" bash "$rwork/recipe.sh" </dev/null >"$rwork/out" 2>"$rwork/err"
    return $?
}

@test "the verify recipe in docs/release.md checks every image and fails on any one that does not verify" {
    verify_recipe
    [ "$(cat "$rwork/verified")" = "$(cat "$rwork/rel/cozystack-images.txt")" ]
    [ -f "$rwork/rel/image-sboms/api.linux-amd64.spdx.json" ]
    rm -rf "$rwork"

    export REJECT="$REG/api@$D_API"
    if verify_recipe; then
        echo "expected the recipe to fail on an image that does not verify" >&2
        exit 1
    fi
    grep -qx "not verified: $REJECT" "$rwork/err"
    if [ -e "$rwork/rel/image-sboms" ]; then
        echo "FAIL: the SBOMs were unpacked after an image failed" >&2
        exit 1
    fi
    cd /
    rm -rf "$rwork"
}

@test "fails when a signature does not verify after signing" {
    make_fixture
    export COSIGN_VERIFY_EXIT=1
    if sign; then
        echo "expected a failed verification to fail the script" >&2
        exit 1
    fi
    grep -q 'does not verify' "$work/err"
    cleanup
}

@test "--verify-only signs nothing and fails on an unsigned ref" {
    make_fixture
    export PRESIGNED="$REG/api@$D_API"
    if sign --verify-only; then
        echo "expected an unsigned ref to fail --verify-only" >&2
        exit 1
    fi
    if [ -e "$work/cosign-sign" ]; then
        echo "expected no signature to be made" >&2
        exit 1
    fi
    grep -q "::error::$REG/foo@$D_FOO" "$work/err"
    if grep -q "::error::$REG/api@" "$work/err"; then echo "FAIL: a verified ref was reported" >&2; exit 1; fi

    export PRESIGNED="$REG/api@$D_API $REG/foo@$D_FOO"
    sign --verify-only
    grep -qx 'sign-images: 0 signed, 2 already signed' "$work/out"

    cleanup
}

@test "--list writes every first-party ref once, by digest, sorted" {
    make_fixture
    sign --list "$work/images.txt"

    [ "$(cat "$work/images.txt")" = "$(printf '%s/api@%s\n%s/foo@%s' "$REG" "$D_API" "$REG" "$D_FOO")" ]

    cleanup
}

@test "refuses a tree that pins no first-party image" {
    make_fixture
    rm -r "$work/tree/system" "$work/tree/apps"
    if sign; then
        echo "expected an empty tree to be refused" >&2
        exit 1
    fi
    cleanup
}

@test "prepare-release signs the images and collects their SBOMs after cosign is set up and before the assets are uploaded" {
    wf="$REPO_ROOT/.github/workflows/tags.yaml"
    steps=$(yq -r '.jobs.prepare-release.steps[].name' "$wf")
    pos() { echo "$steps" | grep -nxF "$1" | cut -d: -f1; }
    cosign=$(pos 'Set up cosign')
    sign=$(pos 'Sign the release images and collect their SBOMs')
    upload=$(pos 'Upload assets')
    [ -n "$cosign" ] && [ -n "$sign" ] && [ -n "$upload" ]
    [ "$cosign" -lt "$sign" ] && [ "$sign" -lt "$upload" ]
    sel='.jobs.prepare-release.steps[] | select(.name == "Sign the release images and collect their SBOMs")'
    run_=$(yq -r "$sel | .run" "$wf")
    echo "$run_" | grep -qxE '[[:space:]]*hack/sign-release-images.sh --list _out/assets/cozystack-images.txt packages "\$REGISTRY"'
    echo "$run_" | grep -qxE '[[:space:]]*hack/collect-image-sboms.sh packages "\$REGISTRY" _out/assets/cozystack-images-sbom.tar.gz'
    [ "$(yq -r "$sel | .env.REGISTRY" "$wf")" = ghcr.io/cozystack/cozystack ]
    [ "$(yq -r '.jobs.prepare-release.permissions."id-token"' "$wf")" = write ]
}

@test "promote verifies the rc image signatures before it stages the stable release" {
    wf="$REPO_ROOT/.github/workflows/promote-rc.yaml"
    steps=$(yq -r '.jobs.promote.steps[].name' "$wf")
    pos() { echo "$steps" | grep -nxF "$1" | cut -d: -f1; }
    cosign=$(pos 'Set up cosign')
    verify=$(pos 'Verify the rc image signatures')
    upload=$(pos 'Create draft stable release and upload assets')
    [ -n "$cosign" ] && [ -n "$verify" ] && [ -n "$upload" ]
    [ "$cosign" -lt "$verify" ] && [ "$verify" -lt "$upload" ]
    run_=$(yq -r '.jobs.promote.steps[] | select(.name == "Verify the rc image signatures") | .run' "$wf")
    echo "$run_" | grep -qxE '[[:space:]]*hack/sign-release-images.sh --verify-only packages "\$REGISTRY"'
    # An rc cut before the script existed has no signatures to check and is
    # promoted as it always was.
    echo "$run_" | grep -q 'if \[ -f hack/sign-release-images.sh \]'
}
