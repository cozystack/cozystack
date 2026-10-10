#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Smoke checks for images whose runtime depends on tools from the base image:
# a base digest or tag bump can drop a package or change its major version with
# nothing failing at build time.
#
# IMAGE_SMOKE_REFS_FILE names a file with one image reference per line, the
# images built by the current CI job. A test whose image is not listed passes
# without running, so the unit lane, which builds nothing, runs no containers.
#
# Run with: IMAGE_SMOKE_REFS_FILE=<file> hack/cozytest.sh hack/image-smoke.bats
# -----------------------------------------------------------------------------

load test_helper

# Prints the first listed reference whose repository is named $1.
smoke_ref() {
  [ -n "${IMAGE_SMOKE_REFS_FILE:-}" ] || return 0
  grep -E "/$1([:@]|\$)" "$IMAGE_SMOKE_REFS_FILE" | head -n 1
}

smoke_run() {
  docker run --rm --network none --entrypoint sh "$1" -c "$2"
}

# readelf and go come from the golang builder the CSI image already pins, since
# the checked images carry neither. It is pulled through mirror.gcr.io, as the
# image builds pull docker.io: anonymous Docker Hub pulls hit its rate limit.
READELF_IMAGE=$(sed -n 's|^ARG builder_image=docker.io/|mirror.gcr.io/|p' "$PWD/packages/apps/kubernetes/images/kubevirt-csi-driver/Dockerfile")

# Fails when any of the given files, copied out of image $1, requires an x86
# ISA level above x86-64-v2: a C object through its GNU property note, a Go
# binary through the GOAMD64 it was built with, which leaves no note. glibc
# refuses to load such an object on an older CPU and a Go binary dies at start,
# so the container is scheduled and every exec of it fails. v2 is the floor the
# AlmaLinux 9 userland already sets. A path prefixed with ? is skipped when the
# image does not have it.
isa_baseline() {
  isa_img=$1
  shift
  isa_dir=$(mktemp -d "${TMPDIR:-/tmp}/image-smoke-isa.XXXXXX")
  isa_cid=$(docker create "$isa_img" true) || { rm -rf "$isa_dir"; return 1; }
  isa_rc=0
  for isa_f in "$@"; do
    case "$isa_f" in
      \?*) docker cp -L "$isa_cid:${isa_f#?}" "$isa_dir/$(basename "$isa_f")" 2>/dev/null || true ;;
      *) docker cp -L "$isa_cid:$isa_f" "$isa_dir/$(basename "$isa_f")" || isa_rc=1 ;;
    esac
  done
  docker rm "$isa_cid" >/dev/null
  isa_out=
  if [ "$isa_rc" -eq 0 ]; then
    # A file that yields neither line was not judged at all, so it fails too.
    isa_out=$(docker run --rm --network none -v "$isa_dir:/x:ro" --entrypoint sh "$READELF_IMAGE" \
      -c 'command -v readelf >/dev/null && command -v go >/dev/null || exit 1
          for f in /x/*; do
            l=$(readelf -n "$f" | sed -n "s|.*x86 ISA needed: *|$f: |p"
                go version -m "$f" 2>/dev/null | sed -n "s|.*GOAMD64=|$f: GOAMD64=|p")
            [ -n "$l" ] || { echo "$f: no ISA level recorded"; exit 1; }
            echo "$l"
          done') || isa_rc=1
  fi
  rm -rf "$isa_dir"
  printf '%s\n' "$isa_out"
  [ "$isa_rc" -eq 0 ] || return 1
  if printf '%s\n' "$isa_out" | grep -E 'x86-64-v[34]|GOAMD64=v[34]'; then
    return 1
  fi
}

@test "kubevirt-csi-driver carries the filesystem and NFS tools" {
  ref=$(smoke_ref kubevirt-csi-driver)
  [ -n "$ref" ] || return 0
  smoke_run "$ref" 'mkfs.ext4 -V && mkfs.xfs -V && mount.nfs -V'
}

# The node service runs inside tenant VMs, whose vCPUs may not offer
# x86-64-v3.
@test "kubevirt-csi-driver runs its filesystem tools without x86-64-v3" {
  ref=$(smoke_ref kubevirt-csi-driver)
  [ -n "$ref" ] || return 0
  paths=$(smoke_run "$ref" 'for b in mkfs.ext4 mkfs.xfs fsck.ext4 blkid mount umount; do p=$(command -v "$b") || { echo "$b: not found" >&2; exit 1; }; readlink -f "$p"; done; readlink -f /usr/lib64/libc.so.6')
  [ "$(docker image inspect --format '{{.Architecture}}' "$ref")" = amd64 ] || return 0
  # shellcheck disable=SC2086
  isa_baseline "$ref" $paths
}

# The CSI sidecars run beside the driver in the same pods; they move with the
# kubevirt-csi-driver build, so they are checked in its job.
@test "the KubeVirt CSI sidecars run without x86-64-v3" {
  ref=$(smoke_ref kubevirt-csi-driver)
  [ -n "$ref" ] || return 0
  for sidecar in $(grep -ohE 'registry\.k8s\.io/sig-storage/[^"[:space:]]+' \
    "$PWD/packages/apps/kubernetes/templates/csi/deploy.yaml" \
    "$PWD/packages/system/kubevirt-csi-node/templates/deploy.yaml" | sort -u); do
    docker pull -q --platform linux/amd64 "$sidecar" >/dev/null
    entry=$(docker image inspect --format '{{index .Config.Entrypoint 0}}' "$sidecar")
    isa_baseline "$sidecar" "$entry" '?/lib/x86_64-linux-gnu/libc.so.6' '?/usr/lib64/libc.so.6'
  done
}

@test "linstor-csi carries the filesystem and NFS tools and an mtab link" {
  ref=$(smoke_ref linstor-csi)
  [ -n "$ref" ] || return 0
  smoke_run "$ref" 'mkfs.ext4 -V && mkfs.xfs -V && mount.nfs -V && test -L /etc/mtab'
}

@test "piraeus-server carries drbd, lvm, zfs, java and the linstor client" {
  ref=$(smoke_ref piraeus-server)
  [ -n "$ref" ] || return 0
  # zfs and zpool exit non-zero without the kernel module, so only presence is checked.
  smoke_run "$ref" 'drbdadm --version && command -v lvm && command -v zfs && command -v zpool && java -version && linstor --version'
}

@test "multus-cni starts and ships the CNI plugins" {
  ref=$(smoke_ref multus-cni)
  [ -n "$ref" ] || return 0
  smoke_run "$ref" '/usr/src/multus-cni/bin/multus-daemon --help >/dev/null 2>&1 && for p in bridge host-local macvlan portmap tuning; do test -x /cni-plugins/$p || exit 1; done'
}

@test "harbor nginx-photon runs nginx as uid 10000" {
  ref=$(smoke_ref nginx-photon)
  [ -n "$ref" ] || return 0
  smoke_run "$ref" 'nginx -v && test "$(id -u nginx)" = 10000'
}

@test "harbor trivy-adapter-photon carries rpm and trivy for scanner uid 10000" {
  ref=$(smoke_ref trivy-adapter-photon)
  [ -n "$ref" ] || return 0
  smoke_run "$ref" 'rpm --version && trivy --version && test "$(id -u scanner)" = 10000'
}

@test "harbor-core runs as harbor uid 10000" {
  ref=$(smoke_ref harbor-core)
  [ -n "$ref" ] || return 0
  smoke_run "$ref" 'test "$(id -u harbor)" = 10000 && test -x /harbor/harbor_core'
}

@test "redis-backup carries redis 8 or newer and curl" {
  ref=$(smoke_ref redis-backup)
  [ -n "$ref" ] || return 0
  smoke_run "$ref" 'major=$(redis-server --version | sed -n "s/^Redis server v=\([0-9]*\)\..*/\1/p") && test "${major:-0}" -ge 8 && curl --version'
}
