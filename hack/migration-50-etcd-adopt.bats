#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for platform migration 50 (adopt legacy etcd.aenix.io/v1alpha1
# clusters onto etcd-operator.cozystack.io/v1alpha2 via etcd-migrate).
#
# These drive the real migration script end-to-end against a fake kubectl and a
# fake etcd-migrate (hack/testdata/migration-50/), mocking only the cluster
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
# Run with: hack/cozytest.sh hack/migration-50-etcd-adopt.bats
# -----------------------------------------------------------------------------

FAKEBIN="$PWD/hack/testdata/migration-50"
MIG="$PWD/packages/core/platform/images/migrations/migrations/50"

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
  export FAKE_CLAIM_CRD=1
  # One healthy claim provisioned into the bucket the projected creds advertise:
  # the COSI path. Tests override this to model external S3 / stale claims.
  export FAKE_CLAIMS="tenant-root bucket-cozy-backups cozy-backups-7f3a -"
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
  # The endpoint is derived from the projected Secret's bare host and forced to
  # https, NOT taken from the Strategy CR — the CR is rendered by the version we
  # are upgrading FROM and its plaintext in-cluster default cannot be reached.
  apply=$(grep -F -- "ETCD-MIGRATE --apply" "$FAKE_CMDLOG")
  echo "$apply" | grep -qF -- "--backup-s3-endpoint=https://s3.example.com"
  ! echo "$apply" | grep -qF -- "seaweedfs-s3.tenant-root.svc.cozy.local:8333"
  # https is forced because a BucketClaim is provisioned into exactly the bucket
  # the creds advertise, not because the projected endpoint happens to exist.
  grep -qF -- "BUCKETCLAIM-LIST" "$FAKE_CMDLOG"
  echo "$apply" | grep -qF -- "--backup-s3-bucket=cozy-backups-7f3a"
  echo "$apply" | grep -qF -- "--backup-s3-credentials-secret=cozy-backups-creds"
  echo "$apply" | grep -qF -- "--backup-s3-region=us-east-1"
  echo "$apply" | grep -qF -- "--backup-s3-force-path-style"
  # snapshots land under a system key prefix, not a tenant <ns>/<name>/ path.
  echo "$apply" | grep -qF -- "--backup-s3-key=cozy-system/etcd-adoption/"
  ! echo "$apply" | grep -qF -- "--skip-backup"
  # --agent-image is explicit (the scaled-down Deployment is still the legacy operator).
  echo "$apply" | grep -qF -- "--agent-image=ghcr.io/cozystack/etcd-operator:v0.5.2"

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
  # Neither source resolves the bucket: the projector dropped bucketName (its
  # source Secret did not carry one) and the Strategy CR that would supply it
  # never rendered.
  export FAKE_STRATEGY=0
  export FAKE_CREDS_COORDS=0
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  grep -qF -- "refusing to adopt" "$WORK/out"
  # The refusal must tell the operator how to actually take the hatch. Naming
  # ETCD_ADOPT_SKIP_BACKUP alone was unactionable advice for the entire life of
  # this migration: nothing can set a var on a Helm-hook Job, so the message has
  # to name the Package CR path that renders it.
  grep -qF -- "package.cozystack.io cozystack.cozystack-platform" "$WORK/out"
  grep -qF -- "etcdAdoptSkipBackup: true" "$WORK/out"
  # Nothing destructive must have run: no scale-down, no --apply, no version stamp.
  ! grep -qF -- "SCALE 0" "$FAKE_CMDLOG"
  ! grep -qF -- "ETCD-MIGRATE --apply" "$FAKE_CMDLOG"
  ! grep -qF -- "STAMP" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "absent Strategy CR still resolves from the projected coordinates" {
  prep
  # The v1.5.x shape where strategy-etcd-default.yaml never rendered: its
  # `if $bucketName` guard lookup raced the BucketClaim the same chart creates,
  # and helm lookup does not re-run on reconcile. Before deriving coordinates
  # from the projected Secret this was a total upgrade block with no reachable
  # escape (ETCD_ADOPT_SKIP_BACKUP is not plumbed through the migration hook).
  export FAKE_STRATEGY=0
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  ! grep -qF -- "refusing to adopt" "$WORK/out"
  apply=$(grep -F -- "ETCD-MIGRATE --apply" "$FAKE_CMDLOG")
  echo "$apply" | grep -qF -- "--backup-s3-endpoint=https://s3.example.com"
  echo "$apply" | grep -qF -- "--backup-s3-bucket=cozy-backups-7f3a"
  echo "$apply" | grep -qF -- "--backup-s3-region=us-east-1"
  echo "$apply" | grep -qF -- "--backup-s3-force-path-style"
  ! echo "$apply" | grep -qF -- "--skip-backup"
  grep -qF -- "STAMP" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "external S3 (no COSI at all) keeps the Strategy CR endpoint scheme verbatim" {
  prep
  # provisionBucket: false on a cluster with no SeaweedFS/COSI at all
  # (docs/operations/backup-classes.md describes exactly this flip), so the
  # BucketClaim CRD itself is absent and every claim query fails the way real
  # kubectl fails on an unknown resource type. That must classify as external
  # and must NOT abort the script under `set -euo pipefail`.
  #
  # The projector still writes a bare `endpoint` here (it always does), so the
  # endpoint's PRESENCE cannot signal this case. The admin's
  # .Values.backupStorage.endpoint is authoritative and may legitimately be
  # plaintext against a private store, so its scheme must survive. Forcing https
  # would be a total upgrade block: the snapshot is mandatory and
  # ETCD_ADOPT_SKIP_BACKUP is not plumbed through templates/migration-hook.yaml,
  # so the operator would have no escape.
  export FAKE_CLAIM_CRD=0
  export FAKE_CLAIMS=""
  export FAKE_CREDS_ENDPOINT="minio.internal:9000"
  # The healthy external shape: the admin's backupStorage.bucketName (which the
  # CR renders) and their source Secret name the same bucket. The disagreeing
  # shape is covered separately by the CR-wholesale test above.
  export FAKE_CREDS_BUCKET="minio-bucket"
  export FAKE_STRATEGY_BUCKET="minio-bucket"
  export FAKE_STRATEGY_ENDPOINT="http://minio.internal:9000"
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  apply=$(grep -F -- "ETCD-MIGRATE --apply" "$FAKE_CMDLOG")
  echo "$apply" | grep -qF -- "--backup-s3-endpoint=http://minio.internal:9000"
  # The projected bare host must NOT be promoted to https.
  ! echo "$apply" | grep -qF -- "https://minio.internal:9000"
  echo "$apply" | grep -qF -- "--backup-s3-bucket=minio-bucket"
  # A missing CRD is answered, not retried: the list is never even attempted.
  ! grep -qF -- "BUCKETCLAIM-LIST" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "external S3 takes every coordinate from the CR, never a Secret/CR hybrid" {
  prep
  # The projector copies bucketName straight from the admin's source Secret and
  # never from backupStorage.bucketName, so the two can disagree outright (a
  # stale projection, or a Secret that simply names a different bucket). Taking
  # the CR's endpoint while keeping the Secret's bucket/region would assemble a
  # pairing nothing else in the system produces and could snapshot into a bucket
  # the platform's own backups never touch. The CR renders backupStorage.* and
  # already points at these same creds, so CR coordinates + these creds is what
  # this cluster's real BackupJobs use — take all four from it.
  export FAKE_CLAIM_CRD=0
  export FAKE_CLAIMS=""
  export FAKE_CREDS_ENDPOINT="minio.internal:9000"
  export FAKE_CREDS_BUCKET="stale-secret-bucket"
  export FAKE_CREDS_REGION="eu-west-9"
  export FAKE_CREDS_FPS="false"
  export FAKE_STRATEGY_ENDPOINT="http://minio.internal:9000"
  export FAKE_STRATEGY_BUCKET="platform-bucket"
  export FAKE_STRATEGY_REGION="us-east-1"
  export FAKE_STRATEGY_FPS="true"
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  [ "$rc" -eq 0 ]
  apply=$(grep -F -- "ETCD-MIGRATE --apply" "$FAKE_CMDLOG")
  echo "$apply" | grep -qF -- "--backup-s3-endpoint=http://minio.internal:9000"
  echo "$apply" | grep -qF -- "--backup-s3-bucket=platform-bucket"
  echo "$apply" | grep -qF -- "--backup-s3-region=us-east-1"
  echo "$apply" | grep -qF -- "--backup-s3-force-path-style"
  # Not one projected coordinate may leak into the external destination.
  ! echo "$apply" | grep -qF -- "stale-secret-bucket"
  ! echo "$apply" | grep -qF -- "eu-west-9"
  rm -rf "$WORK"
}

@test "COSI keeps the projected bucket even when the Strategy CR names another" {
  prep
  # Guard, not a bite: the CR-wholesale rule above is scoped to external S3. On
  # the COSI path the projected bucket is the COSI-assigned name, which the CR
  # can only reproduce through a live BucketClaim lookup that may never have run
  # (the very race this hook works around) — and bucketNameOverride immunity
  # depends on preferring the Secret. A future "simplify both paths to the CR"
  # must fail here.
  export FAKE_STRATEGY_BUCKET="cr-bucket-should-lose"
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  [ "$rc" -eq 0 ]
  apply=$(grep -F -- "ETCD-MIGRATE --apply" "$FAKE_CMDLOG")
  echo "$apply" | grep -qF -- "--backup-s3-bucket=cozy-backups-7f3a"
  ! echo "$apply" | grep -qF -- "cr-bucket-should-lose"
  rm -rf "$WORK"
}

@test "external S3 with a stale Terminating BucketClaim still keeps the CR scheme" {
  prep
  # The regression the name-keyed probe shipped: provisionBucket: false, but a
  # leftover BucketClaim is wedged Terminating — its cosi bucketclaim-protection
  # finalizer outlives the uninstalled COSI controller, so `kubectl get` on the
  # NAME keeps returning 0 forever. An existence-keyed probe reads that as COSI
  # and forces https on the admin's plaintext store, wedging the upgrade with no
  # escape hatch. Matching on .status.bucketName instead: the stale claim names
  # the OLD bucket, which cannot match the bucket the creds advertise.
  export FAKE_CLAIMS="tenant-root bucket-cozy-backups bucket-0bb5096a-stale 2026-07-17T09:00:00Z"
  export FAKE_CREDS_ENDPOINT="minio.internal:9000"
  export FAKE_CREDS_BUCKET="minio-bucket"
  export FAKE_STRATEGY_BUCKET="minio-bucket"
  export FAKE_STRATEGY_ENDPOINT="http://minio.internal:9000"
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  apply=$(grep -F -- "ETCD-MIGRATE --apply" "$FAKE_CMDLOG")
  echo "$apply" | grep -qF -- "--backup-s3-endpoint=http://minio.internal:9000"
  ! echo "$apply" | grep -qF -- "https://minio.internal:9000"
  rm -rf "$WORK"
}

@test "COSI with overridden bucket coordinates is still detected (no hardcoded names)" {
  prep
  # backupStorage.namespace / .bucketName are supported Package-CR overrides
  # (docs/operations/backup-classes.md), and this hook cannot see chart values —
  # nothing plumbs them into the migration Job's env. So the claim may sit in any
  # namespace under any name. Keying on the COSI-assigned .status.bucketName,
  # which is what the projector republishes, makes the probe independent of both.
  # A name-keyed probe would miss this claim, misclassify the cluster as
  # external, and fall back to the v1.5.x plaintext CR — the original P0.
  export FAKE_CLAIMS="tenant-custom bucket-my-backups cozy-backups-7f3a -"
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  apply=$(grep -F -- "ETCD-MIGRATE --apply" "$FAKE_CMDLOG")
  echo "$apply" | grep -qF -- "--backup-s3-endpoint=https://s3.example.com"
  ! echo "$apply" | grep -qF -- "seaweedfs-s3.tenant-root.svc.cozy.local:8333"
  rm -rf "$WORK"
}

@test "COSI is detected on a big multi-tenant cluster (match not last in the claim list)" {
  prep
  # Every other test runs with a single BucketClaim, which is exactly why a
  # first-match-exit consumer looks green: it only misbehaves once the rendered
  # list outgrows one pipe write (~8KB) AND the match is not the last line.
  # `kubectl get -A` sorts by namespace, so tenant-root lands mid-list on a real
  # cluster; this puts the match FIRST, the worst case.
  #
  # A `jq -r ... | grep -qxF` probe SIGPIPEs jq here (rc 141) and `set -o
  # pipefail` surfaces that instead of grep's 0, so the match reads as "not
  # COSI" -> external -> the v1.5.x plaintext :8333 CR endpoint -> the snapshot
  # fails and the upgrade is blocked, on precisely the large clusters that can
  # least afford it. Worse, it is a race: backoffLimit 3 can classify the same
  # cluster differently on each retry.
  export FAKE_CREDS_BUCKET="bucket-0bb5096a-956e-4630-91f4-e1d265de5f51"
  export FAKE_CLAIMS="tenant-root bucket-cozy-backups bucket-0bb5096a-956e-4630-91f4-e1d265de5f51 -"
  export FAKE_CLAIM_FILLER=300
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  [ "$rc" -eq 0 ]
  apply=$(grep -F -- "ETCD-MIGRATE --apply" "$FAKE_CMDLOG")
  echo "$apply" | grep -qF -- "--backup-s3-endpoint=https://s3.example.com"
  echo "$apply" | grep -qF -- "--backup-s3-bucket=bucket-0bb5096a-956e-4630-91f4-e1d265de5f51"
  # The v1.5.x CR endpoint must never be what a large cluster falls back to.
  ! echo "$apply" | grep -qF -- "seaweedfs-s3.tenant-root.svc.cozy.local:8333"
  rm -rf "$WORK"
}

@test "a TERMINATING claim on the matching bucket is ambiguous: refuse, do not guess" {
  prep
  # The state neither reading survives: an admin moved COSI -> external S3
  # REUSING the bucket name (backup continuity), and the old claim is wedged
  # Terminating because COSI is gone. Reading the corpse as ownership forces
  # https on a plaintext store; reading it as "external" would, on a cluster
  # that is still COSI with a mid-recreate claim, resurrect the plaintext-:8333
  # P0. Both are wrong half the time and both fail as a confusing handshake
  # error, so the guess buys nothing — refuse and name the ambiguity.
  export FAKE_CLAIMS="tenant-root bucket-cozy-backups minio-bucket 2026-07-17T09:00:00Z"
  export FAKE_CREDS_BUCKET="minio-bucket"
  export FAKE_CREDS_ENDPOINT="minio.internal:9000"
  export FAKE_STRATEGY_ENDPOINT="http://minio.internal:9000"
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  grep -qF -- "TERMINATING BucketClaim" "$WORK/out"
  grep -qF -- "could not determine" "$WORK/out"
  # It must not have silently taken either branch.
  ! grep -qF -- "treating as external S3" "$WORK/out"
  ! grep -qF -- "forcing https" "$WORK/out"
  # Nothing destructive ran.
  ! grep -qF -- "SCALE 0" "$FAKE_CMDLOG"
  ! grep -qF -- "ETCD-MIGRATE --apply" "$FAKE_CMDLOG"
  ! grep -qF -- "STAMP" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a LIVE claim wins over an unrelated Terminating one (ambiguity is not over-applied)" {
  prep
  # Guard, not a bite: the refusal above must fire ONLY when the match is
  # exclusively Terminating. A healthy COSI cluster that also carries some dead
  # claim must still resolve normally — an over-broad "any Terminating claim is
  # ambiguous" rule would wedge ordinary upgrades.
  export FAKE_CLAIMS="tenant-root bucket-cozy-backups cozy-backups-7f3a -
tenant-old bucket-dead bucket-dead-uuid 2026-07-17T09:00:00Z"
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  [ "$rc" -eq 0 ]
  apply=$(grep -F -- "ETCD-MIGRATE --apply" "$FAKE_CMDLOG")
  echo "$apply" | grep -qF -- "--backup-s3-endpoint=https://s3.example.com"
  ! grep -qF -- "TERMINATING BucketClaim" "$WORK/out"
  rm -rf "$WORK"
}

@test "an uninterpretable claim list fails loud rather than reading as external" {
  prep
  # No producer emits this — kubectl always renders a List with an items array —
  # but without a shape gate every unexpected document reads as "no claim owns
  # the bucket" = external = the plaintext-:8333 P0, silently. Refuse instead.
  export FAKE_CLAIM_LIST_SHAPE=items-not-array
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  [ "$rc" -ne 0 ]
  grep -qF -- "unexpected BucketClaim list shape" "$WORK/out"
  ! grep -qF -- "treating as external S3" "$WORK/out"
  ! grep -qF -- "ETCD-MIGRATE --apply" "$FAKE_CMDLOG"
  ! grep -qF -- "STAMP" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "an unreadable BucketClaim API fails loud instead of guessing external S3" {
  prep
  # A transient CRD read error is NOT the same answer as "the CRD is absent".
  # Treating it as absent classifies a COSI cluster as external and snapshots to
  # the unreachable v1.5 in-cluster endpoint — the original P0, resurrected by an
  # apiserver blip. Refuse to classify instead: a loud failure is recoverable
  # (the Job retries), a wrong classification is not.
  export FAKE_CLAIM_CRD=error
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  grep -qF -- "could not determine" "$WORK/out"
  grep -qF -- "refusing to adopt" "$WORK/out"
  # It must NOT have silently taken the external path.
  ! grep -qF -- "treating as external S3" "$WORK/out"
  # Nothing destructive ran.
  ! grep -qF -- "SCALE 0" "$FAKE_CMDLOG"
  ! grep -qF -- "ETCD-MIGRATE --apply" "$FAKE_CMDLOG"
  ! grep -qF -- "STAMP" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "an unreadable claim LIST fails loud rather than classifying as external" {
  prep
  # Same reasoning one level down: the CRD exists (COSI is installed), so a
  # failing list read cannot mean "no COSI here".
  export FAKE_CLAIM_LIST_ERR=1
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  [ "$rc" -ne 0 ]
  grep -qF -- "could not determine" "$WORK/out"
  ! grep -qF -- "ETCD-MIGRATE --apply" "$FAKE_CMDLOG"
  ! grep -qF -- "STAMP" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "COSI without a projected endpoint falls back to the CR, never a bare https://" {
  prep
  # Defensive: the projector cannot produce this (it fails
  # ReasonSourceMalformed rather than omit the endpoint), but forcing the scheme
  # onto an empty host yields the literal "https://", which is non-empty and so
  # sails through the final resolution guard and reaches etcd-migrate as a
  # destination. origin/main degraded to the CR here; so must this.
  export FAKE_CREDS_ENDPOINT=""
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  apply=$(grep -F -- "ETCD-MIGRATE --apply" "$FAKE_CMDLOG")
  # No scheme-only endpoint reaches etcd-migrate...
  ! echo "$apply" | grep -qE -- "--backup-s3-endpoint=https://( |$)"
  # ...and the CR supplies the real one instead.
  echo "$apply" | grep -qF -- "--backup-s3-endpoint=http://seaweedfs-s3.tenant-root.svc.cozy.local:8333"
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

@test "ETCD_ADOPT_SKIP_BACKUP=1 adopts with --skip-backup and stages no creds" {
  prep
  # The supported escape hatch: an operator who accepts adopting without a
  # snapshot sets ETCD_ADOPT_SKIP_BACKUP=1 (distinct from the inbound
  # ETCD_MIGRATE_BACKUP_ARGS above, which cannot suppress the snapshot). --apply
  # then carries --skip-backup, no creds are staged, but adoption still proceeds.
  export ETCD_ADOPT_SKIP_BACKUP=1
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  apply=$(grep -F -- "ETCD-MIGRATE --apply" "$FAKE_CMDLOG")
  echo "$apply" | grep -qF -- "--skip-backup"
  ! echo "$apply" | grep -qF -- "--backup-s3-endpoint"
  # No snapshot Job runs, so no credentials are staged.
  ! grep -qF -- "STAGE " "$FAKE_CMDLOG"
  # Adoption still runs and stamps: scale down -> --apply -> scale up -> stamp.
  grep -qF -- "SCALE 0" "$FAKE_CMDLOG"
  grep -qF -- "STAMP" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "ETCD_ADOPT_SKIP_BACKUP=true is honoured, not silently ignored" {
  prep
  # The platform renders this var through migrations.etcdAdoptSkipBackup and
  # always emits "1", but an operator setting it by hand will reasonably type
  # "true". An exact match on "1" would accept that, show it in the Job spec and
  # do nothing — a safety valve that is set but silently ignored is worse than
  # one that was never offered, because the operator believes they opted in.
  export ETCD_ADOPT_SKIP_BACKUP=true
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  [ "$rc" -eq 0 ]
  apply=$(grep -F -- "ETCD-MIGRATE --apply" "$FAKE_CMDLOG")
  echo "$apply" | grep -qF -- "--skip-backup"
  ! echo "$apply" | grep -qF -- "--backup-s3-endpoint"
  ! grep -qF -- "STAGE " "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "an unrecognised ETCD_ADOPT_SKIP_BACKUP value does NOT skip the snapshot" {
  prep
  # Skipping the snapshot on live etcd must require an affirmative answer. A
  # typo, a stray value, or a future template bug rendering something unexpected
  # must fall back to the safe side and still take the snapshot.
  export ETCD_ADOPT_SKIP_BACKUP=banana
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  [ "$rc" -eq 0 ]
  apply=$(grep -F -- "ETCD-MIGRATE --apply" "$FAKE_CMDLOG")
  ! echo "$apply" | grep -qF -- "--skip-backup"
  echo "$apply" | grep -qF -- "--backup-s3-endpoint=https://s3.example.com"
  rm -rf "$WORK"
}

@test "ETCD_ADOPT_SKIP_BACKUP=1 unblocks adoption when no backup target resolves" {
  prep
  # The narrowing the reviewer asked for: a cluster with legacy etcd but no
  # resolvable backup target (backups disabled / external S3 without staged
  # creds / SeaweedFS not ready) is no longer a total upgrade block when the
  # operator opts into skipping the snapshot. Resolution is not even attempted,
  # so this passes regardless of why the target would be unresolvable.
  export FAKE_STRATEGY=0
  export FAKE_CREDS_COORDS=0
  export ETCD_ADOPT_SKIP_BACKUP=1
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  ! grep -qF -- "refusing to adopt" "$WORK/out"
  apply=$(grep -F -- "ETCD-MIGRATE --apply" "$FAKE_CMDLOG")
  echo "$apply" | grep -qF -- "--skip-backup"
  grep -qF -- "STAMP" "$FAKE_CMDLOG"
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

@test "no legacy CRD stamps version 51 without adopting" {
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

@test "cert SAN check is exact: a superstring dnsName does not skip the re-issue patch" {
  prep
  # The Certificate's spec.dnsNames already carries *.etcd.<ns>.svc.cluster.local,
  # which CONTAINS the native wildcard *.etcd.<ns>.svc as a substring but is not
  # an exact match. A substring match (the bug) would treat the wildcard as
  # already present and skip the patch; the exact match must still re-issue.
  export FAKE_CERTS=1
  export FAKE_CERT_SUPERSTRING=1
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  # Both Certificates are still patched despite the substring-only SAN.
  grep -qF -- "PATCH-CERT etcd-server" "$FAKE_CMDLOG"
  grep -qF -- "PATCH-CERT etcd-peer" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "etcd-migrate authenticates via a synthesized in-cluster kubeconfig at kubernetes.default.svc" {
  prep
  # etcd-migrate only reads a kubeconfig FILE (no in-cluster SA fallback), so the
  # script synthesizes one from the mounted ServiceAccount. Point the SA dir at a
  # fixture and capture the written kubeconfig.
  sa="$WORK/sa"; mkdir -p "$sa"
  printf 'faketoken'   > "$sa/token"
  printf 'fakeca'      > "$sa/ca.crt"
  printf 'cozy-system' > "$sa/namespace"
  export ETCD_ADOPT_SA_DIR="$sa"
  export ETCD_MIGRATE_KUBECONFIG="$WORK/in-cluster.kubeconfig"
  # A bare IPv6 host that must NOT end up in the (unbracketed, invalid) server URL.
  export KUBERNETES_SERVICE_HOST="fd00::1"
  export KUBERNETES_SERVICE_PORT="443"
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  # Both the dry-run and --apply invocations carry --kubeconfig=<synthesized>.
  [ "$(grep -cF -- "--kubeconfig=$WORK/in-cluster.kubeconfig" "$FAKE_CMDLOG")" -ge 2 ]
  # The kubeconfig is IP-family-agnostic (DNS name, not the bare IPv6 host) and
  # authenticates via the SA token file + CA.
  [ -s "$ETCD_MIGRATE_KUBECONFIG" ]
  grep -qF -- "server: https://kubernetes.default.svc" "$ETCD_MIGRATE_KUBECONFIG"
  ! grep -qF -- "fd00::1" "$ETCD_MIGRATE_KUBECONFIG"
  grep -qF -- "tokenFile: $sa/token" "$ETCD_MIGRATE_KUBECONFIG"
  grep -qF -- "certificate-authority: $sa/ca.crt" "$ETCD_MIGRATE_KUBECONFIG"
  rm -rf "$WORK"
}
