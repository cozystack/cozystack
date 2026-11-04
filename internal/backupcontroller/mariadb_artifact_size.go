// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"errors"
	"fmt"
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

var (
	errMariaDBDumpNotFound  = errors.New("no dump in the bucket belongs to the operator Backup")
	errMariaDBDumpAmbiguous = errors.New("the dump of the operator Backup cannot be told apart from another run's")
)

// mariadbDump is a logical dump mariadb-operator wrote to the bucket.
type mariadbDump struct {
	// Key is the object key; Name is the file name, Key without the prefix.
	Key  string
	Name string
	// Timestamp is the one in the file name, taken by the backup Job pod's
	// clock as the dump started.
	Timestamp time.Time
	Size      int64
	// LastModified is the object store's clock as the upload finished.
	LastModified time.Time
}

// mariadbArtifactSizeBytes measures the dump mariadb-operator uploaded for
// mdbBackup. Returns 0 - leaving Artifact.SizeBytes absent - when the dump
// cannot be found or told apart: an unknown size must not be reported as a
// measured one.
func (r *BackupJobReconciler) mariadbArtifactSizeBytes(
	ctx context.Context,
	namespace string,
	s3 *strategyv1alpha1.MariaDBS3Template,
	mdbBackup *mariadbtypes.Backup,
	completedAt time.Time,
) int64 {
	ctx, cancel := context.WithTimeout(ctx, artifactSizeProbeTimeout)
	defer cancel()
	dump, err := findMariaDBDump(ctx, r.objectStore(), namespace, s3, mdbBackup, completedAt)
	if err != nil {
		getLogger(ctx).Info("MariaDB backup size not measured; leaving it unset", "backup", mdbBackup.Name, "error", err)
		return 0
	}
	return dump.Size
}

// findMariaDBDump lists the bucket the operator Backup wrote to and returns
// the one dump that belongs to it, as matchMariaDBDump decides.
func findMariaDBDump(
	ctx context.Context,
	store objectStore,
	namespace string,
	s3 *strategyv1alpha1.MariaDBS3Template,
	mdbBackup *mariadbtypes.Backup,
	completedAt time.Time,
) (mariadbDump, error) {
	conn, err := mariadbS3Connection(ctx, store, namespace, s3)
	if err != nil {
		return mariadbDump{}, fmt.Errorf("resolve S3 credentials: %w", err)
	}
	lister, err := store.lister(conn)
	if err != nil {
		return mariadbDump{}, fmt.Errorf("build S3 client: %w", err)
	}
	// Non-recursive, as mariadb-operator lists it: the default prefix is also
	// the default CNPG destinationPath, so an app of each kind with the same
	// name shares it and a recursive listing would walk the WAL archive.
	prefix := mariadbOperatorS3Prefix(s3.Prefix)
	objects, err := lister.ListObjects(ctx, s3.Bucket, prefix, false)
	if err != nil {
		return mariadbDump{}, fmt.Errorf("list s3://%s/%s: %w", s3.Bucket, prefix, err)
	}
	return matchMariaDBDump(objects, prefix, mdbBackup.CreationTimestamp.Time, completedAt)
}

// matchMariaDBDump picks the dump of the run that lived from created to
// completed. The prefix is shared by every backup of the application, so
// the dump is identified by both the timestamp in its file name and its
// LastModified falling inside that lifetime: the two come from different
// clocks, so a skewed pod clock alone cannot pull a neighbouring run's dump
// into the window.
func matchMariaDBDump(objects []s3ObjectInfo, prefix string, created, completed time.Time) (mariadbDump, error) {
	lower := created.Add(-mariadbSizeProbeClockSkew)
	upper := completed.Add(mariadbSizeProbeClockSkew)
	var matched []mariadbDump
	for _, obj := range objects {
		name := strings.TrimPrefix(obj.Key, prefix)
		ts, ok := parseMariaDBLogicalBackupFile(name)
		if !ok || !inWindow(ts, lower, upper) || !inWindow(obj.LastModified, lower, upper) {
			continue
		}
		matched = append(matched, mariadbDump{Key: obj.Key, Name: name, Timestamp: ts, Size: obj.Size, LastModified: obj.LastModified})
	}
	switch len(matched) {
	case 0:
		return mariadbDump{}, errMariaDBDumpNotFound
	case 1:
		return matched[0], nil
	default:
		return mariadbDump{}, fmt.Errorf("%w: %d dumps fall in its lifetime", errMariaDBDumpAmbiguous, len(matched))
	}
}

// mariadbS3Connection resolves the Secrets and TLS settings the operator
// Backup was rendered with, so the bucket is read with the tenant's
// credentials rather than the controller's.
func mariadbS3Connection(ctx context.Context, store objectStore, namespace string, s3 *strategyv1alpha1.MariaDBS3Template) (s3Connection, error) {
	conn := s3Connection{
		Endpoint: s3.Endpoint,
		Region:   s3.Region,
		Secure:   s3.TLS != nil && s3.TLS.Enabled,
	}
	var err error
	if conn.AccessKey, err = store.secretValue(ctx, namespace, s3.AccessKeyIDSecretKeyRef.Name, s3.AccessKeyIDSecretKeyRef.Key); err != nil {
		return conn, err
	}
	if conn.SecretKey, err = store.secretValue(ctx, namespace, s3.SecretAccessKeySecretKeyRef.Name, s3.SecretAccessKeySecretKeyRef.Key); err != nil {
		return conn, err
	}
	if ref := s3.SessionTokenSecretKeyRef; ref != nil {
		if conn.SessionToken, err = store.secretValue(ctx, namespace, ref.Name, ref.Key); err != nil {
			return conn, err
		}
	}
	if conn.Secure && s3.TLS.CASecretKeyRef != nil {
		ca, err := store.secretValue(ctx, namespace, s3.TLS.CASecretKeyRef.Name, s3.TLS.CASecretKeyRef.Key)
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
