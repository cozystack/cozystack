#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for platform migration 54 --> 55 (adopt every existing
# RedisFailover onto the freshworks-oss redis-operator's group filter AND pin
# its engine, before the new operator starts).
#
# redis-operator v3.3.5 watches RedisFailovers through a server-side label
# selector (redis-failover.freshworks.com/operator-group=cozystack); a CR
# without that label never reaches the operator's informer. The same window is
# dangerous for the engine version: v3.3.5's compiled-in default moved
# 6.2.6 -> 7.2.4, which rewrites dump.rdb into a format 6.2.6 cannot load. So the
# migration, in one pass ahead of the new operator, ALWAYS stamps the label and
# pins spec.redis.image / spec.sentinel.image to redis:6.2.6-alpine ONLY where
# that field is empty — leaving a preset image (apps/redis pins its data image
# via versionMap) untouched.
#
# Three properties are pinned here:
#
#  1. FAIL CLOSED ON THE PROBE. `kubectl get crd ... --ignore-not-found -o name`
#     separates a genuinely absent CRD (rc 0, empty -> nothing to adopt, stamp
#     and exit) from a real apiserver error (rc != 0 -> abort under
#     set -euo pipefail). A one-off blip read as "absent" would stamp the
#     version and leave the whole fleet unlabelled and invisible to the new
#     operator, permanently — the exact failure this migration exists to
#     prevent.
#
#  2. LABEL ALWAYS, PIN ONLY WHERE EMPTY. Every adopted CR's patch carries the
#     operator-group label. The engine pin is added only for a role whose image
#     is empty: a both-empty CR (Harbor) gets both images pinned; a
#     sentinel-only-empty CR (apps/redis, redis.image preset) gets ONLY the
#     sentinel pinned and its preset redis.image is never overwritten; a
#     fully-preset CR gets the label and no pin at all.
#
#  3. FAIL CLOSED ON THE PATCH. Migrations never re-run, so a swallowed patch
#     error would stamp the version on a half-adopted fleet and never look back.
#     A failed patch must abort BEFORE stamping so the Job retries.
#
# These drive the real migration script end-to-end against a fake kubectl
# (hack/testdata/migration-54-redis/), mocking only the cluster boundary.
#
# SHELL. Production runs migration 54 under /bin/sh = busybox ash: the migrations
# image is FROM alpine and run-migrations.sh execs `/migrations/<n>` BY PATH, so
# the kernel honours the `#!/bin/sh` shebang. `set -euo pipefail` and its
# errexit semantics differ from bash, and pipefail is load-bearing: the fleet
# scan pipes `kubectl ... -o json` into jq. So run_migration() runs the script by
# path inside the image's own pinned base rather than through a host shell — same
# base image, same interpreter, same invocation form.
#
# jq. Migration 54 pipes through jq (fleet scan, per-CR image read, patch
# construction). The raw alpine base ships without it while the built migrations
# image installs it via `apk add`, so prep() bakes jq onto the pinned base once
# (the only step that touches the network) and run_migration() uses that image,
# still --network none.
#
# cozytest.sh's awk parser recognizes only @test blocks and a bare `}` on its own
# line, rewriting the latter into `return 0` + `}`; there is no bats
# `run`/`$status`/`setup`. A helper whose exit status matters must therefore
# capture it and `return` it by hand before its closing brace, or the injected
# `return 0` would mask it (see run_migration below). Assertions are direct shell
# tests that exit non-zero on failure.
#
# Run with: hack/cozytest.sh hack/migration-54-redis-adopt.bats
# -----------------------------------------------------------------------------

FAKEBIN="$PWD/hack/testdata/migration-54-redis"
MIG_DIR="$PWD/packages/core/platform/images/migrations/migrations"

# The production base image, read out of the migrations Dockerfile rather than
# repeated here, so the interpreter under test cannot drift from the one the
# migrations actually ship on when that pin is bumped.
ALPINE=$(sed -n 's/^FROM \(alpine:[^ ]*\).*$/\1/p' \
  "$PWD/packages/core/platform/images/migrations/Dockerfile" | head -1)

# jq-enabled build of that base. The tag is derived from the pinned ref so a
# digest bump rebuilds it instead of reusing a stale layer.
TESTIMG="cozystack-migration54-test:$(printf '%s' "$ALPINE" | sed 's/[^a-zA-Z0-9]/-/g')"

# run_migration <n> -- run migrations/<n> the way run-migrations.sh does.
#
# By path, not `sh <file>`: that is what makes the shebang, and therefore the
# interpreter, part of what is under test. The fake kubectl goes on PATH inside
# the container and $WORK is bind-mounted, so $FAKE_CMDLOG is the same file the
# assertions read back on the host. --network none because nothing here may reach
# a real cluster; --user keeps $WORK removable by the test afterwards.
#
# The explicit `return` is load-bearing: cozytest.sh's awk generator rewrites
# every bare `}` in column 0 into `return 0` + `}`, so a helper that falls off
# its own end returns 0 no matter what it ran, and every fail-closed assertion
# below would pass vacuously. Capture the status and return it by hand.
run_migration() {
  _run_migration_rc=0
  docker run --rm --network none \
    --user "$(id -u):$(id -g)" \
    -v "$MIG_DIR:/migrations:ro" \
    -v "$FAKEBIN:/fakebin:ro" \
    -v "$WORK:/work" \
    -e PATH=/fakebin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    -e FAKE_CMDLOG=/work/cmdlog \
    -e NAMESPACE="${NAMESPACE-}" \
    -e FAKE_RFS="${FAKE_RFS-}" \
    -e FAKE_CRD_ABSENT="${FAKE_CRD_ABSENT-}" \
    -e FAKE_CRD_PROBE_FAIL="${FAKE_CRD_PROBE_FAIL-}" \
    -e FAKE_LABEL_FAIL="${FAKE_LABEL_FAIL-}" \
    "$TESTIMG" "/migrations/$1" || _run_migration_rc=$?
  return "$_run_migration_rc"
}

# prep resets env to a clean scenario. Tests set FAKE_* afterwards.
prep() {
  # Fail here rather than at the first docker run, so the reason is legible.
  docker info >/dev/null 2>&1 || {
    echo "docker is required: these tests run migration 54 inside a jq-enabled" >&2
    echo "build of $ALPINE (the migrations image's base), so that it exercises" >&2
    echo "busybox ash — the interpreter run-migrations.sh actually gives it." >&2
    return 1
  }
  # Bake jq onto the pinned base. Cached after the first run, so this is a no-op
  # on every subsequent test; only the first build touches the network.
  docker build -q -t "$TESTIMG" - >/dev/null <<DOCKERFILE
FROM $ALPINE
RUN apk add --no-cache jq
DOCKERFILE
  chmod +x "$FAKEBIN/kubectl"
  WORK=$(mktemp -d)
  export FAKE_CMDLOG="$WORK/cmdlog"
  : > "$FAKE_CMDLOG"
  export NAMESPACE=cozy-system
  export FAKE_RFS=""
  unset FAKE_CRD_ABSENT FAKE_CRD_PROBE_FAIL FAKE_LABEL_FAIL || true
  return 0
}

# patch_for <ns> <name> -- the merge-patch payload the migration sent for that
# CR (the JSON tail of its `PATCH <ns> <name> <json>` cmdlog line). Used only for
# its stdout, so the awk-injected `return 0` is harmless.
patch_for() {
  grep -E "^PATCH $1 $2 " "$FAKE_CMDLOG" | head -1 | cut -d' ' -f4-
}

# --- 1. fail closed on the probe --------------------------------------------

@test "absent CRD stamps 55 without patching anything" {
  prep
  export FAKE_CRD_ABSENT=1
  rc=0
  run_migration 54 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  # Nothing to adopt: no patch was ever issued...
  [ "$(grep -cE -- '^PATCH ' "$FAKE_CMDLOG")" -eq 0 ]
  # ...but the version is stamped to 55 so the runner advances. Asserting the
  # number, not a bare "STAMP": a wrong version would loop run-migrations.sh.
  grep -qF -- "STAMP 55" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

# A real apiserver error on the CRD probe is NOT "absent". The migration must
# abort under set -euo pipefail before stamping, so the Job retries rather than
# stamping past an un-probed, un-adopted fleet.
@test "a failing CRD probe aborts before stamping" {
  prep
  export FAKE_CRD_PROBE_FAIL=1
  rc=0
  run_migration 54 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  # Must propagate: a swallowed probe error would stamp past the whole fleet.
  [ "$rc" -ne 0 ]
  # Nothing was patched and nothing was stamped.
  [ "$(grep -cE -- '^PATCH ' "$FAKE_CMDLOG")" -eq 0 ]
  [ "$(grep -cF -- 'STAMP 55' "$FAKE_CMDLOG")" -eq 0 ]
  rm -rf "$WORK"
}

# --- 2. label always, pin only where empty ----------------------------------

@test "adopts every RedisFailover across namespaces: label always, engine pin only where empty" {
  prep
  # Three CRs in three namespaces exercising each image shape:
  #   harbor      both images empty  (Harbor)      -> pin BOTH
  #   my-redis    redis preset, sentinel empty     -> pin ONLY sentinel
  #   full-redis  both images preset               -> label only, no pin
  export FAKE_RFS="tenant-harbor harbor - -
tenant-redis my-redis redis:8.4.0 -
tenant-full full-redis redis:7.0.0 redis:7.2.0"
  rc=0
  run_migration 54 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]

  # Exactly the three CRs were patched, no more.
  [ "$(grep -cE '^PATCH ' "$FAKE_CMDLOG")" -eq 3 ]
  # Every patch carries the operator-group label (adoption is unconditional).
  # Scope the count to PATCH lines: the fake also logs a raw KUBECTL line per
  # call, so the label string appears twice per patch across the whole cmdlog.
  [ "$(grep -E '^PATCH ' "$FAKE_CMDLOG" | grep -cF -- '"redis-failover.freshworks.com/operator-group":"cozystack"')" -eq 3 ]

  # Harbor: both images empty -> both pinned to redis:6.2.6-alpine, plus label.
  ph=$(patch_for tenant-harbor harbor)
  echo "$ph" | grep -qF -- '"redis-failover.freshworks.com/operator-group":"cozystack"'
  echo "$ph" | grep -qF -- '"redis":{"image":"redis:6.2.6-alpine"}'
  echo "$ph" | grep -qF -- '"sentinel":{"image":"redis:6.2.6-alpine"}'

  # apps/redis: redis.image preset, sentinel empty -> ONLY sentinel pinned. The
  # preset redis.image is neither overwritten nor even mentioned (a merge patch
  # only carries what changes), so there is no redis node in the payload at all.
  pr=$(patch_for tenant-redis my-redis)
  echo "$pr" | grep -qF -- '"redis-failover.freshworks.com/operator-group":"cozystack"'
  echo "$pr" | grep -qF -- '"sentinel":{"image":"redis:6.2.6-alpine"}'
  [ "$(printf '%s' "$pr" | grep -cF -- '"redis":')" -eq 0 ]
  [ "$(printf '%s' "$pr" | grep -cF -- 'redis:8.4.0')" -eq 0 ]

  # Fully-preset: label only. Neither image is empty, so no engine pin and no
  # spec node at all.
  pf=$(patch_for tenant-full full-redis)
  echo "$pf" | grep -qF -- '"redis-failover.freshworks.com/operator-group":"cozystack"'
  [ "$(printf '%s' "$pf" | grep -cF -- 'redis:6.2.6-alpine')" -eq 0 ]
  [ "$(printf '%s' "$pf" | grep -cF -- '"spec":')" -eq 0 ]

  # Version stamped to 55 once, after adoption.
  grep -qF -- "STAMP 55" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

# --- 3. fail closed on the patch --------------------------------------------

# The adoption patch fails. Applying the label/pin is the whole point of the
# migration, so a failed patch must abort BEFORE stamping — recoverable on the
# Job's next attempt, rather than a fleet stamped past adoption and left
# invisible to the new operator forever.
@test "a failed adoption patch aborts before stamping" {
  prep
  export FAKE_RFS="tenant-harbor harbor - -
tenant-redis my-redis redis:8.4.0 -"
  export FAKE_LABEL_FAIL=1
  rc=0
  run_migration 54 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  # Must propagate: a swallowed patch error would stamp a half-adopted fleet.
  [ "$rc" -ne 0 ]
  # And it must not have stamped the version.
  [ "$(grep -cF -- 'STAMP 55' "$FAKE_CMDLOG")" -eq 0 ]
  rm -rf "$WORK"
}
