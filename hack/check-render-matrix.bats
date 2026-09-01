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

# Every skipped chart has to carry a reason, so a skip cannot quietly become the
# way a chart avoids the check.
@test "every skip names a reason" {
    output=$(hack/check-render-matrix.sh)
    printf '%s\n' "$output" | grep '^SKIP' > /tmp/render-skips.$$ || true
    while read -r line; do
        [ -n "$line" ] || continue
        case "$line" in
            *"("*")"*) ;;
            *) echo "skip without a reason: $line" >&2; rm -f /tmp/render-skips.$$; exit 1 ;;
        esac
    done < /tmp/render-skips.$$
    rm -f /tmp/render-skips.$$
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
        python3 -c 'import sys,yaml; d=yaml.safe_load(open(sys.argv[1])) or {}; c=d.get("_cluster") or {}; bad=[k for k,v in c.items() if isinstance(v,bool)]; sys.exit(f"{sys.argv[1]}: _cluster keys are YAML booleans, the platform injects strings: {bad}") if bad else None; sys.exit(f"{sys.argv[1]}: no _cluster map") if not c else None' "$f"
    done
}

# Each fixture is one cluster state and they must actually differ, or the sweep
# renders the same shape three times and reports triple the coverage it has.
@test "the fixtures represent different cluster states" {
    count=$(find hack/testdata/render-fixtures -name '*.yaml' | wc -l | tr -d ' ')
    [ "$count" -ge 2 ]
    distinct=$(for f in hack/testdata/render-fixtures/*.yaml; do
        grep -E '^  (oidc-enabled|solver|wildcard-issue):' "$f" | tr -d ' ' | sort | tr '\n' ','
        echo
    done | sort -u | wc -l | tr -d ' ')
    if [ "$distinct" -lt "$count" ]; then
        echo "fixtures do not differ on the keys charts branch on ($distinct distinct of $count files)" >&2
        exit 1
    fi
}
