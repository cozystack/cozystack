#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for hack/collect-image-sboms.sh.
#
# docker is a stub on PATH that answers `buildx imagetools inspect --raw` and
# `--format '{{ json .SBOM }}'` from a file per digest, the way the real tool
# answers from the registry, so the tests hold what the script owns: which
# images it reads, one file per platform under a fixed name, an archive whose
# order, times and owners do not depend on the machine, and a failure naming
# any image that lacks an SBOM for a platform its index serves.
#
# Expected failures are written as `if CMD; then ... exit 1; fi`, never as a
# negated command: see hack/bats-no-negated-assert.bats.
# -----------------------------------------------------------------------------

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
SCRIPT="$REPO_ROOT/hack/collect-image-sboms.sh"
REG=registry.example/cozy

D_API=sha256:1111111111111111111111111111111111111111111111111111111111111111
D_FOO=sha256:2222222222222222222222222222222222222222222222222222222222222222
D_OTHER=sha256:3333333333333333333333333333333333333333333333333333333333333333
D_PKGS=sha256:4444444444444444444444444444444444444444444444444444444444444444

# A tree pinning a stitched two-platform image, a single-platform image, a
# third-party image and the packages artifact, and the docker stub. api is an
# index of two platforms and their attestations, and its SBOM is keyed by
# platform, as `imagetools inspect` prints it for such an index; foo is an index
# of one platform and its attestation, and answers with the document itself.
# Anything else is a plain manifest whose SBOM is null.
make_fixture() {
    work="$(mktemp -d)"
    mkdir -p "$work/tree/system/api" "$work/tree/apps/kube/images" "$work/tree/core/installer" "$work/stub" "$work/sbom" "$work/raw"
    cat >"$work/tree/system/api/values.yaml" <<EOF
image: $REG/api:v1.2.3-rc.1@$D_API
sidecar:
  registry: docker.io
  repository: other/thing
  tag: 1.0@$D_OTHER
EOF
    printf '%s/foo:v1.2.3-rc.1@%s\n' "$REG" "$D_FOO" >"$work/tree/apps/kube/images/foo.tag"
    cat >"$work/tree/core/installer/values.yaml" <<EOF
cozystack:
  platformSourceUrl: 'oci://$REG/cozystack-packages'
  platformSourceRef: 'digest=$D_PKGS'
EOF
    echo '{"linux/amd64":{"SPDX":{"spdxVersion":"SPDX-2.3","name":"api-amd64"}},"linux/arm64":{"SPDX":{"spdxVersion":"SPDX-2.3","name":"api-arm64"}}}' >"$work/sbom/${D_API#sha256:}"
    echo '{"SPDX":{"spdxVersion":"SPDX-2.3","name":"foo"}}' >"$work/sbom/${D_FOO#sha256:}"
    att='"annotations":{"vnd.docker.reference.type":"attestation-manifest"},"platform":{"os":"unknown","architecture":"unknown"}'
    # arm64 first, so the documents are written out of name order and only
    # --sort=name puts them back in it, whatever order the filesystem lists.
    echo "{\"manifests\":[{\"platform\":{\"os\":\"linux\",\"architecture\":\"arm64\"}},{\"platform\":{\"os\":\"linux\",\"architecture\":\"amd64\"}},{$att},{$att}]}" >"$work/raw/${D_API#sha256:}"
    echo "{\"manifests\":[{\"platform\":{\"os\":\"linux\",\"architecture\":\"amd64\"}},{$att}]}" >"$work/raw/${D_FOO#sha256:}"

    cat >"$work/stub/docker" <<'STUB'
#!/bin/sh
[ "$1 $2 $3" = "buildx imagetools inspect" ] || exit 2
if [ "$5" = --raw ]; then
    f="$STUB_LOG/raw/${4##*@sha256:}"
    if [ -f "$f" ]; then cat "$f"; else echo '{"mediaType":"application/vnd.oci.image.manifest.v1+json"}'; fi
    exit 0
fi
[ "$5" = --format ] && [ "$6" = '{{ json .SBOM }}' ] || exit 2
echo "$4" >> "$STUB_LOG/inspected"
f="$STUB_LOG/sbom/${4##*@sha256:}"
if [ -f "$f" ]; then cat "$f"; else echo null; fi
STUB
    chmod +x "$work/stub/docker"
    export PATH="$work/stub:$PATH" STUB_LOG="$work"
}

# The status is returned explicitly: cozytest.sh appends `return 0` before a
# closing brace at column zero, which would turn a failed run into a success.
collect() {
    "$SCRIPT" "$work/tree" "$REG" "$work/images-sbom.tar.gz" >"$work/out" 2>"$work/err"
    return $?
}

cleanup() {
    cd /
    rm -rf "$work"
}

@test "writes one SPDX document per platform of every first-party image, and the same bytes on a re-run" {
    make_fixture
    collect

    [ "$(sort "$work/inspected")" = "$(printf '%s/api@%s\n%s/foo@%s' "$REG" "$D_API" "$REG" "$D_FOO")" ]
    # Unsorted here, so the archive has to hold its members in name order.
    members="$(tar -tzf "$work/images-sbom.tar.gz" | grep -v '/$' | sed 's|^\./||')"
    [ "$members" = "$(printf 'api.linux-amd64.spdx.json\napi.linux-arm64.spdx.json\nfoo.spdx.json')" ]
    # Owner and time come from the flags, not from the user and clock of the
    # run, which a re-run on the same machine cannot tell apart.
    headers="$(TZ=UTC tar --numeric-owner -tvzf "$work/images-sbom.tar.gz")"
    [ -n "$headers" ]
    if echo "$headers" | grep -v ' 0/0 .* 1970-01-01 00:00 '; then
        echo "FAIL: a member is not owned by 0/0 at the epoch" >&2
        exit 1
    fi
    mkdir "$work/x"
    tar -xzf "$work/images-sbom.tar.gz" -C "$work/x"
    [ "$(jq -r .name "$work/x/api.linux-arm64.spdx.json")" = api-arm64 ]
    [ "$(jq -r .name "$work/x/foo.spdx.json")" = foo ]
    grep -qx "image-sboms: 3 documents from 2 images in $work/images-sbom.tar.gz" "$work/out"

    first="$(sha256sum <"$work/images-sbom.tar.gz")"
    sleep 1
    collect
    [ "$(sha256sum <"$work/images-sbom.tar.gz")" = "$first" ]

    cleanup
}

@test "fails naming every image that carries no SBOM attestation, and writes no archive" {
    make_fixture
    rm "$work/sbom/${D_FOO#sha256:}"
    if collect; then
        echo "expected an image without an SBOM to fail the run" >&2
        exit 1
    fi
    grep -q "::error::$REG/foo@$D_FOO" "$work/err"
    if grep -q "::error::$REG/api@" "$work/err"; then echo "FAIL: an image with an SBOM was reported" >&2; exit 1; fi
    if [ -e "$work/images-sbom.tar.gz" ]; then
        echo "expected no archive to be written" >&2
        exit 1
    fi
    cleanup
}

@test "refuses a tree that pins no first-party image" {
    make_fixture
    rm -r "$work/tree/system" "$work/tree/apps"
    if collect; then
        echo "expected an empty tree to be refused" >&2
        exit 1
    fi
    cleanup
}

@test "fails on an index whose SBOM covers only some of its platforms" {
    make_fixture
    echo '{"linux/amd64":{"SPDX":{"spdxVersion":"SPDX-2.3","name":"api-amd64"}}}' >"$work/sbom/${D_API#sha256:}"
    if collect; then
        echo "expected an index without an arm64 SBOM to fail the run" >&2
        exit 1
    fi
    grep -q "::error::$REG/api@$D_API carries no SBOM attestation for linux/arm64;" "$work/err"
    if [ -e "$work/images-sbom.tar.gz" ]; then
        echo "expected no archive to be written" >&2
        exit 1
    fi
    cleanup
}

@test "fails on a platform whose SBOM is null" {
    make_fixture
    echo '{"linux/amd64":{"SPDX":{"spdxVersion":"SPDX-2.3","name":"api-amd64"}},"linux/arm64":{"SPDX":null}}' >"$work/sbom/${D_API#sha256:}"
    if collect; then
        echo "expected a null SBOM to fail the run" >&2
        exit 1
    fi
    grep -q "::error::$REG/api@$D_API carries no SBOM attestation for linux/arm64;" "$work/err"
    cleanup
}
