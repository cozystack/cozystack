#!/usr/bin/env bats
# Unit test: on a fresh install, packages/apps/postgres hands CNPG the same
# users.app password it stores in <release>-credentials.
#
# The password is generated at render time, so the two Secrets can only be
# compared within one render. helm-unittest asserts each document against a
# fixed value and has no way to compare one document with another, so a chart
# generating two independent passwords would still pass the chart's own suite.

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
CHART="$REPO_ROOT/packages/apps/postgres"

@test "a fresh users.app gets one generated password in both Secrets" {
  rendered="$BATS_TEST_TMPDIR/init-script.yaml"
  helm template pg "$CHART" --namespace tenant-test \
    --set-json '_cluster={"scheduling":{}}' --set-json 'users={"app":{}}' \
    --show-only templates/init-script.yaml > "$rendered"

  app=$(yq -e 'select(.kind == "Secret" and .metadata.name == "pg-credentials").stringData.app' "$rendered")
  owner=$(yq -e 'select(.kind == "Secret" and .metadata.name == "pg-app-owner").stringData.password' "$rendered")
  [ -n "$app" ] || { echo "no generated password for app in pg-credentials" >&2; return 1; }
  [ "$app" = "$owner" ] || { printf 'pg-credentials app: %s\npg-app-owner password: %s\n' "$app" "$owner" >&2; return 1; }
}
