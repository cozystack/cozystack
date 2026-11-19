#!/usr/bin/env bats
# Unit test: the template-side allowlist for postgresql.sharedPreloadLibraries
# in packages/apps/postgres/templates/db.yaml.
#
# The enum in values.schema.json rejects a bad name before rendering starts, and
# helm-unittest always validates the schema, so the chart's own suite never
# reaches the template check behind it. Rendering with --skip-schema-validation
# is the only way to exercise that backstop, and it is the case the backstop
# exists for.

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
CHART="$REPO_ROOT/packages/apps/postgres"

render_without_schema() {
  helm template pg "$CHART" --namespace tenant-test --skip-schema-validation \
    --set _cluster.cluster-domain=cozy.local \
    --set "postgresql.sharedPreloadLibraries[0]=$1" 2>&1
}

@test "an allowed name renders when the schema is skipped" {
  # Without this, the rejections below could pass on an unrelated render error.
  out=$(render_without_schema pg_stat_statements) || { echo "$out" >&2; return 1; }
  printf '%s\n' "$out" | grep -q 'shared_preload_libraries:'
}

@test "the template rejects a name outside the allowlist when the schema is skipped" {
  if out=$(render_without_schema pg_cron); then
    echo "render succeeded with pg_cron preloaded" >&2
    return 1
  fi
  printf '%s\n' "$out" | grep -qF 'SECURITY: shared preload library "pg_cron" is not allowed.'
}

@test "the template rejects a library path when the schema is skipped" {
  if out=$(render_without_schema /var/lib/postgresql/data/pgdata/evil.so); then
    echo "render succeeded with a library path preloaded" >&2
    return 1
  fi
  printf '%s\n' "$out" | grep -qF 'SECURITY: shared preload library "/var/lib/postgresql/data/pgdata/evil.so" is not allowed.'
}

@test "the template allowlist matches the schema enum" {
  # The template names its list in the rejection message; the two lists drifting
  # apart would leave one guard accepting what the other refuses.
  out=$(render_without_schema pg_cron) && return 1
  template=$(printf '%s\n' "$out" | sed -n 's/.* Allowed: \([^.]*\)\..*/\1/p' | tr -d ' ' | tr ',' '\n' | sort)
  schema=$(jq -r '.properties.postgresql.properties.sharedPreloadLibraries.items.enum[]' "$CHART/values.schema.json" | sort)
  [ -n "$template" ] || { echo "no allowlist found in: $out" >&2; return 1; }
  [ "$template" = "$schema" ] || { printf 'template:\n%s\nschema:\n%s\n' "$template" "$schema" >&2; return 1; }
}
