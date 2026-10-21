// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	"github.com/cozystack/cozystack/internal/backupcontroller/cnpgtypes"
)

func newSizedBarmanObjectStore() *strategyv1alpha1.BarmanObjectStoreTemplate {
	return &strategyv1alpha1.BarmanObjectStoreTemplate{
		DestinationPath: "s3://bkt/tenant/pg/",
		EndpointURL:     "https://s3.example:8443",
		S3Credentials: &strategyv1alpha1.S3CredentialsTemplate{
			SecretRef: corev1.LocalObjectReference{Name: "creds"},
		},
	}
}

func TestBarmanBaseBackupPrefix(t *testing.T) {
	cases := []struct{ path, want string }{
		{"tenant/pg", "tenant/pg/srv/base/20261021T100000/"},
		{"", "srv/base/20261021T100000/"},
	}
	for _, tc := range cases {
		if got := barmanBaseBackupPrefix(tc.path, "srv", "20261021T100000"); got != tc.want {
			t.Errorf("barmanBaseBackupPrefix(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestParseBarmanS3DestinationPath(t *testing.T) {
	cases := []struct {
		in, bucket, path string
		ok               bool
	}{
		{"s3://bkt/tenant/pg/", "bkt", "tenant/pg", true},
		{"s3://bkt", "bkt", "", true},
		{"s3://bkt/", "bkt", "", true},
		{"gs://bkt/x/", "", "", false},
		{"bkt/x/", "", "", false},
	}
	for _, tc := range cases {
		bucket, path, err := parseBarmanS3DestinationPath(tc.in)
		if (err == nil) != tc.ok || bucket != tc.bucket || path != tc.path {
			t.Errorf("parseBarmanS3DestinationPath(%q) = %q, %q, %v", tc.in, bucket, path, err)
		}
	}
}

func TestCNPGArtifactSizeBytes(t *testing.T) {
	const prefix = "tenant/pg/srv/base/20261021T100000/"

	t.Run("sums every object of the base backup", func(t *testing.T) {
		l := &fakeS3Lister{objects: []s3ObjectInfo{
			{Key: prefix + "backup.info", Size: 1},
			{Key: prefix + "data_0000.tar.gz", Size: 1000},
			{Key: prefix + "data_0001.tar.gz", Size: 200},
			{Key: prefix + "16385.tar.gz", Size: 30},
		}}
		r := reconcilerWithLister(t, l, s3CredsSecret("tenant", "creds"))
		got := r.cnpgArtifactSizeBytes(context.Background(), "tenant", newSizedBarmanObjectStore(), "srv", "20261021T100000")
		if got != 1231 {
			t.Errorf("size = %d, want 1231", got)
		}
		if l.bucket != "bkt" || l.prefix != prefix || !l.recursive {
			t.Errorf("listed %q/%q recursive=%v, want bkt/%q recursive", l.bucket, l.prefix, l.recursive, prefix)
		}
		if l.conn.Endpoint != "s3.example:8443" || !l.conn.Secure || l.conn.AccessKey != "AKID" || l.conn.SecretKey != "secret" {
			t.Errorf("connection = %+v", l.conn)
		}
	})

	t.Run("honours custom credential keys and the endpoint CA", func(t *testing.T) {
		store := newSizedBarmanObjectStore()
		store.S3Credentials.AccessKeyIDKey = "id"
		store.S3Credentials.SecretAccessKeyKey = "key"
		store.EndpointCA = &strategyv1alpha1.EndpointCARef{SecretRef: corev1.LocalObjectReference{Name: "ca"}}
		creds := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "creds"},
			Data:       map[string][]byte{"id": []byte("ID2"), "key": []byte("KEY2")},
		}
		ca := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "ca"},
			Data:       map[string][]byte{"ca.crt": []byte("PEM")},
		}
		l := &fakeS3Lister{objects: []s3ObjectInfo{{Key: prefix + "data.tar", Size: 5}}}
		r := reconcilerWithLister(t, l, creds, ca)
		if got := r.cnpgArtifactSizeBytes(context.Background(), "tenant", store, "srv", "20261021T100000"); got != 5 {
			t.Errorf("size = %d, want 5", got)
		}
		if l.conn.AccessKey != "ID2" || l.conn.SecretKey != "KEY2" || string(l.conn.CAPEM) != "PEM" {
			t.Errorf("connection = %+v", l.conn)
		}
	})

	unset := map[string]struct {
		store    func() *strategyv1alpha1.BarmanObjectStoreTemplate
		backupID string
		lister   *fakeS3Lister
		noSecret bool
	}{
		"unknown backup ID": {
			store: newSizedBarmanObjectStore, lister: &fakeS3Lister{objects: []s3ObjectInfo{{Key: "x", Size: 1}}},
		},
		"no objects under the prefix": {
			store: newSizedBarmanObjectStore, backupID: "id", lister: &fakeS3Lister{},
		},
		"list error": {
			store: newSizedBarmanObjectStore, backupID: "id", lister: &fakeS3Lister{err: errors.New("AccessDenied")},
		},
		"missing credentials Secret": {
			store: newSizedBarmanObjectStore, backupID: "id", noSecret: true,
			lister: &fakeS3Lister{objects: []s3ObjectInfo{{Key: "x", Size: 1}}},
		},
		"no s3Credentials": {
			store: func() *strategyv1alpha1.BarmanObjectStoreTemplate {
				s := newSizedBarmanObjectStore()
				s.S3Credentials = nil
				return s
			},
			backupID: "id", lister: &fakeS3Lister{objects: []s3ObjectInfo{{Key: "x", Size: 1}}},
		},
		"non-s3 destination": {
			store: func() *strategyv1alpha1.BarmanObjectStoreTemplate {
				s := newSizedBarmanObjectStore()
				s.DestinationPath = "azure://container/x"
				return s
			},
			backupID: "id", lister: &fakeS3Lister{objects: []s3ObjectInfo{{Key: "x", Size: 1}}},
		},
	}
	for name, tc := range unset {
		t.Run(name+" leaves size unset", func(t *testing.T) {
			var r *BackupJobReconciler
			if tc.noSecret {
				r = reconcilerWithLister(t, tc.lister)
			} else {
				r = reconcilerWithLister(t, tc.lister, s3CredsSecret("tenant", "creds"))
			}
			if got := r.cnpgArtifactSizeBytes(context.Background(), "tenant", tc.store(), "srv", tc.backupID); got != 0 {
				t.Errorf("size = %d, want 0", got)
			}
		})
	}
}

// TestCNPGArtifactSizeBytes_MinioClient drives the real minio client against
// a stub ListObjectsV2 endpoint, so the endpoint URL parsing, Secret wiring
// and response parsing are exercised together.
func TestCNPGArtifactSizeBytes_MinioClient(t *testing.T) {
	const prefix = "tenant/pg/srv/base/20261021T100000/"
	var gotPath, gotPrefix, gotDelimiter, gotAuth string
	srv := httptest.NewServer(cnpgListStub(prefix, func(req *http.Request) {
		gotPath, gotPrefix, gotAuth = req.URL.Path, req.URL.Query().Get("prefix"), req.Header.Get("Authorization")
		gotDelimiter = req.URL.Query().Get("delimiter")
	}))
	defer srv.Close()

	c := newMariaDBStrategyTestClient(t, s3CredsSecret("tenant", "creds"))
	r := &BackupJobReconciler{Client: c, Scheme: c.Scheme()}
	store := newSizedBarmanObjectStore()
	store.EndpointURL = srv.URL

	if got := r.cnpgArtifactSizeBytes(context.Background(), "tenant", store, "srv", "20261021T100000"); got != 4103 {
		t.Fatalf("size = %d, want 4103", got)
	}
	if gotPath != "/bkt/" || gotPrefix != prefix || gotDelimiter != "" {
		t.Errorf("request path=%q prefix=%q delimiter=%q, want /bkt/, %q and a recursive listing", gotPath, gotPrefix, gotDelimiter, prefix)
	}
	if !strings.Contains(gotAuth, "Credential=AKID/") {
		t.Errorf("request not signed with the Secret's access key: %q", gotAuth)
	}
}

func cnpgListStub(prefix string, seen func(*http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		if _, ok := req.URL.Query()["location"]; ok {
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/"></LocationConstraint>`)
			return
		}
		if seen != nil {
			seen(req)
		}
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Name>bkt</Name><Prefix>%[1]s</Prefix><KeyCount>2</KeyCount><MaxKeys>1000</MaxKeys><IsTruncated>false</IsTruncated>
  <Contents><Key>%[1]sbackup.info</Key><Size>7</Size><ETag>"a"</ETag><LastModified>2026-10-21T10:01:00.000Z</LastModified></Contents>
  <Contents><Key>%[1]sdata.tar.gz</Key><Size>4096</Size><ETag>"b"</ETag><LastModified>2026-10-21T10:01:00.000Z</LastModified></Contents>
</ListBucketResult>`, prefix)
	}
}

// TestCNPGArtifactSizeBytes_EndpointCA serves the listing over TLS with a
// self-signed certificate: the probe must trust it only through endpointCA.
func TestCNPGArtifactSizeBytes_EndpointCA(t *testing.T) {
	const prefix = "tenant/pg/srv/base/20261021T100000/"
	srv := httptest.NewTLSServer(cnpgListStub(prefix, nil))
	defer srv.Close()
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})

	ca := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "s3-ca"},
		Data:       map[string][]byte{"ca.crt": caPEM},
	}
	c := newMariaDBStrategyTestClient(t, s3CredsSecret("tenant", "creds"), ca)
	r := &BackupJobReconciler{Client: c, Scheme: c.Scheme()}
	store := newSizedBarmanObjectStore()
	store.EndpointURL = srv.URL

	if got := r.cnpgArtifactSizeBytes(context.Background(), "tenant", store, "srv", "20261021T100000"); got != 0 {
		t.Errorf("without endpointCA: size = %d, want 0 (untrusted certificate)", got)
	}
	store.EndpointCA = &strategyv1alpha1.EndpointCARef{SecretRef: corev1.LocalObjectReference{Name: "s3-ca"}}
	if got := r.cnpgArtifactSizeBytes(context.Background(), "tenant", store, "srv", "20261021T100000"); got != 4103 {
		t.Errorf("with endpointCA: size = %d, want 4103", got)
	}
}

func TestCreateCNPGBackupArtifact_SizeBytes(t *testing.T) {
	apiGroup := backupsv1alpha1.DefaultApplicationAPIGroup
	strategyGroup := strategyv1alpha1.GroupVersion.Group
	j := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj-size"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{APIGroup: &apiGroup, Kind: postgresAppKind, Name: "pg"},
		},
	}
	resolved := &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{APIGroup: &strategyGroup, Kind: strategyv1alpha1.CNPGStrategyKind, Name: "strat"},
	}
	cnpgBk := &cnpgtypes.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: "cnpg-bk"},
		Status:     cnpgtypes.BackupStatus{BackupID: "20261021T100000"},
	}
	rendered := &strategyv1alpha1.CNPGTemplate{BarmanObjectStore: *newSizedBarmanObjectStore()}
	l := &fakeS3Lister{objects: []s3ObjectInfo{{Key: "tenant/pg/srv/base/20261021T100000/data.tar.gz", Size: 777}}}
	r := reconcilerWithLister(t, l, s3CredsSecret("tenant", "creds"))
	r.Client = newCNPGStrategyTestClient(t, s3CredsSecret("tenant", "creds"))

	got, err := r.createCNPGBackupArtifact(context.Background(), j, resolved, cnpgBk, "postgres-pg", "srv", "postgresql", rendered, newPostgresApp("pg", "tenant"))
	if err != nil {
		t.Fatalf("createCNPGBackupArtifact: %v", err)
	}
	if got.Status.Artifact == nil || got.Status.Artifact.SizeBytes != 777 {
		t.Fatalf("Artifact = %#v, want SizeBytes 777", got.Status.Artifact)
	}
	if l.prefix != "tenant/pg/srv/base/20261021T100000/" {
		t.Errorf("listed prefix %q does not use the effective serverName", l.prefix)
	}
}
