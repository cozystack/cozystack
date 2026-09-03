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
#      common tags at every X.9 -> X.10 boundary.
#   2. The default line is frozen to the exact tag it already ships (read from
#      the committed versions.yaml) so a regeneration never bumps the image of
#      existing default-version installs. The freeze follows whichever major is
#      the default, not a hardcoded one.
#   3. Dropping the current default's major from the supported set is a hard
#      error, not a silent promotion of the newest major to default.
#   4. A configured major with no tag common to both images is a hard error,
#      and the generator is atomic: on any error the committed files are left
#      untouched.
#   5. The values.yaml splice is bounded: a malformed section (enum header
#      present, `version:` line absent) does not swallow the rest of the file.
#
# The generator is exercised offline: CH_SERVER_TAGS_FILE / CH_KEEPER_TAGS_FILE
# inject the tag lists (no curl), and VALUES_FILE / VERSIONS_FILE stay in a
# scratch dir. Written in the portable subset hack/cozytest.sh understands (no
# `run`/`$status`/`$lines`, no setup/teardown, no standalone `}` inside a test
# body): each assertion exits non-zero on failure and the body relies on
# `set -e`.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
GEN="$REPO_ROOT/packages/apps/clickhouse/hack/update-versions.sh"

# Minimal values.yaml with the version section the generator rewrites. $2 is
# the default version written into both the enum and the `version:` line.
seed_values() {
  cat > "$1" <<EOF
## @param {string} storageClass - StorageClass used to store the data.
storageClass: ""

## @enum {string} Version
## @value $2
## @param {Version} version - ClickHouse major.minor version to deploy.
version: $2

##
## @section Application-specific parameters
##
## @param {int} logTTL - TTL.
logTTL: 15
EOF
}

init_case() {
  WORK="$(mktemp -d)"
  SERVER="$WORK/server.txt"
  KEEPER="$WORK/keeper.txt"
  export VERSIONS_FILE="$WORK/versions.yaml"
  export VALUES_FILE="$WORK/values.yaml"
  export CH_SERVER_TAGS_FILE="$SERVER"
  export CH_KEEPER_TAGS_FILE="$KEEPER"
  : > "$VERSIONS_FILE"
}

@test "intersection keeps tags across the X.9 -> X.10 boundary" {
  init_case
  seed_values "$VALUES_FILE" v24.9
  printf '%s\n' 24.9.2.42 24.9.3.128 24.10.1.1 > "$SERVER"
  cp "$SERVER" "$KEEPER"
  CH_SUPPORTED_MAJORS="24.10 24.9" bash "$GEN"
  grep -q '^"v24.10": "24.10.1.1"$' "$VERSIONS_FILE" || { echo "24.10 dropped from intersection" >&2; exit 1; }
  grep -q '^"v24.9":' "$VERSIONS_FILE" || { echo "24.9 dropped from intersection" >&2; exit 1; }
}

@test "the default is frozen to its committed tag, not bumped to the latest patch" {
  init_case
  seed_values "$VALUES_FILE" v24.9
  printf '%s\n' '"v24.9": "24.9.2.42"' > "$VERSIONS_FILE"
  printf '%s\n' 24.9.2.42 24.9.3.128 > "$SERVER"
  cp "$SERVER" "$KEEPER"
  CH_SUPPORTED_MAJORS="24.9" bash "$GEN"
  grep -q '^"v24.9": "24.9.2.42"$' "$VERSIONS_FILE" || { echo "default not frozen to 24.9.2.42" >&2; exit 1; }
  if grep -q '24.9.3.128' "$VERSIONS_FILE"; then echo "default bumped to newer patch" >&2; exit 1; fi
}

@test "the freeze follows the default major, not a hardcoded one" {
  init_case
  seed_values "$VALUES_FILE" v25.3
  printf '%s\n' '"v25.3": "25.3.14.14"' '"v24.9": "24.9.2.42"' > "$VERSIONS_FILE"
  printf '%s\n' 24.9.2.42 24.9.3.128 25.3.14.14 25.3.99.9 25.8.28.1 > "$SERVER"
  cp "$SERVER" "$KEEPER"
  CH_SUPPORTED_MAJORS="25.8 25.3 24.9" bash "$GEN"
  # v25.3 is the default -> frozen to its committed 25.3.14.14 despite 25.3.99.9
  grep -q '^"v25.3": "25.3.14.14"$' "$VERSIONS_FILE" || { echo "default v25.3 not frozen" >&2; exit 1; }
  # v24.9 is no longer the default -> free to move to its latest common patch
  grep -q '^"v24.9": "24.9.3.128"$' "$VERSIONS_FILE" || { echo "non-default v24.9 not on latest" >&2; exit 1; }
}

@test "dropping the current default's major from the supported set is a hard error" {
  init_case
  seed_values "$VALUES_FILE" v24.9
  printf '%s\n' 25.3.14.14 25.8.28.1 > "$SERVER"
  cp "$SERVER" "$KEEPER"
  if CH_SUPPORTED_MAJORS="25.8 25.3" bash "$GEN" >/dev/null 2>&1; then echo "expected hard error on dropped default" >&2; exit 1; fi
}

@test "non-default majors resolve to their latest common patch" {
  init_case
  seed_values "$VALUES_FILE" v25.8
  printf '%s\n' 25.3.14.14 25.3.15.1 25.8.28.1 > "$SERVER"
  cp "$SERVER" "$KEEPER"
  CH_SUPPORTED_MAJORS="25.8 25.3" bash "$GEN"
  grep -q '^"v25.3": "25.3.15.1"$' "$VERSIONS_FILE" || { echo "25.3 not latest patch" >&2; exit 1; }
  grep -q '^"v25.8": "25.8.28.1"$' "$VERSIONS_FILE" || { echo "25.8 missing" >&2; exit 1; }
}

@test "a tag present in only one image is not selected" {
  init_case
  seed_values "$VALUES_FILE" v25.8
  printf '%s\n' 25.8.28.1 25.8.29.9 > "$SERVER"
  printf '%s\n' 25.8.28.1 > "$KEEPER"
  CH_SUPPORTED_MAJORS="25.8" bash "$GEN"
  grep -q '^"v25.8": "25.8.28.1"$' "$VERSIONS_FILE" || { echo "server-only tag was selected" >&2; exit 1; }
}

@test "an unresolvable configured major is a hard error" {
  init_case
  seed_values "$VALUES_FILE" v24.9
  printf '%s\n' 24.9.2.42 > "$SERVER"
  cp "$SERVER" "$KEEPER"
  if CH_SUPPORTED_MAJORS="26.99 24.9" bash "$GEN" >/dev/null 2>&1; then echo "expected non-zero exit" >&2; exit 1; fi
}

@test "a frozen default tag missing from the registry is a hard error" {
  init_case
  seed_values "$VALUES_FILE" v24.9
  printf '%s\n' '"v24.9": "24.9.2.42"' > "$VERSIONS_FILE"
  printf '%s\n' 24.9.3.128 > "$SERVER"   # the pinned 24.9.2.42 is absent
  cp "$SERVER" "$KEEPER"
  cp "$VERSIONS_FILE" "$WORK/versions.before"
  cp "$VALUES_FILE" "$WORK/values.before"
  # Must NOT silently fall back to 24.9.3.128 and move the default image.
  if CH_SUPPORTED_MAJORS="24.9" bash "$GEN" >/dev/null 2>&1; then echo "expected hard error on missing frozen tag" >&2; exit 1; fi
  cmp -s "$VERSIONS_FILE" "$WORK/versions.before" || { echo "versions.yaml changed on failure" >&2; exit 1; }
  cmp -s "$VALUES_FILE" "$WORK/values.before" || { echo "values.yaml changed on failure" >&2; exit 1; }
}

@test "no files are written when a configured major fails to resolve" {
  init_case
  seed_values "$VALUES_FILE" v24.9
  # The default (v24.9) is valid and 25.8 resolves, so the run passes the
  # default check and gets deep into the resolution loop before 26.99 fails —
  # exercising that nothing is written after a partial resolution, not just on
  # an early exit.
  printf '%s\n' 24.9.2.42 25.8.28.1 > "$SERVER"
  cp "$SERVER" "$KEEPER"
  printf '%s\n' '"v24.9": "24.9.2.42"' > "$VERSIONS_FILE"
  cp "$VERSIONS_FILE" "$WORK/versions.before"
  cp "$VALUES_FILE" "$WORK/values.before"
  if CH_SUPPORTED_MAJORS="25.8 24.9 26.99" bash "$GEN" >/dev/null 2>&1; then echo "expected non-zero exit" >&2; exit 1; fi
  cmp -s "$VERSIONS_FILE" "$WORK/versions.before" || { echo "versions.yaml changed on failure" >&2; exit 1; }
  cmp -s "$VALUES_FILE" "$WORK/values.before" || { echo "values.yaml changed on failure" >&2; exit 1; }
}

@test "values.yaml enum is rewritten newest-first with the default preserved" {
  init_case
  seed_values "$VALUES_FILE" v24.9
  printf '%s\n' '"v24.9": "24.9.2.42"' > "$VERSIONS_FILE"
  printf '%s\n' 24.9.2.42 25.3.14.14 25.8.28.1 > "$SERVER"
  cp "$SERVER" "$KEEPER"
  CH_SUPPORTED_MAJORS="25.8 25.3 24.9" bash "$GEN"
  first="$(grep -m1 '@value' "$VALUES_FILE")"
  [ "$first" = "## @value v25.8" ] || { echo "enum not newest-first, got: $first" >&2; exit 1; }
  grep -q '^version: v24.9$' "$VALUES_FILE" || { echo "default not preserved as v24.9" >&2; exit 1; }
  grep -q '^## @section Application-specific parameters$' "$VALUES_FILE" || { echo "section boundary lost" >&2; exit 1; }
  grep -q '^logTTL: 15$' "$VALUES_FILE" || { echo "trailing content lost" >&2; exit 1; }
}

@test "a malformed version section does not swallow the rest of the file" {
  init_case
  cat > "$VALUES_FILE" <<'EOF'
## @enum {string} Version
## @value v99.9
## @section Application-specific parameters
SENTINEL_KEEP: yes
EOF
  printf '%s\n' 25.8.28.1 > "$SERVER"
  cp "$SERVER" "$KEEPER"
  CH_SUPPORTED_MAJORS="25.8" bash "$GEN"
  grep -q '^SENTINEL_KEEP: yes$' "$VALUES_FILE" || { echo "content after a malformed section was swallowed" >&2; exit 1; }
  grep -q '^## @section Application-specific parameters$' "$VALUES_FILE" || { echo "section boundary swallowed" >&2; exit 1; }
}
