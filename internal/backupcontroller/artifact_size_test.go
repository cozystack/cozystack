// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
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

// TestExistingBackup_ReadsPastTheCache pins that the retry check sees a
// Backup the cache does not show yet: the previous reconcile created it
// moments earlier.
func TestExistingBackup_ReadsPastTheCache(t *testing.T) {
	backup := &backupsv1alpha1.Backup{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj"}}
	r := &BackupJobReconciler{
		Client:    newMariaDBStrategyTestClient(t),
		APIReader: newMariaDBStrategyTestClient(t, backup),
	}
	if got := r.existingBackup(context.Background(), "tenant", "bj"); got == nil {
		t.Fatal("existingBackup = nil; want the Backup the API server holds")
	}
}

// staleCache wraps a fake client so that Get reports NotFound for Backups,
// the way an informer cache does right after another writer created one.
func staleCache(t *testing.T, c client.Client) client.Client {
	t.Helper()
	return interceptor.NewClient(c.(client.WithWatch), interceptor.Funcs{
		Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*backupsv1alpha1.Backup); ok {
				return apierrors.NewNotFound(backupsv1alpha1.GroupVersion.WithResource("backups").GroupResource(), key.Name)
			}
			return cl.Get(ctx, key, obj, opts...)
		},
	})
}

// notFoundOnce wraps a fake client whose first Backup Get reports NotFound:
// the retry check runs before the Backup exists, the refetch after Create
// collides with it.
func notFoundOnce(t *testing.T, c client.Client) client.Client {
	t.Helper()
	missed := false
	return interceptor.NewClient(c.(client.WithWatch), interceptor.Funcs{
		Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*backupsv1alpha1.Backup); ok && !missed {
				missed = true
				return apierrors.NewNotFound(backupsv1alpha1.GroupVersion.WithResource("backups").GroupResource(), key.Name)
			}
			return cl.Get(ctx, key, obj, opts...)
		},
	})
}

func TestExistingBackup_ReadErrorIsNotFatal(t *testing.T) {
	failing := interceptor.NewClient(newMariaDBStrategyTestClient(t).(client.WithWatch), interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return errors.New("etcdserver: request timed out")
		},
	})
	r := &BackupJobReconciler{Client: newMariaDBStrategyTestClient(t), APIReader: failing}
	if got := r.existingBackup(context.Background(), "tenant", "bj"); got != nil {
		t.Errorf("existingBackup = %#v, want nil on a read error", got)
	}
}

// blockingLister answers nothing until the probe's context is done, the way
// a black-holed endpoint does.
type blockingLister struct{}

func (blockingLister) ListObjects(ctx context.Context, _, _ string, _ bool) ([]s3ObjectInfo, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// withShortProbeTimeout shortens the probe timeout and fails the test if fn
// is not back well before the default timeout would have expired.
func withShortProbeTimeout(t *testing.T, fn func()) {
	t.Helper()
	saved := artifactSizeProbeTimeout
	artifactSizeProbeTimeout = 50 * time.Millisecond
	defer func() { artifactSizeProbeTimeout = saved }()
	done := make(chan struct{})
	go func() {
		fn()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the probe did not give up at its timeout")
	}
}

// failingReads wraps a fake client whose every Get fails, like an apiserver
// that times out.
func failingReads(t *testing.T, c client.Client) client.Client {
	t.Helper()
	return interceptor.NewClient(c.(client.WithWatch), interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return errors.New("etcdserver: request timed out")
		},
	})
}

func TestFailBackupArtifact(t *testing.T) {
	newJob := func() *backupsv1alpha1.BackupJob {
		return &backupsv1alpha1.BackupJob{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj"}}
	}

	t.Run("a Backup that exists but cannot be read is retried", func(t *testing.T) {
		j := newJob()
		r := &BackupJobReconciler{Client: newMariaDBStrategyTestClient(t, j)}
		_, err := r.failBackupArtifact(context.Background(), j, fmt.Errorf("%w: timeout", errBackupNotReadBack))
		if !errors.Is(err, errBackupNotReadBack) {
			t.Errorf("err = %v, want it returned so the reconcile is retried", err)
		}
		if j.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
			t.Error("the BackupJob was failed although its Backup exists")
		}
	})

	t.Run("any other error fails the BackupJob", func(t *testing.T) {
		j := newJob()
		r := &BackupJobReconciler{Client: newMariaDBStrategyTestClient(t, j)}
		if _, err := r.failBackupArtifact(context.Background(), j, errors.New("admission webhook denied")); err != nil {
			t.Fatalf("failBackupArtifact: %v", err)
		}
		if j.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
			t.Errorf("phase = %q, want Failed", j.Status.Phase)
		}
	})
}
