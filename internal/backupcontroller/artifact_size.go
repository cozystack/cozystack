// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
)

// artifactSizeProbeTimeout bounds the object-store round trips a driver
// makes to measure Backup.status.artifact.sizeBytes. The probe runs inside
// the reconcile that creates the Backup, and the BackupJob controller has a
// single worker, so an unreachable endpoint stalls every other BackupJob for
// this long. A variable so tests can shorten it.
var artifactSizeProbeTimeout = 10 * time.Second

type s3ObjectInfo struct {
	Key          string
	Size         int64
	LastModified time.Time
}

type s3ObjectLister interface {
	ListObjects(ctx context.Context, bucket, prefix string, recursive bool) ([]s3ObjectInfo, error)
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
		// Start from minio's transport to keep its proxy and timeout settings.
		tr, err := minio.DefaultTransport(true)
		if err != nil {
			return nil, err
		}
		pool := tr.TLSClientConfig.RootCAs
		if pool == nil {
			if pool, err = x509.SystemCertPool(); err != nil {
				pool = x509.NewCertPool()
			}
		}
		if !pool.AppendCertsFromPEM(conn.CAPEM) {
			return nil, errors.New("no PEM certificates in the endpoint CA bundle")
		}
		tr.TLSClientConfig.RootCAs = pool
		opts.Transport = tr
	}
	// minio.New refuses an endpoint with a path prefix, which it cannot
	// address, and accepts a bare trailing slash.
	client, err := minio.New(conn.Endpoint, opts)
	if err != nil {
		return nil, err
	}
	return &minioObjectLister{client: client}, nil
}

func (l *minioObjectLister) ListObjects(ctx context.Context, bucket, prefix string, recursive bool) ([]s3ObjectInfo, error) {
	return collectObjects(ctx, l.client.ListObjects(ctx, bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: recursive}))
}

// collectObjects drains a minio listing. minio-go ends a listing whose
// context is cancelled between pages by closing the channel without an
// error item, so a cancelled context is checked once the channel closes:
// otherwise a timeout would pass a partial listing off as complete.
func collectObjects(ctx context.Context, objects <-chan minio.ObjectInfo) ([]s3ObjectInfo, error) {
	var out []s3ObjectInfo
	for obj := range objects {
		if obj.Err != nil {
			return nil, obj.Err
		}
		out = append(out, s3ObjectInfo{Key: obj.Key, Size: obj.Size, LastModified: obj.LastModified})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// errBackupNotReadBack reports that Create collided with an existing Backup
// that could then not be read. The backup itself succeeded, so callers
// retry the reconcile instead of failing the BackupJob.
var errBackupNotReadBack = errors.New("the Backup already exists but could not be read back")

// failBackupArtifact handles an error from creating the Backup of a
// completed backup: errBackupNotReadBack is retried through the controller's
// backoff, anything else fails the BackupJob.
func (r *BackupJobReconciler) failBackupArtifact(ctx context.Context, j *backupsv1alpha1.BackupJob, err error) (ctrl.Result, error) {
	if errors.Is(err, errBackupNotReadBack) {
		return ctrl.Result{}, err
	}
	return r.markBackupJobFailed(ctx, j, fmt.Sprintf("failed to create Backup artifact: %v", err))
}

// existingBackup returns the Backup a previous reconcile already created
// for the job, so a retry skips the size probe whose result it would
// discard. It reads past the cache: a retry typically follows a Create made
// moments earlier, which the cache may not show yet. It returns nil both
// when there is no Backup and when the read fails: it only saves a probe,
// and the Create that follows handles an existing Backup on its own, so a
// transient read error must not fail a backup that succeeded.
func (r *BackupJobReconciler) existingBackup(ctx context.Context, namespace, name string) *backupsv1alpha1.Backup {
	existing := &backupsv1alpha1.Backup{}
	if err := r.apiReader().Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, existing); err != nil {
		if !apierrors.IsNotFound(err) {
			getLogger(ctx).Info("cannot check for an existing Backup; creating it", "backup", name, "error", err)
		}
		return nil
	}
	return existing
}

func (r *BackupJobReconciler) secretValue(ctx context.Context, namespace, name, key string) (string, error) {
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, secret); err != nil {
		return "", err
	}
	v, ok := secret.Data[key]
	if !ok {
		return "", fmt.Errorf("secret %s/%s has no key %q", namespace, name, key)
	}
	return string(v), nil
}
