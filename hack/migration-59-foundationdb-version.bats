#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for platform migration 59 --> 60 (carry a FoundationDB
# application's cluster.version over to the version field that replaces it, and
# record what cannot be carried over).
#
# The chart no longer reads cluster.version, so a cluster that set it to a line
# other than 7.3 would move to the v7.3 default on its first reconcile after the
# upgrade. These drive the real migration script against a fake kubectl
# (hack/testdata/migration-59-foundationdb/) and pin that:
#   - each release that set cluster.version on 7.1, 7.3 or 7.4 gets version for
#     that line, through a JSON patch that tests the values it read and leaves
#     cluster.version in place for the old chart;
#   - releases that never set cluster.version, already have version, or are not
#     FoundationDB are not touched;
#   - an unlabelled release is still found by its chart;
#   - a value outside those lines, a value the apiserver stored as a number, a
#     spec.values that is not an object at all, a carried release whose patch
#     moves, and an OpenSearch, Kubernetes or KubernetesNodes release still
#     setting an image override are recorded and do not block the stamp;
#   - a failed scan or patch aborts before the stamp;
#   - the tag map the migration carries matches the chart's.
#
# cozytest.sh's awk parser recognizes only @test blocks and a bare `}` on its
# own line; there is no bats `run`/`$status`/`setup`/`teardown`. Assertions are
# direct shell tests that exit non-zero on failure.
#
# Run with: hack/cozytest.sh hack/migration-59-foundationdb-version.bats
# -----------------------------------------------------------------------------

FAKEBIN="$PWD/hack/testdata/migration-59-foundationdb"
MIG="$PWD/packages/core/platform/images/migrations/migrations/59"

prep() {
  WORK=$(mktemp -d)
  export FAKE_CMDLOG="$WORK/cmdlog"
  export FAKE_HR_LIST="$WORK/hrlist.json"
  : > "$FAKE_CMDLOG"
  export PATH="$FAKEBIN:$PATH"
  export NAMESPACE=cozy-system
  unset FAKE_LIST_FAIL FAKE_PATCH_FAIL MIGRATION_DRY_RUN
  cat > "$FAKE_HR_LIST" <<'JSON'
{"items":[
  {"metadata":{"namespace":"tenant-a","name":"foundationdb-old71","labels":{"apps.cozystack.io/application.kind":"FoundationDB"}},
   "spec":{"chartRef":{"kind":"ExternalArtifact","name":"cozystack-foundationdb-application-default-foundationdb"},
           "values":{"cluster":{"version":"7.1.67","redundancyMode":"double"}}}},
  {"metadata":{"namespace":"tenant-a","name":"foundationdb-new74","labels":{"apps.cozystack.io/application.kind":"FoundationDB"}},
   "spec":{"values":{"cluster":{"version":"7.4.3"}}}},
  {"metadata":{"namespace":"tenant-b","name":"foundationdb-pinned73","labels":{"apps.cozystack.io/application.kind":"FoundationDB"}},
   "spec":{"values":{"cluster":{"version":"7.3.63"}}}},
  {"metadata":{"namespace":"tenant-b","name":"foundationdb-defaults","labels":{"apps.cozystack.io/application.kind":"FoundationDB"}},
   "spec":{"values":{"cluster":{"redundancyMode":"double"}}}},
  {"metadata":{"namespace":"tenant-b","name":"foundationdb-migrated","labels":{"apps.cozystack.io/application.kind":"FoundationDB"}},
   "spec":{"values":{"version":"v7.1","cluster":{"version":"7.1.67"}}}},
  {"metadata":{"namespace":"tenant-c","name":"foundationdb-unlabelled"},
   "spec":{"chartRef":{"kind":"ExternalArtifact","name":"cozystack-foundationdb-application-default-foundationdb"},
           "values":{"cluster":{"version":"7.1.0"}}}},
  {"metadata":{"namespace":"tenant-c","name":"postgres-db","labels":{"apps.cozystack.io/application.kind":"Postgres"}},
   "spec":{"values":{"cluster":{"version":"7.1.0"}}}},
  {"metadata":{"namespace":"tenant-d","name":"foundationdb-ancient","labels":{"apps.cozystack.io/application.kind":"FoundationDB"}},
   "spec":{"values":{"cluster":{"version":"6.3.25"}}}},
  {"metadata":{"namespace":"tenant-d","name":"foundationdb-numeric","labels":{"apps.cozystack.io/application.kind":"FoundationDB"}},
   "spec":{"values":{"cluster":{"version":7.3}}}},
  {"metadata":{"namespace":"tenant-d","name":"foundationdb-scalar-values","labels":{"apps.cozystack.io/application.kind":"FoundationDB"}},
   "spec":{"values":"broken"}},
  {"metadata":{"namespace":"tenant-e","name":"opensearch-logs","labels":{"apps.cozystack.io/application.kind":"OpenSearch"}},
   "spec":{"values":{"images":{"opensearch":"registry.example.test/opensearch:2.19.6"}}}},
  {"metadata":{"namespace":"tenant-e","name":"opensearch-plain","labels":{"apps.cozystack.io/application.kind":"OpenSearch"}},
   "spec":{"values":{"images":{"opensearch":""},"replicas":3}}},
  {"metadata":{"namespace":"tenant-f","name":"kubernetes-mirrored","labels":{"apps.cozystack.io/application.kind":"Kubernetes"}},
   "spec":{"values":{"images":{"kubectl":"","talosCsrSigner":"mirror.example.test/talos-csr-signer:1","waitForKubeconfig":"mirror.example.test/busybox:1"}}}},
  {"metadata":{"namespace":"tenant-f","name":"kubernetes-plain","labels":{"apps.cozystack.io/application.kind":"Kubernetes"}},
   "spec":{"values":{"images":{"kubectl":"","talosCsrSigner":"","waitForKubeconfig":""}}}},
  {"metadata":{"namespace":"tenant-f","name":"kubernetes-nodes-mirrored-md0"},
   "spec":{"chartRef":{"kind":"ExternalArtifact","name":"cozystack-kubernetes-nodes-application-kubevirt-kubernetes-nodes"},
           "values":{"images":{"kubectl":"mirror.example.test/kubectl:1"}}}}
]}
JSON
}

@test "carries each supported line over and leaves everything else alone" {
  prep
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]

  grep -qxF 'PATCH tenant-a foundationdb-old71 json [{"op":"test","path":"/spec/values","value":{"cluster":{"version":"7.1.67","redundancyMode":"double"}}},{"op":"add","path":"/spec/values/version","value":"v7.1"}]' "$FAKE_CMDLOG"
  grep -qxF 'PATCH tenant-a foundationdb-new74 json [{"op":"test","path":"/spec/values","value":{"cluster":{"version":"7.4.3"}}},{"op":"add","path":"/spec/values/version","value":"v7.4"}]' "$FAKE_CMDLOG"
  grep -qxF 'PATCH tenant-b foundationdb-pinned73 json [{"op":"test","path":"/spec/values","value":{"cluster":{"version":"7.3.63"}}},{"op":"add","path":"/spec/values/version","value":"v7.3"}]' "$FAKE_CMDLOG"
  grep -qxF 'PATCH tenant-c foundationdb-unlabelled json [{"op":"test","path":"/spec/values","value":{"cluster":{"version":"7.1.0"}}},{"op":"add","path":"/spec/values/version","value":"v7.1"}]' "$FAKE_CMDLOG"

  [ "$(grep -c '^PATCH ' "$FAKE_CMDLOG")" -eq 4 ]
  if grep -q 'PATCH [^ ]* foundationdb-defaults ' "$FAKE_CMDLOG"; then echo "unexpected line matching 'PATCH [^ ]* foundationdb-defaults '"; exit 1; fi
  if grep -q 'PATCH [^ ]* foundationdb-migrated ' "$FAKE_CMDLOG"; then echo "unexpected line matching 'PATCH [^ ]* foundationdb-migrated '"; exit 1; fi
  if grep -q 'PATCH [^ ]* postgres-db ' "$FAKE_CMDLOG"; then echo "unexpected line matching 'PATCH [^ ]* postgres-db '"; exit 1; fi
  if grep -q 'PATCH [^ ]* foundationdb-ancient ' "$FAKE_CMDLOG"; then echo "unexpected line matching 'PATCH [^ ]* foundationdb-ancient '"; exit 1; fi
  if grep -q 'PATCH [^ ]* opensearch-plain ' "$FAKE_CMDLOG"; then echo "unexpected line matching 'PATCH [^ ]* opensearch-plain '"; exit 1; fi
  if grep -q 'PATCH [^ ]* opensearch-logs ' "$FAKE_CMDLOG"; then echo "unexpected line matching 'PATCH [^ ]* opensearch-logs '"; exit 1; fi
  if grep -q 'PATCH [^ ]* foundationdb-scalar-values ' "$FAKE_CMDLOG"; then echo "unexpected line matching 'PATCH [^ ]* foundationdb-scalar-values '"; exit 1; fi
  if grep -q 'PATCH [^ ]* kubernetes' "$FAKE_CMDLOG"; then echo "patched a Kubernetes release"; exit 1; fi
  if grep -q 'PATCH [^ ]* foundationdb-numeric ' "$FAKE_CMDLOG"; then echo "unexpected line matching 'PATCH [^ ]* foundationdb-numeric '"; exit 1; fi

  annotation=$(grep '^ANNOTATE ' "$FAKE_CMDLOG")
  echo "$annotation" | grep -q 'cozystack.io/migration-59-needs-attention='
  echo "$annotation" | grep -q 'tenant-d/foundationdb-ancient=cluster.version:6.3.25'
  echo "$annotation" | grep -q 'tenant-d/foundationdb-numeric=cluster.version:7.3'
  echo "$annotation" | grep -q 'tenant-d/foundationdb-scalar-values=values-not-an-object'
  echo "$annotation" | grep -q 'tenant-e/opensearch-logs=images.opensearch:registry.example.test/opensearch:2.19.6'
  echo "$annotation" | grep -q 'tenant-a/foundationdb-new74=cluster.version:7.4.3->7.4.1'
  echo "$annotation" | grep -q 'tenant-c/foundationdb-unlabelled=cluster.version:7.1.0->7.1.67'
  echo "$annotation" | grep -q 'tenant-f/kubernetes-mirrored=images.talosCsrSigner:mirror.example.test/talos-csr-signer:1;images.waitForKubeconfig:mirror.example.test/busybox:1'
  echo "$annotation" | grep -q 'tenant-f/kubernetes-nodes-mirrored-md0=images.kubectl:mirror.example.test/kubectl:1'
  for quiet in opensearch-plain kubernetes-plain foundationdb-old71 foundationdb-pinned73; do
    if echo "$annotation" | grep -q "/$quiet="; then echo "recorded $quiet, which needs no attention"; exit 1; fi
  done
  [ "$(tail -n1 "$FAKE_CMDLOG")" = "STAMP 60" ]
  if grep -q '^UNHANDLED' "$FAKE_CMDLOG"; then echo "unexpected line matching '^UNHANDLED'"; exit 1; fi
  rm -rf "$WORK"
}

@test "a failed patch aborts before the stamp" {
  prep
  export FAKE_PATCH_FAIL=1
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  [ "$rc" -ne 0 ]
  if grep -q '^STAMP' "$FAKE_CMDLOG"; then echo "unexpected line matching '^STAMP'"; exit 1; fi
  rm -rf "$WORK"
}

@test "a failed fleet scan aborts before the stamp" {
  prep
  export FAKE_LIST_FAIL=1
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  [ "$rc" -ne 0 ]
  if grep -q '^PATCH' "$FAKE_CMDLOG"; then echo "unexpected line matching '^PATCH'"; exit 1; fi
  if grep -q '^STAMP' "$FAKE_CMDLOG"; then echo "unexpected line matching '^STAMP'"; exit 1; fi
  rm -rf "$WORK"
}

@test "dry run patches nothing and does not stamp" {
  prep
  export MIGRATION_DRY_RUN=1
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  [ "$rc" -eq 0 ]
  grep -q 'would set version=v7.1 on FoundationDB tenant-a/foundationdb-old71' "$WORK/out"
  if grep -q '^PATCH' "$FAKE_CMDLOG"; then echo "unexpected line matching '^PATCH'"; exit 1; fi
  if grep -q '^STAMP' "$FAKE_CMDLOG"; then echo "unexpected line matching '^STAMP'"; exit 1; fi
  rm -rf "$WORK"
}

@test "the tag map matches the chart's versions.yaml" {
  migration=$(sed -n "s/^FOUNDATIONDB_TAGS='\\(.*\\)'$/\\1/p" "$MIG" | jq -S .)
  chart=$(yq -o=json '.' packages/apps/foundationdb/files/versions.yaml | jq -S .)
  echo "migration: $migration"
  echo "chart:     $chart"
  [ -n "$migration" ]
  [ "$migration" = "$chart" ]
}
