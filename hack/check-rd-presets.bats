#!/usr/bin/env bats
# Guards the membership check in hack/check-rd-presets.sh against reporting a
# preset that is present as missing.
#
# That is what it used to do. The check was
# `printf '%s\n' "$enums" | grep -Fqx -- "$want"` in a script running under
# `set -o pipefail`: grep -q exits on its first match and can close the pipe
# while printf is still writing, printf fails with EPIPE, pipefail makes the
# pipeline non-zero, and the `!` reads that as "not found". See #4193 for the
# CI log. These tests do not try to win that race, since a test that only
# reddens when the scheduler cooperates is the flaky kind this repo does not
# add. They pin the two outcomes the check owes: a complete enum passes, an
# incomplete one fails and says which value is gone.
#
# hack/cozytest.sh knows @test and plain bash: no setup/teardown, no `run`.
# The fixture is written with jq alone so the test does not depend on which
# yq dialect is installed.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
SCRIPT="$REPO_ROOT/hack/check-rd-presets.sh"

# Writes a probe ApplicationDefinition under $TMP. $1 is a canonical preset to
# leave out of the enum, or "" to keep all 47.
write_probe_rd() {
  mkdir -p "$TMP/packages/system/probe-rd/cozyrds"
  jq -rn --arg drop "$1" '
    def canonical:
      ["t1","c1","s1","u1","m1"] as $fam
      | ["nano","micro","small","medium","large","xlarge","2xlarge","4xlarge"] as $sz
      | [$fam[] as $f | $sz[] | "\($f).\(.)"]
        + ["nano","micro","small","medium","large","xlarge","2xlarge"];
    {properties:{resources:{properties:{resourcesPreset:{
      enum: (canonical | map(select($drop == "" or . != $drop)))
    }}}}} | tojson
  ' > "$TMP/schema.json"
  # A JSON document carries no single quotes, so a single-quoted YAML scalar
  # needs no escaping, and writing the fixture without yq keeps the test off
  # the mikefarah/python dialect split.
  {
    printf 'spec:\n  application:\n    openAPISchema: '"'"
    cat "$TMP/schema.json"
    printf "'\n"
  } > "$TMP/packages/system/probe-rd/cozyrds/probe.yaml"
}

@test "an enum carrying every canonical preset passes" {
  command -v jq >/dev/null || skip "jq not installed"
  command -v yq >/dev/null || skip "yq not installed"
  TMP=$(mktemp -d)
  write_probe_rd ""
  if ! out=$(cd "$TMP" && bash "$SCRIPT" 2>&1); then
    echo "expected the check to pass, it failed:" >&2
    echo "$out" >&2
    exit 1
  fi
  case "$out" in
    *"All RD schemas carry the full 47-preset enum."*) ;;
    *) echo "unexpected output: $out" >&2; exit 1 ;;
  esac
  rm -rf "$TMP"
}

@test "a missing preset fails the check and is named in the output" {
  command -v jq >/dev/null || skip "jq not installed"
  command -v yq >/dev/null || skip "yq not installed"
  TMP=$(mktemp -d)
  write_probe_rd c1.small
  if out=$(cd "$TMP" && bash "$SCRIPT" 2>&1); then
    echo "expected the check to fail on a missing preset, it passed:" >&2
    echo "$out" >&2
    exit 1
  fi
  case "$out" in
    *c1.small*) ;;
    *) echo "expected c1.small in output, got: $out" >&2; exit 1 ;;
  esac
  rm -rf "$TMP"
}
