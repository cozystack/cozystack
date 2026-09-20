// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	"github.com/cozystack/cozystack/internal/backupcontroller/mongodbapp"
	"github.com/cozystack/cozystack/internal/backupcontroller/psmdbtypes"
)

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

func TestValidateMongoDBApplicationRef(t *testing.T) {
	apps := mongodbapp.GroupName
	other := "other.example.com"
	cases := []struct {
		name    string
		ref     corev1.TypedLocalObjectReference
		wantErr bool
	}{
		{"happy path with apps group", corev1.TypedLocalObjectReference{Kind: "MongoDB", Name: "x", APIGroup: &apps}, false},
		{"empty apiGroup is accepted", corev1.TypedLocalObjectReference{Kind: "MongoDB", Name: "x"}, false},
		{"foreign apiGroup rejected", corev1.TypedLocalObjectReference{Kind: "MongoDB", Name: "x", APIGroup: &other}, true},
		{"wrong kind rejected", corev1.TypedLocalObjectReference{Kind: "MariaDB", Name: "x", APIGroup: &apps}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateMongoDBApplicationRef(tc.ref)
			if tc.wantErr && err == nil {
				t.Fatalf("expected validation error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Defaults
// ---------------------------------------------------------------------------

func TestPsmdbStorageAndTypeDefaults(t *testing.T) {
	if got := psmdbStorageNameOrDefault(""); got != psmdbDefaultStorageName {
		t.Errorf("psmdbStorageNameOrDefault(\"\"): got %q want %q", got, psmdbDefaultStorageName)
	}
	if got := psmdbStorageNameOrDefault("custom"); got != "custom" {
		t.Errorf("psmdbStorageNameOrDefault(custom): got %q", got)
	}
	if got := psmdbBackupTypeOrDefault(""); got != psmdbtypes.BackupTypeLogical {
		t.Errorf("psmdbBackupTypeOrDefault(\"\"): got %q want logical", got)
	}
	if got := psmdbBackupTypeOrDefault("logical"); got != "logical" {
		t.Errorf("psmdbBackupTypeOrDefault(logical): got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Precondition gate
// ---------------------------------------------------------------------------

func TestPsmdbBackupPrecondition(t *testing.T) {
	t.Run("backups disabled", func(t *testing.T) {
		cluster := &psmdbtypes.PerconaServerMongoDB{
			Spec: psmdbtypes.PerconaServerMongoDBSpec{
				Backup: psmdbtypes.PerconaServerMongoDBBackupConfig{Enabled: false},
			},
		}
		if msg := psmdbBackupPrecondition(cluster, "s3-storage"); msg == "" {
			t.Fatal("expected a precondition message when backups are disabled")
		}
	})
	t.Run("storage not declared", func(t *testing.T) {
		cluster := &psmdbtypes.PerconaServerMongoDB{
			Spec: psmdbtypes.PerconaServerMongoDBSpec{
				Backup: psmdbtypes.PerconaServerMongoDBBackupConfig{
					Enabled:  true,
					Storages: map[string]runtime.RawExtension{"other": {}},
				},
			},
		}
		if msg := psmdbBackupPrecondition(cluster, "s3-storage"); msg == "" {
			t.Fatal("expected a precondition message when the named storage is missing")
		}
	})
	t.Run("ready", func(t *testing.T) {
		cluster := &psmdbtypes.PerconaServerMongoDB{
			Spec: psmdbtypes.PerconaServerMongoDBSpec{
				Backup: psmdbtypes.PerconaServerMongoDBBackupConfig{
					Enabled:  true,
					Storages: map[string]runtime.RawExtension{"s3-storage": {}},
				},
			},
		}
		if msg := psmdbBackupPrecondition(cluster, "s3-storage"); msg != "" {
			t.Fatalf("expected no precondition message, got %q", msg)
		}
	})
}

// ---------------------------------------------------------------------------
// Restore credentials re-pointed at the target cluster (DR after source delete)
// ---------------------------------------------------------------------------

// TestPsmdbTargetCredentialsSecret pins the fix for the disaster-recovery gap:
// a restore into a differently-named target must authenticate with the TARGET
// cluster's own S3 credentials, because the source release's -s3-creds Secret is
// deleted together with the source app. The selector prefers the source's
// storage name, then the chart default, then a sole storage.
func TestPsmdbTargetCredentialsSecret(t *testing.T) {
	s3Storage := func(bucket, secret string) runtime.RawExtension {
		return runtime.RawExtension{Raw: []byte(fmt.Sprintf(`{"type":"s3","s3":{"bucket":%q,"credentialsSecret":%q}}`, bucket, secret))}
	}
	clusterWith := func(storages map[string]runtime.RawExtension) *psmdbtypes.PerconaServerMongoDB {
		return &psmdbtypes.PerconaServerMongoDB{
			Spec: psmdbtypes.PerconaServerMongoDBSpec{
				Backup: psmdbtypes.PerconaServerMongoDBBackupConfig{Storages: storages},
			},
		}
	}
	cases := []struct {
		name       string
		storages   map[string]runtime.RawExtension
		preferred  string
		wantBucket string
		want       string
	}{
		{
			name:       "preferred storage on the source bucket wins",
			storages:   map[string]runtime.RawExtension{"s3-storage": s3Storage("shared", "target-creds"), "other": s3Storage("shared", "other-creds")},
			preferred:  "s3-storage",
			wantBucket: "shared",
			want:       "target-creds",
		},
		{
			name:       "falls back to chart default when preferred absent",
			storages:   map[string]runtime.RawExtension{"s3-storage": s3Storage("shared", "target-creds")},
			preferred:  "nonexistent",
			wantBucket: "shared",
			want:       "target-creds",
		},
		{
			name:       "sole non-default storage on the source bucket is used",
			storages:   map[string]runtime.RawExtension{"custom": s3Storage("shared", "custom-creds")},
			wantBucket: "shared",
			want:       "custom-creds",
		},
		{
			name:       "different bucket is NOT adopted (cross-flow guard)",
			storages:   map[string]runtime.RawExtension{"s3-storage": s3Storage("platform-bucket", "cozy-backups-creds")},
			preferred:  "s3-storage",
			wantBucket: "tenant-bucket",
			want:       "",
		},
		{
			name:       "empty wantBucket yields empty (unknown source bucket, no swap)",
			storages:   map[string]runtime.RawExtension{"s3-storage": s3Storage("shared", "target-creds")},
			preferred:  "s3-storage",
			wantBucket: "",
			want:       "",
		},
		{
			name:       "ambiguous (multiple, none default, no preferred) yields empty",
			storages:   map[string]runtime.RawExtension{"a": s3Storage("shared", "a-creds"), "b": s3Storage("shared", "b-creds")},
			wantBucket: "shared",
			want:       "",
		},
		{
			name:       "no storages yields empty",
			storages:   nil,
			wantBucket: "shared",
			want:       "",
		},
		{
			name:       "non-s3 storage yields empty",
			storages:   map[string]runtime.RawExtension{"s3-storage": {Raw: []byte(`{"type":"azure","azure":{"container":"c"}}`)}},
			wantBucket: "shared",
			want:       "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := psmdbTargetCredentialsSecret(clusterWith(tc.storages), tc.preferred, tc.wantBucket); got != tc.want {
				t.Errorf("psmdbTargetCredentialsSecret: got %q want %q", got, tc.want)
			}
		})
	}
}

// TestMongoDBRestoreCredentialsSecret pins the restore credential-swap decision
// across the four {legacy,system}×{legacy,system} flow combinations, so neither
// cross-flow guard can be dropped unnoticed: the projected-secret skip (a system
// source keeps cozy-backups-creds rather than adopting a legacy target's own
// Secret) and the same-bucket match (a legacy source is not handed a credential
// for a bucket its archive does not live in).
func TestMongoDBRestoreCredentialsSecret(t *testing.T) {
	s3Storage := func(bucket, secret string) runtime.RawExtension {
		return runtime.RawExtension{Raw: []byte(fmt.Sprintf(`{"type":"s3","s3":{"bucket":%q,"credentialsSecret":%q}}`, bucket, secret))}
	}
	target := func(storages map[string]runtime.RawExtension) *psmdbtypes.PerconaServerMongoDB {
		return &psmdbtypes.PerconaServerMongoDB{
			Spec: psmdbtypes.PerconaServerMongoDBSpec{
				Backup: psmdbtypes.PerconaServerMongoDBBackupConfig{Storages: storages},
			},
		}
	}
	src := func(bucket, cred string) *psmdbtypes.BackupSource {
		return &psmdbtypes.BackupSource{StorageName: "s3-storage", S3: &psmdbtypes.BackupStorageS3{Bucket: bucket, CredentialsSecret: cred}}
	}
	cases := []struct {
		name   string
		source *psmdbtypes.BackupSource
		target *psmdbtypes.PerconaServerMongoDB
		want   string
	}{
		{
			name:   "legacy source -> legacy target on the same bucket: swap to target creds (DR after source delete)",
			source: src("tenant-bucket", "src-s3-creds"),
			target: target(map[string]runtime.RawExtension{"s3-storage": s3Storage("tenant-bucket", "target-s3-creds")}),
			want:   "target-s3-creds",
		},
		{
			name:   "legacy source -> legacy target on a different bucket: keep source creds (no cross-bucket swap)",
			source: src("tenant-bucket", "src-s3-creds"),
			target: target(map[string]runtime.RawExtension{"s3-storage": s3Storage("other-bucket", "target-s3-creds")}),
			want:   "src-s3-creds",
		},
		{
			name:   "system source -> legacy target: keep projected cozy-backups-creds (outlives source, reads the platform bucket)",
			source: src("cozy-backups-PLATFORM", "cozy-backups-creds"),
			target: target(map[string]runtime.RawExtension{"s3-storage": s3Storage("tenant-bucket", "target-s3-creds")}),
			want:   "cozy-backups-creds",
		},
		{
			name:   "legacy source -> system-bucket target: keep source creds (target storage is on the platform bucket, not the archive's)",
			source: src("tenant-bucket", "src-s3-creds"),
			target: target(map[string]runtime.RawExtension{"s3-storage": s3Storage("cozy-backups-PLATFORM", "cozy-backups-creds")}),
			want:   "src-s3-creds",
		},
		{
			// Reaches the projected-secret early return specifically: the target
			// declares a storage on the SAME bucket as the archive under a
			// different credentialsSecret, so without the "already cozy-backups-
			// creds" short-circuit the same-bucket swap would re-point the restore
			// at the operator-managed secret. Keeping the projected secret is
			// correct — it outlives the source app and reads the platform bucket.
			name:   "system source -> target with same-bucket storage under other creds: keep projected cozy-backups-creds",
			source: src("cozy-backups-PLATFORM", "cozy-backups-creds"),
			target: target(map[string]runtime.RawExtension{"s3-storage": s3Storage("cozy-backups-PLATFORM", "operator-managed-creds")}),
			want:   "cozy-backups-creds",
		},
		{
			name:   "no s3 source: empty (caller keeps the source reference)",
			source: &psmdbtypes.BackupSource{StorageName: "s3-storage"},
			target: target(map[string]runtime.RawExtension{"s3-storage": s3Storage("tenant-bucket", "target-s3-creds")}),
			want:   "",
		},
		{
			// Source archive with no bucket recorded (wantBucket==""): the swap
			// must be refused even against a lone target storage that also has an
			// empty bucket, keeping the source reference. Pins the wantBucket==""
			// short-circuit — without it that sole storage would be adopted.
			name:   "no-bucket source: keep source creds against an empty-bucket target storage",
			source: src("", "src-s3-creds"),
			target: target(map[string]runtime.RawExtension{"only": s3Storage("", "empty-bucket-creds")}),
			want:   "src-s3-creds",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mongodbRestoreCredentialsSecret(tc.source, tc.target); got != tc.want {
				t.Errorf("mongodbRestoreCredentialsSecret: got %q want %q", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Backup-side ensure idempotency + CR shape
// ---------------------------------------------------------------------------

func TestEnsureMongoDBBackup_IdempotentByLabel(t *testing.T) {
	c := newMongoDBStrategyTestClient(t)
	r := &BackupJobReconciler{Client: c, Scheme: c.Scheme()}
	job := newMongoDBBackupJob("bj-1", "tenant")
	if err := c.Create(context.Background(), job); err != nil {
		t.Fatalf("seed BackupJob: %v", err)
	}
	rendered := newRenderedMongoDBTemplate()
	cluster := mongodbNameForApp("mongodb-src")

	first, err := r.ensureMongoDBBackup(context.Background(), job, cluster, "s3-storage", rendered, false)
	if err != nil {
		t.Fatalf("first ensureMongoDBBackup: %v", err)
	}
	second, err := r.ensureMongoDBBackup(context.Background(), job, cluster, "s3-storage", rendered, false)
	if err != nil {
		t.Fatalf("second ensureMongoDBBackup: %v", err)
	}
	if first.Name != second.Name {
		t.Errorf("expected idempotent reuse: first=%q second=%q", first.Name, second.Name)
	}

	list := &psmdbtypes.PerconaServerMongoDBBackupList{}
	if err := c.List(context.Background(), list, client.InNamespace("tenant")); err != nil {
		t.Fatalf("list backups: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("expected exactly one operator Backup CR, got %d", len(list.Items))
	}
	got := &list.Items[0]
	// The cozystack mongodb ApplicationDefinition prefixes the release name with
	// "mongodb-", so a BackupJob targeting applicationRef.name=mongodb-src is
	// reconciled against psmdb.percona.com/PerconaServerMongoDB named
	// mongodb-mongodb-src.
	if got.Spec.ClusterName != "mongodb-mongodb-src" {
		t.Errorf("ClusterName: got %q want mongodb-mongodb-src", got.Spec.ClusterName)
	}
	if got.Spec.StorageName != "s3-storage" {
		t.Errorf("StorageName: got %q want s3-storage", got.Spec.StorageName)
	}
	if got.Spec.Type != psmdbtypes.BackupTypeLogical {
		t.Errorf("Type: got %q want logical", got.Spec.Type)
	}
	if got.Spec.CompressionType != "gzip" {
		t.Errorf("CompressionType: got %q want gzip", got.Spec.CompressionType)
	}
	if got.Labels[backupsv1alpha1.OwningJobNameLabel] != "bj-1" {
		t.Errorf("OwningJobName label missing or wrong: %v", got.Labels)
	}
	// Legacy flow: the archive is the tenant's, so the driver must NOT stamp the
	// delete-backup finalizer that would prune it on CR deletion.
	for _, f := range got.Finalizers {
		if f == psmdbDeleteBackupFinalizer {
			t.Errorf("legacy backup must not carry %q finalizer; got %v", psmdbDeleteBackupFinalizer, got.Finalizers)
		}
	}
}

func TestEnsureMongoDBBackup_SystemBucketStampsDeleteFinalizer(t *testing.T) {
	c := newMongoDBStrategyTestClient(t)
	r := &BackupJobReconciler{Client: c, Scheme: c.Scheme()}
	job := newMongoDBBackupJob("bj-sb", "tenant")
	if err := c.Create(context.Background(), job); err != nil {
		t.Fatalf("seed BackupJob: %v", err)
	}
	got, err := r.ensureMongoDBBackup(context.Background(), job, "mongodb-app", "s3-storage", newRenderedMongoDBTemplate(), true)
	if err != nil {
		t.Fatalf("ensureMongoDBBackup: %v", err)
	}
	found := false
	for _, f := range got.Finalizers {
		if f == psmdbDeleteBackupFinalizer {
			found = true
		}
	}
	if !found {
		// Without it, deleting the Backup on Plan retention would orphan the
		// object in the shared cozy-backups bucket forever.
		t.Errorf("system-bucket backup must carry %q finalizer so its archive is pruned on delete; got %v", psmdbDeleteBackupFinalizer, got.Finalizers)
	}
}

// ---------------------------------------------------------------------------
// Restore-side ensure idempotency + backupSource wiring
// ---------------------------------------------------------------------------

func TestEnsureMongoDBRestore_IdempotentByLabel(t *testing.T) {
	c := newMongoDBStrategyTestClient(t)
	r := &RestoreJobReconciler{Client: c, Scheme: c.Scheme()}
	rj := newMongoDBRestoreJob("rj-1", "tenant")
	if err := c.Create(context.Background(), rj); err != nil {
		t.Fatalf("seed RestoreJob: %v", err)
	}
	fps := true
	source := &psmdbtypes.BackupSource{
		Type:        psmdbtypes.BackupTypeLogical,
		Destination: "s3://bkt/mongodb-src/2026-08-05T00:00:00Z",
		S3: &psmdbtypes.BackupStorageS3{
			Bucket:            "bkt",
			CredentialsSecret: "mongodb-mongodb-src-s3-creds",
			EndpointURL:       "https://seaweedfs-s3.tenant-root:8333",
			ForcePathStyle:    &fps,
		},
	}

	first, err := r.ensureMongoDBRestore(context.Background(), rj, "mongodb-mongodb-target", source, nil)
	if err != nil {
		t.Fatalf("first ensureMongoDBRestore: %v", err)
	}
	second, err := r.ensureMongoDBRestore(context.Background(), rj, "mongodb-mongodb-target", source, nil)
	if err != nil {
		t.Fatalf("second ensureMongoDBRestore: %v", err)
	}
	if first.Name != second.Name {
		t.Errorf("expected idempotent reuse: first=%q second=%q", first.Name, second.Name)
	}
	list := &psmdbtypes.PerconaServerMongoDBRestoreList{}
	if err := c.List(context.Background(), list, client.InNamespace("tenant")); err != nil {
		t.Fatalf("list restores: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("expected exactly one operator Restore CR, got %d", len(list.Items))
	}
	got := &list.Items[0]
	if got.Spec.ClusterName != "mongodb-mongodb-target" {
		t.Errorf("ClusterName: got %q want mongodb-mongodb-target", got.Spec.ClusterName)
	}
	if got.Spec.BackupSource == nil || got.Spec.BackupSource.Destination != source.Destination {
		t.Errorf("BackupSource mismatch: %#v", got.Spec.BackupSource)
	}
	if got.Spec.BackupSource.S3 == nil || got.Spec.BackupSource.S3.Bucket != "bkt" {
		t.Errorf("BackupSource.S3 mismatch: %#v", got.Spec.BackupSource.S3)
	}
}

// ---------------------------------------------------------------------------
// resolveMongoDBRestoreTarget (in-place vs to-copy)
// ---------------------------------------------------------------------------

func TestResolveMongoDBRestoreTarget(t *testing.T) {
	apps := mongodbapp.GroupName
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "src-bk"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{
				Kind: "MongoDB", Name: "mongodb-src", APIGroup: &apps,
			},
		},
	}

	t.Run("in-place: missing targetApplicationRef inherits source", func(t *testing.T) {
		rj := &backupsv1alpha1.RestoreJob{Spec: backupsv1alpha1.RestoreJobSpec{}}
		r := &RestoreJobReconciler{}
		got := r.resolveMongoDBRestoreTarget(rj, backup)
		if got.AppName != "mongodb-src" || got.Kind != "MongoDB" {
			t.Errorf("in-place target: got %+v want AppName=mongodb-src Kind=MongoDB", got)
		}
	})

	t.Run("to-copy: targetApplicationRef wins over source", func(t *testing.T) {
		rj := &backupsv1alpha1.RestoreJob{
			Spec: backupsv1alpha1.RestoreJobSpec{
				TargetApplicationRef: &corev1.TypedLocalObjectReference{
					Kind: "MongoDB", Name: "mongodb-target", APIGroup: &apps,
				},
			},
		}
		r := &RestoreJobReconciler{}
		got := r.resolveMongoDBRestoreTarget(rj, backup)
		if got.AppName != "mongodb-target" {
			t.Errorf("to-copy AppName: got %q want mongodb-target", got.AppName)
		}
	})
}

// ---------------------------------------------------------------------------
// Restore options + PITR
// ---------------------------------------------------------------------------

func TestParseMongoDBRestoreOptionsAndPITR(t *testing.T) {
	t.Run("empty options are permissive", func(t *testing.T) {
		o, unknown, err := parseMongoDBRestoreOptions(nil)
		if err != nil {
			t.Fatalf("parse nil options: %v", err)
		}
		if len(unknown) != 0 {
			t.Errorf("no unknown keys expected, got %v", unknown)
		}
		if o.effectiveRestoreDeadline() != psmdbDefaultRestoreDeadline {
			t.Errorf("default deadline mismatch: %v", o.effectiveRestoreDeadline())
		}
		pitr, err := o.pitrSpec()
		if err != nil || pitr != nil {
			t.Errorf("no pitr expected: pitr=%v err=%v", pitr, err)
		}
	})
	t.Run("malformed json errors", func(t *testing.T) {
		if _, _, err := parseMongoDBRestoreOptions(&runtime.RawExtension{Raw: []byte("{not-json")}); err == nil {
			t.Fatal("expected decode error")
		}
	})
	t.Run("known keys parse with no unknowns", func(t *testing.T) {
		o, unknown, err := parseMongoDBRestoreOptions(&runtime.RawExtension{
			Raw: []byte(`{"recoveryTime":"2026-08-05T12:34:56Z","restoreTimeoutSeconds":600}`),
		})
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if len(unknown) != 0 {
			t.Errorf("expected no unknown keys, got %v", unknown)
		}
		if o.RecoveryTime != "2026-08-05T12:34:56Z" || o.RestoreTimeoutSeconds != 600 {
			t.Errorf("options mismatch: %#v", o)
		}
	})
	t.Run("unknown keys are reported (typo guard)", func(t *testing.T) {
		_, unknown, err := parseMongoDBRestoreOptions(&runtime.RawExtension{
			Raw: []byte(`{"recoverytime":"oops","bogus":1}`),
		})
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		// sorted: bogus, recoverytime
		if len(unknown) != 2 || unknown[0] != "bogus" || unknown[1] != "recoverytime" {
			t.Errorf("unknown keys: got %v want [bogus recoverytime]", unknown)
		}
	})
	t.Run("recoveryTime RFC3339 maps to psmdb date in UTC", func(t *testing.T) {
		// A non-UTC offset must be normalised to UTC in the psmdb date format.
		o := MongoDBRestoreOptions{RecoveryTime: "2026-08-05T14:34:56+02:00"}
		pitr, err := o.pitrSpec()
		if err != nil {
			t.Fatalf("pitrSpec: %v", err)
		}
		if pitr == nil || pitr.Type != "date" || pitr.Date != "2026-08-05 12:34:56" {
			t.Errorf("pitr mismatch: %#v (want date=2026-08-05 12:34:56)", pitr)
		}
	})
	t.Run("empty recoveryTime yields no pitr (snapshot restore)", func(t *testing.T) {
		pitr, err := MongoDBRestoreOptions{}.pitrSpec()
		if err != nil || pitr != nil {
			t.Errorf("expected nil pitr: pitr=%#v err=%v", pitr, err)
		}
	})
	t.Run("malformed recoveryTime errors terminally", func(t *testing.T) {
		if _, err := (MongoDBRestoreOptions{RecoveryTime: "2026-08-05 12:34:56"}).pitrSpec(); err == nil {
			t.Fatal("expected error for non-RFC3339 recoveryTime")
		}
	})
	t.Run("custom deadline honoured", func(t *testing.T) {
		o := MongoDBRestoreOptions{RestoreTimeoutSeconds: 120}
		if o.effectiveRestoreDeadline().Seconds() != 120 {
			t.Errorf("deadline: got %v want 120s", o.effectiveRestoreDeadline())
		}
	})
	// A non-string recoveryTime (spec.options is free-form runtime.RawExtension,
	// so {"recoveryTime": 20260805} passes admission) must be treated as a
	// load-bearing decode failure: the caller fails the RestoreJob terminally
	// rather than silently degrading the PITR request to a snapshot restore. The
	// two conditions the caller ANDs are (a) the typed decode errors and (b) the
	// recoveryTime key is present.
	t.Run("non-string recoveryTime is a load-bearing decode failure (major-2 guard)", func(t *testing.T) {
		opts := &runtime.RawExtension{Raw: []byte(`{"recoveryTime":20260805}`)}
		if _, _, err := parseMongoDBRestoreOptions(opts); err == nil {
			t.Fatal("expected a typed-decode error for a numeric recoveryTime")
		}
		if !restoreOptionsCarryRecoveryTimeKey(opts) {
			t.Fatal("expected the recoveryTime key to be detected so the caller fails terminally")
		}
	})
	t.Run("recoveryTime-key detection is scoped to object blobs carrying the key", func(t *testing.T) {
		if restoreOptionsCarryRecoveryTimeKey(nil) {
			t.Error("nil options must report no recoveryTime key")
		}
		if restoreOptionsCarryRecoveryTimeKey(&runtime.RawExtension{Raw: []byte(`{"restoreTimeoutSeconds":30}`)}) {
			t.Error("a blob without recoveryTime must report no key (benign fall-back applies)")
		}
		if restoreOptionsCarryRecoveryTimeKey(&runtime.RawExtension{Raw: []byte(`"garbage`)}) {
			t.Error("a non-object blob must report no key (no recoveryTime to honour)")
		}
	})
}

// ---------------------------------------------------------------------------
// Snapshot round-trip
// ---------------------------------------------------------------------------

func TestMarshalUnmarshalMongoDBBackupSnapshot_RoundTrip(t *testing.T) {
	fps := true
	mdbBackup := &psmdbtypes.PerconaServerMongoDBBackup{
		Status: psmdbtypes.PerconaServerMongoDBBackupStatus{
			Destination: "s3://bkt/mongodb-src/2026-08-05T00:00:00Z",
			S3: &psmdbtypes.BackupStorageS3{
				Bucket:            "bkt",
				CredentialsSecret: "mongodb-mongodb-src-s3-creds",
				EndpointURL:       "https://seaweedfs-s3.tenant-root:8333",
				ForcePathStyle:    &fps,
			},
		},
	}
	rendered := &strategyv1alpha1.MongoDBTemplate{Type: "logical"}
	raw, err := marshalMongoDBBackupSnapshot(mdbBackup, rendered, "s3-storage", map[string]string{"k": "v"}, false)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	snap, err := unmarshalMongoDBBackupSnapshot(raw)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if snap == nil || snap.Destination != mdbBackup.Status.Destination {
		t.Fatalf("destination round-trip: %#v", snap)
	}
	if snap.Kind != psmdbBackupSnapshotKind || snap.StorageName != "s3-storage" {
		t.Errorf("snapshot metadata mismatch: %#v", snap)
	}
	if snap.S3 == nil || snap.S3.CredentialsSecret != "mongodb-mongodb-src-s3-creds" {
		t.Errorf("snapshot S3 mismatch: %#v", snap.S3)
	}
	// Secret-handling contract: the snapshot must carry only a Secret NAME,
	// never a raw credential. There is no field on BackupStorageS3 for a key,
	// so this is structurally guaranteed; assert the reference is preserved.
	if snap.S3.Bucket != "bkt" {
		t.Errorf("snapshot bucket mismatch: %q", snap.S3.Bucket)
	}
}

// TestMarshalMongoDBBackupSnapshot_SystemBucketFallback guards the
// useSystemBucket restore path: when the operator Backup status carries no S3
// echo, the snapshot must fall back to the strategy's injected coordinates
// (rendered.S3) so restore can rebuild backupSource - with credentialsSecret
// defaulting to cozy-backups-creds.
func TestMarshalMongoDBBackupSnapshot_SystemBucketFallback(t *testing.T) {
	mdbBackup := &psmdbtypes.PerconaServerMongoDBBackup{
		Status: psmdbtypes.PerconaServerMongoDBBackupStatus{
			Destination: "s3://cozybkt/tenant-root/mongodb-src/2026-08-26T00:00:00Z",
			// S3 intentionally nil: operator did not echo storage config.
		},
	}
	rendered := &strategyv1alpha1.MongoDBTemplate{
		Type: "logical",
		S3: &strategyv1alpha1.MongoDBStorageS3{
			Bucket:      "cozybkt",
			EndpointURL: "https://s3.example.org",
			Prefix:      "tenant-root/mongodb-src",
			Region:      "us-east-1",
			// CredentialsSecret intentionally empty -> defaults below.
		},
	}
	raw, err := marshalMongoDBBackupSnapshot(mdbBackup, rendered, "s3-storage", nil, true)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	snap, err := unmarshalMongoDBBackupSnapshot(raw)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if snap.S3 == nil {
		t.Fatal("snapshot S3 must be populated from rendered.S3 when the operator echo is empty")
	}
	if snap.S3.CredentialsSecret != "cozy-backups-creds" {
		t.Errorf("credentialsSecret must default to cozy-backups-creds; got %q", snap.S3.CredentialsSecret)
	}
	if snap.S3.EndpointURL != "https://s3.example.org" || snap.S3.Bucket != "cozybkt" {
		t.Errorf("snapshot S3 coords mismatch: %#v", snap.S3)
	}
}

// TestMarshalMongoDBBackupSnapshot_LegacyNoFallback pins the other half of the
// gate: a legacy app (useSystemBucket=false) whose operator echoed no S3 must
// leave the snapshot S3 nil even though the shared cozy-default strategy now
// carries platform coordinates. Recording the platform bucket for an archive
// the tenant wrote to its own bucket would send restore to the wrong place.
func TestMarshalMongoDBBackupSnapshot_LegacyNoFallback(t *testing.T) {
	mdbBackup := &psmdbtypes.PerconaServerMongoDBBackup{
		Status: psmdbtypes.PerconaServerMongoDBBackupStatus{
			Destination: "s3://tenant-own-bucket/mongodb-src/2026-08-26T00:00:00Z",
			// S3 nil: operator echoed nothing, as on a legacy app whose storage
			// lives on the app CR.
		},
	}
	rendered := &strategyv1alpha1.MongoDBTemplate{
		Type: "logical",
		S3: &strategyv1alpha1.MongoDBStorageS3{
			Bucket:      "cozy-backups-PLATFORM",
			EndpointURL: "https://platform-s3.example",
		},
	}
	raw, err := marshalMongoDBBackupSnapshot(mdbBackup, rendered, "s3-storage", nil, false)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	snap, err := unmarshalMongoDBBackupSnapshot(raw)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if snap.S3 != nil {
		t.Fatalf("legacy snapshot must not adopt the platform coordinates; got %#v", snap.S3)
	}
	if snap.Destination != "s3://tenant-own-bucket/mongodb-src/2026-08-26T00:00:00Z" {
		t.Errorf("legacy destination must be preserved; got %q", snap.Destination)
	}
}

func TestUnmarshalMongoDBBackupSnapshot_Empty(t *testing.T) {
	snap, err := unmarshalMongoDBBackupSnapshot(nil)
	if err != nil || snap != nil {
		t.Fatalf("nil snapshot should decode to (nil,nil): snap=%v err=%v", snap, err)
	}
}

// ---------------------------------------------------------------------------
// resolveMongoDBBackupSource: live status preferred, snapshot fallback
// ---------------------------------------------------------------------------

func TestResolveMongoDBBackupSource_LivePreferred(t *testing.T) {
	liveBackup := &psmdbtypes.PerconaServerMongoDBBackup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "op-backup"},
		Status: psmdbtypes.PerconaServerMongoDBBackupStatus{
			State:       psmdbtypes.StateReady,
			Type:        "logical",
			Destination: "s3://bkt/live/2026",
			S3:          &psmdbtypes.BackupStorageS3{Bucket: "bkt"},
		},
	}
	cozyBackup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "cozy-bk"},
		Spec: backupsv1alpha1.BackupSpec{
			DriverMetadata: map[string]string{
				psmdbBackupNameKey:  "op-backup",
				psmdbDestinationKey: "s3://bkt/stale/2020",
			},
		},
	}
	c := newMongoDBStrategyTestClient(t, liveBackup)
	r := &RestoreJobReconciler{Client: c, Recorder: record.NewFakeRecorder(10)}
	src, err := r.resolveMongoDBBackupSource(context.Background(), cozyBackup)
	if err != nil {
		t.Fatalf("resolveMongoDBBackupSource: %v", err)
	}
	if src.Destination != "s3://bkt/live/2026" {
		t.Errorf("expected live destination to win, got %q", src.Destination)
	}
}

func TestResolveMongoDBBackupSource_SnapshotFallback(t *testing.T) {
	// No live operator Backup CR seeded; a snapshot on the Cozystack Backup is
	// the only source of the destination.
	fps := true
	snapBackup := &psmdbtypes.PerconaServerMongoDBBackup{
		Status: psmdbtypes.PerconaServerMongoDBBackupStatus{
			Destination: "s3://bkt/snap/2026",
			S3:          &psmdbtypes.BackupStorageS3{Bucket: "bkt", ForcePathStyle: &fps},
		},
	}
	raw, err := marshalMongoDBBackupSnapshot(snapBackup, &strategyv1alpha1.MongoDBTemplate{Type: "logical"}, "s3-storage", nil, false)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	cozyBackup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "cozy-bk"},
		Spec: backupsv1alpha1.BackupSpec{
			DriverMetadata: map[string]string{psmdbBackupNameKey: "reaped-backup"},
		},
		Status: backupsv1alpha1.BackupStatus{UnderlyingResources: raw},
	}
	c := newMongoDBStrategyTestClient(t)
	r := &RestoreJobReconciler{Client: c, Recorder: record.NewFakeRecorder(10)}
	src, err := r.resolveMongoDBBackupSource(context.Background(), cozyBackup)
	if err != nil {
		t.Fatalf("resolveMongoDBBackupSource: %v", err)
	}
	if src.Destination != "s3://bkt/snap/2026" {
		t.Errorf("expected snapshot destination, got %q", src.Destination)
	}
	if src.S3 == nil || src.S3.Bucket != "bkt" {
		t.Errorf("expected snapshot S3 reused, got %#v", src.S3)
	}
}

func TestResolveMongoDBBackupSource_NoDestinationFails(t *testing.T) {
	cozyBackup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "cozy-bk"},
		Spec:       backupsv1alpha1.BackupSpec{DriverMetadata: map[string]string{}},
	}
	c := newMongoDBStrategyTestClient(t)
	r := &RestoreJobReconciler{Client: c, Recorder: record.NewFakeRecorder(10)}
	if _, err := r.resolveMongoDBBackupSource(context.Background(), cozyBackup); err == nil {
		t.Fatal("expected an error when no destination is resolvable")
	}
}

// A non-NotFound read error on the live operator Backup CR is transient (cache
// not yet synced, apiserver hiccup): resolveMongoDBBackupSource must wrap it in
// errTransientRestoreSource so the reconciler requeues instead of failing the
// RestoreJob — matching the backup path, which requeues the symmetric read
// error. A terminal snapshot fallback (no destination) must NOT carry the
// sentinel; TestResolveMongoDBBackupSource_NoDestinationFails pins that side.
func TestResolveMongoDBBackupSource_TransientLiveReadRequeues(t *testing.T) {
	cozyBackup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "cozy-bk"},
		Spec: backupsv1alpha1.BackupSpec{
			DriverMetadata: map[string]string{psmdbBackupNameKey: "op-backup"},
		},
	}
	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	_ = backupsv1alpha1.AddToScheme(s)
	_ = strategyv1alpha1.AddToScheme(s)
	_ = psmdbtypes.AddToScheme(s)
	_ = mongodbapp.AddToScheme(s)
	c := clientfake.NewClientBuilder().
		WithScheme(s).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*psmdbtypes.PerconaServerMongoDBBackup); ok {
					return apierrors.NewServiceUnavailable("apiserver hiccup")
				}
				return cl.Get(ctx, key, obj, opts...)
			},
		}).
		Build()
	r := &RestoreJobReconciler{Client: c, Recorder: record.NewFakeRecorder(10)}
	_, err := r.resolveMongoDBBackupSource(context.Background(), cozyBackup)
	if err == nil {
		t.Fatal("expected a transient error from the live-CR read, got nil")
	}
	if !errors.Is(err, errTransientRestoreSource) {
		t.Errorf("expected errTransientRestoreSource so the caller requeues, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// createMongoDBBackupArtifact: driver-metadata + snapshot wiring
// ---------------------------------------------------------------------------

// TestCreateMongoDBBackupArtifact_ArtifactShape pins the producer side of the
// restore contract: the driver-metadata keys and the persisted snapshot that
// resolveMongoDBBackupSource reads back. Without this the producer/consumer
// seam is only exercised end-to-end in e2e — a key-name drift between writer
// and reader would pass every other unit test. Mirrors the MariaDB sibling
// TestCreateMariaDBBackupArtifact_ArtifactShape.
func TestCreateMongoDBBackupArtifact_ArtifactShape(t *testing.T) {
	apps := mongodbapp.GroupName
	resolved := &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{Kind: "MongoDB", Name: "cozy-default-mongodb"},
		Parameters:  map[string]string{},
	}
	rendered := newRenderedMongoDBTemplate()

	t.Run("ready backup with destination populates metadata + artifact + snapshot", func(t *testing.T) {
		job := &backupsv1alpha1.BackupJob{
			ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj-ok"},
			Spec: backupsv1alpha1.BackupJobSpec{
				ApplicationRef: corev1.TypedLocalObjectReference{Kind: "MongoDB", Name: "mongodb-src", APIGroup: &apps},
			},
		}
		c := newMongoDBStrategyTestClient(t, job)
		r := &BackupJobReconciler{Client: c, Scheme: c.Scheme()}
		mdbBackup := &psmdbtypes.PerconaServerMongoDBBackup{
			ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "op-bk"},
			Status: psmdbtypes.PerconaServerMongoDBBackupStatus{
				State:       psmdbtypes.StateReady,
				Type:        "logical",
				Destination: "s3://bkt/mongodb-src/2026-08-05T00:00:00Z",
				S3:          &psmdbtypes.BackupStorageS3{Bucket: "bkt", CredentialsSecret: "mongodb-mongodb-src-s3-creds"},
			},
		}

		artefact, err := r.createMongoDBBackupArtifact(context.Background(), job, resolved, mdbBackup, rendered, "s3-storage", false)
		if err != nil {
			t.Fatalf("createMongoDBBackupArtifact: %v", err)
		}
		if got := artefact.Spec.DriverMetadata[psmdbBackupNameKey]; got != "op-bk" {
			t.Errorf("%s: got %q want op-bk", psmdbBackupNameKey, got)
		}
		if got := artefact.Spec.DriverMetadata[psmdbBackupNamespaceKey]; got != "tenant" {
			t.Errorf("%s: got %q want tenant", psmdbBackupNamespaceKey, got)
		}
		if got := artefact.Spec.DriverMetadata[psmdbDestinationKey]; got != mdbBackup.Status.Destination {
			t.Errorf("%s: got %q want %q", psmdbDestinationKey, got, mdbBackup.Status.Destination)
		}
		if artefact.Status.Phase != backupsv1alpha1.BackupPhaseReady {
			t.Errorf("Status.Phase: got %q want Ready", artefact.Status.Phase)
		}
		if artefact.Status.Artifact == nil || artefact.Status.Artifact.URI != mdbBackup.Status.Destination {
			t.Errorf("Status.Artifact.URI: got %#v want %q", artefact.Status.Artifact, mdbBackup.Status.Destination)
		}
		// The persisted snapshot must decode back to a usable backupSource —
		// this is exactly what resolveMongoDBBackupSource consumes on the
		// restore path when the operator Backup CR has been reaped.
		snap, err := unmarshalMongoDBBackupSnapshot(artefact.Status.UnderlyingResources)
		if err != nil {
			t.Fatalf("decode snapshot: %v", err)
		}
		if snap == nil || snap.Destination != mdbBackup.Status.Destination {
			t.Fatalf("snapshot destination round-trip: %#v", snap)
		}
		if snap.S3 == nil || snap.S3.CredentialsSecret != "mongodb-mongodb-src-s3-creds" {
			t.Errorf("snapshot S3 by-reference mismatch: %#v", snap.S3)
		}
	})

	t.Run("no destination leaves Artifact nil and omits destination key", func(t *testing.T) {
		job := &backupsv1alpha1.BackupJob{
			ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj-nodest"},
			Spec: backupsv1alpha1.BackupJobSpec{
				ApplicationRef: corev1.TypedLocalObjectReference{Kind: "MongoDB", Name: "mongodb-src", APIGroup: &apps},
			},
		}
		c := newMongoDBStrategyTestClient(t, job)
		r := &BackupJobReconciler{Client: c, Scheme: c.Scheme()}
		mdbBackup := &psmdbtypes.PerconaServerMongoDBBackup{
			ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "op-bk2"},
			Status:     psmdbtypes.PerconaServerMongoDBBackupStatus{State: psmdbtypes.StateReady},
		}

		artefact, err := r.createMongoDBBackupArtifact(context.Background(), job, resolved, mdbBackup, rendered, "s3-storage", false)
		if err != nil {
			t.Fatalf("createMongoDBBackupArtifact: %v", err)
		}
		if artefact.Status.Artifact != nil {
			t.Errorf("no destination must leave Status.Artifact nil; got %#v", artefact.Status.Artifact)
		}
		if _, ok := artefact.Spec.DriverMetadata[psmdbDestinationKey]; ok {
			t.Errorf("no destination must omit %s from driverMetadata", psmdbDestinationKey)
		}
	})
}

// ---------------------------------------------------------------------------
// System-bucket storage injection
// ---------------------------------------------------------------------------

// TestShouldInjectMongoDBSystemStorage pins the injection gate: inject only when
// the app opted into the system bucket AND the strategy carries S3 coordinates.
// The flag-false branch is the guarantee that a legacy app is never touched,
// regardless of what storage happens to be on the live cluster.
func TestShouldInjectMongoDBSystemStorage(t *testing.T) {
	s3 := &strategyv1alpha1.MongoDBStorageS3{Bucket: "b", EndpointURL: "https://s3"}
	cases := []struct {
		name            string
		useSystemBucket bool
		rendered        *strategyv1alpha1.MongoDBTemplate
		want            bool
	}{
		{
			name:            "useSystemBucket flow with strategy coordinates: inject",
			useSystemBucket: true,
			rendered:        &strategyv1alpha1.MongoDBTemplate{StorageName: "s3-storage", S3: s3},
			want:            true,
		},
		{
			name:            "legacy app (flag off): skip even when the shared strategy carries coordinates",
			useSystemBucket: false,
			rendered:        &strategyv1alpha1.MongoDBTemplate{StorageName: "s3-storage", S3: s3},
			want:            false,
		},
		{
			name:            "useSystemBucket flag but strategy carries no coordinates: skip (nothing to inject)",
			useSystemBucket: true,
			rendered:        &strategyv1alpha1.MongoDBTemplate{StorageName: "s3-storage"},
			want:            false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldInjectMongoDBSystemStorage(tc.useSystemBucket, tc.rendered); got != tc.want {
				t.Fatalf("shouldInjectMongoDBSystemStorage = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestBuildMongoDBSystemStorageEntry pins the injected storage-entry shape: the
// {"type":"s3","s3":{…}} envelope, the empty-credentialsSecret default, and the
// forcePathStyle omit-when-nil (a nil pointer must not surface as
// forcePathStyle:false the app never chose).
func TestBuildMongoDBSystemStorageEntry(t *testing.T) {
	t.Run("defaults credentialsSecret and omits forcePathStyle when nil", func(t *testing.T) {
		entry := buildMongoDBSystemStorageEntry(&strategyv1alpha1.MongoDBStorageS3{
			Bucket:      "sys-bucket",
			EndpointURL: "https://s3.example",
			Region:      "us-east-1",
			Prefix:      "mongodb-src",
		})
		if entry["type"] != "s3" {
			t.Fatalf("type = %v, want s3", entry["type"])
		}
		s3, ok := entry["s3"].(map[string]interface{})
		if !ok {
			t.Fatalf("s3 block is not a map: %#v", entry["s3"])
		}
		if s3["credentialsSecret"] != "cozy-backups-creds" {
			t.Errorf("credentialsSecret = %v, want cozy-backups-creds", s3["credentialsSecret"])
		}
		if _, present := s3["forcePathStyle"]; present {
			t.Errorf("forcePathStyle must be omitted when the pointer is nil; got %v", s3["forcePathStyle"])
		}
		for k, want := range map[string]interface{}{
			"bucket":                "sys-bucket",
			"endpointUrl":           "https://s3.example",
			"region":                "us-east-1",
			"prefix":                "mongodb-src",
			"insecureSkipTLSVerify": false,
		} {
			if s3[k] != want {
				t.Errorf("s3[%q] = %v, want %v", k, s3[k], want)
			}
		}
	})

	t.Run("honours explicit credentialsSecret and forcePathStyle", func(t *testing.T) {
		fps := true
		entry := buildMongoDBSystemStorageEntry(&strategyv1alpha1.MongoDBStorageS3{
			Bucket:                "b",
			CredentialsSecret:     "custom-creds",
			ForcePathStyle:        &fps,
			InsecureSkipTLSVerify: true,
		})
		s3 := entry["s3"].(map[string]interface{})
		if s3["credentialsSecret"] != "custom-creds" {
			t.Errorf("credentialsSecret = %v, want custom-creds", s3["credentialsSecret"])
		}
		if s3["forcePathStyle"] != true {
			t.Errorf("forcePathStyle = %v, want true", s3["forcePathStyle"])
		}
		if s3["insecureSkipTLSVerify"] != true {
			t.Errorf("insecureSkipTLSVerify = %v, want true", s3["insecureSkipTLSVerify"])
		}
	})
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func newMongoDBStrategyTestClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	_ = backupsv1alpha1.AddToScheme(s)
	_ = strategyv1alpha1.AddToScheme(s)
	_ = psmdbtypes.AddToScheme(s)
	_ = mongodbapp.AddToScheme(s)
	return clientfake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		WithStatusSubresource(&backupsv1alpha1.BackupJob{}, &backupsv1alpha1.RestoreJob{}, &backupsv1alpha1.Backup{}).
		Build()
}

func newMongoDBBackupJob(name, namespace string) *backupsv1alpha1.BackupJob {
	apps := mongodbapp.GroupName
	return &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{
				Kind: "MongoDB", Name: "mongodb-src", APIGroup: &apps,
			},
		},
	}
}

func newMongoDBRestoreJob(name, namespace string) *backupsv1alpha1.RestoreJob {
	return &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: backupsv1alpha1.RestoreJobSpec{
			BackupRef: corev1.LocalObjectReference{Name: "src-backup"},
		},
	}
}

func newRenderedMongoDBTemplate() *strategyv1alpha1.MongoDBTemplate {
	return &strategyv1alpha1.MongoDBTemplate{
		StorageName:     "s3-storage",
		Type:            "logical",
		CompressionType: "gzip",
	}
}

// ---------------------------------------------------------------------------
// Strategy rendering
// ---------------------------------------------------------------------------

// The strategy prefix reaches the CR as a literal Go template
// ("<ns>/<app>") and renderMongoDBTemplate is what turns it into a per-tenant
// path at BackupJob time. That resolution is the only thing keeping two tenants'
// dumps apart inside one shared bucket, so pin it: an unrendered prefix would
// collide every tenant under the same path. Mirrors the CNPG/FoundationDB
// sibling render tests.
func TestRenderMongoDBTemplate_TemplatingApplicationName(t *testing.T) {
	tmpl := strategyv1alpha1.MongoDBTemplate{
		StorageName: "s3-storage",
		Type:        "logical",
		S3: &strategyv1alpha1.MongoDBStorageS3{
			Bucket:            "cozy-backups",
			EndpointURL:       "https://s3.example",
			Prefix:            "{{ .Application.metadata.namespace }}/{{ .Application.metadata.name }}",
			CredentialsSecret: "cozy-backups-creds",
		},
	}
	app := &mongodbapp.MongoDB{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-x", Name: "mongo-y"}}

	got, err := renderMongoDBTemplate(tmpl, app, nil)
	if err != nil {
		t.Fatalf("renderMongoDBTemplate: %v", err)
	}
	if got.S3 == nil {
		t.Fatalf("rendered S3 is nil")
	}
	if got.S3.Prefix != "tenant-x/mongo-y" {
		t.Errorf("prefix not templated per application: got %q want tenant-x/mongo-y", got.S3.Prefix)
	}
	// The non-templated coordinates must pass through untouched.
	if got.S3.Bucket != "cozy-backups" || got.S3.CredentialsSecret != "cozy-backups-creds" {
		t.Errorf("static coordinates altered: %#v", got.S3)
	}
}

// ---------------------------------------------------------------------------
// System-bucket storage injection (BackupJob path)
// ---------------------------------------------------------------------------

// The injection is what makes the useSystemBucket flow work: the app chart omits
// the storage, so reconcileMongoDB SSA-injects it and must read the precondition
// off the post-apply cluster (cluster = injected), not the pre-apply one it
// Got. This pins that within a single reconcile: with a live cluster that has
// backups enabled but no storage declared, the reconcile must inject, pass the
// precondition, and create the operator Backup CR. Drop the reassignment and the
// precondition reads the pre-apply cluster, fails, and no Backup is created.
func TestReconcileMongoDB_InjectsSystemStorageBeforePrecondition(t *testing.T) {
	apps := mongodbapp.GroupName
	strategyGroup := strategyv1alpha1.GroupVersion.Group
	now := metav1.Now()

	job := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj-inj"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{Kind: "MongoDB", Name: "app1", APIGroup: &apps},
		},
		Status: backupsv1alpha1.BackupJobStatus{StartedAt: &now},
	}
	strategy := &strategyv1alpha1.MongoDB{
		ObjectMeta: metav1.ObjectMeta{Name: "cozy-default-mongodb"},
		Spec: strategyv1alpha1.MongoDBSpec{Template: strategyv1alpha1.MongoDBTemplate{
			StorageName: "s3-storage",
			Type:        "logical",
			S3: &strategyv1alpha1.MongoDBStorageS3{
				Bucket:            "cozy-backups",
				EndpointURL:       "https://s3.example",
				Prefix:            "{{ .Application.metadata.namespace }}/{{ .Application.metadata.name }}",
				CredentialsSecret: "cozy-backups-creds",
			},
		}},
	}
	app := &mongodbapp.MongoDB{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "app1"},
		Spec:       mongodbapp.MongoDBSpec{Backup: mongodbapp.MongoDBBackupSpec{UseSystemBucket: true}},
	}
	// Live cluster: backups enabled (agents run) but storage NOT declared — the
	// exact state the chart leaves on the useSystemBucket flow.
	cluster := &psmdbtypes.PerconaServerMongoDB{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "mongodb-app1"},
		Spec:       psmdbtypes.PerconaServerMongoDBSpec{Backup: psmdbtypes.PerconaServerMongoDBBackupConfig{Enabled: true}},
	}

	c := newMongoDBStrategyTestClient(t, job, strategy, app, cluster)
	r := &BackupJobReconciler{Client: c, Scheme: c.Scheme()}

	resolved := &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{APIGroup: &strategyGroup, Kind: strategyv1alpha1.MongoDBStrategyKind, Name: "cozy-default-mongodb"},
		Parameters:  map[string]string{},
	}

	// First pass: the storage is freshly injected, so the driver requeues rather
	// than minting the Backup CR in the same reconcile (see the injection-race
	// note in reconcileMongoDB). The apply is persisted onto the live cluster.
	res, err := r.reconcileMongoDB(context.Background(), job.DeepCopy(), resolved)
	if err != nil {
		t.Fatalf("reconcileMongoDB pass 1: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Fatalf("expected a requeue after the first injection, got %+v", res)
	}
	list := &psmdbtypes.PerconaServerMongoDBBackupList{}
	if err := c.List(context.Background(), list, client.InNamespace("tenant")); err != nil {
		t.Fatalf("list operator backups: %v", err)
	}
	if len(list.Items) != 0 {
		t.Fatalf("no operator Backup should be minted before the requeue, got %d", len(list.Items))
	}
	got := &psmdbtypes.PerconaServerMongoDB{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant", Name: "mongodb-app1"}, got); err != nil {
		t.Fatalf("get cluster: %v", err)
	}
	if _, ok := got.Spec.Backup.Storages["s3-storage"]; !ok {
		t.Errorf("expected injected s3-storage on the cluster, got %#v", got.Spec.Backup.Storages)
	}

	// Second pass: the cluster already declares the storage (the operator cache
	// has had a poll to observe it), so the precondition passes off the injected
	// storage and the operator Backup CR is minted. Re-fetch the BackupJob first —
	// pass 1 wrote its Ready=False condition, so a stale copy would conflict.
	fresh := &backupsv1alpha1.BackupJob{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant", Name: "bj-inj"}, fresh); err != nil {
		t.Fatalf("re-get job: %v", err)
	}
	res, err = r.reconcileMongoDB(context.Background(), fresh, resolved)
	if err != nil {
		t.Fatalf("reconcileMongoDB pass 2: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Fatalf("expected a poll requeue while the operator Backup runs, got %+v", res)
	}
	if err := c.List(context.Background(), list, client.InNamespace("tenant")); err != nil {
		t.Fatalf("list operator backups: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("expected the injected storage to let the precondition pass and create 1 operator Backup, got %d", len(list.Items))
	}
}

// mongodbInjectFixture builds the four objects reconcileMongoDB needs to reach
// the injection point on the useSystemBucket flow, parameterised on the live
// cluster's backup.enabled.
func mongodbInjectFixture(enabled bool) (*backupsv1alpha1.BackupJob, *strategyv1alpha1.MongoDB, *mongodbapp.MongoDB, *psmdbtypes.PerconaServerMongoDB, *ResolvedBackupConfig) {
	apps := mongodbapp.GroupName
	strategyGroup := strategyv1alpha1.GroupVersion.Group
	now := metav1.Now()
	job := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj-inj"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{Kind: "MongoDB", Name: "app1", APIGroup: &apps},
		},
		Status: backupsv1alpha1.BackupJobStatus{StartedAt: &now},
	}
	strategy := &strategyv1alpha1.MongoDB{
		ObjectMeta: metav1.ObjectMeta{Name: "cozy-default-mongodb"},
		Spec: strategyv1alpha1.MongoDBSpec{Template: strategyv1alpha1.MongoDBTemplate{
			StorageName: "s3-storage",
			Type:        "logical",
			S3: &strategyv1alpha1.MongoDBStorageS3{
				Bucket: "cozy-backups", EndpointURL: "https://s3.example",
				Prefix: "{{ .Application.metadata.namespace }}/{{ .Application.metadata.name }}", CredentialsSecret: "cozy-backups-creds",
			},
		}},
	}
	app := &mongodbapp.MongoDB{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "app1"},
		Spec:       mongodbapp.MongoDBSpec{Backup: mongodbapp.MongoDBBackupSpec{UseSystemBucket: true}},
	}
	cluster := &psmdbtypes.PerconaServerMongoDB{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "mongodb-app1"},
		Spec:       psmdbtypes.PerconaServerMongoDBSpec{Backup: psmdbtypes.PerconaServerMongoDBBackupConfig{Enabled: enabled}},
	}
	resolved := &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{APIGroup: &strategyGroup, Kind: strategyv1alpha1.MongoDBStrategyKind, Name: "cozy-default-mongodb"},
		Parameters:  map[string]string{},
	}
	return job, strategy, app, cluster, resolved
}

// The first BackupJob of a useSystemBucket app injects the storage; the psmdb
// operator resolves it from a cached cluster read when it services the Backup
// CR, so minting the CR in the same pass races that cache and can latch it at
// State=error. The driver must requeue after a fresh injection instead of
// minting, giving the cache a poll to observe the storage.
func TestReconcileMongoDB_FirstInjectionRequeuesBeforeMinting(t *testing.T) {
	// Fixture seeds a cluster with backups enabled and NO storage declared.
	job, strategy, app, cluster, resolved := mongodbInjectFixture(true)
	c := newMongoDBStrategyTestClient(t, job, strategy, app, cluster)
	r := &BackupJobReconciler{Client: c, Scheme: c.Scheme()}

	res, err := r.reconcileMongoDB(context.Background(), job.DeepCopy(), resolved)
	if err != nil {
		t.Fatalf("reconcileMongoDB: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Fatalf("expected a requeue after the first injection, got %+v", res)
	}
	// No operator Backup CR minted yet — that waits for the next pass.
	list := &psmdbtypes.PerconaServerMongoDBBackupList{}
	if err := c.List(context.Background(), list, client.InNamespace("tenant")); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 0 {
		t.Errorf("no operator Backup should be created in the same pass as a fresh injection, got %d", len(list.Items))
	}
	// The wait is named on the BackupJob, not silent: a Ready=False condition with
	// the injection reason so the pause is visible in kubectl describe.
	persisted := &backupsv1alpha1.BackupJob{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant", Name: job.Name}, persisted); err != nil {
		t.Fatalf("get job: %v", err)
	}
	cond := apimeta.FindStatusCondition(persisted.Status.Conditions, "Ready")
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "PerconaServerMongoDBStorageInjected" {
		t.Errorf("expected Ready=False with reason PerconaServerMongoDBStorageInjected, got %+v", cond)
	}
}

// A cluster that can never service a backup (backup.enabled=false) must not be
// mutated: injection is gated on Enabled so the disabled cluster fails the
// precondition on the enabled check without the driver having written storage
// onto it.
func TestReconcileMongoDB_DisabledClusterNotMutated(t *testing.T) {
	job, strategy, app, cluster, resolved := mongodbInjectFixture(false)
	c := newMongoDBStrategyTestClient(t, job, strategy, app, cluster)
	r := &BackupJobReconciler{Client: c, Scheme: c.Scheme()}

	res, err := r.reconcileMongoDB(context.Background(), job.DeepCopy(), resolved)
	if err != nil {
		t.Fatalf("reconcileMongoDB: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Fatalf("expected a waiting requeue for a disabled cluster, got %+v", res)
	}
	got := &psmdbtypes.PerconaServerMongoDB{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant", Name: "mongodb-app1"}, got); err != nil {
		t.Fatalf("get cluster: %v", err)
	}
	if len(got.Spec.Backup.Storages) != 0 {
		t.Errorf("disabled cluster must not be injected, got storages %#v", got.Spec.Backup.Storages)
	}
	list := &psmdbtypes.PerconaServerMongoDBBackupList{}
	if err := c.List(context.Background(), list, client.InNamespace("tenant")); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 0 {
		t.Errorf("no operator Backup should be created for a disabled cluster, got %d", len(list.Items))
	}
}

// A server-side apply that fails transiently (apiserver hiccup, operator
// conflict) must requeue with backoff, not fail the BackupJob terminally. Before
// the fix the injection error went straight to markBackupJobFailed, so one
// transient apply killed the backup.
func TestReconcileMongoDB_InjectApplyErrorRequeues(t *testing.T) {
	job, strategy, app, cluster, resolved := mongodbInjectFixture(true)

	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	_ = backupsv1alpha1.AddToScheme(s)
	_ = strategyv1alpha1.AddToScheme(s)
	_ = psmdbtypes.AddToScheme(s)
	_ = mongodbapp.AddToScheme(s)
	c := clientfake.NewClientBuilder().
		WithScheme(s).
		WithObjects(job, strategy, app, cluster).
		WithStatusSubresource(&backupsv1alpha1.BackupJob{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if _, ok := obj.(*psmdbtypes.PerconaServerMongoDB); ok {
					return apierrors.NewConflict(schema.GroupResource{Group: "psmdb.percona.com", Resource: "perconaservermongodbs"}, obj.GetName(), errors.New("racing the operator"))
				}
				return cl.Patch(ctx, obj, patch, opts...)
			},
		}).
		Build()
	r := &BackupJobReconciler{Client: c, Scheme: c.Scheme()}

	_, err := r.reconcileMongoDB(context.Background(), job.DeepCopy(), resolved)
	if err == nil {
		t.Fatal("expected a transient error so the caller requeues, got nil")
	}
	// The BackupJob must NOT be marked Failed by a transient apply error.
	persisted := &backupsv1alpha1.BackupJob{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant", Name: "bj-inj"}, persisted); err != nil {
		t.Fatalf("get job: %v", err)
	}
	if persisted.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
		t.Errorf("a transient inject error must not fail the BackupJob terminally")
	}
}

// resolveMongoDBBackupSource must fill an S3 block the live operator status
// omits from the snapshot: on the useSystemBucket flow the operator can report
// status.destination with status.s3 empty (the storage was injected, not
// declared), and without the coordinates the restore has nothing to authenticate
// or address the archive with. Neither prior test covers a live CR whose
// destination is set but whose S3 is nil.
func TestResolveMongoDBBackupSource_LiveMissingS3BackfillsFromSnapshot(t *testing.T) {
	liveBackup := &psmdbtypes.PerconaServerMongoDBBackup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "op-backup"},
		Status: psmdbtypes.PerconaServerMongoDBBackupStatus{
			State:       psmdbtypes.StateReady,
			Type:        "logical",
			Destination: "s3://cozy-backups/tenant/app1/2026",
			// S3 intentionally nil: the operator echoed a destination only.
		},
	}
	snapBackup := &psmdbtypes.PerconaServerMongoDBBackup{
		Status: psmdbtypes.PerconaServerMongoDBBackupStatus{
			Destination: "s3://cozy-backups/tenant/app1/2026",
			S3:          &psmdbtypes.BackupStorageS3{Bucket: "cozy-backups", CredentialsSecret: "cozy-backups-creds", EndpointURL: "https://s3.example"},
		},
	}
	raw, err := marshalMongoDBBackupSnapshot(snapBackup, &strategyv1alpha1.MongoDBTemplate{Type: "logical"}, "s3-storage", nil, false)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	cozyBackup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "cozy-bk"},
		Spec: backupsv1alpha1.BackupSpec{
			DriverMetadata: map[string]string{psmdbBackupNameKey: "op-backup"},
		},
		Status: backupsv1alpha1.BackupStatus{UnderlyingResources: raw},
	}
	c := newMongoDBStrategyTestClient(t, liveBackup)
	r := &RestoreJobReconciler{Client: c, Recorder: record.NewFakeRecorder(10)}
	src, err := r.resolveMongoDBBackupSource(context.Background(), cozyBackup)
	if err != nil {
		t.Fatalf("resolveMongoDBBackupSource: %v", err)
	}
	if src.Destination != "s3://cozy-backups/tenant/app1/2026" {
		t.Errorf("expected live destination, got %q", src.Destination)
	}
	if src.S3 == nil {
		t.Fatalf("expected S3 backfilled from snapshot, got nil (restore would have no coordinates)")
	}
	if src.S3.CredentialsSecret != "cozy-backups-creds" || src.S3.Bucket != "cozy-backups" {
		t.Errorf("expected snapshot S3 coordinates, got %#v", src.S3)
	}
}

// ---------------------------------------------------------------------------
// System-bucket archive cleanup (retention)
// ---------------------------------------------------------------------------

func TestCleanupMongoDBBackup(t *testing.T) {
	md := map[string]string{psmdbBackupNameKey: "op-bk"}
	newBackup := func(ann map[string]string) *backupsv1alpha1.Backup {
		return &backupsv1alpha1.Backup{
			ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "cozy-bk", Annotations: ann},
			Spec:       backupsv1alpha1.BackupSpec{DriverMetadata: md},
		}
	}
	// The delete-backup finalizer is the ownership marker: present = the driver
	// owns the shared-bucket archive; absent = legacy (tenant bucket).
	opBackup := func(finalized bool) *psmdbtypes.PerconaServerMongoDBBackup {
		b := &psmdbtypes.PerconaServerMongoDBBackup{
			ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "op-bk"},
		}
		if finalized {
			b.Finalizers = []string{psmdbDeleteBackupFinalizer}
		}
		return b
	}
	liveNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant"}}
	getOp := func(t *testing.T, c client.Client) *psmdbtypes.PerconaServerMongoDBBackup {
		got := &psmdbtypes.PerconaServerMongoDBBackup{}
		if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant", Name: "op-bk"}, got); err != nil {
			t.Fatalf("get operator CR: %v", err)
		}
		return got
	}

	t.Run("legacy backup (no finalizer) is a no-op", func(t *testing.T) {
		c := newMongoDBStrategyTestClient(t, opBackup(false), liveNamespace)
		r := &BackupReconciler{Client: c}
		res, err := r.cleanupMongoDBBackup(context.Background(), newBackup(nil))
		if err != nil || res.RequeueAfter != 0 {
			t.Fatalf("legacy cleanup must be a no-op, got res=%+v err=%v", res, err)
		}
		if !getOp(t, c).DeletionTimestamp.IsZero() {
			t.Errorf("legacy cleanup must not delete the operator CR")
		}
	})

	t.Run("owned archive deletes the operator CR and waits", func(t *testing.T) {
		c := newMongoDBStrategyTestClient(t, opBackup(true), liveNamespace)
		r := &BackupReconciler{Client: c}
		res, err := r.cleanupMongoDBBackup(context.Background(), newBackup(nil))
		if err != nil {
			t.Fatalf("cleanupMongoDBBackup: %v", err)
		}
		if res.RequeueAfter == 0 {
			t.Fatalf("expected a requeue while the operator prunes the archive, got %+v", res)
		}
		// The finalizer holds the CR Terminating until the operator prunes; the
		// driver keeps its finalizer and waits, it does not strip it here.
		got := getOp(t, c)
		if got.DeletionTimestamp.IsZero() {
			t.Errorf("expected the operator CR to be marked for deletion")
		}
		if !controllerutil.ContainsFinalizer(got, psmdbDeleteBackupFinalizer) {
			t.Errorf("driver must keep the finalizer while waiting, so the operator can prune")
		}
	})

	t.Run("operator CR already gone releases", func(t *testing.T) {
		c := newMongoDBStrategyTestClient(t, liveNamespace)
		r := &BackupReconciler{Client: c}
		res, err := r.cleanupMongoDBBackup(context.Background(), newBackup(nil))
		if err != nil || res.RequeueAfter != 0 {
			t.Fatalf("a reaped operator CR must release, got res=%+v err=%v", res, err)
		}
	})

	t.Run("skip annotation strips the finalizer and releases", func(t *testing.T) {
		c := newMongoDBStrategyTestClient(t, opBackup(true), liveNamespace)
		r := &BackupReconciler{Client: c, Recorder: record.NewFakeRecorder(10)}
		res, err := r.cleanupMongoDBBackup(context.Background(), newBackup(map[string]string{psmdbSkipArtifactCleanupAnnotation: "true"}))
		if err != nil || res.RequeueAfter != 0 {
			t.Fatalf("skip annotation must release, got res=%+v err=%v", res, err)
		}
		// Releasing strips our finalizer so the CR can be reaped (in the fake
		// client, with no finalizer left, the best-effort delete removes it): the
		// escape hatch actually unwedges. Tolerate either "gone" or "present
		// without our finalizer".
		got := &psmdbtypes.PerconaServerMongoDBBackup{}
		err = c.Get(context.Background(), types.NamespacedName{Namespace: "tenant", Name: "op-bk"}, got)
		if err == nil && controllerutil.ContainsFinalizer(got, psmdbDeleteBackupFinalizer) {
			t.Errorf("release must strip the delete-backup finalizer; still present: %v", got.Finalizers)
		}
	})

	t.Run("terminating namespace strips the finalizer and releases (no wedge)", func(t *testing.T) {
		// No namespace object seeded → namespaceTerminating reports true, the
		// teardown case. The driver must not requeue forever waiting on an
		// operator that can no longer prune; it strips its finalizer and releases.
		c := newMongoDBStrategyTestClient(t, opBackup(true))
		r := &BackupReconciler{Client: c, Recorder: record.NewFakeRecorder(10)}
		res, err := r.cleanupMongoDBBackup(context.Background(), newBackup(nil))
		if err != nil || res.RequeueAfter != 0 {
			t.Fatalf("teardown must release, not wedge; got res=%+v err=%v", res, err)
		}
		got := &psmdbtypes.PerconaServerMongoDBBackup{}
		err = c.Get(context.Background(), types.NamespacedName{Namespace: "tenant", Name: "op-bk"}, got)
		if err == nil && controllerutil.ContainsFinalizer(got, psmdbDeleteBackupFinalizer) {
			t.Errorf("teardown must strip the finalizer so the namespace can finish; still present: %v", got.Finalizers)
		}
	})

	t.Run("terminating release uses the pre-read CR, not a second Get", func(t *testing.T) {
		// The terminating path hands the CR it already read and confirmed owned to
		// releaseMongoDBCleanup, so the strip is not gated on a second Get. Fail
		// every operator-CR Get after the first: cleanup's initial read succeeds
		// and confirms ownership, and the release must strip from the passed object
		// without re-reading. If the call site instead passed nil, the release
		// would re-Get (fail here) and skip the strip — no ArtifactNotDeleted Event.
		getN := 0
		s := runtime.NewScheme()
		_ = scheme.AddToScheme(s)
		_ = backupsv1alpha1.AddToScheme(s)
		_ = psmdbtypes.AddToScheme(s)
		c := clientfake.NewClientBuilder().
			WithScheme(s).
			WithObjects(opBackup(true)).
			WithInterceptorFuncs(interceptor.Funcs{
				Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, ok := obj.(*psmdbtypes.PerconaServerMongoDBBackup); ok {
						getN++
						if getN > 1 {
							return apierrors.NewServiceUnavailable("read throttled after first")
						}
					}
					return cl.Get(ctx, key, obj, opts...)
				},
			}).
			Build()
		rec := record.NewFakeRecorder(10)
		r := &BackupReconciler{Client: c, Recorder: rec}
		// No Namespace object → namespaceTerminating reports true (teardown path).
		res, err := r.cleanupMongoDBBackup(context.Background(), newBackup(nil))
		if err != nil || res.RequeueAfter != 0 {
			t.Fatalf("teardown must release, got res=%+v err=%v", res, err)
		}
		select {
		case ev := <-rec.Events:
			if !strings.Contains(ev, "ArtifactNotDeleted") {
				t.Errorf("expected the owned-release event proving the pre-read CR was used, got %q", ev)
			}
		default:
			t.Errorf("expected an ArtifactNotDeleted event (pre-read CR used, strip ran), got none")
		}
	})

	// A client whose Get on the operator CR hard-fails the way a missing CRD or a
	// revoked verb does — NoKindMatchError / an arbitrary error — models the case
	// the escape hatch exists for. The annotation must free the Backup without
	// depending on that read, and a bare NoMatchError must be treated as "gone".
	failingGetClient := func(getErr error) client.Client {
		s := runtime.NewScheme()
		_ = scheme.AddToScheme(s)
		_ = backupsv1alpha1.AddToScheme(s)
		_ = psmdbtypes.AddToScheme(s)
		return clientfake.NewClientBuilder().
			WithScheme(s).
			WithInterceptorFuncs(interceptor.Funcs{
				Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, ok := obj.(*psmdbtypes.PerconaServerMongoDBBackup); ok {
						return getErr
					}
					return cl.Get(ctx, key, obj, opts...)
				},
			}).
			Build()
	}
	noMatch := &apimeta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "psmdb.percona.com", Kind: "PerconaServerMongoDBBackup"}}

	t.Run("skip annotation releases even when the operator CR is unreadable", func(t *testing.T) {
		// The verb is revoked (Forbidden): the escape hatch runs before the Get, so
		// it must still release rather than propagate the read error and wedge.
		c := failingGetClient(apierrors.NewForbidden(schema.GroupResource{Group: "psmdb.percona.com", Resource: "perconaservermongodbbackups"}, "op-bk", errors.New("rbac revoked")))
		rec := record.NewFakeRecorder(10)
		r := &BackupReconciler{Client: c, Recorder: rec}
		res, err := r.cleanupMongoDBBackup(context.Background(), newBackup(map[string]string{psmdbSkipArtifactCleanupAnnotation: "true"}))
		if err != nil || res.RequeueAfter != 0 {
			t.Fatalf("annotation must release despite an unreadable operator CR, got res=%+v err=%v", res, err)
		}
		// A CR that could not be read may still carry the finalizer, so the release
		// surfaces which object may be left stuck rather than a Debug line nobody sees.
		select {
		case ev := <-rec.Events:
			if !strings.Contains(ev, "FinalizerNotStripped") || !strings.Contains(ev, "op-bk") {
				t.Errorf("expected a FinalizerNotStripped event naming the unreadable CR, got %q", ev)
			}
		default:
			t.Errorf("expected a FinalizerNotStripped Warning for the unreadable CR, got none")
		}
	})

	t.Run("a missing CRD (NoMatchError) releases instead of wedging", func(t *testing.T) {
		c := failingGetClient(noMatch)
		r := &BackupReconciler{Client: c}
		res, err := r.cleanupMongoDBBackup(context.Background(), newBackup(nil))
		if err != nil || res.RequeueAfter != 0 {
			t.Fatalf("NoMatchError must be treated as gone and release, got res=%+v err=%v", res, err)
		}
	})

	// The annotation reaches releaseMongoDBCleanup before the ownership check, so
	// the release must itself refuse to touch a CR the driver does not own — the
	// legacy operator-side backup record is what a psmdb restore-by-name resolves
	// against, and the RBAC comment and docs both promise it is never deleted.
	t.Run("legacy backup with the skip annotation keeps its operator CR", func(t *testing.T) {
		c := newMongoDBStrategyTestClient(t, opBackup(false), liveNamespace)
		r := &BackupReconciler{Client: c, Recorder: record.NewFakeRecorder(10)}
		res, err := r.cleanupMongoDBBackup(context.Background(), newBackup(map[string]string{psmdbSkipArtifactCleanupAnnotation: "true"}))
		if err != nil || res.RequeueAfter != 0 {
			t.Fatalf("legacy + annotation must release, got res=%+v err=%v", res, err)
		}
		got := getOp(t, c)
		if !got.DeletionTimestamp.IsZero() {
			t.Errorf("the escape hatch must not delete a legacy (unowned) operator CR")
		}
	})

	// When the finalizer strip fails, the CR keeps percona.com/delete-backup and
	// can pin its namespace in Terminating; the release still proceeds, but it
	// must say which object is stuck rather than emit the success-case Event.
	t.Run("a failed finalizer strip surfaces the stuck object", func(t *testing.T) {
		s := runtime.NewScheme()
		_ = scheme.AddToScheme(s)
		_ = backupsv1alpha1.AddToScheme(s)
		_ = psmdbtypes.AddToScheme(s)
		c := clientfake.NewClientBuilder().
			WithScheme(s).
			WithObjects(opBackup(true)).
			WithInterceptorFuncs(interceptor.Funcs{
				Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
					if _, ok := obj.(*psmdbtypes.PerconaServerMongoDBBackup); ok {
						return apierrors.NewServiceUnavailable("apiserver down")
					}
					return cl.Patch(ctx, obj, patch, opts...)
				},
			}).
			Build()
		rec := record.NewFakeRecorder(10)
		r := &BackupReconciler{Client: c, Recorder: rec}
		res, err := r.cleanupMongoDBBackup(context.Background(), newBackup(map[string]string{psmdbSkipArtifactCleanupAnnotation: "true"}))
		if err != nil || res.RequeueAfter != 0 {
			t.Fatalf("release must proceed even when the strip fails, got res=%+v err=%v", res, err)
		}
		select {
		case ev := <-rec.Events:
			if !strings.Contains(ev, "FinalizerNotStripped") || !strings.Contains(ev, "op-bk") {
				t.Errorf("expected a FinalizerNotStripped event naming the object, got %q", ev)
			}
		default:
			t.Errorf("expected an event surfacing the stuck finalizer, got none")
		}
	})
}

// On the useSystemBucket flow a dump the operator is actively writing
// (state=running) is never failed on the wall-clock deadline: pbm keeps
// streaming into the shared bucket after the driver gives up, so failing it
// strands an archive no Backup object represents. On the legacy flow the
// archive lands in the tenant's own bucket under psmdb task retention, so the
// pre-existing deadline still applies to running. Not-started states trip the
// deadline on both flows.
func TestPsmdbBackupTimedOut(t *testing.T) {
	past := &metav1.Time{Time: time.Now().Add(-2 * psmdbDefaultBackupDeadline)}
	recent := &metav1.Time{Time: time.Now()}
	cases := []struct {
		name    string
		state   string
		started *metav1.Time
		usb     bool
		want    bool
	}{
		{"system-bucket running past deadline: never strand the shared archive", psmdbtypes.StateRunning, past, true, false},
		{"legacy running past deadline: the pre-existing deadline applies", psmdbtypes.StateRunning, past, false, true},
		{"waiting past deadline: nothing written, safe to fail", psmdbtypes.StateWaiting, past, true, true},
		{"requested past deadline", psmdbtypes.StateRequested, past, false, true},
		{"never observed past deadline", "", past, true, true},
		{"legacy running within deadline", psmdbtypes.StateRunning, recent, false, false},
		{"waiting within deadline", psmdbtypes.StateWaiting, recent, true, false},
		{"legacy running with no StartedAt", psmdbtypes.StateRunning, nil, false, false},
	}
	for _, tc := range cases {
		if got := psmdbBackupTimedOut(tc.state, tc.started, tc.usb); got != tc.want {
			t.Errorf("%s: psmdbBackupTimedOut(state=%q, useSystemBucket=%v): got %v want %v", tc.name, tc.state, tc.usb, got, tc.want)
		}
	}
}

// The SSA-patch call site is the one place that could force-overwrite a running
// legacy tenant's own s3-storage entry. On the legacy flow (useSystemBucket
// false) the driver must never touch the cluster, even when the cluster is
// otherwise ready to back up — the injection is gated on the app flag, not on
// the live storage set.
func TestReconcileMongoDB_LegacyClusterNotPatched(t *testing.T) {
	apps := mongodbapp.GroupName
	strategyGroup := strategyv1alpha1.GroupVersion.Group
	now := metav1.Now()

	job := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj-legacy"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{Kind: "MongoDB", Name: "app1", APIGroup: &apps},
		},
		Status: backupsv1alpha1.BackupJobStatus{StartedAt: &now},
	}
	// Strategy still carries S3 coordinates (they live on the shared default), so
	// the guard cannot lean on rendered.S3 being nil — only the app flag.
	strategy := &strategyv1alpha1.MongoDB{
		ObjectMeta: metav1.ObjectMeta{Name: "cozy-default-mongodb"},
		Spec: strategyv1alpha1.MongoDBSpec{Template: strategyv1alpha1.MongoDBTemplate{
			StorageName: "s3-storage",
			Type:        "logical",
			S3:          &strategyv1alpha1.MongoDBStorageS3{Bucket: "cozy-backups", EndpointURL: "https://s3.example", CredentialsSecret: "cozy-backups-creds"},
		}},
	}
	app := &mongodbapp.MongoDB{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "app1"},
		Spec:       mongodbapp.MongoDBSpec{Backup: mongodbapp.MongoDBBackupSpec{UseSystemBucket: false}},
	}
	// Legacy cluster with backups enabled AND its own tenant-bucket storage.
	ownStorage := runtime.RawExtension{Raw: []byte(`{"type":"s3","s3":{"bucket":"tenant-own","credentialsSecret":"mongodb-app1-s3-creds"}}`)}
	cluster := &psmdbtypes.PerconaServerMongoDB{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "mongodb-app1"},
		Spec: psmdbtypes.PerconaServerMongoDBSpec{Backup: psmdbtypes.PerconaServerMongoDBBackupConfig{
			Enabled:  true,
			Storages: map[string]runtime.RawExtension{"s3-storage": ownStorage},
		}},
	}

	c := newMongoDBStrategyTestClient(t, job, strategy, app, cluster)
	r := &BackupJobReconciler{Client: c, Scheme: c.Scheme()}
	resolved := &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{APIGroup: &strategyGroup, Kind: strategyv1alpha1.MongoDBStrategyKind, Name: "cozy-default-mongodb"},
		Parameters:  map[string]string{},
	}

	if _, err := r.reconcileMongoDB(context.Background(), job.DeepCopy(), resolved); err != nil {
		t.Fatalf("reconcileMongoDB: %v", err)
	}

	got := &psmdbtypes.PerconaServerMongoDB{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant", Name: "mongodb-app1"}, got); err != nil {
		t.Fatalf("get cluster: %v", err)
	}
	bucket, _ := psmdbStorageS3(got.Spec.Backup.Storages["s3-storage"])
	if bucket != "tenant-own" {
		t.Errorf("legacy tenant's own s3-storage was overwritten: bucket=%q, want tenant-own", bucket)
	}
}

// releaseMongoDBCleanup must strip the finalizer off the object the caller
// already read and confirmed owned, without a second Get whose transient
// failure would silently drop the strip and wedge a terminating namespace.
func TestReleaseMongoDBCleanup_UsesPassedObjectNotReRead(t *testing.T) {
	owned := &psmdbtypes.PerconaServerMongoDBBackup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "op-bk", Finalizers: []string{psmdbDeleteBackupFinalizer}},
	}
	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	_ = backupsv1alpha1.AddToScheme(s)
	_ = psmdbtypes.AddToScheme(s)
	c := clientfake.NewClientBuilder().
		WithScheme(s).
		WithObjects(owned.DeepCopy()).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*psmdbtypes.PerconaServerMongoDBBackup); ok {
					return apierrors.NewServiceUnavailable("read throttled")
				}
				return cl.Get(ctx, key, obj, opts...)
			},
		}).
		Build()
	rec := record.NewFakeRecorder(10)
	r := &BackupReconciler{Client: c, Recorder: rec}
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "cozy-bk"},
		Spec:       backupsv1alpha1.BackupSpec{DriverMetadata: map[string]string{psmdbBackupNameKey: "op-bk"}},
	}

	res, err := r.releaseMongoDBCleanup(context.Background(), backup, "op-bk", owned.DeepCopy(), "namespace is terminating")
	if err != nil || res.RequeueAfter != 0 {
		t.Fatalf("release must proceed, got res=%+v err=%v", res, err)
	}
	// The owned-strip path fires ArtifactNotDeleted; if the code had re-read the
	// CR (Get throttled), it would have taken the quiet "nothing of ours" path
	// with no event — so the event proves the passed object was used.
	select {
	case ev := <-rec.Events:
		if !strings.Contains(ev, "ArtifactNotDeleted") {
			t.Errorf("expected the owned-release event proving the passed object was used, got %q", ev)
		}
	default:
		t.Errorf("expected an ArtifactNotDeleted event (passed object used, strip ran), got none")
	}
}

// A streaming dump on the useSystemBucket flow is neither cancelled nor failed
// on the deadline: the pinned operator persists status only on a state/error
// change, so status.lastTransition freezes at the instant the dump entered
// running, and a real dataset routinely outruns any window. The wait is named
// on the BackupJob (Ready=False with the start time) rather than silent. On the
// legacy flow the archive lands in the tenant's own bucket under psmdb task
// retention, so the pre-existing deadline still fails the job — and the operator
// CR is left alone either way.
func TestReconcileMongoDB_RunningPastDeadline(t *testing.T) {
	setup := func(t *testing.T, useSystemBucket bool) (*BackupJobReconciler, client.Client, *ResolvedBackupConfig, *backupsv1alpha1.BackupJob) {
		job, strategy, app, cluster, resolved := mongodbInjectFixture(true)
		app.Spec.Backup.UseSystemBucket = useSystemBucket
		job.Status.StartedAt = &metav1.Time{Time: time.Now().Add(-4 * psmdbDefaultBackupDeadline)}
		bucket := "cozy-backups"
		if !useSystemBucket {
			// Legacy: the strategy carries no coordinates to inject and the cluster
			// declares the tenant's own bucket.
			strategy.Spec.Template.S3 = nil
			bucket = "tenant-own"
		}
		cluster.Spec.Backup.Storages = map[string]runtime.RawExtension{
			"s3-storage": {Raw: []byte(fmt.Sprintf(`{"type":"s3","s3":{"bucket":%q}}`, bucket))},
		}
		running := &psmdbtypes.PerconaServerMongoDBBackup{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "tenant",
				Name:      "op-running",
				Labels: map[string]string{
					backupsv1alpha1.OwningJobNameLabel:      job.Name,
					backupsv1alpha1.OwningJobNamespaceLabel: job.Namespace,
				},
			},
			// lastTransition frozen at the start, as the pinned operator leaves it.
			Status: psmdbtypes.PerconaServerMongoDBBackupStatus{
				State:          psmdbtypes.StateRunning,
				LastTransition: &metav1.Time{Time: time.Now().Add(-4 * psmdbDefaultBackupDeadline)},
			},
		}
		if useSystemBucket {
			running.Finalizers = []string{psmdbDeleteBackupFinalizer}
		}
		c := newMongoDBStrategyTestClient(t, job, strategy, app, cluster, running)
		return &BackupJobReconciler{Client: c, Scheme: c.Scheme(), Recorder: record.NewFakeRecorder(10)}, c, resolved, job
	}
	getCR := func(t *testing.T, c client.Client) *psmdbtypes.PerconaServerMongoDBBackup {
		got := &psmdbtypes.PerconaServerMongoDBBackup{}
		if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant", Name: "op-running"}, got); err != nil {
			t.Fatalf("get running backup: %v", err)
		}
		return got
	}
	getJob := func(t *testing.T, c client.Client, name string) *backupsv1alpha1.BackupJob {
		p := &backupsv1alpha1.BackupJob{}
		if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant", Name: name}, p); err != nil {
			t.Fatalf("get job: %v", err)
		}
		return p
	}

	t.Run("system bucket: left to finish, wait named on the job", func(t *testing.T) {
		r, c, resolved, job := setup(t, true)
		res, err := r.reconcileMongoDB(context.Background(), job.DeepCopy(), resolved)
		if err != nil {
			t.Fatalf("reconcileMongoDB: %v", err)
		}
		if res.RequeueAfter == 0 {
			t.Fatalf("expected a poll requeue for a running system-bucket backup, got %+v", res)
		}
		if !getCR(t, c).DeletionTimestamp.IsZero() {
			t.Errorf("a running system-bucket backup must never be cancelled; it was deleted")
		}
		p := getJob(t, c, job.Name)
		if p.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
			t.Errorf("a running system-bucket backup must not fail the BackupJob on the deadline")
		}
		cond := apimeta.FindStatusCondition(p.Status.Conditions, "Ready")
		if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "PerconaServerMongoDBBackupRunning" || !strings.Contains(cond.Message, "streaming since") {
			t.Errorf("expected Ready=False PerconaServerMongoDBBackupRunning naming the start time, got %+v", cond)
		}
	})

	t.Run("legacy: the deadline still fails the job, CR left alone", func(t *testing.T) {
		r, c, resolved, job := setup(t, false)
		if _, err := r.reconcileMongoDB(context.Background(), job.DeepCopy(), resolved); err != nil {
			t.Fatalf("reconcileMongoDB: %v", err)
		}
		p := getJob(t, c, job.Name)
		if p.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
			t.Fatalf("a legacy backup running past the deadline must fail the BackupJob, got phase=%q", p.Status.Phase)
		}
		cond := apimeta.FindStatusCondition(p.Status.Conditions, "Ready")
		if cond == nil || !strings.Contains(cond.Message, "did not complete within") {
			t.Errorf("expected a Ready condition naming the deadline, got %+v", cond)
		}
		if !getCR(t, c).DeletionTimestamp.IsZero() {
			t.Errorf("the legacy operator CR must be left alone (hands-off); it was deleted")
		}
	})
}

// The storage-race retry hinges on recognising the operator's own
// unable-to-get-storage error and only that: a different error, or the right
// error naming a different storage, must not be swallowed as retryable.
func TestPsmdbErrorIsUnresolvedStorage(t *testing.T) {
	cases := []struct {
		msg, storage string
		want         bool
	}{
		{`unable to get storage "s3-storage": not found`, "s3-storage", true},
		{`unable to get storage "other": not found`, "s3-storage", false},
		{"connection refused", "s3-storage", false},
		{"", "s3-storage", false},
	}
	for _, tc := range cases {
		if got := psmdbErrorIsUnresolvedStorage(tc.msg, tc.storage); got != tc.want {
			t.Errorf("psmdbErrorIsUnresolvedStorage(%q, %q): got %v want %v", tc.msg, tc.storage, got, tc.want)
		}
	}
}

// The first-injection requeue waits for the cluster cache to reflect the applied
// storage, but that wait is deadline-bounded: a cluster whose cache never
// reflects it must fail terminally, not requeue forever. Pin the bound — a fresh
// injection past the deadline fails the BackupJob without minting a Backup CR.
func TestReconcileMongoDB_FirstInjectionDeadlineFails(t *testing.T) {
	job, strategy, app, cluster, resolved := mongodbInjectFixture(true)
	// No storage declared (fresh injection) and started well past the deadline.
	job.Status.StartedAt = &metav1.Time{Time: time.Now().Add(-2 * psmdbDefaultBackupDeadline)}

	c := newMongoDBStrategyTestClient(t, job, strategy, app, cluster)
	r := &BackupJobReconciler{Client: c, Scheme: c.Scheme()}

	if _, err := r.reconcileMongoDB(context.Background(), job.DeepCopy(), resolved); err != nil {
		t.Fatalf("reconcileMongoDB: %v", err)
	}

	persisted := &backupsv1alpha1.BackupJob{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant", Name: job.Name}, persisted); err != nil {
		t.Fatalf("get job: %v", err)
	}
	if persisted.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
		t.Fatalf("expected the BackupJob to be Failed past the injection deadline, got phase=%q", persisted.Status.Phase)
	}
	cond := apimeta.FindStatusCondition(persisted.Status.Conditions, "Ready")
	if cond == nil || !strings.Contains(cond.Message, "did not reflect the injected storage") {
		t.Errorf("expected a Ready condition naming the unreflected storage, got %+v", cond)
	}
	list := &psmdbtypes.PerconaServerMongoDBBackupList{}
	if err := c.List(context.Background(), list, client.InNamespace("tenant")); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 0 {
		t.Errorf("no operator Backup should be minted on the deadline failure, got %d", len(list.Items))
	}
}

// The residual injection race: the operator can latch the Backup CR at
// state=error because it resolved storage from a cache that had not observed the
// apply. Within the deadline the driver must delete that CR and requeue (a fresh
// one resolves against a caught-up cache), not fail the BackupJob — that error
// is the driver's own race, not the tenant's failure.
func TestReconcileMongoDB_StorageRaceErrorRetried(t *testing.T) {
	job, strategy, app, cluster, resolved := mongodbInjectFixture(true)
	job.Status.StartedAt = &metav1.Time{Time: time.Now()} // within deadline
	// Storage already declared so the reconcile skips the first-injection requeue
	// and reaches the state switch.
	cluster.Spec.Backup.Storages = map[string]runtime.RawExtension{
		"s3-storage": {Raw: []byte(`{"type":"s3","s3":{"bucket":"cozy-backups"}}`)},
	}
	errored := &psmdbtypes.PerconaServerMongoDBBackup{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:  "tenant",
			Name:       "op-errored",
			Finalizers: []string{psmdbDeleteBackupFinalizer},
			Labels: map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      job.Name,
				backupsv1alpha1.OwningJobNamespaceLabel: job.Namespace,
			},
		},
		Status: psmdbtypes.PerconaServerMongoDBBackupStatus{
			State: psmdbtypes.StateError,
			Error: `unable to get storage "s3-storage": not found`,
		},
	}

	c := newMongoDBStrategyTestClient(t, job, strategy, app, cluster, errored)
	r := &BackupJobReconciler{Client: c, Scheme: c.Scheme()}

	res, err := r.reconcileMongoDB(context.Background(), job.DeepCopy(), resolved)
	if err != nil {
		t.Fatalf("reconcileMongoDB: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Fatalf("expected a retry requeue on the storage-race error, got %+v", res)
	}
	// The errored CR is deleted (Terminating with its finalizer) so a fresh one
	// resolves against a caught-up cache.
	got := &psmdbtypes.PerconaServerMongoDBBackup{}
	gerr := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant", Name: "op-errored"}, got)
	if gerr == nil && got.DeletionTimestamp.IsZero() {
		t.Errorf("the storage-race errored CR must be deleted before retrying; it was left untouched")
	}
	// The BackupJob is NOT failed.
	persisted := &backupsv1alpha1.BackupJob{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant", Name: job.Name}, persisted); err != nil {
		t.Fatalf("get job: %v", err)
	}
	if persisted.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
		t.Errorf("the storage-race error must be retried, not fail the BackupJob")
	}
}

// The storage-race retry is a system-bucket repair: it deletes the errored CR
// and re-mints. On the legacy flow the same operator error is the tenant's own
// storage problem and the CR is the tenant's — it must be left alone and the
// job failed terminally, never deleted and retried.
func TestReconcileMongoDB_StorageRaceErrorIsTerminalOnLegacy(t *testing.T) {
	job, strategy, app, cluster, resolved := mongodbInjectFixture(true)
	app.Spec.Backup.UseSystemBucket = false
	strategy.Spec.Template.S3 = nil
	job.Status.StartedAt = &metav1.Time{Time: time.Now()}
	cluster.Spec.Backup.Storages = map[string]runtime.RawExtension{
		"s3-storage": {Raw: []byte(`{"type":"s3","s3":{"bucket":"tenant-own","credentialsSecret":"mongodb-app1-s3-creds"}}`)},
	}
	errored := &psmdbtypes.PerconaServerMongoDBBackup{ // no delete-backup finalizer: legacy
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "tenant", Name: "op-legacy-errored",
			Labels: map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      job.Name,
				backupsv1alpha1.OwningJobNamespaceLabel: job.Namespace,
			},
		},
		Status: psmdbtypes.PerconaServerMongoDBBackupStatus{State: psmdbtypes.StateError, Error: `unable to get storage "s3-storage": not found`},
	}
	c := newMongoDBStrategyTestClient(t, job, strategy, app, cluster, errored)
	r := &BackupJobReconciler{Client: c, Scheme: c.Scheme(), Recorder: record.NewFakeRecorder(10)}
	if _, err := r.reconcileMongoDB(context.Background(), job.DeepCopy(), resolved); err != nil {
		t.Fatalf("reconcileMongoDB: %v", err)
	}
	got := &psmdbtypes.PerconaServerMongoDBBackup{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant", Name: "op-legacy-errored"}, got); err != nil || !got.DeletionTimestamp.IsZero() {
		t.Errorf("a legacy operator CR must never be deleted by the storage-race retry, err=%v", err)
	}
	persisted := &backupsv1alpha1.BackupJob{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant", Name: job.Name}, persisted); err != nil {
		t.Fatalf("get job: %v", err)
	}
	if persisted.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
		t.Errorf("a legacy storage error is terminal, got phase=%q", persisted.Status.Phase)
	}
}

// The retry deletes the errored CR, but its release-lock finalizer keeps it
// briefly Terminating. The lookup must skip a Terminating CR so a fresh one is
// minted (against a caught-up cache) rather than re-observing the same error on
// the lingering object and requeuing forever.
func TestReconcileMongoDB_StorageRaceReMintsPastLingeringCR(t *testing.T) {
	job, strategy, app, cluster, resolved := mongodbInjectFixture(true)
	job.Status.StartedAt = &metav1.Time{Time: time.Now()} // within deadline
	cluster.Spec.Backup.Storages = map[string]runtime.RawExtension{
		"s3-storage": {Raw: []byte(`{"type":"s3","s3":{"bucket":"cozy-backups"}}`)},
	}
	// An errored CR from a previous retry, still carrying the finalizer.
	lingering := &psmdbtypes.PerconaServerMongoDBBackup{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:  "tenant",
			Name:       "op-lingering",
			Finalizers: []string{psmdbDeleteBackupFinalizer},
			Labels: map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      job.Name,
				backupsv1alpha1.OwningJobNamespaceLabel: job.Namespace,
			},
		},
		Status: psmdbtypes.PerconaServerMongoDBBackupStatus{State: psmdbtypes.StateError, Error: `unable to get storage "s3-storage": not found`},
	}
	c := newMongoDBStrategyTestClient(t, job, strategy, app, cluster, lingering)
	// Delete it so the finalizer holds it Terminating (DeletionTimestamp set).
	if err := c.Delete(context.Background(), lingering); err != nil {
		t.Fatalf("delete to make it Terminating: %v", err)
	}
	r := &BackupJobReconciler{Client: c, Scheme: c.Scheme(), Recorder: record.NewFakeRecorder(10)}

	if _, err := r.reconcileMongoDB(context.Background(), job.DeepCopy(), resolved); err != nil {
		t.Fatalf("reconcileMongoDB: %v", err)
	}

	// A fresh, non-Terminating CR must exist — the retry re-minted past the
	// lingering one instead of re-observing it.
	list := &psmdbtypes.PerconaServerMongoDBBackupList{}
	if err := c.List(context.Background(), list, client.InNamespace("tenant")); err != nil {
		t.Fatalf("list: %v", err)
	}
	fresh := 0
	for i := range list.Items {
		if list.Items[i].DeletionTimestamp.IsZero() {
			fresh++
		}
	}
	if fresh == 0 {
		t.Errorf("expected a fresh operator Backup minted past the Terminating CR, got none (lookup did not skip the lingering CR)")
	}
}

// psmdbBackupUnstructured builds a live PerconaServerMongoDBBackup for the fake
// dynamic client, carrying status.state (omitted when empty).
func psmdbBackupUnstructured(namespace, name, state string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   psmdbBackupGVR.Group,
		Version: psmdbBackupGVR.Version,
		Kind:    "PerconaServerMongoDBBackup",
	})
	u.SetNamespace(namespace)
	u.SetName(name)
	if state != "" {
		_ = unstructured.SetNestedField(u.Object, state, "status", "state")
	}
	return u
}

// newPsmdbBackupDynamicClient is the dynamic (uncached) client the cancel path
// re-reads live state through. Modelled on altinitystrategy_controller_test.go.
func newPsmdbBackupDynamicClient(objs ...*unstructured.Unstructured) *dynamicfake.FakeDynamicClient {
	rtObjs := make([]runtime.Object, 0, len(objs))
	for _, o := range objs {
		rtObjs = append(rtObjs, o)
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{psmdbBackupGVR: "PerconaServerMongoDBBackupList"},
		rtObjs...,
	)
}

// The deadline cancel re-reads the CR live and must delete it only on a positive
// not-started answer: a live state that has moved to running or ready, or a read
// that cannot be answered at all, must defer instead of deleting a CR whose
// delete-backup finalizer would take a running partial or a completed archive
// with it. The cached mdbBackup reads not-started (waiting) past the deadline in
// every case; only the live answer differs.
func TestReconcileMongoDB_TimedOutCancelUsesLiveState(t *testing.T) {
	setup := func(t *testing.T, live *dynamicfake.FakeDynamicClient) (*BackupJobReconciler, client.Client, *ResolvedBackupConfig, *backupsv1alpha1.BackupJob) {
		job, strategy, app, cluster, resolved := mongodbInjectFixture(true)
		// Past the deadline but inside the live-read grace.
		job.Status.StartedAt = &metav1.Time{Time: time.Now().Add(-psmdbDefaultBackupDeadline - psmdbLiveReadGrace/2)}
		cluster.Spec.Backup.Storages = map[string]runtime.RawExtension{
			"s3-storage": {Raw: []byte(`{"type":"s3","s3":{"bucket":"cozy-backups"}}`)},
		}
		// Cached CR reads a not-started state (waiting) past the deadline.
		cached := &psmdbtypes.PerconaServerMongoDBBackup{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "tenant",
				Name:       "op-waiting",
				Finalizers: []string{psmdbDeleteBackupFinalizer},
				Labels: map[string]string{
					backupsv1alpha1.OwningJobNameLabel:      job.Name,
					backupsv1alpha1.OwningJobNamespaceLabel: job.Namespace,
				},
			},
			Status: psmdbtypes.PerconaServerMongoDBBackupStatus{State: psmdbtypes.StateWaiting},
		}
		c := newMongoDBStrategyTestClient(t, job, strategy, app, cluster, cached)
		return &BackupJobReconciler{Client: c, Interface: live, Scheme: c.Scheme(), Recorder: record.NewFakeRecorder(10)}, c, resolved, job
	}
	deleted := func(t *testing.T, c client.Client) bool {
		got := &psmdbtypes.PerconaServerMongoDBBackup{}
		err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant", Name: "op-waiting"}, got)
		return err == nil && !got.DeletionTimestamp.IsZero()
	}
	failed := func(t *testing.T, c client.Client, name string) bool {
		p := &backupsv1alpha1.BackupJob{}
		if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant", Name: name}, p); err != nil {
			t.Fatalf("get job: %v", err)
		}
		return p.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed
	}

	t.Run("live still not-started → cancel and fail", func(t *testing.T) {
		live := newPsmdbBackupDynamicClient(psmdbBackupUnstructured("tenant", "op-waiting", psmdbtypes.StateWaiting))
		r, c, resolved, job := setup(t, live)
		if _, err := r.reconcileMongoDB(context.Background(), job.DeepCopy(), resolved); err != nil {
			t.Fatalf("reconcileMongoDB: %v", err)
		}
		if !deleted(t, c) {
			t.Errorf("a positively not-started backup past the deadline must be cancelled")
		}
		if !failed(t, c, job.Name) {
			t.Errorf("the BackupJob must be Failed after cancelling a timed-out not-started backup")
		}
	})

	t.Run("live turned running → defer, never cancel", func(t *testing.T) {
		live := newPsmdbBackupDynamicClient(psmdbBackupUnstructured("tenant", "op-waiting", psmdbtypes.StateRunning))
		r, c, resolved, job := setup(t, live)
		res, err := r.reconcileMongoDB(context.Background(), job.DeepCopy(), resolved)
		if err != nil {
			t.Fatalf("reconcileMongoDB: %v", err)
		}
		if res.RequeueAfter == 0 {
			t.Fatalf("a live running state must requeue, got %+v", res)
		}
		if deleted(t, c) {
			t.Errorf("a CR whose live state turned running must not be cancelled (partial archive)")
		}
		if failed(t, c, job.Name) {
			t.Errorf("the BackupJob must not fail while the live backup is running")
		}
	})

	t.Run("live completed ready → defer, archive protected", func(t *testing.T) {
		live := newPsmdbBackupDynamicClient(psmdbBackupUnstructured("tenant", "op-waiting", psmdbtypes.StateReady))
		r, c, resolved, job := setup(t, live)
		if _, err := r.reconcileMongoDB(context.Background(), job.DeepCopy(), resolved); err != nil {
			t.Fatalf("reconcileMongoDB: %v", err)
		}
		if deleted(t, c) {
			t.Errorf("a just-completed (ready) CR must not be deleted — its delete-backup finalizer would prune the archive")
		}
	})

	t.Run("live read cannot be answered → defer, never cancel", func(t *testing.T) {
		// Empty dynamic store: the live Get returns NotFound (an error), which must
		// not be read as a not-started answer.
		live := newPsmdbBackupDynamicClient()
		r, c, resolved, job := setup(t, live)
		res, err := r.reconcileMongoDB(context.Background(), job.DeepCopy(), resolved)
		if err != nil {
			t.Fatalf("reconcileMongoDB: %v", err)
		}
		if res.RequeueAfter == 0 {
			t.Fatalf("an unanswerable live read must requeue, got %+v", res)
		}
		if deleted(t, c) {
			t.Errorf("an unanswerable live read must defer, not delete blind")
		}
		if failed(t, c, job.Name) {
			t.Errorf("an unanswerable live read must not fail the BackupJob inside the grace")
		}
		p := &backupsv1alpha1.BackupJob{}
		if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant", Name: job.Name}, p); err != nil {
			t.Fatalf("get job: %v", err)
		}
		if cond := apimeta.FindStatusCondition(p.Status.Conditions, "Ready"); cond == nil || cond.Reason != "PerconaServerMongoDBLiveStateUnknown" {
			t.Errorf("the deferral must be named on the job (Ready=False PerconaServerMongoDBLiveStateUnknown), got %+v", cond)
		}
	})

	// Deferring cannot be forever: once the grace past the deadline is spent the
	// job fails, and the CR is still not deleted blind.
	t.Run("live read still unanswerable past the grace → fail, CR left alone", func(t *testing.T) {
		live := newPsmdbBackupDynamicClient()
		r, c, resolved, job := setup(t, live)
		job.Status.StartedAt = &metav1.Time{Time: time.Now().Add(-2*psmdbDefaultBackupDeadline - psmdbLiveReadGrace)}
		if _, err := r.reconcileMongoDB(context.Background(), job.DeepCopy(), resolved); err != nil {
			t.Fatalf("reconcileMongoDB: %v", err)
		}
		if deleted(t, c) {
			t.Errorf("an unconfirmed CR must never be deleted blind, even when the job finally fails")
		}
		if !failed(t, c, job.Name) {
			t.Errorf("an unreadable CR must not pin the BackupJob Running forever; expected Failed past the grace")
		}
	})

	t.Run("live requested → fail but leave the CR (already dispatched to pbm)", func(t *testing.T) {
		live := newPsmdbBackupDynamicClient(psmdbBackupUnstructured("tenant", "op-waiting", psmdbtypes.StateRequested))
		r, c, resolved, job := setup(t, live)
		if _, err := r.reconcileMongoDB(context.Background(), job.DeepCopy(), resolved); err != nil {
			t.Fatalf("reconcileMongoDB: %v", err)
		}
		if deleted(t, c) {
			t.Errorf("a requested CR was already handed to pbm; deleting it cannot recall the command and would orphan the dump")
		}
		if !failed(t, c, job.Name) {
			t.Errorf("the BackupJob must still fail on the deadline for a requested-but-never-run backup")
		}
	})

	// The legacy flow keeps its hands-off contract: the deadline fails the job
	// and the operator CR — the tenant's — is left in place, exactly as before
	// this driver existed. No live read is consulted.
	t.Run("legacy waiting past deadline → fail, CR left alone", func(t *testing.T) {
		job, strategy, app, cluster, resolved := mongodbInjectFixture(true)
		app.Spec.Backup.UseSystemBucket = false
		strategy.Spec.Template.S3 = nil
		job.Status.StartedAt = &metav1.Time{Time: time.Now().Add(-2 * psmdbDefaultBackupDeadline)}
		cluster.Spec.Backup.Storages = map[string]runtime.RawExtension{
			"s3-storage": {Raw: []byte(`{"type":"s3","s3":{"bucket":"tenant-own","credentialsSecret":"mongodb-app1-s3-creds"}}`)},
		}
		cached := &psmdbtypes.PerconaServerMongoDBBackup{ // no delete-backup finalizer: legacy
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "tenant", Name: "op-waiting",
				Labels: map[string]string{
					backupsv1alpha1.OwningJobNameLabel:      job.Name,
					backupsv1alpha1.OwningJobNamespaceLabel: job.Namespace,
				},
			},
			Status: psmdbtypes.PerconaServerMongoDBBackupStatus{State: psmdbtypes.StateWaiting},
		}
		c := newMongoDBStrategyTestClient(t, job, strategy, app, cluster, cached)
		// No dynamic client at all: the legacy path must not need a live read.
		r := &BackupJobReconciler{Client: c, Scheme: c.Scheme(), Recorder: record.NewFakeRecorder(10)}
		if _, err := r.reconcileMongoDB(context.Background(), job.DeepCopy(), resolved); err != nil {
			t.Fatalf("reconcileMongoDB: %v", err)
		}
		if !failed(t, c, job.Name) {
			t.Errorf("a legacy backup that never started must fail on the deadline")
		}
		got := &psmdbtypes.PerconaServerMongoDBBackup{}
		if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant", Name: "op-waiting"}, got); err != nil || !got.DeletionTimestamp.IsZero() {
			t.Errorf("the legacy operator CR must be left alone (hands-off), err=%v", err)
		}
	})
}

// PITR needs an oplog stream the useSystemBucket flow never captures, so a
// recoveryTime restore of a backup taken on that flow (recorded by the snapshot
// flag, not inferred from a Secret name) must fail with a named error rather
// than hand the operator a target it cannot serve. The refusal fires before the
// target cluster read, so no target need exist.
func TestReconcileMongoDBRestore_RefusesPITROnSystemBucketBackup(t *testing.T) {
	apps := mongodbapp.GroupName
	snap := &psmdbtypes.PerconaServerMongoDBBackup{
		Status: psmdbtypes.PerconaServerMongoDBBackupStatus{
			Destination: "s3://cozy-backups/tenant/app1/2026",
			S3:          &psmdbtypes.BackupStorageS3{Bucket: "cozy-backups", CredentialsSecret: psmdbDefaultCredentialsSecret},
		},
	}
	raw, err := marshalMongoDBBackupSnapshot(snap, &strategyv1alpha1.MongoDBTemplate{Type: "logical"}, "s3-storage", nil, true)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "src-backup"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{Kind: "MongoDB", Name: "app1", APIGroup: &apps},
		},
		Status: backupsv1alpha1.BackupStatus{UnderlyingResources: raw},
	}
	restoreJob := newMongoDBRestoreJob("rj-pitr", "tenant")
	restoreJob.Status.StartedAt = &metav1.Time{Time: time.Now()}
	restoreJob.Status.Phase = backupsv1alpha1.RestoreJobPhaseRunning
	restoreJob.Spec.Options = &runtime.RawExtension{Raw: []byte(`{"recoveryTime":"2026-08-05T12:34:56Z"}`)}

	c := newMongoDBStrategyTestClient(t, backup, restoreJob)
	r := &RestoreJobReconciler{Client: c, Scheme: c.Scheme(), Recorder: record.NewFakeRecorder(10)}

	if _, err := r.reconcileMongoDBRestore(context.Background(), restoreJob.DeepCopy(), backup); err != nil {
		t.Fatalf("reconcileMongoDBRestore: %v", err)
	}
	persisted := &backupsv1alpha1.RestoreJob{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant", Name: "rj-pitr"}, persisted); err != nil {
		t.Fatalf("get restore job: %v", err)
	}
	if persisted.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
		t.Fatalf("expected the RestoreJob to fail on a PITR request over a system-bucket backup, got phase=%q", persisted.Status.Phase)
	}
	cond := apimeta.FindStatusCondition(persisted.Status.Conditions, "Ready")
	if cond == nil || !strings.Contains(cond.Message, "point-in-time recovery is not available") {
		t.Errorf("expected a Ready condition explaining PITR is unavailable, got %+v", cond)
	}
}

// The refusal keys on the snapshot flow flag, not on the credentialsSecret name:
// a legacy (non-system-bucket) backup that happens to resolve to cozy-backups-creds
// must NOT have its recoveryTime restore refused. Keeps the check honest — the
// Secret name is a proxy a BackupClass can defeat either way.
func TestReconcileMongoDBRestore_PITRAllowedForNonSystemBucketBackup(t *testing.T) {
	apps := mongodbapp.GroupName
	snap := &psmdbtypes.PerconaServerMongoDBBackup{
		Status: psmdbtypes.PerconaServerMongoDBBackupStatus{
			Destination: "s3://tenant-own/tenant/app1/2026",
			S3:          &psmdbtypes.BackupStorageS3{Bucket: "tenant-own", CredentialsSecret: psmdbDefaultCredentialsSecret},
		},
	}
	// useSystemBucket=false recorded on the snapshot despite the cozy-backups-creds name.
	raw, err := marshalMongoDBBackupSnapshot(snap, &strategyv1alpha1.MongoDBTemplate{Type: "logical"}, "s3-storage", nil, false)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "src-backup"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{Kind: "MongoDB", Name: "app1", APIGroup: &apps},
		},
		Status: backupsv1alpha1.BackupStatus{UnderlyingResources: raw},
	}
	restoreJob := newMongoDBRestoreJob("rj-pitr-legacy", "tenant")
	restoreJob.Status.StartedAt = &metav1.Time{Time: time.Now()}
	restoreJob.Status.Phase = backupsv1alpha1.RestoreJobPhaseRunning
	restoreJob.Spec.Options = &runtime.RawExtension{Raw: []byte(`{"recoveryTime":"2026-08-05T12:34:56Z"}`)}

	c := newMongoDBStrategyTestClient(t, backup, restoreJob)
	r := &RestoreJobReconciler{Client: c, Scheme: c.Scheme(), Recorder: record.NewFakeRecorder(10)}

	// No target cluster exists, so the reconcile requeues on the target read — it
	// must get past the PITR refusal to reach it. A PITR-refusal failure here would
	// mean the check still keyed on the Secret name.
	if _, err := r.reconcileMongoDBRestore(context.Background(), restoreJob.DeepCopy(), backup); err != nil {
		t.Fatalf("reconcileMongoDBRestore: %v", err)
	}
	persisted := &backupsv1alpha1.RestoreJob{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant", Name: "rj-pitr-legacy"}, persisted); err != nil {
		t.Fatalf("get restore job: %v", err)
	}
	if persisted.Status.Phase == backupsv1alpha1.RestoreJobPhaseFailed {
		cond := apimeta.FindStatusCondition(persisted.Status.Conditions, "Ready")
		if cond != nil && strings.Contains(cond.Message, "point-in-time recovery is not available") {
			t.Errorf("PITR must not be refused for a non-system-bucket backup (keyed on Secret name, not the flow): %+v", cond)
		}
	}
}

// A recoveryTime restore must know which flow the backup was taken on; a
// snapshot that fails to decode leaves that unknown, and the refusal must fail
// closed rather than hand the operator a point-in-time target it may not serve.
// The live operator CR is present so source resolution succeeds on the live path
// (which never consults the snapshot) — exactly the case that reaches this guard.
func TestReconcileMongoDBRestore_PITRRefusedWhenSnapshotUndecodable(t *testing.T) {
	apps := mongodbapp.GroupName
	live := &psmdbtypes.PerconaServerMongoDBBackup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "op-backup"},
		Status: psmdbtypes.PerconaServerMongoDBBackupStatus{
			State:       psmdbtypes.StateReady,
			Destination: "s3://cozy-backups/tenant/app1/2026",
			S3:          &psmdbtypes.BackupStorageS3{Bucket: "cozy-backups", CredentialsSecret: psmdbDefaultCredentialsSecret},
		},
	}
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "src-backup"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{Kind: "MongoDB", Name: "app1", APIGroup: &apps},
			DriverMetadata: map[string]string{psmdbBackupNameKey: "op-backup"},
		},
		// Valid JSON, wrong shape: the flag field is not a bool, so the typed decode fails.
		Status: backupsv1alpha1.BackupStatus{UnderlyingResources: &runtime.RawExtension{Raw: []byte(`{"kind":"MongoDBBackupSnapshot","useSystemBucket":"not-a-bool"}`)}},
	}
	restoreJob := newMongoDBRestoreJob("rj-pitr-bad", "tenant")
	restoreJob.Status.StartedAt = &metav1.Time{Time: time.Now()}
	restoreJob.Status.Phase = backupsv1alpha1.RestoreJobPhaseRunning
	restoreJob.Spec.Options = &runtime.RawExtension{Raw: []byte(`{"recoveryTime":"2026-08-05T12:34:56Z"}`)}

	c := newMongoDBStrategyTestClient(t, backup, restoreJob, live)
	r := &RestoreJobReconciler{Client: c, Scheme: c.Scheme(), Recorder: record.NewFakeRecorder(10)}

	if _, err := r.reconcileMongoDBRestore(context.Background(), restoreJob.DeepCopy(), backup); err != nil {
		t.Fatalf("reconcileMongoDBRestore: %v", err)
	}
	persisted := &backupsv1alpha1.RestoreJob{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant", Name: "rj-pitr-bad"}, persisted); err != nil {
		t.Fatalf("get restore job: %v", err)
	}
	if persisted.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
		t.Fatalf("a PITR request over an undecodable snapshot must fail closed, got phase=%q", persisted.Status.Phase)
	}
	cond := apimeta.FindStatusCondition(persisted.Status.Conditions, "Ready")
	if cond == nil || !strings.Contains(cond.Message, "cannot be decoded") {
		t.Errorf("expected a Ready condition naming the undecodable snapshot, got %+v", cond)
	}
}

// After an app opts into useSystemBucket the chart prunes its legacy
// <release>-s3-creds, and its live storage moves to the platform bucket, so a
// restore of an older legacy backup finds no same-bucket credential to adopt
// and keeps the source's — now deleted — Secret. It must fail with a legible,
// named reason before the operator sees a reference to a Secret that is gone.
func TestReconcileMongoDBRestore_FailsNamedWhenCredentialsSecretMissing(t *testing.T) {
	apps := mongodbapp.GroupName
	snap := &psmdbtypes.PerconaServerMongoDBBackup{
		Status: psmdbtypes.PerconaServerMongoDBBackupStatus{
			Destination: "s3://tenant-own/tenant/app1/2026",
			S3:          &psmdbtypes.BackupStorageS3{Bucket: "tenant-own", CredentialsSecret: "mongodb-app1-s3-creds"},
		},
	}
	raw, err := marshalMongoDBBackupSnapshot(snap, &strategyv1alpha1.MongoDBTemplate{Type: "logical"}, "s3-storage", nil, false)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	newBackup := func() *backupsv1alpha1.Backup {
		return &backupsv1alpha1.Backup{
			ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "src-backup"},
			Spec: backupsv1alpha1.BackupSpec{
				ApplicationRef: corev1.TypedLocalObjectReference{Kind: "MongoDB", Name: "app1", APIGroup: &apps},
			},
			Status: backupsv1alpha1.BackupStatus{UnderlyingResources: raw},
		}
	}
	// The same app, now on the system bucket: its only storage is on cozy-backups,
	// so nothing on tenant-own is adoptable.
	newTarget := func() *psmdbtypes.PerconaServerMongoDB {
		return &psmdbtypes.PerconaServerMongoDB{
			ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: mongodbNameForApp("app1")},
			Spec: psmdbtypes.PerconaServerMongoDBSpec{Backup: psmdbtypes.PerconaServerMongoDBBackupConfig{
				Enabled:  true,
				Storages: map[string]runtime.RawExtension{"s3-storage": {Raw: []byte(`{"type":"s3","s3":{"bucket":"cozy-backups","credentialsSecret":"cozy-backups-creds"}}`)}},
			}},
		}
	}
	newRJ := func(name string) *backupsv1alpha1.RestoreJob {
		rj := newMongoDBRestoreJob(name, "tenant")
		rj.Status.StartedAt = &metav1.Time{Time: time.Now()}
		rj.Status.Phase = backupsv1alpha1.RestoreJobPhaseRunning
		return rj
	}

	getRJ := func(t *testing.T, c client.Client, name string) *backupsv1alpha1.RestoreJob {
		p := &backupsv1alpha1.RestoreJob{}
		if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant", Name: name}, p); err != nil {
			t.Fatalf("get restore job: %v", err)
		}
		return p
	}

	// Like the sibling preconditions (target absent, backups disabled) the check
	// waits until the restore deadline — the Secret can be re-created — naming
	// the reason on the job, and only then fails.
	t.Run("missing Secret waits with a named reason", func(t *testing.T) {
		backup, rj := newBackup(), newRJ("rj-nocred")
		c := newMongoDBStrategyTestClient(t, backup, rj, newTarget())
		r := &RestoreJobReconciler{Client: c, Scheme: c.Scheme(), Recorder: record.NewFakeRecorder(10)}
		res, err := r.reconcileMongoDBRestore(context.Background(), rj.DeepCopy(), backup)
		if err != nil {
			t.Fatalf("reconcileMongoDBRestore: %v", err)
		}
		if res.RequeueAfter == 0 {
			t.Fatalf("a missing Secret within the deadline must requeue, got %+v", res)
		}
		p := getRJ(t, c, "rj-nocred")
		if p.Status.Phase == backupsv1alpha1.RestoreJobPhaseFailed {
			t.Errorf("must not fail before the restore deadline")
		}
		cond := apimeta.FindStatusCondition(p.Status.Conditions, "Ready")
		if cond == nil || cond.Reason != "RestoreCredentialsMissing" || !strings.Contains(cond.Message, "mongodb-app1-s3-creds") {
			t.Errorf("expected Ready=False RestoreCredentialsMissing naming the Secret, got %+v", cond)
		}
	})

	t.Run("missing Secret past the deadline fails with the named reason", func(t *testing.T) {
		backup, rj := newBackup(), newRJ("rj-nocred-late")
		rj.Status.StartedAt = &metav1.Time{Time: time.Now().Add(-2 * psmdbDefaultRestoreDeadline)}
		c := newMongoDBStrategyTestClient(t, backup, rj, newTarget())
		r := &RestoreJobReconciler{Client: c, Scheme: c.Scheme(), Recorder: record.NewFakeRecorder(10)}
		if _, err := r.reconcileMongoDBRestore(context.Background(), rj.DeepCopy(), backup); err != nil {
			t.Fatalf("reconcileMongoDBRestore: %v", err)
		}
		p := getRJ(t, c, "rj-nocred-late")
		if p.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
			t.Fatalf("expected Failed past the deadline, got phase=%q", p.Status.Phase)
		}
		if cond := apimeta.FindStatusCondition(p.Status.Conditions, "Ready"); cond == nil || cond.Reason != "RestoreCredentialsMissing" {
			t.Errorf("expected RestoreCredentialsMissing, got %+v", cond)
		}
	})

	// Once the operator restore exists the reference has been handed over; a
	// Secret pruned mid-replay must not mark a restore still in progress Failed.
	t.Run("an existing operator restore skips the check", func(t *testing.T) {
		backup, rj := newBackup(), newRJ("rj-replaying")
		inflight := &psmdbtypes.PerconaServerMongoDBRestore{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "tenant", Name: "rj-replaying-abc",
				Labels: map[string]string{
					backupsv1alpha1.OwningJobNameLabel:      rj.Name,
					backupsv1alpha1.OwningJobNamespaceLabel: rj.Namespace,
				},
			},
			Status: psmdbtypes.PerconaServerMongoDBRestoreStatus{State: psmdbtypes.StateRunning},
		}
		c := newMongoDBStrategyTestClient(t, backup, rj, newTarget(), inflight)
		r := &RestoreJobReconciler{Client: c, Scheme: c.Scheme(), Recorder: record.NewFakeRecorder(10)}
		if _, err := r.reconcileMongoDBRestore(context.Background(), rj.DeepCopy(), backup); err != nil {
			t.Fatalf("reconcileMongoDBRestore: %v", err)
		}
		p := getRJ(t, c, "rj-replaying")
		if cond := apimeta.FindStatusCondition(p.Status.Conditions, "Ready"); cond != nil && cond.Reason == "RestoreCredentialsMissing" {
			t.Errorf("a restore already replaying must not be reported as missing credentials: %+v", cond)
		}
		if p.Status.Phase == backupsv1alpha1.RestoreJobPhaseFailed {
			t.Errorf("a restore already replaying must not be failed by the pre-check")
		}
	})

	t.Run("present Secret lets the restore proceed", func(t *testing.T) {
		backup, rj := newBackup(), newRJ("rj-cred-ok")
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "mongodb-app1-s3-creds"}}
		c := newMongoDBStrategyTestClient(t, backup, rj, newTarget(), secret)
		r := &RestoreJobReconciler{Client: c, Scheme: c.Scheme(), Recorder: record.NewFakeRecorder(10)}
		if _, err := r.reconcileMongoDBRestore(context.Background(), rj.DeepCopy(), backup); err != nil {
			t.Fatalf("reconcileMongoDBRestore: %v", err)
		}
		persisted := &backupsv1alpha1.RestoreJob{}
		if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant", Name: "rj-cred-ok"}, persisted); err != nil {
			t.Fatalf("get restore job: %v", err)
		}
		if cond := apimeta.FindStatusCondition(persisted.Status.Conditions, "Ready"); cond != nil && cond.Reason == "RestoreCredentialsMissing" {
			t.Errorf("an existing credentialsSecret must not be reported missing: %+v", cond)
		}
	})
}

// Opting out (useSystemBucket true→false) stops the injection but leaves the
// driver's storage on the live cluster until the chart re-renders. A BackupJob
// in that window must not mint a CR — it would carry no delete-backup finalizer
// and write an object nothing owns into the platform bucket. The wait is named
// and deadline-bounded like the others.
func TestReconcileMongoDB_OptOutRefusesStalePlatformStorage(t *testing.T) {
	setup := func(t *testing.T, startedAt time.Time) (*BackupJobReconciler, client.Client, *ResolvedBackupConfig, *backupsv1alpha1.BackupJob) {
		job, strategy, app, cluster, resolved := mongodbInjectFixture(true)
		app.Spec.Backup.UseSystemBucket = false // opted back out; the strategy still names cozy-backups
		job.Status.StartedAt = &metav1.Time{Time: startedAt}
		// Live cluster still carries the entry the driver injected.
		cluster.Spec.Backup.Storages = map[string]runtime.RawExtension{
			"s3-storage": {Raw: []byte(`{"type":"s3","s3":{"bucket":"cozy-backups","credentialsSecret":"cozy-backups-creds"}}`)},
		}
		c := newMongoDBStrategyTestClient(t, job, strategy, app, cluster)
		return &BackupJobReconciler{Client: c, Scheme: c.Scheme(), Recorder: record.NewFakeRecorder(10)}, c, resolved, job
	}
	countCRs := func(t *testing.T, c client.Client) int {
		list := &psmdbtypes.PerconaServerMongoDBBackupList{}
		if err := c.List(context.Background(), list, client.InNamespace("tenant")); err != nil {
			t.Fatalf("list: %v", err)
		}
		return len(list.Items)
	}

	t.Run("within the deadline: no mint, named wait", func(t *testing.T) {
		r, c, resolved, job := setup(t, time.Now())
		res, err := r.reconcileMongoDB(context.Background(), job.DeepCopy(), resolved)
		if err != nil {
			t.Fatalf("reconcileMongoDB: %v", err)
		}
		if res.RequeueAfter == 0 {
			t.Fatalf("expected a waiting requeue while the platform storage lingers, got %+v", res)
		}
		if n := countCRs(t, c); n != 0 {
			t.Errorf("no operator Backup must be minted onto the platform bucket with useSystemBucket=false, got %d", n)
		}
		persisted := &backupsv1alpha1.BackupJob{}
		if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant", Name: job.Name}, persisted); err != nil {
			t.Fatalf("get job: %v", err)
		}
		cond := apimeta.FindStatusCondition(persisted.Status.Conditions, "Ready")
		if cond == nil || cond.Reason != "PerconaServerMongoDBStorageStale" {
			t.Errorf("expected Ready=False PerconaServerMongoDBStorageStale, got %+v", cond)
		}
	})

	t.Run("past the deadline: fails, still no mint", func(t *testing.T) {
		r, c, resolved, job := setup(t, time.Now().Add(-2*psmdbDefaultBackupDeadline))
		if _, err := r.reconcileMongoDB(context.Background(), job.DeepCopy(), resolved); err != nil {
			t.Fatalf("reconcileMongoDB: %v", err)
		}
		if n := countCRs(t, c); n != 0 {
			t.Errorf("no operator Backup must be minted on the deadline failure either, got %d", n)
		}
		persisted := &backupsv1alpha1.BackupJob{}
		if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant", Name: job.Name}, persisted); err != nil {
			t.Fatalf("get job: %v", err)
		}
		if persisted.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
			t.Errorf("expected the BackupJob to fail once the stale storage outlives the deadline, got phase=%q", persisted.Status.Phase)
		}
	})

	// A CR minted while the flag was true carries the delete-backup finalizer and
	// is still streaming into the platform bucket; flipping the flag must not
	// route later polls into the refusal and fail it at the deadline.
	t.Run("a dump already in flight is not pre-empted by the refusal", func(t *testing.T) {
		r, c, resolved, job := setup(t, time.Now().Add(-2*psmdbDefaultBackupDeadline))
		running := &psmdbtypes.PerconaServerMongoDBBackup{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "tenant", Name: "op-inflight",
				Finalizers: []string{psmdbDeleteBackupFinalizer},
				Labels: map[string]string{
					backupsv1alpha1.OwningJobNameLabel:      job.Name,
					backupsv1alpha1.OwningJobNamespaceLabel: job.Namespace,
				},
			},
			Status: psmdbtypes.PerconaServerMongoDBBackupStatus{State: psmdbtypes.StateRunning},
		}
		if err := c.Create(context.Background(), running); err != nil {
			t.Fatalf("seed in-flight CR: %v", err)
		}
		res, err := r.reconcileMongoDB(context.Background(), job.DeepCopy(), resolved)
		if err != nil {
			t.Fatalf("reconcileMongoDB: %v", err)
		}
		if res.RequeueAfter == 0 {
			t.Fatalf("an in-flight system-bucket dump must keep polling, got %+v", res)
		}
		persisted := &backupsv1alpha1.BackupJob{}
		if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant", Name: job.Name}, persisted); err != nil {
			t.Fatalf("get job: %v", err)
		}
		if persisted.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
			t.Errorf("the opt-out refusal must not fail a dump already streaming (flow comes from the CR's finalizer)")
		}
		cond := apimeta.FindStatusCondition(persisted.Status.Conditions, "Ready")
		if cond == nil || cond.Reason != "PerconaServerMongoDBBackupRunning" {
			t.Errorf("expected the running wait, not the stale-storage refusal, got %+v", cond)
		}
		got := &psmdbtypes.PerconaServerMongoDBBackup{}
		if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant", Name: "op-inflight"}, got); err != nil || !got.DeletionTimestamp.IsZero() {
			t.Errorf("the in-flight CR must be left alone, err=%v deleted=%v", err, err == nil && !got.DeletionTimestamp.IsZero())
		}
	})

	// The injected entry is identified by its credentialsSecret, not by bucket
	// name: a legacy tenant whose own bucket happens to carry the platform's name
	// (external S3, admin-chosen name) must not be refused.
	t.Run("a legacy storage on a same-named bucket with its own Secret is not refused", func(t *testing.T) {
		job, strategy, app, cluster, resolved := mongodbInjectFixture(true)
		app.Spec.Backup.UseSystemBucket = false
		job.Status.StartedAt = &metav1.Time{Time: time.Now()}
		cluster.Spec.Backup.Storages = map[string]runtime.RawExtension{
			"s3-storage": {Raw: []byte(`{"type":"s3","s3":{"bucket":"cozy-backups","credentialsSecret":"mongodb-app1-s3-creds"}}`)},
		}
		c := newMongoDBStrategyTestClient(t, job, strategy, app, cluster)
		r := &BackupJobReconciler{Client: c, Scheme: c.Scheme(), Recorder: record.NewFakeRecorder(10)}
		if _, err := r.reconcileMongoDB(context.Background(), job.DeepCopy(), resolved); err != nil {
			t.Fatalf("reconcileMongoDB: %v", err)
		}
		if n := countCRs(t, c); n != 1 {
			t.Errorf("a legacy storage with the tenant's own Secret must mint normally, got %d CRs", n)
		}
	})
}

// A running dump polls through requeueMongoDBBackupWaiting for hours; the
// condition it writes is stable, so only the first reconcile may write status.
// Counts status updates on the BackupJob across two reconciles of the same
// fixture and requires exactly one.
func TestRequeueMongoDBBackupWaiting_WritesStatusOnlyOnChange(t *testing.T) {
	job, strategy, app, cluster, resolved := mongodbInjectFixture(true)
	job.Status.StartedAt = &metav1.Time{Time: time.Now().Add(-4 * psmdbDefaultBackupDeadline)}
	cluster.Spec.Backup.Storages = map[string]runtime.RawExtension{
		"s3-storage": {Raw: []byte(`{"type":"s3","s3":{"bucket":"cozy-backups"}}`)},
	}
	running := &psmdbtypes.PerconaServerMongoDBBackup{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "tenant", Name: "op-running",
			Finalizers: []string{psmdbDeleteBackupFinalizer},
			Labels: map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      job.Name,
				backupsv1alpha1.OwningJobNamespaceLabel: job.Namespace,
			},
		},
		Status: psmdbtypes.PerconaServerMongoDBBackupStatus{State: psmdbtypes.StateRunning},
	}
	sch := runtime.NewScheme()
	_ = scheme.AddToScheme(sch)
	_ = backupsv1alpha1.AddToScheme(sch)
	_ = strategyv1alpha1.AddToScheme(sch)
	_ = psmdbtypes.AddToScheme(sch)
	_ = mongodbapp.AddToScheme(sch)
	statusWrites := 0
	c := clientfake.NewClientBuilder().
		WithScheme(sch).
		WithObjects(job, strategy, app, cluster, running).
		WithStatusSubresource(&backupsv1alpha1.BackupJob{}).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, cl client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				if _, ok := obj.(*backupsv1alpha1.BackupJob); ok && sub == "status" {
					statusWrites++
				}
				return cl.SubResource(sub).Update(ctx, obj, opts...)
			},
		}).
		Build()
	r := &BackupJobReconciler{Client: c, Scheme: sch, Recorder: record.NewFakeRecorder(10)}

	for pass := 1; pass <= 2; pass++ {
		fresh := &backupsv1alpha1.BackupJob{}
		if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant", Name: job.Name}, fresh); err != nil {
			t.Fatalf("get job: %v", err)
		}
		if _, err := r.reconcileMongoDB(context.Background(), fresh, resolved); err != nil {
			t.Fatalf("reconcileMongoDB pass %d: %v", pass, err)
		}
	}
	// Pass 1 also flips Phase to Running (one write) and writes the condition;
	// pass 2 must add nothing.
	if statusWrites < 1 {
		t.Fatalf("expected the first reconcile to write status, got %d writes", statusWrites)
	}
	first := statusWrites
	fresh := &backupsv1alpha1.BackupJob{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant", Name: job.Name}, fresh); err != nil {
		t.Fatalf("get job: %v", err)
	}
	if _, err := r.reconcileMongoDB(context.Background(), fresh, resolved); err != nil {
		t.Fatalf("reconcileMongoDB pass 3: %v", err)
	}
	if statusWrites != first {
		t.Errorf("an unchanged running wait must not write status again: %d writes before, %d after", first, statusWrites)
	}
}

// The flow a backup ran on is fixed at mint time by its CR's finalizer, and the
// artifact must record that — not the app flag re-read on the completing
// reconcile. Flip the flag to false while the system-bucket dump completes: the
// snapshot must still say useSystemBucket, or the PITR refusal and the S3
// backfill both switch off for an archive that lives in cozy-backups.
func TestReconcileMongoDB_ArtifactRecordsTheCRsFlowNotTheFlag(t *testing.T) {
	job, strategy, app, cluster, resolved := mongodbInjectFixture(true)
	app.Spec.Backup.UseSystemBucket = false // flipped after the mint
	job.Status.StartedAt = &metav1.Time{Time: time.Now()}
	cluster.Spec.Backup.Storages = map[string]runtime.RawExtension{
		"s3-storage": {Raw: []byte(`{"type":"s3","s3":{"bucket":"cozy-backups","credentialsSecret":"cozy-backups-creds"}}`)},
	}
	ready := &psmdbtypes.PerconaServerMongoDBBackup{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "tenant", Name: "op-ready",
			Finalizers: []string{psmdbDeleteBackupFinalizer}, // minted on the system-bucket flow
			Labels: map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      job.Name,
				backupsv1alpha1.OwningJobNamespaceLabel: job.Namespace,
			},
		},
		Status: psmdbtypes.PerconaServerMongoDBBackupStatus{
			State:       psmdbtypes.StateReady,
			Destination: "s3://cozy-backups/tenant/app1/2026-09-20",
			S3:          &psmdbtypes.BackupStorageS3{Bucket: "cozy-backups", CredentialsSecret: psmdbDefaultCredentialsSecret},
		},
	}
	c := newMongoDBStrategyTestClient(t, job, strategy, app, cluster, ready)
	r := &BackupJobReconciler{Client: c, Scheme: c.Scheme(), Recorder: record.NewFakeRecorder(10)}
	if _, err := r.reconcileMongoDB(context.Background(), job.DeepCopy(), resolved); err != nil {
		t.Fatalf("reconcileMongoDB: %v", err)
	}
	persisted := &backupsv1alpha1.BackupJob{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant", Name: job.Name}, persisted); err != nil {
		t.Fatalf("get job: %v", err)
	}
	if persisted.Status.BackupRef == nil {
		t.Fatalf("expected a Backup artifact, got phase=%q conditions=%+v", persisted.Status.Phase, persisted.Status.Conditions)
	}
	artifact := &backupsv1alpha1.Backup{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant", Name: persisted.Status.BackupRef.Name}, artifact); err != nil {
		t.Fatalf("get artifact: %v", err)
	}
	snap, err := unmarshalMongoDBBackupSnapshot(artifact.Status.UnderlyingResources)
	if err != nil || snap == nil {
		t.Fatalf("snapshot: %v (%+v)", err, snap)
	}
	if !snap.UseSystemBucket {
		t.Errorf("the snapshot must record the flow the CR ran on (useSystemBucket), not the app flag re-read at completion")
	}
}

// On useSystemBucket the chart refuses backup.enabled=false, so a missing storage
// means the strategy carried nothing to inject; the wait must say that rather
// than send the tenant to a value they already have set.
func TestReconcileMongoDB_StrategyWithoutS3NamesTheCause(t *testing.T) {
	job, strategy, app, cluster, resolved := mongodbInjectFixture(true)
	strategy.Spec.Template.S3 = nil
	job.Status.StartedAt = &metav1.Time{Time: time.Now()}
	c := newMongoDBStrategyTestClient(t, job, strategy, app, cluster)
	r := &BackupJobReconciler{Client: c, Scheme: c.Scheme(), Recorder: record.NewFakeRecorder(10)}
	if _, err := r.reconcileMongoDB(context.Background(), job.DeepCopy(), resolved); err != nil {
		t.Fatalf("reconcileMongoDB: %v", err)
	}
	persisted := &backupsv1alpha1.BackupJob{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant", Name: job.Name}, persisted); err != nil {
		t.Fatalf("get job: %v", err)
	}
	cond := apimeta.FindStatusCondition(persisted.Status.Conditions, "Ready")
	if cond == nil || cond.Reason != "MongoDBStrategyHasNoS3" || !strings.Contains(cond.Message, "no s3 coordinates") {
		t.Errorf("expected Ready=False MongoDBStrategyHasNoS3 naming the missing coordinates, got %+v", cond)
	}
}

// The storage-race retry mints a fresh operator CR per attempt; it must stop
// after the budget instead of churning CRs every poll for the whole deadline,
// and the error then reaches the tenant.
func TestReconcileMongoDB_StorageRaceRetryBudgetExhausted(t *testing.T) {
	job, strategy, app, cluster, resolved := mongodbInjectFixture(true)
	job.Status.StartedAt = &metav1.Time{Time: time.Now()}
	cluster.Spec.Backup.Storages = map[string]runtime.RawExtension{
		"s3-storage": {Raw: []byte(`{"type":"s3","s3":{"bucket":"cozy-backups","credentialsSecret":"cozy-backups-creds"}}`)},
	}
	mk := func(name string) *psmdbtypes.PerconaServerMongoDBBackup {
		return &psmdbtypes.PerconaServerMongoDBBackup{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "tenant", Name: name, Finalizers: []string{psmdbDeleteBackupFinalizer},
				Labels: map[string]string{
					backupsv1alpha1.OwningJobNameLabel:      job.Name,
					backupsv1alpha1.OwningJobNamespaceLabel: job.Namespace,
				},
			},
			Status: psmdbtypes.PerconaServerMongoDBBackupStatus{State: psmdbtypes.StateError, Error: `unable to get storage "s3-storage": not found`},
		}
	}
	old1, old2, live := mk("op-try-1"), mk("op-try-2"), mk("op-try-3")
	c := newMongoDBStrategyTestClient(t, job, strategy, app, cluster, old1, old2, live)
	// Two earlier attempts already deleted (Terminating under the finalizer).
	for _, o := range []*psmdbtypes.PerconaServerMongoDBBackup{old1, old2} {
		if err := c.Delete(context.Background(), o); err != nil {
			t.Fatalf("delete %s: %v", o.Name, err)
		}
	}
	r := &BackupJobReconciler{Client: c, Scheme: c.Scheme(), Recorder: record.NewFakeRecorder(10)}
	if _, err := r.reconcileMongoDB(context.Background(), job.DeepCopy(), resolved); err != nil {
		t.Fatalf("reconcileMongoDB: %v", err)
	}
	persisted := &backupsv1alpha1.BackupJob{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant", Name: job.Name}, persisted); err != nil {
		t.Fatalf("get job: %v", err)
	}
	if persisted.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
		t.Fatalf("with the retry budget spent the job must fail, got phase=%q", persisted.Status.Phase)
	}
	got := &psmdbtypes.PerconaServerMongoDBBackup{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant", Name: "op-try-3"}, got); err != nil || !got.DeletionTimestamp.IsZero() {
		t.Errorf("the terminal path must not delete the live errored CR, err=%v", err)
	}
	list := &psmdbtypes.PerconaServerMongoDBBackupList{}
	if err := c.List(context.Background(), list, client.InNamespace("tenant")); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 3 {
		t.Errorf("no further CR may be minted once the budget is spent, got %d", len(list.Items))
	}
}
