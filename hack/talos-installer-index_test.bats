#!/usr/bin/env bats
# packages/core/talos/hack/installer-index.sh ties the per-arch installer
# manifests skopeo left in an OCI layout into the index the talos image is
# pushed as. A wrong digest or size makes that push fail; a wrong or missing
# platform makes a node pull the other arch's installer.
#
# Written for POSIX sh: hack/cozytest.sh sources this file into /bin/sh.
#
# Requires: jq, sha256sum.

@test "installer-index.sh indexes each arch's manifest under its platform" {
  layout="$(mktemp -d)"
  mkdir -p "$layout/blobs/sha256"
  cat > "$layout/index.json" <<'EOF'
{"schemaVersion":2,"manifests":[
{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:aa","size":10,"annotations":{"org.opencontainers.image.ref.name":"amd64"}},
{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:bb","size":20,"annotations":{"org.opencontainers.image.ref.name":"arm64"}}]}
EOF
  packages/core/talos/hack/installer-index.sh "$layout" amd64 arm64

  desc=$(jq -c '.manifests[] | select(.annotations["org.opencontainers.image.ref.name"] == "index")' "$layout/index.json")
  digest=$(printf '%s' "$desc" | jq -r .digest)
  blob="$layout/blobs/sha256/${digest#sha256:}"
  [ "$(sha256sum "$blob" | cut -d' ' -f1)" = "${digest#sha256:}" ]
  [ "$(printf '%s' "$desc" | jq .size)" -eq "$(wc -c < "$blob" | tr -d ' ')" ]
  [ "$(jq -c '[.manifests[] | [.digest, .platform.os, .platform.architecture]]' "$blob")" = '[["sha256:aa","linux","amd64"],["sha256:bb","linux","arm64"]]' ]
  rm -rf "$layout"
}

@test "installer-index.sh refuses an arch the layout has no manifest for" {
  layout="$(mktemp -d)"
  mkdir -p "$layout/blobs/sha256"
  echo '{"schemaVersion":2,"manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:aa","size":10,"annotations":{"org.opencontainers.image.ref.name":"amd64"}}]}' > "$layout/index.json"
  if packages/core/talos/hack/installer-index.sh "$layout" amd64 arm64 2>/dev/null; then
    echo "FAIL: indexed an arch with no manifest"; rm -rf "$layout"; false
  fi
  rm -rf "$layout"
}
