#!/usr/bin/env bats

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
CHECK="$REPO_ROOT/hack/check-rd-presets.sh"

canonical_presets() {
    for family in t1 c1 s1 u1 m1; do
        for size in nano micro small medium large xlarge 2xlarge 4xlarge; do
            echo "$family.$size"
        done
    done
    printf '%s\n' nano micro small medium large xlarge 2xlarge
}

write_schema() {
    {
        printf 'spec:\n  application:\n    openAPISchema: |\n'
        sed 's/^/      /'
    } > packages/system/foo-rd/cozyrds/foo.yaml
}

enum_schema() {
    jq -Rn '{type: "object", properties: {resourcesPreset: {enum: [inputs]}}}' enums
}

make_tree() {
    dir=$(mktemp -d)
    cd "$dir" || return 1
    mkdir -p packages/system/foo-rd/cozyrds packages/apps packages/extra
    canonical_presets > enums
    enum_schema | write_schema
}

run_check() {
    rc=0
    out=$("$CHECK" 2>&1) || rc=$?
    printf '%s\n' "$out"
}

assert_unreadable() {
    [ "$rc" -ne 0 ]
    printf '%s\n' "$out" | grep -F 'FAIL: packages/system/foo-rd/cozyrds/foo.yaml openAPISchema could not be read:'
    printf '%s\n' "$out" | grep -F 'parse error:'
    case "$out" in
        *"resourcesPreset enum missing"*) return 1 ;;
    esac
}

cleanup() {
    cd "$REPO_ROOT" || return 1
    rm -rf "$dir"
}

@test "malformed schema fails with the file and jq diagnostic" {
    make_tree
    printf '{\n' | write_schema
    run_check
    assert_unreadable
    cleanup
}

@test "jq failure after emitting presets cannot pass or report drift" {
    make_tree
    printf '%s\n' t1.nano t1.micro > enums
    # jq emits the first document's enum before parsing the malformed next one.
    { enum_schema; printf '{\n'; } | write_schema
    run_check
    assert_unreadable
    cleanup
}

@test "a schema without resourcesPreset is skipped" {
    make_tree
    printf '{"type":"object"}\n' | write_schema
    run_check
    [ "$rc" -eq 0 ]
    cleanup
}

@test "an absent schema is skipped" {
    make_tree
    printf 'spec: {}\n' > packages/system/foo-rd/cozyrds/foo.yaml
    run_check
    [ "$rc" -eq 0 ]
    cleanup
}

@test "a complete canonical enum passes" {
    make_tree
    run_check
    [ "$rc" -eq 0 ]
    cleanup
}

@test "a preset list larger than the pipe buffer passes" {
    make_tree
    # Exceed the CI runner's 1 MiB pipe maximum to make a SIGPIPE regression deterministic.
    while [ "$(wc -c < enums)" -lt 4194304 ]; do
        cat enums enums > enums.next
        mv enums.next enums
    done
    mkdir bin
    cat > bin/jq <<'STUB'
#!/bin/sh
cat enums
STUB
    chmod +x bin/jq
    PATH="$dir/bin:$PATH" run_check
    [ "$rc" -eq 0 ]
    cleanup
}

@test "a missing preset is reported by name" {
    make_tree
    canonical_presets | sed '/^c1\.medium$/d' > enums
    enum_schema | write_schema
    run_check
    [ "$rc" -ne 0 ]
    printf '%s\n' "$out" | grep -F 'resourcesPreset enum missing: c1.medium'
    cleanup
}

@test "similar preset names do not satisfy exact membership" {
    make_tree
    canonical_presets | sed 's/^t1\.nano$/t1.nanoX/; s/^c1\.small$/c1Xsmall/' > enums
    enum_schema | write_schema
    run_check
    [ "$rc" -ne 0 ]
    printf '%s\n' "$out" | grep -F 'resourcesPreset enum missing: t1.nano c1.small'
    cleanup
}

@test "EXPECTED contains exactly the canonical names" {
    make_tree
    sort enums > canonical.sorted
    sed -n '/^EXPECTED=(/,/^)/p' "$CHECK" | sed '1d;$d' | tr -s '[:space:]' '\n' | sed '/^$/d' | sort > actual.sorted
    [ "$(wc -l < canonical.sorted)" -eq 47 ]
    diff -u canonical.sorted actual.sorted
    cleanup
}
