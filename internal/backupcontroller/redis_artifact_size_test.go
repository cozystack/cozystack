// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
)

const redisTestKey = "tenant/cache/bj-1.rdb"

func projectedCredsSecret(data map[string]string) *corev1.Secret {
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "cozy-backups-creds"},
		Data:       map[string][]byte{},
	}
	for k, v := range map[string]string{
		"endpoint":              "s3.example.org",
		"bucketName":            "cozy-backups",
		"region":                "us-east-1",
		"AWS_ACCESS_KEY_ID":     "AKID",
		"AWS_SECRET_ACCESS_KEY": "secret",
	} {
		s.Data[k] = []byte(v)
	}
	for k, v := range data {
		if v == "" {
			delete(s.Data, k)
			continue
		}
		s.Data[k] = []byte(v)
	}
	return s
}

func redisReconciler(t *testing.T, l *fakeS3Lister, objs ...client.Object) *BackupJobReconciler {
	r := reconcilerWithLister(t, l, objs...)
	r.CredentialsConfig.TargetSecretName = "cozy-backups-creds"
	return r
}

func TestRedisArtifact(t *testing.T) {
	t.Run("stats the snapshot with the projected credentials", func(t *testing.T) {
		l := &fakeS3Lister{statSize: 2048}
		r := redisReconciler(t, l, projectedCredsSecret(nil))
		got := r.redisArtifact(context.Background(), "tenant", redisTestKey)
		if got == nil || got.URI != "s3://cozy-backups/"+redisTestKey || got.SizeBytes != 2048 {
			t.Fatalf("artifact = %#v", got)
		}
		if l.bucket != "cozy-backups" || l.statKey != redisTestKey {
			t.Errorf("stat %q/%q, want cozy-backups/%q", l.bucket, l.statKey, redisTestKey)
		}
		if l.conn.Endpoint != "s3.example.org" || !l.conn.Secure || l.conn.AccessKey != "AKID" || l.conn.Region != "us-east-1" {
			t.Errorf("connection = %+v", l.conn)
		}
	})

	t.Run("an endpoint with a scheme sets the transport", func(t *testing.T) {
		l := &fakeS3Lister{statSize: 1}
		r := redisReconciler(t, l, projectedCredsSecret(map[string]string{"endpoint": "http://seaweedfs:8333"}))
		if got := r.redisArtifact(context.Background(), "tenant", redisTestKey); got == nil {
			t.Fatal("artifact = nil")
		}
		if l.conn.Endpoint != "seaweedfs:8333" || l.conn.Secure {
			t.Errorf("connection = %+v", l.conn)
		}
	})

	unset := map[string]struct {
		lister   *fakeS3Lister
		objs     []client.Object
		noTarget bool
	}{
		"missing object":  {lister: &fakeS3Lister{statErr: errors.New("NoSuchKey")}, objs: []client.Object{projectedCredsSecret(nil)}},
		"missing Secret":  {lister: &fakeS3Lister{statSize: 1}},
		"missing bucket":  {lister: &fakeS3Lister{statSize: 1}, objs: []client.Object{projectedCredsSecret(map[string]string{"bucketName": ""})}},
		"missing keys":    {lister: &fakeS3Lister{statSize: 1}, objs: []client.Object{projectedCredsSecret(map[string]string{"AWS_SECRET_ACCESS_KEY": ""})}},
		"no target name":  {lister: &fakeS3Lister{statSize: 1}, objs: []client.Object{projectedCredsSecret(nil)}, noTarget: true},
		"broken endpoint": {lister: &fakeS3Lister{statSize: 1}, objs: []client.Object{projectedCredsSecret(map[string]string{"endpoint": "https://"})}},
	}
	for name, tc := range unset {
		t.Run(name+" leaves the artifact unset", func(t *testing.T) {
			r := redisReconciler(t, tc.lister, tc.objs...)
			if tc.noTarget {
				r.CredentialsConfig.TargetSecretName = ""
			}
			if got := r.redisArtifact(context.Background(), "tenant", redisTestKey); got != nil {
				t.Errorf("artifact = %#v, want nil", got)
			}
		})
	}
}

// TestRedisArtifact_MinioClient drives the real minio client against a stub
// that answers the HEAD request, so addressing, signing and the
// Content-Length parsing are exercised together.
func TestRedisArtifact_MinioClient(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotMethod, gotPath, gotAuth = req.Method, req.URL.Path, req.Header.Get("Authorization")
		w.Header().Set("Content-Length", "4096")
		w.Header().Set("ETag", `"abc"`)
		w.Header().Set("Last-Modified", "Wed, 21 Oct 2026 10:00:00 GMT")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	creds := projectedCredsSecret(map[string]string{"endpoint": srv.URL})
	c := newMariaDBStrategyTestClient(t, creds)
	r := &BackupJobReconciler{Client: c, Scheme: c.Scheme()}
	r.CredentialsConfig.TargetSecretName = "cozy-backups-creds"

	got := r.redisArtifact(context.Background(), "tenant", redisTestKey)
	if got == nil || got.SizeBytes != 4096 {
		t.Fatalf("artifact = %#v, want SizeBytes 4096", got)
	}
	if gotMethod != http.MethodHead || gotPath != "/cozy-backups/"+redisTestKey {
		t.Errorf("request %s %s, want HEAD /cozy-backups/%s", gotMethod, gotPath, redisTestKey)
	}
	if !strings.Contains(gotAuth, "Credential=AKID/") {
		t.Errorf("request not signed with the Secret's access key: %q", gotAuth)
	}
}

func TestCreateRedisBackupArtifact_Artifact(t *testing.T) {
	apiGroup := backupsv1alpha1.DefaultApplicationAPIGroup
	j := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj-1"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{APIGroup: &apiGroup, Kind: "Redis", Name: "cache"},
		},
	}

	t.Run("records the snapshot location and size", func(t *testing.T) {
		l := &fakeS3Lister{statSize: 777}
		r := redisReconciler(t, l, j, projectedCredsSecret(nil))
		got, err := r.createRedisBackupArtifact(context.Background(), j, &ResolvedBackupConfig{}, redisTestKey)
		if err != nil {
			t.Fatalf("createRedisBackupArtifact: %v", err)
		}
		if got.Status.Artifact == nil || got.Status.Artifact.SizeBytes != 777 || got.Status.Artifact.URI != "s3://cozy-backups/"+redisTestKey {
			t.Fatalf("Artifact = %#v", got.Status.Artifact)
		}
	})

	t.Run("an existing Backup is returned without probing", func(t *testing.T) {
		existing := &backupsv1alpha1.Backup{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj-1"}}
		l := &fakeS3Lister{statSize: 777}
		r := redisReconciler(t, l, j, existing, projectedCredsSecret(nil))
		if _, err := r.createRedisBackupArtifact(context.Background(), j, &ResolvedBackupConfig{}, redisTestKey); err != nil {
			t.Fatalf("createRedisBackupArtifact: %v", err)
		}
		if l.calls != 0 {
			t.Errorf("S3 probed %d times for a Backup that already exists", l.calls)
		}
	})
}
