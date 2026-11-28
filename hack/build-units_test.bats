#!/usr/bin/env bats
# Unit tests for hack/build-units.sh, run against a fake unit list and a fake
# `make` that records when each unit starts and ends and how many build at once.
#
# Test-level EXIT traps replace Bats' own handler and hide failing TAP results.
# Cleanup follows aborting assertions; see docs/agents/e2e-testing.md.

load test_helper

# Copies the script into an empty tree with a five-unit matrix and a fake make.
# Every fake build takes half a second, so builds allowed to overlap do overlap.
fake_tree() {
  work="$(mktemp -d)"
  mkdir -p "$work/hack" "$work/bin"
  cp hack/build-units.sh "$work/hack/"
  echo 0 > "$work/active"
  cat > "$work/hack/build-matrix.sh" <<'EOF'
#!/bin/sh
[ -z "${MATRIX_FAILS:-}" ] || exit 1
echo '["packages/system/seaweedfs","packages/system/objectstorage-controller","packages/apps/a","packages/apps/b","packages/apps/c"]'
EOF
  cat > "$work/bin/make" <<'EOF'
#!/bin/sh
unit="$2"
add() {
  flock "$WORK/lock" sh -c 'n=$(( $(cat "$1/active") + $2 )); echo "$n" > "$1/active"; echo "$n" >> "$1/running"' _ "$WORK" "$1"
}
echo "start $unit" >> "$WORK/order"
echo "working on $unit"
add 1
[ "$unit" != "${HANG_UNIT:-}" ] || sleep 3
if [ "$unit" = packages/system/objectstorage-controller ] && ! grep -qx 'end packages/system/seaweedfs' "$WORK/order"; then
  echo "objectstorage-controller started while seaweedfs was still building"
  exit 1
fi
sleep 0.5
add -1
if [ "$unit" = "${FAIL_UNIT:-}" ]; then
  echo "boom from $unit"
  exit 1
fi
echo "end $unit" >> "$WORK/order"
EOF
  chmod +x "$work/hack/build-matrix.sh" "$work/bin/make"
}

run_units() {
  run bash -c 'cd "$1" && WORK="$1" PATH="$1/bin:$PATH" BUILD_JOBS=2 timeout "${TIMEOUT:-60}" hack/build-units.sh' _ "$work"
  echo "$output"
}

@test "every unit is built once, two at a time, and objectstorage-controller after seaweedfs" {
  fake_tree
  run_units
  [ "$status" -eq 0 ]
  for unit in packages/system/seaweedfs packages/system/objectstorage-controller packages/apps/a packages/apps/b packages/apps/c; do
    [ "$(grep -cx "start $unit" "$work/order")" -eq 1 ]
    [ "$(grep -cx "end $unit" "$work/order")" -eq 1 ]
  done
  [ "$(sort -n "$work/running" | tail -n 1)" -eq 2 ]
  rm -rf "$work"
}

@test "a failing unit fails the build, its output is shown, and objectstorage-controller is not built" {
  fake_tree
  FAIL_UNIT=packages/apps/b run_units
  [ "$status" -ne 0 ]
  echo "$output" | grep -qF 'boom from packages/apps/b'
  if grep -qx 'start packages/system/objectstorage-controller' "$work/order"; then
    echo "FAIL: objectstorage-controller was built after another unit failed"; false
  fi
  rm -rf "$work"
}

@test "a unit list that cannot be generated fails the build before anything is built" {
  fake_tree
  MATRIX_FAILS=1 run_units
  [ "$status" -ne 0 ]
  if [ -e "$work/order" ]; then echo "FAIL: units were built without a unit list"; false; fi
  rm -rf "$work"
}

@test "a failing objectstorage-controller, built after the others, fails the build too" {
  fake_tree
  FAIL_UNIT=packages/system/objectstorage-controller run_units
  [ "$status" -ne 0 ]
  echo "$output" | grep -qF 'packages/system/objectstorage-controller: boom from packages/system/objectstorage-controller'
  rm -rf "$work"
}

@test "a unit's output shows while it builds, so a job that times out keeps it" {
  fake_tree
  HANG_UNIT=packages/apps/a TIMEOUT=1 run_units
  [ "$status" -eq 124 ]
  echo "$output" | grep -qF 'packages/apps/a: working on packages/apps/a'
  rm -rf "$work"
}
