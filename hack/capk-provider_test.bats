#!/usr/bin/env bats
# EXIT-TRAP DEBT: 3 -- see hack/bats-no-exit-trap.bats.
# None of the three is debt: each trap sits inside an explicit subshell and
# removes that test's own temp dir, so it does not displace the handler the
# bats binary installs and a failure still prints its `not ok`. The count is
# declared because the ratchet is exact in both directions: it fails if a
# test-level trap is added here, and it fails if one of these is removed.

_capk_fixture() {
  mkdir -p "$1/packages/system" "$1/hack" "$1/bin" "$1/oci"
  cp -R packages/system/capi-providers-infraprovider "$1/packages/system/"
  cp hack/common-envs.mk hack/package.mk "$1/hack/"
  cat > "$1/bin/docker" <<'EOF'
#!/bin/sh
set -eu
printf '%s\n' "$@" > "$CAPK_BUILD_ARGS"
[ "${CAPK_BUILD_FAIL:-0}" = 0 ] || exit 77
while [ "$#" -gt 0 ]; do
  if [ "$1" = --metadata-file ]; then
    shift
    printf '{"containerimage.digest":"%s"}\n' "${CAPK_TEST_DIGEST:-sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa}" > "$1"
    exit 0
  fi
  shift
done
exit 2
EOF
  chmod +x "$1/bin/docker"
}

@test "CAPK compressed provider ships the migration create permission" {
  pkg=packages/system/capi-providers-infraprovider
  gzip -dc "$pkg/files/components.gz" | cmp - "$pkg/files/infrastructure-components.yaml"
  rules=$(gzip -dc "$pkg/files/components.gz" | yq -o=json -I=0 'select(.kind == "ClusterRole" and .metadata.name == "capk-manager-role") | .rules')
  printf '%s\n' "$rules" | jq -e '[.[] | select(.apiGroups == ["kubevirt.io"] and .resources == ["virtualmachineinstancemigrations"])] == [{"apiGroups":["kubevirt.io"],"resources":["virtualmachineinstancemigrations"],"verbs":["create"]}]'
}

@test "CAPK rendered provider reads the digest pin outside the gzip" {
  (
    work=$(mktemp -d)
    trap 'rm -rf "$work"' EXIT
    pkg=packages/system/capi-providers-infraprovider
    helm template capk "$pkg" --namespace cozy-cluster-api > "$work/rendered.yaml"
    image=$(yq -r 'select(.kind == "InfrastructureProvider") | .spec.deployment.containers[] | select(.name == "manager") | .imageUrl' "$work/rendered.yaml")
    [ "$image" = "$(cat "$pkg/images/cluster-api-provider-kubevirt.tag")" ]
    [ "$(yq -r 'select(.kind == "InfrastructureProvider") | .spec.version' "$work/rendered.yaml")" = v0.1.10-infraprovider ]
    yq -r 'select(.kind == "ConfigMap") | .binaryData.components' "$work/rendered.yaml" \
      | python3 -c 'import base64,gzip,sys; sys.stdout.buffer.write(gzip.decompress(base64.b64decode(sys.stdin.read())))' \
      | cmp - "$pkg/files/infrastructure-components.yaml"
  )
}

@test "CAPK OCI build stamps a discoverable ref without rewriting components" {
  (
    work=$(mktemp -d)
    trap 'rm -rf "$work"' EXIT
    _capk_fixture "$work"
    pkg="$work/packages/system/capi-providers-infraprovider"
    PATH="$work/bin:$PATH" CAPK_BUILD_ARGS="$work/args" \
      make -C "$pkg" image COZYSTACK_VERSION=0 REGISTRY=registry.example.test/cozy \
      IMAGE_TAG=pr-test OCI_EXPORT_DIR="$work/oci" PLATFORM=linux/amd64
    ref=registry.example.test/cozy/cluster-api-provider-kubevirt:pr-test@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    [ "$(cat "$pkg/images/cluster-api-provider-kubevirt.tag")" = "$ref" ]
    grep -Fx -- --push=0 "$work/args"
    grep -Fx -- --load=0 "$work/args"
    grep -Fx -- "type=oci,dest=$work/oci/cluster-api-provider-kubevirt.oci.tar" "$work/args"
    cmp "$pkg/files/components.gz" packages/system/capi-providers-infraprovider/files/components.gz
    . hack/lib/image-refs.sh
    collect_image_refs "$work/packages" | grep -Fx "$ref"
  )
}

@test "CAPK failed builds and missing digests cannot replace the prior pin" {
  (
    work=$(mktemp -d)
    trap 'rm -rf "$work"' EXIT
    _capk_fixture "$work"
    pkg="$work/packages/system/capi-providers-infraprovider"
    pin=$(cat "$pkg/images/cluster-api-provider-kubevirt.tag")
    for failure in build digest; do
      fail=0
      digest=invalid
      [ "$failure" != build ] || fail=1
      rc=0
      PATH="$work/bin:$PATH" CAPK_BUILD_ARGS="$work/args" CAPK_BUILD_FAIL="$fail" CAPK_TEST_DIGEST="$digest" \
        make -C "$pkg" image COZYSTACK_VERSION=0 REGISTRY=registry.example.test/cozy \
        IMAGE_TAG=pr-test OCI_EXPORT_DIR="$work/oci" > "$work/build.log" 2>&1 || rc=$?
      [ "$rc" -ne 0 ]
      [ "$(cat "$pkg/images/cluster-api-provider-kubevirt.tag")" = "$pin" ]
    done
  )
}
