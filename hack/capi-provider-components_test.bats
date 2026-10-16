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

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"

check_pair() {
  local pkg="$1" yaml="$2" blob="${3:-components.gz}"
  local dir="$REPO_ROOT/packages/system/$pkg/files"

  [ -f "$dir/$yaml" ]      || { echo "missing $pkg/files/$yaml" >&2; return 1; }
  [ -f "$dir/$blob" ] || { echo "missing $pkg/files/$blob" >&2; return 1; }

  local work; work="$(mktemp -d)"
  gzip -dc "$dir/$blob" > "$work/decompressed.yaml"

  if ! cmp -s "$work/decompressed.yaml" "$dir/$yaml"; then
    echo "$pkg: $blob does not match $yaml" >&2
    echo "the chart mounts the blob, so the cluster would run what the YAML does not say" >&2
    if [ "$blob" = components.gz ] && grep -q '^components:' "$REPO_ROOT/packages/system/$pkg/Makefile" 2>/dev/null; then
      echo "run: make -C packages/system/$pkg components" >&2
    else
      echo "regenerate: gzip -9 -n -c files/$yaml > files/$blob" >&2
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

# Packages that carry patches/ vendor a file that differs from the release
# asset, so its sum cannot be pinned directly. Reverting the patches recovers
# the asset, and that is checked against the pinned sum instead: a hand edit of
# the vendored file, a patch changed without a refresh, or a sum moved without
# the file all show up here.
check_patched_sums() {
  local pkg="$1" makefile_var="$2" yaml="$3" metadata="${4:-metadata.yaml}"
  local dir="$REPO_ROOT/packages/system/$pkg"

  local sum_components sum_metadata
  sum_components="$(sed -n "s/^${makefile_var}_SHA256_COMPONENTS=//p" "$dir/Makefile")"
  sum_metadata="$(sed -n "s/^${makefile_var}_SHA256_METADATA=//p" "$dir/Makefile")"
  [ -n "$sum_components" ] && [ -n "$sum_metadata" ] \
    || { echo "$pkg: Makefile pins no ${makefile_var}_SHA256_* sums" >&2; return 1; }

  local work; work="$(mktemp -d)"
  cp "$dir/files/$yaml" "$work/asset.yaml"
  local patches=() i
  for i in "$dir"/patches/*.diff; do
    [ -e "$i" ] && patches+=("$i")
  done
  for (( i = ${#patches[@]} - 1; i >= 0; i-- )); do
    patch -s -f -R "$work/asset.yaml" < "${patches[$i]}" \
      || { echo "$pkg: $(basename "${patches[$i]}") does not revert cleanly from files/$yaml" >&2; rm -rf "$work"; return 1; }
  done

  if ! printf '%s  %s\n%s  %s\n' "$sum_components" "$work/asset.yaml" "$sum_metadata" "$dir/files/$metadata" \
      | sha256sum -c - >/dev/null 2>&1; then
    echo "$pkg: files/$yaml without patches/, or files/$metadata, does not match the sum its Makefile pins" >&2
    rm -rf "$work"
    return 1
  fi
  rm -rf "$work"
}

@test "capmox vendored files match the sums its Makefile pins" {
  check_sums capi-providers-infraprovider-proxmox CAPMOX infrastructure-components.yaml
}

@test "in-cluster IPAM vendored files match the sums its Makefile pins" {
  check_sums capi-providers-ipam-in-cluster CAIP ipam-components.yaml
}

@test "CAPI core components.gz matches the vendored core-components.yaml" {
  check_pair capi-providers-core core-components.yaml
}

@test "CAPI core vendored files match the sums its Makefile pins" {
  check_patched_sums capi-providers-core CAPI core-components.yaml
}

@test "CAPI core ships the version its Makefile vendored" {
  check_version capi-providers-core CAPI_VERSION
}

# capi-providers-bootstrap ships two providers, kubeadm and Talos, each with its
# own ConfigMap and BootstrapProvider, so the single-provider check above does
# not fit it. This asserts that the ConfigMap and the provider object for one of
# them both carry <Makefile version>-<suffix>.
check_bootstrap_version() {
  local makefile_var="$1" suffix="$2"
  local dir="${3:-$REPO_ROOT/packages/system/capi-providers-bootstrap}"

  local version
  version="$(sed -n "s/^${makefile_var} *= *//p" "$dir/Makefile")"
  [ -n "$version" ] || { echo "capi-providers-bootstrap: no $makefile_var in Makefile" >&2; return 1; }

  local want="$version-$suffix"
  grep -qxF "  name: $want" "$dir/templates/configmaps.yaml" \
    || { echo "capi-providers-bootstrap: no ConfigMap named $want" >&2; return 1; }
  grep -qxF "  version: $want" "$dir/templates/providers.yaml" \
    || { echo "capi-providers-bootstrap: no BootstrapProvider at version $want" >&2; return 1; }
}

# A version carries dots, which a pattern match would take for any character.
@test "the bootstrap version check compares the version literally" {
  local fx="$BATS_TEST_TMPDIR/bootstrap"
  mkdir -p "$fx/templates"
  echo 'CAPI_VERSION=v1.13.7' > "$fx/Makefile"
  printf 'metadata:\n  name: v1x13x7-bootstrap\n' > "$fx/templates/configmaps.yaml"
  printf 'spec:\n  version: v1x13x7-bootstrap\n' > "$fx/templates/providers.yaml"
  run check_bootstrap_version CAPI_VERSION bootstrap "$fx"
  [ "$status" -ne 0 ]
}

@test "kubeadm bootstrap ships the version its Makefile vendored" {
  check_bootstrap_version CAPI_VERSION bootstrap
}

@test "Talos bootstrap ships the version its Makefile vendored" {
  check_bootstrap_version CABPT_UPSTREAM_VERSION bootstrap-talos
}

@test "Talos bootstrap components-talos.gz matches the vendored bootstrap-components-talos.yaml" {
  check_pair capi-providers-bootstrap bootstrap-components-talos.yaml components-talos.gz
}

@test "Talos bootstrap vendored files match the sums its Makefile pins" {
  check_patched_sums capi-providers-bootstrap CABPT bootstrap-components-talos.yaml metadata-talos.yaml
}

@test "kubeadm bootstrap components.gz matches the vendored bootstrap-components.yaml" {
  check_pair capi-providers-bootstrap bootstrap-components.yaml
}

@test "kubeadm bootstrap vendored files match the sums its Makefile pins" {
  check_sums capi-providers-bootstrap CAPI bootstrap-components.yaml
}

# The core and the kubeadm bootstrap provider are cut from one Cluster API
# release; running them at different versions is not a combination upstream
# tests.
@test "CAPI core and kubeadm bootstrap vendor the same Cluster API release" {
  local core boot
  core="$(sed -n 's/^CAPI_VERSION=//p' "$REPO_ROOT/packages/system/capi-providers-core/Makefile")"
  boot="$(sed -n 's/^CAPI_VERSION=//p' "$REPO_ROOT/packages/system/capi-providers-bootstrap/Makefile")"
  [ -n "$core" ] && [ "$core" = "$boot" ] \
    || { echo "core vendors '$core', kubeadm bootstrap vendors '$boot'" >&2; return 1; }
}
