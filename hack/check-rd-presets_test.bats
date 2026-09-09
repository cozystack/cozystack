#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Regression guard for hack/check-rd-presets.sh: a schema it could not read must
# be reported as unreadable, never as a chart whose presets drifted.
#
# The distinction is not academic. The script used to feed jq through a pipe and
# discard its status (`printf '%s' "$schema" | jq ... 2>/dev/null || true`). When
# jq exited early -- seen on the 4-CPU/16 GiB unit runner under `make -j4` --
# printf died of EPIPE and the variable held PARTIAL output. Non-empty, so the
# emptiness guard passed it through, and every expected preset past the
# truncation point was reported missing from a file that contained all of them.
# Two PRs the same day were reddened that way, naming different files and
# different presets, which is the tell: real drift names the same pair every run.
#
# The stub reproduces exactly that shape -- some values on stdout, an error on
# stderr, a non-zero exit -- because that is the case the old code turned into a
# false verdict, and a stub that merely fails with no output would pass against
# the old code too.
#
# Compatible with the in-repo cozytest.sh runner, which runs each @test in a
# fresh subshell under `set -u` and provides no bats `run`/`$status`.
# -----------------------------------------------------------------------------

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
    out=$(PATH="$work:$PATH" hack/check-rd-presets.sh 2>&1) || rc=$?
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
