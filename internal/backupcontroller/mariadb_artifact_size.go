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
// matching a dump: the timestamp in its file name comes from the backup Job
// pod's clock and is truncated to seconds, and its LastModified comes from
// the object store's clock, neither of which is the apiserver's.
const mariadbSizeProbeClockSkew = time.Minute

// mariadbArtifactSizeBytes measures the dump mariadb-operator uploaded for
// mdbBackup. The strategy prefix is shared by every backup of the
// application, so the dump is identified by both the timestamp in its file
// name and its LastModified falling inside the operator Backup's lifetime:
// the two come from different clocks, so a skewed pod clock alone cannot
// pull a neighbouring run's dump into the window. Returns 0 - leaving
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
		if !ok || !inWindow(ts, lower, upper) || !inWindow(obj.LastModified, lower, upper) {
			continue
		}
		matched = append(matched, obj)
	}
	if len(matched) != 1 {
		logger.Info("MariaDB backup dump not attributable to a single object; leaving size unset",
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

func inWindow(t, lower, upper time.Time) bool {
	return !t.Before(lower) && !t.After(upper)
}

// Compression tokens mariadb-operator puts in a dump file name: the
// algorithm before .sql in v25.10 (backup.<ts>.gzip.sql), the extension
// after it in later releases (backup.<ts>.sql.gz).
var (
	mariadbDumpAlgorithms = map[string]bool{"none": true, "bzip2": true, "gzip": true, "zstd": true}
	mariadbDumpExtensions = map[string]bool{"bz2": true, "gz": true, "zst": true}
)

// parseMariaDBLogicalBackupFile accepts the logical-backup file names
// mariadb-operator writes and returns the embedded timestamp.
func parseMariaDBLogicalBackupFile(name string) (time.Time, bool) {
	parts := strings.Split(name, ".")
	if parts[0] != "backup" {
		return time.Time{}, false
	}
	switch {
	case len(parts) == 3 && parts[2] == "sql":
	case len(parts) == 4 && parts[2] == "sql" && mariadbDumpExtensions[parts[3]]:
	case len(parts) == 4 && parts[3] == "sql" && mariadbDumpAlgorithms[parts[2]]:
	default:
		return time.Time{}, false
	}
	ts, err := time.Parse(time.RFC3339, parts[1])
	if err != nil {
		return time.Time{}, false
	}
	return ts, true
}
