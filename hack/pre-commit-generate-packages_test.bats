#!/usr/bin/env bats

setup() {
    REPO_ROOT="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
    TEST_ROOT="$(mktemp -d)"
}

teardown() {
    rm -rf "$TEST_ROOT"
}

add_generator() {
    dir="$1"
    mkdir -p "$TEST_ROOT/$dir"
    printf 'generate:\n\t@printf "%%s\\n" "%s" >> "$${GEN_LOG}"\n' "$dir" > "$TEST_ROOT/$dir/Makefile"
}

@test "discovers package generators across categories" {
    add_generator packages/apps/app
    add_generator packages/extra/extra
    add_generator packages/library/library
    add_generator packages/system/system

    mkdir -p "$TEST_ROOT/packages/system/no-generator"
    printf '# generate:\n.PHONY: generate\nall:\n\t@:\n' > "$TEST_ROOT/packages/system/no-generator/Makefile"
    mkdir -p "$TEST_ROOT/packages/system/system/charts/vendor"
    printf 'generate:\n\t@printf "vendored\\n" >> "$${GEN_LOG}"\n' > "$TEST_ROOT/packages/system/system/charts/vendor/Makefile"

    run sh -c 'cd "$1" && GEN_LOG="$2" "$3/hack/pre-commit-generate-packages.sh"' sh "$TEST_ROOT" "$TEST_ROOT/generators.log" "$REPO_ROOT"

    [ "$status" -eq 0 ]
    run sort "$TEST_ROOT/generators.log"
    [ "$status" -eq 0 ]
    [ "$output" = $'packages/apps/app\npackages/extra/extra\npackages/library/library\npackages/system/system' ]
}

@test "stops after a package generator fails" {
    mkdir -p "$TEST_ROOT/packages/apps/failing"
    printf 'generate:\n\t@false\n' > "$TEST_ROOT/packages/apps/failing/Makefile"
    mkdir -p "$TEST_ROOT/packages/system/later"
    printf 'generate:\n\t@touch "$${LATER_MARKER}"\n' > "$TEST_ROOT/packages/system/later/Makefile"

    run sh -c 'cd "$1" && LATER_MARKER="$2" "$3/hack/pre-commit-generate-packages.sh"' sh "$TEST_ROOT" "$TEST_ROOT/later-ran" "$REPO_ROOT"

    [ "$status" -ne 0 ]
    [ ! -e "$TEST_ROOT/later-ran" ]
}
