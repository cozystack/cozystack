// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"encoding/json"
	"errors"
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
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	"github.com/cozystack/cozystack/internal/backupcontroller/etcdapp"
	"github.com/cozystack/cozystack/internal/backupcontroller/etcdtypes"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func newEtcdTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	_ = backupsv1alpha1.AddToScheme(s)
	_ = strategyv1alpha1.AddToScheme(s)
	_ = etcdapp.AddToScheme(s)
	_ = etcdtypes.AddToScheme(s)
	return s
}

func newEtcdTestClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	return clientfake.NewClientBuilder().
		WithScheme(newEtcdTestScheme(t)).
		WithObjects(objs...).
		WithStatusSubresource(
			&backupsv1alpha1.BackupJob{},
			&backupsv1alpha1.RestoreJob{},
			&backupsv1alpha1.Backup{},
			&etcdtypes.EtcdCluster{},
			&etcdtypes.EtcdBackup{},
		).
		Build()
}

func ptrBool(v bool) *bool { return &v }

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

func TestValidateEtcdApplicationRef(t *testing.T) {
	apps := etcdapp.GroupName
	other := "other.example.com"
	cases := []struct {
		name    string
		ref     corev1.TypedLocalObjectReference
		wantErr bool
	}{
		{"happy path with apps group", corev1.TypedLocalObjectReference{Kind: "Etcd", Name: "x", APIGroup: &apps}, false},
		{"empty apiGroup accepted", corev1.TypedLocalObjectReference{Kind: "Etcd", Name: "x"}, false},
		{"foreign apiGroup rejected", corev1.TypedLocalObjectReference{Kind: "Etcd", Name: "x", APIGroup: &other}, true},
		{"wrong kind rejected", corev1.TypedLocalObjectReference{Kind: "Postgres", Name: "x", APIGroup: &apps}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateEtcdApplicationRef(tc.ref)
			if tc.wantErr && err == nil {
				t.Fatalf("expected validation error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}
		})
	}
}

func TestValidateRenderedEtcdDestination(t *testing.T) {
	good := strategyv1alpha1.EtcdDestinationTemplate{
		S3: &strategyv1alpha1.EtcdS3Template{
			Bucket:               "b",
			Endpoint:             "https://e",
			CredentialsSecretRef: strategyv1alpha1.EtcdLocalObjectReference{Name: "creds"},
		},
	}
	if err := validateRenderedEtcdDestination(good); err != nil {
		t.Fatalf("good S3: %v", err)
	}

	cases := []struct {
		name string
		in   strategyv1alpha1.EtcdDestinationTemplate
	}{
		{"empty rejected", strategyv1alpha1.EtcdDestinationTemplate{}},
		{"both rejected", strategyv1alpha1.EtcdDestinationTemplate{
			S3: good.S3, PVC: &strategyv1alpha1.EtcdPVCTemplate{ClaimName: "x"},
		}},
		{"missing bucket", strategyv1alpha1.EtcdDestinationTemplate{S3: &strategyv1alpha1.EtcdS3Template{
			Endpoint: "https://e",
			CredentialsSecretRef: strategyv1alpha1.EtcdLocalObjectReference{Name: "creds"},
		}}},
		{"missing endpoint", strategyv1alpha1.EtcdDestinationTemplate{S3: &strategyv1alpha1.EtcdS3Template{
			Bucket: "b",
			CredentialsSecretRef: strategyv1alpha1.EtcdLocalObjectReference{Name: "creds"},
		}}},
		{"missing credsRef name", strategyv1alpha1.EtcdDestinationTemplate{S3: &strategyv1alpha1.EtcdS3Template{
			Bucket: "b", Endpoint: "https://e",
		}}},
		{"pvc no claimName", strategyv1alpha1.EtcdDestinationTemplate{
			PVC: &strategyv1alpha1.EtcdPVCTemplate{},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateRenderedEtcdDestination(tc.in); err == nil {
				t.Fatalf("expected error")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Templating
// ---------------------------------------------------------------------------

func TestRenderEtcdTemplate_AppName(t *testing.T) {
	// The rendered template walks every string field and substitutes Go
	// templates against .Application / .Parameters. Pin the canonical
	// pattern (per-app credentials Secret name keyed off the app name)
	// so a future refactor that drops templating from CredentialsSecretRef
	// fails this test.
	app := &etcdapp.Etcd{ObjectMeta: metav1.ObjectMeta{Name: "etcd-src", Namespace: "tenant-root"}}
	in := strategyv1alpha1.EtcdTemplate{
		Destination: strategyv1alpha1.EtcdDestinationTemplate{
			S3: &strategyv1alpha1.EtcdS3Template{
				Bucket:   "{{ .Parameters.bucket }}",
				Endpoint: "https://s3.example.invalid",
				Key:      "{{ .Application.metadata.name }}/",
				CredentialsSecretRef: strategyv1alpha1.EtcdLocalObjectReference{
					Name: "{{ .Application.metadata.name }}-etcd-backup-creds",
				},
			},
		},
	}
	rendered, err := renderEtcdTemplate(in, app, map[string]string{"bucket": "etcd-backups"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if rendered.Destination.S3.Bucket != "etcd-backups" {
		t.Errorf("bucket: got %q want etcd-backups", rendered.Destination.S3.Bucket)
	}
	if rendered.Destination.S3.Key != "etcd-src/" {
		t.Errorf("key: got %q want etcd-src/", rendered.Destination.S3.Key)
	}
	if rendered.Destination.S3.CredentialsSecretRef.Name != "etcd-src-etcd-backup-creds" {
		t.Errorf("credentialsSecretRef.Name: got %q want etcd-src-etcd-backup-creds",
			rendered.Destination.S3.CredentialsSecretRef.Name)
	}
}

// ---------------------------------------------------------------------------
// strategyToEtcdBackupDestination shape-cast
// ---------------------------------------------------------------------------

func TestStrategyToEtcdBackupDestination_S3(t *testing.T) {
	in := strategyv1alpha1.EtcdDestinationTemplate{
		S3: &strategyv1alpha1.EtcdS3Template{
			Bucket:               "b",
			Endpoint:             "https://e",
			Key:                  "etcd-src/",
			Region:               "us-east-1",
			ForcePathStyle:       ptrBool(true),
			CredentialsSecretRef: strategyv1alpha1.EtcdLocalObjectReference{Name: "creds"},
		},
	}
	out, err := strategyToEtcdBackupDestination(in)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if out.S3 == nil || out.PVC != nil {
		t.Fatalf("expected S3 set, PVC nil; got s3=%v pvc=%v", out.S3, out.PVC)
	}
	if out.S3.Bucket != "b" || out.S3.Endpoint != "https://e" || out.S3.Key != "etcd-src/" ||
		out.S3.Region != "us-east-1" || out.S3.CredentialsSecretRef.Name != "creds" {
		t.Errorf("shape cast: %+v", out.S3)
	}
	if out.S3.ForcePathStyle == nil || !*out.S3.ForcePathStyle {
		t.Errorf("force path style not propagated: %v", out.S3.ForcePathStyle)
	}
	// Pin pointer-independence so a future refactor that aliases the
	// strategy pointer into the operator type doesn't silently couple
	// downstream mutations back into the strategy CR cache.
	*in.S3.ForcePathStyle = false
	if !*out.S3.ForcePathStyle {
		t.Errorf("output should be independent of input pointer")
	}
}

// ---------------------------------------------------------------------------
// Snapshot encode/decode contract
// ---------------------------------------------------------------------------

func TestEtcdBackupSnapshot_RoundTrip(t *testing.T) {
	rendered := &strategyv1alpha1.EtcdTemplate{
		Destination: strategyv1alpha1.EtcdDestinationTemplate{
			S3: &strategyv1alpha1.EtcdS3Template{
				Bucket:               "b",
				Endpoint:             "https://e",
				Key:                  "etcd-src/",
				ForcePathStyle:       ptrBool(true),
				CredentialsSecretRef: strategyv1alpha1.EtcdLocalObjectReference{Name: "creds"},
			},
		},
	}
	raw, err := marshalEtcdBackupSnapshot(rendered, map[string]string{"bucket": "etcd-backups"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	snap, err := decodeEtcdBackupSnapshot(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if snap == nil || snap.Destination.S3 == nil || snap.Destination.S3.Bucket != "b" {
		t.Fatalf("round-trip lost data: %+v", snap)
	}
	if snap.Parameters["bucket"] != "etcd-backups" {
		t.Errorf("parameters not persisted: %v", snap.Parameters)
	}
}

func TestEtcdBackupSnapshot_UnrecognisedKindIsTerminal(t *testing.T) {
	bad := map[string]any{
		"kind":        "SomethingElse",
		"apiVersion":  etcdBackupSnapshotAPIVersion,
		"destination": map[string]any{"s3": map[string]any{"bucket": "b", "endpoint": "e", "credentialsSecretRef": map[string]any{"name": "c"}}},
	}
	raw, _ := json.Marshal(bad)
	_, err := decodeEtcdBackupSnapshot(&runtime.RawExtension{Raw: raw})
	if !errors.Is(err, errEtcdSnapshotUnrecognised) {
		t.Fatalf("expected errEtcdSnapshotUnrecognised, got %v", err)
	}
}

func TestEtcdBackupSnapshot_MalformedJSONIsRecoverable(t *testing.T) {
	_, err := decodeEtcdBackupSnapshot(&runtime.RawExtension{Raw: []byte("{not json")})
	if err == nil || errors.Is(err, errEtcdSnapshotUnrecognised) {
		t.Fatalf("expected recoverable decode error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// resolveEtcdRestoreDestination: snapshot wins, driverMetadata falls back
// ---------------------------------------------------------------------------

func TestResolveEtcdRestoreDestination_PrefersSnapshot(t *testing.T) {
	snap := &etcdBackupSnapshot{
		Destination: strategyv1alpha1.EtcdDestinationTemplate{
			S3: &strategyv1alpha1.EtcdS3Template{
				Bucket:               "from-snap",
				Endpoint:             "https://snap",
				CredentialsSecretRef: strategyv1alpha1.EtcdLocalObjectReference{Name: "creds-snap"},
			},
		},
	}
	backup := &backupsv1alpha1.Backup{Spec: backupsv1alpha1.BackupSpec{DriverMetadata: map[string]string{
		etcdBackupBucketKey:      "from-md",
		etcdBackupEndpointKey:    "https://md",
		etcdBackupCredsSecretKey: "creds-md",
	}}}
	d, ok := resolveEtcdRestoreDestination(backup, snap)
	if !ok || d.S3 == nil {
		t.Fatalf("expected S3 destination, got %+v ok=%v", d, ok)
	}
	if d.S3.Bucket != "from-snap" || d.S3.CredentialsSecretRef.Name != "creds-snap" {
		t.Errorf("snapshot should win; got %+v", d.S3)
	}
}

func TestResolveEtcdRestoreDestination_FallsBackToDriverMetadata(t *testing.T) {
	backup := &backupsv1alpha1.Backup{Spec: backupsv1alpha1.BackupSpec{DriverMetadata: map[string]string{
		etcdBackupBucketKey:         "from-md",
		etcdBackupEndpointKey:       "https://md",
		etcdBackupCredsSecretKey:    "creds-md",
		etcdBackupForcePathStyleKey: "true",
	}}}
	d, ok := resolveEtcdRestoreDestination(backup, nil)
	if !ok || d.S3 == nil {
		t.Fatalf("expected S3 destination, got %+v ok=%v", d, ok)
	}
	if d.S3.Bucket != "from-md" || d.S3.CredentialsSecretRef.Name != "creds-md" {
		t.Errorf("driverMetadata fallback incorrect: %+v", d.S3)
	}
	if d.S3.ForcePathStyle == nil || !*d.S3.ForcePathStyle {
		t.Errorf("ForcePathStyle should parse as true: %v", d.S3.ForcePathStyle)
	}
}

func TestResolveEtcdRestoreDestination_MissingEverythingFails(t *testing.T) {
	if _, ok := resolveEtcdRestoreDestination(&backupsv1alpha1.Backup{}, nil); ok {
		t.Fatalf("expected !ok for empty backup")
	}
}

// TestResolveEtcdRestoreDestination_FullS3KeyForRestore pins the operator
// filename convention. The backup-side persists the strategy-rendered
// prefix (e.g. "etcd/") because internal/controller/factory/backup_job.go
// appends "<backupName>.db" when writing. The restore-agent reads
// S3_KEY verbatim, so the driver must rebuild "<prefix>/<backupName>.db"
// at restore-resolve time. A regression here surfaces in production as
// "downloaded snapshot is empty (0 bytes)" - the agent fetches the
// directory marker for the prefix.
func TestResolveEtcdRestoreDestination_FullS3KeyForRestore(t *testing.T) {
	t.Run("snapshot prefix gets backup-name appended", func(t *testing.T) {
		snap := &etcdBackupSnapshot{
			Destination: strategyv1alpha1.EtcdDestinationTemplate{
				S3: &strategyv1alpha1.EtcdS3Template{
					Bucket:               "b",
					Endpoint:             "https://e",
					Key:                  "etcd/",
					CredentialsSecretRef: strategyv1alpha1.EtcdLocalObjectReference{Name: "creds"},
				},
			},
		}
		backup := &backupsv1alpha1.Backup{Spec: backupsv1alpha1.BackupSpec{
			DriverMetadata: map[string]string{etcdBackupNameKey: "etcd-bk-abc123"},
		}}
		d, ok := resolveEtcdRestoreDestination(backup, snap)
		if !ok || d.S3 == nil {
			t.Fatalf("expected S3 destination, got %+v ok=%v", d, ok)
		}
		if d.S3.Key != "etcd/etcd-bk-abc123.db" {
			t.Errorf("S3 restore key: got %q want %q", d.S3.Key, "etcd/etcd-bk-abc123.db")
		}
	})
	t.Run("driverMetadata fallback also gets the filename", func(t *testing.T) {
		backup := &backupsv1alpha1.Backup{Spec: backupsv1alpha1.BackupSpec{DriverMetadata: map[string]string{
			etcdBackupBucketKey:      "b",
			etcdBackupEndpointKey:    "https://e",
			etcdBackupKeyKey:         "etcd/",
			etcdBackupCredsSecretKey: "creds",
			etcdBackupNameKey:        "etcd-bk-def456",
		}}}
		d, ok := resolveEtcdRestoreDestination(backup, nil)
		if !ok || d.S3 == nil {
			t.Fatalf("expected S3 destination, got %+v ok=%v", d, ok)
		}
		if d.S3.Key != "etcd/etcd-bk-def456.db" {
			t.Errorf("S3 restore key: got %q want %q", d.S3.Key, "etcd/etcd-bk-def456.db")
		}
	})
}

func TestBuildEtcdRestoreS3Key(t *testing.T) {
	cases := []struct {
		prefix string
		name   string
		want   string
	}{
		{"", "bk", "bk.db"},
		{"etcd/", "bk", "etcd/bk.db"},
		{"etcd", "bk", "etcd/bk.db"},
		{"etcd/backups/", "bk", "etcd/backups/bk.db"},
		{"etcd/", "", "etcd/"}, // empty backup-name → prefix verbatim (legacy artefact)
	}
	for _, tc := range cases {
		if got := buildEtcdRestoreS3Key(tc.prefix, tc.name); got != tc.want {
			t.Errorf("buildEtcdRestoreS3Key(%q, %q): got %q want %q", tc.prefix, tc.name, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// ensureEtcdBackup idempotency: same labels => reuse, not duplicate
// ---------------------------------------------------------------------------

func TestEnsureEtcdBackup_LabelIdempotency(t *testing.T) {
	bj := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-root", Name: "bj-1"},
	}
	c := newEtcdTestClient(t, bj)
	r := &BackupJobReconciler{Client: c, Scheme: newEtcdTestScheme(t)}

	rendered := &strategyv1alpha1.EtcdTemplate{
		Destination: strategyv1alpha1.EtcdDestinationTemplate{
			S3: &strategyv1alpha1.EtcdS3Template{
				Bucket:               "b",
				Endpoint:             "https://e",
				CredentialsSecretRef: strategyv1alpha1.EtcdLocalObjectReference{Name: "creds"},
			},
		},
	}

	first, err := r.ensureEtcdBackup(context.Background(), bj, rendered)
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	second, err := r.ensureEtcdBackup(context.Background(), bj, rendered)
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if first.Name != second.Name {
		t.Errorf("expected idempotent reuse; got %q vs %q", first.Name, second.Name)
	}

	// And there's exactly one EtcdBackup in the namespace.
	list := &etcdtypes.EtcdBackupList{}
	if err := c.List(context.Background(), list, client.InNamespace(bj.Namespace)); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 1 {
		t.Errorf("expected 1 EtcdBackup, got %d", len(list.Items))
	}
}

func TestEnsureEtcdBackup_NoOwnerReference(t *testing.T) {
	// Same contract as the FoundationDB driver: per-BackupJob CRs must
	// NOT carry an OwnerReference back to the BackupJob, because a
	// kubectl delete && kubectl apply with the same BackupJob name has
	// to reuse the prior operator CR via OwningJob labels. An owner ref
	// would let Kubernetes GC reap the operator CR on parent deletion
	// and defeat the reuse contract.
	bj := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-root", Name: "bj-1", UID: "abc"},
	}
	c := newEtcdTestClient(t, bj)
	r := &BackupJobReconciler{Client: c, Scheme: newEtcdTestScheme(t)}

	rendered := &strategyv1alpha1.EtcdTemplate{
		Destination: strategyv1alpha1.EtcdDestinationTemplate{
			S3: &strategyv1alpha1.EtcdS3Template{
				Bucket:               "b",
				Endpoint:             "https://e",
				CredentialsSecretRef: strategyv1alpha1.EtcdLocalObjectReference{Name: "creds"},
			},
		},
	}
	eb, err := r.ensureEtcdBackup(context.Background(), bj, rendered)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if len(eb.OwnerReferences) != 0 {
		t.Errorf("EtcdBackup must not carry an OwnerReference; got %+v", eb.OwnerReferences)
	}
}

// ---------------------------------------------------------------------------
// createEtcdBackupArtifact: pass-through from EtcdBackup.status.snapshot
// ---------------------------------------------------------------------------

// TestCreateEtcdBackupArtifact_PassesSnapshotThrough pins the contract
// between this driver and the upstream etcd-operator change that
// introduced EtcdBackup.status.snapshot (URI / SizeBytes / Checksum).
// The cozystack Backup.status.artifact must mirror those fields, NOT
// synthesise a URI from the spec destination (the rendered destination
// is the prefix, not the final key — the agent rewrites the suffix at
// write time, see the upstream BackupSnapshot godoc).
func TestCreateEtcdBackupArtifact_PassesSnapshotThrough(t *testing.T) {
	apps := etcdapp.GroupName
	bj := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-root", Name: "bj-1"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{Kind: "Etcd", Name: "etcd", APIGroup: &apps},
		},
	}
	c := newEtcdTestClient(t, bj)
	r := &BackupJobReconciler{Client: c, Scheme: newEtcdTestScheme(t)}

	eb := &etcdtypes.EtcdBackup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-root", Name: "bj-1-abcde"},
		Status: etcdtypes.EtcdBackupStatus{
			Phase: etcdtypes.BackupPhaseComplete,
			Snapshot: &etcdtypes.EtcdBackupSnapshot{
				URI:       "s3://my-bucket/etcd/bj-1-abcde-rev42.db",
				SizeBytes: 20512,
				Checksum:  "sha256:abcd1234",
			},
		},
	}
	rendered := &strategyv1alpha1.EtcdTemplate{
		Destination: strategyv1alpha1.EtcdDestinationTemplate{
			S3: &strategyv1alpha1.EtcdS3Template{
				Bucket:               "my-bucket",
				Endpoint:             "https://s3.example",
				Key:                  "etcd/",
				CredentialsSecretRef: strategyv1alpha1.EtcdLocalObjectReference{Name: "creds"},
			},
		},
	}
	resolved := &ResolvedBackupConfig{StrategyRef: corev1.TypedLocalObjectReference{
		Kind: "Etcd", Name: "etcd-strategy-default",
	}}

	backup, err := r.createEtcdBackupArtifact(context.Background(), bj, resolved, eb, rendered)
	if err != nil {
		t.Fatalf("createEtcdBackupArtifact: %v", err)
	}
	if backup.Status.Artifact == nil {
		t.Fatal("status.artifact: got nil, want pass-through")
	}
	if backup.Status.Artifact.URI != eb.Status.Snapshot.URI {
		t.Errorf("artifact.URI: got %q want %q", backup.Status.Artifact.URI, eb.Status.Snapshot.URI)
	}
	if backup.Status.Artifact.SizeBytes != eb.Status.Snapshot.SizeBytes {
		t.Errorf("artifact.SizeBytes: got %d want %d", backup.Status.Artifact.SizeBytes, eb.Status.Snapshot.SizeBytes)
	}
	if backup.Status.Artifact.Checksum != eb.Status.Snapshot.Checksum {
		t.Errorf("artifact.Checksum: got %q want %q", backup.Status.Artifact.Checksum, eb.Status.Snapshot.Checksum)
	}
}

// TestCreateEtcdBackupArtifact_NoSnapshotLeavesArtifactNil pins the
// older-operator fallback path: when EtcdBackup.status.snapshot is nil
// (operator pre-dates the upstream feature), the driver must leave
// Backup.status.artifact unset. Synthesising a URI from spec.destination
// would point at the prefix, not the actual object the agent wrote —
// strictly worse than nothing.
func TestCreateEtcdBackupArtifact_NoSnapshotLeavesArtifactNil(t *testing.T) {
	apps := etcdapp.GroupName
	bj := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-root", Name: "bj-2"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{Kind: "Etcd", Name: "etcd", APIGroup: &apps},
		},
	}
	c := newEtcdTestClient(t, bj)
	r := &BackupJobReconciler{Client: c, Scheme: newEtcdTestScheme(t)}

	eb := &etcdtypes.EtcdBackup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-root", Name: "bj-2-xyz"},
		Status: etcdtypes.EtcdBackupStatus{
			Phase: etcdtypes.BackupPhaseComplete,
			// Snapshot is nil — older operator version.
		},
	}
	rendered := &strategyv1alpha1.EtcdTemplate{
		Destination: strategyv1alpha1.EtcdDestinationTemplate{
			S3: &strategyv1alpha1.EtcdS3Template{
				Bucket:               "b",
				Endpoint:             "https://e",
				CredentialsSecretRef: strategyv1alpha1.EtcdLocalObjectReference{Name: "creds"},
			},
		},
	}
	resolved := &ResolvedBackupConfig{StrategyRef: corev1.TypedLocalObjectReference{
		Kind: "Etcd", Name: "etcd-strategy-default",
	}}

	backup, err := r.createEtcdBackupArtifact(context.Background(), bj, resolved, eb, rendered)
	if err != nil {
		t.Fatalf("createEtcdBackupArtifact: %v", err)
	}
	if backup.Status.Artifact != nil {
		t.Errorf("status.artifact: got %+v, want nil (older operator without snapshot field)", backup.Status.Artifact)
	}
}

// ---------------------------------------------------------------------------
// RestoreJob: to-copy rejection
// ---------------------------------------------------------------------------

func TestReconcileEtcdRestore_RejectsToCopy(t *testing.T) {
	apps := etcdapp.GroupName
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-root", Name: "bk"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{Kind: "Etcd", Name: "etcd", APIGroup: &apps},
		},
	}
	rj := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-root", Name: "rj"},
		Spec: backupsv1alpha1.RestoreJobSpec{
			BackupRef:            corev1.LocalObjectReference{Name: "bk"},
			TargetApplicationRef: &corev1.TypedLocalObjectReference{Kind: "Etcd", Name: "etcd-dst", APIGroup: &apps},
		},
	}
	c := newEtcdTestClient(t, backup, rj)
	r := &RestoreJobReconciler{Client: c, Scheme: newEtcdTestScheme(t), Recorder: record.NewFakeRecorder(10)}

	if _, err := r.reconcileEtcdRestore(context.Background(), rj, backup); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got := &backupsv1alpha1.RestoreJob{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(rj), got); err != nil {
		t.Fatalf("refetch: %v", err)
	}
	if got.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
		t.Errorf("expected Failed, got %q", got.Status.Phase)
	}
	if !strings.Contains(got.Status.Message, "to-copy") {
		t.Errorf("message should explain the limitation; got %q", got.Status.Message)
	}
}

// ---------------------------------------------------------------------------
// RestoreJob options parsing
// ---------------------------------------------------------------------------

func TestParseEtcdRestoreOptions(t *testing.T) {
	t.Run("empty options falls back to default deadline", func(t *testing.T) {
		o, err := parseEtcdRestoreOptions(nil)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if got := o.effectiveRestoreDeadline(); got != etcdDefaultRestoreDeadline {
			t.Errorf("default deadline: got %v want %v", got, etcdDefaultRestoreDeadline)
		}
	})
	t.Run("malformed JSON is terminal", func(t *testing.T) {
		_, err := parseEtcdRestoreOptions(&runtime.RawExtension{Raw: []byte("{nope")})
		if err == nil {
			t.Fatalf("expected parse error")
		}
	})
	t.Run("override propagates", func(t *testing.T) {
		o, err := parseEtcdRestoreOptions(&runtime.RawExtension{Raw: []byte(`{"restoreTimeoutSeconds":120}`)})
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if d := o.effectiveRestoreDeadline(); d.Seconds() != 120 {
			t.Errorf("deadline override: got %v want 120s", d)
		}
	})
}

// ---------------------------------------------------------------------------
// etcdClusterReady gates on the operator-side Ready condition
// ---------------------------------------------------------------------------

func TestEtcdClusterReady(t *testing.T) {
	t.Run("nil cluster is not ready", func(t *testing.T) {
		if etcdClusterReady(nil) {
			t.Error("nil ready?")
		}
	})
	t.Run("no conditions is not ready", func(t *testing.T) {
		c := &etcdtypes.EtcdCluster{}
		if etcdClusterReady(c) {
			t.Error("no-conditions ready?")
		}
	})
	t.Run("Ready=False is not ready", func(t *testing.T) {
		c := &etcdtypes.EtcdCluster{}
		apimeta.SetStatusCondition(&c.Status.Conditions, metav1.Condition{
			Type: etcdtypes.ClusterConditionReady, Status: metav1.ConditionFalse, Reason: "Bootstrapping",
		})
		if etcdClusterReady(c) {
			t.Error("Ready=False reported ready")
		}
	})
	t.Run("Ready=True is ready", func(t *testing.T) {
		c := &etcdtypes.EtcdCluster{}
		apimeta.SetStatusCondition(&c.Status.Conditions, metav1.Condition{
			Type: etcdtypes.ClusterConditionReady, Status: metav1.ConditionTrue, Reason: "Ok",
		})
		if !etcdClusterReady(c) {
			t.Error("Ready=True not reported ready")
		}
	})
}

// ---------------------------------------------------------------------------
// BackupJob terminal paths
// ---------------------------------------------------------------------------

func TestReconcileEtcd_RejectsWrongKind(t *testing.T) {
	apps := etcdapp.GroupName
	bj := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-root", Name: "bj"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef:  corev1.TypedLocalObjectReference{Kind: "Postgres", Name: "x", APIGroup: &apps},
			BackupClassName: "etcd-default",
		},
	}
	c := newEtcdTestClient(t, bj)
	r := &BackupJobReconciler{Client: c, Scheme: newEtcdTestScheme(t), Recorder: record.NewFakeRecorder(10)}
	resolved := &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{Kind: "Etcd", Name: "etcd-strategy-default", APIGroup: &apps},
	}
	if _, err := r.reconcileEtcd(context.Background(), bj, resolved); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got := &backupsv1alpha1.BackupJob{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(bj), got); err != nil {
		t.Fatalf("refetch: %v", err)
	}
	if got.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
		t.Errorf("expected Failed, got %q (msg=%q)", got.Status.Phase, got.Status.Message)
	}
}

func TestReconcileEtcd_StrategyNotFoundIsTerminal(t *testing.T) {
	apps := etcdapp.GroupName
	now := metav1.Now()
	bj := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-root", Name: "bj"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef:  corev1.TypedLocalObjectReference{Kind: "Etcd", Name: "etcd", APIGroup: &apps},
			BackupClassName: "etcd-default",
		},
		Status: backupsv1alpha1.BackupJobStatus{StartedAt: &now},
	}
	c := newEtcdTestClient(t, bj)
	r := &BackupJobReconciler{Client: c, Scheme: newEtcdTestScheme(t), Recorder: record.NewFakeRecorder(10)}
	apiGroup := "strategy.backups.cozystack.io"
	strategyRef := corev1.TypedLocalObjectReference{
		Kind:     "Etcd",
		Name:     "missing-strategy",
		APIGroup: &apiGroup,
	}
	resolved := &ResolvedBackupConfig{StrategyRef: strategyRef}
	if _, err := r.reconcileEtcd(context.Background(), bj, resolved); err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("reconcile: %v", err)
	}
	got := &backupsv1alpha1.BackupJob{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(bj), got); err != nil {
		t.Fatalf("refetch: %v", err)
	}
	if got.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
		t.Errorf("expected Failed, got %q", got.Status.Phase)
	}
}

// ---------------------------------------------------------------------------
// latestEtcdBackupConditionMessage prefers failure-shaped conditions
// ---------------------------------------------------------------------------

// TestLatestEtcdBackupConditionMessage_PrefersFailedCondition pins the
// behaviour that a Failed/Ready=False condition wins over a later
// housekeeping update. Without this gate the upstream operator's
// post-failure status reconcile (e.g. flipping Started=False on the
// pod-gone path) would shadow the actual failure reason and tenants
// would see a misleading message on a Cozystack BackupJob.
func TestLatestEtcdBackupConditionMessage_PrefersFailedCondition(t *testing.T) {
	failedAt := metav1.NewTime(time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC))
	laterAt := metav1.NewTime(failedAt.Add(5 * time.Minute))

	t.Run("Failed condition wins over later housekeeping", func(t *testing.T) {
		eb := &etcdtypes.EtcdBackup{
			Status: etcdtypes.EtcdBackupStatus{
				Conditions: []metav1.Condition{
					{Type: "Failed", Status: metav1.ConditionTrue, Reason: "UploadFailed",
						Message: "upload to s3://x failed: AccessDenied", LastTransitionTime: failedAt},
					{Type: "Started", Status: metav1.ConditionFalse, Reason: "JobGone",
						Message: "job pod garbage collected", LastTransitionTime: laterAt},
				},
			},
		}
		got := latestEtcdBackupConditionMessage(eb)
		if !strings.Contains(got, "AccessDenied") {
			t.Errorf("expected failure message, got %q", got)
		}
	})

	t.Run("Ready=False also counts as failure-shaped", func(t *testing.T) {
		eb := &etcdtypes.EtcdBackup{
			Status: etcdtypes.EtcdBackupStatus{
				Conditions: []metav1.Condition{
					{Type: etcdtypes.ClusterConditionReady, Status: metav1.ConditionFalse, Reason: "Backoff",
						Message: "etcdctl exited 1: tls handshake timeout", LastTransitionTime: failedAt},
					{Type: "Started", Status: metav1.ConditionFalse, Reason: "Idle",
						Message: "idle", LastTransitionTime: laterAt},
				},
			},
		}
		got := latestEtcdBackupConditionMessage(eb)
		if !strings.Contains(got, "tls handshake") {
			t.Errorf("expected Ready=False message to be preferred, got %q", got)
		}
	})

	t.Run("falls back to latest when no failure-shaped condition exists", func(t *testing.T) {
		eb := &etcdtypes.EtcdBackup{
			Status: etcdtypes.EtcdBackupStatus{
				Conditions: []metav1.Condition{
					{Type: "Started", Status: metav1.ConditionTrue, Reason: "Started",
						Message: "snapshot in progress", LastTransitionTime: failedAt},
					{Type: "Uploaded", Status: metav1.ConditionTrue, Reason: "Done",
						Message: "uploaded 20512 bytes", LastTransitionTime: laterAt},
				},
			},
		}
		got := latestEtcdBackupConditionMessage(eb)
		if !strings.Contains(got, "uploaded") {
			t.Errorf("expected latest-by-time fallback, got %q", got)
		}
	})

	t.Run("nil and empty are empty string", func(t *testing.T) {
		if latestEtcdBackupConditionMessage(nil) != "" {
			t.Error("nil should produce empty string")
		}
		if latestEtcdBackupConditionMessage(&etcdtypes.EtcdBackup{}) != "" {
			t.Error("empty conditions should produce empty string")
		}
	})
}

// ---------------------------------------------------------------------------
// requeueBackupJobWithReason flips phase to Running
// ---------------------------------------------------------------------------

// TestRequeueBackupJobWithReason_SetsRunningPhase pins the contract that
// the first observable iteration (e.g. waiting for the source EtcdCluster
// to reach Ready) sets Phase=Running so tenants tailing
// BackupJob.status.phase see activity instead of an empty string while
// the condition spells out the wait reason.
func TestRequeueBackupJobWithReason_SetsRunningPhase(t *testing.T) {
	bj := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj"},
	}
	c := newEtcdTestClient(t, bj)
	r := &BackupJobReconciler{Client: c, Scheme: newEtcdTestScheme(t)}
	if _, err := r.requeueBackupJobWithReason(context.Background(), bj, "EtcdClusterNotReady", "waiting"); err != nil {
		t.Fatalf("requeue: %v", err)
	}
	got := &backupsv1alpha1.BackupJob{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(bj), got); err != nil {
		t.Fatalf("refetch: %v", err)
	}
	if got.Status.Phase != backupsv1alpha1.BackupJobPhaseRunning {
		t.Errorf("expected phase=Running on first observable iteration, got %q", got.Status.Phase)
	}
	cond := apimeta.FindStatusCondition(got.Status.Conditions, "Ready")
	if cond == nil || cond.Reason != "EtcdClusterNotReady" {
		t.Errorf("expected Ready=False/EtcdClusterNotReady condition, got %+v", cond)
	}
}

// ---------------------------------------------------------------------------
// RestoreJob: captured-spec on Condition.Message size cap
// ---------------------------------------------------------------------------

// testEtcdDynamicScheme returns a runtime.Scheme registered ONLY with
// the unstructured GVKs the restore path operates on through the
// dynamic client. Kept separate from newEtcdTestScheme (which carries
// the typed EtcdCluster) so the dynamic fake doesn't double-register
// the EtcdCluster GVK and panic.
func testEtcdDynamicScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	s.AddKnownTypeWithName(schema.GroupVersionKind{Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmRelease"}, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(schema.GroupVersionKind{Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmReleaseList"}, &unstructured.UnstructuredList{})
	s.AddKnownTypeWithName(schema.GroupVersionKind{Group: etcdtypes.GroupName, Version: etcdtypes.Version, Kind: "EtcdCluster"}, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(schema.GroupVersionKind{Group: etcdtypes.GroupName, Version: etcdtypes.Version, Kind: "EtcdClusterList"}, &unstructured.UnstructuredList{})
	return s
}

// newOversizedEtcdCluster fabricates an unstructured EtcdCluster whose
// spec marshals to > etcdRestoreCapturedSpecMaxBytes. Used to exercise
// the fail-fast cap on Condition.Message overflow.
func newOversizedEtcdCluster(namespace string, bytes int) *unstructured.Unstructured {
	bloat := make([]string, 0, bytes/16)
	for len(bloat)*16 < bytes {
		bloat = append(bloat, strings.Repeat("a", 15))
	}
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: etcdtypes.GroupName, Version: etcdtypes.Version, Kind: "EtcdCluster"})
	u.SetNamespace(namespace)
	u.SetName(etcdClusterName)
	_ = unstructured.SetNestedStringSlice(u.Object, bloat, "spec", "_bloat")
	_ = unstructured.SetNestedField(u.Object, int64(3), "spec", "replicas")
	return u
}

// TestReconcileEtcdRestore_OversizedSpecFailsFastBeforePurge pins the
// fail-fast guard on Condition.Message overflow. If the captured spec
// would exceed etcdRestoreCapturedSpecMaxBytes the destructive purge
// MUST NOT start - the spec couldn't be durably persisted, so any crash
// between snapshot and recreate would leave the RestoreJob stuck with
// the cluster gone and the spec lost forever.
func TestReconcileEtcdRestore_OversizedSpecFailsFastBeforePurge(t *testing.T) {
	const ns = "tenant"
	apps := etcdapp.GroupName
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "bk"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{Kind: "Etcd", Name: "etcd", APIGroup: &apps},
			DriverMetadata: map[string]string{
				etcdBackupBucketKey:      "b",
				etcdBackupEndpointKey:    "https://e",
				etcdBackupCredsSecretKey: "creds",
			},
		},
	}
	now := metav1.Now()
	rj := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "rj"},
		Spec:       backupsv1alpha1.RestoreJobSpec{BackupRef: corev1.LocalObjectReference{Name: "bk"}},
		Status:     backupsv1alpha1.RestoreJobStatus{StartedAt: &now},
	}

	cluster := newOversizedEtcdCluster(ns, etcdRestoreCapturedSpecMaxBytes*2)
	dyn := dynamicfake.NewSimpleDynamicClient(testEtcdDynamicScheme(t), cluster)
	c := newEtcdTestClient(t, backup, rj)
	r := &RestoreJobReconciler{Client: c, Interface: dyn, Scheme: newEtcdTestScheme(t), Recorder: record.NewFakeRecorder(10)}

	if _, err := r.reconcileEtcdRestore(context.Background(), rj, backup); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got := &backupsv1alpha1.RestoreJob{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(rj), got); err != nil {
		t.Fatalf("refetch: %v", err)
	}
	if got.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
		t.Fatalf("expected Failed phase, got %q (msg=%q)", got.Status.Phase, got.Status.Message)
	}
	if !strings.Contains(got.Status.Message, "exceeds") {
		t.Errorf("failure message should explain the cap; got %q", got.Status.Message)
	}
	// The cluster must still exist - we must not have started the purge.
	if _, err := dyn.Resource(etcdClusterGVR).Namespace(ns).Get(context.Background(), etcdClusterName, metav1.GetOptions{}); err != nil {
		t.Errorf("oversized-spec gate fired AFTER deletion (cluster gone: %v); fail-fast contract broken", err)
	}
}

// TestReconcileEtcdRestore_EmptySpecFailsBeforePurge pins the empty-spec
// gate: a live EtcdCluster with an empty spec is the corrupted state the
// driver cannot recover from. The RestoreJob must terminate WITHOUT
// touching the cluster (deletion would lose the only reference to the
// chart-rendered fields needed for recreation).
func TestReconcileEtcdRestore_EmptySpecFailsBeforePurge(t *testing.T) {
	const ns = "tenant"
	apps := etcdapp.GroupName
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "bk"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{Kind: "Etcd", Name: "etcd", APIGroup: &apps},
			DriverMetadata: map[string]string{
				etcdBackupBucketKey:      "b",
				etcdBackupEndpointKey:    "https://e",
				etcdBackupCredsSecretKey: "creds",
			},
		},
	}
	now := metav1.Now()
	rj := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "rj"},
		Spec:       backupsv1alpha1.RestoreJobSpec{BackupRef: corev1.LocalObjectReference{Name: "bk"}},
		Status:     backupsv1alpha1.RestoreJobStatus{StartedAt: &now},
	}

	empty := &unstructured.Unstructured{}
	empty.SetGroupVersionKind(schema.GroupVersionKind{Group: etcdtypes.GroupName, Version: etcdtypes.Version, Kind: "EtcdCluster"})
	empty.SetNamespace(ns)
	empty.SetName(etcdClusterName)
	// spec deliberately absent / empty.

	dyn := dynamicfake.NewSimpleDynamicClient(testEtcdDynamicScheme(t), empty)
	c := newEtcdTestClient(t, backup, rj)
	r := &RestoreJobReconciler{Client: c, Interface: dyn, Scheme: newEtcdTestScheme(t), Recorder: record.NewFakeRecorder(10)}

	if _, err := r.reconcileEtcdRestore(context.Background(), rj, backup); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got := &backupsv1alpha1.RestoreJob{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(rj), got); err != nil {
		t.Fatalf("refetch: %v", err)
	}
	if got.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
		t.Fatalf("expected Failed phase on empty spec, got %q", got.Status.Phase)
	}
	if !strings.Contains(got.Status.Message, "empty spec") {
		t.Errorf("failure message should name the empty-spec condition; got %q", got.Status.Message)
	}
	if _, err := dyn.Resource(etcdClusterGVR).Namespace(ns).Get(context.Background(), etcdClusterName, metav1.GetOptions{}); err != nil {
		t.Errorf("empty-spec gate must not delete the cluster; got: %v", err)
	}
}

// TestReconcileEtcdRestore_CrashRecoveryAdvancesWhenSpecCaptured pins the
// crash-safety branch: if the controller crashed AFTER capturing the
// spec on a Condition.Message and AFTER deleting the EtcdCluster but
// BEFORE stamping TargetPurged, the next reconcile sees NotFound on the
// dynamic Get. Without specCaptured we'd treat that as "tenant never
// deployed the source" and terminate; with specCaptured we treat it as
// "the previous iteration already purged" and advance the state machine.
func TestReconcileEtcdRestore_CrashRecoveryAdvancesWhenSpecCaptured(t *testing.T) {
	const ns = "tenant"
	apps := etcdapp.GroupName
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "bk"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{Kind: "Etcd", Name: "etcd", APIGroup: &apps},
			DriverMetadata: map[string]string{
				etcdBackupBucketKey:      "b",
				etcdBackupEndpointKey:    "https://e",
				etcdBackupCredsSecretKey: "creds",
			},
		},
	}
	now := metav1.Now()
	rj := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "rj"},
		Spec:       backupsv1alpha1.RestoreJobSpec{BackupRef: corev1.LocalObjectReference{Name: "bk"}},
		Status: backupsv1alpha1.RestoreJobStatus{
			StartedAt: &now,
			Conditions: []metav1.Condition{
				{
					Type:               etcdRestoreCondClusterSpecCaptured,
					Status:             metav1.ConditionTrue,
					Reason:             "SpecCaptured",
					Message:            `{"replicas":3,"storage":{"volumeClaimTemplate":{}}}`,
					LastTransitionTime: now,
				},
			},
		},
	}

	dyn := dynamicfake.NewSimpleDynamicClient(testEtcdDynamicScheme(t)) // no cluster -> NotFound
	c := newEtcdTestClient(t, backup, rj)
	r := &RestoreJobReconciler{Client: c, Interface: dyn, Scheme: newEtcdTestScheme(t), Recorder: record.NewFakeRecorder(10)}

	if _, err := r.reconcileEtcdRestore(context.Background(), rj, backup); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got := &backupsv1alpha1.RestoreJob{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(rj), got); err != nil {
		t.Fatalf("refetch: %v", err)
	}
	if got.Status.Phase == backupsv1alpha1.RestoreJobPhaseFailed {
		t.Fatalf("expected reconcile to advance (NOT Failed); got Failed with msg=%q", got.Status.Message)
	}
	cond := apimeta.FindStatusCondition(got.Status.Conditions, etcdRestoreCondTargetPurged)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Errorf("expected TargetPurged=True (crash-recovery advance); got %+v", cond)
	}
	if cond != nil && cond.Reason != "ClusterPurgedRecovered" {
		t.Errorf("expected Reason=ClusterPurgedRecovered to flag the crash-recovery path; got %q", cond.Reason)
	}
}

// TestReconcileEtcdRestore_MissingClusterWithoutCaptureIsTerminal pins
// the other side of the disambiguation: a tenant who fires a RestoreJob
// without ever having deployed the source app must NOT have the
// controller fall through to the recreate phase (which would create an
// EtcdCluster with an empty captured spec). The job must terminate.
func TestReconcileEtcdRestore_MissingClusterWithoutCaptureIsTerminal(t *testing.T) {
	const ns = "tenant"
	apps := etcdapp.GroupName
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "bk"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{Kind: "Etcd", Name: "etcd", APIGroup: &apps},
			DriverMetadata: map[string]string{
				etcdBackupBucketKey:      "b",
				etcdBackupEndpointKey:    "https://e",
				etcdBackupCredsSecretKey: "creds",
			},
		},
	}
	now := metav1.Now()
	rj := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "rj"},
		Spec:       backupsv1alpha1.RestoreJobSpec{BackupRef: corev1.LocalObjectReference{Name: "bk"}},
		Status:     backupsv1alpha1.RestoreJobStatus{StartedAt: &now},
	}

	dyn := dynamicfake.NewSimpleDynamicClient(testEtcdDynamicScheme(t)) // no cluster + no capture
	c := newEtcdTestClient(t, backup, rj)
	r := &RestoreJobReconciler{Client: c, Interface: dyn, Scheme: newEtcdTestScheme(t), Recorder: record.NewFakeRecorder(10)}

	if _, err := r.reconcileEtcdRestore(context.Background(), rj, backup); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got := &backupsv1alpha1.RestoreJob{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(rj), got); err != nil {
		t.Fatalf("refetch: %v", err)
	}
	if got.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
		t.Errorf("expected Failed when source cluster never existed; got %q", got.Status.Phase)
	}
}

// ---------------------------------------------------------------------------
// HR-resume on terminal failure inside the destructive window
// (without this gate, helm-controller is frozen on the Etcd app forever)
// ---------------------------------------------------------------------------

// suspendedHelmRelease fabricates an unstructured HelmRelease with the
// requested spec.suspend value, under the GVK the driver mutates.
func suspendedHelmRelease(namespace, name string, suspend bool) *unstructured.Unstructured {
	hr := &unstructured.Unstructured{}
	hr.SetGroupVersionKind(schema.GroupVersionKind{Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmRelease"})
	hr.SetNamespace(namespace)
	hr.SetName(name)
	_ = unstructured.SetNestedMap(hr.Object, map[string]interface{}{"suspend": suspend}, "spec")
	return hr
}

func hrSuspended(t *testing.T, dyn dynamic.Interface, namespace, name string) bool {
	t.Helper()
	hr, err := dyn.Resource(helmReleaseGVR).Namespace(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get HR: %v", err)
	}
	got, _, _ := unstructured.NestedBool(hr.Object, "spec", "suspend")
	return got
}

// TestReconcileEtcdRestore_ReadyDeadlineResumesHR pins the most likely
// production failure path: the recreated EtcdCluster does NOT reach
// Ready within restoreTimeoutSeconds (e.g. snapshot download stuck, S3
// creds wrong, PVC provisioner slow). Without this gate, the driver
// flips the RestoreJob Failed and leaves spec.suspend=true on the
// HelmRelease, freezing helm-controller on the Etcd app indefinitely —
// the only manual recovery is `kubectl patch hr/etcd -p
// '{"spec":{"suspend":false}}'`.
//
// Setup walks the state machine to the Ready-poll branch:
//   - EtcdCluster exists (bootstrap.restore stamped, not Ready),
//   - pre-seeded TargetPurged AND EtcdClusterSpecCaptured so the
//     destructive path is skipped on this reconcile,
//   - HR is already suspended (simulating that a prior reconcile ran
//     the destructive window),
//   - StartedAt is far enough in the past that the deadline trips.
func TestReconcileEtcdRestore_ReadyDeadlineResumesHR(t *testing.T) {
	const ns = "tenant"
	apps := etcdapp.GroupName
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "bk"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{Kind: "Etcd", Name: "etcd", APIGroup: &apps},
			DriverMetadata: map[string]string{
				etcdBackupBucketKey:      "b",
				etcdBackupEndpointKey:    "https://e",
				etcdBackupCredsSecretKey: "creds",
			},
		},
	}
	long := metav1.NewTime(time.Now().Add(-2 * etcdDefaultRestoreDeadline))
	rj := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "rj"},
		Spec:       backupsv1alpha1.RestoreJobSpec{BackupRef: corev1.LocalObjectReference{Name: "bk"}},
		Status: backupsv1alpha1.RestoreJobStatus{
			StartedAt: &long,
			Conditions: []metav1.Condition{
				{Type: etcdRestoreCondClusterSpecCaptured, Status: metav1.ConditionTrue, Reason: "SpecCaptured",
					Message: `{"replicas":3,"storage":{"volumeClaimTemplate":{}}}`, LastTransitionTime: long},
				{Type: etcdRestoreCondTargetPurged, Status: metav1.ConditionTrue, Reason: "ClusterPurged",
					Message: "purged", LastTransitionTime: long},
			},
		},
	}

	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(schema.GroupVersionKind{Group: etcdtypes.GroupName, Version: etcdtypes.Version, Kind: "EtcdCluster"})
	cluster.SetNamespace(ns)
	cluster.SetName(etcdClusterName)
	_ = unstructured.SetNestedField(cluster.Object, int64(3), "spec", "replicas")

	hr := suspendedHelmRelease(ns, etcdClusterName, true)
	dyn := dynamicfake.NewSimpleDynamicClient(testEtcdDynamicScheme(t), cluster, hr)

	// typed-client EtcdCluster (no Ready=True condition) so the Ready-poll branch trips the deadline.
	typedCluster := &etcdtypes.EtcdCluster{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: etcdClusterName}}
	c := newEtcdTestClient(t, backup, rj, typedCluster)
	r := &RestoreJobReconciler{Client: c, Interface: dyn, Scheme: newEtcdTestScheme(t), Recorder: record.NewFakeRecorder(10)}

	if _, err := r.reconcileEtcdRestore(context.Background(), rj, backup); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got := &backupsv1alpha1.RestoreJob{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(rj), got); err != nil {
		t.Fatalf("refetch: %v", err)
	}
	if got.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
		t.Fatalf("expected Failed phase on Ready deadline; got %q (msg=%q)", got.Status.Phase, got.Status.Message)
	}
	if !strings.Contains(got.Status.Message, "did not reach Ready") {
		t.Errorf("failure message should name the Ready deadline; got %q", got.Status.Message)
	}
	if hrSuspended(t, dyn, ns, etcdClusterName) {
		t.Fatalf("HelmRelease must NOT remain suspended after terminal Ready-deadline failure - helm-controller would be frozen on the Etcd app")
	}
}

// TestReconcileEtcdRestore_RecoverSpecErrorResumesHR pins the
// readCapturedEtcdClusterSpec failure branch: if the captured-spec
// condition is wiped (admission webhook corruption, manual edit, ...)
// after TargetPurged is set, the driver terminates the RestoreJob -
// and must resume the HR before doing so, because the destructive
// purge already happened on a prior iteration.
func TestReconcileEtcdRestore_RecoverSpecErrorResumesHR(t *testing.T) {
	const ns = "tenant"
	apps := etcdapp.GroupName
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "bk"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{Kind: "Etcd", Name: "etcd", APIGroup: &apps},
			DriverMetadata: map[string]string{
				etcdBackupBucketKey:      "b",
				etcdBackupEndpointKey:    "https://e",
				etcdBackupCredsSecretKey: "creds",
			},
		},
	}
	now := metav1.Now()
	rj := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "rj"},
		Spec:       backupsv1alpha1.RestoreJobSpec{BackupRef: corev1.LocalObjectReference{Name: "bk"}},
		Status: backupsv1alpha1.RestoreJobStatus{
			StartedAt: &now,
			// TargetPurged set but EtcdClusterSpecCaptured deliberately missing.
			Conditions: []metav1.Condition{
				{Type: etcdRestoreCondTargetPurged, Status: metav1.ConditionTrue, Reason: "ClusterPurged",
					Message: "purged", LastTransitionTime: now},
			},
		},
	}

	hr := suspendedHelmRelease(ns, etcdClusterName, true)
	dyn := dynamicfake.NewSimpleDynamicClient(testEtcdDynamicScheme(t), hr) // no EtcdCluster -> recreate branch
	c := newEtcdTestClient(t, backup, rj)
	r := &RestoreJobReconciler{Client: c, Interface: dyn, Scheme: newEtcdTestScheme(t), Recorder: record.NewFakeRecorder(10)}

	if _, err := r.reconcileEtcdRestore(context.Background(), rj, backup); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got := &backupsv1alpha1.RestoreJob{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(rj), got); err != nil {
		t.Fatalf("refetch: %v", err)
	}
	if got.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
		t.Fatalf("expected Failed phase; got %q", got.Status.Phase)
	}
	if hrSuspended(t, dyn, ns, etcdClusterName) {
		t.Fatalf("HelmRelease must be resumed on recover-spec failure; got suspended=true")
	}
}

// ---------------------------------------------------------------------------
// etcdClusterFullyGone gates HR-resume until cluster + PVCs disappear
// ---------------------------------------------------------------------------

// TestEtcdClusterFullyGone covers the wait-for-purge helper. Returning
// early on a still-terminating cluster lets the recreate step race the
// finalizer drain and the new Create races on PVC name collision.
func TestEtcdClusterFullyGone(t *testing.T) {
	const ns = "tenant"

	t.Run("cluster + PVCs absent -> gone=true", func(t *testing.T) {
		c := newEtcdTestClient(t)
		r := &RestoreJobReconciler{Client: c}
		gone, err := r.etcdClusterFullyGone(context.Background(), ns)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !gone {
			t.Fatalf("expected gone=true with no cluster and no PVCs")
		}
	})

	t.Run("live cluster -> gone=false", func(t *testing.T) {
		cluster := &etcdtypes.EtcdCluster{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: etcdClusterName}}
		c := newEtcdTestClient(t, cluster)
		r := &RestoreJobReconciler{Client: c}
		gone, err := r.etcdClusterFullyGone(context.Background(), ns)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if gone {
			t.Fatalf("expected gone=false when cluster still exists")
		}
	})

	t.Run("cluster gone but member PVC remains -> gone=false", func(t *testing.T) {
		// The etcd-operator's StatefulSet-style volumeClaimTemplate names
		// PVCs etcd-data-etcd-<i> and labels them with
		// app.kubernetes.io/instance=etcd. Pin the label match: if the
		// operator changes the label the restore will silently race the
		// still-draining PVC.
		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ns,
				Name:      "etcd-data-etcd-0",
				Labels:    map[string]string{"app.kubernetes.io/instance": etcdClusterName},
			},
		}
		c := newEtcdTestClient(t, pvc)
		r := &RestoreJobReconciler{Client: c}
		gone, err := r.etcdClusterFullyGone(context.Background(), ns)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if gone {
			t.Fatalf("expected gone=false while member PVC still exists")
		}
	})
}

// ---------------------------------------------------------------------------
// setEtcdRestoreHRSuspended toggles spec.suspend on the HelmRelease
// ---------------------------------------------------------------------------

// TestSetEtcdRestoreHRSuspended mirrors the CNPG driver's tests on the
// same helper. The destructive purge must suspend helm-controller before
// deleting the live EtcdCluster (otherwise Helm re-renders a
// bootstrap-less cluster on its next sync and races the restore) and
// resume it after the new cluster is Ready. A missing HR is treated as a
// no-op.
func TestSetEtcdRestoreHRSuspended(t *testing.T) {
	const (
		ns   = "tenant"
		name = "etcd"
	)
	mkHR := func(suspended *bool) *unstructured.Unstructured {
		hr := &unstructured.Unstructured{}
		hr.SetGroupVersionKind(schema.GroupVersionKind{Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmRelease"})
		hr.SetNamespace(ns)
		hr.SetName(name)
		spec := map[string]interface{}{}
		if suspended != nil {
			spec["suspend"] = *suspended
		}
		_ = unstructured.SetNestedMap(hr.Object, spec, "spec")
		return hr
	}
	suspendedField := func(t *testing.T, dyn dynamic.Interface) bool {
		t.Helper()
		hr, err := dyn.Resource(helmReleaseGVR).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get HR: %v", err)
		}
		got, _, _ := unstructured.NestedBool(hr.Object, "spec", "suspend")
		return got
	}

	t.Run("flips spec.suspend false -> true", func(t *testing.T) {
		falsePtr := false
		dyn := dynamicfake.NewSimpleDynamicClient(testEtcdDynamicScheme(t), mkHR(&falsePtr))
		r := &RestoreJobReconciler{Interface: dyn}
		if err := r.setEtcdRestoreHRSuspended(context.Background(), ns, name, true); err != nil {
			t.Fatalf("suspend: %v", err)
		}
		if !suspendedField(t, dyn) {
			t.Fatalf("expected spec.suspend=true after suspend call")
		}
	})

	t.Run("flips spec.suspend true -> false", func(t *testing.T) {
		truePtr := true
		dyn := dynamicfake.NewSimpleDynamicClient(testEtcdDynamicScheme(t), mkHR(&truePtr))
		r := &RestoreJobReconciler{Interface: dyn}
		if err := r.setEtcdRestoreHRSuspended(context.Background(), ns, name, false); err != nil {
			t.Fatalf("resume: %v", err)
		}
		if suspendedField(t, dyn) {
			t.Fatalf("expected spec.suspend=false after resume call")
		}
	})

	t.Run("idempotent when already at desired state", func(t *testing.T) {
		truePtr := true
		dyn := dynamicfake.NewSimpleDynamicClient(testEtcdDynamicScheme(t), mkHR(&truePtr))
		r := &RestoreJobReconciler{Interface: dyn}
		if err := r.setEtcdRestoreHRSuspended(context.Background(), ns, name, true); err != nil {
			t.Fatalf("idempotent suspend: %v", err)
		}
		if !suspendedField(t, dyn) {
			t.Fatalf("expected spec.suspend to stay true")
		}
	})

	t.Run("missing HR returns nil (no-op)", func(t *testing.T) {
		// A tenant deleting the Etcd app mid-restore must not strand the
		// driver on a NotFound; the destructive flow should proceed and
		// the next reconcile will note the missing HR.
		dyn := dynamicfake.NewSimpleDynamicClient(testEtcdDynamicScheme(t))
		r := &RestoreJobReconciler{Interface: dyn}
		if err := r.setEtcdRestoreHRSuspended(context.Background(), ns, name, true); err != nil {
			t.Fatalf("missing HR should be no-op: %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// EtcdRestoreOptions: negative override falls back to default deadline
// ---------------------------------------------------------------------------

// TestEffectiveRestoreDeadline_NegativeFallsBack pins the contract on
// negative inputs. A tenant submitting restoreTimeoutSeconds=-1 (or any
// non-positive value) must get the default deadline rather than a
// zero/negative deadline that would mark the RestoreJob Failed on its
// very next poll.
func TestEffectiveRestoreDeadline_NegativeFallsBack(t *testing.T) {
	for _, in := range []int64{-1, -3600, 0} {
		o := EtcdRestoreOptions{RestoreTimeoutSeconds: in}
		if got := o.effectiveRestoreDeadline(); got != etcdDefaultRestoreDeadline {
			t.Errorf("RestoreTimeoutSeconds=%d: deadline=%v want %v", in, got, etcdDefaultRestoreDeadline)
		}
	}
}
