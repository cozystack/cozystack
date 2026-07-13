#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for hack/upgrade-prev-version.sh
#
# Run under cozytest.sh (hack/cozytest.sh hack/upgrade-prev-version_test.bats),
# NOT real bats: there is no `run`/`$status`/setup(); each @test is a shell
# function under `set -eu -x`, so assertions are direct shell tests that exit
# non-zero on failure. Each test writes its own synthetic tag list to a scratch
# file and passes it as the resolver's TAGS_FILE arg, so no real git history is
# touched and results are deterministic. Auto-discovered by the Makefile's
# bats-unit-tests target (it is NOT an hack/e2e-*.bats, so it is included).
# -----------------------------------------------------------------------------

# Fixture: a representative multi-line tag set covering several minor lines,
# patches, and pre-releases.
_fixture() {
  cat <<'EOF'
v1.4.0
v1.4.5
v1.4.6
v1.5.0-rc.1
v1.5.0
v1.5.1
v1.5.2
v1.5.3
v1.6.0-rc.1
v1.6.0-rc.2
EOF
}

@test "rc target resolves to previous minor's latest stable" {
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  _fixture > "$tmp/tags"
  output=$(hack/upgrade-prev-version.sh v1.6.0-rc.1 "$tmp/tags")
  [ "$output" = "v1.5.3" ]
}

@test "stable .0 target resolves to previous minor's latest stable" {
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  _fixture > "$tmp/tags"
  output=$(hack/upgrade-prev-version.sh v1.6.0 "$tmp/tags")
  [ "$output" = "v1.5.3" ]
}

@test "patch target resolves to the PREVIOUS minor, not same-line patches" {
  # Promoting v1.5.4 must upgrade FROM the 1.4 line's latest stable (previous
  # minor), never from an earlier 1.5 patch.
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  _fixture > "$tmp/tags"
  output=$(hack/upgrade-prev-version.sh v1.5.4 "$tmp/tags")
  [ "$output" = "v1.4.6" ]
}

@test "leading-v optional in target" {
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  _fixture > "$tmp/tags"
  output=$(hack/upgrade-prev-version.sh 1.6.0-rc.2 "$tmp/tags")
  [ "$output" = "v1.5.3" ]
}

@test "pre-release tags are never selected as the baseline" {
  # Only v1.5.x stable tags exist below 1.6; the 1.6 rc tags must be ignored.
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  _fixture > "$tmp/tags"
  output=$(hack/upgrade-prev-version.sh v1.6.0-rc.1 "$tmp/tags")
  case "$output" in *-*) echo "FAIL: selected a pre-release: $output" >&2; false ;; esac
}

@test "skips a minor line that never shipped a stable release" {
  # 1.5 line has only a pre-release; target 1.6 must fall through to 1.4.
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  cat > "$tmp/tags" <<'EOF'
v1.4.6
v1.5.0-rc.1
v1.6.0-rc.1
EOF
  output=$(hack/upgrade-prev-version.sh v1.6.0-rc.1 "$tmp/tags")
  [ "$output" = "v1.4.6" ]
}

@test "line match is dot-anchored (1.5 does not match v155.x)" {
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  cat > "$tmp/tags" <<'EOF'
v1.5.3
v155.0.0
v1.6.0-rc.1
EOF
  output=$(hack/upgrade-prev-version.sh v1.6.0-rc.1 "$tmp/tags")
  [ "$output" = "v1.5.3" ]
}

@test "empty target falls back to the highest stable tag overall" {
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  _fixture > "$tmp/tags"
  output=$(hack/upgrade-prev-version.sh "" "$tmp/tags")
  [ "$output" = "v1.5.3" ]
}

@test "no previous minor (major 0, minor 0) is an error" {
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  cat > "$tmp/tags" <<'EOF'
v0.0.1
EOF
  if out=$(hack/upgrade-prev-version.sh v0.0.5 "$tmp/tags" 2>/dev/null); then
    echo "FAIL: expected non-zero exit, got '$out'" >&2
    false
  fi
}

@test "unparseable target is an error" {
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  _fixture > "$tmp/tags"
  if out=$(hack/upgrade-prev-version.sh "not-a-version" "$tmp/tags" 2>/dev/null); then
    echo "FAIL: expected non-zero exit, got '$out'" >&2
    false
  fi
}
