#!/usr/bin/env bats
# Unit test: packages/apps/clickhouse/hack/update-versions.sh (the ClickHouse
# version-map generator).
#
# The generator turns the Docker Hub tag lists of clickhouse/clickhouse-server
# and clickhouse/clickhouse-keeper into files/versions.yaml (a major -> full
# tag map) and rewrites the version enum in values.yaml. These properties are
# load-bearing and each guards a specific failure the review of #3476 caught:
#
#   1. The server/keeper tag intersection must be computed with a byte
#      (lexicographic) collation, not a version collation. `comm` walks its
#      inputs assuming byte order; feeding it `sort -V` output silently drops
#      common tags at every X.9 -> X.10 boundary, so a whole major line can
#      vanish from the enum and existing tenant CRs pinned to it then fail
#      schema validation.
#   2. The default line is patch-pinned and must be reproduced verbatim, so a
#      regeneration never bumps the image of existing default-version installs
#      (the "renders byte-for-byte identically" guarantee of the PR).
#   3. A configured major with no tag common to both images is a hard error,
#      and the generator is atomic: on that error the committed files are left
#      untouched rather than half-rewritten.
#
# The generator is exercised offline: CH_SERVER_TAGS_FILE / CH_KEEPER_TAGS_FILE
# inject the tag lists (no curl), and VALUES_FILE / VERSIONS_FILE stay in a
# scratch dir so the real chart is never touched. Written in the portable
# subset hack/cozytest.sh understands (no `run`/`$status`/`$lines`, no
# setup/teardown, no standalone `}` inside a test body): each assertion exits
# non-zero on failure and the body relies on `set -e`.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
GEN="$REPO_ROOT/packages/apps/clickhouse/hack/update-versions.sh"

# A minimal values.yaml carrying the version section the generator rewrites,
# plus the section boundary it inserts before when the section is absent.
seed_values() {
  cat > "$1" <<'EOF'
## @param {string} storageClass - StorageClass used to store the data.
storageClass: ""

## @enum {string} Version
## @value v24.9
## @param {Version} version - ClickHouse major.minor version to deploy.
version: v24.9

##
## @section Application-specific parameters
##
## @param {int} logTTL - TTL.
logTTL: 15
EOF
}

# Per-test scratch state (no setup() — cozytest.sh does not call it).
init_case() {
  WORK="$(mktemp -d)"
  SERVER="$WORK/server.txt"
  KEEPER="$WORK/keeper.txt"
  export VERSIONS_FILE="$WORK/versions.yaml"
  export VALUES_FILE="$WORK/values.yaml"
  export CH_SERVER_TAGS_FILE="$SERVER"
  export CH_KEEPER_TAGS_FILE="$KEEPER"
  seed_values "$VALUES_FILE"
  : > "$VERSIONS_FILE"
}

@test "intersection keeps tags across the X.9 -> X.10 boundary" {
  init_case
  printf '%s\n' 24.9.2.42 24.9.3.128 24.10.1.1 > "$SERVER"
  cp "$SERVER" "$KEEPER"
  CH_SUPPORTED_MAJORS="24.10 24.9" bash "$GEN"
  grep -q '^"v24.10": "24.10.1.1"$' "$VERSIONS_FILE" || { echo "24.10 dropped from intersection" >&2; exit 1; }
  grep -q '^"v24.9":' "$VERSIONS_FILE" || { echo "24.9 dropped from intersection" >&2; exit 1; }
}

@test "the pinned default line is reproduced, not bumped to the latest patch" {
  init_case
  printf '%s\n' 24.9.2.42 24.9.3.128 > "$SERVER"
  cp "$SERVER" "$KEEPER"
  CH_SUPPORTED_MAJORS="24.9" bash "$GEN"
  grep -q '^"v24.9": "24.9.2.42"$' "$VERSIONS_FILE" || { echo "default not pinned to 24.9.2.42" >&2; exit 1; }
  if grep -q '24.9.3.128' "$VERSIONS_FILE"; then echo "default bumped to newer patch" >&2; exit 1; fi
}

@test "non-default majors resolve to their latest common patch" {
  init_case
  printf '%s\n' 25.3.14.14 25.3.15.1 25.8.28.1 > "$SERVER"
  cp "$SERVER" "$KEEPER"
  CH_SUPPORTED_MAJORS="25.8 25.3" bash "$GEN"
  grep -q '^"v25.3": "25.3.15.1"$' "$VERSIONS_FILE" || { echo "25.3 not latest patch" >&2; exit 1; }
  grep -q '^"v25.8": "25.8.28.1"$' "$VERSIONS_FILE" || { echo "25.8 missing" >&2; exit 1; }
}

@test "a tag present in only one image is not selected" {
  init_case
  printf '%s\n' 25.8.28.1 25.8.29.9 > "$SERVER"
  printf '%s\n' 25.8.28.1 > "$KEEPER"
  CH_SUPPORTED_MAJORS="25.8" bash "$GEN"
  grep -q '^"v25.8": "25.8.28.1"$' "$VERSIONS_FILE" || { echo "server-only tag was selected" >&2; exit 1; }
}

@test "an unresolvable configured major is a hard error" {
  init_case
  printf '%s\n' 24.9.2.42 > "$SERVER"
  cp "$SERVER" "$KEEPER"
  if CH_SUPPORTED_MAJORS="26.99 24.9" bash "$GEN" >/dev/null 2>&1; then echo "expected non-zero exit" >&2; exit 1; fi
}

@test "on error the committed files are left untouched (atomic)" {
  init_case
  printf '%s\n' 24.9.2.42 > "$SERVER"
  cp "$SERVER" "$KEEPER"
  cp "$VERSIONS_FILE" "$WORK/versions.before"
  cp "$VALUES_FILE" "$WORK/values.before"
  if CH_SUPPORTED_MAJORS="26.99" bash "$GEN" >/dev/null 2>&1; then echo "expected non-zero exit" >&2; exit 1; fi
  cmp -s "$VERSIONS_FILE" "$WORK/versions.before" || { echo "versions.yaml changed on failure" >&2; exit 1; }
  cmp -s "$VALUES_FILE" "$WORK/values.before" || { echo "values.yaml changed on failure" >&2; exit 1; }
}

@test "values.yaml enum is rewritten newest-first with the pinned default" {
  init_case
  printf '%s\n' 24.9.2.42 25.3.14.14 25.8.28.1 > "$SERVER"
  cp "$SERVER" "$KEEPER"
  CH_SUPPORTED_MAJORS="25.8 25.3 24.9" bash "$GEN"
  first="$(grep -m1 '@value' "$VALUES_FILE")"
  [ "$first" = "## @value v25.8" ] || { echo "enum not newest-first, got: $first" >&2; exit 1; }
  grep -q '^version: v24.9$' "$VALUES_FILE" || { echo "default not preserved as v24.9" >&2; exit 1; }
  grep -q '^## @section Application-specific parameters$' "$VALUES_FILE" || { echo "section boundary lost" >&2; exit 1; }
  grep -q '^logTTL: 15$' "$VALUES_FILE" || { echo "trailing content lost" >&2; exit 1; }
}
