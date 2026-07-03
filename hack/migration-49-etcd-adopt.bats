#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for platform migration 49 (adopt legacy etcd.aenix.io/v1alpha1
# clusters onto etcd-operator.cozystack.io/v1alpha2 via etcd-migrate).
#
# These drive the real migration script end-to-end against a fake kubectl and a
# fake etcd-migrate (hack/testdata/migration-49/), mocking only the cluster
# boundary — the same approach test/check-readiness uses. Every fake invocation
# is logged so we can assert on the ordering and arguments of the destructive
# steps, which is exactly the contract that cannot be checked by reading the
# script.
#
# The behaviours pinned here are the review blockers:
#   1. the safety snapshot is ALWAYS taken to the platform cozy-backups bucket —
#      never skipped — and the script fails loudly if the target is unresolvable;
#   2. the staged credentials Secret is tenant-invisible (managed-by set,
#      tenantresource stripped); and
#   3. the operator is scaled to 0 BEFORE etcd-migrate --apply and back to 1
#      AFTER, with --apply always carrying the S3 backup destination.
#
# cozytest.sh's awk parser recognizes only @test blocks and a bare `}` on its
# own line; there is no bats `run`/`$status`/`setup`. Assertions are direct
# shell tests that exit non-zero on failure.
#
# Run with: hack/cozytest.sh hack/migration-49-etcd-adopt.bats
# -----------------------------------------------------------------------------

FAKEBIN="$PWD/hack/testdata/migration-49"
MIG="$PWD/packages/core/platform/images/migrations/migrations/49"

# prep resets PATH/env to a clean scenario (one legacy cluster, a resolvable
# platform target). Tests override the FAKE_* knobs afterwards.
prep() {
  chmod +x "$FAKEBIN/kubectl" "$FAKEBIN/etcd-migrate"
  WORK=$(mktemp -d)
  export FAKE_CMDLOG="$WORK/cmdlog"
  export FAKE_STAGE_DIR="$WORK/staged"
  mkdir -p "$FAKE_STAGE_DIR"
  : > "$FAKE_CMDLOG"
  export PATH="$FAKEBIN:$PATH"
  export NAMESPACE=cozy-system
  export ETCD_OPERATOR_NS=cozy-etcd-operator
  export ETCD_OPERATOR_DEPLOY=etcd-operator-controller-manager
  export DRY_RUN=0
  export CLUSTER_DOMAIN=cozy.local
  export PLATFORM_CREDS_NS=cozy-velero
  export FAKE_LEGACY_CRD=1
  export FAKE_CLUSTERS="tenant-foo etcd"
  export FAKE_STRATEGY=1
  export FAKE_CREDS=1
  export FAKE_V2_CRD=0
  # The baked-CRD dir must exist for the script's `-d` guard; the fake kubectl
  # logs the apply without reading it, so an empty dir is enough.
  export ETCD_CRD_DIR="$WORK/etcd-operator-crds"
  mkdir -p "$ETCD_CRD_DIR"
  unset ETCD_MIGRATE_BACKUP_ARGS || true
}

# lineno echoes the 1-based line number of the first $FAKE_CMDLOG line that
# contains the fixed string $1 (empty if absent).
lineno() {
  grep -nF -- "$1" "$FAKE_CMDLOG" | head -1 | cut -d: -f1
}

@test "platform path auto-derives the S3 snapshot args and stages tenant-safe creds before adopting" {
  prep
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]

  # --apply carries the platform-derived S3 destination, NOT --skip-backup.
  apply=$(grep -F -- "ETCD-MIGRATE --apply" "$FAKE_CMDLOG")
  echo "$apply" | grep -qF -- "--backup-s3-endpoint=http://seaweedfs-s3.tenant-root.svc.cozy.local:8333"
  echo "$apply" | grep -qF -- "--backup-s3-bucket=cozy-backups-7f3a"
  echo "$apply" | grep -qF -- "--backup-s3-credentials-secret=cozy-backups-creds"
  echo "$apply" | grep -qF -- "--backup-s3-region=us-east-1"
  echo "$apply" | grep -qF -- "--backup-s3-force-path-style"
  # snapshots land under a system key prefix, not a tenant <ns>/<name>/ path.
  echo "$apply" | grep -qF -- "--backup-s3-key=cozy-system/etcd-adoption/"
  ! echo "$apply" | grep -qF -- "--skip-backup"
  # --agent-image is explicit (the scaled-down Deployment is still the legacy operator).
  echo "$apply" | grep -qF -- "--agent-image=ghcr.io/cozystack/etcd-operator:v0.5.1"

  # The snapshot credentials are staged into the adopted namespace, and the
  # staged Secret is tenant-invisible (managed-by set, tenantresource stripped)
  # while still carrying the AWS_* keys etcd-migrate needs.
  grep -qF -- "STAGE tenant-foo" "$FAKE_CMDLOG"
  staged="$FAKE_STAGE_DIR/tenant-foo.json"
  [ -s "$staged" ]
  [ "$(jq -r '.metadata.labels["apps.cozystack.io/managed-by"]' "$staged")" = "cozystack-backups" ]
  [ "$(jq -r '.metadata.labels["internal.cozystack.io/tenantresource"] // "absent"' "$staged")" = "absent" ]
  [ "$(jq -r '.data.AWS_ACCESS_KEY_ID' "$staged")" = "QUtJQUVYQU1QTEU=" ]
  [ "$(jq -r '.metadata.namespace // "absent"' "$staged")" = "absent" ]

  # Order: stage creds -> scale operator down -> --apply -> scale up -> stamp.
  s_stage=$(lineno "STAGE tenant-foo")
  s_down=$(lineno "SCALE 0")
  s_apply=$(lineno "ETCD-MIGRATE --apply")
  s_up=$(lineno "SCALE 1")
  s_stamp=$(lineno "STAMP")
  [ -n "$s_stage" ] && [ -n "$s_down" ] && [ -n "$s_apply" ] && [ -n "$s_up" ] && [ -n "$s_stamp" ]
  [ "$s_stage" -lt "$s_down" ]
  [ "$s_down" -lt "$s_apply" ]
  [ "$s_apply" -lt "$s_up" ]
  [ "$s_up" -lt "$s_stamp" ]
  rm -rf "$WORK"
}

@test "applies the baked v1alpha2 CRDs before adopting when they are absent" {
  prep
  export FAKE_V2_CRD=0
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  # CRDs applied, and BEFORE etcd-migrate (which lists v1alpha2 clusters).
  grep -qF -- "APPLY-CRDS" "$FAKE_CMDLOG"
  s_crds=$(lineno "APPLY-CRDS")
  s_apply=$(lineno "ETCD-MIGRATE --apply")
  [ -n "$s_crds" ] && [ -n "$s_apply" ]
  [ "$s_crds" -lt "$s_apply" ]
}

@test "does not re-apply v1alpha2 CRDs when already installed" {
  prep
  export FAKE_V2_CRD=1
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  ! grep -qF -- "APPLY-CRDS" "$FAKE_CMDLOG"
  # Adoption still proceeds.
  grep -qF -- "ETCD-MIGRATE --apply" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "re-issues server and peer certs with the native wildcard SAN before adopting" {
  prep
  # Certificates exist but lack the operator's native wildcard (the legacy
  # enumerated-SAN situation ensure_wildcard_sans exists to fix).
  export FAKE_CERTS=1
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  # Both server and peer Certificates are patched...
  grep -qF -- "PATCH-CERT etcd-server" "$FAKE_CMDLOG"
  grep -qF -- "PATCH-CERT etcd-peer" "$FAKE_CMDLOG"
  # ...and the patch payload carries the native wildcard SAN.
  grep -F -- "patch certificate.cert-manager.io etcd-server" "$FAKE_CMDLOG" | grep -qF -- "*.etcd.tenant-foo.svc"
  # The re-issue happens BEFORE adoption (so a replacement member never fails TLS).
  s_patch=$(lineno "PATCH-CERT etcd-server")
  s_apply=$(lineno "ETCD-MIGRATE --apply")
  [ -n "$s_patch" ] && [ -n "$s_apply" ]
  [ "$s_patch" -lt "$s_apply" ]
  rm -rf "$WORK"
}

@test "failed adoption restores the operator to 1 replica (no stranded scale-to-0)" {
  prep
  export FAKE_MIGRATE_FAIL=1
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  cat "$FAKE_CMDLOG"
  # The migration must propagate the failure...
  [ "$rc" -ne 0 ]
  # ...but the EXIT trap must have scaled the cluster-wide operator back to 1
  # AFTER the failed --apply, so reconciliation is not frozen platform-wide.
  s_down=$(lineno "SCALE 0")
  s_apply=$(lineno "ETCD-MIGRATE --apply")
  s_up=$(lineno "SCALE 1")
  [ -n "$s_down" ] && [ -n "$s_apply" ] && [ -n "$s_up" ]
  [ "$s_down" -lt "$s_apply" ]
  [ "$s_apply" -lt "$s_up" ]
  # The version must NOT be stamped on a failed adoption (the Job retries).
  ! grep -qF -- "STAMP" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "no resolvable destination hard-fails without scaling or adopting" {
  prep
  export FAKE_STRATEGY=0
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  grep -qF -- "refusing to adopt" "$WORK/out"
  # Nothing destructive must have run: no scale-down, no --apply, no version stamp.
  ! grep -qF -- "SCALE 0" "$FAKE_CMDLOG"
  ! grep -qF -- "ETCD-MIGRATE --apply" "$FAKE_CMDLOG"
  ! grep -qF -- "STAMP" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "inbound --skip-backup is ignored: still snapshots to the platform bucket" {
  prep
  # There is no opt-out anymore. An operator setting --skip-backup must NOT be
  # able to suppress the snapshot: the platform target is resolved regardless
  # and --apply carries the S3 destination, never --skip-backup.
  export ETCD_MIGRATE_BACKUP_ARGS="--skip-backup"
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  apply=$(grep -F -- "ETCD-MIGRATE --apply" "$FAKE_CMDLOG")
  echo "$apply" | grep -qF -- "--backup-s3-bucket=cozy-backups-7f3a"
  ! echo "$apply" | grep -qF -- "--skip-backup"
  grep -qF -- "STAGE tenant-foo" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "idempotent re-run with zero remaining clusters stamps without adopting" {
  prep
  # CRD still present (a prior run adopted everything) but no legacy clusters left.
  export FAKE_CLUSTERS=""
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  grep -qF -- "STAMP" "$FAKE_CMDLOG"
  ! grep -qF -- "SCALE 0" "$FAKE_CMDLOG"
  ! grep -qF -- "ETCD-MIGRATE --apply" "$FAKE_CMDLOG"
  ! grep -qF -- "STAGE " "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "no legacy CRD stamps version 50 without adopting" {
  prep
  export FAKE_LEGACY_CRD=0
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  grep -qF -- "STAMP" "$FAKE_CMDLOG"
  ! grep -qF -- "SCALE 0" "$FAKE_CMDLOG"
  ! grep -qF -- "ETCD-MIGRATE --apply" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}
