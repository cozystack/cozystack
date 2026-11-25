#!/usr/bin/env bats

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"

# The script is copied into a scratch tree with go and helm stubbed: helm
# renders a chart's render.yaml followed by the values file it was given, and
# the checker built by go fails on a stream containing BROKEN.
setup() {
    strict_setup
    dir=$(mktemp -d)
    mkdir -p "$dir/hack" "$dir/bin" "$dir/packages/system/foo-rd" "$dir/packages/system/kubevirt-instancetypes"
    cp "$REPO_ROOT/hack/check-rd-consumption.sh" "$dir/hack/"
    echo 'kind: ApplicationDefinition' > "$dir/packages/system/foo-rd/render.yaml"
    cat > "$dir/bin/go" <<'STUB'
#!/bin/sh
printf '#!/bin/sh\n! grep -q BROKEN\n' > "$3"
chmod +x "$3"
STUB
    cat > "$dir/bin/helm" <<'STUB'
#!/bin/sh
echo "$*" >> "$HELM_LOG"
[ "$3" = packages/system/kubevirt-instancetypes ] && exit 0
[ -e "$3/unrenderable" ] && exit 1
cat "$3/render.yaml"
[ "${4:-}" = --values ] && cat "$5"
exit 0
STUB
    chmod +x "$dir/bin/go" "$dir/bin/helm"
    export HELM_LOG="$dir/helm.log"
}

teardown() {
    rm -rf "$dir"
}

run_check() {
    rc=0
    out=$(PATH="$dir/bin:$PATH" "$dir/hack/check-rd-consumption.sh" 2>&1) || rc=$?
    printf '%s\n' "$out"
}

@test "a chart without variants renders once and passes" {
    run_check
    [ "$rc" -eq 0 ]
    [ "$(grep -c 'packages/system/foo-rd' "$HELM_LOG")" -eq 1 ]
}

@test "a declaration broken only in a variant fails, naming the variant" {
    echo 'BROKEN' > "$dir/packages/system/foo-rd/values-bar.yaml"
    run_check
    [ "$rc" -ne 0 ]
    printf '%s\n' "$out" | grep -Fx 'FAIL: packages/system/foo-rd with values-bar.yaml'
    if printf '%s\n' "$out" | grep -Fx 'FAIL: packages/system/foo-rd'; then
        return 1
    fi
}

@test "a chart that does not render fails" {
    touch "$dir/packages/system/foo-rd/unrenderable"
    run_check
    [ "$rc" -ne 0 ]
    printf '%s\n' "$out" | grep -Fx 'FAIL: packages/system/foo-rd does not render'
}
