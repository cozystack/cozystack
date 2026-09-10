#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Regression guards for hack/check-rd-presets.sh, covering the two ways it used
# to reach a verdict the files do not support, plus the matching contract the
# fix rests on.
#
# The first is a read it could not complete. `printf '%s' "$schema" | jq ...
# 2>/dev/null || true` discarded jq's status, so a read that failed was skipped
# in silence, or worse, passed on whatever jq had already written before it
# died: the check that exists to parse a schema was free not to parse it.
#
# The second is the one that reddened pull requests. Membership was decided by
# `printf '%s\n' "$enums" | grep -Fqx -- "$want"`, and `grep -q` exits at its
# first match. Once the list is longer than the pipe buffer the writer is still
# writing when the reader leaves, the write fails, and `set -o pipefail` reports
# the pipeline as failed even though grep matched -- a verdict indistinguishable
# from "not found", so a preset that is present is named as missing. Below the
# buffer size the writer usually wins, which is why it stayed hidden while
# `make unit-tests` ran serially and started firing on the 4-CPU runner under
# `make -j4`: 32 of the 48 unit-lane failures over 2026-09-04..09-10, each
# naming a different preset in a different file, each carrying one
# "printf: write error: Broken pipe" per preset it accused.
#
# Cases that need control over which files are walked build a package tree with
# a single ApplicationDefinition in it, because the checker globs
# packages/system/*-rd/cozyrds/*.yaml relative to the working directory. That
# is what keeps the large-payload case to 47 greps rather than 1504.
#
# Compatible with the in-repo cozytest.sh runner, which runs each @test in a
# fresh subshell under `set -u` and provides no bats `run`/`$status`.
# -----------------------------------------------------------------------------

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
CHECK="$REPO_ROOT/hack/check-rd-presets.sh"

# The presets the checker expects, one per line. Read from its own source of
# truth so a fixture cannot disagree with it by accident; the last case below is
# what stops that array from being whatever the schemas happen to carry.
expected_presets() {
    sed -n '/^EXPECTED=(/,/^)/p' "$CHECK" | sed '1d;$d' | tr -s '[:space:]' '\n' | sed '/^$/d'
}

# A package tree holding one ApplicationDefinition, left as the current
# directory. The schema body is a placeholder: every case that uses this drives
# the enum through the jq stub instead.
make_tree() {
    dir=$(mktemp -d)
    cd "$dir"
    mkdir -p packages/system/foo-rd/cozyrds packages/apps packages/extra
    printf 'spec:\n  application:\n    openAPISchema: |-\n      {"type":"object"}\n' \
        > packages/system/foo-rd/cozyrds/foo.yaml
}

# A jq stub in $1/jq that prints the file $2 and exits 0.
install_jq_stub() {
    cat > "$1/jq" <<STUB
#!/bin/sh
cat "$2"
STUB
    chmod +x "$1/jq"
}

# Last line of a test body rather than a trap, which the runners forbid: both
# set -e, so a failing test never reaches this and its tree survives for
# inspection, which is what a failed test wants.
cleanup() {
    cd /
    rm -rf "$dir"
}

@test "a schema that cannot be read is named as unreadable, not as a missing preset" {
    work=$(mktemp -d)
    cat > "$work/jq" <<'STUB'
#!/bin/sh
printf '%s\n' t1.nano t1.micro
echo "jq: error (at <stdin>:0): simulated early exit" >&2
exit 2
STUB
    chmod +x "$work/jq"

    rc=0
    out=$(PATH="$work:$PATH" "$CHECK" 2>&1) || rc=$?
    rm -rf "$work"

    if [ "$rc" -eq 0 ]; then
        echo "the check passed while every schema read was failing" >&2
        printf '%s\n' "$out" >&2
        exit 1
    fi
    case "$out" in
        *"could not be read"*) ;;
        *)
            echo "a failed read was not named as one" >&2
            printf '%s\n' "$out" >&2
            exit 1 ;;
    esac
    case "$out" in
        *"resourcesPreset enum missing"*)
            echo "a failed read was reported as drift, which is the regression this guards" >&2
            printf '%s\n' "$out" >&2
            exit 1 ;;
    esac
    # The reason travels with the verdict, so a reader does not have to reproduce
    # the failure to learn what jq objected to.
    case "$out" in
        *"simulated early exit"*) ;;
        *)
            echo "the message dropped jq's own reason" >&2
            printf '%s\n' "$out" >&2
            exit 1 ;;
    esac
}

@test "a preset list far past any pipe capacity is read whole, not reported missing" {
    # A schema that repeats the set per component is ordinary: seaweedfs-rd
    # carries 329 entries today. Doubled to 4 MiB it is past what a process can
    # even ask for -- fs/pipe-max-size is 1 MiB on the runners -- so the old
    # pipeline cannot finish its write before grep leaves, on any host, rather
    # than losing a race the writer often wins at the sizes seen in CI.
    make_tree
    expected_presets > enums
    while [ "$(wc -c < enums)" -lt 4194304 ]; do
        cat enums enums > enums.next
        mv enums.next enums
    done
    install_jq_stub "$dir" "$dir/enums"

    rc=0
    out=$(PATH="$dir:$PATH" "$CHECK" 2>&1) || rc=$?
    if [ "$rc" -ne 0 ]; then
        echo "a complete preset list was rejected because of its length" >&2
        printf '%s\n' "$out" >&2
        exit 1
    fi
    cleanup
}

@test "a preset genuinely absent from the schema is reported and named" {
    make_tree
    expected_presets | sed '/^c1\.medium$/d' > enums
    install_jq_stub "$dir" "$dir/enums"

    rc=0
    out=$(PATH="$dir:$PATH" "$CHECK" 2>&1) || rc=$?
    if [ "$rc" -eq 0 ]; then
        echo "a schema missing c1.medium passed" >&2
        printf '%s\n' "$out" >&2
        exit 1
    fi
    case "$out" in
        *"resourcesPreset enum missing: c1.medium"*) ;;
        *)
            echo "the verdict did not name c1.medium as the missing preset" >&2
            printf '%s\n' "$out" >&2
            exit 1 ;;
    esac
    cleanup
}

@test "a preset the schema only resembles does not count as carrying it" {
    # Both flags on the membership grep are load-bearing and neither is visible
    # from a fixture that simply omits a value. `t1.nanoX` satisfies a match
    # without -x, and `c1Xsmall` satisfies one without -F because the `.` in the
    # pattern then matches any character. Either way the checker would pass a
    # schema that carries neither preset, so the verdict has to name both.
    make_tree
    expected_presets | sed 's/^t1\.nano$/t1.nanoX/; s/^c1\.small$/c1Xsmall/' > enums
    install_jq_stub "$dir" "$dir/enums"

    rc=0
    out=$(PATH="$dir:$PATH" "$CHECK" 2>&1) || rc=$?
    if [ "$rc" -eq 0 ]; then
        echo "a schema carrying t1.nanoX and c1Xsmall passed as carrying t1.nano and c1.small" >&2
        printf '%s\n' "$out" >&2
        exit 1
    fi
    for want in t1.nano c1.small; do
        case "$out" in
            *"$want"*) ;;
            *)
                echo "$want was not reported missing, so the match is not literal and whole-line" >&2
                printf '%s\n' "$out" >&2
                exit 1 ;;
        esac
    done
    cleanup
}

@test "EXPECTED is the canonical set, not whatever the schemas happen to carry" {
    # Built from the contract the script's own header states -- five instance
    # type families across eight sizes, plus the seven legacy aliases, which
    # have no 4xlarge -- rather than read out of the array. Every case above
    # takes its fixture from EXPECTED and therefore cannot see a name being
    # dropped from it, which is the one thing this check exists to prevent.
    work=$(mktemp -d)
    for family in t1 c1 s1 u1 m1; do
        for size in nano micro small medium large xlarge 2xlarge 4xlarge; do
            echo "$family.$size"
        done
    done > "$work/canonical"
    printf '%s\n' nano micro small medium large xlarge 2xlarge >> "$work/canonical"
    sort "$work/canonical" > "$work/canonical.sorted"
    expected_presets | sort > "$work/actual.sorted"

    n=$(wc -l < "$work/canonical.sorted")
    if [ "$n" -ne 47 ]; then
        echo "the canonical set built here is $n names, not the documented 47" >&2
        rm -rf "$work"
        exit 1
    fi
    if ! diff -u "$work/canonical.sorted" "$work/actual.sorted"; then
        echo "EXPECTED no longer matches the canonical 40 instance types plus 7 aliases" >&2
        rm -rf "$work"
        exit 1
    fi
    rm -rf "$work"
}
