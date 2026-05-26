#!/usr/bin/env bats

# E2E for the platform-managed cozy-default BackupClass.
#
# Covers the parts of the design where tenants no longer supply S3
# configuration:
#   1. The cozystack-installed cozy-default BackupClass exists and binds
#      Postgres -> cozy-default-cnpg without any tenant-side bucket/secret
#      setup.
#   2. A Postgres BackupJob referencing cozy-default succeeds and lands a
#      Ready Backup artefact under the s3://cozy-backups/<ns>/<app>/ prefix
#      enforced by the default strategy template.
#   3. The platform-projected cozy-backups-creds Secret in the tenant
#      namespace is not readable by the tenant ServiceAccount — the RBAC
#      check anchors the credentials-isolation invariant.
#
# Edge cases (projection retry on missing source Secret, format
# normalisation accessKey -> AWS_ACCESS_KEY_ID) belong in the Go unit
# tests at internal/backupcontroller/credentials_projector_test.go.

TEST_NAMESPACE='tenant-test'
TEST_POSTGRES_NAME='pgapp'
TEST_BACKUPJOB_NAME='pg-default-job'
TEST_CLICKHOUSE_NAME='chapp'
TEST_CLICKHOUSE_BACKUPJOB_NAME='ch-default-job'
TEST_PROJECTED_SECRET='cozy-backups-creds'
SYSTEM_BUCKET_NS='tenant-root'
SYSTEM_BUCKET_NAME='cozy-backups'

setup_file() {
  kubectl -n "$TEST_NAMESPACE" delete backupjob.backups.cozystack.io --all --ignore-not-found --timeout=60s
  kubectl -n "$TEST_NAMESPACE" delete backup.backups.cozystack.io --all --ignore-not-found --timeout=60s
  kubectl -n "$TEST_NAMESPACE" delete postgres.apps.cozystack.io --all --ignore-not-found --timeout=2m
  kubectl -n "$TEST_NAMESPACE" delete clickhouse.apps.cozystack.io --all --ignore-not-found --timeout=2m
  kubectl -n "$TEST_NAMESPACE" delete secret "$TEST_PROJECTED_SECRET" --ignore-not-found --timeout=60s
}

teardown_file() {
  setup_file
}

print_log() {
  # cozytest.sh's run_one merges stdout/stderr through `2>&1 | tee`; fd 3
  # (the bats convention for surfacing diagnostic output) is not opened,
  # so write to stderr instead — picked up by the same pipe.
  echo "# $1" 1>&2
}

dump_diagnostics() {
  echo "# --- diagnostics ---" 1>&2
  kubectl get backupclass cozy-default -o yaml 1>&2 || true
  kubectl -n "$SYSTEM_BUCKET_NS" get bucket,bucketclaim,secret 1>&2 || true
  kubectl -n "$TEST_NAMESPACE" get postgres,cnpgcluster,backupjob,backup,secret -o wide 1>&2 || true
  kubectl -n "$TEST_NAMESPACE" describe backupjob "$TEST_BACKUPJOB_NAME" 1>&2 || true
  # backupstrategy-controller Deployment lives in cozy-backup-controller
  # (see packages/core/platform/sources/backupstrategy-controller.yaml)
  # with label app=backupstrategy-controller.
  kubectl -n cozy-backup-controller logs -l app=backupstrategy-controller --tail=200 1>&2 || true
}

@test "Platform-managed cozy-default BackupClass and bucket exist" {
  kubectl get backupclass cozy-default >/dev/null
  kubectl -n "$SYSTEM_BUCKET_NS" get bucket "$SYSTEM_BUCKET_NAME" >/dev/null
  KIND_COUNT=$(kubectl get backupclass cozy-default -o jsonpath='{range .spec.strategies[*]}{.application.kind}{"\n"}{end}' | sort -u | wc -l)
  [ "$KIND_COUNT" -ge 3 ]
}

@test "Postgres BackupJob via cozy-default Succeeds without tenant S3 config" {
  print_log "Apply Postgres app with the documented useSystemBucket opt-in"
  # useSystemBucket: true is the canonical platform-managed flow documented
  # in docs/operations/backup-classes.md and described in db.yaml. Setting
  # it explicitly here keeps the e2e in lockstep with what tenants are told
  # to do — a regression that breaks the useSystemBucket=true path would
  # otherwise still pass because $renderBarman is already gated on a
  # non-empty destinationPath.
  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Postgres
metadata:
  name: ${TEST_POSTGRES_NAME}
  namespace: ${TEST_NAMESPACE}
spec:
  replicas: 1
  size: 1Gi
  resources:
    cpu: 100m
    memory: 256Mi
  backup:
    enabled: true
    useSystemBucket: true
EOF

  kubectl -n "$TEST_NAMESPACE" wait hr "${TEST_POSTGRES_NAME}" --for=condition=ready --timeout=600s

  print_log "Submit BackupJob referencing cozy-default"
  kubectl apply -f - <<EOF
apiVersion: backups.cozystack.io/v1alpha1
kind: BackupJob
metadata:
  name: ${TEST_BACKUPJOB_NAME}
  namespace: ${TEST_NAMESPACE}
spec:
  applicationRef:
    apiGroup: apps.cozystack.io
    kind: Postgres
    name: ${TEST_POSTGRES_NAME}
  backupClassName: cozy-default
EOF

  print_log "Wait for BackupJob phase=Succeeded"
  kubectl -n "$TEST_NAMESPACE" wait backupjob.backups.cozystack.io "$TEST_BACKUPJOB_NAME" \
    --for=jsonpath='{.status.phase}'=Succeeded --timeout=1200s || { dump_diagnostics; false; }

  print_log "Inspect the projected credentials Secret"
  kubectl -n "$TEST_NAMESPACE" get secret "$TEST_PROJECTED_SECRET" >/dev/null
  AK=$(kubectl -n "$TEST_NAMESPACE" get secret "$TEST_PROJECTED_SECRET" -o jsonpath='{.data.AWS_ACCESS_KEY_ID}')
  SK=$(kubectl -n "$TEST_NAMESPACE" get secret "$TEST_PROJECTED_SECRET" -o jsonpath='{.data.AWS_SECRET_ACCESS_KEY}')
  [ -n "$AK" ]
  [ -n "$SK" ]

  # With useSystemBucket=true the chart deliberately omits
  # spec.backup.barmanObjectStore. The CNPG strategy controller is
  # supposed to SSA-patch it onto the live cnpg.io/Cluster at first
  # BackupJob time. Assert that explicitly here: BackupJob.phase=Succeeded
  # alone could be satisfied by some legacy path; what we really want to
  # see is the patched destinationPath on the Cluster CR.
  print_log "Verify cnpg.io/Cluster carries the platform-managed destinationPath"
  # The COSI driver (SeaweedFS) assigns its own UUID-suffixed bucket
  # name per BucketClaim, so the destinationPath embeds the COSI name
  # rather than the chart-side Bucket CR name. Mirror the chart's
  # bucketName helper (packages/system/backupstrategy-controller/templates/_helpers.tpl)
  # — read the actual name from BucketClaim.status.bucketName and
  # assert against that.
  REAL_BUCKET=$(kubectl -n "$SYSTEM_BUCKET_NS" get bucketclaim "bucket-${SYSTEM_BUCKET_NAME}" \
    -o jsonpath='{.status.bucketName}' 2>/dev/null || true)
  [ -n "$REAL_BUCKET" ] || { dump_diagnostics; echo "BucketClaim status.bucketName not populated" 1>&2; false; }
  CNPG_DEST=$(kubectl -n "$TEST_NAMESPACE" get cluster.postgresql.cnpg.io "${TEST_POSTGRES_NAME}" \
    -o jsonpath='{.spec.backup.barmanObjectStore.destinationPath}' 2>/dev/null || true)
  [ "$CNPG_DEST" = "s3://${REAL_BUCKET}/${TEST_NAMESPACE}/${TEST_POSTGRES_NAME}/" ] || { dump_diagnostics; echo "got destinationPath=$CNPG_DEST (expected s3://${REAL_BUCKET}/${TEST_NAMESPACE}/${TEST_POSTGRES_NAME}/)" 1>&2; false; }
}

@test "ClickHouse BackupJob via cozy-default succeeds with useSystemBucket" {
  # ClickHouse wires the platform Secret into its in-Pod clickhouse-backup
  # sidecar via secretKeyRef (S3_BUCKET / S3_ENDPOINT / S3_REGION /
  # S3_ACCESS_KEY / S3_SECRET_KEY) and sets S3_PATH=<ns>/<release>. A
  # regression in the projector or in the chart's sidecar wiring would
  # silently break this; the Postgres case above does not catch it
  # because it exercises a different driver path entirely.
  print_log "Apply ClickHouse app with useSystemBucket=true"
  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: ClickHouse
metadata:
  name: ${TEST_CLICKHOUSE_NAME}
  namespace: ${TEST_NAMESPACE}
spec:
  backup:
    enabled: true
    useSystemBucket: true
EOF

  kubectl -n "$TEST_NAMESPACE" wait hr "${TEST_CLICKHOUSE_NAME}" --for=condition=ready --timeout=600s

  print_log "Submit ClickHouse BackupJob referencing cozy-default"
  kubectl apply -f - <<EOF
apiVersion: backups.cozystack.io/v1alpha1
kind: BackupJob
metadata:
  name: ${TEST_CLICKHOUSE_BACKUPJOB_NAME}
  namespace: ${TEST_NAMESPACE}
spec:
  applicationRef:
    apiGroup: apps.cozystack.io
    kind: ClickHouse
    name: ${TEST_CLICKHOUSE_NAME}
  backupClassName: cozy-default
EOF

  print_log "Wait for ClickHouse BackupJob phase=Succeeded"
  kubectl -n "$TEST_NAMESPACE" wait backupjob.backups.cozystack.io "$TEST_CLICKHOUSE_BACKUPJOB_NAME" \
    --for=jsonpath='{.status.phase}'=Succeeded --timeout=1200s || { dump_diagnostics; false; }

  # Sidecar must have been wired against the projected Secret. Confirm via
  # the rendered ClickHouseInstallation Pod spec — a regression in the
  # chart's useSystemBucket branch (e.g. falling back to <release>-backup-s3)
  # would show a different Secret name here.
  #
  # IMPORTANT: the apps.cozystack.io/ClickHouse ApplicationDefinition
  # prefixes the Helm release name with "clickhouse-" (see
  # packages/system/clickhouse-rd/cozyrds/clickhouse.yaml release.prefix),
  # so a ClickHouse named "chapp" renders a ClickHouseInstallation named
  # "clickhouse-chapp". The Altinity operator labels its Pods with the
  # CHI name. Selector MUST include the prefix or it matches zero pods
  # and the secretKeyRef assertion silently passes against an empty value.
  POD_SECRET=$(kubectl -n "$TEST_NAMESPACE" get pod -l clickhouse.altinity.com/chi="clickhouse-${TEST_CLICKHOUSE_NAME}" \
    -o jsonpath='{.items[0].spec.containers[?(@.name=="clickhouse-backup")].env[?(@.name=="S3_BUCKET")].valueFrom.secretKeyRef.name}')
  [ "$POD_SECRET" = "$TEST_PROJECTED_SECRET" ] || { dump_diagnostics; echo "got POD_SECRET=$POD_SECRET (expected $TEST_PROJECTED_SECRET)" 1>&2; false; }
}

@test "Tenant ServiceAccount cannot read the projected credentials Secret" {
  # The tenant's actual SA is named the same as its namespace and is bound
  # to the aggregated cozy:tenant ClusterRole (see packages/apps/tenant/
  # templates/tenant.yaml). That binding is what gives the SA real
  # tenant-level access — using the `default` SA here would assert nothing
  # because no aggregated role is bound to it.
  #
  # The projector deliberately omits the lineage tenantresource label so
  # the Secret is not promoted to a TenantSecret view. The aggregated
  # cozy:tenant role chain has zero verbs on core/v1.Secret, so
  # `kubectl auth can-i` MUST return no.
  RESULT=$(kubectl auth can-i get secret "$TEST_PROJECTED_SECRET" \
    --namespace "$TEST_NAMESPACE" \
    --as "system:serviceaccount:${TEST_NAMESPACE}:${TEST_NAMESPACE}" 2>/dev/null || true)
  [ "$RESULT" = "no" ]
}
