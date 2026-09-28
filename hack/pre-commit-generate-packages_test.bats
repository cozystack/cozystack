#!/usr/bin/env bats

load test_helper

add_generator() {
    root="$1"
    dir="$2"
    mkdir -p "$root/$dir"
    printf 'generate:\n\t@printf "%%s\\n" "%s" >> "$${GEN_LOG}"\n' "$dir" > "$root/$dir/Makefile"
}

@test "discovers package generators across categories" {
    script="$PWD/hack/pre-commit-generate-packages.sh"
    root=$(mktemp -d)
    add_generator "$root" packages/apps/app
    add_generator "$root" packages/extra/extra
    add_generator "$root" packages/library/library
    add_generator "$root" packages/system/system

    mkdir -p "$root/packages/system/no-generator"
    printf '# generate:\n.PHONY: generate\nall:\n\t@:\n' > "$root/packages/system/no-generator/Makefile"
    mkdir -p "$root/packages/system/system/charts/vendor"
    printf 'generate:\n\t@printf "vendored\\n" >> "$${GEN_LOG}"\n' > "$root/packages/system/system/charts/vendor/Makefile"

    (cd "$root" && GEN_LOG="$root/generators.log" "$script")

    expected=$(printf '%s\n' packages/apps/app packages/extra/extra packages/library/library packages/system/system)
    actual=$(sort "$root/generators.log")
    [ "$actual" = "$expected" ]
    rm -rf "$root"
}

@test "stops after a package generator fails" {
    script="$PWD/hack/pre-commit-generate-packages.sh"
    root=$(mktemp -d)
    mkdir -p "$root/packages/apps/failing" "$root/packages/system/later"
    printf 'generate:\n\t@false\n' > "$root/packages/apps/failing/Makefile"
    printf 'generate:\n\t@touch "$${LATER_MARKER}"\n' > "$root/packages/system/later/Makefile"

    rc=0
    (cd "$root" && LATER_MARKER="$root/later-ran" "$script") || rc=$?

    [ "$rc" -ne 0 ]
    [ ! -e "$root/later-ran" ]
    rm -rf "$root"
}
