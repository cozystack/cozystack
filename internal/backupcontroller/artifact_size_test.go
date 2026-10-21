// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"errors"
	"testing"

	"github.com/minio/minio-go/v7"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type fakeS3Lister struct {
	objects []s3ObjectInfo
	err     error

	conn           s3Connection
	bucket, prefix string
	recursive      bool
	calls          int
}

func (f *fakeS3Lister) ListObjects(_ context.Context, bucket, prefix string, recursive bool) ([]s3ObjectInfo, error) {
	f.calls++
	f.bucket, f.prefix, f.recursive = bucket, prefix, recursive
	return f.objects, f.err
}

func s3CredsSecret(namespace, name string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Data: map[string][]byte{
			"AWS_ACCESS_KEY_ID":     []byte("AKID"),
			"AWS_SECRET_ACCESS_KEY": []byte("secret"),
		},
	}
}

// reconcilerWithLister returns a reconciler whose S3 reads go to l, backed
// by a fake client seeded with objs (typically the credentials Secret).
func reconcilerWithLister(t *testing.T, l *fakeS3Lister, objs ...client.Object) *BackupJobReconciler {
	t.Helper()
	c := newMariaDBStrategyTestClient(t, objs...)
	return &BackupJobReconciler{
		Client: c,
		Scheme: c.Scheme(),
		newS3ObjectLister: func(conn s3Connection) (s3ObjectLister, error) {
			l.conn = conn
			return l, nil
		},
	}
}

func TestCollectObjects(t *testing.T) {
	listing := func() <-chan minio.ObjectInfo {
		ch := make(chan minio.ObjectInfo, 1)
		ch <- minio.ObjectInfo{Key: "a", Size: 1}
		close(ch)
		return ch
	}

	got, err := collectObjects(context.Background(), listing())
	if err != nil || len(got) != 1 || got[0].Size != 1 {
		t.Fatalf("collectObjects = %v, %v; want one object", got, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := collectObjects(ctx, listing()); !errors.Is(err, context.Canceled) {
		t.Errorf("listing closed after cancellation: got %v, %v; want context.Canceled", got, err)
	}

	errCh := make(chan minio.ObjectInfo, 1)
	errCh <- minio.ObjectInfo{Err: errors.New("AccessDenied")}
	close(errCh)
	if _, err := collectObjects(context.Background(), errCh); err == nil {
		t.Error("listing error was not returned")
	}
}

func TestNewMinioObjectLister_RejectsEndpointPath(t *testing.T) {
	if _, err := newMinioObjectLister(s3Connection{Endpoint: "s3.example.org/prefix", Secure: true}); err == nil {
		t.Error("an endpoint with a path was accepted")
	}
	if _, err := newMinioObjectLister(s3Connection{Endpoint: "s3.example.org:9000", Secure: true}); err != nil {
		t.Errorf("host:port endpoint rejected: %v", err)
	}
}
