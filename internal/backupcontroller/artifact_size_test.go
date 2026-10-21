// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"testing"

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
}

func (f *fakeS3Lister) ListObjects(_ context.Context, bucket, prefix string, recursive bool) ([]s3ObjectInfo, error) {
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
