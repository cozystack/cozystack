#!/usr/bin/env bats
# A pool on a Kairos class without its own image boots the disk named in
# images/kairos-worker-<minor>.tag for its Kubernetes minor, and the chart
# refuses to render when that file is missing. So every minor the chart offers
# needs one, and nothing else may carry a stale one. Next to each disk the OS
# image it is made from is published as kairos-worker-base-<minor>, for
# operators to build their own worker images FROM.

load test_helper

PKG="$(pwd)/packages/apps/kubernetes-nodes"

minors() {
  sed -n 's/^"\(v[0-9][0-9.]*\)": .*/\1/p' "$PKG/files/versions.yaml"
}

# The minor with its dots escaped for an extended regex. Plain POSIX, because
# the file sources a shell library and so also runs in the POSIX lane.
minor_re() {
  printf '%s\n' "$1" | sed 's/\./\\./g'
}

@test "every Kubernetes minor of the pool chart has a worker disk pinned by digest" {
  [ -n "$(minors)" ]
  for m in $(minors); do
    f="$PKG/images/kairos-worker-$m.tag"
    [ -f "$f" ] || { echo "missing $f" >&2; exit 1; }
    grep -qE "/kairos-worker-$(minor_re "$m"):[^@[:space:]]+@sha256:[0-9a-f]{64}$" "$f" \
      || { echo "$f does not pin kairos-worker-$m by digest: $(cat "$f")" >&2; exit 1; }
  done
}

# The OS image ref is bare until the first build that publishes it stamps its
# digest; the chart does not read it, so a bare ref breaks no render. Once it
# is pinned it must come from the same build as the disk, which is what makes
# it the image the disk was made from: the two carry the same tag.
@test "every Kubernetes minor of the pool chart has its OS image next to its disk" {
  for m in $(minors); do
    f="$PKG/images/kairos-worker-base-$m.tag"
    [ -f "$f" ] || { echo "missing $f" >&2; exit 1; }
    ref="$(cat "$f")"
    printf '%s\n' "$ref" | grep -qE "/kairos-worker-base-$(minor_re "$m"):[^@[:space:]]+(@sha256:[0-9a-f]{64})?$" \
      || { echo "$f does not name kairos-worker-base-$m: $ref" >&2; exit 1; }
    case "$ref" in
      *@sha256:*)
        tag="$(printf '%s\n' "$ref" | sed 's|.*:\([^:@]*\)@sha256:.*|\1|')"
        disk="$(cat "$PKG/images/kairos-worker-$m.tag")"
        case "$disk" in
          *"/kairos-worker-$m:$tag@"*) ;;
          *) echo "$f is pinned from tag $tag, the disk from another build: $disk" >&2; exit 1 ;;
        esac
        ;;
    esac
  done
}

@test "no worker disk or OS image is pinned for a minor the chart no longer offers" {
  for f in "$PKG"/images/kairos-worker-*.tag; do
    [ -e "$f" ] || continue
    m="${f##*/kairos-worker-}"; m="${m#base-}"; m="${m%.tag}"
    minors | grep -qxF "$m" || { echo "$f has no minor in files/versions.yaml" >&2; exit 1; }
  done
}

@test "the release tooling enumerates the published OS images" {
  . hack/lib/image-refs.sh
  files="$(image_ref_files "$(pwd)/packages")"
  for m in $(minors); do
    printf '%s\n' "$files" | grep -qxF "$PKG/images/kairos-worker-base-$m.tag" \
      || { echo "image_ref_files does not list kairos-worker-base-$m.tag" >&2; exit 1; }
  done
}
