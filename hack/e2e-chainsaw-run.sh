#!/usr/bin/env bash
# Runs the Chainsaw app suite one suite directory at a time and, after each,
# fails the suite whose pods outlived its own cleanup (hack/e2e-chainsaw/_lib/
# leak-guard.sh says how a pod is charged to a suite, and why).
#
# Usage, from hack/e2e-chainsaw/ (where .chainsaw.yaml is):
#   ../e2e-chainsaw-run.sh [--set-string KEY=VALUE]... [SUITE...]
#
# One invocation per suite rather than one for the lot because Chainsaw v0.2.15
# has no hook that runs after a test's cleanup: its Configuration carries a
# global `error.catch`, which runs only on failure, and nothing else per test.
# Between two invocations is the one point where every test in a suite has
# finished its teardown and nothing from the next suite exists yet.
#
# The suites run are the ones a single `chainsaw test [SUITE...]` would run:
# the named directories when there are any; otherwise every directory directly
# under this one, each handed to chainsaw, whose own discovery decides what in
# it is a test (a directory holding none, like _lib, runs nothing). That split
# is exact only while no test sits at the top level itself, so one there stops
# the run instead of being skipped.
#
# The per-suite JUnit reports are merged into chainsaw-report.xml, the file and
# shape the workflows already collect. The run goes on past a red suite, as a
# single invocation did, and exits non-zero when any suite failed or leaked.
set -u

here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=hack/e2e-chainsaw/_lib/leak-guard.sh
. "$here/e2e-chainsaw/_lib/leak-guard.sh"
allowlist=${COZY_LEAK_ALLOWLIST:-$here/e2e-chainsaw/_lib/leak-allowlist.txt}

flags=()
suites=()
while [ $# -gt 0 ]; do
    case $1 in
        --set-string)
            [ $# -ge 2 ] || { echo "e2e-chainsaw-run: --set-string needs a value" >&2; exit 2; }
            flags+=("$1" "$2"); shift 2 ;;
        --set-string=*) flags+=("$1"); shift ;;
        -*) echo "e2e-chainsaw-run: unsupported option $1" >&2; exit 2 ;;
        *) suites+=("${1%/}"); shift ;;
    esac
done

if [ "${#suites[@]}" -eq 0 ]; then
    if compgen -G 'chainsaw-test.y*ml' >/dev/null || compgen -G '[0-9]*-*.y*ml' >/dev/null; then
        echo "e2e-chainsaw-run: a test at the top level of $PWD would not belong to any suite" >&2
        exit 2
    fi
    while IFS= read -r d; do
        suites+=("${d#./}")
    done < <(find . -mindepth 1 -maxdepth 1 -type d | LC_ALL=C sort)
fi

lg_allowlist_validate "$allowlist" "$PWD" || exit 2

reports=$(mktemp -d)
rc=0
for suite in "${suites[@]}"; do
    key=${suite//\//_}
    echo "=== suite $suite"
    if ! start=$(lg_apiserver_now) || ! baseline=$(lg_baseline); then
        echo "::error title=leak-guard::suite=$suite could not take the pre-suite reading; suite not run"
        lg_synthetic_suite "$suite" "$suite" "the leak guard could not take the pre-suite reading; chainsaw did not run" >"$reports/$key.xml"
        rc=1
        continue
    fi
    # The extension is spelled out: chainsaw appends .xml only to a name that
    # has none (getFile in pkg/report/report.go, v0.2.15), so a suite directory
    # with a dot in its name would otherwise write a file the merge never finds.
    chainsaw test ${flags[@]+"${flags[@]}"} --report-path "$reports" --report-name "$key.xml" "$suite"
    crc=$?
    [ "$crc" -eq 0 ] || rc=1
    lg_check_suite "$suite" "$start" "$baseline" "$crc" "$allowlist"
    case $? in
        0) ;;
        2) : >"$reports/$key.unchecked"; rc=1 ;;
        *) : >"$reports/$key.leaked"; rc=1 ;;
    esac
done

lg_merge_reports "$reports" "${suites[@]}" >chainsaw-report.xml
exit "$rc"
