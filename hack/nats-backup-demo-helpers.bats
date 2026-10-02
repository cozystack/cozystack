#!/usr/bin/env bats
# Unit tests for nats_cli in examples/backups/nats/00-helpers.sh, the helper the
# nats roundtrip suite seeds and verifies JetStream streams through.
#
# kubectl and the `nats` CLI are stub executables on PATH. The kubectl stub
# answers the calls nats_cli makes -- get the CLI Pod's phase and owner label,
# create it when absent, wait for it, and `exec` into it -- and runs the exec'd
# `nats` invocation locally against the nats stub. STUB_PHASE / STUB_OWNER drive
# the reuse guard, and STUB_EXEC_ERROR makes the exec fail so the "a failed read
# says why" claim is pinned. The password is piped on stdin, so it must not
# appear in the recorded argv.
#
# cozytest.sh's awk runner recognizes only @test blocks and a bare `}`; there is
# no bats `run` or `$status`, and no setup/teardown, so each case calls
# make_stubs itself and asserts with direct shell tests that exit non-zero. The
# helper functions close on an indented brace because the runner rewrites a
# column-0 `}` into `return 0`, which would mask run_stream_count's exit status.

make_stubs() {
    stub=$(mktemp -d)
    cat > "$stub/kubectl" <<'EOF'
#!/bin/bash
printf '%s\n' "$*" >> "$STUB_DIR/kubectl.argv"
case "$1 $2 $3" in
"-n tenant-root get")
    [ -n "${STUB_GET_ERROR:-}" ] && { echo "Unable to connect to the server: i/o timeout" >&2; exit 1; }
    case "$*" in
    *"jsonpath={.status.phase}"*)          printf '%s' "${STUB_PHASE-Running}" ;;
    *"jsonpath={.metadata.labels"*)         printf '%s' "${STUB_OWNER-nats}" ;;
    esac
    exit 0 ;;
"-n tenant-root delete")
    exit 0 ;;
"-n tenant-root run")
    # Reachable only when the Pod is absent; a reuse that starts a Pod is a bug.
    [ -z "${STUB_PHASE-Running}" ] && exit 0
    echo "nats_cli started a Pod though a Ready one it owns exists" >&2; exit 95 ;;
"-n tenant-root wait")
    exit 0 ;;
"-n tenant-root exec")
    [ -n "${STUB_EXEC_ERROR:-}" ] && { echo "error: unable to upgrade connection: container not found" >&2; exit 1; }
    shift 6   # drop: -n tenant-root exec -i <pod> --
    exec "$@" ;;   # sh -c <script> sh <url> <args...>
*)
    echo "unexpected kubectl call: $*" >&2; exit 97 ;;
esac
EOF
    cat > "$stub/nats" <<'EOF'
#!/bin/sh
# nats stub: only `stream info <name> --json` is exercised.
case "$*" in
*"stream info"*"--json"*)
    printf '{"state":{"messages":42}}\n'; exit 0 ;;
esac
echo "unexpected nats call: $*" >&2; exit 90
EOF
    chmod +x "$stub/kubectl" "$stub/nats"
    : > "$stub/kubectl.argv"
    }

run_stream_count() {
    STUB_DIR=$stub STUB_PHASE="${STUB_PHASE-Running}" STUB_OWNER="${STUB_OWNER-nats}" \
        STUB_EXEC_ERROR="${STUB_EXEC_ERROR:-}" STUB_GET_ERROR="${STUB_GET_ERROR:-}" PATH="$stub:$PATH" NAMESPACE=tenant-root \
        NATS_USER=demo NATS_PASSWORD=s3cret bash -c '
        set -euo pipefail
        . examples/backups/nats/00-helpers.sh
        # Read the way the numbered scripts read it: inside a command
        # substitution, where bash does not apply errexit.
        got=$(stream_message_count demo orders)
        printf "%s\n" "$got"
    '
    }

@test "nats_cli reuses a Ready Pod it owns and reads the reply by exec" {
    make_stubs
    out=$(run_stream_count)
    [ "$out" = "42" ]
    grep -q 'exec -i nats-cli -- sh -c' "$stub/kubectl.argv"
}

@test "nats_cli passes the password on stdin, keeping it out of the exec argv" {
    make_stubs
    out=$(run_stream_count)
    [ "$out" = "42" ]
    grep -q 'nats://demo@demo.tenant-root.svc:4222' "$stub/kubectl.argv"
    if grep -q s3cret "$stub/kubectl.argv"; then
        echo "the password reached kubectl's argv" >&2
        exit 1
    fi
}

@test "nats_cli creates the CLI Pod when it is absent, then execs into it" {
    make_stubs
    export STUB_PHASE=
    out=$(run_stream_count)
    [ "$out" = "42" ]
    grep -q 'run nats-cli --image=' "$stub/kubectl.argv"
    grep -q 'labels=cozystack.io/backup-demo=nats' "$stub/kubectl.argv"
    grep -q 'exec -i nats-cli -- sh -c' "$stub/kubectl.argv"
}

@test "nats_cli refuses a same-named Pod the demo does not own" {
    make_stubs
    export STUB_OWNER=someone-else
    err=$(mktemp)
    if run_stream_count 2>"$err"; then
        echo "nats_cli used a Pod it does not own" >&2
        exit 1
    fi
    grep -q 'does not own it' "$err"
    if grep -q ' exec ' "$stub/kubectl.argv"; then
        echo "nats_cli exec'd into a Pod it refused to use" >&2
        exit 1
    fi
}

@test "nats_cli touches no Pod when it cannot read the CLI Pod's state" {
    make_stubs
    export STUB_GET_ERROR=1
    err=$(mktemp)
    if run_stream_count 2>"$err"; then
        echo "a failed Pod lookup read as a successful reply" >&2
        exit 1
    fi
    grep -q 'Unable to connect to the server' "$err"
    if grep -qE ' (delete|run|exec) ' "$stub/kubectl.argv"; then
        echo "nats_cli_pod acted on a Pod whose owner it could not read:" >&2
        cat "$stub/kubectl.argv" >&2
        exit 1
    fi
}

@test "nats_cli fails with kubectl's own error when the exec fails" {
    make_stubs
    # A flag, not the message: the runner traces the command that passes
    # STUB_EXEC_ERROR on, into the same stderr the grep below reads, so a
    # message carried in it would match with the helper's stderr dropped.
    export STUB_EXEC_ERROR=1
    err=$(mktemp)
    if run_stream_count 2>"$err"; then
        echo "a failed exec read as a successful empty count" >&2
        exit 1
    fi
    grep -q 'unable to upgrade connection: container not found' "$err"
}
