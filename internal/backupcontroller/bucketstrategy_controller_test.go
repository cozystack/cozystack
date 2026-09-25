// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	"github.com/cozystack/cozystack/internal/backupcontroller/buckettypes"
)

func TestDeriveAccessClassName(t *testing.T) {
	cases := []struct {
		name      string
		className string
		readonly  bool
		want      string
	}{
		{"readwrite plain", "seaweedfs", false, "seaweedfs"},
		{"readonly plain", "seaweedfs", true, "seaweedfs-readonly"},
		{"readwrite lock stripped", "seaweedfs-lock", false, "seaweedfs"},
		{"readonly lock stripped", "seaweedfs-lock", true, "seaweedfs-readonly"},
		{"readwrite pool", "seaweedfs-hdd", false, "seaweedfs-hdd"},
		{"readonly pool lock", "seaweedfs-hdd-lock", true, "seaweedfs-hdd-readonly"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveAccessClassName(tc.className, tc.readonly); got != tc.want {
				t.Fatalf("deriveAccessClassName(%q, %v) = %q, want %q", tc.className, tc.readonly, got, tc.want)
			}
		})
	}
}

func TestJoinRepoPrefix(t *testing.T) {
	cases := []struct {
		base, segment, want string
	}{
		{"tenant-x/app/", "backup-1", "tenant-x/app/backup-1/"},
		{"tenant-x/app", "backup-1", "tenant-x/app/backup-1/"},
		{"/tenant-x/app/", "/backup-1/", "tenant-x/app/backup-1/"},
		{"", "backup-1", "backup-1/"},
		{"tenant-x/app/", "", "tenant-x/app/"},
		{"", "", ""},
	}
	for _, tc := range cases {
		if got := joinRepoPrefix(tc.base, tc.segment); got != tc.want {
			t.Fatalf("joinRepoPrefix(%q, %q) = %q, want %q", tc.base, tc.segment, got, tc.want)
		}
	}
}

func TestValidateBucketApplicationRef(t *testing.T) {
	group := backupsv1alpha1.DefaultApplicationAPIGroup
	other := "example.com"
	cases := []struct {
		name    string
		ref     corev1.TypedLocalObjectReference
		wantErr bool
	}{
		{"kind + default group", corev1.TypedLocalObjectReference{Kind: "Bucket", APIGroup: &group}, false},
		{"kind + empty group", corev1.TypedLocalObjectReference{Kind: "Bucket"}, false},
		{"wrong kind", corev1.TypedLocalObjectReference{Kind: "Postgres"}, true},
		{"wrong group", corev1.TypedLocalObjectReference{Kind: "Bucket", APIGroup: &other}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateBucketApplicationRef(tc.ref)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateBucketApplicationRef(%+v) err=%v, wantErr=%v", tc.ref, err, tc.wantErr)
			}
		})
	}
}

func TestBucketBackupPrecondition(t *testing.T) {
	cases := []struct {
		name       string
		claim      *buckettypes.BucketClaim
		wantReady  bool
		wantReason string
	}{
		{"nil claim", nil, false, "SourceBucketNotFound"},
		{
			"not ready",
			&buckettypes.BucketClaim{Status: buckettypes.BucketClaimStatus{BucketReady: false}},
			false, "SourceBucketNotReady",
		},
		{
			"ready but no bucket name",
			&buckettypes.BucketClaim{Status: buckettypes.BucketClaimStatus{BucketReady: true}},
			false, "SourceBucketNameUnresolved",
		},
		{
			"ready",
			&buckettypes.BucketClaim{Status: buckettypes.BucketClaimStatus{BucketReady: true, BucketName: "bucket-abc"}},
			true, "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ready, reason, _ := bucketBackupPrecondition(tc.claim)
			if ready != tc.wantReady || reason != tc.wantReason {
				t.Fatalf("bucketBackupPrecondition = (%v, %q), want (%v, %q)", ready, reason, tc.wantReady, tc.wantReason)
			}
		})
	}
}

func TestBucketSnapshotRoundTrip(t *testing.T) {
	in := bucketBackupSnapshot{
		Kind:                           bucketSnapshotKind,
		SourceBucket:                   "bucket-src",
		RepoBucket:                     "cozy-backups-xyz",
		RepoEndpoint:                   "s3.example.com",
		RepoPrefix:                     "tenant-x/app/backup-1/",
		RepoRegion:                     "us-east-1",
		RepoCredentialsSecret:          "cozy-backups-creds",
		RepoCredentialsSecretKeySecret: "cozy-backups-creds-secretkey",
		RepoCredentialsAccessKeyKey:    "AWS_ACCESS_KEY_ID",
		RepoCredentialsSecretKeyKey:    "AWS_SECRET_ACCESS_KEY",
		RepoCACertSecret:               "cozy-backups-ca",
		RepoCACertKey:                  "ca.crt",
		InsecureSkipVerify:             true,
		ServerSideCopy:                 false,
	}
	raw, err := marshalBucketSnapshot(in)
	if err != nil {
		t.Fatalf("marshalBucketSnapshot: %v", err)
	}
	out, err := unmarshalBucketSnapshot(raw)
	if err != nil {
		t.Fatalf("unmarshalBucketSnapshot: %v", err)
	}
	if out == nil || *out != in {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", out, in)
	}

	// A nil / empty RawExtension decodes to a nil snapshot, not an error.
	if got, err := unmarshalBucketSnapshot(nil); err != nil || got != nil {
		t.Fatalf("unmarshalBucketSnapshot(nil) = (%+v, %v), want (nil, nil)", got, err)
	}
}

func TestResolveBucketRestoreTarget(t *testing.T) {
	group := backupsv1alpha1.DefaultApplicationAPIGroup
	backup := &backupsv1alpha1.Backup{
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{APIGroup: &group, Kind: "Bucket", Name: "src"},
		},
	}

	t.Run("in-place defaults to source", func(t *testing.T) {
		rj := &backupsv1alpha1.RestoreJob{}
		got := resolveBucketRestoreTarget(rj, backup)
		if got.Name != "src" || got.Kind != "Bucket" {
			t.Fatalf("got %+v, want source src/Bucket", got)
		}
	})

	t.Run("to-copy overrides name only", func(t *testing.T) {
		rj := &backupsv1alpha1.RestoreJob{
			Spec: backupsv1alpha1.RestoreJobSpec{
				TargetApplicationRef: &corev1.TypedLocalObjectReference{Name: "dst"},
			},
		}
		got := resolveBucketRestoreTarget(rj, backup)
		if got.Name != "dst" || got.Kind != "Bucket" {
			t.Fatalf("got %+v, want dst/Bucket (kind inherited)", got)
		}
	})
}

func TestRenderBucketTemplate(t *testing.T) {
	app := map[string]interface{}{
		"metadata": map[string]interface{}{
			"name":      "mybucket",
			"namespace": "tenant-x",
		},
	}
	tmpl := strategyv1alpha1.BucketTemplate{
		Destination: strategyv1alpha1.BucketDestination{
			Bucket:                      "cozy-backups",
			Endpoint:                    "s3.example.com",
			Prefix:                      "{{ .Application.metadata.namespace }}/{{ .Application.metadata.name }}/",
			AccessKeyIDSecretKeyRef:     strategyv1alpha1.BucketSecretKeySelector{Name: "cozy-backups-creds", Key: "AWS_ACCESS_KEY_ID"},
			SecretAccessKeySecretKeyRef: strategyv1alpha1.BucketSecretKeySelector{Name: "cozy-backups-creds", Key: "AWS_SECRET_ACCESS_KEY"},
		},
		Image: "controller:latest",
	}
	rendered, err := renderBucketTemplate(tmpl, app, "mybucket", "tenant-x", bucketModeBackup, nil)
	if err != nil {
		t.Fatalf("renderBucketTemplate: %v", err)
	}
	if rendered.Destination.Prefix != "tenant-x/mybucket/" {
		t.Fatalf("prefix = %q, want %q", rendered.Destination.Prefix, "tenant-x/mybucket/")
	}
}

func newBucketAccessTestClient(t *testing.T, objs ...*buckettypes.BucketAccess) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	if err := buckettypes.AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	b := clientfake.NewClientBuilder().WithScheme(s)
	for _, o := range objs {
		b = b.WithObjects(o)
	}
	return b.Build()
}

func bucketAccess(name string, labels map[string]string, owners []metav1.OwnerReference, spec buckettypes.BucketAccessSpec) *buckettypes.BucketAccess {
	return &buckettypes.BucketAccess{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-x", Name: name, Labels: labels, OwnerReferences: owners},
		Spec:       spec,
	}
}

func testBucketClaim() *buckettypes.BucketClaim {
	return &buckettypes.BucketClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-x", Name: "bucket-web", UID: "claim-uid"}}
}

func TestReconcileBucketAccessRefusesUnownedName(t *testing.T) {
	// A same-named object without the driver's label belongs to someone else:
	// reusing it would point the mirror at whatever claim/class/Secret it names.
	squatted := bucketAccess("bucket-web-cozy-backup", nil, nil, buckettypes.BucketAccessSpec{
		BucketClaimName:       "attacker-claim",
		BucketAccessClassName: "attacker-class",
		Protocol:              bucketProtocolS3,
		CredentialsSecretName: "bucket-web-cozy-backup",
	})
	c := newBucketAccessTestClient(t, squatted)

	if _, err := reconcileBucketAccess(context.Background(), c, "tenant-x", "bucket-web-cozy-backup", testBucketClaim(), "bucket-web-readonly"); err == nil {
		t.Fatal("reconcileBucketAccess: want conflict error for an unowned same-named object, got nil")
	}
}

func TestReconcileBucketAccessReusesOwnedMatch(t *testing.T) {
	claim := testBucketClaim()
	owned := bucketAccess("bucket-web-cozy-backup",
		map[string]string{managedByLabel: managedByValue},
		[]metav1.OwnerReference{{APIVersion: buckettypes.GroupVersion.String(), Kind: "BucketClaim", Name: claim.Name, UID: claim.UID}},
		buckettypes.BucketAccessSpec{
			BucketClaimName:       "bucket-web",
			BucketAccessClassName: "bucket-web-readonly",
			Protocol:              bucketProtocolS3,
			CredentialsSecretName: "bucket-web-cozy-backup",
		})
	c := newBucketAccessTestClient(t, owned)

	got, err := reconcileBucketAccess(context.Background(), c, "tenant-x", "bucket-web-cozy-backup", claim, "bucket-web-readonly")
	if err != nil {
		t.Fatalf("reconcileBucketAccess: %v", err)
	}
	if got.Spec != owned.Spec {
		t.Fatalf("returned spec = %+v, want the existing %+v", got.Spec, owned.Spec)
	}
	if !hasOwnerUID(got, claim.UID) {
		t.Fatalf("returned access has no ownerRef to the BucketClaim: %+v", got.OwnerReferences)
	}
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func TestBuildBucketMirrorPod(t *testing.T) {
	tmpl := strategyv1alpha1.BucketTemplate{
		Image: "controller:latest",
		Destination: strategyv1alpha1.BucketDestination{
			Bucket:                      "cozy-backups",
			Endpoint:                    "http://seaweedfs:8333",
			Region:                      "us-east-1",
			AccessKeyIDSecretKeyRef:     strategyv1alpha1.BucketSecretKeySelector{Name: "cozy-backups-creds", Key: "AWS_ACCESS_KEY_ID"},
			SecretAccessKeySecretKeyRef: strategyv1alpha1.BucketSecretKeySelector{Name: "cozy-backups-creds", Key: "AWS_SECRET_ACCESS_KEY"},
		},
	}

	backupPod := buildBucketMirrorPod(bucketModeBackup, tmpl, "bucket-web-cozy-backup", "tenant-x/web/bk1/", false, false)
	c := backupPod.Spec.Containers[0]
	for _, want := range []string{"s3-mirror", "--mode=backup", "--repo-bucket=cozy-backups", "--repo-prefix=tenant-x/web/bk1/", "--repo-region=us-east-1"} {
		if !hasArg(c.Args, want) {
			t.Errorf("backup args missing %q: %v", want, c.Args)
		}
	}
	if hasArg(c.Args, "--delete-extraneous") {
		t.Errorf("backup must not set --delete-extraneous: %v", c.Args)
	}
	if backupPod.Spec.ActiveDeadlineSeconds == nil {
		t.Error("mirror pod has no ActiveDeadlineSeconds")
	}
	gotEnv := map[string]bool{}
	for _, e := range c.Env {
		gotEnv[e.Name] = true
	}
	for _, want := range []string{"APP_BUCKETINFO", "REPO_ACCESS_KEY", "REPO_SECRET_KEY"} {
		if !gotEnv[want] {
			t.Errorf("env missing %q", want)
		}
	}

	// The delete flag (the one that erases objects) rides only on an in-place
	// restore, so it must appear exactly when the caller asks for it.
	restorePod := buildBucketMirrorPod(bucketModeRestore, tmpl, "bucket-web-cozy-restore", "tenant-x/web/bk1/", true, false)
	if !hasArg(restorePod.Spec.Containers[0].Args, "--delete-extraneous") {
		t.Errorf("in-place restore must set --delete-extraneous: %v", restorePod.Spec.Containers[0].Args)
	}
}

func TestBuildBucketMirrorPodTLSKnobsIndependent(t *testing.T) {
	// A private CA and an explicit verify opt-out are composable: both --ca-file
	// and --insecure must be emitted when both are set, not one or the other.
	tmpl := strategyv1alpha1.BucketTemplate{
		Image: "controller:latest",
		Destination: strategyv1alpha1.BucketDestination{
			Bucket:   "cozy-backups",
			Endpoint: "https://s3",
			TLS: &strategyv1alpha1.BucketTLS{
				InsecureSkipVerify: true,
				CASecretKeyRef:     &strategyv1alpha1.BucketSecretKeySelector{Name: "my-ca", Key: "ca.crt"},
			},
		},
	}
	args := buildBucketMirrorPod(bucketModeBackup, tmpl, "acc", "p/", false, false).Spec.Containers[0].Args
	if !hasArg(args, "--ca-file=/etc/s3-ca/ca.crt") {
		t.Errorf("want --ca-file with a CA ref: %v", args)
	}
	if !hasArg(args, "--insecure") {
		t.Errorf("want --insecure preserved alongside a CA ref: %v", args)
	}

	// The other direction: a CA without the opt-out must NOT turn verification
	// off. Pins that the independence did not flip --insecure always-on.
	caOnly := tmpl
	caOnly.Destination.TLS = &strategyv1alpha1.BucketTLS{CASecretKeyRef: &strategyv1alpha1.BucketSecretKeySelector{Name: "my-ca", Key: "ca.crt"}}
	caArgs := buildBucketMirrorPod(bucketModeBackup, caOnly, "acc", "p/", false, false).Spec.Containers[0].Args
	if hasArg(caArgs, "--insecure") {
		t.Errorf("CA ref without insecureSkipVerify must not set --insecure: %v", caArgs)
	}
}

func TestBuildBucketMirrorPodAllowEmptySource(t *testing.T) {
	tmpl := strategyv1alpha1.BucketTemplate{Image: "img", Destination: strategyv1alpha1.BucketDestination{Bucket: "cozy-backups", Endpoint: "https://s3"}}
	on := buildBucketMirrorPod(bucketModeRestore, tmpl, "acc", "p/", true, true).Spec.Containers[0].Args
	if !hasArg(on, "--allow-empty-source") {
		t.Errorf("want --allow-empty-source when the annotation opts in: %v", on)
	}
	off := buildBucketMirrorPod(bucketModeRestore, tmpl, "acc", "p/", true, false).Spec.Containers[0].Args
	if hasArg(off, "--allow-empty-source") {
		t.Errorf("must not set --allow-empty-source by default: %v", off)
	}
}

func TestCleanupBucketBackupRefusesUnrelatedJob(t *testing.T) {
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		backupsv1alpha1.AddToScheme, strategyv1alpha1.AddToScheme, batchv1.AddToScheme,
	} {
		if err := add(s); err != nil {
			t.Fatalf("AddToScheme: %v", err)
		}
	}

	raw, err := marshalBucketSnapshot(bucketBackupSnapshot{
		Kind:                        bucketSnapshotKind,
		RepoBucket:                  "cozy-backups",
		RepoPrefix:                  "tenant-x/app/bk1/",
		RepoEndpoint:                "http://s3",
		RepoCredentialsSecret:       "cozy-backups-creds",
		RepoCredentialsAccessKeyKey: "AWS_ACCESS_KEY_ID",
		RepoCredentialsSecretKeyKey: "AWS_SECRET_ACCESS_KEY",
	})
	if err != nil {
		t.Fatalf("marshalBucketSnapshot: %v", err)
	}

	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-x", Name: "bk1", UID: "backup-uid"},
		Spec:       backupsv1alpha1.BackupSpec{StrategyRef: corev1.TypedLocalObjectReference{Kind: strategyv1alpha1.BucketStrategyKind, Name: "cozy-default-bucket"}},
		Status:     backupsv1alpha1.BackupStatus{UnderlyingResources: raw},
	}
	strategy := &strategyv1alpha1.Bucket{
		ObjectMeta: metav1.ObjectMeta{Name: "cozy-default-bucket"},
		Spec:       strategyv1alpha1.BucketSpec{Template: strategyv1alpha1.BucketTemplate{Image: "controller:latest"}},
	}
	// A same-named Job owned by nobody must not drive the release decision.
	alien := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-x", Name: "bk1-cleanup"}}

	c := clientfake.NewClientBuilder().WithScheme(s).WithObjects(backup, strategy, alien).Build()
	r := &BackupReconciler{Client: c, Scheme: s}
	if _, err := r.cleanupBucketBackup(context.Background(), backup); err == nil {
		t.Fatal("cleanupBucketBackup: want a conflict error for an unrelated cleanup Job, got nil")
	}
}

func TestIsInPlaceBucketRestore(t *testing.T) {
	// This decision is what gates the destructive delete-extraneous purge, so
	// pin both branches: a restore onto the backup's own app deletes extraneous
	// objects, a restore into a differently-named copy target must not.
	backup := &backupsv1alpha1.Backup{
		Spec: backupsv1alpha1.BackupSpec{ApplicationRef: corev1.TypedLocalObjectReference{Kind: "Bucket", Name: "web"}},
	}
	if !isInPlaceBucketRestore("web", backup) {
		t.Error("target == source app must be in-place (delete-extraneous)")
	}
	if isInPlaceBucketRestore("web-copy", backup) {
		t.Error("a differently-named target must be a to-copy restore (no delete-extraneous)")
	}
}

func TestRestoreJobActiveForBackupHoldsOnActiveMirrorJob(t *testing.T) {
	// The purge must be held while a restore mirror Job for the Backup still has
	// active Pods, even after its RestoreJob has gone Failed (whose Pods a GC has
	// not yet stopped) - otherwise cleanup purges next to a live restore.
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{backupsv1alpha1.AddToScheme, batchv1.AddToScheme} {
		if err := add(s); err != nil {
			t.Fatalf("AddToScheme: %v", err)
		}
	}
	backup := &backupsv1alpha1.Backup{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-x", Name: "bk1"}}
	failedRJ := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-x", Name: "rj1"},
		Spec:       backupsv1alpha1.RestoreJobSpec{BackupRef: corev1.LocalObjectReference{Name: "bk1"}},
		Status:     backupsv1alpha1.RestoreJobStatus{Phase: backupsv1alpha1.RestoreJobPhaseFailed},
	}
	mirrorJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-x", Name: "rj1-restore", Labels: map[string]string{bucketRestoreBackupLabel: "bk1"}},
		Status:     batchv1.JobStatus{Active: 1},
	}
	c := clientfake.NewClientBuilder().WithScheme(s).WithObjects(backup, failedRJ, mirrorJob).Build()
	r := &BackupReconciler{Client: c, Scheme: s}

	active, err := r.restoreJobActiveForBackup(context.Background(), backup)
	if err != nil {
		t.Fatalf("restoreJobActiveForBackup: %v", err)
	}
	if !active {
		t.Fatal("want the purge held while a labelled restore mirror Job has active Pods")
	}
}

// newBucketApp returns an apps.cozystack.io/Bucket the driver's dynamic client
// serves. The GVK must align with the RESTMapping newBucketReconcileEnv builds.
func newBucketApp(name, namespace string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   backupsv1alpha1.DefaultApplicationAPIGroup,
		Version: "v1alpha1",
		Kind:    bucketAppKind,
	})
	u.SetName(name)
	u.SetNamespace(namespace)
	return u
}

// newBucketReconcileEnv wires the controller-runtime fake client (with the
// buckettypes scheme the mirror provisioning needs), a dynamic client serving
// the target app, and a fixed REST mapper into the reconciler harness.
func newBucketReconcileEnv(t *testing.T, app *unstructured.Unstructured, builder *clientfake.ClientBuilder) (*BackupJobReconciler, *RestoreJobReconciler) {
	t.Helper()

	testScheme := runtime.NewScheme()
	_ = scheme.AddToScheme(testScheme)
	_ = backupsv1alpha1.AddToScheme(testScheme)
	_ = strategyv1alpha1.AddToScheme(testScheme)
	_ = buckettypes.AddToScheme(testScheme)

	gvr := schema.GroupVersionResource{
		Group:    backupsv1alpha1.DefaultApplicationAPIGroup,
		Version:  "v1alpha1",
		Resource: "buckets",
	}
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		testScheme,
		map[schema.GroupVersionResource]string{gvr: "BucketList"},
		app,
	)
	mapping := &meta.RESTMapping{
		Resource:         gvr,
		GroupVersionKind: app.GroupVersionKind(),
		Scope:            meta.RESTScopeNamespace,
	}
	restMapper := &mockRESTMapper{mapping: mapping}

	c := builder.WithScheme(testScheme).
		WithStatusSubresource(&backupsv1alpha1.BackupJob{}, &backupsv1alpha1.RestoreJob{}, &backupsv1alpha1.Backup{}).
		Build()

	return &BackupJobReconciler{
			Client:     c,
			Interface:  dynamicClient,
			RESTMapper: restMapper,
			Scheme:     testScheme,
			Recorder:   record.NewFakeRecorder(10),
		}, &RestoreJobReconciler{
			Client:     c,
			Interface:  dynamicClient,
			RESTMapper: restMapper,
			Scheme:     testScheme,
			Recorder:   record.NewFakeRecorder(10),
		}
}

func TestReconcileBucketRefusesSourceEqualsDestination(t *testing.T) {
	// The default BackupClass routes every apps.cozystack.io/Bucket to this
	// strategy, the cozy-backups repo bucket included. Backing that one up would
	// mirror every tenant's backups into a prefix inside the same bucket, so a
	// source whose S3 bucket IS the repo destination is refused. The distinct
	// source is exercised too, so an inverted comparison cannot pass both cases.
	now := metav1.Now()
	strategy := &strategyv1alpha1.Bucket{
		ObjectMeta: metav1.ObjectMeta{Name: "cozy-default-bucket"},
		Spec: strategyv1alpha1.BucketSpec{Template: strategyv1alpha1.BucketTemplate{
			Image:       "controller:latest",
			Destination: strategyv1alpha1.BucketDestination{Bucket: "cozy-backups", Endpoint: "http://s3"},
		}},
	}
	resolved := &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{
			APIGroup: stringPtr(strategyv1alpha1.GroupVersion.Group),
			Kind:     strategyv1alpha1.BucketStrategyKind,
			Name:     "cozy-default-bucket",
		},
	}
	newJob := func() *backupsv1alpha1.BackupJob {
		return &backupsv1alpha1.BackupJob{
			ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-x", Name: "bj1"},
			Spec: backupsv1alpha1.BackupJobSpec{
				ApplicationRef: corev1.TypedLocalObjectReference{Kind: bucketAppKind, Name: "web"},
			},
			Status: backupsv1alpha1.BackupJobStatus{StartedAt: &now, Phase: backupsv1alpha1.BackupJobPhaseRunning},
		}
	}
	newClaim := func(s3Bucket string) *buckettypes.BucketClaim {
		return &buckettypes.BucketClaim{
			ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-x", Name: "bucket-web", UID: "claim-uid"},
			Spec:       buckettypes.BucketClaimSpec{BucketClassName: "seaweedfs"},
			Status:     buckettypes.BucketClaimStatus{BucketReady: true, BucketName: s3Bucket},
		}
	}
	phaseAfterReconcile := func(t *testing.T, objs ...client.Object) backupsv1alpha1.BackupJobPhase {
		t.Helper()
		job := newJob()
		builder := clientfake.NewClientBuilder().WithObjects(job, strategy)
		for _, o := range objs {
			builder = builder.WithObjects(o)
		}
		r, _ := newBucketReconcileEnv(t, newBucketApp("web", "tenant-x"), builder)
		if _, err := r.reconcileBucket(context.Background(), job, resolved); err != nil {
			t.Fatalf("reconcileBucket: %v", err)
		}
		updated := &backupsv1alpha1.BackupJob{}
		if err := r.Get(context.Background(), client.ObjectKeyFromObject(job), updated); err != nil {
			t.Fatalf("get backupjob: %v", err)
		}
		return updated.Status.Phase
	}

	t.Run("source is the repo bucket", func(t *testing.T) {
		if got := phaseAfterReconcile(t, newClaim("cozy-backups")); got != backupsv1alpha1.BackupJobPhaseFailed {
			t.Fatalf("phase = %q, want Failed (self-backup refused)", got)
		}
	})
	t.Run("distinct source proceeds past the check", func(t *testing.T) {
		// Pre-grant the source access so the reconcile clears the grant gate and
		// reaches Job creation rather than failing on provisioning; the phase then
		// stays Running, proving the refusal did not fire for a distinct bucket.
		access := bucketAccess("bucket-web-cozy-backup",
			map[string]string{managedByLabel: managedByValue},
			[]metav1.OwnerReference{{APIVersion: buckettypes.GroupVersion.String(), Kind: "BucketClaim", Name: "bucket-web", UID: "claim-uid"}},
			buckettypes.BucketAccessSpec{
				BucketClaimName:       "bucket-web",
				BucketAccessClassName: "seaweedfs-readonly",
				Protocol:              bucketProtocolS3,
				CredentialsSecretName: "bucket-web-cozy-backup",
			})
		access.Status.AccessGranted = true
		if got := phaseAfterReconcile(t, newClaim("bucket-web-data"), access); got == backupsv1alpha1.BackupJobPhaseFailed {
			t.Fatalf("a distinct source bucket must not be refused as self-backup (phase = %q)", got)
		}
	})
}

func TestReconcileBucketRestoreInPlaceVsToCopyJobArgs(t *testing.T) {
	// The delete-extraneous purge erases live objects the snapshot does not name.
	// It must ride an in-place restore (target == the backup's own app) and NOT a
	// restore into a differently-named copy target. Drive the reconcile both ways
	// and read the flag off the mirror Job it creates, catching a regression in
	// the inPlace plumbing end to end, not only in the pure predicate.
	raw, err := marshalBucketSnapshot(bucketBackupSnapshot{
		Kind:                        bucketSnapshotKind,
		RepoBucket:                  "cozy-backups",
		RepoEndpoint:                "http://s3",
		RepoPrefix:                  "tenant-x/web/bk1/",
		RepoCredentialsSecret:       "cozy-backups-creds",
		RepoCredentialsAccessKeyKey: "AWS_ACCESS_KEY_ID",
		RepoCredentialsSecretKeyKey: "AWS_SECRET_ACCESS_KEY",
	})
	if err != nil {
		t.Fatalf("marshalBucketSnapshot: %v", err)
	}
	strategy := &strategyv1alpha1.Bucket{
		ObjectMeta: metav1.ObjectMeta{Name: "cozy-default-bucket"},
		Spec:       strategyv1alpha1.BucketSpec{Template: strategyv1alpha1.BucketTemplate{Image: "controller:latest"}},
	}
	newBackup := func() *backupsv1alpha1.Backup {
		return &backupsv1alpha1.Backup{
			ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-x", Name: "bk1", UID: "backup-uid"},
			Spec: backupsv1alpha1.BackupSpec{
				ApplicationRef: corev1.TypedLocalObjectReference{Kind: bucketAppKind, Name: "web"},
				StrategyRef:    corev1.TypedLocalObjectReference{Kind: strategyv1alpha1.BucketStrategyKind, Name: "cozy-default-bucket"},
			},
			Status: backupsv1alpha1.BackupStatus{UnderlyingResources: raw},
		}
	}
	grantedClaim := func(app string) *buckettypes.BucketClaim {
		claimName := bucketReleaseName(app)
		return &buckettypes.BucketClaim{
			ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-x", Name: claimName, UID: types.UID(claimName + "-uid")},
			Spec:       buckettypes.BucketClaimSpec{BucketClassName: "seaweedfs"},
			Status:     buckettypes.BucketClaimStatus{BucketReady: true, BucketName: "bucket-" + app + "-data"},
		}
	}
	grantedAccess := func(app string) *buckettypes.BucketAccess {
		claimName := bucketReleaseName(app)
		a := bucketAccess(restoreAccessName(app),
			map[string]string{managedByLabel: managedByValue},
			[]metav1.OwnerReference{{APIVersion: buckettypes.GroupVersion.String(), Kind: "BucketClaim", Name: claimName, UID: types.UID(claimName + "-uid")}},
			buckettypes.BucketAccessSpec{
				BucketClaimName:       claimName,
				BucketAccessClassName: "seaweedfs",
				Protocol:              bucketProtocolS3,
				CredentialsSecretName: restoreAccessName(app),
			})
		a.Status.AccessGranted = true
		return a
	}
	now := metav1.Now()

	restoreArgs := func(t *testing.T, targetApp string, target *corev1.TypedLocalObjectReference) []string {
		t.Helper()
		rj := &backupsv1alpha1.RestoreJob{
			ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-x", Name: "rj1"},
			Spec: backupsv1alpha1.RestoreJobSpec{
				BackupRef:            corev1.LocalObjectReference{Name: "bk1"},
				TargetApplicationRef: target,
			},
			Status: backupsv1alpha1.RestoreJobStatus{StartedAt: &now, Phase: backupsv1alpha1.RestoreJobPhaseRunning},
		}
		backup := newBackup()
		_, rr := newBucketReconcileEnv(t, newBucketApp(targetApp, "tenant-x"),
			clientfake.NewClientBuilder().WithObjects(rj, backup, strategy, grantedClaim(targetApp), grantedAccess(targetApp)))
		if _, err := rr.reconcileBucketRestore(context.Background(), rj, backup); err != nil {
			t.Fatalf("reconcileBucketRestore: %v", err)
		}
		mirror := &batchv1.Job{}
		if err := rr.Get(context.Background(), types.NamespacedName{Namespace: "tenant-x", Name: jobNameForRestoreJob(rj)}, mirror); err != nil {
			t.Fatalf("get mirror Job: %v", err)
		}
		return mirror.Spec.Template.Spec.Containers[0].Args
	}

	t.Run("in-place restore purges", func(t *testing.T) {
		if args := restoreArgs(t, "web", nil); !hasArg(args, "--delete-extraneous") {
			t.Errorf("in-place restore Job must carry --delete-extraneous: %v", args)
		}
	})
	t.Run("to-copy restore merges", func(t *testing.T) {
		target := &corev1.TypedLocalObjectReference{Kind: bucketAppKind, Name: "web-copy"}
		if args := restoreArgs(t, "web-copy", target); hasArg(args, "--delete-extraneous") {
			t.Errorf("to-copy restore Job must NOT carry --delete-extraneous: %v", args)
		}
	})
}

func TestReconcileBucketFailsOnArtifactNameCollision(t *testing.T) {
	// A Backup carries no ownerRef to its BackupJob and outlives it, so a
	// BackupJob whose name is reused after the old one was deleted finds a stale
	// same-named Backup pointing at the previous run's UID-scoped prefix. The
	// mirror has already written a fresh copy under this run's prefix; returning
	// the stale Backup would report Succeeded against the old snapshot and strand
	// the fresh copy unreferenced. The run must fail instead.
	strategy := &strategyv1alpha1.Bucket{
		ObjectMeta: metav1.ObjectMeta{Name: "cozy-default-bucket"},
		Spec: strategyv1alpha1.BucketSpec{Template: strategyv1alpha1.BucketTemplate{
			Image:       "controller:latest",
			Destination: strategyv1alpha1.BucketDestination{Bucket: "cozy-backups", Endpoint: "http://s3"},
		}},
	}
	resolved := &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{
			APIGroup: stringPtr(strategyv1alpha1.GroupVersion.Group),
			Kind:     strategyv1alpha1.BucketStrategyKind,
			Name:     "cozy-default-bucket",
		},
	}
	now := metav1.Now()
	job := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-x", Name: "manual-1", UID: "new-uid"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{Kind: bucketAppKind, Name: "web"},
		},
		Status: backupsv1alpha1.BackupJobStatus{StartedAt: &now, Phase: backupsv1alpha1.BackupJobPhaseRunning},
	}
	claim := &buckettypes.BucketClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-x", Name: "bucket-web", UID: "claim-uid"},
		Spec:       buckettypes.BucketClaimSpec{BucketClassName: "seaweedfs"},
		Status:     buckettypes.BucketClaimStatus{BucketReady: true, BucketName: "app-bucket"},
	}
	access := bucketAccess("bucket-web-cozy-backup",
		map[string]string{managedByLabel: managedByValue},
		[]metav1.OwnerReference{{APIVersion: buckettypes.GroupVersion.String(), Kind: "BucketClaim", Name: "bucket-web", UID: "claim-uid"}},
		buckettypes.BucketAccessSpec{
			BucketClaimName:       "bucket-web",
			BucketAccessClassName: "seaweedfs-readonly",
			Protocol:              bucketProtocolS3,
			CredentialsSecretName: "bucket-web-cozy-backup",
		})
	access.Status.AccessGranted = true
	completedMirror := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       "tenant-x",
			Name:            jobNameForBackupJob(job),
			OwnerReferences: bucketJobControllerRef(job),
			Labels: map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      job.Name,
				backupsv1alpha1.OwningJobNamespaceLabel: job.Namespace,
			},
		},
		Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{
			{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
		}},
	}
	staleRaw, err := marshalBucketSnapshot(bucketBackupSnapshot{
		Kind:       bucketSnapshotKind,
		RepoBucket: "cozy-backups",
		RepoPrefix: "manual-1-old-uid/", // a prior run's prefix, not this run's
	})
	if err != nil {
		t.Fatalf("marshalBucketSnapshot: %v", err)
	}
	staleBackup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-x", Name: "manual-1"},
		Status:     backupsv1alpha1.BackupStatus{UnderlyingResources: staleRaw},
	}

	r, _ := newBucketReconcileEnv(t, newBucketApp("web", "tenant-x"),
		clientfake.NewClientBuilder().WithObjects(job, strategy, claim, access, completedMirror, staleBackup))
	if _, err := r.reconcileBucket(context.Background(), job, resolved); err != nil {
		t.Fatalf("reconcileBucket: %v", err)
	}
	updated := &backupsv1alpha1.BackupJob{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(job), updated); err != nil {
		t.Fatalf("get backupjob: %v", err)
	}
	if updated.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
		t.Fatalf("phase = %q, want Failed (stale same-named Backup must not be reused)", updated.Status.Phase)
	}
	if updated.Status.BackupRef != nil {
		t.Fatalf("BackupRef = %+v, want nil (must not point at the stale snapshot)", updated.Status.BackupRef)
	}
	// The mirror wrote a fresh copy under this run's prefix; a terminal exit must
	// reclaim it, so a reclaim Job is created rather than leaving the copy stranded.
	reclaim := &batchv1.Job{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "tenant-x", Name: job.Name + "-reclaim"}, reclaim); err != nil {
		t.Fatalf("reclaim Job not created on the collision exit: %v", err)
	}
}

// bucketJobControllerRef builds the controller ownerRef ensureJobStrategyJob
// stamps on a mirror Job it creates, so a test fixture Job is adopted rather
// than refused by the ownership guard.
func bucketJobControllerRef(job *backupsv1alpha1.BackupJob) []metav1.OwnerReference {
	yes := true
	return []metav1.OwnerReference{{
		APIVersion: backupsv1alpha1.GroupVersion.String(),
		Kind:       "BackupJob",
		Name:       job.Name,
		UID:        job.UID,
		Controller: &yes,
	}}
}

func TestReconcileBucketRefusesUnownedMirrorJob(t *testing.T) {
	// A finished mirror Job left by a prior identically-named ad-hoc BackupJob is
	// adopted by name; without an ownership check this run would read JobComplete
	// and record a Backup for a prefix nothing wrote. The run must refuse instead.
	strategy := &strategyv1alpha1.Bucket{
		ObjectMeta: metav1.ObjectMeta{Name: "cozy-default-bucket"},
		Spec: strategyv1alpha1.BucketSpec{Template: strategyv1alpha1.BucketTemplate{
			Image:       "controller:latest",
			Destination: strategyv1alpha1.BucketDestination{Bucket: "cozy-backups", Endpoint: "http://s3"},
		}},
	}
	resolved := &ResolvedBackupConfig{StrategyRef: corev1.TypedLocalObjectReference{
		APIGroup: stringPtr(strategyv1alpha1.GroupVersion.Group),
		Kind:     strategyv1alpha1.BucketStrategyKind,
		Name:     "cozy-default-bucket",
	}}
	now := metav1.Now()
	job := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-x", Name: "manual-1", UID: "new-uid"},
		Spec:       backupsv1alpha1.BackupJobSpec{ApplicationRef: corev1.TypedLocalObjectReference{Kind: bucketAppKind, Name: "web"}},
		Status:     backupsv1alpha1.BackupJobStatus{StartedAt: &now, Phase: backupsv1alpha1.BackupJobPhaseRunning},
	}
	claim := &buckettypes.BucketClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-x", Name: "bucket-web", UID: "claim-uid"},
		Spec:       buckettypes.BucketClaimSpec{BucketClassName: "seaweedfs"},
		Status:     buckettypes.BucketClaimStatus{BucketReady: true, BucketName: "app-bucket"},
	}
	access := bucketAccess("bucket-web-cozy-backup",
		map[string]string{managedByLabel: managedByValue},
		[]metav1.OwnerReference{{APIVersion: buckettypes.GroupVersion.String(), Kind: "BucketClaim", Name: "bucket-web", UID: "claim-uid"}},
		buckettypes.BucketAccessSpec{
			BucketClaimName: "bucket-web", BucketAccessClassName: "seaweedfs-readonly",
			Protocol: bucketProtocolS3, CredentialsSecretName: "bucket-web-cozy-backup",
		})
	access.Status.AccessGranted = true
	// A completed Job of the right name owned by a DIFFERENT BackupJob UID.
	yes := true
	alienMirror := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "tenant-x", Name: jobNameForBackupJob(job),
			OwnerReferences: []metav1.OwnerReference{{APIVersion: backupsv1alpha1.GroupVersion.String(), Kind: "BackupJob", Name: "manual-1", UID: "old-uid", Controller: &yes}},
		},
		Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}},
	}
	r, _ := newBucketReconcileEnv(t, newBucketApp("web", "tenant-x"),
		clientfake.NewClientBuilder().WithObjects(job, strategy, claim, access, alienMirror))
	if _, err := r.reconcileBucket(context.Background(), job, resolved); err != nil {
		t.Fatalf("reconcileBucket: %v", err)
	}
	updated := &backupsv1alpha1.BackupJob{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(job), updated); err != nil {
		t.Fatalf("get backupjob: %v", err)
	}
	if updated.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
		t.Fatalf("phase = %q, want Failed (unowned mirror Job must not be adopted)", updated.Status.Phase)
	}
	if updated.Status.BackupRef != nil {
		t.Fatalf("BackupRef must stay nil, got %+v", updated.Status.BackupRef)
	}
}

func TestReclaimFailedBucketBackupWarnsWhenJobCannotStart(t *testing.T) {
	// The ReclaimNotStarted Event is the only signal that a partial copy was left
	// in the shared repo bucket, so it must fire when the reclaim Job cannot be
	// created. A scheme without BackupJob makes SetControllerReference fail.
	s := runtime.NewScheme()
	_ = batchv1.AddToScheme(s)
	rec := record.NewFakeRecorder(10)
	r := &BackupJobReconciler{
		Client:   clientfake.NewClientBuilder().WithScheme(s).Build(),
		Scheme:   s,
		Recorder: rec,
	}
	j := &backupsv1alpha1.BackupJob{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-x", Name: "bj1"}}
	rendered := strategyv1alpha1.BucketTemplate{Image: "img", Destination: strategyv1alpha1.BucketDestination{Bucket: "cozy-backups", Endpoint: "http://s3"}}
	r.reclaimFailedBucketBackup(context.Background(), j, rendered, "tenant-x/web/bj1-uid/")
	select {
	case ev := <-rec.Events:
		if !strings.Contains(ev, "ReclaimNotStarted") {
			t.Fatalf("event = %q, want a ReclaimNotStarted warning", ev)
		}
	default:
		t.Fatal("no Event emitted when the reclaim Job could not be created")
	}
}

func TestBuildBucketCleanupPodHasRestrictedSecurityContext(t *testing.T) {
	snap := &bucketBackupSnapshot{
		RepoBucket: "cozy-backups", RepoEndpoint: "http://s3", RepoPrefix: "tenant-x/web/bk1/",
		RepoCredentialsSecret: "cozy-backups-creds", RepoCredentialsAccessKeyKey: "AWS_ACCESS_KEY_ID", RepoCredentialsSecretKeyKey: "AWS_SECRET_ACCESS_KEY",
	}
	assertRestrictedSecurityContext(t, "cleanup", buildBucketCleanupPod(snap, "img").Spec.Containers[0].SecurityContext)
}

func TestBuildBucketMirrorPodHasRestrictedSecurityContext(t *testing.T) {
	tmpl := strategyv1alpha1.BucketTemplate{Image: "img", Destination: strategyv1alpha1.BucketDestination{Bucket: "cozy-backups", Endpoint: "http://s3"}}
	assertRestrictedSecurityContext(t, "mirror", buildBucketMirrorPod(bucketModeBackup, tmpl, "acc", "p/", false, false).Spec.Containers[0].SecurityContext)
}

func assertRestrictedSecurityContext(t *testing.T, name string, sc *corev1.SecurityContext) {
	t.Helper()
	if sc == nil {
		t.Fatalf("%s container has no SecurityContext", name)
	}
	if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		t.Errorf("%s: AllowPrivilegeEscalation must be false", name)
	}
	if sc.Capabilities == nil || len(sc.Capabilities.Drop) == 0 || sc.Capabilities.Drop[0] != "ALL" {
		t.Errorf("%s: capabilities must drop ALL, got %+v", name, sc.Capabilities)
	}
	if sc.SeccompProfile == nil || sc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("%s: seccomp profile must be RuntimeDefault", name)
	}
}

// bucketBackupScenario builds a BackupJob past its preconditions (ready claim,
// granted source access, strategy resolved) so a seeded mirror Job drives the
// JobComplete/JobFailed branch of reconcileBucket.
func bucketBackupScenario(uid string) (*backupsv1alpha1.BackupJob, *ResolvedBackupConfig, []client.Object) {
	now := metav1.Now()
	job := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-x", Name: "manual-1", UID: types.UID(uid)},
		Spec:       backupsv1alpha1.BackupJobSpec{ApplicationRef: corev1.TypedLocalObjectReference{Kind: bucketAppKind, Name: "web"}},
		Status:     backupsv1alpha1.BackupJobStatus{StartedAt: &now, Phase: backupsv1alpha1.BackupJobPhaseRunning},
	}
	strategy := &strategyv1alpha1.Bucket{
		ObjectMeta: metav1.ObjectMeta{Name: "cozy-default-bucket"},
		Spec: strategyv1alpha1.BucketSpec{Template: strategyv1alpha1.BucketTemplate{
			Image:       "controller:latest",
			Destination: strategyv1alpha1.BucketDestination{Bucket: "cozy-backups", Endpoint: "http://s3"},
		}},
	}
	resolved := &ResolvedBackupConfig{StrategyRef: corev1.TypedLocalObjectReference{
		APIGroup: stringPtr(strategyv1alpha1.GroupVersion.Group), Kind: strategyv1alpha1.BucketStrategyKind, Name: "cozy-default-bucket",
	}}
	claim := &buckettypes.BucketClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-x", Name: "bucket-web", UID: "claim-uid"},
		Spec:       buckettypes.BucketClaimSpec{BucketClassName: "seaweedfs"},
		Status:     buckettypes.BucketClaimStatus{BucketReady: true, BucketName: "app-bucket"},
	}
	access := bucketAccess("bucket-web-cozy-backup",
		map[string]string{managedByLabel: managedByValue},
		[]metav1.OwnerReference{{APIVersion: buckettypes.GroupVersion.String(), Kind: "BucketClaim", Name: "bucket-web", UID: "claim-uid"}},
		buckettypes.BucketAccessSpec{BucketClaimName: "bucket-web", BucketAccessClassName: "seaweedfs-readonly", Protocol: bucketProtocolS3, CredentialsSecretName: "bucket-web-cozy-backup"})
	access.Status.AccessGranted = true
	return job, resolved, []client.Object{strategy, claim, access}
}

func ownedMirrorJob(job *backupsv1alpha1.BackupJob, cond batchv1.JobConditionType) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-x", Name: jobNameForBackupJob(job), OwnerReferences: bucketJobControllerRef(job)},
		Status:     batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: cond, Status: corev1.ConditionTrue}}},
	}
}

func reclaimJobExists(t *testing.T, c client.Client, jobName string) bool {
	t.Helper()
	err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant-x", Name: jobName + "-reclaim"}, &batchv1.Job{})
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("get reclaim Job: %v", err)
	}
	return err == nil
}

func TestReconcileBucketReclaimsOnMirrorJobFailed(t *testing.T) {
	// A mirror that dies partway has already put a prefix's worth into the shared
	// repo bucket with no Backup for cleanup to key off, so a JobFailed must
	// reclaim the partial prefix before the BackupJob goes terminal.
	job, resolved, objs := bucketBackupScenario("new-uid")
	objs = append(objs, job, ownedMirrorJob(job, batchv1.JobFailed))
	r, _ := newBucketReconcileEnv(t, newBucketApp("web", "tenant-x"), clientfake.NewClientBuilder().WithObjects(objs...))
	if _, err := r.reconcileBucket(context.Background(), job, resolved); err != nil {
		t.Fatalf("reconcileBucket: %v", err)
	}
	updated := &backupsv1alpha1.BackupJob{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(job), updated); err != nil {
		t.Fatalf("get backupjob: %v", err)
	}
	if updated.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
		t.Fatalf("phase = %q, want Failed", updated.Status.Phase)
	}
	if !reclaimJobExists(t, r.Client, job.Name) {
		t.Fatal("a reclaim Job must be created on the JobFailed exit")
	}
}

var errTransientArtifactCreate = errors.New("transient artifact create conflict")

func failBackupCreate() interceptor.Funcs {
	return interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*backupsv1alpha1.Backup); ok {
				return apierrors.NewConflict(schema.GroupResource{Group: "backups.cozystack.io", Resource: "backups"}, obj.GetName(), errTransientArtifactCreate)
			}
			return c.Create(ctx, obj, opts...)
		},
	}
}

func TestArtifactWriteRetryUsesOwnClock(t *testing.T) {
	// StartedAt is stamped before the mirror Job, which may run for hours, so the
	// artifact-write retry cannot be clocked off it. A transient Backup create
	// failure requeues while the ArtifactWritePending condition is young and only
	// fails (reclaiming the copy) once that condition's own clock is spent.
	t.Run("young condition requeues", func(t *testing.T) {
		job, resolved, objs := bucketBackupScenario("uid-a")
		objs = append(objs, job, ownedMirrorJob(job, batchv1.JobComplete))
		r, _ := newBucketReconcileEnv(t, newBucketApp("web", "tenant-x"),
			clientfake.NewClientBuilder().WithObjects(objs...).WithInterceptorFuncs(failBackupCreate()))
		res, err := r.reconcileBucket(context.Background(), job, resolved)
		if err != nil {
			t.Fatalf("reconcileBucket: %v", err)
		}
		if res.RequeueAfter <= 0 {
			t.Fatalf("want a requeue while the retry clock is young, got %+v", res)
		}
		updated := &backupsv1alpha1.BackupJob{}
		if err := r.Get(context.Background(), client.ObjectKeyFromObject(job), updated); err != nil {
			t.Fatalf("get backupjob: %v", err)
		}
		if updated.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
			t.Fatal("must not fail while the artifact-write clock is young")
		}
		if cond := meta.FindStatusCondition(updated.Status.Conditions, bucketArtifactWritePendingCondition); cond == nil {
			t.Fatal("ArtifactWritePending condition must be set to start the retry clock")
		}
		if reclaimJobExists(t, r.Client, job.Name) {
			t.Fatal("must not reclaim while still retrying")
		}
	})

	t.Run("spent condition reclaims and fails", func(t *testing.T) {
		job, resolved, objs := bucketBackupScenario("uid-b")
		job.Status.Conditions = []metav1.Condition{{
			Type:               bucketArtifactWritePendingCondition,
			Status:             metav1.ConditionTrue,
			Reason:             "ArtifactWriteFailing",
			Message:            "retrying",
			LastTransitionTime: metav1.NewTime(time.Now().Add(-StrategyNotReadyDeadline - time.Minute)),
		}}
		objs = append(objs, job, ownedMirrorJob(job, batchv1.JobComplete))
		r, _ := newBucketReconcileEnv(t, newBucketApp("web", "tenant-x"),
			clientfake.NewClientBuilder().WithObjects(objs...).WithInterceptorFuncs(failBackupCreate()))
		if _, err := r.reconcileBucket(context.Background(), job, resolved); err != nil {
			t.Fatalf("reconcileBucket: %v", err)
		}
		updated := &backupsv1alpha1.BackupJob{}
		if err := r.Get(context.Background(), client.ObjectKeyFromObject(job), updated); err != nil {
			t.Fatalf("get backupjob: %v", err)
		}
		if updated.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
			t.Fatalf("phase = %q, want Failed once the retry clock is spent", updated.Status.Phase)
		}
		if !reclaimJobExists(t, r.Client, job.Name) {
			t.Fatal("must reclaim the stranded copy when giving up on the artifact write")
		}
	})
}

func TestCleanupBucketBackupHoldsPurgeWhileRestoreActive(t *testing.T) {
	// An in-place restore lists the repo prefix to build its keep-set; a purge
	// removing a key before the listing reaches it drops that key and the restore
	// then wipes the live copy. Cleanup must hold while a RestoreJob still reads
	// the Backup, creating no cleanup Job.
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{backupsv1alpha1.AddToScheme, batchv1.AddToScheme, strategyv1alpha1.AddToScheme} {
		if err := add(s); err != nil {
			t.Fatalf("AddToScheme: %v", err)
		}
	}
	raw, err := marshalBucketSnapshot(bucketBackupSnapshot{
		Kind: bucketSnapshotKind, RepoBucket: "cozy-backups", RepoEndpoint: "http://s3", RepoPrefix: "tenant-x/web/bk1/",
	})
	if err != nil {
		t.Fatalf("marshalBucketSnapshot: %v", err)
	}
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-x", Name: "bk1"},
		Spec:       backupsv1alpha1.BackupSpec{StrategyRef: corev1.TypedLocalObjectReference{Kind: strategyv1alpha1.BucketStrategyKind, Name: "cozy-default-bucket"}},
		Status:     backupsv1alpha1.BackupStatus{UnderlyingResources: raw},
	}
	activeRJ := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-x", Name: "rj1"},
		Spec:       backupsv1alpha1.RestoreJobSpec{BackupRef: corev1.LocalObjectReference{Name: "bk1"}},
		Status:     backupsv1alpha1.RestoreJobStatus{Phase: backupsv1alpha1.RestoreJobPhaseRunning},
	}
	c := clientfake.NewClientBuilder().WithScheme(s).WithObjects(backup, activeRJ).Build()
	r := &BackupReconciler{Client: c, Scheme: s}

	res, err := r.cleanupBucketBackup(context.Background(), backup)
	if err != nil {
		t.Fatalf("cleanupBucketBackup: %v", err)
	}
	if res.RequeueAfter <= 0 {
		t.Fatalf("want a requeue while a restore is active, got %+v", res)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant-x", Name: "bk1-cleanup"}, &batchv1.Job{}); !apierrors.IsNotFound(err) {
		t.Fatalf("a cleanup Job must not be created while the purge is held (err=%v)", err)
	}
}

func TestReconcileBucketRestoreRefusesDeletingBackup(t *testing.T) {
	// A Backup mid-reclaim may have its repo objects half-purged; starting an
	// in-place restore against it would mirror a subset and then delete the rest
	// of the live bucket, so a restore whose mirror Job does not yet exist is
	// refused while the Backup is being deleted.
	now := metav1.Now()
	raw, err := marshalBucketSnapshot(bucketBackupSnapshot{
		Kind: bucketSnapshotKind, RepoBucket: "cozy-backups", RepoEndpoint: "http://s3", RepoPrefix: "tenant-x/web/bk1/",
	})
	if err != nil {
		t.Fatalf("marshalBucketSnapshot: %v", err)
	}
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-x", Name: "bk1", DeletionTimestamp: &now, Finalizers: []string{"backups.cozystack.io/cleanup"}},
		Spec:       backupsv1alpha1.BackupSpec{ApplicationRef: corev1.TypedLocalObjectReference{Kind: bucketAppKind, Name: "web"}, StrategyRef: corev1.TypedLocalObjectReference{Kind: strategyv1alpha1.BucketStrategyKind, Name: "cozy-default-bucket"}},
		Status:     backupsv1alpha1.BackupStatus{UnderlyingResources: raw},
	}
	rj := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-x", Name: "rj1"},
		Spec:       backupsv1alpha1.RestoreJobSpec{BackupRef: corev1.LocalObjectReference{Name: "bk1"}},
		Status:     backupsv1alpha1.RestoreJobStatus{Phase: backupsv1alpha1.RestoreJobPhaseRunning},
	}
	_, rr := newBucketReconcileEnv(t, newBucketApp("web", "tenant-x"), clientfake.NewClientBuilder().WithObjects(rj))
	if _, err := rr.reconcileBucketRestore(context.Background(), rj, backup); err != nil {
		t.Fatalf("reconcileBucketRestore: %v", err)
	}
	updated := &backupsv1alpha1.RestoreJob{}
	if err := rr.Get(context.Background(), client.ObjectKeyFromObject(rj), updated); err != nil {
		t.Fatalf("get restorejob: %v", err)
	}
	if updated.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
		t.Fatalf("phase = %q, want Failed (restore from a deleting Backup must be refused)", updated.Status.Phase)
	}
}

func TestReconcileBucketRestoreRefusesUnownedMirrorJob(t *testing.T) {
	// The restore path adopts a same-named mirror Job by name (<restoreJob>-restore).
	// A finished Job left by a prior identically-named RestoreJob must not be read
	// as this run's completion: without an ownership check the new RestoreJob is
	// marked Succeeded though no mirror ran for the current target - a green
	// restore that never happened. Symmetric to the backup-path guard.
	raw, err := marshalBucketSnapshot(bucketBackupSnapshot{
		Kind: bucketSnapshotKind, RepoBucket: "cozy-backups", RepoEndpoint: "http://s3", RepoPrefix: "tenant-x/web/bk1/",
		RepoCredentialsSecret: "cozy-backups-creds", RepoCredentialsAccessKeyKey: "AWS_ACCESS_KEY_ID", RepoCredentialsSecretKeyKey: "AWS_SECRET_ACCESS_KEY",
	})
	if err != nil {
		t.Fatalf("marshalBucketSnapshot: %v", err)
	}
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-x", Name: "bk1", UID: "backup-uid"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{Kind: bucketAppKind, Name: "web"},
			StrategyRef:    corev1.TypedLocalObjectReference{Kind: strategyv1alpha1.BucketStrategyKind, Name: "cozy-default-bucket"},
		},
		Status: backupsv1alpha1.BackupStatus{UnderlyingResources: raw},
	}
	strategy := &strategyv1alpha1.Bucket{
		ObjectMeta: metav1.ObjectMeta{Name: "cozy-default-bucket"},
		Spec:       strategyv1alpha1.BucketSpec{Template: strategyv1alpha1.BucketTemplate{Image: "controller:latest"}},
	}
	claim := &buckettypes.BucketClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-x", Name: "bucket-web", UID: "claim-uid"},
		Spec:       buckettypes.BucketClaimSpec{BucketClassName: "seaweedfs"},
		Status:     buckettypes.BucketClaimStatus{BucketReady: true, BucketName: "app-bucket"},
	}
	access := bucketAccess(restoreAccessName("web"),
		map[string]string{managedByLabel: managedByValue},
		[]metav1.OwnerReference{{APIVersion: buckettypes.GroupVersion.String(), Kind: "BucketClaim", Name: "bucket-web", UID: "claim-uid"}},
		buckettypes.BucketAccessSpec{BucketClaimName: "bucket-web", BucketAccessClassName: "seaweedfs", Protocol: bucketProtocolS3, CredentialsSecretName: restoreAccessName("web")})
	access.Status.AccessGranted = true
	now := metav1.Now()
	rj := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-x", Name: "rj1", UID: "new-uid"},
		Spec:       backupsv1alpha1.RestoreJobSpec{BackupRef: corev1.LocalObjectReference{Name: "bk1"}},
		Status:     backupsv1alpha1.RestoreJobStatus{StartedAt: &now, Phase: backupsv1alpha1.RestoreJobPhaseRunning},
	}
	// A completed restore Job of the right name owned by a DIFFERENT RestoreJob UID.
	yes := true
	alienMirror := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "tenant-x", Name: jobNameForRestoreJob(rj),
			OwnerReferences: []metav1.OwnerReference{{APIVersion: backupsv1alpha1.GroupVersion.String(), Kind: "RestoreJob", Name: "rj1", UID: "old-uid", Controller: &yes}},
		},
		Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}},
	}
	_, rr := newBucketReconcileEnv(t, newBucketApp("web", "tenant-x"),
		clientfake.NewClientBuilder().WithObjects(rj, strategy, claim, access, alienMirror))
	if _, err := rr.reconcileBucketRestore(context.Background(), rj, backup); err != nil {
		t.Fatalf("reconcileBucketRestore: %v", err)
	}
	updated := &backupsv1alpha1.RestoreJob{}
	if err := rr.Get(context.Background(), client.ObjectKeyFromObject(rj), updated); err != nil {
		t.Fatalf("get restorejob: %v", err)
	}
	if updated.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
		t.Fatalf("phase = %q, want Failed (unowned mirror Job must not be adopted as this restore's completion)", updated.Status.Phase)
	}
}

func TestReconcileBucketStampsReclaimGuard(t *testing.T) {
	// Before the mirror writes into the shared repo bucket, the BackupJob must
	// carry the reclaim finalizer and the stashed coordinates, so cancelling a
	// running backup can reclaim the partial copy.
	job, resolved, objs := bucketBackupScenario("uid-guard")
	objs = append(objs, job) // no mirror Job seeded: the reconcile creates one and requeues
	r, _ := newBucketReconcileEnv(t, newBucketApp("web", "tenant-x"), clientfake.NewClientBuilder().WithObjects(objs...))
	if _, err := r.reconcileBucket(context.Background(), job, resolved); err != nil {
		t.Fatalf("reconcileBucket: %v", err)
	}
	updated := &backupsv1alpha1.BackupJob{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(job), updated); err != nil {
		t.Fatalf("get backupjob: %v", err)
	}
	if !controllerutil.ContainsFinalizer(updated, bucketBackupFinalizer) {
		t.Fatal("BackupJob must carry the reclaim finalizer once the mirror is started")
	}
	if updated.Annotations[bucketReclaimStashAnnotation] == "" {
		t.Fatal("BackupJob must stash the reclaim coordinates")
	}
}

func deletingBucketBackupJob(t *testing.T, name string, withStash, withBackupRef bool) *backupsv1alpha1.BackupJob {
	t.Helper()
	j := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-x", Name: name, UID: types.UID(name + "-uid"), Finalizers: []string{bucketBackupFinalizer}},
	}
	if withStash {
		raw, err := json.Marshal(bucketReclaimStash{
			Snapshot: bucketBackupSnapshot{
				RepoBucket: "cozy-backups", RepoEndpoint: "http://s3", RepoPrefix: "tenant-x/web/" + name + "-uid/",
				RepoCredentialsSecret: "cozy-backups-creds", RepoCredentialsAccessKeyKey: "AWS_ACCESS_KEY_ID", RepoCredentialsSecretKeyKey: "AWS_SECRET_ACCESS_KEY",
			},
			Image: "controller:latest",
		})
		if err != nil {
			t.Fatalf("marshal stash: %v", err)
		}
		j.Annotations = map[string]string{bucketReclaimStashAnnotation: string(raw)}
	}
	if withBackupRef {
		j.Status.BackupRef = &corev1.LocalObjectReference{Name: name}
	}
	return j
}

func newBucketBackupReconciler(t *testing.T, objs ...client.Object) *BackupJobReconciler {
	t.Helper()
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{backupsv1alpha1.AddToScheme, batchv1.AddToScheme} {
		if err := add(s); err != nil {
			t.Fatalf("AddToScheme: %v", err)
		}
	}
	c := clientfake.NewClientBuilder().WithScheme(s).
		WithStatusSubresource(&backupsv1alpha1.BackupJob{}).
		WithObjects(objs...).Build()
	return &BackupJobReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(10)}
}

func TestFinalizeBucketBackupJobReclaimsInFlight(t *testing.T) {
	// Cancelling a running backup (no Backup recorded) must reclaim the partial
	// copy: a reclaim Job is created, not owner-referenced to the deleting
	// BackupJob (GC would remove it first) and self-deleting via TTL, and the
	// finalizer is cleared so the delete proceeds.
	j := deletingBucketBackupJob(t, "bj1", true, false)
	r := newBucketBackupReconciler(t, j)
	if err := r.Delete(context.Background(), j); err != nil {
		t.Fatalf("delete (sets DeletionTimestamp; finalizer holds it): %v", err)
	}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(j), j); err != nil {
		t.Fatalf("re-get deleting backupjob: %v", err)
	}

	if _, err := r.finalizeBucketBackupJob(context.Background(), j); err != nil {
		t.Fatalf("finalizeBucketBackupJob: %v", err)
	}
	reclaim := &batchv1.Job{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "tenant-x", Name: "bj1-reclaim-cancel"}, reclaim); err != nil {
		t.Fatalf("reclaim Job not created on delete: %v", err)
	}
	if reclaim.Spec.TTLSecondsAfterFinished == nil {
		t.Error("delete-time reclaim Job must self-delete via TTL")
	}
	if metav1.IsControlledBy(reclaim, j) {
		t.Error("reclaim Job must not be owner-referenced to the deleting BackupJob")
	}
	// Finalizer cleared -> the fake client removes the object.
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(j), &backupsv1alpha1.BackupJob{}); !apierrors.IsNotFound(err) {
		t.Fatalf("finalizer must be cleared so the delete completes (err=%v)", err)
	}
}

func TestFinalizeBucketBackupJobSkipsReclaimWhenBackupRecorded(t *testing.T) {
	// A recorded Backup owns its objects (cleanupBucketBackup reclaims them), so
	// finalizing a completed run must not create a second reclaim Job.
	j := deletingBucketBackupJob(t, "bj2", true, true)
	r := newBucketBackupReconciler(t, j)
	if err := r.Delete(context.Background(), j); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(j), j); err != nil {
		t.Fatalf("re-get: %v", err)
	}
	if _, err := r.finalizeBucketBackupJob(context.Background(), j); err != nil {
		t.Fatalf("finalizeBucketBackupJob: %v", err)
	}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "tenant-x", Name: "bj2-reclaim-cancel"}, &batchv1.Job{}); !apierrors.IsNotFound(err) {
		t.Fatalf("no reclaim Job expected when a Backup was recorded (err=%v)", err)
	}
}

func TestReconcileDeletingSucceededBucketBackupJobClearsFinalizer(t *testing.T) {
	// The finalizer is added when the mirror starts and never removed on success,
	// so a Succeeded Bucket BackupJob carries it for life. The deletion branch in
	// Reconcile sits above the terminal-phase early return and must clear it, or a
	// Succeeded Bucket BackupJob becomes undeletable and wedges its namespace.
	j := deletingBucketBackupJob(t, "bj-succ", true, true)
	j.Status.Phase = backupsv1alpha1.BackupJobPhaseSucceeded
	r := newBucketBackupReconciler(t, j)
	if err := r.Delete(context.Background(), j); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "tenant-x", Name: "bj-succ"}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "tenant-x", Name: "bj-succ"}, &backupsv1alpha1.BackupJob{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the deletion branch must clear the finalizer on a Succeeded BackupJob (err=%v)", err)
	}
}

func TestFinalizeBucketBackupJobWaitsForRunningMirror(t *testing.T) {
	// A cancelled in-flight run must not purge while the mirror pod may still be
	// writing: finalize deletes the mirror Job and holds the finalizer until it is
	// gone, creating no reclaim Job on the pass that still sees the mirror.
	j := deletingBucketBackupJob(t, "bj-run", true, false)
	mirror := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-x", Name: jobNameForBackupJob(j)}}
	r := newBucketBackupReconciler(t, j, mirror)
	if err := r.Delete(context.Background(), j); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(j), j); err != nil {
		t.Fatalf("re-get: %v", err)
	}
	res, err := r.finalizeBucketBackupJob(context.Background(), j)
	if err != nil {
		t.Fatalf("finalizeBucketBackupJob: %v", err)
	}
	if res.RequeueAfter <= 0 {
		t.Fatalf("must requeue while the mirror Job is still present, got %+v", res)
	}
	fresh := &backupsv1alpha1.BackupJob{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(j), fresh); err != nil {
		t.Fatalf("BackupJob must still exist while waiting for the mirror: %v", err)
	}
	if !controllerutil.ContainsFinalizer(fresh, bucketBackupFinalizer) {
		t.Fatal("finalizer must be held while the mirror is still being stopped")
	}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "tenant-x", Name: "bj-run-reclaim-cancel"}, &batchv1.Job{}); !apierrors.IsNotFound(err) {
		t.Fatalf("must not reclaim before the mirror Job is gone (err=%v)", err)
	}
}

func TestCleanupBucketBackupReleasesAfterDeadline(t *testing.T) {
	// A cleanup Job whose Pod keeps failing must not churn a Job/Pod pair forever
	// with the Backup stuck Terminating. Once the Backup has been deleting past the
	// deadline, release it - surfacing the ArtifactNotDeleted Event - instead of
	// retrying silently.
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{backupsv1alpha1.AddToScheme, batchv1.AddToScheme, strategyv1alpha1.AddToScheme} {
		if err := add(s); err != nil {
			t.Fatalf("AddToScheme: %v", err)
		}
	}
	raw, err := marshalBucketSnapshot(bucketBackupSnapshot{
		Kind: bucketSnapshotKind, RepoBucket: "cozy-backups", RepoEndpoint: "http://s3", RepoPrefix: "tenant-x/web/bk1/",
	})
	if err != nil {
		t.Fatalf("marshalBucketSnapshot: %v", err)
	}
	old := metav1.NewTime(time.Now().Add(-StrategyNotReadyDeadline - time.Minute))
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-x", Name: "bk1", UID: "backup-uid", DeletionTimestamp: &old, Finalizers: []string{"backups.cozystack.io/cleanup"}},
		Spec:       backupsv1alpha1.BackupSpec{StrategyRef: corev1.TypedLocalObjectReference{Kind: strategyv1alpha1.BucketStrategyKind, Name: "cozy-default-bucket"}},
		Status:     backupsv1alpha1.BackupStatus{UnderlyingResources: raw},
	}
	strategy := &strategyv1alpha1.Bucket{ObjectMeta: metav1.ObjectMeta{Name: "cozy-default-bucket"}, Spec: strategyv1alpha1.BucketSpec{Template: strategyv1alpha1.BucketTemplate{Image: "controller:latest"}}}
	yes := true
	failedCleanup := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "tenant-x", Name: "bk1-cleanup",
			Labels:          map[string]string{bucketLabelMode: bucketModeCleanup},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: backupsv1alpha1.GroupVersion.String(), Kind: "Backup", Name: "bk1", UID: "backup-uid", Controller: &yes}},
		},
		Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}},
	}
	rec := record.NewFakeRecorder(10)
	c := clientfake.NewClientBuilder().WithScheme(s).WithObjects(strategy, failedCleanup).Build()
	r := &BackupReconciler{Client: c, Scheme: s, Recorder: rec}

	res, err := r.cleanupBucketBackup(context.Background(), backup)
	if err != nil {
		t.Fatalf("cleanupBucketBackup: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Fatalf("must not requeue after releasing past the deadline, got %+v", res)
	}
	select {
	case ev := <-rec.Events:
		if !strings.Contains(ev, "ArtifactNotDeleted") {
			t.Fatalf("event = %q, want ArtifactNotDeleted on release", ev)
		}
	default:
		t.Fatal("release past the deadline must emit an ArtifactNotDeleted Event")
	}
}

func TestReconcileBucketAccessErrorTransientVsTerminal(t *testing.T) {
	t.Run("transient access error requeues", func(t *testing.T) {
		// A flaky Get on the access path must requeue, not fail the run terminally.
		job, resolved, objs := bucketBackupScenario("uid-acc-t")
		objs = append(objs, job)
		failAccessGet := interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*buckettypes.BucketAccess); ok {
					return apierrors.NewInternalError(errTransientArtifactCreate)
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}
		r, _ := newBucketReconcileEnv(t, newBucketApp("web", "tenant-x"),
			clientfake.NewClientBuilder().WithObjects(objs...).WithInterceptorFuncs(failAccessGet))
		res, err := r.reconcileBucket(context.Background(), job, resolved)
		if err != nil {
			t.Fatalf("reconcileBucket: %v", err)
		}
		if res.RequeueAfter <= 0 {
			t.Fatalf("a transient access error must requeue, got %+v", res)
		}
		updated := &backupsv1alpha1.BackupJob{}
		if err := r.Get(context.Background(), client.ObjectKeyFromObject(job), updated); err != nil {
			t.Fatalf("get backupjob: %v", err)
		}
		if updated.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
			t.Fatal("a transient access error must not fail the run terminally")
		}
	})

	t.Run("unowned access is terminal", func(t *testing.T) {
		// A same-named BucketAccess owned by someone else is the one terminal case.
		job, resolved, objs := bucketBackupScenario("uid-acc-x")
		// Replace the granted access with an unowned same-named one (no managed-by).
		filtered := objs[:0]
		for _, o := range objs {
			if _, ok := o.(*buckettypes.BucketAccess); ok {
				continue
			}
			filtered = append(filtered, o)
		}
		squatted := bucketAccess("bucket-web-cozy-backup", nil, nil, buckettypes.BucketAccessSpec{
			BucketClaimName: "attacker", BucketAccessClassName: "attacker", Protocol: bucketProtocolS3, CredentialsSecretName: "bucket-web-cozy-backup",
		})
		filtered = append(filtered, job, squatted)
		r, _ := newBucketReconcileEnv(t, newBucketApp("web", "tenant-x"), clientfake.NewClientBuilder().WithObjects(filtered...))
		if _, err := r.reconcileBucket(context.Background(), job, resolved); err != nil {
			t.Fatalf("reconcileBucket: %v", err)
		}
		updated := &backupsv1alpha1.BackupJob{}
		if err := r.Get(context.Background(), client.ObjectKeyFromObject(job), updated); err != nil {
			t.Fatalf("get backupjob: %v", err)
		}
		if updated.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
			t.Fatalf("an unowned BucketAccess must fail terminally, got phase %q", updated.Status.Phase)
		}
	})
}

func TestRequeueBucketWaitingUsesOwnClock(t *testing.T) {
	// The precondition wait clocks off its own PreconditionPending condition, not
	// StartedAt (which also starts the multi-hour mirror deadline). A run whose
	// StartedAt is long past still gets a fresh grace window when a precondition
	// first goes unsatisfied.
	newJob := func(name string, conds []metav1.Condition) *backupsv1alpha1.BackupJob {
		staleStart := metav1.NewTime(time.Now().Add(-StrategyNotReadyDeadline - time.Hour))
		return &backupsv1alpha1.BackupJob{
			ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-x", Name: name},
			Status:     backupsv1alpha1.BackupJobStatus{StartedAt: &staleStart, Phase: backupsv1alpha1.BackupJobPhaseRunning, Conditions: conds},
		}
	}

	t.Run("fresh precondition requeues though StartedAt is long past", func(t *testing.T) {
		j := newJob("bw-fresh", nil)
		r := newBucketBackupReconciler(t, j)
		res, err := r.requeueBucketWaiting(context.Background(), j, "SourceAccessPending", "waiting")
		if err != nil {
			t.Fatalf("requeueBucketWaiting: %v", err)
		}
		if res.RequeueAfter <= 0 {
			t.Fatalf("a first-seen precondition must requeue regardless of StartedAt, got %+v", res)
		}
		updated := &backupsv1alpha1.BackupJob{}
		if err := r.Get(context.Background(), client.ObjectKeyFromObject(j), updated); err != nil {
			t.Fatalf("get: %v", err)
		}
		if updated.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
			t.Fatal("must not fail on the first precondition evaluation")
		}
		if meta.FindStatusCondition(updated.Status.Conditions, bucketPreconditionPendingCondition) == nil {
			t.Fatal("PreconditionPending condition must be stamped to start the clock")
		}
	})

	t.Run("spent precondition clock fails", func(t *testing.T) {
		old := metav1.NewTime(time.Now().Add(-StrategyNotReadyDeadline - time.Minute))
		j := newJob("bw-spent", []metav1.Condition{{
			Type: bucketPreconditionPendingCondition, Status: metav1.ConditionTrue,
			Reason: "SourceAccessPending", Message: "waiting", LastTransitionTime: old,
		}})
		r := newBucketBackupReconciler(t, j)
		if _, err := r.requeueBucketWaiting(context.Background(), j, "SourceAccessPending", "waiting"); err != nil {
			t.Fatalf("requeueBucketWaiting: %v", err)
		}
		updated := &backupsv1alpha1.BackupJob{}
		if err := r.Get(context.Background(), client.ObjectKeyFromObject(j), updated); err != nil {
			t.Fatalf("get: %v", err)
		}
		if updated.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
			t.Fatalf("a precondition unsatisfied past its own deadline must fail, got %q", updated.Status.Phase)
		}
	})
}
