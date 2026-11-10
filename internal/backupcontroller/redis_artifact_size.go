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
	// The script passes the CA as curl --cacert, which replaces the default
	// trust store rather than adding to it.
	conn.CAOnly = true
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
// projects into the application namespace. The projector stores the endpoint
// without a scheme, and the strategy scripts reach it over https. It can
// still carry a path, which minio-go refuses.
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
// endpoint: it trusts ca.crt, then tls.crt, from the s3-ca volume, and only
// when neither exists does it honour S3_INSECURE_SKIP_VERIFY=true. Both are
// read from the pod spec the backup Job ran with, but the Secret is read
// now, so an s3-ca volume that yields no CA now - a Secret that is gone or
// lacks the keys, or a source other than a Secret - is refused: the Job may
// have verified against a CA that is gone, and neither the system roots nor
// skip-verify would trust as narrowly. The default strategy mounts s3-ca
// only when the admin configured an endpoint CA.
func (r *BackupJobReconciler) redisS3Trust(ctx context.Context, namespace string, podSpec *corev1.PodSpec) ([]byte, bool, error) {
	if podSpec == nil {
		return nil, false, nil
	}
	caUnknown := false
	for _, v := range podSpec.Volumes {
		if v.Name != redisS3CAVolume {
			continue
		}
		if v.Secret == nil {
			caUnknown = true
			break
		}
		secret := &corev1.Secret{}
		err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: v.Secret.SecretName}, secret)
		if apierrors.IsNotFound(err) {
			caUnknown = true
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
		caUnknown = true
	}
	if caUnknown {
		return nil, false, fmt.Errorf("the %s volume yields no CA now", redisS3CAVolume)
	}
	return nil, redisSkipsVerify(podSpec.Containers), nil
}

// redisSkipsVerify reports whether every container sets
// S3_INSECURE_SKIP_VERIFY=true: the probe cannot tell which container
// uploaded, so one that verified is enough to keep verification on. When a
// container lists the variable twice, the last entry wins, as in the
// kubelet.
func redisSkipsVerify(containers []corev1.Container) bool {
	for _, c := range containers {
		value := ""
		for _, e := range c.Env {
			if e.Name == redisS3InsecureSkipEnv {
				value = e.Value
			}
		}
		if value != "true" {
			return false
		}
	}
	return len(containers) > 0
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
