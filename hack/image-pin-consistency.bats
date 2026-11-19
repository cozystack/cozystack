#!/usr/bin/env bats
# Asserts that no first-party image repository is pinned at more than one
# digest across the committed tree.
#
# Promotion retags by digest: hack/promote-retag.sh copies every collected
# <repo>@<digest> to <repo>:<stable-version>. Two different digests under one
# repository therefore produce two copies competing for the same destination
# tag. The first wins, the second hits the write-once guard, and the promotion
# fails — on release day, in a workflow that runs once per release.
#
# This is not hypothetical. platform-migrations was once pinned twice, the
# second copy by a key that had no producer: it froze at v1.4.0-rc.2 while the
# other advanced to v1.5.0, and v1.6.0, the first release cut through the
# promote path, would have been the first to hit it. A chart that renders one
# image under several keys (backupstrategy-controller's backup client) must
# have them stamped together.
#
# Harness compatibility note: CI runs this file under Bats through
# `make bats-unit-tests`. It also remains compatible with the narrower legacy
# `hack/cozytest.sh` translator, so it avoids `run`, `$status`, `$output`,
# `skip`, and setup()/teardown(). Paths stay repo-root-relative for both
# runners.
#
# Run with: bats hack/image-pin-consistency.bats

load test_helper

@test "no image repository is pinned at more than one digest" {
  tmp=$(mktemp -d)

  # Drive the real promotion selector rather than a reimplementation of it, so
  # this tracks whatever set promotion actually acts on. `env -u REGISTRY`:
  # the CI workflow exports REGISTRY=<OCIR build registry> for every job, but
  # the committed tree vendors its digests under the script's default
  # ghcr.io/cozystack/cozystack — inheriting the ambient value would filter for
  # the wrong registry and match nothing.
  rc=0
  env -u REGISTRY hack/promote-retag.sh v9.9.9 --dry-run \
    >"$tmp/out" 2>"$tmp/err" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "promote-retag.sh exited $rc" >&2
    cat "$tmp/err" >&2
    rm -rf "$tmp"
    return "$rc"
  fi

  # Every planned copy ends in docker://<repo>:v9.9.9. A repository appearing
  # twice means two source digests aimed at one destination tag.
  sed -n 's|.*docker://\(.*\):v9\.9\.9$|\1|p' "$tmp/out" | sort > "$tmp/dests"
  dupes=$(uniq -d < "$tmp/dests")

  if [ -n "$dupes" ]; then
    echo "these repositories are pinned at more than one digest, so promotion" >&2
    echo "would try to retag several digests to the same stable tag and fail:" >&2
    printf '%s\n' "$dupes" >&2
    echo >&2
    echo "Offending pins:" >&2
    for repo in $dupes; do
      grep -rn --include='*.yaml' --include='*.tag' --exclude-dir=charts \
        -- "${repo#ghcr.io/cozystack/cozystack/}" packages/ >&2 || true
    done
    echo >&2
    echo "Give the duplicate key a producer that stamps the same ref, or drop" >&2
    echo "the duplicate pin." >&2
    rm -rf "$tmp"
    return 1
  fi
  rm -rf "$tmp"
}
