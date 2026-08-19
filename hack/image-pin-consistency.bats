#!/usr/bin/env bats
# Asserts that platform-migrations is pinned identically in both of its
# consumers, so a promotion cannot try to retag two digests to one stable tag.
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
# and so froze at v1.4.0-rc.2 while the other advanced. v1.5.4 is the first
# release on this line to be cut through the promote path rather than a full
# rebuild, so it would have been the first to hit it.
#
# BRANCH NOTE (release-1.5): main's copy of this file leads with the GENERAL
# invariant — no first-party repository pinned at more than one digest, derived
# by running promote-retag.sh --dry-run and looking for duplicate destinations.
# That check CANNOT pass on this line and is deliberately not ported. release-1.5
# is the last branch that still builds ubuntu-container-disk, one image per
# Kubernetes minor, all six under a single repository and distinguished only by
# their tag prefix (v1.32-v1.5.2, v1.33-v1.5.2, ...). Six legitimate digests, one
# repository: promote-retag.sh would aim all six at
# ubuntu-container-disk:v1.5.4, write whichever sorts first, and then fail. main
# does not have the problem because it dropped the image in 39dcd8866, six days
# before this branch was cut. Teaching promote-retag.sh to preserve a tag prefix
# is a change to the promotion contract, not a port, so it is reported rather
# than made here. Until it is made, a v1.5.4 promotion needs the retag step run
# by hand for that repository.
#
# Harness note: the CI path is hack/cozytest.sh, NOT real bats. There is no
# `run`, `$status`, `$output`, `skip`, or setup()/teardown(); each test runs as
# a shell function under `set -eu -x`, so a non-zero exit is the failure.
# Paths are repo-root-relative: BATS_TEST_DIRNAME is unset and would abort the
# whole suite under `set -u`.
#
# Run with: hack/cozytest.sh hack/image-pin-consistency.bats

@test "platform-migrations is pinned identically in both consumers" {
  # The two must match exactly, not merely resolve to the same digest:
  # backupstrategy-controller renders its copy into a Pod spec, so a stale tag
  # string there is what an operator reads when asking what is running.
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

@test "packages/core/platform/Makefile is the producer of both pins" {
  # The parity above is a postcondition; this is the mechanism that holds it.
  # Without a producer the second pin drifts again the moment the first moves,
  # and the drift is invisible until a release day.
  mk=packages/core/platform/Makefile
  grep -qF ".migrations.image = strenv(IMAGE)" "$mk"
  grep -qF ".backupStrategyController.chBackupClientImage = strenv(IMAGE)" "$mk"
}
