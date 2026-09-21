#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for platform migration 57 --> 58 (keep every existing SeaweedFS
# instance at a master volume size limit of 1000 unless it already names one or
# its master is not an object, before the app chart that defaults it to 400
# reaches it).
#
# Five properties are pinned here:
#
#  1. THE RIGHT INSTANCES. The fleet list uses the selector cozystack-api finds
#     SeaweedFS with, across all namespaces. The fake kubectl refuses any other
#     list instead of answering it with nothing, because an empty fleet is a
#     legitimate answer: a mistyped selector would otherwise stay green and
#     leave every production instance unpinned.
#
#  2. ONLY WHERE NOT CHOSEN, AND EXACTLY THIS PATCH. A release with no
#     spec.values (the tenant module's shape), one whose values lack master,
#     one whose master lacks the key, and one holding a null, a string, a zero,
#     a fraction or a number past 30000 are each patched, in their own
#     namespace, with exactly
#     {"spec":{"values":{"master":{"volumeSizeLimitMB":1000}}}}. The previous
#     chart ignored every one of those and the new schema refuses the last
#     four. A release naming a whole number from 1 to 30000 is left alone,
#     edges included, and named in the Job log, since the value now takes
#     effect, unless the number is 1000, which changes nothing and is what a
#     retried Job finds on the releases it already pinned. A spec.values that
#     is not an object is patched too, since helm-controller ignores it and the
#     instance runs on the chart's defaults. A master that is not an object is
#     not patched, and a valuesFrom beyond the cozystack-values Secret does not
#     stop the patch. Each of the three is named in the Job log with what it
#     holds, and the releases after it are still reached.
#
#  3. FAIL CLOSED ON THE LIST. A failed list aborts before the version stamp, so
#     the platform upgrade stops before any instance can reach the new chart.
#
#  4. FAIL CLOSED ON THE PATCH. Migrations never re-run, so a swallowed patch
#     error would stamp past an instance that then drops to 400.
#
#  5. THE RUNBOOK AGREES. The two steps of
#     docs/operations/seaweedfs-lowering-the-volume-size-limit.md that restate
#     this selection, the listing every variant runs before upgrading and the
#     pinning the default variant runs instead of this migration, found by the
#     HTML comment above each, pick the same releases on the same fleet, and
#     the pinning step stops at a failed patch before its closing line.
#
# These drive the real migration script end-to-end against a fake kubectl
# (hack/testdata/migration-57-seaweedfs-size-limit/), mocking only the cluster
# boundary.
#
# SHELL. Production runs the migration under /bin/sh = busybox ash, by path, so
# run_migration() runs it by path inside the migrations image's own pinned base
# with jq added, --network none. See hack/migration-54-redis-adopt.bats for the
# full reasoning; it applies unchanged.
#
# cozytest.sh's awk parser recognizes only @test blocks and a bare `}` on its own
# line, rewriting the latter into `return 0` + `}`. A helper whose exit status
# matters must capture it and `return` it by hand before its closing brace.
#
# Run with: hack/cozytest.sh hack/migration-57-seaweedfs-size-limit.bats
# -----------------------------------------------------------------------------

FAKEBIN="$PWD/hack/testdata/migration-57-seaweedfs-size-limit"
MIG_DIR="$PWD/packages/core/platform/images/migrations/migrations"
RUNBOOK="$PWD/docs/operations/seaweedfs-lowering-the-volume-size-limit.md"
SELECTOR='apps.cozystack.io/application.kind=SeaweedFS,apps.cozystack.io/application.group=apps.cozystack.io'
PIN='{"spec":{"values":{"master":{"volumeSizeLimitMB":1000}}}}'

ALPINE=$(sed -n 's/^FROM \(alpine:[^ ]*\).*$/\1/p' \
  "$PWD/packages/core/platform/images/migrations/Dockerfile" | head -1)

TESTIMG="cozystack-migration57-test:$(printf '%s' "$ALPINE" | sed 's/[^a-zA-Z0-9]/-/g')"

# run_in_image <command...> -- run a command in the test image against the fake
# kubectl, with $WORK mounted at /work.
run_in_image() {
  _run_in_image_rc=0
  docker run --rm --network none \
    --user "$(id -u):$(id -g)" \
    -v "$MIG_DIR:/migrations:ro" \
    -v "$FAKEBIN:/fakebin:ro" \
    -v "$WORK:/work" \
    -e PATH=/fakebin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    -e FAKE_CMDLOG=/work/cmdlog \
    -e NAMESPACE="${NAMESPACE-}" \
    -e FAKE_HRS="${FAKE_HRS-}" \
    -e FAKE_LIST_FAIL="${FAKE_LIST_FAIL-}" \
    -e FAKE_PATCH_FAIL="${FAKE_PATCH_FAIL-}" \
    -e FAKE_VALUESFROM="${FAKE_VALUESFROM-}" \
    "$TESTIMG" "$@" || _run_in_image_rc=$?
  return "$_run_in_image_rc"
}

run_migration() {
  run_in_image "/migrations/$1"
  return $?
}

# runbook_step <marker> -- copy the sh block that follows <!-- <marker> --> in
# the runbook to $WORK/<marker>.sh, failing if there is none.
runbook_step() {
  awk -v m="<!-- $1 -->" '$0 == m { f = 1; next } f && /^```sh$/ { b = 1; next } b && /^```$/ { exit } b' \
    "$RUNBOOK" > "$WORK/$1.sh"
  grep -q 'kubectl get helmreleases' "$WORK/$1.sh"
  return $?
}

prep() {
  docker info >/dev/null 2>&1 || {
    echo "docker is required: these tests run migration 57 inside a jq-enabled" >&2
    echo "build of $ALPINE, the migrations image's base." >&2
    return 1
  }
  docker build -q -t "$TESTIMG" - >/dev/null <<DOCKERFILE
FROM $ALPINE
RUN apk add --no-cache jq
DOCKERFILE
  chmod +x "$FAKEBIN/kubectl"
  WORK=$(mktemp -d)
  export FAKE_CMDLOG="$WORK/cmdlog"
  : > "$FAKE_CMDLOG"
  export NAMESPACE=cozy-system
  export FAKE_HRS=""
  unset FAKE_LIST_FAIL FAKE_PATCH_FAIL FAKE_VALUESFROM || true
  return 0
}

# patch_for <ns> <name> -- the merge-patch body sent for that release, from its
# `PATCH <ns> <name> <json>` cmdlog line. Used only for its stdout.
patch_for() {
  grep -E "^PATCH $1 $2 " "$FAKE_CMDLOG" | head -1 | cut -d' ' -f4-
}

# listed_with_selector -- the fleet list was made once, with the exact selector.
listed_with_selector() {
  _n=$(grep -cxF -- "KUBECTL get helmreleases.helm.toolkit.fluxcd.io --all-namespaces -l $SELECTOR -o json" "$FAKE_CMDLOG")
  [ "$_n" -eq 1 ]
  return $?
}

# --- 1 and 2. the right instances, only where not chosen, exactly this patch -

@test "pins every instance that has not chosen a limit and leaves the rest alone" {
  prep
  # One row per shape. The first eight have not chosen a limit the new chart
  # accepts and must be pinned; the last six must not be touched.
  export FAKE_HRS='tenant-root seaweedfs -
tenant-api seaweedfs {"host":"s3.example.org"}
tenant-rep seaweedfs {"master":{"replicas":3}}
tenant-null seaweedfs {"master":{"volumeSizeLimitMB":null}}
tenant-str seaweedfs {"master":{"volumeSizeLimitMB":"400"}}
tenant-zero seaweedfs {"master":{"volumeSizeLimitMB":0}}
tenant-frac seaweedfs {"master":{"volumeSizeLimitMB":400.5}}
tenant-big seaweedfs {"master":{"volumeSizeLimitMB":30001}}
tenant-1 seaweedfs {"master":{"volumeSizeLimitMB":1}}
tenant-400 seaweedfs {"master":{"volumeSizeLimitMB":400}}
tenant-30000 seaweedfs {"master":{"volumeSizeLimitMB":30000}}
tenant-2000 seaweedfs {"master":{"volumeSizeLimitMB":2000}}
tenant-1000 seaweedfs {"master":{"volumeSizeLimitMB":1000}}
tenant-odd seaweedfs {"master":"unexpected"}'
  rc=0
  run_migration 57 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  listed_with_selector

  [ "$(grep -cE '^PATCH ' "$FAKE_CMDLOG")" -eq 8 ]
  for ns in tenant-root tenant-api tenant-rep tenant-null tenant-str tenant-zero tenant-frac tenant-big; do
    [ "$(patch_for "$ns" seaweedfs)" = "$PIN" ]
  done
  for ns in tenant-1 tenant-400 tenant-30000 tenant-2000 tenant-1000 tenant-odd; do
    [ -z "$(patch_for "$ns" seaweedfs)" ]
  done
  # A patch sent without its namespace would land in the Job's own namespace.
  [ "$(grep -cE '^PATCH <none> ' "$FAKE_CMDLOG")" -eq 0 ]

  # Every kept value but 1000 is named in the Job log with the number it now
  # applies, since the previous chart ignored it; nothing else is.
  for kept in tenant-1:1 tenant-400:400 tenant-30000:30000 tenant-2000:2000; do
    grep -qF -- "Keeping SeaweedFS ${kept%%:*}/seaweedfs at master.volumeSizeLimitMB=${kept#*:}:" "$WORK/out"
  done
  [ "$(grep -c '^Keeping SeaweedFS ' "$WORK/out")" -eq 4 ]

  grep -qxF -- "STAMP 58" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a release whose spec.values is not an object does not stop the fleet" {
  prep
  export FAKE_HRS='tenant-bad seaweedfs "oops"
tenant-root seaweedfs -
tenant-list seaweedfs ["a"]
tenant-api seaweedfs {"host":"s3.example.org"}'
  rc=0
  run_migration 57 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  listed_with_selector
  for ns in tenant-root tenant-api; do
    [ "$(patch_for "$ns" seaweedfs)" = "$PIN" ]
  done
  for ns in tenant-bad tenant-list; do
    [ "$(patch_for "$ns" seaweedfs)" = "$PIN" ]
  done
  grep -qF -- 'WARNING: SeaweedFS tenant-bad/seaweedfs has a spec.values that is not an object' "$WORK/out"
  grep -qF -- 'It held: "oops"' "$WORK/out"
  grep -qF -- 'WARNING: SeaweedFS tenant-list/seaweedfs has a spec.values that is not an object' "$WORK/out"
  grep -qF -- 'It held: ["a"]' "$WORK/out"
  [ "$(grep -c '^WARNING: ' "$WORK/out")" -eq 2 ]
  grep -qxF -- "STAMP 58" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a master that is not an object is named and left unpinned" {
  prep
  export FAKE_HRS='tenant-odd seaweedfs {"master":"unexpected"}
tenant-root seaweedfs -
tenant-olist seaweedfs {"master":[400]}
tenant-api seaweedfs {"host":"s3.example.org"}'
  rc=0
  run_migration 57 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  for ns in tenant-root tenant-api; do
    [ "$(patch_for "$ns" seaweedfs)" = "$PIN" ]
  done
  for ns in tenant-odd tenant-olist; do
    [ -z "$(patch_for "$ns" seaweedfs)" ]
  done
  grep -qF -- 'WARNING: SeaweedFS tenant-odd/seaweedfs is not pinned: its master is not an object' "$WORK/out"
  grep -qF -- 'master holds: "unexpected"' "$WORK/out"
  grep -qF -- 'WARNING: SeaweedFS tenant-olist/seaweedfs is not pinned: its master is not an object' "$WORK/out"
  grep -qF -- 'master holds: [400]' "$WORK/out"
  [ "$(grep -c '^WARNING: ' "$WORK/out")" -eq 2 ]
  grep -qxF -- "STAMP 58" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a valuesFrom beyond the cozystack-values Secret is named and does not stop the pin" {
  prep
  export FAKE_HRS='tenant-vf seaweedfs -
tenant-vfkeep seaweedfs {"master":{"volumeSizeLimitMB":400}}
tenant-std seaweedfs -
tenant-nofrom seaweedfs -'
  export FAKE_VALUESFROM='tenant-vf [{"kind":"Secret","name":"cozystack-values"},{"kind":"ConfigMap","name":"limits","valuesKey":"limit","targetPath":"master.volumeSizeLimitMB"}]
tenant-vfkeep [{"kind":"ConfigMap","name":"limits"}]
tenant-nofrom -'
  rc=0
  run_migration 57 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  for ns in tenant-vf tenant-std tenant-nofrom; do
    [ "$(patch_for "$ns" seaweedfs)" = "$PIN" ]
  done
  [ -z "$(patch_for tenant-vfkeep seaweedfs)" ]
  # Only the entries besides the platform's own Secret are listed.
  grep -qxF -- 'WARNING: SeaweedFS tenant-vf/seaweedfs also takes values from [{"kind":"ConfigMap","name":"limits","valuesKey":"limit","targetPath":"master.volumeSizeLimitMB"}]. spec.values is merged over them, so a master.volumeSizeLimitMB set there without targetPath does not apply, and one set through targetPath can, depending on the entries before it' "$WORK/out"
  grep -qF -- 'WARNING: SeaweedFS tenant-vfkeep/seaweedfs also takes values from [{"kind":"ConfigMap","name":"limits"}].' "$WORK/out"
  [ "$(grep -c '^WARNING: ' "$WORK/out")" -eq 2 ]
  grep -qxF -- "STAMP 58" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "an empty fleet stamps 58 without patching anything" {
  prep
  export FAKE_HRS=""
  rc=0
  run_migration 57 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  # The list was really made, with the right selector: an empty answer to a
  # list that never happened would look the same from here.
  listed_with_selector
  [ "$(grep -cE '^PATCH ' "$FAKE_CMDLOG")" -eq 0 ]
  grep -qxF -- "STAMP 58" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

# --- 3. fail closed on the list ----------------------------------------------

@test "a failing fleet list aborts before stamping" {
  prep
  export FAKE_HRS='tenant-root seaweedfs -'
  export FAKE_LIST_FAIL=1
  rc=0
  run_migration 57 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  [ "$(grep -cE '^PATCH ' "$FAKE_CMDLOG")" -eq 0 ]
  [ "$(grep -cE '^STAMP ' "$FAKE_CMDLOG")" -eq 0 ]
  rm -rf "$WORK"
}

# --- 4. fail closed on the patch ---------------------------------------------

@test "a failed patch aborts before stamping" {
  prep
  export FAKE_HRS='tenant-root seaweedfs -
tenant-api seaweedfs {"host":"s3.example.org"}'
  export FAKE_PATCH_FAIL=1
  rc=0
  run_migration 57 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  # The abort is the patch's: it was attempted, and nothing after it was.
  [ "$(grep -c ' patch helmreleases' "$FAKE_CMDLOG")" -eq 1 ]
  grep -qF -- "KUBECTL -n tenant-root patch helmreleases.helm.toolkit.fluxcd.io seaweedfs " "$FAKE_CMDLOG"
  [ "$(grep -cE '^STAMP ' "$FAKE_CMDLOG")" -eq 0 ]
  rm -rf "$WORK"
}

# --- 5. the runbook agrees ----------------------------------------------------

# One row per shape the selection tells apart.
RUNBOOK_FLEET='tenant-root seaweedfs -
tenant-bad seaweedfs "oops"
tenant-list seaweedfs ["a"]
tenant-api seaweedfs {"host":"s3.example.org"}
tenant-null seaweedfs {"master":{"volumeSizeLimitMB":null}}
tenant-str seaweedfs {"master":{"volumeSizeLimitMB":"400"}}
tenant-frac seaweedfs {"master":{"volumeSizeLimitMB":400.5}}
tenant-big seaweedfs {"master":{"volumeSizeLimitMB":30001}}
tenant-400 seaweedfs {"master":{"volumeSizeLimitMB":400}}
tenant-2000 seaweedfs {"master":{"volumeSizeLimitMB":2000}}
tenant-1000 seaweedfs {"master":{"volumeSizeLimitMB":1000}}
tenant-odd seaweedfs {"master":"unexpected"}
tenant-vf seaweedfs -
tenant-vfkeep seaweedfs {"master":{"volumeSizeLimitMB":400}}'
RUNBOOK_VALUESFROM='tenant-vf [{"kind":"Secret","name":"cozystack-values"},{"kind":"ConfigMap","name":"limits"}]
tenant-vfkeep [{"kind":"ConfigMap","name":"limits"}]'

# releases_in <file> <sed-expression> -- the sorted "<ns> <name>" pairs the
# expression prints from the file.
releases_in() {
  sed -n "$2" "$1" | sort
}

@test "the runbook's steps select the releases migration 57 does" {
  prep
  export FAKE_HRS="$RUNBOOK_FLEET" FAKE_VALUESFROM="$RUNBOOK_VALUESFROM"
  runbook_step migration-57-listing
  runbook_step migration-57-pin
  run_migration 57 >"$WORK/mig.out" 2>&1
  cp "$FAKE_CMDLOG" "$WORK/mig.log"; : > "$FAKE_CMDLOG"
  # stdout only: the xtrace of this runner goes to stderr, and the closing line
  # is checked by position.
  run_in_image sh /work/migration-57-listing.sh >"$WORK/listing.out" 2>"$WORK/listing.err"
  run_in_image sh /work/migration-57-pin.sh >"$WORK/pin.out" 2>"$WORK/pin.err"
  cp "$FAKE_CMDLOG" "$WORK/pin.log"
  cat "$WORK/mig.out" "$WORK/listing.out" "$WORK/pin.out"

  releases_in "$WORK/mig.log" 's/^PATCH \([^ ]*\) \([^ ]*\) .*/\1 \2/p' > "$WORK/a"
  releases_in "$WORK/pin.log" 's/^PATCH \([^ ]*\) \([^ ]*\) .*/\1 \2/p' > "$WORK/b"
  [ "$(wc -l < "$WORK/a")" -eq 9 ]
  cmp "$WORK/a" "$WORK/b"

  releases_in "$WORK/mig.out" 's/^Keeping SeaweedFS \([^/]*\)\/\([^ ]*\) at master.volumeSizeLimitMB=\([0-9]*\):.*/\1 \2 \3/p' > "$WORK/a"
  releases_in "$WORK/listing.out" 's/^changes //p' > "$WORK/b"
  [ "$(wc -l < "$WORK/a")" -eq 3 ]
  cmp "$WORK/a" "$WORK/b"

  for what in unreadable:'has a spec.values' unrenderable:'is not pinned' valuesfrom:'also takes values'; do
    releases_in "$WORK/mig.out" "s/^WARNING: SeaweedFS \\([^/]*\\)\\/\\([^ ]*\\) ${what#*:}.*/\\1 \\2/p" > "$WORK/a"
    releases_in "$WORK/listing.out" "s/^${what%%:*} \\([^ ]*\\) \\([^ ]*\\) .*/\\1 \\2/p" > "$WORK/b"
    releases_in "$WORK/pin.out" "s/^${what%%:*} \\([^ ]*\\) \\([^ ]*\\) .*/\\1 \\2/p" > "$WORK/c"
    [ -s "$WORK/a" ]
    cmp "$WORK/a" "$WORK/b"
    cmp "$WORK/a" "$WORK/c"
  done

  for out in listing pin; do
    [ "$(head -1 "$WORK/$out.out")" = "instances 14" ]
    [ "$(tail -1 "$WORK/$out.out")" = "complete 14" ]
  done
  rm -rf "$WORK"
}

@test "the runbook's pinning step stops at a failed patch without its closing line" {
  prep
  export FAKE_HRS="$RUNBOOK_FLEET" FAKE_VALUESFROM="$RUNBOOK_VALUESFROM"
  export FAKE_PATCH_FAIL=1
  runbook_step migration-57-pin
  run_in_image sh /work/migration-57-pin.sh >"$WORK/pin.out" 2>"$WORK/pin.err"
  cat "$WORK/pin.out" "$FAKE_CMDLOG"
  [ "$(grep -c ' patch helmreleases' "$FAKE_CMDLOG")" -eq 1 ]
  grep -qxF -- "stopped: patching tenant-root/seaweedfs failed" "$WORK/pin.out"
  [ "$(grep -c '^complete' "$WORK/pin.out")" -eq 0 ]
  rm -rf "$WORK"
}
