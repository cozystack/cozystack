#!/usr/bin/env bats
# A kairos pool without kairos.image boots the disk named in
# images/kairos-worker-<minor>.tag for its Kubernetes minor, and the chart
# refuses to render when that file is missing. So every minor the chart offers
# needs one, and nothing else may carry a stale one.

load test_helper

PKG="$(pwd)/packages/apps/kubernetes-nodes"

minors() {
  sed -n 's/^"\(v[0-9][0-9.]*\)": .*/\1/p' "$PKG/files/versions.yaml"
}

@test "every Kubernetes minor of the pool chart has a worker disk pinned by digest" {
  [ -n "$(minors)" ]
  for m in $(minors); do
    f="$PKG/images/kairos-worker-$m.tag"
    [ -f "$f" ] || { echo "missing $f" >&2; exit 1; }
    grep -qE "/kairos-worker-${m//./\\.}:[^@[:space:]]+@sha256:[0-9a-f]{64}$" "$f" \
      || { echo "$f does not pin kairos-worker-$m by digest: $(cat "$f")" >&2; exit 1; }
  done
}

@test "no worker disk is pinned for a minor the chart no longer offers" {
  for f in "$PKG"/images/kairos-worker-*.tag; do
    [ -e "$f" ] || continue
    m="${f##*/kairos-worker-}"; m="${m%.tag}"
    minors | grep -qxF "$m" || { echo "$f has no minor in files/versions.yaml" >&2; exit 1; }
  done
}
