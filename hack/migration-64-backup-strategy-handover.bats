#!/usr/bin/env bats
# Platform migration 64 hands the objects of the single backupstrategy-controller
# Helm release over to the per-strategy releases before the charts change. A
# migration runs once and is never repeated, and what this one fails to protect
# is deleted by the upgrade that follows, so these cases run the real script
# against a kubectl that serves a cluster described as files
# (hack/testdata/migration-64/kubectl) and assert on the order of what it
# changes. The steps of the handover are tested where they are maintained, in
# github.com/cozystack/backup-strategies.
#
# CI runs this file under Bats through `make bats-unit-tests`. It stays
# compatible with the `hack/cozytest.sh` translator, so it avoids `run`,
# `$status` and setup().

load test_helper

MIG="$PWD/packages/core/platform/images/migrations/migrations/64"
GROUP=strategy.backups.cozystack.io

cluster() {
  FAKE_STATE=$(mktemp -d)
  export FAKE_STATE
  mkdir -p "$FAKE_STATE/bin" "$FAKE_STATE/owner" "$FAKE_STATE/objects"
  cp "$PWD/hack/testdata/migration-64/kubectl" "$FAKE_STATE/bin/kubectl"
  PATH="$FAKE_STATE/bin:$PATH"
  export PATH
  unset KUBECTL
  : > "$FAKE_STATE/calls"
  : > "$FAKE_STATE/release"
  echo '["--leader-elect"]' > "$FAKE_STATE/deployment-args"
  printf '%s\n' "cnpgs.$GROUP" backupclasses.backups.cozystack.io > "$FAKE_STATE/crds"
  echo backupstrategy-controller > "$FAKE_STATE/owner/customresourcedefinition__cnpgs.$GROUP"
  echo backupstrategy-controller > "$FAKE_STATE/owner/backupclasses.backups.cozystack.io__cozy-default"
  echo '[{"application":{"kind":"Postgres"},"strategyRef":{"kind":"CNPG","name":"cozy-default-cnpg"}}]' \
    > "$FAKE_STATE/class-strategies"
}

@test "migration 64 protects the strategy CRDs, then stamps version 65" {
  cluster
  sh "$MIG"
  grep -F "customresourcedefinition cnpgs.$GROUP" "$FAKE_STATE/calls" | grep -F -q "helm.sh/resource-policy=keep"
  [ "$(tail -n 1 "$FAKE_STATE/calls")" = stamp ]
  [ "$(yq '.data.version' "$FAKE_STATE/stamped")" = 65 ]
}

@test "migration 64 stamps version 65 on a cluster that never had the single release" {
  cluster
  rm "$FAKE_STATE/release"
  sh "$MIG"
  [ "$(cat "$FAKE_STATE/calls")" = stamp ]
}

@test "migration 64 does not stamp when the handover fails" {
  cluster
  : > "$FAKE_STATE/fail-annotate"
  if sh "$MIG"; then
    echo "FAIL: the migration reported success with a CRD left unprotected"
    false
  fi
  if grep -q stamp "$FAKE_STATE/calls"; then
    echo "FAIL: the version was stamped past a failed handover"
    false
  fi
}
