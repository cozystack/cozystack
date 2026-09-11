#!/usr/bin/env bats

# Shared by bats and cozytest.sh: inspect exit codes directly, without bats-only
# helpers. Scratch directories stay available if an assertion fails.

make_render_chart() {
    mkdir -p "$1/templates"
    printf 'apiVersion: v2\nname: render-test\nversion: 0.0.0\n' > "$1/Chart.yaml"
    printf 'apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: render-test\n' > "$1/templates/object.yaml"
}

expect_render_failure() {
    if output=$(dash hack/check-render-matrix.sh "$@" 2>&1); then
        echo "expected render failure, got: $output" >&2
        return 1
    fi
    printf '%s\n' "$output"
}

@test "rendered plus skipped accounts for every app chart" {
    total=$(find packages/apps -mindepth 1 -maxdepth 1 -type d | wc -l | tr -d ' ')
    output=$(dash hack/check-render-matrix.sh)
    summary=$(printf '%s\n' "$output" | grep '^check-render-matrix:')
    rendered=$(printf '%s\n' "$summary" | sed 's/.*: \([0-9]*\) rendered.*/\1/')
    skipped=$(printf '%s\n' "$summary" | sed 's/.*, \([0-9]*\) skipped.*/\1/')
    [ "$rendered" -gt 0 ]
    [ "$((rendered + skipped))" -eq "$total" ]
}

@test "render matrix preserves multiple chart paths containing spaces" {
    tmp=$(mktemp -d)
    make_render_chart "$tmp/space in parent/first"
    make_render_chart "$tmp/space in parent/second"
    output=$(dash hack/check-render-matrix.sh "$tmp/space in parent/first/" "$tmp/space in parent/second")
    printf '%s\n' "$output" | grep -Fx 'check-render-matrix: 2 rendered, 0 skipped'
    rm -rf "$tmp"
}

@test "render matrix reports a broken chart and still checks the next chart" {
    tmp=$(mktemp -d)
    make_render_chart "$tmp/broken"
    make_render_chart "$tmp/valid"
    printf '{{ required "deliberately broken" .Values.nope }}\n' > "$tmp/broken/templates/object.yaml"
    output=$(expect_render_failure "$tmp/broken" "$tmp/valid")
    for fixture in hack/testdata/render-fixtures/*.yaml; do
        state=$(basename "$fixture" .yaml)
        printf '%s\n' "$output" | grep -Fx "FAIL broken ($state)"
    done
    printf '%s\n' "$output" | grep -F 'deliberately broken'
    printf '%s\n' "$output" | grep -Fx 'check-render-matrix: 1 rendered, 0 skipped'
    rm -rf "$tmp"
}

@test "render matrix fails on malformed YAML" {
    tmp=$(mktemp -d)
    make_render_chart "$tmp/badyaml"
    printf 'a: b\n  c: d\n' > "$tmp/badyaml/templates/object.yaml"
    output=$(expect_render_failure "$tmp/badyaml")
    printf '%s\n' "$output" | grep -F 'FAIL badyaml'
    printf '%s\n' "$output" | grep -F 'YAML parse error'
    rm -rf "$tmp"
}

@test "render matrix does not count a nonexistent chart as rendered" {
    tmp=$(mktemp -d)
    output=$(expect_render_failure "$tmp/missing")
    printf '%s\n' "$output" | grep -F 'FAIL missing'
    printf '%s\n' "$output" | grep -Fx 'check-render-matrix: 0 rendered, 0 skipped'
    rm -rf "$tmp"
}

@test "render matrix rejects skips that now render or fail for another reason" {
    tmp=$(mktemp -d)
    for name in vm-instance kubernetes-nodes; do
        make_render_chart "$tmp/$name"
        output=$(expect_render_failure "$tmp/$name")
        printf '%s\n' "$output" | grep -F 'chart now renders; remove its skip'
        printf '{{ fail "unrelated render failure" }}\n' > "$tmp/$name/templates/object.yaml"
        output=$(expect_render_failure "$tmp/$name")
        printf '%s\n' "$output" | grep -F 'unrelated render failure'
        printf '%s\n' "$output" | grep -Fx 'check-render-matrix: 0 rendered, 0 skipped'
    done
    rm -rf "$tmp"
}

@test "fixtures match the injected value types" {
    for fixture in hack/testdata/render-fixtures/*.yaml; do
        helm template fixture-check hack/testdata/render-fixtures/validator \
            --set-file "fixture=$fixture"
    done
}

@test "fixture validation rejects wrong scalar and scheduling types" {
    tmp=$(mktemp -d)
    for value in true 12 null '[]' '{}'; do
        printf '_cluster:\n  oidc-enabled: %s\n_namespace: {host: "example.org"}\n' "$value" > "$tmp/value.yaml"
        if output=$(helm template fixture-check hack/testdata/render-fixtures/validator \
            --set-file "fixture=$tmp/value.yaml" 2>&1); then
            echo "accepted non-string oidc-enabled: $value" >&2
            exit 1
        fi
        printf '%s\n' "$output" | grep -F '_cluster.oidc-enabled must be a string'
    done
    for value in true 12 null '[]' '{}'; do
        printf '_cluster:\n  scheduling:\n    globalAppTopologySpreadConstraints: %s\n    dedicatedNodesForWindowsVMs: "false"\n_namespace: {host: "example.org"}\n' "$value" > "$tmp/value.yaml"
        if output=$(helm template fixture-check hack/testdata/render-fixtures/validator \
            --set-file "fixture=$tmp/value.yaml" 2>&1); then
            echo "accepted non-string scheduling constraint: $value" >&2
            exit 1
        fi
        printf '%s\n' "$output" | grep -F '_cluster.scheduling.globalAppTopologySpreadConstraints must be a string'
    done
    rm -rf "$tmp"
}

@test "configured scheduling reaches the postgres manifest" {
    tmp=$(mktemp -d)
    for state in fresh configured wildcard; do
        helm template postgres-render-check packages/apps/postgres -n tenant-test \
            -f "hack/testdata/render-fixtures/$state.yaml" > "$tmp/$state.yaml"
    done
    if grep -q '^  topologySpreadConstraints:' "$tmp/fresh.yaml"; then
        echo 'fresh unexpectedly enables topology spread' >&2
        exit 1
    fi
    for state in configured wildcard; do
        grep -A 7 '^  topologySpreadConstraints:' "$tmp/$state.yaml" > "$tmp/constraints"
        grep -F 'maxSkew: 1' "$tmp/constraints"
        grep -F 'topologyKey: topology.kubernetes.io/zone' "$tmp/constraints"
        grep -F 'cnpg.io/cluster: postgres-render-check' "$tmp/constraints"
    done
    rm -rf "$tmp"
}

@test "render matrix reaches capability-gated OIDC templates" {
    tmp=$(mktemp -d)
    make_render_chart "$tmp/tenant"
    cat > "$tmp/tenant/templates/object.yaml" <<'EOF'
{{- if and (eq (index .Values._cluster "oidc-enabled") "true") (.Capabilities.APIVersions.Has "v1.edp.epam.com/v1") }}
{{- fail "OIDC branch rendered" }}
{{- end }}
EOF
    output=$(expect_render_failure "$tmp/tenant")
    printf '%s\n' "$output" | grep -Fx 'FAIL tenant (configured)'
    printf '%s\n' "$output" | grep -Fx 'FAIL tenant (wildcard)'
    printf '%s\n' "$output" | grep -F 'OIDC branch rendered'
    if printf '%s\n' "$output" | grep -Fq 'FAIL tenant (fresh)'; then
        echo 'OIDC branch enabled for fresh fixture' >&2
        exit 1
    fi
    rm -rf "$tmp"
}

@test "fixture states enable OIDC groups and shared-service resources" {
    tmp=$(mktemp -d)
    for state in fresh configured wildcard; do
        helm template tenant-check packages/apps/tenant -n tenant-test \
            --api-versions v1.edp.epam.com/v1 \
            -f "hack/testdata/render-fixtures/$state.yaml" > "$tmp/tenant.yaml"
        helm template kubernetes-check packages/apps/kubernetes -n tenant-test \
            -f "hack/testdata/render-fixtures/$state.yaml" > "$tmp/kubernetes.yaml"
        groups=$(awk '/^kind: KeycloakRealmGroup$/ {n++} END {print n+0}' "$tmp/tenant.yaml")
        metrics=$(awk '/^  name: kubernetes-check-metrics-server$/ {n++} END {print n+0}' "$tmp/kubernetes.yaml")
        case "$state" in
            fresh) [ "$groups" -eq 0 ]; [ "$metrics" -eq 0 ] ;;
            *) [ "$groups" -eq 4 ]; [ "$metrics" -eq 1 ] ;;
        esac
    done
    rm -rf "$tmp"
}

@test "certificate fixtures exercise per-host ACME and wildcard ingress" {
    tmp=$(mktemp -d)
    for state in configured wildcard; do
        helm template harbor-check packages/apps/harbor -n tenant-test \
            -f "hack/testdata/render-fixtures/$state.yaml" \
            --show-only templates/ingress.yaml > "$tmp/$state.yaml"
        grep -Fx 'kind: Ingress' "$tmp/$state.yaml"
        grep -F 'harbor-check.example.org' "$tmp/$state.yaml"
    done
    grep -F 'acme.cert-manager.io/http01-ingress-ingressclassname: tenant-test' "$tmp/configured.yaml"
    grep -F 'cert-manager.io/cluster-issuer: letsencrypt-prod' "$tmp/configured.yaml"
    grep -F 'secretName: harbor-check-ingress-tls' "$tmp/configured.yaml"
    grep -Fx '  tls:' "$tmp/wildcard.yaml"
    if grep -Eq '^[[:space:]]+(acme.cert-manager.io/|cert-manager.io/cluster-issuer:|secretName:)' "$tmp/wildcard.yaml"; then
        echo 'wildcard ingress unexpectedly requests a per-host certificate' >&2
        exit 1
    fi
    rm -rf "$tmp"
}

@test "every pair of fixture states produces different deterministic manifests" {
    tmp=$(mktemp -d)
    count=0
    for fixture in hack/testdata/render-fixtures/*.yaml; do
        state=$(basename "$fixture" .yaml)
        count=$((count + 1))
        # This compares whole states; the tests above assert specific branches.
        # Render twice so randomness cannot masquerade as state coverage.
        for run in first second; do
            helm template tenant-check packages/apps/tenant -n tenant-test \
                --api-versions v1.edp.epam.com/v1 -f "$fixture" > "$tmp/$state-$run.yaml"
        done
        cmp "$tmp/$state-first.yaml" "$tmp/$state-second.yaml"
        cp "$tmp/$state-first.yaml" "$tmp/$state.manifests"
    done
    [ "$count" -ge 3 ]
    for a in "$tmp"/*.manifests; do
        for b in "$tmp"/*.manifests; do
            [ "$a" != "$b" ] || continue
            if cmp -s "$a" "$b"; then
                echo "fixture states produce identical manifests: $a and $b" >&2
                exit 1
            fi
        done
    done
    rm -rf "$tmp"
}
