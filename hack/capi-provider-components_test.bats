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
# The comparison is on the decompressed bytes, so the gzip header plays no part
# in it. `make components` still passes -n, for a different reason: without it
# the header carries a timestamp and every regeneration rewrites the committed
# blob even when the input has not changed.

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
    if grep -q '^components:' "$REPO_ROOT/packages/system/$pkg/Makefile" 2>/dev/null; then
      echo "run: make -C packages/system/$pkg components" >&2
    else
      echo "regenerate: gzip -9 -n -c files/$yaml > files/components.gz" >&2
    fi
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

# A version bump can land in git and never reach a cluster. capi-operator hashes
# the provider spec and the config Secret into applied-spec-hash, and the
# ConfigMap is in neither: bumping the Makefile and running `make components`
# while spec.version and the ConfigMap name stay behind produces a release that
# installs the old provider and reports the old version, with no error anywhere.
# The three have to be one value, so this asserts they are.
check_version() {
  local pkg="$1" makefile_var="$2"
  local dir="$REPO_ROOT/packages/system/$pkg"

  local version
  version="$(sed -n "s/^${makefile_var}=//p" "$dir/Makefile")"
  [ -n "$version" ] || { echo "$pkg: no $makefile_var in Makefile" >&2; return 1; }

  local cm_name provider_version
  cm_name="$(sed -n 's/^  name: //p' "$dir/templates/configmaps.yaml")"
  provider_version="$(sed -n 's/^  version: //p' "$dir/templates/providers.yaml")"

  if [ "$cm_name" != "$provider_version" ]; then
    echo "$pkg: ConfigMap name '$cm_name' and spec.version '$provider_version' differ" >&2
    echo "fetchConfig would find no ConfigMap for the version the operator installs" >&2
    return 1
  fi

  case "$cm_name" in
    "$version"-*) ;;
    *)
      echo "$pkg: Makefile has $makefile_var=$version, the chart installs '$cm_name'" >&2
      echo "the vendored files were regenerated without moving the version the operator reads" >&2
      return 1
      ;;
  esac
}

@test "capmox ships the version its Makefile vendored" {
  check_version capi-providers-infraprovider-proxmox CAPMOX_VERSION
}

@test "in-cluster IPAM ships the version its Makefile vendored" {
  check_version capi-providers-ipam-in-cluster CAIP_VERSION
}

# The Makefile pins the SHA-256 of both release assets and `make components`
# checks its downloads against them. The pin is only as good as the files it
# describes, so the vendored copies are checked against it here as well: a
# vendored file edited by hand, or a sum bumped without the file, both show up
# without anyone running the refresh target.
check_sums() {
  local pkg="$1" makefile_var="$2" yaml="$3"
  local dir="$REPO_ROOT/packages/system/$pkg"

  local sum_components sum_metadata
  sum_components="$(sed -n "s/^${makefile_var}_SHA256_COMPONENTS=//p" "$dir/Makefile")"
  sum_metadata="$(sed -n "s/^${makefile_var}_SHA256_METADATA=//p" "$dir/Makefile")"
  [ -n "$sum_components" ] && [ -n "$sum_metadata" ] \
    || { echo "$pkg: Makefile pins no ${makefile_var}_SHA256_* sums" >&2; return 1; }

  if ! printf '%s  %s\n%s  %s\n' "$sum_components" "$dir/files/$yaml" "$sum_metadata" "$dir/files/metadata.yaml" \
      | sha256sum -c - >/dev/null 2>&1; then
    echo "$pkg: a vendored file does not match the sum its Makefile pins" >&2
    echo "either the file was edited without a refresh, or the sum moved without the file" >&2
    return 1
  fi
}

@test "capmox vendored files match the sums its Makefile pins" {
  check_sums capi-providers-infraprovider-proxmox CAPMOX infrastructure-components.yaml
}

@test "in-cluster IPAM vendored files match the sums its Makefile pins" {
  check_sums capi-providers-ipam-in-cluster CAIP ipam-components.yaml
}
