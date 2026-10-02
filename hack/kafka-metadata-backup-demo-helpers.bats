#!/usr/bin/env bats
# Unit tests for kafka_run in examples/backups/kafka-metadata/00-helpers.sh, the
# helper the kafka-3-metadata-roundtrip suite seeds and verifies topics through.
#
# kubectl and the Kafka CLI are stub executables on PATH. The kubectl stub
# answers the calls kafka_run makes -- get the CLI Pod's phase, owner label and
# runAsNonRoot, create it when absent or started without the restricted profile
# (recording the overrides it is given), wait for it, and `exec` into it -- and
# runs the exec'd snippet locally against the kafka-topics.sh stub. STUB_PHASE /
# STUB_OWNER / STUB_NONROOT drive the reuse guard, and STUB_EXEC_ERROR makes the
# exec fail so the "a failed read says why" claim is pinned rather than asserted
# in prose.
#
# cozytest.sh's awk runner recognizes only @test blocks and a bare `}`; there is
# no bats `run` or `$status`, and no setup/teardown, so each case calls
# make_stubs itself and asserts with direct shell tests that exit non-zero. The
# helper functions close on an indented brace because the runner rewrites a
# column-0 `}` into `return 0`, which would mask run_topic_meta's exit status.

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
    *"jsonpath={.metadata.labels"*)         printf '%s' "${STUB_OWNER-kafka-metadata}" ;;
    *"jsonpath={.spec.securityContext.runAsNonRoot}"*) printf '%s' "${STUB_NONROOT-true}" ;;
    esac
    exit 0 ;;
"-n tenant-root delete")
    exit 0 ;;
"-n tenant-root run")
    for a in "$@"; do
        case "$a" in
        --overrides=*)     printf '%s' "${a#--overrides=}" > "$STUB_DIR/overrides.json" ;;
        --override-type=*) printf '%s' "${a#--override-type=}" > "$STUB_DIR/override-type" ;;
        esac
    done
    # Reachable only when the Pod is absent or predates the restricted
    # profile; a reuse that starts a Pod is a bug.
    { [ -z "${STUB_PHASE-Running}" ] || [ -z "${STUB_NONROOT-true}" ]; } && exit 0
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
# Minimal --describe stub: one line carrying PartitionCount and retention.ms so
# topic_meta has a realistic line to parse.
printf 'Topic: orders\tPartitionCount: 3\tReplicationFactor: 1\tConfigs: retention.ms=123456\n'
EOF
    chmod +x "$stub/kubectl" "$stub/kafka-topics.sh"
    : > "$stub/kubectl.argv"
    }

run_topic_meta() {
    STUB_DIR=$stub STUB_PHASE="${STUB_PHASE-Running}" STUB_OWNER="${STUB_OWNER-kafka-metadata}" STUB_NONROOT="${STUB_NONROOT-true}" \
        STUB_EXEC_ERROR="${STUB_EXEC_ERROR:-}" STUB_GET_ERROR="${STUB_GET_ERROR:-}" PATH="$stub:$PATH" NAMESPACE=tenant-root \
        KAFKA_BIN=$stub TOPIC=orders bash -c '
        set -euo pipefail
        . examples/backups/kafka-metadata/00-helpers.sh
        # Read the way the numbered scripts read it: inside a command
        # substitution, where bash does not apply errexit.
        got=$(topic_meta demo)
        printf "%s\n" "$got"
    '
    }

@test "kafka_run reuses a Ready Pod it owns and reads the reply by exec" {
    make_stubs
    out=$(run_topic_meta)
    [ "$out" = "3 123456" ]
    grep -q 'exec -i kafka-cli -- bash -c' "$stub/kubectl.argv"
}

@test "kafka_run creates the CLI Pod when it is absent, then execs into it" {
    make_stubs
    export STUB_PHASE=
    out=$(run_topic_meta)
    [ "$out" = "3 123456" ]
    grep -q 'run kafka-cli --image=' "$stub/kubectl.argv"
    grep -q 'labels=cozystack.io/backup-demo=kafka-metadata' "$stub/kubectl.argv"
    grep -q 'exec -i kafka-cli -- bash -c' "$stub/kubectl.argv"
}

@test "kafka_run starts the CLI Pod under the restricted Pod Security profile" {
    make_stubs
    export STUB_PHASE=
    run_topic_meta >/dev/null
    # The overrides are a JSON patch, which kubectl applies only with
    # --override-type=json; under the default merge type the array is rejected.
    [ "$(cat "$stub/override-type")" = "json" ]
    jq --exit-status '
        (map(select(.op == "add" and .path == "/spec/securityContext")) | .[0].value) as $pod
        | (map(select(.op == "add" and .path == "/spec/containers/0/securityContext")) | .[0].value) as $ctr
        | $pod.runAsNonRoot == true
          and $pod.seccompProfile.type == "RuntimeDefault"
          and $ctr.allowPrivilegeEscalation == false
          and $ctr.capabilities.drop == ["ALL"]
    ' "$stub/overrides.json"
}

@test "kafka_run replaces a Pod it owns that predates the restricted profile" {
    make_stubs
    export STUB_NONROOT=
    out=$(run_topic_meta)
    [ "$out" = "3 123456" ]
    grep -q 'delete pod kafka-cli' "$stub/kubectl.argv"
    grep -q 'run kafka-cli --image=' "$stub/kubectl.argv"
    [ "$(cat "$stub/override-type")" = "json" ]
}

@test "kafka_run refuses a same-named Pod the demo does not own" {
    make_stubs
    export STUB_OWNER=someone-else
    err=$(mktemp)
    if run_topic_meta 2>"$err"; then
        echo "kafka_run used a Pod it does not own" >&2
        exit 1
    fi
    grep -q 'does not own it' "$err"
    if grep -q ' exec ' "$stub/kubectl.argv"; then
        echo "kafka_run exec'd into a Pod it refused to use" >&2
        exit 1
    fi
}

@test "kafka_run touches no Pod when it cannot read the CLI Pod's state" {
    make_stubs
    export STUB_GET_ERROR=1
    err=$(mktemp)
    if run_topic_meta 2>"$err"; then
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

@test "kafka_run fails with kubectl's own error when the exec fails" {
    make_stubs
    # A flag, not the message: the runner traces the command that passes
    # STUB_EXEC_ERROR on, into the same stderr the grep below reads, so a
    # message carried in it would match with the helper's stderr dropped.
    export STUB_EXEC_ERROR=1
    err=$(mktemp)
    if run_topic_meta 2>"$err"; then
        echo "a failed exec read as a successful empty reply" >&2
        exit 1
    fi
    grep -q 'unable to upgrade connection: container not found' "$err"
}
