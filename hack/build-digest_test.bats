#!/usr/bin/env bats
# A build that exported no image, or a metadata file that lost its digest,
# must stop the image target before it writes a reference anything ships.

load test_helper

@test "the digest buildx recorded is printed when it is a sha256 digest" {
  tmp=$(mktemp -d)
  digest="sha256:$(printf 'a%.0s' $(seq 64))"
  printf '{"containerimage.digest": "%s"}\n' "$digest" > "$tmp/metadata.json"
  [ "$(hack/build-digest.sh "$tmp/metadata.json")" = "$digest" ]
  rm -rf "$tmp"
}

@test "a missing or malformed digest fails with the metadata file named" {
  tmp=$(mktemp -d)
  for metadata in '{}' '{"containerimage.digest": null}' '{"containerimage.digest": ""}' \
    '{"containerimage.digest": "sha256:abc"}' "{\"containerimage.digest\": \"sha512:$(printf 'a%.0s' $(seq 64))\"}" \
    "{\"containerimage.digest\": \"sha256:$(printf 'a%.0s' $(seq 64))\\nsha256:$(printf 'b%.0s' $(seq 64))\"}"; do
    printf '%s\n' "$metadata" > "$tmp/metadata.json"
    rc=0; output=$(hack/build-digest.sh "$tmp/metadata.json" 2>&1) || rc=$?
    [ "$rc" -ne 0 ] || { echo "accepted $metadata" >&2; false; }
    printf '%s\n' "$output" | grep -qF "$tmp/metadata.json"
  done
  rc=0; hack/build-digest.sh "$tmp/absent.json" > /dev/null 2>&1 || rc=$?
  [ "$rc" -ne 0 ]
  rm -rf "$tmp"
}
