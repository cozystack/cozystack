#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for hack/select-e2e.sh
#
# cozytest.sh's awk parser recognizes only @test blocks and a bare `}` on its
# own line; there is no bats `run` or `$status`. Each test runs as a shell
# function under `set -eu -x`, so assertions are direct shell tests that exit
# non-zero on failure. setup()/teardown() are not honored — each test creates
# and cleans its own scratch dir.
#
# "Full suite" is asserted as an exact match against the suite list derived the
# same way select-e2e.sh derives it, not as a count: a threshold like `-gt 5`
# passes just as happily on a wrong set that happens to be large, and did hide
# two suites that the graph could not reach.
#
# Run with: hack/cozytest.sh hack/select-e2e_test.bats
# -----------------------------------------------------------------------------

@test "single app diff selects only that suite" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    echo "packages/apps/postgres/values.yaml" > "$tmp/diff"
    output=$(hack/select-e2e.sh "$tmp/diff" "$tmp/sources")
    [ "$output" = "postgres" ]
}

@test "engine-dependency change does not fan out via the ordering edge" {
    # cert-manager is a dependency of cozystack-engine, and every app declares
    # dependsOn cozystack-engine purely as an INSTALL-ORDERING edge (the app's
    # *-rd HelmRelease waits for the ApplicationDefinition CRD). That edge must
    # not propagate test selection: a cert-manager change selects only its
    # genuine direct dependents (postgres, harbor, ...), never unrelated apps
    # like kafka that reach cert-manager solely through the engine.
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    echo "packages/system/cert-manager/values.yaml" > "$tmp/diff"
    output=$(hack/select-e2e.sh "$tmp/diff" "$tmp/sources")
    echo "$output" | grep -wq postgres
    echo "$output" | grep -wq harbor
    if echo "$output" | grep -wq kafka; then
        echo "cert-manager change must not fan out via engine; got: $output" >&2
        exit 1
    fi
}

@test "networking change triggers full suite" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    echo "packages/system/cilium/values.yaml" > "$tmp/diff"
    output=$(hack/select-e2e.sh "$tmp/diff" "$tmp/sources")
    full=$(find hack/e2e-chainsaw -mindepth 2 -maxdepth 2 -name chainsaw-test.yaml | sed -e 's,^hack/e2e-chainsaw/,,' -e 's,/chainsaw-test\.yaml$,,' | sort | paste -sd ' ' -)
    [ "$output" = "$full" ]
}

@test "library change triggers full suite" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    echo "packages/library/cozy-lib/templates/_helpers.tpl" > "$tmp/diff"
    output=$(hack/select-e2e.sh "$tmp/diff" "$tmp/sources")
    full=$(find hack/e2e-chainsaw -mindepth 2 -maxdepth 2 -name chainsaw-test.yaml | sed -e 's,^hack/e2e-chainsaw/,,' -e 's,/chainsaw-test\.yaml$,,' | sort | paste -sd ' ' -)
    [ "$output" = "$full" ]
}

@test "docs-only diff selects nothing" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    echo "docs/README.md" > "$tmp/diff"
    output=$(hack/select-e2e.sh "$tmp/diff" "$tmp/sources")
    [ -z "$output" ]
}

@test "kubernetes-application maps to the four kubernetes suites" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    echo "packages/apps/kubernetes/values.yaml" > "$tmp/diff"
    output=$(hack/select-e2e.sh "$tmp/diff" "$tmp/sources")
    echo "$output" | grep -q "kubernetes-latest"
    echo "$output" | grep -q "kubernetes-previous"
    # The OIDC render-side suites exercise the same kubernetes app chart, so a
    # chart-only change must select them too.
    echo "$output" | grep -q "kubernetes-oidc-system"
    echo "$output" | grep -q "kubernetes-oidc-customconfig"
}

@test "dashboards-only diff selects nothing (path is plural)" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    echo "dashboards/gpu/gpu-fleet.json" > "$tmp/diff"
    output=$(hack/select-e2e.sh "$tmp/diff" "$tmp/sources")
    [ -z "$output" ]
}

@test "shared E2E helper script triggers full suite" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    echo "hack/e2e-chainsaw/_lib/run-kubernetes.sh" > "$tmp/diff"
    output=$(hack/select-e2e.sh "$tmp/diff" "$tmp/sources")
    full=$(find hack/e2e-chainsaw -mindepth 2 -maxdepth 2 -name chainsaw-test.yaml | sed -e 's,^hack/e2e-chainsaw/,,' -e 's,/chainsaw-test\.yaml$,,' | sort | paste -sd ' ' -)
    [ "$output" = "$full" ]
}

@test "chainsaw config change triggers full suite" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    echo "hack/e2e-chainsaw/.chainsaw.yaml" > "$tmp/diff"
    output=$(hack/select-e2e.sh "$tmp/diff" "$tmp/sources")
    full=$(find hack/e2e-chainsaw -mindepth 2 -maxdepth 2 -name chainsaw-test.yaml | sed -e 's,^hack/e2e-chainsaw/,,' -e 's,/chainsaw-test\.yaml$,,' | sort | paste -sd ' ' -)
    [ "$output" = "$full" ]
}

@test "install bats triggers full suite" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    echo "hack/e2e-install-cozystack.bats" > "$tmp/diff"
    output=$(hack/select-e2e.sh "$tmp/diff" "$tmp/sources")
    full=$(find hack/e2e-chainsaw -mindepth 2 -maxdepth 2 -name chainsaw-test.yaml | sed -e 's,^hack/e2e-chainsaw/,,' -e 's,/chainsaw-test\.yaml$,,' | sort | paste -sd ' ' -)
    [ "$output" = "$full" ]
}

@test "per-suite edit selects only that suite, never escalates" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    echo "hack/e2e-chainsaw/redis/chainsaw-test.yaml" > "$tmp/diff"
    output=$(hack/select-e2e.sh "$tmp/diff" "$tmp/sources")
    [ "$output" = "redis" ]
}

@test "pull-requests workflow change triggers full suite" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    echo ".github/workflows/pull-requests.yaml" > "$tmp/diff"
    output=$(hack/select-e2e.sh "$tmp/diff" "$tmp/sources")
    full=$(find hack/e2e-chainsaw -mindepth 2 -maxdepth 2 -name chainsaw-test.yaml | sed -e 's,^hack/e2e-chainsaw/,,' -e 's,/chainsaw-test\.yaml$,,' | sort | paste -sd ' ' -)
    [ "$output" = "$full" ]
}

@test "backup example harness edit selects its app suite" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    echo "examples/backups/postgres/run-all.sh" > "$tmp/diff"
    output=$(hack/select-e2e.sh "$tmp/diff" "$tmp/sources")
    [ "$output" = "postgres" ]
}

@test "backup example without a matching suite selects nothing" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    echo "examples/backups/no-such-app/run.sh" > "$tmp/diff"
    output=$(hack/select-e2e.sh "$tmp/diff" "$tmp/sources") || true
    [ -z "$output" ]
}

@test "a package covered by no suite escalates despite other selections" {
    # cozystack-basics is in the graph but reaches no runnable Chainsaw suite,
    # so a change to it must run everything. That escalation belongs to the
    # changed path, not to the shape of the rest of the diff: adding an
    # unrelated per-suite Chainsaw edit — the ordinary "change a platform
    # component and adjust e2e nearby" PR — must not narrow the run down to
    # that one suite.
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    # Guard the premise: if the package ever leaves the graph it escalates as an
    # unrecognised packages/ path instead, and this test would pass vacuously.
    grep -rq 'path: system/cozystack-basics' "$tmp/sources"
    full=$(find hack/e2e-chainsaw -mindepth 2 -maxdepth 2 -name chainsaw-test.yaml | sed -e 's,^hack/e2e-chainsaw/,,' -e 's,/chainsaw-test\.yaml$,,' | sort | paste -sd ' ' -)
    echo "packages/system/cozystack-basics/templates/ingress-hostname-policy.yaml" > "$tmp/diff"
    alone=$(hack/select-e2e.sh "$tmp/diff" "$tmp/sources")
    [ "$alone" = "$full" ]
    echo "hack/e2e-chainsaw/postgres/chainsaw-test.yaml" >> "$tmp/diff"
    output=$(hack/select-e2e.sh "$tmp/diff" "$tmp/sources")
    if [ "$output" != "$full" ]; then
        echo "per-suite edit swallowed the full-suite escalation; got: $output" >&2
        exit 1
    fi
}

@test "escalation also survives another package that does select suites" {
    # The swallowing is not specific to Chainsaw edits: any other changed path
    # contributing a suite name used to hide it, a second packages/ path
    # included.
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    full=$(find hack/e2e-chainsaw -mindepth 2 -maxdepth 2 -name chainsaw-test.yaml | sed -e 's,^hack/e2e-chainsaw/,,' -e 's,/chainsaw-test\.yaml$,,' | sort | paste -sd ' ' -)
    printf 'packages/system/cozystack-basics/templates/ingress-hostname-policy.yaml\npackages/apps/postgres/values.yaml\n' > "$tmp/diff"
    output=$(hack/select-e2e.sh "$tmp/diff" "$tmp/sources")
    [ "$output" = "$full" ]
}

@test "two covered packages still narrow to their own suites" {
    # The escalation loop is the code that can over-escalate; pin the negative
    # direction so a path that is covered never drags the full suite in.
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    printf 'packages/apps/postgres/values.yaml\npackages/apps/redis/values.yaml\n' > "$tmp/diff"
    output=$(hack/select-e2e.sh "$tmp/diff" "$tmp/sources")
    [ "$output" = "postgres redis" ]
}

@test "a path owned by several sources counts as covered if any one is" {
    # system/postgres-operator belongs to two PackageSources: cozystack.monitoring,
    # which reaches no runnable suite, and cozystack.postgres-operator, which
    # reaches postgres-application and harbor-application (Harbor uses postgres as
    # its backing DB). Coverage is decided over the path's sources together, so
    # this must narrow rather than escalate on the monitoring half — and it must
    # not fan out to unrelated suites like kafka either.
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    # Guard the premise. Filter outside yq: a trailing `| $n` re-emits the
    # binding whether or not the select() matched, so counting that way returns
    # every source in the graph and can never fail. Route the yq output through
    # `echo "$VAR" | awk`, the same as select-e2e.sh's own owners split: older
    # mikefarah yq emits `"\t"` as a literal backslash-t, and `echo` is what
    # turns it into a real tab, so `awk -F'\t'` splits on every yq version.
    owners_tsv=$(yq -rN '.metadata.name as $n | .spec.variants[]?.components[]?.path | select(. != null) | . + "\t" + $n' "$tmp/sources"/*.yaml)
    owners=$(echo "$owners_tsv" | awk -F'\t' '$1=="system/postgres-operator"{print $2}' | sort -u | wc -l)
    [ "$owners" -ge 2 ]
    echo "packages/system/postgres-operator/values.yaml" > "$tmp/diff"
    output=$(hack/select-e2e.sh "$tmp/diff" "$tmp/sources")
    [ "$output" = "harbor postgres" ]
}

@test "an edit to a switched-off suite is ignored" {
    # hack/e2e-chainsaw/backup/ holds chainsaw-test.yaml.disabled, so it defines
    # no runnable suite. Such an edit cannot change what the run tests and must
    # select nothing on its own — and must not distort what the rest of the diff
    # selects either.
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    [ ! -f hack/e2e-chainsaw/backup/chainsaw-test.yaml ]
    [ -f hack/e2e-chainsaw/backup/chainsaw-test.yaml.disabled ]
    echo "hack/e2e-chainsaw/backup/chainsaw-test.yaml.disabled" > "$tmp/diff"
    output=$(hack/select-e2e.sh "$tmp/diff" "$tmp/sources")
    [ -z "$output" ]
    echo "packages/apps/postgres/values.yaml" >> "$tmp/diff"
    output=$(hack/select-e2e.sh "$tmp/diff" "$tmp/sources")
    [ "$output" = "postgres" ]
}

@test "an edit to a non-suite directory under e2e-chainsaw escalates" {
    # Only a switched-off suite is ignorable. Shared material next to _lib/, or
    # a suite nested deeper than the depth-2 scan looks, is invisible to
    # all_apps but runs under Chainsaw's recursive discovery — selecting
    # nothing for it would skip E2E outright.
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    full=$(find hack/e2e-chainsaw -mindepth 2 -maxdepth 2 -name chainsaw-test.yaml | sed -e 's,^hack/e2e-chainsaw/,,' -e 's,/chainsaw-test\.yaml$,,' | sort | paste -sd ' ' -)
    [ ! -d hack/e2e-chainsaw/_fixtures ]
    echo "hack/e2e-chainsaw/_fixtures/tenant.yaml" > "$tmp/diff"
    output=$(hack/select-e2e.sh "$tmp/diff" "$tmp/sources")
    [ "$output" = "$full" ]
}

@test "a suite owned by a non-application source is reachable from its package" {
    # kuberture and securitygroup are owned by sources that are not named
    # *-application, and external-dns by one that carries the suite name itself.
    # All three map through src_to_suites, so a change to their package selects
    # that suite instead of escalating the whole run.
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    echo "packages/system/kuberture/values.yaml" > "$tmp/diff"
    output=$(hack/select-e2e.sh "$tmp/diff" "$tmp/sources")
    [ "$output" = "kuberture" ]
    echo "packages/system/securitygroup-controller/values.yaml" > "$tmp/diff"
    output=$(hack/select-e2e.sh "$tmp/diff" "$tmp/sources")
    [ "$output" = "securitygroup" ]
    echo "packages/system/external-dns/values.yaml" > "$tmp/diff"
    output=$(hack/select-e2e.sh "$tmp/diff" "$tmp/sources")
    [ "$output" = "external-dns" ]
}

@test "every suite round-trips between the two mapping tables" {
    # select-install.sh maps a suite to the PackageSource that installs it;
    # select-e2e.sh must map that source back to the suite. A suite that does
    # not round-trip is unreachable from its own package, so every change to it
    # escalates to the full run — which is how the kuberture and securitygroup
    # entries came to be missing. Pinning the property rather than those two
    # names is what stops the next off-convention source repeating it.
    eval "$(sed -n '/^src_to_suites()/,/^}/p' hack/select-e2e.sh)"
    eval "$(sed -n '/^suite_to_source()/,/^}/p' hack/select-install.sh)"
    NODES=$(yq -rN '.metadata.name' packages/core/platform/sources/*.yaml | sort -u)
    for suite in $(find hack/e2e-chainsaw -mindepth 2 -maxdepth 2 -name chainsaw-test.yaml | sed -e 's,^hack/e2e-chainsaw/,,' -e 's,/chainsaw-test\.yaml$,,' | sort); do
        src=$(suite_to_source "$suite")
        if [ -z "$src" ]; then
            echo "suite '$suite' has no source in select-install.sh's suite_to_source" >&2
            exit 1
        fi
        if ! src_to_suites "${src#cozystack.}" | tr ' ' '\n' | grep -Fxq "$suite"; then
            echo "suite '$suite' maps to $src, which select-e2e.sh maps back to '$(src_to_suites "${src#cozystack.}")'" >&2
            exit 1
        fi
    done
}

@test "an empty suite tree or source graph fails instead of selecting nothing" {
    # Every escalation prints the suite list, and CI reads empty output as
    # "skip E2E". A discovery that comes up empty would therefore turn each
    # fail-safe into its opposite, silently, behind a green run.
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    mkdir -p "$tmp/nosuites" "$tmp/nosources"
    cp hack/select-e2e.sh "$tmp/nosuites/"
    echo "packages/library/cozy-lib/templates/_helpers.tpl" > "$tmp/diff"
    rc=0
    ( cd "$tmp/nosuites" && sh ./select-e2e.sh "$tmp/diff" "$tmp/sources" ) >/dev/null 2>&1 || rc=$?
    [ "$rc" -ne 0 ]
    rc=0
    hack/select-e2e.sh "$tmp/diff" "$tmp/nosources" >/dev/null 2>&1 || rc=$?
    [ "$rc" -ne 0 ]
}

@test "resolve_suites still has exactly one call site" {
    # resolve_suites writes its working variables into the caller's scope
    # (POSIX sh, no local). That is safe only because its single call sits
    # inside $( ). A second call added without noticing would clobber state
    # silently, so the count guards the variable-scoping note above the
    # function. 2 = one definition plus one call.
    count=$(grep -c 'resolve_suites' hack/select-e2e.sh)
    [ "$count" -eq 2 ]
}
