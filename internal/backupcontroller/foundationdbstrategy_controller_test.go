// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	"github.com/cozystack/cozystack/internal/backupcontroller/foundationdbapp"
	"github.com/cozystack/cozystack/internal/backupcontroller/foundationdbtypes"
)

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

func TestValidateFoundationDBApplicationRef(t *testing.T) {
	apps := foundationdbapp.GroupName
	other := "other.example.com"
	cases := []struct {
		name    string
		ref     corev1.TypedLocalObjectReference
		wantErr bool
	}{
		{"happy path with apps group", corev1.TypedLocalObjectReference{Kind: "FoundationDB", Name: "x", APIGroup: &apps}, false},
		{"empty apiGroup is accepted", corev1.TypedLocalObjectReference{Kind: "FoundationDB", Name: "x"}, false},
		{"foreign apiGroup rejected", corev1.TypedLocalObjectReference{Kind: "FoundationDB", Name: "x", APIGroup: &other}, true},
		{"wrong kind rejected", corev1.TypedLocalObjectReference{Kind: "Postgres", Name: "x", APIGroup: &apps}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateFoundationDBApplicationRef(tc.ref)
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
// Cluster-name mapping (foundationdb release.prefix)
// ---------------------------------------------------------------------------

func TestFoundationDBClusterNameForApp(t *testing.T) {
	// The cozystack foundationdb ApplicationDefinition prefixes the
	// release name with "foundationdb-", so an app named "fdb-src"
	// renders apps.foundationdb.org/FoundationDBCluster
	// metadata.name=foundationdb-fdb-src.
	if got := foundationdbClusterNameForApp("fdb-src"); got != "foundationdb-fdb-src" {
		t.Errorf("foundationdbClusterNameForApp: got %q want foundationdb-fdb-src", got)
	}
}

// ---------------------------------------------------------------------------
// Templating
// ---------------------------------------------------------------------------

func TestRenderFoundationDBTemplate_TemplatingApplicationName(t *testing.T) {
	tmpl := strategyv1alpha1.FoundationDBTemplate{
		BlobStoreConfiguration: strategyv1alpha1.FoundationDBBlobStoreTemplate{
			AccountName:   "key@s3.example:9000",
			Bucket:        "{{ .Parameters.bucket }}",
			BackupName:    "{{ .Application.metadata.name }}-fdb",
			URLParameters: []string{"region={{ .Parameters.region }}"},
		},
		CustomParameters: []string{"--blob_credentials=/var/{{ .Application.metadata.name }}/creds"},
	}
	app := newFoundationDBApp("fdb-src", "tenant")
	got, err := renderFoundationDBTemplate(tmpl, app, map[string]string{"bucket": "shared-bucket", "region": "us-east-1"})
	if err != nil {
		t.Fatalf("renderFoundationDBTemplate: %v", err)
	}
	if got.BlobStoreConfiguration.Bucket != "shared-bucket" {
		t.Errorf("Bucket parameter not templated: got %q", got.BlobStoreConfiguration.Bucket)
	}
	if got.BlobStoreConfiguration.BackupName != "fdb-src-fdb" {
		t.Errorf("BackupName not templated: got %q", got.BlobStoreConfiguration.BackupName)
	}
	if len(got.BlobStoreConfiguration.URLParameters) != 1 || got.BlobStoreConfiguration.URLParameters[0] != "region=us-east-1" {
		t.Errorf("URLParameters not templated: got %#v", got.BlobStoreConfiguration.URLParameters)
	}
	if len(got.CustomParameters) != 1 || got.CustomParameters[0] != "--blob_credentials=/var/fdb-src/creds" {
		t.Errorf("CustomParameters not templated: got %#v", got.CustomParameters)
	}
}

// ---------------------------------------------------------------------------
// Backup-side ensure idempotency
// ---------------------------------------------------------------------------

func TestEnsureFoundationDBBackup_IdempotentByLabel(t *testing.T) {
	c := newFoundationDBStrategyTestClient(t)
	r := &BackupJobReconciler{Client: c, Scheme: c.Scheme()}
	job := newFoundationDBBackupJob("bj-1", "tenant")
	if err := c.Create(context.Background(), job); err != nil {
		t.Fatalf("seed BackupJob: %v", err)
	}
	rendered := newRenderedFoundationDBTemplate()
	clusterName := foundationdbClusterNameForApp("fdb-src")

	first, err := r.ensureFoundationDBBackup(context.Background(), job, clusterName, "7.3.63", rendered)
	if err != nil {
		t.Fatalf("first ensureFoundationDBBackup: %v", err)
	}
	second, err := r.ensureFoundationDBBackup(context.Background(), job, clusterName, "7.3.63", rendered)
	if err != nil {
		t.Fatalf("second ensureFoundationDBBackup: %v", err)
	}
	if first.Name != second.Name {
		t.Errorf("expected idempotent reuse: first=%q second=%q", first.Name, second.Name)
	}

	list := &foundationdbtypes.FoundationDBBackupList{}
	if err := c.List(context.Background(), list, client.InNamespace("tenant")); err != nil {
		t.Fatalf("list backups: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("expected exactly one operator FoundationDBBackup CR, got %d", len(list.Items))
	}
	got := &list.Items[0]
	if got.Spec.ClusterName != "foundationdb-fdb-src" {
		t.Errorf("ClusterName: got %q want foundationdb-fdb-src", got.Spec.ClusterName)
	}
	if got.Spec.Version != "7.3.63" {
		// Operator OpenAPI requires spec.version; the driver injects it
		// from the source FoundationDBCluster.spec.version.
		t.Errorf("Version: got %q want 7.3.63", got.Spec.Version)
	}
	if got.Spec.BackupState != foundationdbtypes.BackupStateRunning {
		t.Errorf("BackupState: got %q want %q", got.Spec.BackupState, foundationdbtypes.BackupStateRunning)
	}
	if got.Spec.BlobStoreConfiguration.BackupName != "bj-1" {
		// Template left BackupName empty, ensure path must default it to
		// the BackupJob name so each Cozystack BackupJob owns a discrete
		// blob-store directory.
		t.Errorf("BlobStoreConfiguration.BackupName: got %q want bj-1 (BackupJob name fallback)", got.Spec.BlobStoreConfiguration.BackupName)
	}
	if got.Labels[backupsv1alpha1.OwningJobNameLabel] != "bj-1" {
		t.Errorf("OwningJobName label missing or wrong: %v", got.Labels)
	}
	if got.Labels[foundationdbClusterLabel] != "foundationdb-fdb-src" {
		t.Errorf("foundationdbClusterLabel missing or wrong: %v", got.Labels)
	}
}

// TestEnsureFoundationDBBackup_StopsPriorRunning pins the FDB-specific
// invariant: the operator only permits one running backup directory per
// cluster, so an earlier BackupJob's CR has to flip backupState=Stopped
// before the new one can start. Without this gate the operator would
// reject the new FoundationDBBackup or both agents would race on the
// same blob-store path.
func TestEnsureFoundationDBBackup_StopsPriorRunning(t *testing.T) {
	clusterName := foundationdbClusterNameForApp("fdb-src")
	prior := &foundationdbtypes.FoundationDBBackup{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "tenant", Name: "bj-0-aaa",
			Labels: map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      "bj-0",
				backupsv1alpha1.OwningJobNamespaceLabel: "tenant",
				foundationdbClusterLabel:                clusterName,
			},
		},
		Spec: foundationdbtypes.FoundationDBBackupSpec{
			ClusterName: clusterName,
			BackupState: foundationdbtypes.BackupStateRunning,
		},
	}
	c := newFoundationDBStrategyTestClient(t, prior)
	r := &BackupJobReconciler{Client: c, Scheme: c.Scheme()}
	job := newFoundationDBBackupJob("bj-1", "tenant")
	rendered := newRenderedFoundationDBTemplate()

	if _, err := r.ensureFoundationDBBackup(context.Background(), job, clusterName, "7.3.63", rendered); err != nil {
		t.Fatalf("ensureFoundationDBBackup: %v", err)
	}

	got := &foundationdbtypes.FoundationDBBackup{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "tenant", Name: prior.Name}, got); err != nil {
		t.Fatalf("get prior CR: %v", err)
	}
	if got.Spec.BackupState != foundationdbtypes.BackupStateStopped {
		t.Errorf("prior FoundationDBBackup must be flipped to Stopped; got %q", got.Spec.BackupState)
	}
}

// ---------------------------------------------------------------------------
// Restore-side ensure idempotency + target rewrite
// ---------------------------------------------------------------------------

func TestEnsureFoundationDBRestore_IdempotentByLabel(t *testing.T) {
	c := newFoundationDBStrategyTestClient(t)
	r := &RestoreJobReconciler{Client: c, Scheme: c.Scheme()}
	rj := newFoundationDBRestoreJob("rj-1", "tenant")
	if err := c.Create(context.Background(), rj); err != nil {
		t.Fatalf("seed RestoreJob: %v", err)
	}
	blob := foundationdbtypes.BlobStoreConfiguration{
		AccountName: "key@s3.example:9000",
		Bucket:      "bkt",
		BackupName:  "bj-1",
	}

	first, err := r.ensureFoundationDBRestore(context.Background(), rj, "foundationdb-fdb-target", blob, nil, nil)
	if err != nil {
		t.Fatalf("first ensureFoundationDBRestore: %v", err)
	}
	second, err := r.ensureFoundationDBRestore(context.Background(), rj, "foundationdb-fdb-target", blob, nil, nil)
	if err != nil {
		t.Fatalf("second ensureFoundationDBRestore: %v", err)
	}
	if first.Name != second.Name {
		t.Errorf("expected idempotent reuse: first=%q second=%q", first.Name, second.Name)
	}
	list := &foundationdbtypes.FoundationDBRestoreList{}
	if err := c.List(context.Background(), list, client.InNamespace("tenant")); err != nil {
		t.Fatalf("list restores: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("expected exactly one operator FoundationDBRestore CR, got %d", len(list.Items))
	}
	got := &list.Items[0]
	if got.Spec.DestinationClusterName != "foundationdb-fdb-target" {
		t.Errorf("DestinationClusterName: got %q want foundationdb-fdb-target", got.Spec.DestinationClusterName)
	}
	if !apiequality.Semantic.DeepEqual(got.Spec.BlobStoreConfiguration, blob) {
		t.Errorf("BlobStoreConfiguration: got %#v want %#v", got.Spec.BlobStoreConfiguration, blob)
	}
}

// ---------------------------------------------------------------------------
// resolveFoundationDBRestoreTarget (in-place vs to-copy)
// ---------------------------------------------------------------------------

func TestResolveFoundationDBRestoreTarget(t *testing.T) {
	apps := foundationdbapp.GroupName
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "src-bk"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{
				Kind: "FoundationDB", Name: "fdb-src", APIGroup: &apps,
			},
		},
	}

	t.Run("in-place: missing targetApplicationRef inherits source", func(t *testing.T) {
		rj := &backupsv1alpha1.RestoreJob{Spec: backupsv1alpha1.RestoreJobSpec{}}
		r := &RestoreJobReconciler{}
		got := r.resolveFoundationDBRestoreTarget(rj, backup)
		if got.AppName != "fdb-src" {
			t.Errorf("in-place AppName: got %q want fdb-src", got.AppName)
		}
		if got.Kind != "FoundationDB" {
			t.Errorf("in-place Kind: got %q want FoundationDB", got.Kind)
		}
	})

	t.Run("to-copy: targetApplicationRef wins over source", func(t *testing.T) {
		rj := &backupsv1alpha1.RestoreJob{
			Spec: backupsv1alpha1.RestoreJobSpec{
				TargetApplicationRef: &corev1.TypedLocalObjectReference{
					Kind: "FoundationDB", Name: "fdb-target", APIGroup: &apps,
				},
			},
		}
		r := &RestoreJobReconciler{}
		got := r.resolveFoundationDBRestoreTarget(rj, backup)
		if got.AppName != "fdb-target" {
			t.Errorf("to-copy AppName: got %q want fdb-target", got.AppName)
		}
	})
}

// ---------------------------------------------------------------------------
// Snapshot persistence round-trip
// ---------------------------------------------------------------------------

func TestMarshalDecodeFoundationDBBackupSnapshot_RoundTrip(t *testing.T) {
	rendered := newRenderedFoundationDBTemplate()
	parameters := map[string]string{"region": "us-east-1"}
	fdbBackup := &foundationdbtypes.FoundationDBBackup{
		Spec: foundationdbtypes.FoundationDBBackupSpec{
			BlobStoreConfiguration: foundationdbtypes.BlobStoreConfiguration{
				BackupName: "bj-1",
			},
		},
	}
	raw, err := marshalFoundationDBBackupSnapshot(newFoundationDBApp("fdb-src", "tenant"), rendered, parameters, fdbBackup)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if raw == nil || len(raw.Raw) == 0 {
		t.Fatalf("expected non-empty RawExtension")
	}
	var snap foundationdbBackupSnapshot
	if err := json.Unmarshal(raw.Raw, &snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if snap.Kind != foundationdbBackupSnapshotKind {
		t.Errorf("snapshot kind: got %q want %q", snap.Kind, foundationdbBackupSnapshotKind)
	}
	// Template left BackupName empty; marshal must backfill it from the
	// per-run FoundationDBBackup so restore-time reuse picks the right
	// blob-store path.
	if snap.Storage.BackupName != "bj-1" {
		t.Errorf("snapshot Storage.BackupName: got %q want bj-1 (backfilled from FoundationDBBackup)", snap.Storage.BackupName)
	}
	if got := snap.Parameters["region"]; got != "us-east-1" {
		t.Errorf("snapshot parameters: got %#v", snap.Parameters)
	}

	decoded, err := decodeFoundationDBBackupSnapshot(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded == nil || decoded.Kind != foundationdbBackupSnapshotKind {
		t.Errorf("decoded snapshot mismatch: %#v", decoded)
	}
}

// ---------------------------------------------------------------------------
// resolveFoundationDBRestoreBlob: snapshot vs driverMetadata fallback
// ---------------------------------------------------------------------------

func TestResolveFoundationDBRestoreBlob_SnapshotWinsOverMetadata(t *testing.T) {
	snap := &foundationdbBackupSnapshot{
		Storage: strategyv1alpha1.FoundationDBBlobStoreTemplate{
			AccountName: "from-snap",
			Bucket:      "snap-bucket",
			BackupName:  "snap-name",
		},
	}
	backup := &backupsv1alpha1.Backup{
		Spec: backupsv1alpha1.BackupSpec{
			DriverMetadata: map[string]string{
				foundationdbAccountNameKey:    "from-md",
				foundationdbBucketKey:         "md-bucket",
				foundationdbBlobBackupNameKey: "md-name",
			},
		},
	}
	got, ok := resolveFoundationDBRestoreBlob(backup, snap)
	if !ok {
		t.Fatalf("expected ok=true with snapshot")
	}
	if got.AccountName != "from-snap" || got.Bucket != "snap-bucket" || got.BackupName != "snap-name" {
		t.Errorf("snapshot must win over driverMetadata; got %#v", got)
	}
}

func TestResolveFoundationDBRestoreBlob_MetadataFallback(t *testing.T) {
	backup := &backupsv1alpha1.Backup{
		Spec: backupsv1alpha1.BackupSpec{
			DriverMetadata: map[string]string{
				foundationdbAccountNameKey:    "from-md",
				foundationdbBucketKey:         "md-bucket",
				foundationdbBlobBackupNameKey: "md-name",
			},
		},
	}
	got, ok := resolveFoundationDBRestoreBlob(backup, nil)
	if !ok {
		t.Fatalf("expected ok=true with driverMetadata fallback")
	}
	if got.AccountName != "from-md" || got.Bucket != "md-bucket" || got.BackupName != "md-name" {
		t.Errorf("driverMetadata fallback: got %#v", got)
	}
}

func TestResolveFoundationDBRestoreBlob_EmptyReturnsFalse(t *testing.T) {
	backup := &backupsv1alpha1.Backup{Spec: backupsv1alpha1.BackupSpec{}}
	_, ok := resolveFoundationDBRestoreBlob(backup, nil)
	if ok {
		t.Fatalf("empty backup must return ok=false")
	}
}

// ---------------------------------------------------------------------------
// Backup readiness
// ---------------------------------------------------------------------------

func TestFoundationDBBackupReady(t *testing.T) {
	t.Run("nil backup is not ready", func(t *testing.T) {
		if foundationdbBackupReady(nil) {
			t.Fatalf("nil backup must not be ready")
		}
	})
	t.Run("missing backupDetails is not ready", func(t *testing.T) {
		if foundationdbBackupReady(&foundationdbtypes.FoundationDBBackup{}) {
			t.Fatalf("backup without details must not be ready")
		}
	})
	t.Run("not running is not ready", func(t *testing.T) {
		b := &foundationdbtypes.FoundationDBBackup{Status: foundationdbtypes.FoundationDBBackupStatus{
			BackupDetails: &foundationdbtypes.BackupDetails{Running: false, SnapshotTime: 100},
		}}
		if foundationdbBackupReady(b) {
			t.Fatalf("running=false must not be ready")
		}
	})
	t.Run("snapshotTime=0 is not ready", func(t *testing.T) {
		b := &foundationdbtypes.FoundationDBBackup{Status: foundationdbtypes.FoundationDBBackupStatus{
			BackupDetails: &foundationdbtypes.BackupDetails{Running: true, SnapshotTime: 0},
		}}
		if foundationdbBackupReady(b) {
			t.Fatalf("snapshotTime=0 must not be ready")
		}
	})
	t.Run("generations not reconciled is not ready", func(t *testing.T) {
		b := &foundationdbtypes.FoundationDBBackup{
			ObjectMeta: metav1.ObjectMeta{Generation: 2},
			Status: foundationdbtypes.FoundationDBBackupStatus{
				BackupDetails: &foundationdbtypes.BackupDetails{Running: true, SnapshotTime: 100},
				Generations:   &foundationdbtypes.BackupGenerationStatus{Reconciled: 1},
			},
		}
		if foundationdbBackupReady(b) {
			t.Fatalf("generations.reconciled < generation must not be ready")
		}
	})
	t.Run("ready when running + snapshotTime > 0 + reconciled", func(t *testing.T) {
		b := &foundationdbtypes.FoundationDBBackup{
			ObjectMeta: metav1.ObjectMeta{Generation: 1},
			Status: foundationdbtypes.FoundationDBBackupStatus{
				BackupDetails: &foundationdbtypes.BackupDetails{Running: true, SnapshotTime: 100},
				Generations:   &foundationdbtypes.BackupGenerationStatus{Reconciled: 1},
			},
		}
		if !foundationdbBackupReady(b) {
			t.Fatalf("expected ready=true")
		}
	})
}

// ---------------------------------------------------------------------------
// Restore options + deadline
// ---------------------------------------------------------------------------

func TestParseFoundationDBRestoreOptions(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    int64
		wantErr bool
	}{
		{"nil blob", "", 0, false},
		{"missing fields", `{"foo":"bar"}`, 0, false},
		{"override", `{"restoreTimeoutSeconds":7200}`, 7200, false},
		{"malformed", `not-json`, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ext *runtime.RawExtension
			if tc.raw != "" {
				ext = &runtime.RawExtension{Raw: []byte(tc.raw)}
			}
			got, err := parseFoundationDBRestoreOptions(ext)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected parse error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected parse error: %v", err)
			}
			if got.RestoreTimeoutSeconds != tc.want {
				t.Errorf("RestoreTimeoutSeconds: got %d want %d", got.RestoreTimeoutSeconds, tc.want)
			}
		})
	}
}

func TestFoundationDBRestoreOptions_EffectiveDeadline(t *testing.T) {
	cases := []struct {
		name string
		opts FoundationDBRestoreOptions
		want time.Duration
	}{
		{"unset", FoundationDBRestoreOptions{}, foundationdbDefaultRestoreDeadline},
		{"zero", FoundationDBRestoreOptions{RestoreTimeoutSeconds: 0}, foundationdbDefaultRestoreDeadline},
		{"negative", FoundationDBRestoreOptions{RestoreTimeoutSeconds: -1}, foundationdbDefaultRestoreDeadline},
		{"override", FoundationDBRestoreOptions{RestoreTimeoutSeconds: 7200}, 2 * time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.opts.effectiveRestoreDeadline(); got != tc.want {
				t.Errorf("got %s want %s", got, tc.want)
			}
		})
	}
}

func TestFoundationDBBackupDeadlineExceeded(t *testing.T) {
	if foundationdbBackupDeadlineExceeded(nil) {
		t.Fatalf("nil StartedAt must not trip the gate (deadline starts when StartedAt is set)")
	}
	recent := metav1.NewTime(time.Now())
	if foundationdbBackupDeadlineExceeded(&recent) {
		t.Fatalf("recent StartedAt must not trip the gate")
	}
	old := metav1.NewTime(time.Now().Add(-2 * foundationdbDefaultBackupDeadline))
	if !foundationdbBackupDeadlineExceeded(&old) {
		t.Fatalf("StartedAt past default deadline must trip the gate")
	}
}

// ---------------------------------------------------------------------------
// URI synthesis
// ---------------------------------------------------------------------------

func TestFoundationDBBackupURI_PrefersOperatorReportedURL(t *testing.T) {
	fdb := &foundationdbtypes.FoundationDBBackup{
		Spec: foundationdbtypes.FoundationDBBackupSpec{
			BlobStoreConfiguration: foundationdbtypes.BlobStoreConfiguration{
				AccountName: "key@s3.example:9000",
				Bucket:      "bkt",
				BackupName:  "bj-1",
			},
		},
		Status: foundationdbtypes.FoundationDBBackupStatus{
			BackupDetails: &foundationdbtypes.BackupDetails{URL: "blobstore://bkt/bj-1?secure_connection=0"},
		},
	}
	if got, want := foundationdbBackupURI(fdb), "blobstore://bkt/bj-1?secure_connection=0"; got != want {
		t.Errorf("URI: got %q want %q", got, want)
	}
}

func TestFoundationDBBackupURI_FallsBackToSpec(t *testing.T) {
	fdb := &foundationdbtypes.FoundationDBBackup{
		Spec: foundationdbtypes.FoundationDBBackupSpec{
			BlobStoreConfiguration: foundationdbtypes.BlobStoreConfiguration{
				AccountName: "key@s3.example:9000",
				Bucket:      "bkt",
				BackupName:  "bj-1",
			},
		},
	}
	if got, want := foundationdbBackupURI(fdb), "blobstore://bkt/bj-1"; got != want {
		t.Errorf("URI: got %q want %q", got, want)
	}
}

func TestFoundationDBBackupURI_EmptyIsEmpty(t *testing.T) {
	if got := foundationdbBackupURI(nil); got != "" {
		t.Errorf("nil URI: got %q want \"\"", got)
	}
	if got := foundationdbBackupURI(&foundationdbtypes.FoundationDBBackup{}); got != "" {
		t.Errorf("empty URI: got %q want \"\"", got)
	}
}

// TestFoundationDBBackupStateConstants_OnlyReadableValues pins #12 from
// review: the driver writes spec.backupState to either "Running" (start a
// backup) or "Stopped" (close a prior backup directory). The operator's
// CRD enum also defines "Paused", but no code path in this driver reads
// or writes it - so foundationdbtypes.BackupStatePaused was removed to
// avoid an importable dead identifier that a future reader could misuse.
// This test pins the surviving constants' values so a refactor that
// renames them (or quietly reintroduces a dead "Paused" constant) has to
// update the assertion as well.
func TestFoundationDBBackupStateConstants_OnlyReadableValues(t *testing.T) {
	if foundationdbtypes.BackupStateRunning != "Running" {
		t.Errorf("BackupStateRunning: got %q want \"Running\"", foundationdbtypes.BackupStateRunning)
	}
	if foundationdbtypes.BackupStateStopped != "Stopped" {
		t.Errorf("BackupStateStopped: got %q want \"Stopped\"", foundationdbtypes.BackupStateStopped)
	}
}

// TestFoundationDBBackupURI_EmptyBucketIsEmpty pins #10 from review: when
// bucket is unset the helper must NOT fall back to AccountName as the
// bucket-slot value. AccountName looks like "<api_key>@<host:port>", so
// the resulting URL "blobstore://<api_key>@<host>/<backupName>" matches
// nothing the operator actually writes and would mislead anyone trying
// to locate the artefact in object storage. The helper is informational
// only (load-bearing coordinates live on Backup.status.driverMetadata),
// so the right answer is "".
func TestFoundationDBBackupURI_EmptyBucketIsEmpty(t *testing.T) {
	fdb := &foundationdbtypes.FoundationDBBackup{
		Spec: foundationdbtypes.FoundationDBBackupSpec{
			BlobStoreConfiguration: foundationdbtypes.BlobStoreConfiguration{
				AccountName: "key@s3.example:9000",
				BackupName:  "bj-1",
				// Bucket intentionally empty.
			},
		},
	}
	if got := foundationdbBackupURI(fdb); got != "" {
		t.Errorf("empty bucket must produce empty URI; got %q (must not graft AccountName into the bucket slot)", got)
	}
}

// ---------------------------------------------------------------------------
// Restore reconcile: target cluster transient + kind validation
// ---------------------------------------------------------------------------

func TestReconcileFoundationDBRestore_TargetClusterNotFoundIsTransient(t *testing.T) {
	apps := foundationdbapp.GroupName
	now := metav1.Now()
	cozyBackup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "src-cozy-bk"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{
				Kind: "FoundationDB", Name: "fdb-src", APIGroup: &apps,
			},
			DriverMetadata: map[string]string{
				foundationdbAccountNameKey:    "key@s3.example:9000",
				foundationdbBucketKey:         "bkt",
				foundationdbBlobBackupNameKey: "src-op-bk",
			},
		},
	}
	rj := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "rj-target-missing"},
		Spec: backupsv1alpha1.RestoreJobSpec{
			BackupRef: corev1.LocalObjectReference{Name: cozyBackup.Name},
			TargetApplicationRef: &corev1.TypedLocalObjectReference{
				Kind: "FoundationDB", Name: "target", APIGroup: &apps,
			},
		},
		Status: backupsv1alpha1.RestoreJobStatus{
			StartedAt: &now,
		},
	}
	c := newFoundationDBStrategyTestClient(t, cozyBackup, rj)
	r := &RestoreJobReconciler{Client: c, Recorder: record.NewFakeRecorder(10)}

	res, err := r.reconcileFoundationDBRestore(context.Background(), rj, cozyBackup)
	if err != nil {
		t.Fatalf("reconcileFoundationDBRestore: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("missing target cluster must produce transient requeue, got %+v", res)
	}
	if rj.Status.Phase == backupsv1alpha1.RestoreJobPhaseFailed {
		t.Errorf("missing target cluster must NOT mark RestoreJob Failed; got phase=%q", rj.Status.Phase)
	}
	cond := apimeta.FindStatusCondition(rj.Status.Conditions, "Ready")
	if cond == nil {
		t.Fatalf("expected Ready condition, got none")
	}
	if cond.Status != metav1.ConditionFalse || cond.Reason != "TargetFoundationDBClusterNotReady" {
		t.Errorf("expected Ready=False/TargetFoundationDBClusterNotReady, got %s/%s", cond.Status, cond.Reason)
	}
}

func TestReconcileFoundationDBRestore_KindCheckedBeforeBlobLookup(t *testing.T) {
	apps := foundationdbapp.GroupName
	now := metav1.Now()
	cozyBackup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "src-cozy-bk"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{
				Kind: "FoundationDB", Name: "fdb-src", APIGroup: &apps,
			},
			DriverMetadata: map[string]string{
				foundationdbAccountNameKey:    "key@s3.example:9000",
				foundationdbBucketKey:         "bkt",
				foundationdbBlobBackupNameKey: "src-op-bk",
			},
		},
	}
	rj := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "rj-bad-kind"},
		Spec: backupsv1alpha1.RestoreJobSpec{
			BackupRef: corev1.LocalObjectReference{Name: cozyBackup.Name},
			TargetApplicationRef: &corev1.TypedLocalObjectReference{
				Kind: "Postgres", Name: "wrong", APIGroup: &apps,
			},
		},
		Status: backupsv1alpha1.RestoreJobStatus{StartedAt: &now},
	}
	c := newFoundationDBStrategyTestClient(t, cozyBackup, rj)
	r := &RestoreJobReconciler{Client: c, Recorder: record.NewFakeRecorder(10)}

	if _, err := r.reconcileFoundationDBRestore(context.Background(), rj, cozyBackup); err != nil {
		t.Fatalf("reconcileFoundationDBRestore: %v", err)
	}
	if rj.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
		t.Fatalf("expected RestoreJob to be Failed on bad TargetApplicationRef.Kind, got phase=%q", rj.Status.Phase)
	}
	cond := apimeta.FindStatusCondition(rj.Status.Conditions, "Ready")
	if cond == nil {
		t.Fatalf("expected Ready condition, got none")
	}
	if !strings.Contains(cond.Message, "applicationRef.kind") {
		t.Errorf("expected Kind-rejection message, got %q", cond.Message)
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func newFoundationDBStrategyTestClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	_ = backupsv1alpha1.AddToScheme(s)
	_ = strategyv1alpha1.AddToScheme(s)
	_ = foundationdbtypes.AddToScheme(s)
	_ = foundationdbapp.AddToScheme(s)
	return clientfake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		WithStatusSubresource(&backupsv1alpha1.BackupJob{}, &backupsv1alpha1.RestoreJob{}, &backupsv1alpha1.Backup{}).
		Build()
}

func newFoundationDBApp(name, namespace string) *foundationdbapp.FoundationDB {
	return &foundationdbapp.FoundationDB{
		TypeMeta: metav1.TypeMeta{
			APIVersion: foundationdbapp.GroupVersion.String(),
			Kind:       foundationdbapp.Kind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
}

func newFoundationDBBackupJob(name, namespace string) *backupsv1alpha1.BackupJob {
	apps := foundationdbapp.GroupName
	return &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{
				Kind: "FoundationDB", Name: "fdb-src", APIGroup: &apps,
			},
		},
	}
}

func newFoundationDBRestoreJob(name, namespace string) *backupsv1alpha1.RestoreJob {
	return &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: backupsv1alpha1.RestoreJobSpec{
			BackupRef: corev1.LocalObjectReference{Name: "src-backup"},
		},
	}
}

func newRenderedFoundationDBTemplate() *strategyv1alpha1.FoundationDBTemplate {
	return &strategyv1alpha1.FoundationDBTemplate{
		BlobStoreConfiguration: strategyv1alpha1.FoundationDBBlobStoreTemplate{
			AccountName: "key@s3.example:9000",
			Bucket:      "bkt",
		},
		CustomParameters: []string{"--blob_credentials=/var/fdb-blob-credentials/credentials"},
	}
}

// ---------------------------------------------------------------------------
// Cluster gates on the BackupJob reconcile path
// ---------------------------------------------------------------------------

// reconcileFoundationDBSeed assembles the minimum object graph
// reconcileFoundationDB walks before reaching the FoundationDBBackup
// ensure path: the strategy CR (cluster-scoped), the source application
// (apps.cozystack.io/FoundationDB), and the operator-side
// FoundationDBCluster the test wants to vary. Returns the seeded BackupJob
// (StartedAt prefilled to skip the bootstrap branch) and the
// ResolvedBackupConfig the dispatcher would have produced.
func reconcileFoundationDBSeed(
	t *testing.T,
	clusterSpec foundationdbtypes.FoundationDBClusterSpec,
	clusterStatus foundationdbtypes.FoundationDBClusterStatus,
) (client.Client, *backupsv1alpha1.BackupJob, *ResolvedBackupConfig) {
	t.Helper()
	apps := foundationdbapp.GroupName
	strategyAPIGroup := strategyv1alpha1.GroupVersion.Group

	strategy := &strategyv1alpha1.FoundationDB{
		ObjectMeta: metav1.ObjectMeta{Name: "fdb-strategy"},
		Spec:       strategyv1alpha1.FoundationDBSpec{Template: *newRenderedFoundationDBTemplate()},
	}
	app := newFoundationDBApp("fdb-src", "tenant")
	cluster := &foundationdbtypes.FoundationDBCluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "tenant",
			Name:      foundationdbClusterNameForApp("fdb-src"),
		},
		Spec:   clusterSpec,
		Status: clusterStatus,
	}
	now := metav1.NewTime(time.Now().Add(-1 * time.Minute))
	job := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{
				Kind: "FoundationDB", Name: "fdb-src", APIGroup: &apps,
			},
		},
		// Pre-fill StartedAt so reconcileFoundationDB skips the
		// stale-cache bootstrap requeue and reaches the cluster gate in
		// the same call. Without this, the cluster gate test would
		// return on the bootstrap branch and the assertion would fire
		// against the wrong code path.
		Status: backupsv1alpha1.BackupJobStatus{StartedAt: &now},
	}

	c := newFoundationDBStrategyTestClient(t, strategy, app, cluster, job)
	resolved := &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{
			APIGroup: &strategyAPIGroup,
			Kind:     strategyv1alpha1.FoundationDBStrategyKind,
			Name:     strategy.Name,
		},
		Parameters: map[string]string{},
	}
	// Re-fetch the BackupJob through the client so the in-test handle
	// carries the resourceVersion the fake client assigned at seed time;
	// otherwise a subsequent Status().Update from the reconcile path sees
	// a stale RV and returns 409.
	fresh := &backupsv1alpha1.BackupJob{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(job), fresh); err != nil {
		t.Fatalf("seed Get BackupJob: %v", err)
	}
	return c, fresh, resolved
}

// TestReconcileFoundationDB_VersionEmptyIsTransient pins the gate
// introduced when the operator's OpenAPI schema started requiring
// FoundationDBBackup.spec.version. Before the gate the driver would
// submit a CR with an empty spec.version, the apiserver would reject it,
// and the BackupJob would terminally Fail until the cluster caught up.
// The fix requeues with Ready=False/FoundationDBClusterVersionUnknown
// instead. This test fails without the gate.
func TestReconcileFoundationDB_VersionEmptyIsTransient(t *testing.T) {
	c, job, resolved := reconcileFoundationDBSeed(
		t,
		foundationdbtypes.FoundationDBClusterSpec{Version: ""},
		foundationdbtypes.FoundationDBClusterStatus{
			Health: foundationdbtypes.FoundationDBClusterHealth{Available: true},
		},
	)
	r := &BackupJobReconciler{Client: c, Scheme: c.Scheme(), Recorder: record.NewFakeRecorder(10)}

	res, err := r.reconcileFoundationDB(context.Background(), job, resolved)
	if err != nil {
		t.Fatalf("reconcileFoundationDB: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("empty cluster.spec.version must produce transient requeue, got %+v", res)
	}
	if job.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
		t.Errorf("empty cluster.spec.version must NOT mark BackupJob Failed; got phase=%q", job.Status.Phase)
	}
	cond := apimeta.FindStatusCondition(job.Status.Conditions, "Ready")
	if cond == nil {
		t.Fatalf("expected Ready condition, got none")
	}
	if cond.Status != metav1.ConditionFalse || cond.Reason != "FoundationDBClusterVersionUnknown" {
		t.Errorf("expected Ready=False/FoundationDBClusterVersionUnknown, got %s/%s", cond.Status, cond.Reason)
	}
	// The driver must NOT have materialised a FoundationDBBackup with
	// an empty version - that would tickle the apiserver-side OpenAPI
	// rejection that the gate exists to avoid.
	list := &foundationdbtypes.FoundationDBBackupList{}
	if err := c.List(context.Background(), list, client.InNamespace("tenant")); err != nil {
		t.Fatalf("list FoundationDBBackup: %v", err)
	}
	if len(list.Items) > 0 {
		t.Errorf("expected zero FoundationDBBackup CRs before the cluster reports a version; got %d", len(list.Items))
	}
}

// TestReconcileFoundationDB_HealthUnavailableIsTransient pins the gate
// that defers the backup until the operator reports
// FoundationDBCluster.status.health.available=true. Without it, a
// freshly-rendered cluster (HelmRelease applied but processes not yet
// reconciled) would race into FoundationDBBackup creation against a
// cluster that hasn't joined a quorum yet; the operator would either
// stall or fail the start-backup transaction. The fix produces a
// Ready=False/FoundationDBClusterNotAvailable requeue instead.
func TestReconcileFoundationDB_HealthUnavailableIsTransient(t *testing.T) {
	c, job, resolved := reconcileFoundationDBSeed(
		t,
		foundationdbtypes.FoundationDBClusterSpec{Version: "7.3.63"},
		foundationdbtypes.FoundationDBClusterStatus{
			Health: foundationdbtypes.FoundationDBClusterHealth{Available: false},
		},
	)
	r := &BackupJobReconciler{Client: c, Scheme: c.Scheme(), Recorder: record.NewFakeRecorder(10)}

	res, err := r.reconcileFoundationDB(context.Background(), job, resolved)
	if err != nil {
		t.Fatalf("reconcileFoundationDB: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("health.available=false must produce transient requeue, got %+v", res)
	}
	if job.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
		t.Errorf("health.available=false must NOT mark BackupJob Failed; got phase=%q", job.Status.Phase)
	}
	cond := apimeta.FindStatusCondition(job.Status.Conditions, "Ready")
	if cond == nil {
		t.Fatalf("expected Ready condition, got none")
	}
	if cond.Status != metav1.ConditionFalse || cond.Reason != "FoundationDBClusterNotAvailable" {
		t.Errorf("expected Ready=False/FoundationDBClusterNotAvailable, got %s/%s", cond.Status, cond.Reason)
	}
	list := &foundationdbtypes.FoundationDBBackupList{}
	if err := c.List(context.Background(), list, client.InNamespace("tenant")); err != nil {
		t.Fatalf("list FoundationDBBackup: %v", err)
	}
	if len(list.Items) > 0 {
		t.Errorf("expected zero FoundationDBBackup CRs while the cluster is unavailable; got %d", len(list.Items))
	}
}

// ---------------------------------------------------------------------------
// Backup-artifact takenAt semantics
// ---------------------------------------------------------------------------

// TestCreateFoundationDBBackupArtifact_TakenAtIsWallClock pins the
// semantics noted in the artifact builder's comment block: the FDB
// operator surfaces backupDetails.snapshotTime as an FDB-internal
// read-version (an integer counter, not a Unix timestamp), so the
// Cozystack Backup.spec.takenAt must come from wall-clock "now" instead.
// Before this regression test a no-op conditional reassigned takenAt with
// the same wall-clock value, hiding the intent and inviting a future
// refactor to mis-convert SnapshotTime (e.g. into time.Unix(... , 0)) and
// produce a year-1970 takenAt. The check below would catch that
// regression: a SnapshotTime of 43017506 interpreted as a Unix timestamp
// lands in 1971.
func TestCreateFoundationDBBackupArtifact_TakenAtIsWallClock(t *testing.T) {
	apps := foundationdbapp.GroupName
	strategyAPIGroup := strategyv1alpha1.GroupVersion.Group
	job := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj-wallclock"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{
				Kind: "FoundationDB", Name: "fdb-src", APIGroup: &apps,
			},
		},
	}
	c := newFoundationDBStrategyTestClient(t, job)
	r := &BackupJobReconciler{Client: c, Scheme: c.Scheme()}
	resolved := &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{
			APIGroup: &strategyAPIGroup,
			Kind:     strategyv1alpha1.FoundationDBStrategyKind,
			Name:     "fdb-strategy",
		},
		Parameters: map[string]string{},
	}
	// SnapshotTime is FDB read-version - a large integer that is NOT a
	// Unix timestamp. If a future refactor lifts SnapshotTime into
	// takenAt directly, the resulting time lands far away from "now".
	fdbBackup := &foundationdbtypes.FoundationDBBackup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "fdb-bk"},
		Status: foundationdbtypes.FoundationDBBackupStatus{
			BackupDetails: &foundationdbtypes.BackupDetails{
				Running: true, SnapshotTime: 43017506,
			},
		},
	}
	rendered := newRenderedFoundationDBTemplate()
	app := newFoundationDBApp("fdb-src", "tenant")

	before := time.Now()
	artifact, err := r.createFoundationDBBackupArtifact(context.Background(), job, resolved, fdbBackup, rendered, app)
	if err != nil {
		t.Fatalf("createFoundationDBBackupArtifact: %v", err)
	}
	after := time.Now()
	// takenAt must fall in the wall-clock window the test observed, with a
	// small slack on either side for clock jitter and the helper's own
	// metav1.Now() calls. If SnapshotTime ever bled into takenAt the value
	// would land decades away from this window.
	got := artifact.Spec.TakenAt.Time
	if got.Before(before.Add(-2 * time.Second)) {
		t.Errorf("takenAt %s is before the call window start %s; SnapshotTime must not bleed into the wall-clock value", got, before)
	}
	if got.After(after.Add(2 * time.Second)) {
		t.Errorf("takenAt %s is after the call window end %s", got, after)
	}
}

// ---------------------------------------------------------------------------
// Precondition: missing blob-credentials Secret (review #2)
// ---------------------------------------------------------------------------

// reconcileFoundationDBSeedWithTemplate is reconcileFoundationDBSeed with
// an explicit strategy template override. The default helper uses the
// minimal template returned by newRenderedFoundationDBTemplate(), which
// has no BackupDeploymentPodTemplateSpec - tests that exercise the
// PodTemplateSpec-traversal preconditions need a template that
// references Secrets.
func reconcileFoundationDBSeedWithTemplate(
	t *testing.T,
	tmpl strategyv1alpha1.FoundationDBTemplate,
	clusterSpec foundationdbtypes.FoundationDBClusterSpec,
	clusterStatus foundationdbtypes.FoundationDBClusterStatus,
	extra ...client.Object,
) (client.Client, *backupsv1alpha1.BackupJob, *ResolvedBackupConfig) {
	t.Helper()
	apps := foundationdbapp.GroupName
	strategyAPIGroup := strategyv1alpha1.GroupVersion.Group

	strategy := &strategyv1alpha1.FoundationDB{
		ObjectMeta: metav1.ObjectMeta{Name: "fdb-strategy"},
		Spec:       strategyv1alpha1.FoundationDBSpec{Template: tmpl},
	}
	app := newFoundationDBApp("fdb-src", "tenant")
	cluster := &foundationdbtypes.FoundationDBCluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "tenant",
			Name:      foundationdbClusterNameForApp("fdb-src"),
		},
		Spec:   clusterSpec,
		Status: clusterStatus,
	}
	now := metav1.NewTime(time.Now().Add(-1 * time.Minute))
	job := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{
				Kind: "FoundationDB", Name: "fdb-src", APIGroup: &apps,
			},
		},
		Status: backupsv1alpha1.BackupJobStatus{StartedAt: &now},
	}

	objs := append([]client.Object{strategy, app, cluster, job}, extra...)
	c := newFoundationDBStrategyTestClient(t, objs...)
	resolved := &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{
			APIGroup: &strategyAPIGroup,
			Kind:     strategyv1alpha1.FoundationDBStrategyKind,
			Name:     strategy.Name,
		},
		Parameters: map[string]string{},
	}
	fresh := &backupsv1alpha1.BackupJob{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(job), fresh); err != nil {
		t.Fatalf("seed Get BackupJob: %v", err)
	}
	return c, fresh, resolved
}

// templateReferencingSecret returns a minimal strategy template whose
// BackupDeploymentPodTemplateSpec mounts a Secret by the given name (and
// references the same Secret via envFrom to exercise both traversal
// paths in podTemplateSpecMissingSecrets).
func templateReferencingSecret(secretName string) strategyv1alpha1.FoundationDBTemplate {
	tmpl := *newRenderedFoundationDBTemplate()
	tmpl.BackupDeploymentPodTemplateSpec = &corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name: "foundationdb",
				EnvFrom: []corev1.EnvFromSource{{
					SecretRef: &corev1.SecretEnvSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
					},
				}},
				VolumeMounts: []corev1.VolumeMount{{
					Name: "blob-credentials", MountPath: "/var/fdb-blob-credentials", ReadOnly: true,
				}},
			}},
			Volumes: []corev1.Volume{{
				Name: "blob-credentials",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{SecretName: secretName},
				},
			}},
		},
	}
	return tmpl
}

// TestPodTemplateSpecMissingSecrets_DedupesAndFiltersOptional pins the
// behaviour of the traversal helper used by the missing-Secret
// precondition: the same Secret referenced by both `volumes` and
// `envFrom` should be reported once, and optional references should be
// skipped. Catches a regression where someone touches the helper and
// loses dedup (the reconcile loop would then Get the same Secret
// twice per reconcile, harmless but wasteful) or starts reporting
// optional refs (which would cause spurious "missing Secret" failures
// for opt-in mounts the operator is expected to tolerate).
func TestPodTemplateSpecMissingSecrets_DedupesAndFiltersOptional(t *testing.T) {
	tr := true
	tmpl := &corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name: "x",
				EnvFrom: []corev1.EnvFromSource{
					{SecretRef: &corev1.SecretEnvSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: "creds"},
					}},
					{SecretRef: &corev1.SecretEnvSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: "optional-extras"},
						Optional:             &tr,
					}},
				},
			}},
			Volumes: []corev1.Volume{
				{Name: "v1", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "creds"}}},
				{Name: "v2", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "ca"}}},
				{Name: "v3", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "opt-ca", Optional: &tr}}},
			},
		},
	}
	got := podTemplateSpecMissingSecrets(tmpl)
	want := map[string]bool{"creds": true, "ca": true}
	for _, name := range got {
		if !want[name] {
			t.Errorf("unexpected Secret %q reported (optional refs must be skipped)", name)
		}
		delete(want, name)
	}
	if len(want) > 0 {
		t.Errorf("missing Secrets not reported: %v", want)
	}
	// And ensure no duplicates.
	seen := map[string]int{}
	for _, name := range got {
		seen[name]++
	}
	for name, n := range seen {
		if n > 1 {
			t.Errorf("Secret %q reported %d times; expected exactly once", name, n)
		}
	}
}

// TestReconcileFoundationDB_MissingBlobCredsSecretIsTransient pins #2 from
// review. A strategy template whose PodTemplateSpec mounts a per-app
// blob-credentials Secret must NOT cause the BackupJob to spin until the
// 45-minute deadline when the tenant forgot to pre-create the Secret;
// the driver must surface a fast Ready=False/MissingBlobCredentialsSecret
// requeue and not materialise a FoundationDBBackup CR (whose backup_agent
// Deployment would crash-loop).
func TestReconcileFoundationDB_MissingBlobCredsSecretIsTransient(t *testing.T) {
	secretName := "fdb-src-fdb-backup-creds"
	tmpl := templateReferencingSecret(secretName)
	c, job, resolved := reconcileFoundationDBSeedWithTemplate(
		t, tmpl,
		foundationdbtypes.FoundationDBClusterSpec{Version: "7.3.63"},
		foundationdbtypes.FoundationDBClusterStatus{
			Health: foundationdbtypes.FoundationDBClusterHealth{Available: true},
		},
	)
	r := &BackupJobReconciler{Client: c, Scheme: c.Scheme(), Recorder: record.NewFakeRecorder(10)}

	res, err := r.reconcileFoundationDB(context.Background(), job, resolved)
	if err != nil {
		t.Fatalf("reconcileFoundationDB: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("missing blob-creds Secret must produce transient requeue, got %+v", res)
	}
	if job.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
		t.Errorf("missing Secret must NOT mark BackupJob Failed; got phase=%q", job.Status.Phase)
	}
	cond := apimeta.FindStatusCondition(job.Status.Conditions, "Ready")
	if cond == nil {
		t.Fatalf("expected Ready condition, got none")
	}
	if cond.Status != metav1.ConditionFalse || cond.Reason != "MissingBlobCredentialsSecret" {
		t.Errorf("expected Ready=False/MissingBlobCredentialsSecret, got %s/%s", cond.Status, cond.Reason)
	}
	if !strings.Contains(cond.Message, secretName) {
		t.Errorf("expected Ready message to name the missing Secret %q; got %q", secretName, cond.Message)
	}
	list := &foundationdbtypes.FoundationDBBackupList{}
	if err := c.List(context.Background(), list, client.InNamespace("tenant")); err != nil {
		t.Fatalf("list FoundationDBBackup: %v", err)
	}
	if len(list.Items) > 0 {
		t.Errorf("expected zero FoundationDBBackup CRs while Secret is missing; got %d", len(list.Items))
	}
}

// TestReconcileFoundationDB_MissingBlobCredsSecretPresentProceeds is the
// positive companion: when the same Secret IS present, the reconcile
// must move past the precondition and materialise the FoundationDBBackup.
// Without this companion the test pair could silently regress to "always
// requeue on a non-empty PodTemplateSpec" and the negative test would
// still pass.
func TestReconcileFoundationDB_MissingBlobCredsSecretPresentProceeds(t *testing.T) {
	secretName := "fdb-src-fdb-backup-creds"
	tmpl := templateReferencingSecret(secretName)
	creds := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: secretName},
		Type:       corev1.SecretTypeOpaque,
	}
	c, job, resolved := reconcileFoundationDBSeedWithTemplate(
		t, tmpl,
		foundationdbtypes.FoundationDBClusterSpec{Version: "7.3.63"},
		foundationdbtypes.FoundationDBClusterStatus{
			Health: foundationdbtypes.FoundationDBClusterHealth{Available: true},
		},
		creds,
	)
	r := &BackupJobReconciler{Client: c, Scheme: c.Scheme(), Recorder: record.NewFakeRecorder(10)}

	if _, err := r.reconcileFoundationDB(context.Background(), job, resolved); err != nil {
		t.Fatalf("reconcileFoundationDB: %v", err)
	}
	list := &foundationdbtypes.FoundationDBBackupList{}
	if err := c.List(context.Background(), list, client.InNamespace("tenant")); err != nil {
		t.Fatalf("list FoundationDBBackup: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("expected exactly one FoundationDBBackup once Secret is present; got %d", len(list.Items))
	}
}

// ---------------------------------------------------------------------------
// Precondition: conflicting in-chart backup (review #3)
// ---------------------------------------------------------------------------

// TestReconcileFoundationDB_ConflictingInChartBackupIsFatal pins #3 from
// review. A chart-rendered FoundationDBBackup (no driver labels, Running,
// targeting the same cluster) must produce a terminal BackupJob failure
// with the conflict-specific reason. Silently stopping the chart's CR
// would split the in-chart user's backup stream and corrupt both flows.
// stopOtherFoundationDBBackups labels-on-list cannot find the chart CR,
// so this gate is the only thing standing between the two.
func TestReconcileFoundationDB_ConflictingInChartBackupIsFatal(t *testing.T) {
	chartBackup := &foundationdbtypes.FoundationDBBackup{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "tenant",
			Name:      "foundationdb-fdb-src-backup", // chart-rendered name
			// Critically: no driver labels.
		},
		Spec: foundationdbtypes.FoundationDBBackupSpec{
			ClusterName: foundationdbClusterNameForApp("fdb-src"),
			Version:     "7.3.63",
			BackupState: foundationdbtypes.BackupStateRunning,
		},
	}
	c, job, resolved := reconcileFoundationDBSeedWithTemplate(
		t,
		*newRenderedFoundationDBTemplate(),
		foundationdbtypes.FoundationDBClusterSpec{Version: "7.3.63"},
		foundationdbtypes.FoundationDBClusterStatus{
			Health: foundationdbtypes.FoundationDBClusterHealth{Available: true},
		},
		chartBackup,
	)
	r := &BackupJobReconciler{Client: c, Scheme: c.Scheme(), Recorder: record.NewFakeRecorder(10)}

	if _, err := r.reconcileFoundationDB(context.Background(), job, resolved); err != nil {
		t.Fatalf("reconcileFoundationDB: %v", err)
	}
	if job.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
		t.Fatalf("expected BackupJob to be Failed on in-chart conflict, got phase=%q", job.Status.Phase)
	}
	if !strings.Contains(job.Status.Message, "conflicting in-chart FoundationDBBackup") {
		t.Errorf("expected message to mention 'conflicting in-chart FoundationDBBackup'; got %q", job.Status.Message)
	}
	if !strings.Contains(job.Status.Message, chartBackup.Name) {
		t.Errorf("expected message to name the conflicting CR %q; got %q", chartBackup.Name, job.Status.Message)
	}
	// The chart-rendered CR must remain untouched - the diagnostic is
	// loud precisely so the user can review it before deleting.
	got := &foundationdbtypes.FoundationDBBackup{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(chartBackup), got); err != nil {
		t.Fatalf("get chart backup post-reconcile: %v", err)
	}
	if got.Spec.BackupState != foundationdbtypes.BackupStateRunning {
		t.Errorf("driver must not modify the chart-rendered CR's spec.backupState; got %q", got.Spec.BackupState)
	}
}

// TestFindConflictingInChartBackup_IgnoresDriverManagedCRs pins the
// label-key gate inside findConflictingInChartBackup: a Running
// FoundationDBBackup that DOES carry foundationdbClusterLabel is a
// driver-managed CR (e.g. a prior BackupJob's leftover), not a
// chart-rendered conflict, and must be passed through to
// stopOtherFoundationDBBackups instead of failing the new BackupJob.
func TestFindConflictingInChartBackup_IgnoresDriverManagedCRs(t *testing.T) {
	clusterName := foundationdbClusterNameForApp("fdb-src")
	driverBackup := &foundationdbtypes.FoundationDBBackup{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "tenant",
			Name:      "bj-prev-aaa",
			Labels: map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      "bj-prev",
				backupsv1alpha1.OwningJobNamespaceLabel: "tenant",
				foundationdbClusterLabel:                clusterName,
			},
		},
		Spec: foundationdbtypes.FoundationDBBackupSpec{
			ClusterName: clusterName,
			BackupState: foundationdbtypes.BackupStateRunning,
		},
	}
	c := newFoundationDBStrategyTestClient(t, driverBackup)
	r := &BackupJobReconciler{Client: c, Scheme: c.Scheme()}

	name, err := r.findConflictingInChartBackup(context.Background(), "tenant", clusterName)
	if err != nil {
		t.Fatalf("findConflictingInChartBackup: %v", err)
	}
	if name != "" {
		t.Errorf("driver-managed Running CR must NOT be reported as conflicting; got %q", name)
	}
}

// ---------------------------------------------------------------------------
// stopOtherFoundationDBBackups: retry on conflict (review #6)
// ---------------------------------------------------------------------------

// TestStopOtherFoundationDBBackups_RetriesOnConflict pins #6 from review.
// The operator reconciles spec.backupState concurrently with our patch;
// a stale resourceVersion produces a 409 Conflict that previously failed
// the entire BackupJob with "stop prior FoundationDBBackup ...". The fix
// is to refetch + retry on 409. This test injects a single 409 on the
// first Patch via an interceptor, then lets the second patch through;
// the prior CR must end up Stopped and no error must propagate.
func TestStopOtherFoundationDBBackups_RetriesOnConflict(t *testing.T) {
	clusterName := foundationdbClusterNameForApp("fdb-src")
	prior := &foundationdbtypes.FoundationDBBackup{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "tenant",
			Name:      "bj-prev-bbb",
			Labels: map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      "bj-prev",
				backupsv1alpha1.OwningJobNamespaceLabel: "tenant",
				foundationdbClusterLabel:                clusterName,
			},
		},
		Spec: foundationdbtypes.FoundationDBBackupSpec{
			ClusterName: clusterName,
			BackupState: foundationdbtypes.BackupStateRunning,
		},
	}
	job := newFoundationDBBackupJob("bj-new", "tenant")

	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	_ = backupsv1alpha1.AddToScheme(s)
	_ = strategyv1alpha1.AddToScheme(s)
	_ = foundationdbtypes.AddToScheme(s)
	_ = foundationdbapp.AddToScheme(s)

	patchAttempts := 0
	fdbBackupGR := schema.GroupResource{
		Group:    foundationdbtypes.GroupName,
		Resource: "foundationdbbackups",
	}
	c := clientfake.NewClientBuilder().
		WithScheme(s).
		WithObjects(prior, job).
		WithStatusSubresource(&backupsv1alpha1.BackupJob{}, &backupsv1alpha1.RestoreJob{}, &backupsv1alpha1.Backup{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if _, ok := obj.(*foundationdbtypes.FoundationDBBackup); ok {
					patchAttempts++
					if patchAttempts == 1 {
						return apierrors.NewConflict(fdbBackupGR, obj.GetName(),
							fmt.Errorf("simulated stale resourceVersion"))
					}
				}
				return cl.Patch(ctx, obj, patch, opts...)
			},
		}).
		Build()
	r := &BackupJobReconciler{Client: c, Scheme: s}

	if err := r.stopOtherFoundationDBBackups(context.Background(), job, clusterName); err != nil {
		t.Fatalf("stopOtherFoundationDBBackups returned error despite retry budget; the 409 should have been absorbed: %v", err)
	}
	if patchAttempts < 2 {
		t.Errorf("expected at least 2 Patch attempts (one Conflict + one success), got %d", patchAttempts)
	}
	got := &foundationdbtypes.FoundationDBBackup{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(prior), got); err != nil {
		t.Fatalf("get prior post-stop: %v", err)
	}
	if got.Spec.BackupState != foundationdbtypes.BackupStateStopped {
		t.Errorf("prior FoundationDBBackup must be Stopped after retry; got %q", got.Spec.BackupState)
	}
}

// ---------------------------------------------------------------------------
// Terminal-failure cleanup (review #7)
// ---------------------------------------------------------------------------

// TestReconcileFoundationDB_DeadlineExpiry_StopsFoundationDBBackup pins
// #7 from review. When the BackupJob hits the 45-minute deadline before
// the FoundationDBBackup lands a snapshot, the driver must Stop the
// operator-side CR before marking the BackupJob Failed. Otherwise the
// backup_agent Deployment continues retrying against a broken S3
// endpoint forever (one Deployment leaked per failed BackupJob) and the
// FDB operator keeps reconciling against an undeleted CR.
func TestReconcileFoundationDB_DeadlineExpiry_StopsFoundationDBBackup(t *testing.T) {
	apps := foundationdbapp.GroupName
	strategyAPIGroup := strategyv1alpha1.GroupVersion.Group
	clusterName := foundationdbClusterNameForApp("fdb-src")
	pastStart := metav1.NewTime(time.Now().Add(-2 * foundationdbDefaultBackupDeadline))

	strategy := &strategyv1alpha1.FoundationDB{
		ObjectMeta: metav1.ObjectMeta{Name: "fdb-strategy"},
		Spec:       strategyv1alpha1.FoundationDBSpec{Template: *newRenderedFoundationDBTemplate()},
	}
	app := newFoundationDBApp("fdb-src", "tenant")
	cluster := &foundationdbtypes.FoundationDBCluster{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: clusterName},
		Spec:       foundationdbtypes.FoundationDBClusterSpec{Version: "7.3.63"},
		Status: foundationdbtypes.FoundationDBClusterStatus{
			Health: foundationdbtypes.FoundationDBClusterHealth{Available: true},
		},
	}
	job := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{
				Kind: "FoundationDB", Name: "fdb-src", APIGroup: &apps,
			},
		},
		// StartedAt 2x deadline ago - foundationdbBackupDeadlineExceeded
		// will return true on the first reconcile that reaches the gate.
		Status: backupsv1alpha1.BackupJobStatus{
			StartedAt: &pastStart,
			Phase:     backupsv1alpha1.BackupJobPhaseRunning,
		},
	}
	// Pre-seeded prior FoundationDBBackup with the BackupJob's labels and
	// no backupDetails (so foundationdbBackupReady returns false) - this
	// is the CR the driver must Stop on deadline expiry.
	stuck := &foundationdbtypes.FoundationDBBackup{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "tenant",
			Name:      "bj-stuck",
			Labels: map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      job.Name,
				backupsv1alpha1.OwningJobNamespaceLabel: job.Namespace,
				foundationdbClusterLabel:                clusterName,
			},
		},
		Spec: foundationdbtypes.FoundationDBBackupSpec{
			ClusterName: clusterName,
			Version:     "7.3.63",
			BackupState: foundationdbtypes.BackupStateRunning,
		},
	}
	c := newFoundationDBStrategyTestClient(t, strategy, app, cluster, job, stuck)
	fresh := &backupsv1alpha1.BackupJob{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(job), fresh); err != nil {
		t.Fatalf("seed Get BackupJob: %v", err)
	}
	resolved := &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{
			APIGroup: &strategyAPIGroup,
			Kind:     strategyv1alpha1.FoundationDBStrategyKind,
			Name:     strategy.Name,
		},
		Parameters: map[string]string{},
	}
	r := &BackupJobReconciler{Client: c, Scheme: c.Scheme(), Recorder: record.NewFakeRecorder(10)}

	if _, err := r.reconcileFoundationDB(context.Background(), fresh, resolved); err != nil {
		t.Fatalf("reconcileFoundationDB: %v", err)
	}
	if fresh.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
		t.Fatalf("expected BackupJob to be Failed on deadline expiry; got phase=%q", fresh.Status.Phase)
	}
	got := &foundationdbtypes.FoundationDBBackup{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(stuck), got); err != nil {
		t.Fatalf("get stuck CR post-reconcile: %v", err)
	}
	if got.Spec.BackupState != foundationdbtypes.BackupStateStopped {
		t.Errorf("FoundationDBBackup must be Stopped before terminal failure; got %q (backup_agent Deployment would otherwise leak past the BackupJob's lifetime)", got.Spec.BackupState)
	}
}

// ---------------------------------------------------------------------------
// Malformed restoreJob.spec.options is terminal (review #11)
// ---------------------------------------------------------------------------

// TestReconcileFoundationDBRestore_MalformedOptionsIsTerminal pins #11
// from review. spec.options is a tenant-supplied JSON blob; when it is
// malformed (e.g. user wrote `restoreTimeoutSeconds: "5h"` as a string
// instead of an int64), the driver previously silently fell back to the
// 30-minute default deadline and the misconfiguration would persist
// until a restore exceeded that default. The fix marks the RestoreJob
// Failed terminally so the tenant gets an actionable signal on the
// first reconcile. This test seeds malformed options and asserts the
// terminal failure plus that no FoundationDBRestore CR was created.
func TestReconcileFoundationDBRestore_MalformedOptionsIsTerminal(t *testing.T) {
	apps := foundationdbapp.GroupName
	now := metav1.Now()
	cozyBackup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "src-cozy-bk"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{
				Kind: "FoundationDB", Name: "fdb-src", APIGroup: &apps,
			},
			DriverMetadata: map[string]string{
				foundationdbAccountNameKey:    "key@s3.example:9000",
				foundationdbBucketKey:         "bkt",
				foundationdbBlobBackupNameKey: "src-op-bk",
			},
		},
	}
	// Valid JSON, invalid against the FoundationDBRestoreOptions schema:
	// restoreTimeoutSeconds is declared as int64 but a tenant has typed it
	// as a string. parseFoundationDBRestoreOptions surfaces this as an
	// unmarshal error. (Using raw "not-json" would fail RawExtension's
	// MarshalJSON during fake-client seeding, before the code under test
	// runs - choose a payload that round-trips through the apiserver and
	// is rejected only inside parseFoundationDBRestoreOptions.)
	rj := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "rj-malformed"},
		Spec: backupsv1alpha1.RestoreJobSpec{
			BackupRef: corev1.LocalObjectReference{Name: cozyBackup.Name},
			Options:   &runtime.RawExtension{Raw: []byte(`{"restoreTimeoutSeconds": "five-hours"}`)},
		},
		Status: backupsv1alpha1.RestoreJobStatus{StartedAt: &now},
	}
	c := newFoundationDBStrategyTestClient(t, cozyBackup, rj)
	r := &RestoreJobReconciler{Client: c, Recorder: record.NewFakeRecorder(10)}

	if _, err := r.reconcileFoundationDBRestore(context.Background(), rj, cozyBackup); err != nil {
		t.Fatalf("reconcileFoundationDBRestore: %v", err)
	}
	if rj.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
		t.Fatalf("malformed spec.options must mark RestoreJob Failed; got phase=%q", rj.Status.Phase)
	}
	cond := apimeta.FindStatusCondition(rj.Status.Conditions, "Ready")
	if cond == nil {
		t.Fatalf("expected Ready condition, got none")
	}
	if !strings.Contains(cond.Message, "malformed") {
		t.Errorf("expected Ready message to flag malformed options; got %q", cond.Message)
	}
	list := &foundationdbtypes.FoundationDBRestoreList{}
	if err := c.List(context.Background(), list, client.InNamespace("tenant")); err != nil {
		t.Fatalf("list FoundationDBRestore: %v", err)
	}
	if len(list.Items) > 0 {
		t.Errorf("expected zero FoundationDBRestore CRs on malformed options; got %d", len(list.Items))
	}
}

// ---------------------------------------------------------------------------
// Snapshot APIVersion/Kind contract (review #1 from lllamnyp)
// ---------------------------------------------------------------------------

// TestDecodeFoundationDBBackupSnapshot_RejectsMismatchedAPIVersion pins
// the contract documented next to foundationdbBackupSnapshotKind: a
// future v2 snapshot with the same Kind but a different APIVersion must
// be rejected with errSnapshotUnrecognised, not silently parsed as v1.
// Before this fix the decoder ignored APIVersion entirely; a v2
// snapshot with extra fields would have been mis-interpreted as v1.
func TestDecodeFoundationDBBackupSnapshot_RejectsMismatchedAPIVersion(t *testing.T) {
	raw, err := json.Marshal(foundationdbBackupSnapshot{
		Kind:       foundationdbBackupSnapshotKind,
		APIVersion: "future.cozystack.io/v2",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := decodeFoundationDBBackupSnapshot(&runtime.RawExtension{Raw: raw})
	if err == nil {
		t.Fatalf("expected decode error on mismatched APIVersion, got nil")
	}
	if !errors.Is(err, errSnapshotUnrecognised) {
		t.Errorf("expected error to wrap errSnapshotUnrecognised; got %v", err)
	}
	if got != nil {
		t.Errorf("expected nil snapshot on mismatch; got %+v", got)
	}
}

// TestDecodeFoundationDBBackupSnapshot_RejectsMismatchedKind pins the
// same contract for the Kind field. Previously the decoder returned
// (nil, nil) on Kind mismatch, indistinguishable from "no snapshot
// present", which let a foreign payload silently fall through to the
// driverMetadata fallback at the call site.
func TestDecodeFoundationDBBackupSnapshot_RejectsMismatchedKind(t *testing.T) {
	raw, err := json.Marshal(foundationdbBackupSnapshot{
		Kind:       "SomeOtherSnapshot",
		APIVersion: foundationdbBackupSnapshotAPIVersion,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := decodeFoundationDBBackupSnapshot(&runtime.RawExtension{Raw: raw})
	if err == nil {
		t.Fatalf("expected decode error on mismatched Kind, got nil")
	}
	if !errors.Is(err, errSnapshotUnrecognised) {
		t.Errorf("expected error to wrap errSnapshotUnrecognised; got %v", err)
	}
	if got != nil {
		t.Errorf("expected nil snapshot on mismatch; got %+v", got)
	}
}

// TestDecodeFoundationDBBackupSnapshot_NilRawReturnsNilNil pins the
// "no snapshot present" path so the call site can keep distinguishing
// it from the unrecognised case via err == nil + snap == nil.
func TestDecodeFoundationDBBackupSnapshot_NilRawReturnsNilNil(t *testing.T) {
	got, err := decodeFoundationDBBackupSnapshot(nil)
	if err != nil || got != nil {
		t.Errorf("nil raw: got (%+v, %v) want (nil, nil)", got, err)
	}
	got, err = decodeFoundationDBBackupSnapshot(&runtime.RawExtension{})
	if err != nil || got != nil {
		t.Errorf("empty raw: got (%+v, %v) want (nil, nil)", got, err)
	}
}

// TestReconcileFoundationDBRestore_SnapshotAPIVersionMismatchIsTerminal
// pins the call-site half of the contract: a Backup carrying a
// snapshot with a foreign APIVersion must terminate the RestoreJob
// instead of silently falling back to driverMetadata. Without this
// gate a v1 reader would proceed against a v2 snapshot's stale
// interpretation, corrupting the restore (any v2 fields the v1 reader
// doesn't carry - e.g. new CustomParameters - would be silently
// dropped).
func TestReconcileFoundationDBRestore_SnapshotAPIVersionMismatchIsTerminal(t *testing.T) {
	apps := foundationdbapp.GroupName
	now := metav1.Now()
	rawSnap, err := json.Marshal(foundationdbBackupSnapshot{
		Kind:       foundationdbBackupSnapshotKind,
		APIVersion: "future.cozystack.io/v2",
	})
	if err != nil {
		t.Fatalf("marshal snap: %v", err)
	}
	cozyBackup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "src-cozy-bk"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{
				Kind: "FoundationDB", Name: "fdb-src", APIGroup: &apps,
			},
			// driverMetadata IS present - confirms the call site does
			// not fall back to it when the snapshot is unrecognised.
			DriverMetadata: map[string]string{
				foundationdbAccountNameKey:    "key@s3.example:9000",
				foundationdbBucketKey:         "bkt",
				foundationdbBlobBackupNameKey: "src-op-bk",
			},
		},
		Status: backupsv1alpha1.BackupStatus{
			UnderlyingResources: &runtime.RawExtension{Raw: rawSnap},
		},
	}
	rj := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "rj-version-mismatch"},
		Spec: backupsv1alpha1.RestoreJobSpec{
			BackupRef: corev1.LocalObjectReference{Name: cozyBackup.Name},
		},
		Status: backupsv1alpha1.RestoreJobStatus{StartedAt: &now},
	}
	c := newFoundationDBStrategyTestClient(t, cozyBackup, rj)
	r := &RestoreJobReconciler{Client: c, Recorder: record.NewFakeRecorder(10)}

	if _, err := r.reconcileFoundationDBRestore(context.Background(), rj, cozyBackup); err != nil {
		t.Fatalf("reconcileFoundationDBRestore: %v", err)
	}
	if rj.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
		t.Fatalf("unrecognised snapshot apiVersion must mark RestoreJob Failed; got phase=%q", rj.Status.Phase)
	}
	cond := apimeta.FindStatusCondition(rj.Status.Conditions, "Ready")
	if cond == nil {
		t.Fatalf("expected Ready condition, got none")
	}
	if !strings.Contains(cond.Message, "unrecognised") && !strings.Contains(cond.Message, "apiVersion") {
		t.Errorf("expected Ready message to flag the apiVersion/kind mismatch; got %q", cond.Message)
	}
	list := &foundationdbtypes.FoundationDBRestoreList{}
	if err := c.List(context.Background(), list, client.InNamespace("tenant")); err != nil {
		t.Fatalf("list FoundationDBRestore: %v", err)
	}
	if len(list.Items) > 0 {
		t.Errorf("expected zero FoundationDBRestore CRs on snapshot mismatch; got %d", len(list.Items))
	}
}

// ---------------------------------------------------------------------------
// No-OwnerReference contract (review #4 from lllamnyp)
// ---------------------------------------------------------------------------

// TestEnsureFoundationDBBackup_DoesNotSetOwnerReference pins the
// deliberate omission of OwnerReferences from the operator-side
// FoundationDBBackup CR. The driver labels the CR with the BackupJob's
// OwningJob{Name,Namespace} for idempotent ensure-by-label semantics
// across BackupJob recreates with the same name (`kubectl delete &&
// kubectl apply` retries find the prior CR and reuse it instead of
// leaking a duplicate backup_agent Deployment). Adding an
// OwnerReference back to the BackupJob would make Kubernetes GC reap
// the operator CR the moment the parent BackupJob is deleted,
// defeating the reuse path. The framework's label-based cleanup
// (cleanup.sh, stopFoundationDBBackupForJob) handles teardown
// explicitly.
//
// If a future change adds OwnerReferences "for safety", this test
// fails immediately so the regression is caught before users hit it.
func TestEnsureFoundationDBBackup_DoesNotSetOwnerReference(t *testing.T) {
	c := newFoundationDBStrategyTestClient(t)
	r := &BackupJobReconciler{Client: c, Scheme: c.Scheme()}
	job := newFoundationDBBackupJob("bj-no-owner", "tenant")
	if err := c.Create(context.Background(), job); err != nil {
		t.Fatalf("seed BackupJob: %v", err)
	}
	clusterName := foundationdbClusterNameForApp("fdb-src")
	rendered := newRenderedFoundationDBTemplate()

	got, err := r.ensureFoundationDBBackup(context.Background(), job, clusterName, "7.3.63", rendered)
	if err != nil {
		t.Fatalf("ensureFoundationDBBackup: %v", err)
	}
	if got == nil {
		t.Fatalf("expected a FoundationDBBackup, got nil")
	}
	if len(got.OwnerReferences) != 0 {
		t.Errorf("FoundationDBBackup must NOT carry OwnerReferences (would defeat the idempotent ensure-by-label reuse across BackupJob recreates); got %#v", got.OwnerReferences)
	}
	// Also confirm the labels that DO power the reuse path are present -
	// the no-OwnerReference contract only makes sense alongside them.
	if got.Labels[backupsv1alpha1.OwningJobNameLabel] != job.Name {
		t.Errorf("OwningJobName label missing or wrong: %v", got.Labels)
	}
	if got.Labels[backupsv1alpha1.OwningJobNamespaceLabel] != job.Namespace {
		t.Errorf("OwningJobNamespace label missing or wrong: %v", got.Labels)
	}
}

// ---------------------------------------------------------------------------
// requeueWithReason retry on Status Conflict (review #5 from lllamnyp)
// ---------------------------------------------------------------------------

// TestRequeueWithReason_RetriesOnStatusConflict pins #5 from review.
// requeueWithReason is hot on the cold-start reconcile path (six call
// sites, five of which fire before the BackupJob's first successful
// Status update), where the informer cache is most likely to be
// stale. Before this fix a 409 Conflict on the first Status().Update
// bubbled up to controller-runtime, which rescheduled the reconcile -
// on a heavily-contended namespace the next reschedule could observe
// the same stale RV and busy-spin until the informer caught up.
//
// The fix refetches the BackupJob on 409 and retries the Update with
// the fresh resourceVersion (mirroring the patch-retry budget used by
// stopOtherFoundationDBBackups). This test injects a single 409 on
// the first Status().Update via interceptor.Funcs, then lets the
// second through; the condition must land without returning an error.
func TestRequeueWithReason_RetriesOnStatusConflict(t *testing.T) {
	job := newFoundationDBBackupJob("bj-conflict", "tenant")

	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	_ = backupsv1alpha1.AddToScheme(s)
	_ = strategyv1alpha1.AddToScheme(s)
	_ = foundationdbtypes.AddToScheme(s)
	_ = foundationdbapp.AddToScheme(s)

	patchAttempts := 0
	gr := schema.GroupResource{Group: backupsv1alpha1.GroupVersion.Group, Resource: "backupjobs"}
	c := clientfake.NewClientBuilder().
		WithScheme(s).
		WithObjects(job).
		WithStatusSubresource(&backupsv1alpha1.BackupJob{}, &backupsv1alpha1.RestoreJob{}, &backupsv1alpha1.Backup{}).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, cl client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				if _, ok := obj.(*backupsv1alpha1.BackupJob); ok && subResourceName == "status" {
					patchAttempts++
					if patchAttempts == 1 {
						return apierrors.NewConflict(gr, obj.GetName(),
							fmt.Errorf("simulated stale resourceVersion"))
					}
				}
				return cl.Status().Update(ctx, obj, opts...)
			},
		}).
		Build()
	r := &BackupJobReconciler{Client: c, Scheme: s}

	fresh := &backupsv1alpha1.BackupJob{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(job), fresh); err != nil {
		t.Fatalf("seed Get: %v", err)
	}

	res, err := r.requeueWithReason(context.Background(), fresh, "TestReason", "test message")
	if err != nil {
		t.Fatalf("requeueWithReason returned error despite retry budget; the 409 should have been absorbed: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("requeueWithReason must still requeue after a successful retry; got %+v", res)
	}
	if patchAttempts < 2 {
		t.Errorf("expected at least 2 Status Update attempts (one Conflict + one success), got %d", patchAttempts)
	}
	got := &backupsv1alpha1.BackupJob{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(job), got); err != nil {
		t.Fatalf("post-reconcile Get: %v", err)
	}
	cond := apimeta.FindStatusCondition(got.Status.Conditions, "Ready")
	if cond == nil {
		t.Fatalf("expected Ready condition to land after retry; got none")
	}
	if cond.Reason != "TestReason" {
		t.Errorf("Ready reason: got %q want %q", cond.Reason, "TestReason")
	}
}
