#!/usr/bin/env bats
# Unit tests for redis_cmd in examples/backups/redis/00-helpers.sh, the helper
# the redis-2-backup-roundtrip suite reads and writes its marker key through.
#
# kubectl and redis-cli are stub executables on PATH. The kubectl stub accepts
# exactly the calls the helper is expected to make and runs an exec'd script
# locally with the redis-cli stub, so the in-Pod shell is exercised too. Any
# other kubectl call, a throwaway `kubectl run` Pod among them, fails the test.
#
# The helpers are Bash and this runner is POSIX sh, so each case drives them
# through `bash -c` under the same `set -euo pipefail` the demo scripts use.
# The two helper functions close on an indented brace because the runner
# rewrites a column-0 `}` into `return 0`, which would mask run_redis_cmd's
# exit status.

make_stubs() {
    stub=$(mktemp -d)
    cat > "$stub/kubectl" <<'EOF'
#!/bin/bash
printf '%s\n' "$*" >> "$STUB_DIR/kubectl.argv"
if [ "$*" = "-n tenant-root get secret redis-demo-auth --ignore-not-found -o jsonpath={.data.password}" ]; then
    printf '%s' "${STUB_PW_B64:-}"
    exit 0
fi
if [ "$1 $2 $3 $4 $5 $6 $7 $8 $9 ${10}" != "-n tenant-root exec -i deploy/rfs-redis-demo -c sentinel -- sh -c" ]; then
    echo "unexpected kubectl call: $*" >&2
    exit 97
fi
if [ -n "${STUB_EXEC_ERROR:-}" ]; then
    echo "$STUB_EXEC_ERROR" >&2
    exit 1
fi
shift 10
script=$1
shift
exec sh -c "$script" "$@"
EOF
    cat > "$stub/redis-cli" <<'EOF'
#!/bin/sh
if [ "$*" = "-p 26379 sentinel get-master-addr-by-name mymaster" ]; then
    [ -n "${STUB_NO_MASTER:-}" ] || printf '10.0.0.5\n6379\n'
    exit 0
fi
printf '%s|%s\n' "${REDISCLI_AUTH-unset}" "$*" >> "$STUB_DIR/redis-cli.calls"
printf 'reply-value\n'
EOF
    chmod +x "$stub/kubectl" "$stub/redis-cli"
    : > "$stub/kubectl.argv"
    : > "$stub/redis-cli.calls"
    }

run_redis_cmd() {
    STUB_DIR=$stub PATH="$stub:$PATH" NAMESPACE=tenant-root bash -c '
        set -euo pipefail
        . examples/backups/redis/00-helpers.sh
        redis_cmd demo "$@"
    ' bash "$@"
    }

@test "redis_cmd reads the reply through exec and passes the password on stdin" {
    make_stubs
    export STUB_PW_B64
    STUB_PW_B64=$(printf 's3cret' | base64)
    out=$(run_redis_cmd GET sentinel:marker)
    [ "$out" = "reply-value" ]
    [ "$(cat "$stub/redis-cli.calls")" = "s3cret|-h 10.0.0.5 -p 6379 GET sentinel:marker" ]
    if grep -q s3cret "$stub/kubectl.argv"; then
        echo "the password reached kubectl's argv" >&2
        exit 1
    fi
}

@test "redis_cmd sends no AUTH when the app has no password Secret" {
    make_stubs
    out=$(run_redis_cmd SET sentinel:marker v1)
    [ "$out" = "reply-value" ]
    [ "$(cat "$stub/redis-cli.calls")" = "unset|-h 10.0.0.5 -p 6379 SET sentinel:marker v1" ]
}

@test "redis_cmd fails with kubectl's own error when the exec fails" {
    make_stubs
    err=$(mktemp)
    export STUB_EXEC_ERROR="error: unable to upgrade connection: container not found"
    if run_redis_cmd GET sentinel:marker 2>"$err"; then
        echo "a failed exec read as a successful empty reply" >&2
        exit 1
    fi
    grep -q 'unable to upgrade connection: container not found' "$err"
}

@test "redis_cmd fails when the sentinel names no master" {
    make_stubs
    err=$(mktemp)
    export STUB_NO_MASTER=1
    if run_redis_cmd GET sentinel:marker 2>"$err"; then
        echo "a sentinel with no master read as a successful reply" >&2
        exit 1
    fi
    grep -q 'sentinel knows no master for mymaster' "$err"
    [ ! -s "$stub/redis-cli.calls" ]
}
