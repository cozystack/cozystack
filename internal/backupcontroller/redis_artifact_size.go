// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
)

// redisArtifact locates the RDB snapshot the backup Job uploaded and returns
// an Artifact carrying its s3:// URI and size. The default Redis strategy
// takes its S3 coordinates from the projected backup credentials Secret, so
// the object is looked up there. Returns nil - leaving Status.Artifact
// absent - when the Secret cannot be read or the object is not found: a
// custom strategy that writes elsewhere must not get a URI pointing at an
// object that does not exist.
func (r *BackupJobReconciler) redisArtifact(ctx context.Context, namespace, objectKey string) *backupsv1alpha1.BackupArtifact {
	logger := getLogger(ctx)
	ctx, cancel := context.WithTimeout(ctx, artifactSizeProbeTimeout)
	defer cancel()

	bucket, conn, err := r.projectedS3Connection(ctx, namespace)
	if err != nil {
		logger.Info("cannot resolve backup credentials to locate the Redis snapshot", "objectKey", objectKey, "error", err)
		return nil
	}
	lister, err := r.s3Lister(conn)
	if err != nil {
		logger.Info("cannot build S3 client to locate the Redis snapshot", "objectKey", objectKey, "error", err)
		return nil
	}
	size, err := lister.StatObject(ctx, bucket, objectKey)
	if err != nil {
		logger.Info("cannot stat the Redis snapshot; leaving the artifact unset", "bucket", bucket, "objectKey", objectKey, "error", err)
		return nil
	}
	return &backupsv1alpha1.BackupArtifact{
		URI:       fmt.Sprintf("s3://%s/%s", bucket, objectKey),
		SizeBytes: size,
	}
}

// projectedS3Connection reads the backup credentials Secret the controller
// projects into the application namespace. Its endpoint is a bare host,
// which the strategy scripts reach over https.
func (r *BackupJobReconciler) projectedS3Connection(ctx context.Context, namespace string) (string, s3Connection, error) {
	var conn s3Connection
	name := r.CredentialsConfig.TargetSecretName
	if name == "" {
		return "", conn, errors.New("no projected credentials Secret is configured")
	}
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, secret); err != nil {
		return "", conn, err
	}
	value := func(key string) string { return string(secret.Data[key]) }

	bucket := value("bucketName")
	conn.AccessKey, conn.SecretKey = value("AWS_ACCESS_KEY_ID"), value("AWS_SECRET_ACCESS_KEY")
	if bucket == "" || value("endpoint") == "" || conn.AccessKey == "" || conn.SecretKey == "" {
		return "", conn, fmt.Errorf("secret %s/%s lacks bucketName, endpoint or access keys", namespace, name)
	}
	conn.Endpoint, conn.Secure = value("endpoint"), true
	if strings.Contains(conn.Endpoint, "://") {
		u, err := url.Parse(conn.Endpoint)
		if err != nil || u.Host == "" {
			return "", conn, fmt.Errorf("invalid endpoint %q in secret %s/%s", conn.Endpoint, namespace, name)
		}
		conn.Endpoint, conn.Secure = u.Host, u.Scheme == "https"
	}
	conn.Region = value("region")
	return bucket, conn, nil
}
