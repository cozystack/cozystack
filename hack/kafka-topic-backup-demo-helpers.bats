#!/usr/bin/env bats
# Unit tests for kafka_run in examples/backups/kafka/00-helpers.sh, the helper
# the kafka topic-data round trip seeds and verifies topics through.
#
# kubectl and the Kafka CLI are stub executables on PATH. The kubectl stub
# answers the calls kafka_run makes -- get the CLI Pod's phase and owner label,
# create it when absent, wait for it, and `exec` into it -- and runs the exec'd
# snippet locally against the kafka-topics.sh stub. Any other kubectl call, a
# throwaway `kubectl run -i` Pod among them, fails the test.
#
# cozytest.sh's runner recognizes only @test blocks and a bare `}`, and rewrites
# a column-0 `}` into `return 0`, so the helpers below close on an indented
# brace to keep run_replication_factor's exit status.

make_stubs() {
    stub=$(mktemp -d)
    cat > "$stub/kubectl" <<'EOF'
#!/bin/bash
printf '%s\n' "$*" >> "$STUB_DIR/kubectl.argv"
case "$1 $2 $3" in
"-n tenant-root get")
    [ -n "${STUB_GET_ERROR:-}" ] && { echo "Unable to connect to the server: i/o timeout" >&2; exit 1; }
    case "$*" in
    *"jsonpath={.status.phase}"*)   printf '%s' "${STUB_PHASE-Running}" ;;
    *"jsonpath={.metadata.labels"*) printf '%s' "${STUB_OWNER-kafka}" ;;
    *) echo "unexpected kubectl get: $*" >&2; exit 98 ;;
    esac
    exit 0 ;;
"-n tenant-root delete")
    exit 0 ;;
"-n tenant-root run")
    case "$*" in
    *" --rm"*|*" -i "*) echo "kafka_run read through a throwaway attached Pod: $*" >&2; exit 94 ;;
    esac
    [ -z "${STUB_PHASE-Running}" ] && exit 0
    echo "kafka_run started a Pod though a Ready one it owns exists" >&2; exit 95 ;;
"-n tenant-root wait")
    exit 0 ;;
"-n tenant-root exec")
    [ -n "${STUB_EXEC_ERROR:-}" ] && { echo "error: unable to upgrade connection: container not found" >&2; exit 1; }
    shift 6   # drop: -n tenant-root exec -i <pod> --
    if [ "$1" != "bash" ] || [ "$2" != "-c" ]; then
        echo "unexpected exec payload: $*" >&2; exit 96
    fi
    shift 2
    exec bash -c "$1" ;;
*)
    echo "unexpected kubectl call: $*" >&2; exit 97 ;;
esac
EOF
    cat > "$stub/kafka-topics.sh" <<'EOF'
#!/bin/sh
printf 'Topic: orders\tTopicId: x\tPartitionCount: 3\tReplicationFactor: 2\tConfigs: \n'
EOF
    chmod +x "$stub/kubectl" "$stub/kafka-topics.sh"
    : > "$stub/kubectl.argv"
    }

run_replication_factor() {
    STUB_DIR=$stub STUB_PHASE="${STUB_PHASE-Running}" STUB_OWNER="${STUB_OWNER-kafka}" \
        STUB_EXEC_ERROR="${STUB_EXEC_ERROR:-}" STUB_GET_ERROR="${STUB_GET_ERROR:-}" \
        PATH="$stub:$PATH" NAMESPACE=tenant-root \
        KAFKA_IMAGE=example.test/kafka:unit KAFKA_BIN=$stub TOPIC=orders bash -c '
        set -euo pipefail
        . examples/backups/kafka/00-helpers.sh
        # Read the way the numbered scripts read it: inside a command
        # substitution, where bash does not apply errexit, so a helper that
        # only fails under -e would carry on here.
        rf=$(topic_replication_factor demo)
        printf "%s\n" "$rf"
    '
    }

@test "kafka topic demo reuses a Ready CLI Pod it owns and reads the reply by exec" {
    make_stubs
    out=$(run_replication_factor)
    [ "$out" = "2" ]
    grep -q 'exec -i kafka-topic-cli -- bash -c' "$stub/kubectl.argv"
    if grep -q ' run ' "$stub/kubectl.argv"; then
        echo "a Ready CLI Pod was not reused:" >&2
        cat "$stub/kubectl.argv" >&2
        exit 1
    fi
}

@test "kafka topic demo creates a restricted CLI Pod when it is absent, then execs into it" {
    make_stubs
    export STUB_PHASE=
    out=$(run_replication_factor)
    [ "$out" = "2" ]
    grep -q 'run kafka-topic-cli --image=example.test/kafka:unit' "$stub/kubectl.argv"
    grep -q 'labels=cozystack.io/backup-demo=kafka' "$stub/kubectl.argv"
    grep -q '"runAsNonRoot":true' "$stub/kubectl.argv"
    grep -q '"drop":\["ALL"\]' "$stub/kubectl.argv"
    grep -q 'exec -i kafka-topic-cli -- bash -c' "$stub/kubectl.argv"
}

@test "kafka topic demo refuses a same-named Pod it does not own" {
    make_stubs
    export STUB_OWNER=kafka-metadata
    err=$(mktemp)
    if run_replication_factor 2>"$err"; then
        echo "kafka_run used a Pod it does not own" >&2
        exit 1
    fi
    grep -q 'does not own it' "$err"
    if grep -q ' exec ' "$stub/kubectl.argv"; then
        echo "kafka_run exec'd into a Pod it refused to use" >&2
        exit 1
    fi
}

@test "kafka topic demo touches no Pod when it cannot read the CLI Pod's state" {
    make_stubs
    export STUB_GET_ERROR=1
    err=$(mktemp)
    if run_replication_factor 2>"$err"; then
        echo "a failed Pod lookup read as a successful reply" >&2
        exit 1
    fi
    grep -q 'Unable to connect to the server' "$err"
    if grep -qE ' (delete|run|exec) ' "$stub/kubectl.argv"; then
        echo "kafka_cli_pod acted on a Pod whose owner it could not read:" >&2
        cat "$stub/kubectl.argv" >&2
        exit 1
    fi
}

@test "kafka topic demo fails with kubectl's own error when the exec fails" {
    make_stubs
    # A flag, not the message: the runner traces the command that passes
    # STUB_EXEC_ERROR on, into the same stderr the grep below reads, so a
    # message carried in it would match with the helper's stderr dropped.
    export STUB_EXEC_ERROR=1
    err=$(mktemp)
    if run_replication_factor 2>"$err"; then
        echo "a failed exec read as a successful empty reply" >&2
        exit 1
    fi
    grep -q 'unable to upgrade connection: container not found' "$err"
}

@test "kafka topic demo keeps its own CLI Pod when the kafka-metadata demo's name is set" {
    make_stubs
    export KAFKA_CLI_POD=kafka-cli
    out=$(run_replication_factor)
    [ "$out" = "2" ]
    grep -q 'exec -i kafka-topic-cli -- bash -c' "$stub/kubectl.argv" || {
        echo "the kafka-metadata demo's KAFKA_CLI_POD moved the topic demo's Pod:" >&2
        cat "$stub/kubectl.argv" >&2
        exit 1
    }
}
