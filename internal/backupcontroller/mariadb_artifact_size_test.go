// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	"github.com/cozystack/cozystack/internal/backupcontroller/mariadbapp"
	"github.com/cozystack/cozystack/internal/backupcontroller/mariadbtypes"
)

func dumpKey(prefix string, ts time.Time, compression string) string {
	if compression == "" {
		return fmt.Sprintf("%sbackup.%s.sql", prefix, ts.UTC().Format(time.RFC3339))
	}
	return fmt.Sprintf("%sbackup.%s.%s.sql", prefix, ts.UTC().Format(time.RFC3339), compression)
}

func TestParseMariaDBLogicalBackupFile(t *testing.T) {
	cases := map[string]bool{
		"backup.2026-10-21T10:00:00Z.sql":        true,
		"backup.2026-10-21T10:00:00Z.gzip.sql":   true,
		"backup.2026-10-21T10:00:00+04:00.sql":   true,
		"backup.2026-10-21T10:00:00Z.sql.tmp":    false,
		"backup.2026-10-21T10:00:00Z.sql.gz":     true,
		"backup.2026-10-21T10:00:00Z.sql.zst":    true,
		"backup.2026-10-21T10:00:00Z.zstd.sql":   true,
		"backup.2026-10-21T10:00:00Z.xz.sql":     false,
		"backup.2026-10-21T10:00:00Z.sql.gz.tmp": false,
		"physicalbackup-20261021100000.xb":       false,
		"backup.not-a-date.sql":                  false,
		"sub/backup.2026-10-21T10:00:00Z.sql":    false,
		"backup.2026-10-21T10:00:00Z.a.gzip.sql": false,
		"backups.2026-10-21T10:00:00Z.gzip.sql":  false,
		"backup.2026-10-21T10:00:00Z.gzip.dump":  false,
		"":                                       false,
	}
	for name, want := range cases {
		if _, got := parseMariaDBLogicalBackupFile(name); got != want {
			t.Errorf("parseMariaDBLogicalBackupFile(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestMariaDBOperatorS3Prefix(t *testing.T) {
	cases := map[string]string{"": "", "/": "", "ns/app": "ns/app/", "ns/app/": "ns/app/"}
	for in, want := range cases {
		if got := mariadbOperatorS3Prefix(in); got != want {
			t.Errorf("mariadbOperatorS3Prefix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMariaDBArtifactSizeBytes(t *testing.T) {
	created := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	completed := created.Add(3 * time.Minute)
	mdbBackup := &mariadbtypes.Backup{ObjectMeta: metav1.ObjectMeta{
		Namespace: "tenant", Name: "op-bk", CreationTimestamp: metav1.NewTime(created),
	}}
	s3 := newRenderedMariaDBTemplate().Storage.S3
	s3.Prefix = "tenant/db"
	const prefix = "tenant/db/"

	t.Run("single dump inside the backup window", func(t *testing.T) {
		l := &fakeS3Lister{objects: []s3ObjectInfo{
			{Key: dumpKey(prefix, created.Add(-24*time.Hour), "gzip"), Size: 111, LastModified: created.Add(-24 * time.Hour)},
			{Key: dumpKey(prefix, created.Add(time.Minute), "gzip"), Size: 4242, LastModified: created.Add(time.Minute)},
			{Key: prefix + "0-backup-target.txt", Size: 7},
		}}
		got := reconcilerWithLister(t, l, s3CredsSecret("tenant", "creds")).mariadbArtifactSizeBytes(context.Background(), "tenant", s3, mdbBackup, completed)
		if got != 4242 {
			t.Errorf("size = %d, want 4242", got)
		}
		if l.conn.AccessKey != "AKID" || l.conn.Endpoint != "s3.example:9000" {
			t.Errorf("connection = %+v, want the Secret's key and the rendered endpoint", l.conn)
		}
		if l.bucket != "bkt" || l.prefix != prefix || l.recursive {
			t.Errorf("listed %q/%q recursive=%v, want bkt/%q non-recursive", l.bucket, l.prefix, l.recursive, prefix)
		}
	})

	t.Run("reads the endpoint CA from its Secret", func(t *testing.T) {
		tlsS3 := newRenderedMariaDBTemplate().Storage.S3
		tlsS3.Prefix = "tenant/db"
		tlsS3.TLS = &strategyv1alpha1.MariaDBS3TLS{
			Enabled:        true,
			CASecretKeyRef: &strategyv1alpha1.MariaDBSecretKeySelector{Name: "ca", Key: "ca.crt"},
		}
		ca := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "ca"},
			Data:       map[string][]byte{"ca.crt": []byte("PEM")},
		}
		l := &fakeS3Lister{objects: []s3ObjectInfo{{Key: dumpKey(prefix, created.Add(time.Minute), "gzip"), Size: 3, LastModified: created.Add(time.Minute)}}}
		r := reconcilerWithLister(t, l, s3CredsSecret("tenant", "creds"), ca)
		if got := r.mariadbArtifactSizeBytes(context.Background(), "tenant", tlsS3, mdbBackup, completed); got != 3 {
			t.Errorf("size = %d, want 3", got)
		}
		if !l.conn.Secure || string(l.conn.CAPEM) != "PEM" {
			t.Errorf("connection = %+v, want TLS with the Secret's CA", l.conn)
		}
	})

	t.Run("reads the session token from its Secret", func(t *testing.T) {
		stsS3 := newRenderedMariaDBTemplate().Storage.S3
		stsS3.Prefix = "tenant/db"
		stsS3.SessionTokenSecretKeyRef = &strategyv1alpha1.MariaDBSecretKeySelector{Name: "sts", Key: "token"}
		sts := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "sts"},
			Data:       map[string][]byte{"token": []byte("TOKEN")},
		}
		l := &fakeS3Lister{objects: []s3ObjectInfo{{Key: dumpKey(prefix, created.Add(time.Minute), "gzip"), Size: 4, LastModified: created.Add(time.Minute)}}}
		r := reconcilerWithLister(t, l, s3CredsSecret("tenant", "creds"), sts)
		if got := r.mariadbArtifactSizeBytes(context.Background(), "tenant", stsS3, mdbBackup, completed); got != 4 {
			t.Errorf("size = %d, want 4", got)
		}
		if l.conn.SessionToken != "TOKEN" {
			t.Errorf("session token = %q, want the Secret's", l.conn.SessionToken)
		}
	})

	t.Run("pod clock skew within tolerance", func(t *testing.T) {
		l := &fakeS3Lister{objects: []s3ObjectInfo{
			{Key: dumpKey(prefix, created.Add(-30*time.Second), ""), Size: 9, LastModified: created.Add(time.Minute)},
		}}
		if got := reconcilerWithLister(t, l, s3CredsSecret("tenant", "creds")).mariadbArtifactSizeBytes(context.Background(), "tenant", s3, mdbBackup, completed); got != 9 {
			t.Errorf("size = %d, want 9", got)
		}
	})

	t.Run("a skewed pod clock does not attribute a neighbouring run's dump", func(t *testing.T) {
		// The pod clock runs 5m behind. This run's dump is named outside the
		// window; the previous run's dump, uploaded before this run started,
		// is named inside it.
		l := &fakeS3Lister{objects: []s3ObjectInfo{
			{Key: dumpKey(prefix, created.Add(-4*time.Minute), "gzip"), Size: 7, LastModified: created.Add(time.Minute)},
			{Key: dumpKey(prefix, created.Add(30*time.Second), "gzip"), Size: 666, LastModified: created.Add(-4 * time.Minute)},
		}}
		if got := reconcilerWithLister(t, l, s3CredsSecret("tenant", "creds")).mariadbArtifactSizeBytes(context.Background(), "tenant", s3, mdbBackup, completed); got != 0 {
			t.Errorf("size = %d, want 0: no dump is inside the window on both clocks", got)
		}
	})

	t.Run("a dump uploaded just after Complete is inside the slack", func(t *testing.T) {
		l := &fakeS3Lister{objects: []s3ObjectInfo{
			{Key: dumpKey(prefix, completed.Add(30*time.Second), "gzip"), Size: 5, LastModified: completed.Add(30 * time.Second)},
		}}
		if got := reconcilerWithLister(t, l, s3CredsSecret("tenant", "creds")).mariadbArtifactSizeBytes(context.Background(), "tenant", s3, mdbBackup, completed); got != 5 {
			t.Errorf("size = %d, want 5", got)
		}
	})

	t.Run("a hanging endpoint is given up at the probe timeout", func(t *testing.T) {
		r := reconcilerWithLister(t, &fakeS3Lister{}, s3CredsSecret("tenant", "creds"))
		r.newS3ObjectLister = func(s3Connection) (s3ObjectLister, error) { return blockingLister{}, nil }
		withShortProbeTimeout(t, func() {
			if got := r.mariadbArtifactSizeBytes(context.Background(), "tenant", s3, mdbBackup, completed); got != 0 {
				t.Errorf("size = %d, want 0", got)
			}
		})
	})

	t.Run("two dumps in the window leave size unset", func(t *testing.T) {
		l := &fakeS3Lister{objects: []s3ObjectInfo{
			{Key: dumpKey(prefix, created.Add(time.Minute), "gzip"), Size: 1, LastModified: created.Add(time.Minute)},
			{Key: dumpKey(prefix, created.Add(2*time.Minute), "gzip"), Size: 2, LastModified: created.Add(2 * time.Minute)},
		}}
		if got := reconcilerWithLister(t, l, s3CredsSecret("tenant", "creds")).mariadbArtifactSizeBytes(context.Background(), "tenant", s3, mdbBackup, completed); got != 0 {
			t.Errorf("size = %d, want 0 (ambiguous)", got)
		}
	})

	t.Run("no dump in the window leaves size unset", func(t *testing.T) {
		l := &fakeS3Lister{objects: []s3ObjectInfo{
			{Key: dumpKey(prefix, completed.Add(time.Hour), "gzip"), Size: 1, LastModified: completed.Add(time.Hour)},
		}}
		if got := reconcilerWithLister(t, l, s3CredsSecret("tenant", "creds")).mariadbArtifactSizeBytes(context.Background(), "tenant", s3, mdbBackup, completed); got != 0 {
			t.Errorf("size = %d, want 0", got)
		}
	})

	t.Run("list error leaves size unset", func(t *testing.T) {
		l := &fakeS3Lister{err: errors.New("AccessDenied")}
		if got := reconcilerWithLister(t, l, s3CredsSecret("tenant", "creds")).mariadbArtifactSizeBytes(context.Background(), "tenant", s3, mdbBackup, completed); got != 0 {
			t.Errorf("size = %d, want 0", got)
		}
	})

	t.Run("missing credentials Secret leaves size unset", func(t *testing.T) {
		c := newMariaDBStrategyTestClient(t)
		r := &BackupJobReconciler{Client: c, Scheme: c.Scheme()}
		if got := r.mariadbArtifactSizeBytes(context.Background(), "tenant", s3, mdbBackup, completed); got != 0 {
			t.Errorf("size = %d, want 0", got)
		}
	})
}

// TestMariaDBArtifactSizeBytes_MinioClient drives the real minio client
// against a stub ListObjectsV2 endpoint, so the Secret wiring, prefix and
// response parsing are exercised together.
func TestMariaDBArtifactSizeBytes_MinioClient(t *testing.T) {
	created := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	key := dumpKey("tenant/db/", created.Add(time.Minute), "gzip")

	var gotPath, gotPrefix, gotDelimiter, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotPath, gotPrefix, gotAuth = req.URL.Path, req.URL.Query().Get("prefix"), req.Header.Get("Authorization")
		gotDelimiter = req.URL.Query().Get("delimiter")
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Name>bkt</Name><Prefix>tenant/db/</Prefix><KeyCount>1</KeyCount><MaxKeys>1000</MaxKeys><IsTruncated>false</IsTruncated>
  <Contents><Key>%s</Key><Size>31337</Size><ETag>"x"</ETag><LastModified>2026-10-21T10:01:00.000Z</LastModified></Contents>
  <CommonPrefixes><Prefix>tenant/db/srv/</Prefix></CommonPrefixes>
</ListBucketResult>`, key)
	}))
	defer srv.Close()

	c := newMariaDBStrategyTestClient(t, s3CredsSecret("tenant", "creds"))
	r := &BackupJobReconciler{Client: c, Scheme: c.Scheme()}

	s3 := newRenderedMariaDBTemplate().Storage.S3
	s3.Endpoint = strings.TrimPrefix(srv.URL, "http://")
	s3.Prefix = "tenant/db/"
	s3.Region = "us-east-1"
	mdbBackup := &mariadbtypes.Backup{ObjectMeta: metav1.ObjectMeta{
		Namespace: "tenant", Name: "op-bk", CreationTimestamp: metav1.NewTime(created),
	}}

	if got := r.mariadbArtifactSizeBytes(context.Background(), "tenant", s3, mdbBackup, created.Add(3*time.Minute)); got != 31337 {
		t.Fatalf("size = %d, want 31337", got)
	}
	if gotPath != "/bkt/" || gotPrefix != "tenant/db/" || gotDelimiter != "/" {
		t.Errorf("request path=%q prefix=%q delimiter=%q, want /bkt/, tenant/db/ and a non-recursive listing", gotPath, gotPrefix, gotDelimiter)
	}
	if !strings.Contains(gotAuth, "Credential=AKID/") {
		t.Errorf("request not signed with the Secret's access key: %q", gotAuth)
	}
}

func TestCreateMariaDBBackupArtifact_SizeBytes(t *testing.T) {
	apps := mariadbapp.GroupName
	completed := metav1.NewTime(time.Date(2026, 10, 21, 10, 3, 0, 0, time.UTC))
	mdbBackup := &mariadbtypes.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "tenant", Name: "src-op-bk",
			CreationTimestamp: metav1.NewTime(completed.Add(-3 * time.Minute)),
		},
		Status: mariadbtypes.BackupStatus{Conditions: []metav1.Condition{{
			Type: mariadbtypes.ConditionTypeComplete, Status: metav1.ConditionTrue, LastTransitionTime: completed,
		}}},
	}
	resolved := &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{Kind: "MariaDB", Name: "mariadb-strategy-default"},
		Parameters:  map[string]string{},
	}
	job := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj-size"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{Kind: "MariaDB", Name: "src", APIGroup: &apps},
		},
	}
	r := reconcilerWithLister(t, &fakeS3Lister{objects: []s3ObjectInfo{
		{Key: dumpKey("src/", completed.Add(-time.Minute), "gzip"), Size: 2048, LastModified: completed.Add(-time.Minute)},
	}}, job, s3CredsSecret("tenant", "creds"))
	rendered := newRenderedMariaDBTemplate()
	rendered.Storage.S3.Prefix = "src/"

	artefact, err := r.createMariaDBBackupArtifact(context.Background(), job, resolved, mdbBackup, rendered)
	if err != nil {
		t.Fatalf("createMariaDBBackupArtifact: %v", err)
	}
	if artefact.Status.Artifact == nil || artefact.Status.Artifact.SizeBytes != 2048 {
		t.Fatalf("Artifact = %#v, want SizeBytes 2048", artefact.Status.Artifact)
	}
}

func TestCreateMariaDBBackupArtifact_ExistingBackupSkipsProbe(t *testing.T) {
	apps := mariadbapp.GroupName
	job := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj-retry"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{Kind: "MariaDB", Name: "src", APIGroup: &apps},
		},
	}
	existing := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj-retry"},
		Status:     backupsv1alpha1.BackupStatus{Artifact: &backupsv1alpha1.BackupArtifact{URI: "s3://bkt/src/op-bk", SizeBytes: 99}},
	}
	l := &fakeS3Lister{}
	r := reconcilerWithLister(t, l, job, existing, s3CredsSecret("tenant", "creds"))
	rendered := newRenderedMariaDBTemplate()
	rendered.Storage.S3.Prefix = "src/"
	resolved := &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{Kind: "MariaDB", Name: "mariadb-strategy-default"},
		Parameters:  map[string]string{},
	}

	got, err := r.createMariaDBBackupArtifact(context.Background(), job, resolved, &mariadbtypes.Backup{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "op-bk"}}, rendered)
	if err != nil {
		t.Fatalf("createMariaDBBackupArtifact: %v", err)
	}
	if l.calls != 0 {
		t.Errorf("S3 listed %d times for a Backup that already exists", l.calls)
	}
	if got.Status.Artifact == nil || got.Status.Artifact.SizeBytes != 99 {
		t.Errorf("Artifact = %#v, want the existing one", got.Status.Artifact)
	}
}

func TestCreateMariaDBBackupArtifact_TransientRetryReadStillCreates(t *testing.T) {
	apps := mariadbapp.GroupName
	job := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj-transient"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{Kind: "MariaDB", Name: "src", APIGroup: &apps},
		},
	}
	r := reconcilerWithLister(t, &fakeS3Lister{}, job)
	r.APIReader = interceptor.NewClient(newMariaDBStrategyTestClient(t).(client.WithWatch), interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return errors.New("etcdserver: request timed out")
		},
	})
	got, err := r.createMariaDBBackupArtifact(context.Background(), job, &ResolvedBackupConfig{}, &mariadbtypes.Backup{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "op"}}, newRenderedMariaDBTemplate())
	if err != nil || got == nil || got.Name != "bj-transient" {
		t.Fatalf("createMariaDBBackupArtifact = %v, %v; a transient read must not fail the backup", got, err)
	}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(got), &backupsv1alpha1.Backup{}); err != nil {
		t.Errorf("the Backup was not created: %v", err)
	}
}

func TestCreateMariaDBBackupArtifact_AlreadyExistsRefetchReadsPastTheCache(t *testing.T) {
	apps := mariadbapp.GroupName
	job := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj-race"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{Kind: "MariaDB", Name: "src", APIGroup: &apps},
		},
	}
	backup := &backupsv1alpha1.Backup{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj-race"}}
	store := newMariaDBStrategyTestClient(t, job, backup)
	r := &BackupJobReconciler{Client: staleCache(t, store), APIReader: notFoundOnce(t, store), Scheme: store.Scheme()}
	got, err := r.createMariaDBBackupArtifact(context.Background(), job, &ResolvedBackupConfig{}, &mariadbtypes.Backup{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "op"}}, &strategyv1alpha1.MariaDBTemplate{})
	if err != nil || got == nil || got.Name != "bj-race" {
		t.Fatalf("createMariaDBBackupArtifact = %v, %v; want the Backup that already exists", got, err)
	}
}
