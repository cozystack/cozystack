#!/usr/bin/env bats

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"

setup() {
    strict_setup
    mkdir -p "$BATS_TEST_TMPDIR/bin"
    export SEED_CAPTURE="$BATS_TEST_TMPDIR/seed"
    export TMPDIR="$BATS_TEST_TMPDIR"
    export PATH="$BATS_TEST_TMPDIR/bin:$PATH"
    cat > "$BATS_TEST_TMPDIR/bin/curl" <<'EOF'
#!/usr/bin/env bash
set -eu
printf '%s\n' "$@" > "$SEED_CAPTURE.args"
cat > "$SEED_CAPTURE.body"
if ! grep --fixed-strings --line-regexp --quiet 'Content-Type: application/json' "$SEED_CAPTURE.args"; then
    echo 'seed request is missing its JSON content type' >&2
    exit 22
fi
EOF
    chmod +x "$BATS_TEST_TMPDIR/bin/curl"
}

assert_seed_request() {
    local account=$1
    # shellcheck source=e2e-chainsaw/observability-proxy/lib.sh
    . "$REPO_ROOT/hack/e2e-chainsaw/observability-proxy/lib.sh"
    if [ "$account" = 0 ]; then
        ingest tenant-test-logsa e2e-marker-a
    else
        ingest tenant-test-logsa e2e-marker-a "$account"
    fi
    grep --fixed-strings --line-regexp --quiet "AccountID: $account" "$SEED_CAPTURE.args"
    grep --fixed-strings --line-regexp --quiet 'ProjectID: 0' "$SEED_CAPTURE.args"
    grep --fixed-strings --line-regexp --quiet 'http://localhost:19471/insert/jsonline?_stream_fields=kubernetes_namespace_name&_msg_field=log' "$SEED_CAPTURE.args"
    grep --fixed-strings --line-regexp --quiet -- '--data-binary' "$SEED_CAPTURE.args"
    grep --fixed-strings --line-regexp --quiet '@-' "$SEED_CAPTURE.args"
    printf '%s\n' '{"kubernetes_namespace_name":"tenant-test-logsa","log":"e2e-marker-a"}' > "$SEED_CAPTURE.expected"
    cmp "$SEED_CAPTURE.expected" "$SEED_CAPTURE.body"
    rm "$body"
}

@test "observability seeds send JSON without consuming the body as form data" {
    assert_seed_request 0
}

@test "observability seeds preserve an explicit VictoriaLogs account" {
    assert_seed_request 7
}
