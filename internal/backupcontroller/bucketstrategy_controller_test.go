// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

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
		Kind:                        bucketSnapshotKind,
		SourceBucket:                "bucket-src",
		RepoBucket:                  "cozy-backups-xyz",
		RepoEndpoint:                "s3.example.com",
		RepoPrefix:                  "tenant-x/app/backup-1/",
		RepoRegion:                  "us-east-1",
		RepoCredentialsSecret:       "cozy-backups-creds",
		RepoCredentialsAccessKeyKey: "AWS_ACCESS_KEY_ID",
		RepoCredentialsSecretKeyKey: "AWS_SECRET_ACCESS_KEY",
		InsecureSkipVerify:          true,
		ServerSideCopy:              false,
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
