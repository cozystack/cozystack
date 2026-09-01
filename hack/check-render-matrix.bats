#!/usr/bin/env bats

# Contract for hack/check-render-matrix.sh. The point of these is that the check
# FAILS on a broken chart: a smoke check that cannot go red is worse than none,
# because it reports a number that looks like coverage.
#
# Written for cozytest.sh, which is not real bats: no `run`, no `$status`, no
# setup()/teardown(). Each test calls the script directly and inspects its exit
# with `if`, the way the other hack/*.bats here do.

@test "render matrix passes on the tree as it stands" {
    output=$(hack/check-render-matrix.sh)
    case "$output" in
        *"rendered,"*" skipped"*) ;;
        *) echo "expected a summary line, got: $output" >&2; exit 1 ;;
    esac
}

@test "render matrix fails on a chart whose template is broken" {
    tmp=$(mktemp -d)
    mkdir -p "$tmp/broken/templates"
    printf 'apiVersion: v2\nname: broken\nversion: 0.0.0\n' > "$tmp/broken/Chart.yaml"
    # `required` with no value is the cheapest deterministic render failure.
    printf '{{ required "deliberately broken" .Values.nope }}\n' > "$tmp/broken/templates/boom.yaml"

    if out=$(hack/check-render-matrix.sh "$tmp/broken" 2>&1); then
        echo "expected a non-zero exit for a broken chart, got success: $out" >&2
        rm -rf "$tmp"
        exit 1
    fi
    case "$out" in
        *"FAIL broken"*) ;;
        *) echo "expected 'FAIL broken', got: $out" >&2; rm -rf "$tmp"; exit 1 ;;
    esac
    rm -rf "$tmp"
}

# helm's own parser is what catches this, which is why the script carries no
# separate YAML pass. Pinned so that stays true: if a future helm accepted it,
# the check would silently start passing malformed manifests.
@test "render matrix fails on a template that is not valid YAML" {
    tmp=$(mktemp -d)
    mkdir -p "$tmp/badyaml/templates"
    printf 'apiVersion: v2\nname: badyaml\nversion: 0.0.0\n' > "$tmp/badyaml/Chart.yaml"
    printf 'a: b\n  c: d\n' > "$tmp/badyaml/templates/boom.yaml"

    if out=$(hack/check-render-matrix.sh "$tmp/badyaml" 2>&1); then
        echo "expected a non-zero exit for invalid YAML, got success: $out" >&2
        rm -rf "$tmp"
        exit 1
    fi
    case "$out" in
        *"FAIL badyaml"*) ;;
        *) echo "expected 'FAIL badyaml', got: $out" >&2; rm -rf "$tmp"; exit 1 ;;
    esac
    rm -rf "$tmp"
}

# A skip that has stopped being necessary is worse than no skip: it silently
# drops a chart from the sweep forever. This renders each skipped chart directly
# and requires it to STILL fail, so a chart that becomes renderable shows up as a
# red test rather than as permanent invisible exclusion.
#
# Replaces an earlier test that asserted every SKIP line contains parentheses.
# The only line that emits one is `echo "SKIP $name ($reason)"` guarded on a
# non-empty reason, so that assertion was unreachable by construction: it could
# not fail for the property it named.
@test "every skipped chart still genuinely fails to render" {
    # mktemp, not $BATS_TMPDIR: cozytest.sh is not real bats and defines none of
    # bats's variables, so under `set -u` that name aborts the suite.
    skips=$(mktemp)
    hack/check-render-matrix.sh | grep '^SKIP' | sed 's/^SKIP \([^ ]*\) .*/\1/' > "$skips" || true
    while read -r name; do
        [ -n "$name" ] || continue
        if helm template "$name-render-check" "packages/apps/$name" -n tenant-test \
             -f hack/testdata/render-fixtures/fresh.yaml >/dev/null 2>&1; then
            echo "chart '$name' is on the skip list but renders fine now — remove the skip" >&2
            rm -f "$skips"
            exit 1
        fi
    done < "$skips"
    rm -f "$skips"
}

# The sweep must cover the charts that exist rather than a number frozen when
# this was written: a chart added to packages/apps must land in the check or in
# the skip list, and the two together have to account for all of them.
@test "rendered plus skipped accounts for every app chart" {
    total=$(find packages/apps -mindepth 1 -maxdepth 1 -type d | wc -l | tr -d ' ')
    output=$(hack/check-render-matrix.sh)
    summary=$(printf '%s\n' "$output" | grep '^check-render-matrix:')
    rendered=$(printf '%s\n' "$summary" | sed 's/.*: \([0-9]*\) rendered.*/\1/')
    skipped=$(printf '%s\n' "$summary" | sed 's/.*, \([0-9]*\) skipped.*/\1/')
    sum=$((rendered + skipped))
    if [ "$sum" -ne "$total" ]; then
        echo "sweep covered $sum charts ($rendered rendered, $skipped skipped) but packages/apps holds $total" >&2
        exit 1
    fi
}

# A fixture that does not reproduce the platform's types tests a shape the
# platform never produces. The booleans are the trap: charts compare them with
# `eq $x "true"`, so a real YAML bool makes helm fail on incompatible types.
@test "fixtures carry the platform's string-typed values" {
    for f in hack/testdata/render-fixtures/*.yaml; do
        python3 -c 'import sys,yaml;d=yaml.safe_load(open(sys.argv[1])) or {};bad=[];[bad.append(f"{sec}.{k}") for sec in ("_cluster","_namespace") for k,v in (d.get(sec) or {}).items() if isinstance(v,(bool,int,float)) or v is None];sys.exit(f"{sys.argv[1]}: scalars must be strings, the platform quotes them: {bad}") if bad else None;sys.exit(f"{sys.argv[1]}: no _cluster map") if not (d.get("_cluster") or {}) else None' "$f"
    done
}

# Each fixture is one cluster state, and what matters is that they produce
# DIFFERENT RENDERS -- otherwise the sweep runs helm three times over the same
# shape and reports triple the coverage it has.
#
# An earlier version compared the fixture FILES on three key names. That passed
# while the sweep was in fact rendering 19 of 21 charts identically across all
# three states, which is the exact condition it claimed to prevent. Comparing
# output is the only form of this test that can fail for the right reason.
@test "the fixtures produce different renders for at least one chart" {
    count=$(find hack/testdata/render-fixtures -maxdepth 1 -name '*.yaml' | wc -l | tr -d ' ')
    [ "$count" -ge 2 ]

    # packages/apps/kubernetes is the chart that branches most on these values
    # (the _namespace.<service> flags gate ~15 manifests). If a future fixture
    # change stops moving even this one, the states have collapsed.
    a=$(helm template kubernetes-render-check packages/apps/kubernetes -n tenant-test \
          -f hack/testdata/render-fixtures/fresh.yaml 2>/dev/null | grep -c '^kind:')
    b=$(helm template kubernetes-render-check packages/apps/kubernetes -n tenant-test \
          -f hack/testdata/render-fixtures/configured.yaml 2>/dev/null | grep -c '^kind:')
    if [ "$a" = "$b" ]; then
        echo "fresh and configured render the same number of documents ($a) for packages/apps/kubernetes — the fixtures no longer represent different cluster states" >&2
        exit 1
    fi
}
