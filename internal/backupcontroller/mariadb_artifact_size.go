// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	"github.com/cozystack/cozystack/internal/backupcontroller/mariadbtypes"
)

const (
	mariadbSizeProbeTimeout = 30 * time.Second

	// Slack around the [created, completed] window of the operator Backup
	// when matching the timestamp mariadb-operator embeds in the dump file
	// name: that timestamp comes from the backup Job pod's clock, not the
	// apiserver's, and is truncated to seconds.
	mariadbSizeProbeClockSkew = time.Minute
)

type s3ObjectInfo struct {
	Key  string
	Size int64
}

type s3ObjectLister interface {
	ListObjects(ctx context.Context, bucket, prefix string) ([]s3ObjectInfo, error)
}

// mariadbArtifactSizeBytes measures the dump mariadb-operator uploaded for
// mdbBackup. The strategy prefix is shared by every backup of the
// application, so the dump is identified by the timestamp in its file name
// falling inside the operator Backup's lifetime. Returns 0 - leaving
// Artifact.SizeBytes absent - when the object store cannot be read or the
// window does not match exactly one dump: an unknown size must not be
// reported as a measured one.
func (r *BackupJobReconciler) mariadbArtifactSizeBytes(
	ctx context.Context,
	namespace string,
	s3 *strategyv1alpha1.MariaDBS3Template,
	mdbBackup *mariadbtypes.Backup,
	completedAt time.Time,
) int64 {
	logger := getLogger(ctx)
	ctx, cancel := context.WithTimeout(ctx, mariadbSizeProbeTimeout)
	defer cancel()

	newLister := r.newS3ObjectLister
	if newLister == nil {
		newLister = r.newMariaDBMinioLister
	}
	lister, err := newLister(ctx, namespace, s3)
	if err != nil {
		logger.Debug("cannot build S3 client to measure MariaDB backup size", "backup", mdbBackup.Name, "error", err)
		return 0
	}
	prefix := mariadbOperatorS3Prefix(s3.Prefix)
	objects, err := lister.ListObjects(ctx, s3.Bucket, prefix)
	if err != nil {
		logger.Debug("cannot list MariaDB backup objects to measure size", "backup", mdbBackup.Name, "error", err)
		return 0
	}

	lower := mdbBackup.CreationTimestamp.Add(-mariadbSizeProbeClockSkew)
	upper := completedAt.Add(mariadbSizeProbeClockSkew)
	var matched []s3ObjectInfo
	for _, obj := range objects {
		ts, ok := parseMariaDBLogicalBackupFile(strings.TrimPrefix(obj.Key, prefix))
		if !ok || ts.Before(lower) || ts.After(upper) {
			continue
		}
		matched = append(matched, obj)
	}
	if len(matched) != 1 {
		logger.Debug("MariaDB backup dump not attributable to a single object; leaving size unset",
			"backup", mdbBackup.Name, "candidates", len(matched))
		return 0
	}
	return matched[0].Size
}

// mariadbOperatorS3Prefix mirrors how mariadb-operator normalises
// spec.storage.s3.prefix before prepending it to object keys.
func mariadbOperatorS3Prefix(prefix string) string {
	if prefix == "" || prefix == "/" {
		return ""
	}
	if !strings.HasSuffix(prefix, "/") {
		return prefix + "/"
	}
	return prefix
}

// parseMariaDBLogicalBackupFile accepts the logical-backup file names
// mariadb-operator writes, backup.<RFC3339>.sql and
// backup.<RFC3339>.<compression>.sql, and returns the embedded timestamp.
func parseMariaDBLogicalBackupFile(name string) (time.Time, bool) {
	parts := strings.Split(name, ".")
	if len(parts) != 3 && len(parts) != 4 {
		return time.Time{}, false
	}
	if parts[0] != "backup" || parts[len(parts)-1] != "sql" {
		return time.Time{}, false
	}
	ts, err := time.Parse(time.RFC3339, parts[1])
	if err != nil {
		return time.Time{}, false
	}
	return ts, true
}

type minioObjectLister struct {
	client *minio.Client
}

func (l *minioObjectLister) ListObjects(ctx context.Context, bucket, prefix string) ([]s3ObjectInfo, error) {
	var out []s3ObjectInfo
	for obj := range l.client.ListObjects(ctx, bucket, minio.ListObjectsOptions{Prefix: prefix}) {
		if obj.Err != nil {
			return nil, obj.Err
		}
		out = append(out, s3ObjectInfo{Key: obj.Key, Size: obj.Size})
	}
	return out, nil
}

// newMariaDBMinioLister builds an S3 client from the same Secrets and TLS
// settings the operator Backup was rendered with, so the controller reads
// the bucket with the tenant's credentials rather than its own.
func (r *BackupJobReconciler) newMariaDBMinioLister(ctx context.Context, namespace string, s3 *strategyv1alpha1.MariaDBS3Template) (s3ObjectLister, error) {
	accessKey, err := r.mariadbSecretValue(ctx, namespace, s3.AccessKeyIDSecretKeyRef)
	if err != nil {
		return nil, err
	}
	secretKey, err := r.mariadbSecretValue(ctx, namespace, s3.SecretAccessKeySecretKeyRef)
	if err != nil {
		return nil, err
	}
	var sessionToken string
	if s3.SessionTokenSecretKeyRef != nil {
		if sessionToken, err = r.mariadbSecretValue(ctx, namespace, *s3.SessionTokenSecretKeyRef); err != nil {
			return nil, err
		}
	}

	opts := &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, sessionToken),
		Region: s3.Region,
		Secure: s3.TLS != nil && s3.TLS.Enabled,
	}
	if opts.Secure && s3.TLS.CASecretKeyRef != nil {
		caPEM, err := r.mariadbSecretValue(ctx, namespace, *s3.TLS.CASecretKeyRef)
		if err != nil {
			return nil, err
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM([]byte(caPEM)) {
			return nil, fmt.Errorf("no PEM certificates in Secret %s/%s key %q", namespace, s3.TLS.CASecretKeyRef.Name, s3.TLS.CASecretKeyRef.Key)
		}
		opts.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	}
	client, err := minio.New(s3.Endpoint, opts)
	if err != nil {
		return nil, err
	}
	return &minioObjectLister{client: client}, nil
}

func (r *BackupJobReconciler) mariadbSecretValue(ctx context.Context, namespace string, ref strategyv1alpha1.MariaDBSecretKeySelector) (string, error) {
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: ref.Name}, secret); err != nil {
		return "", err
	}
	v, ok := secret.Data[ref.Key]
	if !ok {
		return "", fmt.Errorf("Secret %s/%s has no key %q", namespace, ref.Name, ref.Key)
	}
	return string(v), nil
}
