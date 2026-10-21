// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

// artifactSizeProbeTimeout bounds the object-store round trips a driver
// makes to measure Backup.status.artifact.sizeBytes; the probe runs inside
// the reconcile that creates the Backup.
const artifactSizeProbeTimeout = 30 * time.Second

type s3ObjectInfo struct {
	Key  string
	Size int64
}

type s3ObjectLister interface {
	ListObjects(ctx context.Context, bucket, prefix string) ([]s3ObjectInfo, error)
}

// s3Connection is what a driver resolves from its rendered strategy to
// read the bucket with the same identity the backup was written with.
type s3Connection struct {
	// Endpoint is host[:port], without a scheme.
	Endpoint     string
	Secure       bool
	Region       string
	AccessKey    string
	SecretKey    string
	SessionToken string
	// CAPEM is trusted on top of the system pool when set.
	CAPEM []byte
}

func (r *BackupJobReconciler) s3Lister(conn s3Connection) (s3ObjectLister, error) {
	if r.newS3ObjectLister != nil {
		return r.newS3ObjectLister(conn)
	}
	return newMinioObjectLister(conn)
}

type minioObjectLister struct {
	client *minio.Client
}

func newMinioObjectLister(conn s3Connection) (s3ObjectLister, error) {
	opts := &minio.Options{
		Creds:  credentials.NewStaticV4(conn.AccessKey, conn.SecretKey, conn.SessionToken),
		Region: conn.Region,
		Secure: conn.Secure,
	}
	if conn.Secure && len(conn.CAPEM) > 0 {
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(conn.CAPEM) {
			return nil, errors.New("no PEM certificates in the endpoint CA bundle")
		}
		opts.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	}
	client, err := minio.New(conn.Endpoint, opts)
	if err != nil {
		return nil, err
	}
	return &minioObjectLister{client: client}, nil
}

func (l *minioObjectLister) ListObjects(ctx context.Context, bucket, prefix string) ([]s3ObjectInfo, error) {
	var out []s3ObjectInfo
	for obj := range l.client.ListObjects(ctx, bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if obj.Err != nil {
			return nil, obj.Err
		}
		out = append(out, s3ObjectInfo{Key: obj.Key, Size: obj.Size})
	}
	return out, nil
}

func (r *BackupJobReconciler) secretValue(ctx context.Context, namespace, name, key string) (string, error) {
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, secret); err != nil {
		return "", err
	}
	v, ok := secret.Data[key]
	if !ok {
		return "", fmt.Errorf("Secret %s/%s has no key %q", namespace, name, key)
	}
	return string(v), nil
}
