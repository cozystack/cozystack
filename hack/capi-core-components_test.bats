#!/usr/bin/env bats
# capi-providers-core ships the Cluster API core manifests twice: as
# files/core-components.yaml, which a reviewer reads, and as files/components.gz,
# which the chart mounts into the cluster. Both are edited by hand; this package
# has no `make components`. An edit to the YAML alone reviews cleanly and changes
# nothing in a cluster, which is how the startupProbe from #2946 stayed out of
# the shipped manager. This holds the two files to the same bytes.
#
# The comparison is on the decompressed bytes, so the gzip header plays no part
# in it. `gzip -n` is still the flag to regenerate with: without it the header
# carries a timestamp and every regeneration rewrites the committed blob even
# when the input has not changed.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
FILES="$REPO_ROOT/packages/system/capi-providers-core/files"

@test "core provider components.gz matches core-components.yaml" {
  [ -f "$FILES/core-components.yaml" ] || { echo "missing core-components.yaml" >&2; return 1; }
  [ -f "$FILES/components.gz" ]        || { echo "missing components.gz" >&2; return 1; }

  local work; work="$(mktemp -d)"
  gzip -dc "$FILES/components.gz" > "$work/decompressed.yaml"
  if ! cmp -s "$work/decompressed.yaml" "$FILES/core-components.yaml"; then
    echo "components.gz does not match core-components.yaml" >&2
    echo "the chart mounts the blob, so the cluster would run what the YAML does not say" >&2
    echo "regenerate: gzip -9 -n -c files/core-components.yaml > files/components.gz" >&2
    diff "$work/decompressed.yaml" "$FILES/core-components.yaml" | head -20 >&2 || true
    rm -rf "$work"
    return 1
  fi
  rm -rf "$work"
}
