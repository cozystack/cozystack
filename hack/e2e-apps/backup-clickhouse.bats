#!/usr/bin/env bats

# End-to-end smoke for ClickHouse backup/restore via the Altinity strategy
# driver. Runs against a tenant cluster that already has the backup-controller
# and backupstrategy-controller installed. Exercises the BackupJob -> Backup
# round-trip and the in-place restore variant.

TEST_NAMESPACE='tenant-test'
TEST_BUCKET_NAME='backup-clickhouse-bucket'
TEST_STRATEGY_NAME='altinity'
TEST_BACKUPCLASS_NAME='clickhouse-e2e'
TEST_CH_NAME='clickhouse-e2e-source'
TEST_CH_NAME_B='clickhouse-e2e-second'
TEST_BACKUPJOB_NAME='clickhouse-e2e-backup'
TEST_BACKUPJOB_NAME_B='clickhouse-e2e-backup-second'
TEST_RESTOREJOB_INPLACE='clickhouse-e2e-restore-inplace'
TEST_RESTOREJOB_ISOLATION='clickhouse-e2e-restore-second'

teardown() {
    kubectl -n "$TEST_NAMESPACE" delete restorejob "$TEST_RESTOREJOB_ISOLATION" --wait=false 2>/dev/null || true
    kubectl -n "$TEST_NAMESPACE" delete restorejob "$TEST_RESTOREJOB_INPLACE" --wait=false 2>/dev/null || true
    kubectl -n "$TEST_NAMESPACE" delete backupjob "$TEST_BACKUPJOB_NAME_B" --wait=false 2>/dev/null || true
    kubectl -n "$TEST_NAMESPACE" delete backupjob "$TEST_BACKUPJOB_NAME" --wait=false 2>/dev/null || true
    kubectl -n "$TEST_NAMESPACE" delete backup "$TEST_BACKUPJOB_NAME_B" --wait=false 2>/dev/null || true
    kubectl -n "$TEST_NAMESPACE" delete backup "$TEST_BACKUPJOB_NAME" --wait=false 2>/dev/null || true
    kubectl -n "$TEST_NAMESPACE" delete clickhouse "$TEST_CH_NAME_B" --wait=false 2>/dev/null || true
    kubectl -n "$TEST_NAMESPACE" delete clickhouse "$TEST_CH_NAME" --wait=false 2>/dev/null || true
    kubectl -n "$TEST_NAMESPACE" delete bucket "$TEST_BUCKET_NAME" --wait=false 2>/dev/null || true
    kubectl delete backupclass "$TEST_BACKUPCLASS_NAME" --wait=false 2>/dev/null || true
    kubectl delete altinity.strategy.backups.cozystack.io "$TEST_STRATEGY_NAME" --wait=false 2>/dev/null || true
}

print_log() {
    echo "# $1" >&3
}

clickhouse_exec() {
    # The Cozystack ClickHouse RD prefixes Helm release names with
    # "clickhouse-" (packages/system/clickhouse-rd/cozyrds/clickhouse.yaml),
    # so the chart-rendered StatefulSet/Secret names also carry that prefix
    # even when the user-facing application name does not. Mirror the
    # examples/backups/clickhouse/00-helpers.sh helpers.
    local release="$1"
    local sql="$2"
    local pwd
    pwd=$(kubectl -n "$TEST_NAMESPACE" get secret "clickhouse-${release}-credentials" -o jsonpath='{.data.backup}' | base64 -d)
    kubectl -n "$TEST_NAMESPACE" exec -i \
        "statefulset/chi-clickhouse-${release}-clickhouse-0-0" -c clickhouse -- \
        clickhouse-client -u backup --password "$pwd" -q "$sql"
}

@test "Backup and in-place restore a ClickHouse application via Altinity strategy" {
    print_log "Step 0: Ensure backup CRDs are installed"
    kubectl apply -f packages/system/backup-controller/definitions/backups.cozystack.io_backupjobs.yaml
    kubectl apply -f packages/system/backup-controller/definitions/backups.cozystack.io_backupclasses.yaml
    kubectl apply -f packages/system/backup-controller/definitions/backups.cozystack.io_backups.yaml
    kubectl apply -f packages/system/backup-controller/definitions/backups.cozystack.io_restorejobs.yaml
    kubectl apply -f packages/system/backupstrategy-controller/definitions/strategy.backups.cozystack.io_altinities.yaml
    kubectl wait --for condition=established --timeout=30s crd backupjobs.backups.cozystack.io
    kubectl wait --for condition=established --timeout=30s crd backupclasses.backups.cozystack.io
    kubectl wait --for condition=established --timeout=30s crd backups.backups.cozystack.io
    kubectl wait --for condition=established --timeout=30s crd restorejobs.backups.cozystack.io
    kubectl wait --for condition=established --timeout=30s crd altinities.strategy.backups.cozystack.io

    print_log "Step 1: Provision Bucket and read credentials"
    # spec.users.backup is required for the Bucket app to render a
    # per-user BucketAccess + credentials Secret named
    # "bucket-${BUCKET}-${USER}". Mirrors examples/backups/clickhouse/
    # 03-create-bucket.sh - the Bucket chart's templates iterate
    # `range $name, $user := .Values.users` to emit one BucketAccess
    # per user, so an empty users map produces no BucketAccess and the
    # waits below would time out.
    kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Bucket
metadata:
  name: ${TEST_BUCKET_NAME}
  namespace: ${TEST_NAMESPACE}
spec:
  users:
    backup:
      readonly: false
EOF
    kubectl -n "$TEST_NAMESPACE" wait hr "bucket-${TEST_BUCKET_NAME}" --timeout=300s --for=condition=ready
    kubectl -n "$TEST_NAMESPACE" wait bucketclaims.objectstorage.k8s.io "bucket-${TEST_BUCKET_NAME}" --timeout=300s --for=jsonpath='{.status.bucketReady}'=true
    kubectl -n "$TEST_NAMESPACE" wait bucketaccesses.objectstorage.k8s.io "bucket-${TEST_BUCKET_NAME}-backup" --timeout=300s --for=jsonpath='{.status.accessGranted}'=true

    BI=$(kubectl -n "$TEST_NAMESPACE" get secret "bucket-${TEST_BUCKET_NAME}-backup" -o jsonpath='{.data.BucketInfo}' | base64 -d)
    AKID=$(echo "$BI" | jq -r '.spec.secretS3.accessKeyID')
    AKSECRET=$(echo "$BI" | jq -r '.spec.secretS3.accessSecretKey')
    ENDPOINT=$(echo "$BI" | jq -r '.spec.secretS3.endpoint')
    REGION=$(echo "$BI" | jq -r '.spec.secretS3.region // "us-east-1"')
    BUCKET=$(echo "$BI" | jq -r '.spec.bucketName')

    print_log "Step 2: Create Altinity strategy and BackupClass"
    # Strategy Pod is a tiny curl/jq client; the heavy lifting happens in the
    # clickhouse-backup sidecar that the chart materialises inside chi-* Pods
    # when backup.enabled=true on the ClickHouse spec.
    kubectl apply -f - <<EOF
apiVersion: strategy.backups.cozystack.io/v1alpha1
kind: Altinity
metadata:
  name: ${TEST_STRATEGY_NAME}
spec:
  jobTemplate:
    spec:
      restartPolicy: Never
      containers:
        - name: ch-backup-client
          image: alpine:3.19
          imagePullPolicy: IfNotPresent
          env:
            - name: API_USERNAME
              valueFrom:
                secretKeyRef:
                  name: clickhouse-{{ .Release.Name }}-backup-api-auth
                  key: username
            - name: API_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: clickhouse-{{ .Release.Name }}-backup-api-auth
                  key: password
          command: ["/bin/sh","-c"]
          args:
            - |
              set -euo pipefail
              apk add --no-cache curl jq >/dev/null
              host="chi-clickhouse-{{ .Release.Name }}-clickhouse-0-0.{{ .Release.Namespace }}.svc"
              api="http://\${host}:7171"
              auth="-u \${API_USERNAME}:\${API_PASSWORD}"
              for _ in \$(seq 1 60); do
                if curl -fsS \${auth} --max-time 2 "\${api}/backup/status" >/dev/null 2>&1; then break; fi
                sleep 5
              done
              if [ "{{ .Mode }}" = "backup" ]; then
                prefix="clickhouse-{{ .Release.Name }}-"
                target="\${prefix}\$(date -u +%Y%m%dT%H%M%SZ)"
                curl -fsS \${auth} -X POST "\${api}/backup/create_remote?name=\${target}" >/dev/null
              else
                # Filter by source-release prefix so restore on release A
                # cannot pick up an archive that release B left in the same
                # bucket. .Backup.ApplicationRef.Name is the source release.
                prefix="clickhouse-{{ .Backup.ApplicationRef.Name }}-"
                target=\$(curl -fsS \${auth} "\${api}/backup/list/remote" | jq -rs --arg prefix "\${prefix}" '[.[] | select((.name // "") | startswith(\$prefix))] | sort_by(.created // "") | last.name // ""')
                [ -n "\${target}" ] && [ "\${target}" != "null" ] || { echo "no remote backup found" >&2; exit 1; }
                curl -fsS \${auth} -X POST "\${api}/backup/restore_remote/\${target}" >/dev/null
              fi
              while true; do
                row=\$(curl -fsS \${auth} "\${api}/backup/actions" | jq -rs --arg suffix "\${target}" '[.[] | select((.command // "") | endswith(\$suffix))] | sort_by(.start) | last')
                status=\$(echo "\${row}" | jq -r '.status // ""')
                case "\${status}" in
                  success) exit 0 ;;
                  error) echo "operation failed: \$(echo "\${row}" | jq -r '.error // "unknown"')" >&2 ; exit 1 ;;
                esac
                sleep 5
              done
          resources:
            requests: { cpu: 10m, memory: 32Mi }
            limits:   { cpu: 200m, memory: 128Mi }
          securityContext:
            allowPrivilegeEscalation: false
            capabilities: { drop: ["ALL"] }
            seccompProfile: { type: RuntimeDefault }
EOF

    kubectl apply -f - <<EOF
apiVersion: backups.cozystack.io/v1alpha1
kind: BackupClass
metadata:
  name: ${TEST_BACKUPCLASS_NAME}
spec:
  strategies:
    - application:
        apiGroup: apps.cozystack.io
        kind: ClickHouse
      strategyRef:
        apiGroup: strategy.backups.cozystack.io
        kind: Altinity
        name: ${TEST_STRATEGY_NAME}
EOF

    print_log "Step 3: Provision source ClickHouse with backup.enabled (chart materialises <release>-backup-s3) and write a sentinel row"
    kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: ClickHouse
metadata:
  name: ${TEST_CH_NAME}
  namespace: ${TEST_NAMESPACE}
spec:
  size: 5Gi
  logStorageSize: 1Gi
  shards: 1
  replicas: 1
  resourcesPreset: nano
  resources: {}
  backup:
    enabled: true
    s3Bucket: "${BUCKET}"
    s3Region: "${REGION}"
    endpoint: "${ENDPOINT}"
    s3AccessKey: "${AKID}"
    s3SecretKey: "${AKSECRET}"
  clickhouseKeeper:
    enabled: true
    replicas: 1
    size: 1Gi
    resourcesPreset: micro
EOF
    kubectl -n "$TEST_NAMESPACE" wait hr "clickhouse-${TEST_CH_NAME}" --timeout=300s --for=condition=ready
    timeout 300 sh -ec "until kubectl -n ${TEST_NAMESPACE} get sts chi-clickhouse-${TEST_CH_NAME}-clickhouse-0-0 ; do sleep 10; done"
    kubectl -n "$TEST_NAMESPACE" wait statefulset.apps/"chi-clickhouse-${TEST_CH_NAME}-clickhouse-0-0" --timeout=300s --for=jsonpath='{.status.readyReplicas}'=1

    clickhouse_exec "$TEST_CH_NAME" "CREATE TABLE IF NOT EXISTS default.sentinel (id UInt32, label String) ENGINE = MergeTree ORDER BY id"
    clickhouse_exec "$TEST_CH_NAME" "INSERT INTO default.sentinel VALUES (1,'before-backup')"

    print_log "Step 4: Submit BackupJob and wait for Succeeded"
    kubectl apply -f - <<EOF
apiVersion: backups.cozystack.io/v1alpha1
kind: BackupJob
metadata:
  name: ${TEST_BACKUPJOB_NAME}
  namespace: ${TEST_NAMESPACE}
spec:
  applicationRef:
    apiGroup: apps.cozystack.io
    kind: ClickHouse
    name: ${TEST_CH_NAME}
  backupClassName: ${TEST_BACKUPCLASS_NAME}
EOF
    kubectl -n "$TEST_NAMESPACE" wait backupjob "$TEST_BACKUPJOB_NAME" --timeout=120s --for=jsonpath='{.status.phase}'=Running
    timeout 600 sh -ec "until [ \"\$(kubectl -n ${TEST_NAMESPACE} get backupjob ${TEST_BACKUPJOB_NAME} -o jsonpath='{.status.phase}')\" = Succeeded ]; do sleep 10; done"
    backup_ref=$(kubectl -n "$TEST_NAMESPACE" get backupjob "$TEST_BACKUPJOB_NAME" -o jsonpath='{.status.backupRef.name}')
    [ -n "$backup_ref" ]

    print_log "Step 5: In-place restore (drop sentinel, then RestoreJob with no targetApplicationRef)"
    clickhouse_exec "$TEST_CH_NAME" "DROP TABLE IF EXISTS default.sentinel SYNC"

    kubectl apply -f - <<EOF
apiVersion: backups.cozystack.io/v1alpha1
kind: RestoreJob
metadata:
  name: ${TEST_RESTOREJOB_INPLACE}
  namespace: ${TEST_NAMESPACE}
spec:
  backupRef:
    name: ${TEST_BACKUPJOB_NAME}
EOF
    timeout 600 sh -ec "until [ \"\$(kubectl -n ${TEST_NAMESPACE} get restorejob ${TEST_RESTOREJOB_INPLACE} -o jsonpath='{.status.phase}')\" = Succeeded ]; do sleep 10; done"

    count=$(clickhouse_exec "$TEST_CH_NAME" "SELECT count() FROM default.sentinel" | tr -d '[:space:]')
    [ "$count" -ge "1" ]

    print_log "Step 6: Provision SECOND ClickHouse on the SAME bucket (multi-release isolation)"
    # The S3_PATH fix scopes each release's backups under a release-named
    # prefix inside the shared bucket; without it, list/remote on release
    # B would pick up release A's newer archive and corrupt B's data on
    # in-place restore. Provision a second release that points at the
    # same Bucket but writes a different sentinel value, then restore B
    # from B's own backup and assert B's data survives.
    kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: ClickHouse
metadata:
  name: ${TEST_CH_NAME_B}
  namespace: ${TEST_NAMESPACE}
spec:
  size: 5Gi
  logStorageSize: 1Gi
  shards: 1
  replicas: 1
  resourcesPreset: nano
  resources: {}
  backup:
    enabled: true
    s3Bucket: "${BUCKET}"
    s3Region: "${REGION}"
    endpoint: "${ENDPOINT}"
    s3AccessKey: "${AKID}"
    s3SecretKey: "${AKSECRET}"
  clickhouseKeeper:
    enabled: true
    replicas: 1
    size: 1Gi
    resourcesPreset: micro
EOF
    kubectl -n "$TEST_NAMESPACE" wait hr "clickhouse-${TEST_CH_NAME_B}" --timeout=300s --for=condition=ready
    timeout 300 sh -ec "until kubectl -n ${TEST_NAMESPACE} get sts chi-clickhouse-${TEST_CH_NAME_B}-clickhouse-0-0 ; do sleep 10; done"
    kubectl -n "$TEST_NAMESPACE" wait statefulset.apps/"chi-clickhouse-${TEST_CH_NAME_B}-clickhouse-0-0" --timeout=300s --for=jsonpath='{.status.readyReplicas}'=1

    clickhouse_exec "$TEST_CH_NAME_B" "CREATE TABLE IF NOT EXISTS default.sentinel (id UInt32, label String) ENGINE = MergeTree ORDER BY id"
    clickhouse_exec "$TEST_CH_NAME_B" "INSERT INTO default.sentinel VALUES (2,'release-B-sentinel')"

    print_log "Step 7: Backup release B (B's archive becomes newest in the shared bucket)"
    kubectl apply -f - <<EOF
apiVersion: backups.cozystack.io/v1alpha1
kind: BackupJob
metadata:
  name: ${TEST_BACKUPJOB_NAME_B}
  namespace: ${TEST_NAMESPACE}
spec:
  applicationRef:
    apiGroup: apps.cozystack.io
    kind: ClickHouse
    name: ${TEST_CH_NAME_B}
  backupClassName: ${TEST_BACKUPCLASS_NAME}
EOF
    timeout 600 sh -ec "until [ \"\$(kubectl -n ${TEST_NAMESPACE} get backupjob ${TEST_BACKUPJOB_NAME_B} -o jsonpath='{.status.phase}')\" = Succeeded ]; do sleep 10; done"

    print_log "Step 8: Drop B's sentinel and trigger an in-place restore on B"
    # Without S3_PATH isolation, the strategy script's "pick latest in
    # bucket" semantics would let A's older archive (or A's newer
    # archive if we re-took it after B's) compete with B's archive,
    # silently corrupting B's data. With the fix, B's sidecar lists
    # under B's S3_PATH only and the restore lands on B's archive.
    clickhouse_exec "$TEST_CH_NAME_B" "DROP TABLE IF EXISTS default.sentinel SYNC"
    kubectl apply -f - <<EOF
apiVersion: backups.cozystack.io/v1alpha1
kind: RestoreJob
metadata:
  name: ${TEST_RESTOREJOB_ISOLATION}
  namespace: ${TEST_NAMESPACE}
spec:
  backupRef:
    name: ${TEST_BACKUPJOB_NAME_B}
EOF
    timeout 600 sh -ec "until [ \"\$(kubectl -n ${TEST_NAMESPACE} get restorejob ${TEST_RESTOREJOB_ISOLATION} -o jsonpath='{.status.phase}')\" = Succeeded ]; do sleep 10; done"

    label_b=$(clickhouse_exec "$TEST_CH_NAME_B" "SELECT label FROM default.sentinel WHERE id = 2" | tr -d '[:space:]')
    [ "$label_b" = "release-B-sentinel" ]
    # Cross-check: A's data should NOT show up in B (no row with id=1).
    crosscount=$(clickhouse_exec "$TEST_CH_NAME_B" "SELECT count() FROM default.sentinel WHERE id = 1" | tr -d '[:space:]')
    [ "$crosscount" = "0" ]
}
