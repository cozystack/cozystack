// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
)

// redisArtifact locates the RDB snapshot the backup Job uploaded and returns
// an Artifact carrying its s3:// URI and size. The default Redis strategy
// takes its S3 coordinates from the projected backup credentials Secret, so
// the object is looked up there, with the TLS trust the Job's pod applied
// (see redisS3Trust). Returns nil - leaving Status.Artifact absent - when
// the Secret cannot be read or the object is not found: a custom strategy
// that writes elsewhere must not get a URI pointing at an object that does
// not exist.
func (r *BackupJobReconciler) redisArtifact(ctx context.Context, namespace, objectKey string, podSpec *corev1.PodSpec) *backupsv1alpha1.BackupArtifact {
	logger := getLogger(ctx)
	ctx, cancel := context.WithTimeout(ctx, artifactSizeProbeTimeout)
	defer cancel()

	bucket, conn, err := r.projectedS3Connection(ctx, namespace)
	if err != nil {
		logger.Info("cannot resolve backup credentials to locate the Redis snapshot", "objectKey", objectKey, "error", err)
		return nil
	}
	if conn.CAPEM, conn.InsecureSkipVerify, err = r.redisS3Trust(ctx, namespace, podSpec); err != nil {
		logger.Info("cannot read the S3 endpoint CA to locate the Redis snapshot", "objectKey", objectKey, "error", err)
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
// projects into the application namespace. The projector always stores the
// endpoint as a bare host, which the strategy scripts reach over https.
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
	conn.Region = value("region")
	return bucket, conn, nil
}

const (
	redisS3CAVolume        = "s3-ca"
	redisS3InsecureSkipEnv = "S3_INSECURE_SKIP_VERIFY"
)

// redisS3Trust mirrors how the default strategy script verifies the S3
// endpoint: it trusts ca.crt, then tls.crt, from the Secret mounted as the
// s3-ca volume, and only when neither exists does it honour
// S3_INSECURE_SKIP_VERIFY=true. Both are read from the pod spec the backup
// Job ran with. A missing CA Secret is not an error, because the script
// mounts it optionally.
func (r *BackupJobReconciler) redisS3Trust(ctx context.Context, namespace string, podSpec *corev1.PodSpec) ([]byte, bool, error) {
	if podSpec == nil {
		return nil, false, nil
	}
	for _, v := range podSpec.Volumes {
		if v.Name != redisS3CAVolume || v.Secret == nil {
			continue
		}
		secret := &corev1.Secret{}
		err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: v.Secret.SecretName}, secret)
		if apierrors.IsNotFound(err) {
			break
		}
		if err != nil {
			return nil, false, err
		}
		for _, file := range []string{"ca.crt", "tls.crt"} {
			if pem := secret.Data[secretKeyForFile(v.Secret.Items, file)]; len(pem) > 0 {
				return pem, false, nil
			}
		}
	}
	for _, c := range podSpec.Containers {
		for _, e := range c.Env {
			if e.Name == redisS3InsecureSkipEnv && e.Value == "true" {
				return nil, true, nil
			}
		}
	}
	return nil, false, nil
}

// secretKeyForFile returns the Secret key a secret volume projects to file:
// the key itself when the volume lists no items.
func secretKeyForFile(items []corev1.KeyToPath, file string) string {
	if len(items) == 0 {
		return file
	}
	for _, it := range items {
		if it.Path == file {
			return it.Key
		}
	}
	return ""
}
