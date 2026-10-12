#!/usr/bin/env bats
# Contract for the wait helpers the backup walkthroughs share: each is defined
# once, in examples/backups/_lib/wait-helpers.sh, so a fix made there reaches
# every walkthrough; each walkthrough's 00-helpers.sh provides them; and
# wait_for_field reports why kubectl could not read a field while staying quiet
# about a resource that does not exist yet.

load test_helper

SHARED=examples/backups/_lib/wait-helpers.sh
SHARED_HELPERS="wait_for_field wait_hr_ready wait_deleted"

@test "each shared wait helper is defined once, in the shared file" {
    fail=
    for fn in $SHARED_HELPERS; do
        definers=$(grep -rlE --include='*.sh' "^[[:space:]]*(function[[:space:]]+)?${fn}[[:space:]]*\(\)" examples packages | sort)
        if [ "$definers" != "$SHARED" ]; then
            fail="$fail
${fn} is defined in:
${definers:-  (nowhere)}"
        fi
    done
    [ -z "$fail" ] || {
        echo "a walkthrough carries its own copy of a shared helper; use the one in $SHARED:$fail" >&2
        exit 1
    }
}

@test "every walkthrough's helpers provide the shared wait helpers" {
    # Some helpers read the cluster while being sourced (kafka resolves its
    # image from the operator); a stub keeps this test off any real cluster.
    stub=$(mktemp -d)
    printf '#!/bin/sh\nexit 1\n' > "$stub/kubectl"
    chmod +x "$stub/kubectl"
    for helpers in examples/backups/*/00-helpers.sh; do
        PATH="$stub:$PATH" bash -c '
            set -euo pipefail
            . "$1"
            for fn in $2; do
                declare -F "$fn" >/dev/null || { echo "$1 does not provide $fn" >&2; exit 1; }
            done
        ' _ "$helpers" "$SHARED_HELPERS"
    done
}

@test "wait_for_field shows why kubectl could not read the field" {
    # A Forbidden read has to reach the caller; swallowed, it looks like a
    # field that is still empty.
    stub=$(mktemp -d)
    printf '#!/bin/sh\necho "Error from server (Forbidden): backupjobs is forbidden" >&2\nexit 1\n' > "$stub/kubectl"
    chmod +x "$stub/kubectl"
    err=$(mktemp)
    if PATH="$stub:$PATH" bash -c '
        set -euo pipefail
        . examples/backups/postgres/00-helpers.sh
        wait_for_field backupjob demo "{.status.phase}" Succeeded tenant-test 0
    ' 2>"$err"; then
        echo "wait_for_field succeeded against a kubectl that cannot read" >&2
        exit 1
    fi
    grep -q 'backupjobs is forbidden' "$err" || {
        echo "wait_for_field dropped kubectl's reason:" >&2
        cat "$err" >&2
        exit 1
    }
}

@test "wait_for_field stays quiet while the resource does not exist yet" {
    # The stub answers like kubectl: NotFound on stderr and exit 1, unless the
    # read asks for --ignore-not-found, which makes an absent resource empty.
    stub=$(mktemp -d)
    cat > "$stub/kubectl" <<'EOF'
#!/bin/sh
case " $* " in
*" --ignore-not-found "*) exit 0 ;;
esac
echo 'Error from server (NotFound): backupjobs.backups.cozystack.io "demo" not found' >&2
exit 1
EOF
    chmod +x "$stub/kubectl"
    err=$(mktemp)
    if PATH="$stub:$PATH" bash -c '
        set -euo pipefail
        . examples/backups/postgres/00-helpers.sh
        wait_for_field backupjob demo "{.status.phase}" Succeeded tenant-test 0
    ' 2>"$err"; then
        echo "wait_for_field succeeded against a resource that does not exist" >&2
        exit 1
    fi
    if grep -q 'NotFound' "$err"; then
        echo "wait_for_field printed kubectl's NotFound for a resource it is waiting on:" >&2
        cat "$err" >&2
        exit 1
    fi
}
