// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"errors"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

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
	s3Storage := func(secret string) runtime.RawExtension {
		return runtime.RawExtension{Raw: []byte(fmt.Sprintf(`{"type":"s3","s3":{"credentialsSecret":%q}}`, secret))}
	}
	clusterWith := func(storages map[string]runtime.RawExtension) *psmdbtypes.PerconaServerMongoDB {
		return &psmdbtypes.PerconaServerMongoDB{
			Spec: psmdbtypes.PerconaServerMongoDBSpec{
				Backup: psmdbtypes.PerconaServerMongoDBBackupConfig{Storages: storages},
			},
		}
	}
	cases := []struct {
		name      string
		storages  map[string]runtime.RawExtension
		preferred string
		want      string
	}{
		{
			name:      "preferred storage wins",
			storages:  map[string]runtime.RawExtension{"s3-storage": s3Storage("target-creds"), "other": s3Storage("other-creds")},
			preferred: "s3-storage",
			want:      "target-creds",
		},
		{
			name:      "falls back to chart default when preferred absent",
			storages:  map[string]runtime.RawExtension{"s3-storage": s3Storage("target-creds")},
			preferred: "nonexistent",
			want:      "target-creds",
		},
		{
			name:     "sole non-default storage is used",
			storages: map[string]runtime.RawExtension{"custom": s3Storage("custom-creds")},
			want:     "custom-creds",
		},
		{
			name:     "ambiguous (multiple, none default, no preferred) yields empty",
			storages: map[string]runtime.RawExtension{"a": s3Storage("a-creds"), "b": s3Storage("b-creds")},
			want:     "",
		},
		{
			name:     "no storages yields empty",
			storages: nil,
			want:     "",
		},
		{
			name:     "non-s3 storage yields empty",
			storages: map[string]runtime.RawExtension{"s3-storage": {Raw: []byte(`{"type":"azure","azure":{"container":"c"}}`)}},
			want:     "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := psmdbTargetCredentialsSecret(clusterWith(tc.storages), tc.preferred); got != tc.want {
				t.Errorf("psmdbTargetCredentialsSecret: got %q want %q", got, tc.want)
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

	first, err := r.ensureMongoDBBackup(context.Background(), job, cluster, "s3-storage", rendered)
	if err != nil {
		t.Fatalf("first ensureMongoDBBackup: %v", err)
	}
	second, err := r.ensureMongoDBBackup(context.Background(), job, cluster, "s3-storage", rendered)
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
	raw, err := marshalMongoDBBackupSnapshot(mdbBackup, rendered, "s3-storage", map[string]string{"k": "v"})
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
	raw, err := marshalMongoDBBackupSnapshot(snapBackup, &strategyv1alpha1.MongoDBTemplate{Type: "logical"}, "s3-storage", nil)
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

		artefact, err := r.createMongoDBBackupArtifact(context.Background(), job, resolved, mdbBackup, rendered, "s3-storage")
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

		artefact, err := r.createMongoDBBackupArtifact(context.Background(), job, resolved, mdbBackup, rendered, "s3-storage")
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
