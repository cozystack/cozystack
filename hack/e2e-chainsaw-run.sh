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
# shellcheck source=hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh
. "$here/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh"
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

if [ "${COZY_WORKER_UPGRADE_MODE:-off}" = required ]; then
    ordered=(kubernetes-latest)
    found=0
    for suite in "${suites[@]}"; do
        if [ "$suite" = kubernetes-latest ]; then
            found=$((found + 1))
        else
            ordered+=("$suite")
        fi
    done
    [ "$found" -eq 1 ] || { echo "required worker upgrade suite must be selected exactly once" >&2; exit 1; }
    suites=("${ordered[@]}")
fi

lg_allowlist_validate "$allowlist" "$PWD" || exit 2

reports=$(mktemp -d)
rc=0
stopped=
for suite in "${suites[@]}"; do
    key=${suite//\//_}
    : >"$reports/$key.reached"
    bound=()
    if [ "${COZY_WORKER_UPGRADE_MODE:-off}" = required ]; then
        deadline=${COZY_WORKER_UPGRADE_DEADLINE:-0}
        case "$deadline" in ''|*[!0-9]*) deadline=0 ;; esac
        # The step has no timeout of its own, so a suite still running at the
        # deadline would eat the reporting reserve; the leak check after it
        # needs its budget too.
        left=$((deadline - $(date +%s) - LG_BUDGET))
        # The suite's own headroom checks must measure against the bound, which
        # ends before the job deadline by the leak check's budget.
        bound=(cozy_worker_upgrade_timeout "${left}s" env "COZY_WORKER_UPGRADE_DEADLINE=$((deadline - LG_BUDGET))")
        if [ "$left" -lt 1 ]; then
            lg_synthetic_suite "$suite" budget "required upgrade job budget exhausted; suite not run" >"$reports/$key.xml"
            rc=1
            stopped="required upgrade job budget exhausted"
            break
        fi
    fi
    echo "=== suite $suite"
    preflight_rc=0
    if [ "${#bound[@]}" -gt 0 ]; then
        ${bound[@]+"${bound[@]}"} bash -c '. "$1"; lg_apiserver_now > "$2" && lg_baseline > "$3"' _ \
            "$here/e2e-chainsaw/_lib/leak-guard.sh" "$reports/$key.start" "$reports/$key.baseline" || preflight_rc=$?
        if [ "$preflight_rc" -eq 0 ]; then
            start=$(cat "$reports/$key.start") && baseline=$(cat "$reports/$key.baseline") || preflight_rc=1
        fi
    elif ! start=$(lg_apiserver_now) || ! baseline=$(lg_baseline); then
        preflight_rc=1
    fi
    if [ "$preflight_rc" -eq 124 ]; then
        lg_synthetic_suite "$suite" budget "required upgrade job budget exhausted during the pre-suite reading; suite not run" >"$reports/$key.xml"
        rc=1
        stopped="required upgrade job budget exhausted during the pre-suite reading"
        break
    fi
    if [ "$preflight_rc" -ne 0 ]; then
        echo "::error title=leak-guard::suite=$suite could not take the pre-suite reading; suite not run"
        lg_synthetic_suite "$suite" "$suite" "the leak guard could not take the pre-suite reading; chainsaw did not run" >"$reports/$key.xml"
        rc=1
        if [ "${COZY_WORKER_UPGRADE_MODE:-off}" = required ] && [ "$suite" = kubernetes-latest ]; then
            stopped="the leak guard could not take the reading before the required upgrade suite"
            break
        fi
        continue
    fi
    if [ "${#bound[@]}" -gt 0 ]; then
        left=$((deadline - $(date +%s) - LG_BUDGET))
        if [ "$left" -lt 1 ]; then
            lg_synthetic_suite "$suite" budget "required upgrade job budget exhausted; suite not run" >"$reports/$key.xml"
            rc=1
            stopped="required upgrade job budget exhausted"
            break
        fi
        bound=(cozy_worker_upgrade_timeout "${left}s" env "COZY_WORKER_UPGRADE_DEADLINE=$((deadline - LG_BUDGET))")
    fi
    # The extension is spelled out: chainsaw appends .xml only to a name that
    # has none (getFile in pkg/report/report.go, v0.2.15), so a suite directory
    # with a dot in its name would otherwise write a file the merge never finds.
    ${bound[@]+"${bound[@]}"} chainsaw test ${flags[@]+"${flags[@]}"} --report-path "$reports" --report-name "$key.xml" "$suite"
    crc=$?
    if [ "${#bound[@]}" -gt 0 ] && [ "$crc" -eq 124 ]; then
        lg_synthetic_suite "$suite" budget "required upgrade job budget ran out while this suite was running" >"$reports/$key.xml"
        rc=1
        stopped="required upgrade job budget ran out while $suite was running"
        break
    fi
    [ "$crc" -eq 0 ] || rc=1
    LG_BASELINE_ROOT_UIDS='[]'
    recorded=1
    if [ "${COZY_WORKER_UPGRADE_MODE:-off}" = required ] && [ "$suite" = kubernetes-latest ]; then
        roots="$here/../_out/cozyreport/snapshots/worker-upgrade/platform-roots.json"
        before="$here/../_out/worker-upgrade-platform-before.json"
        # Without the snapshot taken before the upgrade, the suite failed
        # earlier and the upgrade created nothing, so the ordinary check
        # applies. A failed upgrade may stop before it records the roots; that
        # failure is already reported and needs no second suite.
        if [ -f "$before" ]; then
            recorded=0
            if cozy_worker_upgrade_roots_valid "$before" < "$roots" 2>/dev/null && LG_BASELINE_ROOT_UIDS=$(jq --compact-output 'map(.uid) | unique' "$roots" 2>/dev/null); then
                recorded=1
            else
                LG_BASELINE_ROOT_UIDS='[]'
            fi
        fi
        if [ "$crc" -eq 0 ] && [ "$recorded" -eq 0 ]; then
            lg_synthetic_suite "$suite" platform-baseline "the upgrade did not record its platform roots" >"$reports/worker-upgrade-platform.xml"
            suites+=(worker-upgrade-platform)
            crc=1
            rc=1
        fi
    fi
    if [ "$recorded" -eq 0 ]; then
        # Without the roots every workload the upgrade created would read as a
        # leak, so the suite is not leak-checked; unchecked fails the run too.
        printf '%s\n' "the required upgrade failed before it recorded its platform roots, so the leak guard cannot tell its workloads from leaks" >"$reports/$key.unchecked"
        rc=1
    else
        lrc=0
        if [ "${COZY_WORKER_UPGRADE_MODE:-off}" = required ]; then
            # LG_BUDGET controls cleanup waiting, including a valid zero-wait
            # policy; the job deadline bounds the complete read/check phase.
            left=$((deadline - $(date +%s)))
            if [ "$left" -lt 1 ]; then
                lrc=124
            else
                cozy_worker_upgrade_timeout "${left}s" env "COZY_LEAK_BUDGET=$LG_BUDGET" "LG_BASELINE_ROOT_UIDS=$LG_BASELINE_ROOT_UIDS" \
                    bash -c '. "$1"; shift; lg_check_suite "$@"' _ "$here/e2e-chainsaw/_lib/leak-guard.sh" \
                    "$suite" "$start" "$baseline" "$crc" "$allowlist" || lrc=$?
            fi
            if [ "$lrc" -eq 124 ]; then
                printf '%s\n' 'required upgrade leak check budget exhausted; result unchecked' >"$reports/$key.unchecked"
                rc=1
                stopped="required upgrade leak check budget exhausted"
                break
            fi
        else
            lg_check_suite "$suite" "$start" "$baseline" "$crc" "$allowlist"
            lrc=$?
        fi
        case $lrc in
            0) ;;
            2) : >"$reports/$key.unchecked"; rc=1 ;;
            *) : >"$reports/$key.leaked"; rc=1 ;;
        esac
    fi
    if [ "${COZY_WORKER_UPGRADE_MODE:-off}" = required ] && [ "$suite" = kubernetes-latest ]; then
        if [ "$rc" -ne 0 ]; then
            echo "required worker upgrade failed; candidate checks were not run" >&2
            stopped="the required worker upgrade failed"
            break
        fi
        left=$((deadline - $(date +%s) - LG_BUDGET))
        orc=0
        if [ "$left" -lt 1 ]; then
            orc=124
        else
            (cd "$here/.." && cozy_worker_upgrade_timeout "${left}s" bash hack/cozytest.sh hack/e2e-test-openapi.bats) || orc=$?
        fi
        if [ "$orc" -ne 0 ]; then
            case $orc in
                124) why="required upgrade job budget ran out during the candidate OpenAPI checks" ;;
                *) why="candidate OpenAPI checks failed after the upgrade" ;;
            esac
            lg_synthetic_suite openapi openapi "$why" >"$reports/openapi.xml"
            suites+=(openapi)
            rc=1
            stopped=$why
            break
        fi
    fi
done

# A suite the loop never reached has no report of its own, which the merge
# would otherwise read as chainsaw having run it and written nothing.
if [ -n "$stopped" ]; then
    for suite in "${suites[@]}"; do
        key=${suite//\//_}
        [ -f "$reports/$key.xml" ] || [ -f "$reports/$key.reached" ] || lg_synthetic_suite "$suite" "$suite" "suite not run: $stopped" >"$reports/$key.xml"
    done
fi
lg_merge_reports "$reports" "${suites[@]}" >chainsaw-report.xml
exit "$rc"
