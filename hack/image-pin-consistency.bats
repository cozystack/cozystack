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
# This is not hypothetical. platform-migrations was pinned twice: once in
# packages/core/platform/values.yaml (stamped by its Makefile) and once as
# .backupStrategyController.chBackupClientImage, which had no producer at all
# and so froze at v1.4.0-rc.2 while the other advanced to v1.5.0. v1.6.0 is the
# first release cut through the promote path rather than a full rebuild, so it
# would have been the first to hit it.
#
# The general invariant is cheaper to hold than the specific one: a duplicate
# pin can arise from any package reusing another's image, which the tree does
# deliberately (backupstrategy-controller reuses platform-migrations as a
# curl+jq runner rather than shipping a second one-binary tag).
#
# Harness note: the CI path is hack/cozytest.sh, NOT real bats. There is no
# `run`, `$status`, `$output`, `skip`, or setup()/teardown(); each test runs as
# a shell function under `set -eu -x`, so a non-zero exit is the failure.
# Paths are repo-root-relative: BATS_TEST_DIRNAME is unset and would abort the
# whole suite under `set -u`.
#
# Run with: hack/cozytest.sh hack/image-pin-consistency.bats

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
    echo "Give the duplicate key a producer that stamps the same ref (see" >&2
    echo "packages/core/platform/Makefile), or drop the duplicate pin." >&2
    rm -rf "$tmp"
    return 1
  fi
  rm -rf "$tmp"
}

@test "platform-migrations is pinned identically in both consumers" {
  # The specific instance, pinned separately so a regression names its cause
  # rather than only tripping the general check above. The two must match
  # exactly, not merely resolve to the same digest: backupstrategy-controller
  # renders its copy into a Pod spec, so a stale tag string there is what an
  # operator reads when asking what is running.
  a=$(yq -r '.migrations.image' packages/core/platform/values.yaml)
  b=$(yq -r '.backupStrategyController.chBackupClientImage' \
    packages/system/backupstrategy-controller/values.yaml)

  if [ "$a" != "$b" ]; then
    echo "platform-migrations pins have drifted:" >&2
    echo "  packages/core/platform/values.yaml            .migrations.image" >&2
    echo "    $a" >&2
    echo "  backupstrategy-controller/values.yaml         .chBackupClientImage" >&2
    echo "    $b" >&2
    echo >&2
    echo "packages/core/platform/Makefile stamps both; do not hand-edit either." >&2
    return 1
  fi
}
