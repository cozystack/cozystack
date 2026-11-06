// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

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

// attributedMariaDBDump returns the dump mariadb-operator uploaded for
// mdbBackup. ok is false when the dump cannot be found or told apart, and
// the Backup then reports neither a size nor a dump: an unknown dump must
// not be reported as a measured one.
func (r *BackupJobReconciler) attributedMariaDBDump(ctx context.Context, mdbBackup *mariadbtypes.Backup, completedAt time.Time) (dump mariadbDump, ok bool) {
	ctx, cancel := context.WithTimeout(ctx, artifactSizeProbeTimeout)
	defer cancel()
	dump, err := findMariaDBDump(ctx, r.objectStore(), mdbBackup, completedAt)
	if err != nil {
		getLogger(ctx).Info("MariaDB dump not attributed; leaving its size and name unset", "backup", mdbBackup.Name, "error", err)
		return mariadbDump{}, false
	}
	return dump, true
}

// findMariaDBDump lists the bucket mdbBackup wrote to and returns the one
// dump that belongs to it, as matchMariaDBDump decides. The location and
// credentials come from mdbBackup's own spec: the strategy it was rendered
// from can have been edited since, and pointing at another bucket now says
// nothing about where this dump went.
func findMariaDBDump(ctx context.Context, store objectStore, mdbBackup *mariadbtypes.Backup, completedAt time.Time) (mariadbDump, error) {
	lister, s3, err := mariadbStorage(ctx, store, mdbBackup)
	if err != nil {
		return mariadbDump{}, err
	}
	others, err := finishedMariaDBRuns(ctx, store.reader, mdbBackup)
	if err != nil {
		return mariadbDump{}, fmt.Errorf("list operator Backups: %w", err)
	}
	// Non-recursive, as mariadb-operator lists it: the default prefix is also
	// the default CNPG destinationPath, so an app of each kind with the same
	// name shares it and a recursive listing would walk the WAL archive.
	prefix := mariadbOperatorS3Prefix(s3.Prefix)
	objects, err := lister.ListObjects(ctx, s3.Bucket, prefix, false)
	if err != nil {
		return mariadbDump{}, fmt.Errorf("list s3://%s/%s: %w", s3.Bucket, prefix, err)
	}
	own := newMariaDBRun(mdbBackup, mdbBackup.CreationTimestamp.Time, completedAt)
	return matchMariaDBDump(objects, prefix, own, others)
}

// mariadbDumpExists reports whether the dump file name is still in the
// storage mdbBackup wrote to.
func mariadbDumpExists(ctx context.Context, store objectStore, mdbBackup *mariadbtypes.Backup, name string) (bool, error) {
	lister, s3, err := mariadbStorage(ctx, store, mdbBackup)
	if err != nil {
		return false, err
	}
	key := mariadbOperatorS3Prefix(s3.Prefix) + name
	objects, err := lister.ListObjects(ctx, s3.Bucket, key, false)
	if err != nil {
		return false, fmt.Errorf("list s3://%s/%s: %w", s3.Bucket, key, err)
	}
	for _, obj := range objects {
		if obj.Key == key {
			return true, nil
		}
	}
	return false, nil
}

// mariadbStorage opens the bucket mdbBackup wrote to, with the credentials
// its own spec names.
func mariadbStorage(ctx context.Context, store objectStore, mdbBackup *mariadbtypes.Backup) (s3ObjectLister, *mariadbtypes.S3Storage, error) {
	s3 := mdbBackup.Spec.Storage.S3
	if s3 == nil {
		return nil, nil, errors.New("the operator Backup does not store to S3")
	}
	conn, err := mariadbS3Connection(ctx, store, mdbBackup.Namespace, s3)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve S3 credentials: %w", err)
	}
	lister, err := store.lister(conn)
	if err != nil {
		return nil, nil, fmt.Errorf("build S3 client: %w", err)
	}
	return lister, s3, nil
}

// mariadbRun is the span in which an operator Backup can have written its
// dump: its lifetime, widened by mariadbSizeProbeClockSkew on each side.
type mariadbRun struct {
	name         string
	lower, upper time.Time
}

func newMariaDBRun(b *mariadbtypes.Backup, created, completed time.Time) mariadbRun {
	return mariadbRun{
		name:  b.Namespace + "/" + b.Name,
		lower: created.Add(-mariadbSizeProbeClockSkew),
		upper: completed.Add(mariadbSizeProbeClockSkew),
	}
}

func (r mariadbRun) placesOnBothClocks(d mariadbDump) bool {
	return inWindow(d.Timestamp, r.lower, r.upper) && inWindow(d.LastModified, r.lower, r.upper)
}

func (r mariadbRun) placesOnEitherClock(d mariadbDump) bool {
	return inWindow(d.Timestamp, r.lower, r.upper) || inWindow(d.LastModified, r.lower, r.upper)
}

// matchMariaDBDump picks the dump of run own among objects, which share a
// prefix with every other run of the application. A dump carries two
// clocks, the backup pod's in its file name and the object store's in its
// LastModified, and either can be skewed. A dump is taken only when it is
// the one dump either clock places in own's window, both clocks place it
// there, and neither places it in the window of another finished run: a
// dump in the window on one clock only may be this run's own, misplaced by
// a skewed clock, which leaves any other candidate unproven. A neighbour's
// dump is still taken if this run's own is missing from the listing, or
// misplaced on both clocks, and the neighbour's operator Backup is gone or
// has not completed.
func matchMariaDBDump(objects []s3ObjectInfo, prefix string, own mariadbRun, others []mariadbRun) (mariadbDump, error) {
	var near []mariadbDump
	for _, obj := range objects {
		name := strings.TrimPrefix(obj.Key, prefix)
		ts, ok := parseMariaDBLogicalBackupFile(name)
		if !ok {
			continue
		}
		d := mariadbDump{Key: obj.Key, Name: name, Timestamp: ts, Size: obj.Size, LastModified: obj.LastModified}
		if own.placesOnEitherClock(d) {
			near = append(near, d)
		}
	}
	if len(near) == 0 {
		return mariadbDump{}, errMariaDBDumpNotFound
	}
	if len(near) > 1 {
		return mariadbDump{}, fmt.Errorf("%w: %d dumps fall in its lifetime on at least one clock", errMariaDBDumpAmbiguous, len(near))
	}
	d := near[0]
	if !own.placesOnBothClocks(d) {
		return mariadbDump{}, fmt.Errorf("%w: %s falls in its lifetime on one clock only", errMariaDBDumpAmbiguous, d.Name)
	}
	for _, other := range others {
		if other.placesOnEitherClock(d) {
			return mariadbDump{}, fmt.Errorf("%w: %s also falls in the lifetime of operator Backup %s", errMariaDBDumpAmbiguous, d.Name, other.name)
		}
	}
	return d, nil
}

// finishedMariaDBRuns returns the runs of the other operator Backups that
// write under the same bucket and prefix as b and have completed, whether
// their Job succeeded or failed. One still running is left out: its window
// has no end yet, and an operator Backup stuck short of Complete, as one
// whose Job pod never starts is, would otherwise contest every later run of
// the application for good. The endpoint is not compared, since one has
// several spellings, and a false neighbour only leaves a size unset.
func finishedMariaDBRuns(ctx context.Context, reader client.Reader, b *mariadbtypes.Backup) ([]mariadbRun, error) {
	list := &mariadbtypes.BackupList{}
	if err := reader.List(ctx, list); err != nil {
		return nil, err
	}
	var runs []mariadbRun
	for i := range list.Items {
		other := &list.Items[i]
		if other.Namespace == b.Namespace && other.Name == b.Name {
			continue
		}
		s3 := other.Spec.Storage.S3
		if s3 == nil || s3.Bucket != b.Spec.Storage.S3.Bucket || mariadbOperatorS3Prefix(s3.Prefix) != mariadbOperatorS3Prefix(b.Spec.Storage.S3.Prefix) {
			continue
		}
		cond := apimeta.FindStatusCondition(other.Status.Conditions, mariadbtypes.ConditionTypeComplete)
		if cond == nil || cond.Status != metav1.ConditionTrue {
			continue
		}
		runs = append(runs, newMariaDBRun(other, other.CreationTimestamp.Time, cond.LastTransitionTime.Time))
	}
	return runs, nil
}

// mariadbS3Connection resolves the Secrets and TLS settings of the operator
// Backup's storage, so the bucket is read with the tenant's credentials
// rather than the controller's.
func mariadbS3Connection(ctx context.Context, store objectStore, namespace string, s3 *mariadbtypes.S3Storage) (s3Connection, error) {
	conn := s3Connection{
		Endpoint: s3.Endpoint,
		Region:   s3.Region,
		Secure:   s3.TLS != nil && s3.TLS.Enabled,
	}
	var err error
	if conn.AccessKey, err = store.secretValue(ctx, namespace, s3.AccessKeyIdSecretKeyRef.Name, s3.AccessKeyIdSecretKeyRef.Key); err != nil {
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
