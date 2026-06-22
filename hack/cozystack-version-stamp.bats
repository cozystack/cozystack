#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for
# packages/core/platform/images/migrations/migrations/lib/cozystack-version.sh
#
# render_cozystack_version_manifest <version> emits the cozystack-version
# ConfigMap manifest on stdout, always carrying the
# platform.cozystack.io/no-delete=true label that the
# cozystack-no-delete-guardrail ValidatingAdmissionPolicy keys on. Migrations
# apply it under the default kubectl field manager; a label-less apply by that
# same manager would strip the label, so the manifest is centralized here and
# every migration sources it instead of copy-pasting a heredoc.
#
# cozytest.sh's awk parser recognizes only @test blocks and a bare `}` on its
# own line; there is no bats `run` or `$status`. Assertions are expressed as
# direct shell tests that exit non-zero on failure. Each test runs in its own
# subshell, so NAMESPACE set/unset in one test does not leak into another.
#
# Run with: hack/cozytest.sh hack/cozystack-version-stamp.bats
# -----------------------------------------------------------------------------

@test "renders parseable YAML for a version" {
    . packages/core/platform/images/migrations/migrations/lib/cozystack-version.sh
    render_cozystack_version_manifest 45 | yq . >/dev/null
}

@test "carries the no-delete label set to true" {
    . packages/core/platform/images/migrations/migrations/lib/cozystack-version.sh
    val=$(render_cozystack_version_manifest 45 \
      | yq -r '.metadata.labels."platform.cozystack.io/no-delete"')
    [ "$val" = "true" ]
}

@test "names the ConfigMap cozystack-version" {
    . packages/core/platform/images/migrations/migrations/lib/cozystack-version.sh
    name=$(render_cozystack_version_manifest 45 | yq -r '.metadata.name')
    [ "$name" = "cozystack-version" ]
}

@test "stamps the requested version as a quoted string" {
    # The version MUST render as a quoted YAML string; an unquoted numeric
    # scalar (version: 45) would change the ConfigMap data type the rest of
    # the platform reads as a string.
    . packages/core/platform/images/migrations/migrations/lib/cozystack-version.sh
    out=$(render_cozystack_version_manifest 45)
    printf '%s\n' "$out" | grep -qx '  version: "45"'
    v=$(printf '%s\n' "$out" | yq -r '.data.version')
    [ "$v" = "45" ]
}

@test "defaults namespace to cozy-system when NAMESPACE is unset" {
    unset NAMESPACE
    . packages/core/platform/images/migrations/migrations/lib/cozystack-version.sh
    ns=$(render_cozystack_version_manifest 45 | yq -r '.metadata.namespace')
    [ "$ns" = "cozy-system" ]
}

@test "honors a NAMESPACE override" {
    . packages/core/platform/images/migrations/migrations/lib/cozystack-version.sh
    out=$(export NAMESPACE=cozy-other; render_cozystack_version_manifest 45)
    ns=$(printf '%s\n' "$out" | yq -r '.metadata.namespace')
    [ "$ns" = "cozy-other" ]
}

@test "requires a version argument" {
    . packages/core/platform/images/migrations/migrations/lib/cozystack-version.sh
    if ( render_cozystack_version_manifest ) 2>/dev/null; then
        echo "expected non-zero exit when version arg is missing" >&2
        exit 1
    fi
}

@test "stamp_cozystack_version requires a version argument" {
    # The guard fires before the kubectl pipe, so this needs no cluster: a
    # missing version must abort rather than apply empty input to kubectl.
    . packages/core/platform/images/migrations/migrations/lib/cozystack-version.sh
    if ( stamp_cozystack_version ) 2>/dev/null; then
        echo "expected non-zero exit when version arg is missing" >&2
        exit 1
    fi
}

@test "render output matches the canonical labeled manifest" {
    # Golden test: pins the exact bytes migrations 42/43/44 apply today, so the
    # refactor that routes them through this helper is provably manifest-stable.
    unset NAMESPACE
    . packages/core/platform/images/migrations/migrations/lib/cozystack-version.sh
    expected=$(cat <<'EOF'
apiVersion: v1
kind: ConfigMap
metadata:
  name: cozystack-version
  namespace: cozy-system
  labels:
    platform.cozystack.io/no-delete: "true"
data:
  version: "45"
EOF
)
    actual=$(render_cozystack_version_manifest 45)
    if [ "$actual" != "$expected" ]; then
        printf 'expected:\n%s\n---\nactual:\n%s\n' "$expected" "$actual" >&2
        exit 1
    fi
}
