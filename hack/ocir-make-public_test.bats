#!/usr/bin/env bats
# Tests for hack/ocir-make-public.sh — the OCIR new-repo public-visibility flip.
#
# Guards that only PRIVATE repositories under the cozystack/ display-name prefix
# are flipped public (already-public and non-cozystack repos are left alone),
# that an empty selection is a clean no-op, that a transient list failure is
# non-fatal, and that a missing compartment OCID aborts loudly.
#
# Harness note: the CI path is hack/cozytest.sh, NOT real bats. No `run`,
# `$status`, `$output`, `skip`, or setup()/teardown(); each @test is a shell
# function under `set -eu -x`, so a non-zero exit aborts the test (that is the
# exit-0 assertion). A test that expects a non-zero exit must capture it with
# `|| rc=$?`. Under `set -e`, `! cmd` is exempt from auto-exit, so "must NOT
# have happened" checks are written as `if cmd; then exit 1; fi`. Real jq is
# assumed present (build-dep).
#
# Run with: hack/cozytest.sh hack/ocir-make-public_test.bats

# Write a mock `oci` CLI into $1/bin. It reads three env knobs:
#   OCI_MOCK_LIST_JSON  JSON printed for `... repository list ...`
#   OCI_MOCK_LIST_RC    when non-zero, the list call fails with that code
#   OCI_MOCK_UPDATES    file the `... repository update --repository-id X ...`
#                       call appends X to (one OCID per line) on success
#   OCI_MOCK_UPDATE_FAIL_ID  when set, the update of that repo-id fails (and is
#                       NOT recorded), mimicking a per-repo API error
_make_oci_mock() {
  d="$1"
  mkdir -p "$d/bin"
  cat > "$d/bin/oci" <<'EOF'
#!/bin/sh
mode=""
for a in "$@"; do
  case "$a" in
    list) mode=list ;;
    update) mode=update ;;
  esac
done
if [ "$mode" = list ]; then
  if [ "${OCI_MOCK_LIST_RC:-0}" -ne 0 ]; then
    echo "mock oci: list failed" >&2
    exit "$OCI_MOCK_LIST_RC"
  fi
  printf '%s' "${OCI_MOCK_LIST_JSON:-}"
  exit 0
fi
if [ "$mode" = update ]; then
  id="" prev=""
  for a in "$@"; do
    [ "$prev" = "--repository-id" ] && id="$a"
    prev="$a"
  done
  if [ -n "${OCI_MOCK_UPDATE_FAIL_ID:-}" ] && [ "$id" = "$OCI_MOCK_UPDATE_FAIL_ID" ]; then
    echo "mock oci: update failed for $id" >&2
    exit 1
  fi
  echo "$id" >> "$OCI_MOCK_UPDATES"
  exit 0
fi
exit 0
EOF
  chmod +x "$d/bin/oci"
}

# A mixed repository listing: one new private cozystack repo (must be flipped),
# one already-public cozystack repo (must be left), one private non-cozystack
# repo (must be left).
_list_json_mixed() {
  cat <<'EOF'
{"data":{"items":[
  {"id":"ocid1.repo.priv-new","display-name":"cozystack/securitygroup-controller","is-public":false},
  {"id":"ocid1.repo.pub","display-name":"cozystack/cozystack-api","is-public":true},
  {"id":"ocid1.repo.other","display-name":"other/thing","is-public":false}
]}}
EOF
}

@test "flips only private cozystack/* repositories public" {
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  _make_oci_mock "$tmp"
  export PATH="$tmp/bin:$PATH"
  export OCIR_COMPARTMENT_OCID="ocid1.compartment.test"
  export OCI_MOCK_UPDATES="$tmp/updates.txt"
  : > "$OCI_MOCK_UPDATES"
  export OCI_MOCK_LIST_JSON="$(_list_json_mixed)"

  hack/ocir-make-public.sh > "$tmp/out" 2>&1

  # The new private cozystack repo WAS flipped.
  grep -qx 'ocid1.repo.priv-new' "$OCI_MOCK_UPDATES"
  # The already-public cozystack repo was NOT touched.
  if grep -qx 'ocid1.repo.pub' "$OCI_MOCK_UPDATES"; then
    echo "BUG: already-public repo was updated" >&2; exit 1
  fi
  # The non-cozystack repo was NOT touched.
  if grep -qx 'ocid1.repo.other' "$OCI_MOCK_UPDATES"; then
    echo "BUG: non-cozystack repo was updated" >&2; exit 1
  fi
  # Exactly one repo flipped.
  [ "$(wc -l < "$OCI_MOCK_UPDATES")" -eq 1 ]
}

@test "one repo's update failure is non-fatal; the rest still flip" {
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  _make_oci_mock "$tmp"
  export PATH="$tmp/bin:$PATH"
  export OCIR_COMPARTMENT_OCID="ocid1.compartment.test"
  export OCI_MOCK_UPDATES="$tmp/updates.txt"
  : > "$OCI_MOCK_UPDATES"
  export OCI_MOCK_LIST_JSON='{"data":{"items":[{"id":"ocid1.repo.priv-a","display-name":"cozystack/a","is-public":false},{"id":"ocid1.repo.priv-b","display-name":"cozystack/b","is-public":false}]}}'
  export OCI_MOCK_UPDATE_FAIL_ID="ocid1.repo.priv-a"

  rc=0
  hack/ocir-make-public.sh > "$tmp/out" 2>&1 || rc=$?

  # The step does not fail the build on a per-repo error.
  [ "$rc" -eq 0 ]
  # The healthy repo was still flipped.
  grep -qx 'ocid1.repo.priv-b' "$OCI_MOCK_UPDATES"
  # The failing repo was NOT recorded (its visibility was not changed).
  if grep -qx 'ocid1.repo.priv-a' "$OCI_MOCK_UPDATES"; then
    echo "BUG: failed repo recorded as updated" >&2; exit 1
  fi
  # A warning names the repo that could not be flipped.
  grep -q '::warning::Failed to set ocid1.repo.priv-a public' "$tmp/out"
}

@test "OCIR_REPO_PREFIX overrides the selected namespace" {
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  _make_oci_mock "$tmp"
  export PATH="$tmp/bin:$PATH"
  export OCIR_COMPARTMENT_OCID="ocid1.compartment.test"
  export OCIR_REPO_PREFIX="custom/"
  export OCI_MOCK_UPDATES="$tmp/updates.txt"
  : > "$OCI_MOCK_UPDATES"
  export OCI_MOCK_LIST_JSON='{"data":{"items":[{"id":"ocid1.repo.custom","display-name":"custom/thing","is-public":false},{"id":"ocid1.repo.cozy","display-name":"cozystack/thing","is-public":false}]}}'

  hack/ocir-make-public.sh > "$tmp/out" 2>&1

  # Only the custom/ repo is selected under the overridden prefix.
  grep -qx 'ocid1.repo.custom' "$OCI_MOCK_UPDATES"
  if grep -qx 'ocid1.repo.cozy' "$OCI_MOCK_UPDATES"; then
    echo "BUG: cozystack/ repo selected despite prefix override" >&2; exit 1
  fi
  [ "$(wc -l < "$OCI_MOCK_UPDATES")" -eq 1 ]
}

@test "an item without a display-name is skipped, not fatal" {
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  _make_oci_mock "$tmp"
  export PATH="$tmp/bin:$PATH"
  export OCIR_COMPARTMENT_OCID="ocid1.compartment.test"
  export OCI_MOCK_UPDATES="$tmp/updates.txt"
  : > "$OCI_MOCK_UPDATES"
  export OCI_MOCK_LIST_JSON='{"data":{"items":[{"id":"ocid1.repo.nodn","is-public":false},{"id":"ocid1.repo.ok","display-name":"cozystack/ok","is-public":false}]}}'

  # Without the `// ""` jq guard the null display-name aborts the step non-zero.
  hack/ocir-make-public.sh > "$tmp/out" 2>&1

  grep -qx 'ocid1.repo.ok' "$OCI_MOCK_UPDATES"
  if grep -qx 'ocid1.repo.nodn' "$OCI_MOCK_UPDATES"; then
    echo "BUG: display-name-less item selected" >&2; exit 1
  fi
  [ "$(wc -l < "$OCI_MOCK_UPDATES")" -eq 1 ]
}

@test "missing jq is a clean no-op (warns, exit 0)" {
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  mkdir -p "$tmp/empty"
  export OCIR_COMPARTMENT_OCID="ocid1.compartment.test"

  # Scope the stripped PATH to the script only, so the test's own grep still
  # works. The jq guard is reached before any external tool is needed.
  rc=0
  PATH="$tmp/empty" hack/ocir-make-public.sh > "$tmp/out" 2>&1 || rc=$?

  [ "$rc" -eq 0 ]
  grep -q '::warning::jq not found' "$tmp/out"
}

@test "no private cozystack/* repositories -> no updates, exit 0" {
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  _make_oci_mock "$tmp"
  export PATH="$tmp/bin:$PATH"
  export OCIR_COMPARTMENT_OCID="ocid1.compartment.test"
  export OCI_MOCK_UPDATES="$tmp/updates.txt"
  : > "$OCI_MOCK_UPDATES"
  export OCI_MOCK_LIST_JSON='{"data":{"items":[{"id":"ocid1.repo.pub","display-name":"cozystack/cozystack-api","is-public":true}]}}'

  hack/ocir-make-public.sh > "$tmp/out" 2>&1

  [ ! -s "$OCI_MOCK_UPDATES" ]
  grep -q 'No private cozystack/\* repositories' "$tmp/out"
}

@test "transient list failure warns and exits 0 without updating" {
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  _make_oci_mock "$tmp"
  export PATH="$tmp/bin:$PATH"
  export OCIR_COMPARTMENT_OCID="ocid1.compartment.test"
  export OCI_MOCK_UPDATES="$tmp/updates.txt"
  : > "$OCI_MOCK_UPDATES"
  export OCI_MOCK_LIST_RC=1

  rc=0
  hack/ocir-make-public.sh > "$tmp/out" 2>&1 || rc=$?

  [ "$rc" -eq 0 ]
  [ ! -s "$OCI_MOCK_UPDATES" ]
  grep -q '::warning::OCIR repository list failed' "$tmp/out"
}

@test "missing OCIR_COMPARTMENT_OCID aborts non-zero" {
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  _make_oci_mock "$tmp"
  export PATH="$tmp/bin:$PATH"
  unset OCIR_COMPARTMENT_OCID 2>/dev/null || true
  export OCI_MOCK_UPDATES="$tmp/updates.txt"
  : > "$OCI_MOCK_UPDATES"

  rc=0
  hack/ocir-make-public.sh > "$tmp/out" 2>&1 || rc=$?

  [ "$rc" -ne 0 ]
  [ ! -s "$OCI_MOCK_UPDATES" ]
}
