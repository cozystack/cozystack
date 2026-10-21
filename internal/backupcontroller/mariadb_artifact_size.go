// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"strings"
	"time"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	"github.com/cozystack/cozystack/internal/backupcontroller/mariadbtypes"
)

// Slack around the [created, completed] window of the operator Backup when
// matching the timestamp mariadb-operator embeds in the dump file name: that
// timestamp comes from the backup Job pod's clock, not the apiserver's, and
// is truncated to seconds.
const mariadbSizeProbeClockSkew = time.Minute

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
	ctx, cancel := context.WithTimeout(ctx, artifactSizeProbeTimeout)
	defer cancel()

	conn, err := r.mariadbS3Connection(ctx, namespace, s3)
	if err != nil {
		logger.Info("cannot resolve S3 credentials to measure MariaDB backup size", "backup", mdbBackup.Name, "error", err)
		return 0
	}
	lister, err := r.s3Lister(conn)
	if err != nil {
		logger.Info("cannot build S3 client to measure MariaDB backup size", "backup", mdbBackup.Name, "error", err)
		return 0
	}
	// Non-recursive, as mariadb-operator lists it: the default prefix is also
	// the default CNPG destinationPath, so an app of each kind with the same
	// name shares it and a recursive listing would walk the WAL archive.
	prefix := mariadbOperatorS3Prefix(s3.Prefix)
	objects, err := lister.ListObjects(ctx, s3.Bucket, prefix, false)
	if err != nil {
		logger.Info("cannot list MariaDB backup objects to measure size", "backup", mdbBackup.Name, "error", err)
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

// mariadbS3Connection resolves the Secrets and TLS settings the operator
// Backup was rendered with, so the bucket is read with the tenant's
// credentials rather than the controller's.
func (r *BackupJobReconciler) mariadbS3Connection(ctx context.Context, namespace string, s3 *strategyv1alpha1.MariaDBS3Template) (s3Connection, error) {
	conn := s3Connection{
		Endpoint: s3.Endpoint,
		Region:   s3.Region,
		Secure:   s3.TLS != nil && s3.TLS.Enabled,
	}
	var err error
	if conn.AccessKey, err = r.secretValue(ctx, namespace, s3.AccessKeyIDSecretKeyRef.Name, s3.AccessKeyIDSecretKeyRef.Key); err != nil {
		return conn, err
	}
	if conn.SecretKey, err = r.secretValue(ctx, namespace, s3.SecretAccessKeySecretKeyRef.Name, s3.SecretAccessKeySecretKeyRef.Key); err != nil {
		return conn, err
	}
	if ref := s3.SessionTokenSecretKeyRef; ref != nil {
		if conn.SessionToken, err = r.secretValue(ctx, namespace, ref.Name, ref.Key); err != nil {
			return conn, err
		}
	}
	if conn.Secure && s3.TLS.CASecretKeyRef != nil {
		ca, err := r.secretValue(ctx, namespace, s3.TLS.CASecretKeyRef.Name, s3.TLS.CASecretKeyRef.Key)
		if err != nil {
			return conn, err
		}
		conn.CAPEM = []byte(ca)
	}
	return conn, nil
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
