#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Consistency between .github/workflows/pr-labeler.yaml and .github/labels.yml.
#
# The labeler applies its result with issues.addLabels. What that call does with
# a label the repository has not declared is not documented for this endpoint,
# and the test does not need it to be: either it creates one with a default
# colour and no description, and the failure is a stray label nobody declared
# that nothing reports, or it errors and the job goes red on every PR whose
# title happens to hit that scope. The first is silent and the second is loud,
# both are bad, and both are what this test prevents.
# .github/workflows/labels.yaml validates labels.yml on its own but never reads
# the workflow, and its path filter names labels.yml and itself, so an edit to
# the map alone does not even run it.
#
# The second test guards the other direction for CRD-bundle packages: a new one
# added without a scope key falls back to area/uncategorized. The labeler does
# emit a core.warning naming the unmapped scope, so the fallback is not silent
# -- but it only fires once someone opens such a PR, whereas this test fires
# when the package is added, which is when it is still cheap to fix.
#
# The labeler is evaluated rather than pattern-matched. A pattern over the
# workflow source answers "is this spelling present", and the same break spelled
# differently walks past it — a double-quoted key, a space before the colon, a
# value built by concatenation, a key repeated lower down that wins at runtime,
# an object parked where nothing consults it. All five were demonstrated against
# a pattern-based version of these tests. hack/pr-labeler-probe.js runs the step
# in node and reports what it would apply, which removes the class rather than
# the instances.
#
# node and python3 are therefore required, and a missing one fails loudly rather
# than skipping, on the precedent hack/md-no-hardwrap.bats sets for its own
# interpreter: a green suite that ran nothing is worse than a red one.
#
# cozytest.sh's awk parser recognizes @test blocks and rewrites a bare `}` on its
# own line; every other line it emits verbatim, so file-scope helpers work while
# bats `setup`/`teardown` are never called. There is no `run` or `$status`.
# Assertions are direct shell tests, and one that must hold when a command
# SUCCEEDS is written as `if ... exit 1` rather than `! cmd`: errexit is
# suppressed for a `!`-inverted command, for an `if`/`while` condition, and for a
# command that is not the LAST in an `&&`/`||` list — the last one fires
# normally, so `true && false` does abort.
#
# Run with: hack/cozytest.sh hack/pr-labeler-areas.bats
# -----------------------------------------------------------------------------

have_node() {
  command -v node >/dev/null 2>&1 && return 0
  echo "node is required to evaluate .github/workflows/pr-labeler.yaml" >&2
  return 1
}

have_python3() {
  command -v python3 >/dev/null 2>&1 && return 0
  echo "python3 is required to read .github/workflows/pr-labeler.yaml" >&2
  return 1
}

# Extract the github-script body, evaluate it, leave the JSON report in $1.
labeler_report() {
  python3 -c 'import yaml;w=yaml.safe_load(open(".github/workflows/pr-labeler.yaml"));print([st["with"]["script"] for j in w["jobs"].values() for st in j["steps"] if "script" in st.get("with",{})][0])' >"$1/labeler.js"
  node hack/pr-labeler-probe.js "$1/labeler.js" >"$1/report.json"
}

@test "every label the PR labeler can apply is declared in .github/labels.yml" {
    have_node
    have_python3
    WORK=$(mktemp -d)
    labeler_report "$WORK"

    # Every declared label, and every label the step actually reaches. No
    # namespace filter on either side: kind/* travels through the same addLabels
    # call as area/*, and so would anything else somebody adds.
    declared=$(grep -E '^- name: ' .github/labels.yml | sed 's/^- name: //' | tr -d "'\"" | sort -u)
    used=$(sed -n '/"labels":/,/\]/p' "$WORK/report.json" | sed -nE 's/^ *"([^"]+)",?$/\1/p' | sort -u)

    # Neither side may be empty: an extraction that silently stops matching
    # would turn this test into a vacuous pass.
    if [ -z "$declared" ] || [ -z "$used" ]; then
        echo "extraction produced nothing — declared=[$declared] used=[$used]" >&2
        exit 1
    fi

    missing=""
    for a in $used; do
        printf '%s\n' "$declared" | grep -qx "$a" || missing="$missing $a"
    done

    if [ -n "$missing" ]; then
        echo "pr-labeler.yaml can apply labels that labels.yml does not declare:$missing" >&2
        echo "addLabels either creates each one undeclared or fails the job" >&2
        exit 1
    fi

    rm -rf "$WORK"
}

@test "every CRD-bundle package under packages/system has a scope in the labeler map" {
    # application-definition-crd is deliberately absent from the map. Its CRD is
    # cozystack's own API surface rather than a vendored upstream bundle, its
    # changes ship under the `api` scope, and `api` already maps to area/api.
    # Grouping it by packaging shape would cost accuracy for uniformity.
    have_node
    have_python3
    WORK=$(mktemp -d)
    labeler_report "$WORK"

    found=0
    missing=""

    for d in packages/system/*-crd packages/system/*-crds; do
        [ -d "$d" ] || continue
        n=$(basename "$d")
        case "$n" in
            application-definition-crd) continue ;;
        esac
        found=$((found + 1))
        # The value the scope RESOLVES to, so a key that is present but
        # unreachable does not count as mapped: parked in an object nothing
        # consults, commented out, or overridden by a duplicate lower down.
        grep -qE "^ *\"$n\": \"area/crds\",?\$" "$WORK/report.json" || missing="$missing $n"
    done

    if [ "$found" -eq 0 ]; then
        echo "no *-crd/*-crds packages matched under packages/system — glob broke" >&2
        exit 1
    fi

    if [ -n "$missing" ]; then
        echo "CRD-bundle packages whose scope does not resolve to area/crds:$missing" >&2
        echo "a PR scoped to one of them falls back to area/uncategorized." >&2
        echo "Add the key, or -- if the package ships cozystack's own API rather" >&2
        echo "than a vendored bundle -- add it to the skip list above instead." >&2
        exit 1
    fi

    rm -rf "$WORK"
}
