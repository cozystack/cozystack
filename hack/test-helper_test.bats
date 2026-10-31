#!/usr/bin/env bats

load test_helper

# A separate shell keeps errexit active inside the assertion, unlike calling
# the function directly through Bats' run helper.
run_lacks_pattern() {
  run bash -eu -c '
    . hack/test_helper.bash
    assert_file_lacks_pattern "$1" "$2"
    printf "checked\n"
  ' _ "$@"
}

@test "a valid non-matching pattern passes under errexit" {
  printf 'safe content\n' >"$BATS_TEST_TMPDIR/artifact"

  run_lacks_pattern 'secret:|secretName:' "$BATS_TEST_TMPDIR/artifact"

  [ "$status" -eq 0 ]
  [ "$output" = checked ]
}

@test "an empty readable file passes for a valid pattern" {
  : >"$BATS_TEST_TMPDIR/artifact"

  run_lacks_pattern 'secret:' "$BATS_TEST_TMPDIR/artifact"

  [ "$status" -eq 0 ]
  [ "$output" = checked ]
}

@test "a matching pattern fails the assertion" {
  printf 'safe content\nsecretName: credential\n' >"$BATS_TEST_TMPDIR/artifact"

  run_lacks_pattern 'secret:|secretName:' "$BATS_TEST_TMPDIR/artifact"

  [ "$status" -eq 1 ]
  [[ "$output" == *'not to match: secret:|secretName:'* ]]
}

@test "an invalid pattern fails on a populated file" {
  printf 'safe content\n' >"$BATS_TEST_TMPDIR/artifact"

  run_lacks_pattern '[' "$BATS_TEST_TMPDIR/artifact"

  [ "$status" -eq 1 ]
  [[ "$output" == *'could not evaluate pattern ['* ]]
}

@test "an invalid pattern fails even on an empty file" {
  : >"$BATS_TEST_TMPDIR/artifact"

  run_lacks_pattern '[' "$BATS_TEST_TMPDIR/artifact"

  [ "$status" -eq 1 ]
  [[ "$output" == *'could not evaluate pattern ['* ]]
}

@test "a missing artifact fails rather than passing as no match" {
  run_lacks_pattern 'secret:' "$BATS_TEST_TMPDIR/missing"

  [ "$status" -eq 1 ]
  [[ "$output" == *'to exist so it could be checked'* ]]
}

@test "a directory is not an artifact file" {
  run_lacks_pattern 'secret:' "$BATS_TEST_TMPDIR"

  [ "$status" -eq 1 ]
  [[ "$output" == *'to exist so it could be checked'* ]]
}

@test "an unreadable artifact fails rather than passing as no match" {
  printf 'safe content\n' >"$BATS_TEST_TMPDIR/artifact"
  chmod 000 "$BATS_TEST_TMPDIR/artifact"
  if [ -r "$BATS_TEST_TMPDIR/artifact" ]; then
    skip 'this user can read files without read permission'
  fi

  run_lacks_pattern 'secret:' "$BATS_TEST_TMPDIR/artifact"

  [ "$status" -eq 1 ]
  [[ "$output" == *'could not evaluate pattern secret:'* ]]
}
