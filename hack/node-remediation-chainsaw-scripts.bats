#!/usr/bin/env bats
# Every `script` op of the node-remediation Chainsaw suite, checked by
# shellcheck under the shell Chainsaw runs it with: `sh` unless the op sets
# `shell:`. The sandbox's /bin/sh is dash, so a bash-only construct in an op
# without `shell: bash` ($SECONDS, arrays, pipefail) fails there on every run
# while passing every local `bash -n`. This is the check that reads the op as
# dash does. Warnings are the floor: the "not POSIX" family (SC3xxx) is
# reported at that level.
#
# Harness compatibility note: CI runs this file under Bats through
# `make bats-unit-tests`. It avoids `run`, `$status`, `$output`, `skip` and
# setup()/teardown() so the narrower legacy `hack/cozytest.sh` translator can
# run it too. Paths stay repo-root-relative for both runners.
#
# Run with: bats hack/node-remediation-chainsaw-scripts.bats

load test_helper

SUITE=hack/e2e-chainsaw/node-remediation/chainsaw-test.yaml

@test "every script op of the node-remediation suite is clean under the shell Chainsaw runs it with" {
  if ! command -v shellcheck > /dev/null 2>&1; then
    echo "shellcheck is required for this check; a silent skip would pass a dash-unsafe op" >&2
    return 1
  fi
  tmp=$(mktemp -d)
  # One line per op: step name, shell (sh when the op sets none), content as base64.
  yq -r '.spec.steps[] | .name as $step | (.try[], .catch[]?, .cleanup[]?) | select(.script != null) | $step + "\t" + (.script.shell // "sh") + "\t" + (.script.content | @base64)' "$SUITE" > "$tmp/ops"
  n=$(wc -l < "$tmp/ops" | tr -d ' ')
  if [ "$n" -lt 3 ]; then
    echo "found $n script ops in $SUITE; the suite has more, so the extraction read nothing" >&2
    rm -rf "$tmp"
    return 1
  fi
  : > "$tmp/findings"
  i=0
  while IFS="$(printf '\t')" read -r step shell content; do
    i=$((i + 1))
    printf '%s' "$content" | base64 -d > "$tmp/op$i.sh"
    if ! shellcheck --shell="$shell" --severity=warning --format=gcc "$tmp/op$i.sh" > "$tmp/out" 2>&1; then
      {
        echo "step '$step', script op $i, shell $shell:"
        sed "s|$tmp/op$i.sh|  line|" "$tmp/out"
      } >> "$tmp/findings"
    fi
  done < "$tmp/ops"
  if [ -s "$tmp/findings" ]; then
    echo "script ops of $SUITE that shellcheck rejects under the shell they run with ($n ops checked):" >&2
    cat "$tmp/findings" >&2
    rm -rf "$tmp"
    return 1
  fi
  echo "$n script ops checked, none rejected"
  rm -rf "$tmp"
}
