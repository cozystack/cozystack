// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	"github.com/cozystack/cozystack/internal/backupcontroller/mariadbapp"
	"github.com/cozystack/cozystack/internal/backupcontroller/mariadbtypes"
)

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

func TestValidateMariaDBApplicationRef(t *testing.T) {
	apps := mariadbapp.GroupName
	other := "other.example.com"
	cases := []struct {
		name    string
		ref     corev1.TypedLocalObjectReference
		wantErr bool
	}{
		{"happy path with apps group", corev1.TypedLocalObjectReference{Kind: "MariaDB", Name: "x", APIGroup: &apps}, false},
		{"empty apiGroup is accepted", corev1.TypedLocalObjectReference{Kind: "MariaDB", Name: "x"}, false},
		{"foreign apiGroup rejected", corev1.TypedLocalObjectReference{Kind: "MariaDB", Name: "x", APIGroup: &other}, true},
		{"wrong kind rejected", corev1.TypedLocalObjectReference{Kind: "Postgres", Name: "x", APIGroup: &apps}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateMariaDBApplicationRef(tc.ref)
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
// Storage builder
// ---------------------------------------------------------------------------

func TestBuildMariaDBBackupStorage_S3(t *testing.T) {
	tlsEnabled := true
	in := strategyv1alpha1.MariaDBStorageTemplate{
		S3: &strategyv1alpha1.MariaDBS3Template{
			Bucket:   "bkt",
			Endpoint: "s3.example:9000",
			Prefix:   "mariadb/",
			Region:   "us-east-1",
			AccessKeyIDSecretKeyRef: strategyv1alpha1.MariaDBSecretKeySelector{
				Name: "creds", Key: "AWS_ACCESS_KEY_ID",
			},
			SecretAccessKeySecretKeyRef: strategyv1alpha1.MariaDBSecretKeySelector{
				Name: "creds", Key: "AWS_SECRET_ACCESS_KEY",
			},
			TLS: &strategyv1alpha1.MariaDBS3TLS{
				Enabled: tlsEnabled,
				CASecretKeyRef: &strategyv1alpha1.MariaDBSecretKeySelector{
					Name: "ca", Key: "ca.crt",
				},
			},
		},
	}
	got, err := buildMariaDBBackupStorage(in)
	if err != nil {
		t.Fatalf("buildMariaDBBackupStorage: %v", err)
	}
	want := mariadbtypes.BackupStorage{
		S3: &mariadbtypes.S3Storage{
			Bucket:   "bkt",
			Endpoint: "s3.example:9000",
			Prefix:   "mariadb/",
			Region:   "us-east-1",
			AccessKeyIdSecretKeyRef:     mariadbtypes.SecretKeySelector{Name: "creds", Key: "AWS_ACCESS_KEY_ID"},
			SecretAccessKeySecretKeyRef: mariadbtypes.SecretKeySelector{Name: "creds", Key: "AWS_SECRET_ACCESS_KEY"},
			TLS: &mariadbtypes.S3TLS{
				Enabled:        true,
				CASecretKeyRef: &mariadbtypes.SecretKeySelector{Name: "ca", Key: "ca.crt"},
			},
		},
	}
	if !apiequality.Semantic.DeepEqual(got, want) {
		t.Fatalf("S3 storage mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestBuildMariaDBBackupStorage_PVC(t *testing.T) {
	pvc := &corev1.PersistentVolumeClaimSpec{
		AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
	}
	got, err := buildMariaDBBackupStorage(strategyv1alpha1.MariaDBStorageTemplate{
		PersistentVolumeClaim: pvc,
	})
	if err != nil {
		t.Fatalf("buildMariaDBBackupStorage: %v", err)
	}
	if got.PersistentVolumeClaim == nil || got.S3 != nil || got.Volume != nil {
		t.Fatalf("expected only PVC branch populated, got %#v", got)
	}
}

func TestBuildMariaDBBackupStorage_RejectsEmpty(t *testing.T) {
	if _, err := buildMariaDBBackupStorage(strategyv1alpha1.MariaDBStorageTemplate{}); err == nil {
		t.Fatalf("expected error for empty storage block, got nil")
	}
}

// TestBuildMariaDBBackupStorage_RejectsMultiple pins the defence-in-depth
// check against a strategy CR that names more than one storage branch. The
// CRD's XValidation rule rejects this at admission, but a strategy CR that
// pre-dates the rule (or one applied with --force or by an actor bypassing
// admission) must still surface a clean validation error instead of having
// the driver silently pick whichever branch comes first in source order.
func TestBuildMariaDBBackupStorage_RejectsMultiple(t *testing.T) {
	in := strategyv1alpha1.MariaDBStorageTemplate{
		S3:                    &strategyv1alpha1.MariaDBS3Template{Bucket: "b", Endpoint: "e"},
		PersistentVolumeClaim: &corev1.PersistentVolumeClaimSpec{},
	}
	if _, err := buildMariaDBBackupStorage(in); err == nil {
		t.Fatalf("expected error when both s3 and persistentVolumeClaim are set, got nil")
	}
}

// ---------------------------------------------------------------------------
// Templating
// ---------------------------------------------------------------------------

func TestRenderMariaDBTemplate_TemplatingApplicationName(t *testing.T) {
	tmpl := strategyv1alpha1.MariaDBTemplate{
		Storage: strategyv1alpha1.MariaDBStorageTemplate{
			S3: &strategyv1alpha1.MariaDBS3Template{
				Bucket:   "{{ .Parameters.bucket }}",
				Endpoint: "s3.example:9000",
				Prefix:   "{{ .Application.metadata.name }}/",
				AccessKeyIDSecretKeyRef: strategyv1alpha1.MariaDBSecretKeySelector{
					Name: "{{ .Application.metadata.name }}-creds", Key: "AWS_ACCESS_KEY_ID",
				},
				SecretAccessKeySecretKeyRef: strategyv1alpha1.MariaDBSecretKeySelector{
					Name: "{{ .Application.metadata.name }}-creds", Key: "AWS_SECRET_ACCESS_KEY",
				},
			},
		},
	}
	app := newMariaDBApp("mariadb-src", "tenant")
	got, err := renderMariaDBTemplate(tmpl, app, map[string]string{"bucket": "shared-bucket"})
	if err != nil {
		t.Fatalf("renderMariaDBTemplate: %v", err)
	}
	if got.Storage.S3.Bucket != "shared-bucket" {
		t.Errorf("Bucket parameter not templated: got %q", got.Storage.S3.Bucket)
	}
	if got.Storage.S3.Prefix != "mariadb-src/" {
		t.Errorf("Prefix not templated: got %q", got.Storage.S3.Prefix)
	}
	if got.Storage.S3.AccessKeyIDSecretKeyRef.Name != "mariadb-src-creds" {
		t.Errorf("AccessKey Secret name not templated: got %q", got.Storage.S3.AccessKeyIDSecretKeyRef.Name)
	}
}

// ---------------------------------------------------------------------------
// Backup-side ensure idempotency
// ---------------------------------------------------------------------------

func TestEnsureMariaDBBackup_IdempotentByLabel(t *testing.T) {
	c := newMariaDBStrategyTestClient(t)
	r := &BackupJobReconciler{Client: c, Scheme: c.Scheme()}
	job := newMariaDBBackupJob("bj-1", "tenant")
	if err := c.Create(context.Background(), job); err != nil {
		t.Fatalf("seed BackupJob: %v", err)
	}
	rendered := newRenderedMariaDBTemplate()

	first, err := r.ensureMariaDBBackup(context.Background(), job, rendered)
	if err != nil {
		t.Fatalf("first ensureMariaDBBackup: %v", err)
	}
	second, err := r.ensureMariaDBBackup(context.Background(), job, rendered)
	if err != nil {
		t.Fatalf("second ensureMariaDBBackup: %v", err)
	}
	if first.Name != second.Name {
		t.Errorf("expected idempotent reuse: first=%q second=%q", first.Name, second.Name)
	}

	list := &mariadbtypes.BackupList{}
	if err := c.List(context.Background(), list, client.InNamespace("tenant")); err != nil {
		t.Fatalf("list backups: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("expected exactly one operator Backup CR, got %d", len(list.Items))
	}
	got := &list.Items[0]
	// The cozystack mariadb ApplicationDefinition prefixes the release name
	// with "mariadb-", so a BackupJob targeting applicationRef.name=mariadb-src
	// is reconciled against k8s.mariadb.com/MariaDB named mariadb-mariadb-src.
	if got.Spec.MariaDBRef.Name != "mariadb-mariadb-src" {
		t.Errorf("MariaDBRef.Name: got %q want mariadb-mariadb-src", got.Spec.MariaDBRef.Name)
	}
	if got.Spec.Storage.S3 == nil || got.Spec.Storage.S3.Bucket != "bkt" {
		t.Errorf("storage S3 bucket: got %#v want bkt", got.Spec.Storage)
	}
	if got.Labels[backupsv1alpha1.OwningJobNameLabel] != "bj-1" {
		t.Errorf("OwningJobName label missing or wrong: %v", got.Labels)
	}
}

// ---------------------------------------------------------------------------
// Restore-side ensure idempotency + target rewrite
// ---------------------------------------------------------------------------

func TestEnsureMariaDBRestore_IdempotentByLabel(t *testing.T) {
	c := newMariaDBStrategyTestClient(t)
	r := &RestoreJobReconciler{Client: c, Scheme: c.Scheme()}
	rj := newMariaDBRestoreJob("rj-1", "tenant")
	if err := c.Create(context.Background(), rj); err != nil {
		t.Fatalf("seed RestoreJob: %v", err)
	}

	first, err := r.ensureMariaDBRestore(context.Background(), rj, "src-backup", "mariadb-target")
	if err != nil {
		t.Fatalf("first ensureMariaDBRestore: %v", err)
	}
	second, err := r.ensureMariaDBRestore(context.Background(), rj, "src-backup", "mariadb-target")
	if err != nil {
		t.Fatalf("second ensureMariaDBRestore: %v", err)
	}
	if first.Name != second.Name {
		t.Errorf("expected idempotent reuse: first=%q second=%q", first.Name, second.Name)
	}
	list := &mariadbtypes.RestoreList{}
	if err := c.List(context.Background(), list, client.InNamespace("tenant")); err != nil {
		t.Fatalf("list restores: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("expected exactly one operator Restore CR, got %d", len(list.Items))
	}
	got := &list.Items[0]
	if got.Spec.MariaDBRef.Name != "mariadb-target" {
		t.Errorf("MariaDBRef.Name: got %q want mariadb-target", got.Spec.MariaDBRef.Name)
	}
	if got.Spec.BackupRef == nil || got.Spec.BackupRef.Name != "src-backup" || got.Spec.BackupRef.Kind != mariadbtypes.BackupKind {
		t.Errorf("BackupRef mismatch: %#v", got.Spec.BackupRef)
	}
}

// ---------------------------------------------------------------------------
// resolveMariaDBRestoreTarget (in-place vs to-copy)
// ---------------------------------------------------------------------------

func TestResolveMariaDBRestoreTarget(t *testing.T) {
	apps := mariadbapp.GroupName
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "src-bk"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{
				Kind: "MariaDB", Name: "mariadb-src", APIGroup: &apps,
			},
		},
	}

	t.Run("in-place: missing targetApplicationRef inherits source", func(t *testing.T) {
		rj := &backupsv1alpha1.RestoreJob{Spec: backupsv1alpha1.RestoreJobSpec{}}
		r := &RestoreJobReconciler{}
		got := r.resolveMariaDBRestoreTarget(rj, backup)
		if got.AppName != "mariadb-src" {
			t.Errorf("in-place AppName: got %q want mariadb-src", got.AppName)
		}
		if got.Kind != "MariaDB" {
			t.Errorf("in-place Kind: got %q want MariaDB", got.Kind)
		}
	})

	t.Run("to-copy: targetApplicationRef wins over source", func(t *testing.T) {
		rj := &backupsv1alpha1.RestoreJob{
			Spec: backupsv1alpha1.RestoreJobSpec{
				TargetApplicationRef: &corev1.TypedLocalObjectReference{
					Kind: "MariaDB", Name: "mariadb-target", APIGroup: &apps,
				},
			},
		}
		r := &RestoreJobReconciler{}
		got := r.resolveMariaDBRestoreTarget(rj, backup)
		if got.AppName != "mariadb-target" {
			t.Errorf("to-copy AppName: got %q want mariadb-target", got.AppName)
		}
	})
}

// ---------------------------------------------------------------------------
// Snapshot persistence round-trip
// ---------------------------------------------------------------------------

func TestMarshalMariaDBBackupSnapshot_RoundTrip(t *testing.T) {
	rendered := newRenderedMariaDBTemplate()
	parameters := map[string]string{"region": "us-east-1"}
	raw, err := marshalMariaDBBackupSnapshot(rendered, parameters)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if raw == nil || len(raw.Raw) == 0 {
		t.Fatalf("expected non-empty RawExtension")
	}
	var snap mariadbBackupSnapshot
	if err := json.Unmarshal(raw.Raw, &snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if snap.Kind != mariadbBackupSnapshotKind {
		t.Errorf("snapshot kind: got %q want %q", snap.Kind, mariadbBackupSnapshotKind)
	}
	if snap.Storage == nil || snap.Storage.S3 == nil || snap.Storage.S3.Bucket != "bkt" {
		t.Errorf("snapshot storage: %#v", snap.Storage)
	}
	if got := snap.Parameters["region"]; got != "us-east-1" {
		t.Errorf("snapshot parameters: got %#v", snap.Parameters)
	}
}

// ---------------------------------------------------------------------------
// Restore options
// ---------------------------------------------------------------------------

func TestParseMariaDBRestoreOptions(t *testing.T) {
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
			got, err := parseMariaDBRestoreOptions(ext)
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

func TestMariaDBRestoreOptions_EffectiveDeadline(t *testing.T) {
	cases := []struct {
		name string
		opts MariaDBRestoreOptions
		want time.Duration
	}{
		{"unset", MariaDBRestoreOptions{}, mariadbDefaultRestoreDeadline},
		{"zero", MariaDBRestoreOptions{RestoreTimeoutSeconds: 0}, mariadbDefaultRestoreDeadline},
		{"negative", MariaDBRestoreOptions{RestoreTimeoutSeconds: -1}, mariadbDefaultRestoreDeadline},
		{"override", MariaDBRestoreOptions{RestoreTimeoutSeconds: 7200}, 2 * time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.opts.effectiveRestoreDeadline(); got != tc.want {
				t.Errorf("got %s want %s", got, tc.want)
			}
		})
	}
}

func TestMariaDBBackupDeadlineExceeded(t *testing.T) {
	if mariadbBackupDeadlineExceeded(nil) {
		t.Fatalf("nil StartedAt must not trip the gate (deadline starts when StartedAt is set)")
	}
	recent := metav1.NewTime(time.Now())
	if mariadbBackupDeadlineExceeded(&recent) {
		t.Fatalf("recent StartedAt must not trip the gate")
	}
	old := metav1.NewTime(time.Now().Add(-2 * mariadbDefaultBackupDeadline))
	if !mariadbBackupDeadlineExceeded(&old) {
		t.Fatalf("StartedAt past default deadline must trip the gate")
	}
}

// ---------------------------------------------------------------------------
// URI synthesis
// ---------------------------------------------------------------------------

func TestMariaDBBackupURI(t *testing.T) {
	rendered := &strategyv1alpha1.MariaDBTemplate{
		Storage: strategyv1alpha1.MariaDBStorageTemplate{
			S3: &strategyv1alpha1.MariaDBS3Template{
				Bucket: "bkt",
				Prefix: "mariadb-src",
			},
		},
	}
	mdb := &mariadbtypes.Backup{ObjectMeta: metav1.ObjectMeta{Name: "bj-1-abc"}}
	got := mariadbBackupURI(rendered, mdb)
	want := "s3://bkt/mariadb-src/bj-1-abc"
	if got != want {
		t.Errorf("URI: got %q want %q", got, want)
	}
}

func TestMariaDBBackupURI_NoS3IsEmpty(t *testing.T) {
	rendered := &strategyv1alpha1.MariaDBTemplate{
		Storage: strategyv1alpha1.MariaDBStorageTemplate{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimSpec{},
		},
	}
	if got := mariadbBackupURI(rendered, &mariadbtypes.Backup{}); got != "" {
		t.Fatalf("non-S3 storage must produce empty URI; got %q", got)
	}
}

// TestMariaDBBackupURI_EmptyPrefix pins the no-prefix S3 URI shape. With
// Prefix="" the helper must produce s3://<bucket>/<name> (no leading
// double slash), not s3://<bucket>//<name>. Without this, a future
// refactor that reorders the `%s/%s` formatting could regress
// silently because the only currently-exercised path uses a non-empty
// prefix.
func TestMariaDBBackupURI_EmptyPrefix(t *testing.T) {
	rendered := &strategyv1alpha1.MariaDBTemplate{
		Storage: strategyv1alpha1.MariaDBStorageTemplate{
			S3: &strategyv1alpha1.MariaDBS3Template{
				Bucket: "bkt",
				Prefix: "",
			},
		},
	}
	mdb := &mariadbtypes.Backup{ObjectMeta: metav1.ObjectMeta{Name: "bj-1-abc"}}
	got := mariadbBackupURI(rendered, mdb)
	want := "s3://bkt/bj-1-abc"
	if got != want {
		t.Errorf("URI: got %q want %q", got, want)
	}
}

// TestFindMariaDBBackupForJob_DuplicateMatches pins #9: when more than
// one operator-side Backup carries the same OwningJob label pair (a
// theoretical race between two controller replicas calling Create
// between the first list returning empty and the cache catching up), the
// function must still return a deterministic single result rather than
// panicking on multi-hit. The contract is "return list.Items[0]" - the
// caller's caches and the API list ordering are stable per-revision, so
// a steady stream of reconciles converges on the same pick.
func TestFindMariaDBBackupForJob_DuplicateMatches(t *testing.T) {
	job := newMariaDBBackupJob("bj-dup", "tenant")
	dup1 := &mariadbtypes.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "tenant", Name: "bj-dup-aaa",
			Labels: map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      job.Name,
				backupsv1alpha1.OwningJobNamespaceLabel: job.Namespace,
			},
		},
	}
	dup2 := &mariadbtypes.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "tenant", Name: "bj-dup-bbb",
			Labels: map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      job.Name,
				backupsv1alpha1.OwningJobNamespaceLabel: job.Namespace,
			},
		},
	}
	c := newMariaDBStrategyTestClient(t, job, dup1, dup2)
	r := &BackupJobReconciler{Client: c, Scheme: c.Scheme()}

	got, err := r.findMariaDBBackupForJob(context.Background(), job)
	if err != nil {
		t.Fatalf("findMariaDBBackupForJob: %v", err)
	}
	if got == nil {
		t.Fatalf("expected one Backup, got nil")
	}
	// fake client lists in name order; either name is a valid pick under
	// the contract, but the result must be one of the seeded duplicates,
	// not a synthesised empty.
	if got.Name != dup1.Name && got.Name != dup2.Name {
		t.Errorf("returned Backup name %q did not match any seeded duplicate (%q, %q)", got.Name, dup1.Name, dup2.Name)
	}

	// Double-call must return the same pick - the function is meant to be
	// idempotent under repeated reconciles even when duplicates exist.
	again, err := r.findMariaDBBackupForJob(context.Background(), job)
	if err != nil {
		t.Fatalf("second findMariaDBBackupForJob: %v", err)
	}
	if again == nil || again.Name != got.Name {
		t.Errorf("non-deterministic duplicate pick: first=%q second=%q", got.Name, name(again))
	}
}

func name(b *mariadbtypes.Backup) string {
	if b == nil {
		return "<nil>"
	}
	return b.Name
}

// TestCreateMariaDBBackupArtifact_ArtifactShape pins #8: the Cozystack
// Backup artefact gets Status.Artifact populated only when the rendered
// storage produces a stable URI (S3); for PVC/Volume the URI helper
// returns "" and the artefact should leave Status.Artifact nil rather
// than carry Artifact{URI: ""} which is a worse signal than no artefact
// at all.
func TestCreateMariaDBBackupArtifact_ArtifactShape(t *testing.T) {
	apps := mariadbapp.GroupName
	resolved := &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{
			Kind: "MariaDB", Name: "mariadb-strategy-default",
		},
		Parameters: map[string]string{},
	}
	mdbBackup := &mariadbtypes.Backup{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "src-op-bk"}}

	t.Run("S3 storage populates Artifact with URI", func(t *testing.T) {
		job := &backupsv1alpha1.BackupJob{
			ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj-s3"},
			Spec: backupsv1alpha1.BackupJobSpec{
				ApplicationRef: corev1.TypedLocalObjectReference{Kind: "MariaDB", Name: "src", APIGroup: &apps},
			},
		}
		c := newMariaDBStrategyTestClient(t, job)
		r := &BackupJobReconciler{Client: c, Scheme: c.Scheme()}
		rendered := newRenderedMariaDBTemplate()
		rendered.Storage.S3.Prefix = "src/"

		artefact, err := r.createMariaDBBackupArtifact(context.Background(), job, resolved, mdbBackup, rendered)
		if err != nil {
			t.Fatalf("createMariaDBBackupArtifact: %v", err)
		}
		if artefact.Status.Artifact == nil {
			t.Fatalf("S3 storage must populate Status.Artifact")
		}
		if artefact.Status.Artifact.URI == "" {
			t.Errorf("S3 storage Artifact.URI: got empty, want non-empty")
		}
	})

	t.Run("PVC storage leaves Artifact nil", func(t *testing.T) {
		job := &backupsv1alpha1.BackupJob{
			ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj-pvc"},
			Spec: backupsv1alpha1.BackupJobSpec{
				ApplicationRef: corev1.TypedLocalObjectReference{Kind: "MariaDB", Name: "src", APIGroup: &apps},
			},
		}
		c := newMariaDBStrategyTestClient(t, job)
		r := &BackupJobReconciler{Client: c, Scheme: c.Scheme()}
		rendered := &strategyv1alpha1.MariaDBTemplate{
			Storage: strategyv1alpha1.MariaDBStorageTemplate{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimSpec{},
			},
		}

		artefact, err := r.createMariaDBBackupArtifact(context.Background(), job, resolved, mdbBackup, rendered)
		if err != nil {
			t.Fatalf("createMariaDBBackupArtifact: %v", err)
		}
		if artefact.Status.Artifact != nil {
			t.Errorf("PVC storage must leave Status.Artifact nil; got %#v", artefact.Status.Artifact)
		}
	})
}

// TestReconcileMariaDBRestore_StartedAtPersistedContinuesInline pins the
// idempotency tweak: when StartedAt has already been persisted by an
// earlier reconcile (or a peer replica), the bootstrap branch must adopt
// it locally and continue executing the rest of the reconcile inline,
// not return early with RequeueAfter. Before the fix the function
// wasted a reconcile cycle by requeuing here even though everything
// downstream could run with the just-refetched state - the BackupJob
// path always continued inline, so the asymmetry was a small cold-path
// inefficiency surfaced in review. The test seeds an absent operator
// Backup so post-fix execution reaches the markRestoreJobFailed("reaped")
// terminal branch in the same call; pre-fix it would stop at the early
// Requeue and leave Phase unchanged.
func TestReconcileMariaDBRestore_StartedAtPersistedContinuesInline(t *testing.T) {
	apps := mariadbapp.GroupName
	pastStarted := metav1.NewTime(time.Now().Add(-1 * time.Minute))

	cozyBackup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "src-cozy-bk"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{
				Kind: "MariaDB", Name: "src", APIGroup: &apps,
			},
			DriverMetadata: map[string]string{mariadbBackupNameKey: "src-op-bk"},
		},
	}
	// API-server copy already has StartedAt persisted (and Phase=Running).
	// The in-memory copy passed into Reconcile has StartedAt=nil so it
	// must take the bootstrap branch, observe fresh.StartedAt set, and
	// continue. No operator Backup CR is seeded - post-fix the reconcile
	// reaches the "operator-side artifact has been reaped" terminal
	// failure in the same call.
	rjPersisted := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "rj-restart"},
		Spec: backupsv1alpha1.RestoreJobSpec{
			BackupRef: corev1.LocalObjectReference{Name: cozyBackup.Name},
			TargetApplicationRef: &corev1.TypedLocalObjectReference{
				Kind: "MariaDB", Name: "target", APIGroup: &apps,
			},
		},
		Status: backupsv1alpha1.RestoreJobStatus{
			StartedAt: &pastStarted,
			Phase:     backupsv1alpha1.RestoreJobPhaseRunning,
		},
	}
	c := newMariaDBStrategyTestClient(t, cozyBackup, rjPersisted)
	r := &RestoreJobReconciler{Client: c, Recorder: record.NewFakeRecorder(10)}

	// Refetch via the client so rjInMem inherits the ResourceVersion the
	// fake client assigned at seed time - otherwise a later
	// markRestoreJobFailed Update sees stale RV and the fake client
	// returns a 409 that has nothing to do with the behaviour under test.
	fetched := &backupsv1alpha1.RestoreJob{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: rjPersisted.Namespace, Name: rjPersisted.Name}, fetched); err != nil {
		t.Fatalf("seed Get: %v", err)
	}
	rjInMem := fetched.DeepCopy()
	rjInMem.Status.StartedAt = nil
	rjInMem.Status.Phase = ""

	if _, err := r.reconcileMariaDBRestore(context.Background(), rjInMem, cozyBackup); err != nil {
		t.Fatalf("reconcileMariaDBRestore: %v", err)
	}
	if rjInMem.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
		t.Errorf("reconcile must continue inline once StartedAt is observed persisted; expected Phase=%q (reaped-artefact terminal failure), got %q",
			backupsv1alpha1.RestoreJobPhaseFailed, rjInMem.Status.Phase)
	}
	cond := apimeta.FindStatusCondition(rjInMem.Status.Conditions, "Ready")
	if cond == nil || !strings.Contains(cond.Message, "reaped") {
		var msg string
		if cond != nil {
			msg = cond.Message
		}
		t.Errorf("expected Ready condition to carry the reaped-artefact message; got %q", msg)
	}
}

// TestReconcileMariaDBRestore_TargetMariaDBNotFoundIsTransient pins #4:
// the restore path treats a missing operator-side target MariaDB CR as
// transient (Ready=False/TargetMariaDBNotReady + requeue) instead of a
// terminal failure - mirroring the backup path. Before the fix a
// RestoreJob applied seconds before the target HelmRelease finished
// rendering would terminally fail and force the tenant to recreate it.
func TestReconcileMariaDBRestore_TargetMariaDBNotFoundIsTransient(t *testing.T) {
	apps := mariadbapp.GroupName
	now := metav1.Now()

	// Source backup the operator already created and the cozystack
	// artefact already references. Restore is gated on the operator
	// Backup CR still existing, so seed it in the fake client.
	srcOperatorBackup := &mariadbtypes.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "src-op-bk"},
	}
	cozyBackup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "src-cozy-bk"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{
				Kind: "MariaDB", Name: "src", APIGroup: &apps,
			},
			DriverMetadata: map[string]string{mariadbBackupNameKey: "src-op-bk"},
		},
	}
	rj := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "rj-target-missing"},
		Spec: backupsv1alpha1.RestoreJobSpec{
			BackupRef: corev1.LocalObjectReference{Name: cozyBackup.Name},
			TargetApplicationRef: &corev1.TypedLocalObjectReference{
				Kind: "MariaDB", Name: "target", APIGroup: &apps,
			},
		},
		Status: backupsv1alpha1.RestoreJobStatus{
			StartedAt: &now, // skip the bootstrap branch
		},
	}

	c := newMariaDBStrategyTestClient(t, srcOperatorBackup, cozyBackup, rj)
	r := &RestoreJobReconciler{Client: c, Recorder: record.NewFakeRecorder(10)}

	res, err := r.reconcileMariaDBRestore(context.Background(), rj, cozyBackup)
	if err != nil {
		t.Fatalf("reconcileMariaDBRestore: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("missing target MariaDB CR must produce transient requeue, got %+v", res)
	}
	if rj.Status.Phase == backupsv1alpha1.RestoreJobPhaseFailed {
		t.Errorf("missing target MariaDB CR must NOT mark RestoreJob Failed; got phase=%q", rj.Status.Phase)
	}
	cond := apimeta.FindStatusCondition(rj.Status.Conditions, "Ready")
	if cond == nil {
		t.Fatalf("expected Ready condition, got none")
	}
	if cond.Status != metav1.ConditionFalse || cond.Reason != "TargetMariaDBNotReady" {
		t.Errorf("expected Ready=False/TargetMariaDBNotReady, got %s/%s", cond.Status, cond.Reason)
	}
}

// TestReconcileMariaDBRestore_KindCheckedBeforeSrcBackupGet pins #7: the
// target Kind / APIGroup validation must run before any apiserver call
// so a malformed TargetApplicationRef fails fast with the Kind
// diagnostic, not with "operator-side artifact has been reaped" after a
// pointless srcBackup Get. The fake client deliberately does NOT carry
// the operator Backup CR; if validation got hoisted away by a refactor
// the test would observe the reaped-artefact failure instead of the
// Kind-rejection one.
func TestReconcileMariaDBRestore_KindCheckedBeforeSrcBackupGet(t *testing.T) {
	apps := mariadbapp.GroupName
	now := metav1.Now()

	cozyBackup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "src-cozy-bk"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{
				Kind: "MariaDB", Name: "src", APIGroup: &apps,
			},
			DriverMetadata: map[string]string{mariadbBackupNameKey: "src-op-bk"},
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
		Status: backupsv1alpha1.RestoreJobStatus{
			StartedAt: &now,
		},
	}

	c := newMariaDBStrategyTestClient(t, cozyBackup, rj)
	r := &RestoreJobReconciler{Client: c, Recorder: record.NewFakeRecorder(10)}

	if _, err := r.reconcileMariaDBRestore(context.Background(), rj, cozyBackup); err != nil {
		t.Fatalf("reconcileMariaDBRestore: %v", err)
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
	if strings.Contains(cond.Message, "reaped") {
		t.Errorf("validation must run before srcBackup Get; got reaped-artefact message instead: %q", cond.Message)
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newMariaDBStrategyTestClient returns a fake client.Client wired up with
// the schemes the MariaDB-strategy reconciler needs.
func newMariaDBStrategyTestClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	_ = backupsv1alpha1.AddToScheme(s)
	_ = strategyv1alpha1.AddToScheme(s)
	_ = mariadbtypes.AddToScheme(s)
	_ = mariadbapp.AddToScheme(s)
	return clientfake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		WithStatusSubresource(&backupsv1alpha1.BackupJob{}, &backupsv1alpha1.RestoreJob{}, &backupsv1alpha1.Backup{}).
		Build()
}

// newMariaDBApp builds a typed apps.cozystack.io MariaDB CR for tests.
func newMariaDBApp(name, namespace string) *mariadbapp.MariaDB {
	return &mariadbapp.MariaDB{
		TypeMeta: metav1.TypeMeta{
			APIVersion: mariadbapp.GroupVersion.String(),
			Kind:       mariadbapp.Kind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
}

func newMariaDBBackupJob(name, namespace string) *backupsv1alpha1.BackupJob {
	apps := mariadbapp.GroupName
	return &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{
				Kind: "MariaDB", Name: "mariadb-src", APIGroup: &apps,
			},
		},
	}
}

func newMariaDBRestoreJob(name, namespace string) *backupsv1alpha1.RestoreJob {
	return &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: backupsv1alpha1.RestoreJobSpec{
			BackupRef: corev1.LocalObjectReference{Name: "src-backup"},
		},
	}
}

func newRenderedMariaDBTemplate() *strategyv1alpha1.MariaDBTemplate {
	return &strategyv1alpha1.MariaDBTemplate{
		Storage: strategyv1alpha1.MariaDBStorageTemplate{
			S3: &strategyv1alpha1.MariaDBS3Template{
				Bucket:   "bkt",
				Endpoint: "s3.example:9000",
				AccessKeyIDSecretKeyRef: strategyv1alpha1.MariaDBSecretKeySelector{
					Name: "creds", Key: "AWS_ACCESS_KEY_ID",
				},
				SecretAccessKeySecretKeyRef: strategyv1alpha1.MariaDBSecretKeySelector{
					Name: "creds", Key: "AWS_SECRET_ACCESS_KEY",
				},
			},
		},
		Compression: "gzip",
	}
}
