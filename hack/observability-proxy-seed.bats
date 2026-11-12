#!/usr/bin/env bats

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"

prepare_seed_capture() {
    strict_setup
    seed_tmp=$(mktemp -d)
    seed_old_path=$PATH
    mkdir -p "$seed_tmp/bin"
    export SEED_CAPTURE="$seed_tmp/seed"
    export PATH="$seed_tmp/bin:$PATH"
    cat > "$seed_tmp/bin/curl" <<'EOF'
#!/usr/bin/env bash
set -eu
printf '%s\n' "$@" > "$SEED_CAPTURE.args"
cat > "$SEED_CAPTURE.body"
if ! grep --fixed-strings --line-regexp --quiet 'Content-Type: application/json' "$SEED_CAPTURE.args"; then
    echo 'seed request is missing its JSON content type' >&2
    exit 22
fi
EOF
    chmod +x "$seed_tmp/bin/curl"
}

assert_seed_request() {
    local account=$1
    prepare_seed_capture
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
    rm "$body" "$SEED_CAPTURE.args" "$SEED_CAPTURE.body" "$SEED_CAPTURE.expected" "$seed_tmp/bin/curl"
    rmdir "$seed_tmp/bin" "$seed_tmp"
    export PATH="$seed_old_path"
}

@test "observability seeds send JSON without consuming the body as form data" {
    assert_seed_request 0
}

@test "observability seeds preserve an explicit VictoriaLogs account" {
    assert_seed_request 7
}
