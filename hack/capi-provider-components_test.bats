#!/usr/bin/env bats
# The vendored CAPI provider packages ship the provider manifests twice: as the
# readable YAML a reviewer reads, and as the gzip blob the chart actually mounts
# into the cluster. `make components` writes both from one download, so they
# agree at the moment of vendoring and nothing keeps them in step afterwards.
#
# An edit to the YAML alone is silent in every way that matters. The diff looks
# right, review passes on it, and the cluster runs whatever the blob held —
# which is the version nobody read. This asserts the two are the same bytes.
#
# gzip -n is what makes the comparison possible at all: without it the header
# carries a timestamp, every regeneration differs for identical input, and the
# only stable thing to compare is the decompressed content.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"

check_pair() {
  local pkg="$1" yaml="$2"
  local dir="$REPO_ROOT/packages/system/$pkg/files"

  [ -f "$dir/$yaml" ]      || { echo "missing $pkg/files/$yaml" >&2; return 1; }
  [ -f "$dir/components.gz" ] || { echo "missing $pkg/files/components.gz" >&2; return 1; }

  local work; work="$(mktemp -d)"
  gzip -dc "$dir/components.gz" > "$work/decompressed.yaml"

  if ! cmp -s "$work/decompressed.yaml" "$dir/$yaml"; then
    echo "$pkg: components.gz does not match $yaml" >&2
    echo "the chart mounts the blob, so the cluster would run what the YAML does not say" >&2
    echo "run: make -C packages/system/$pkg components" >&2
    head -40 "$work/decompressed.yaml" > "$work/a"
    head -40 "$dir/$yaml" > "$work/b"
    diff "$work/a" "$work/b" | head -20 >&2 || true
    rm -rf "$work"
    return 1
  fi
  rm -rf "$work"
}

@test "capmox components.gz matches the vendored infrastructure-components.yaml" {
  check_pair capi-providers-infraprovider-proxmox infrastructure-components.yaml
}

@test "in-cluster IPAM components.gz matches the vendored ipam-components.yaml" {
  check_pair capi-providers-ipam-in-cluster ipam-components.yaml
}
