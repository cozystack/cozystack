// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

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

	backupPod := buildBucketMirrorPod(bucketModeBackup, tmpl, "bucket-web-cozy-backup", "tenant-x/web/bk1/", false)
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
	restorePod := buildBucketMirrorPod(bucketModeRestore, tmpl, "bucket-web-cozy-restore", "tenant-x/web/bk1/", true)
	if !hasArg(restorePod.Spec.Containers[0].Args, "--delete-extraneous") {
		t.Errorf("in-place restore must set --delete-extraneous: %v", restorePod.Spec.Containers[0].Args)
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
