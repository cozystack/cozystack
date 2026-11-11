// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	"github.com/cozystack/cozystack/internal/backupcontroller/mariadbapp"
	"github.com/cozystack/cozystack/internal/backupcontroller/mariadbtypes"
)

// fakeS3Bucket stands in for the object store on both the listing and the
// delete path, and records what was asked of it.
type fakeS3Bucket struct {
	objects   []s3ObjectInfo
	listErr   error
	removeErr error

	conns   []s3Connection
	removed []string
}

func (f *fakeS3Bucket) ListObjects(_ context.Context, _, _ string, _ bool) ([]s3ObjectInfo, error) {
	return f.objects, f.listErr
}

func (f *fakeS3Bucket) RemoveObject(_ context.Context, bucket, key string) error {
	if f.removeErr != nil {
		return f.removeErr
	}
	f.removed = append(f.removed, bucket+"/"+key)
	return nil
}

var (
	cleanupCreated   = time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	cleanupCompleted = cleanupCreated.Add(3 * time.Minute)
	cleanupDumpName  = "backup." + cleanupCreated.Add(time.Minute).Format(time.RFC3339) + ".gzip.sql"
)

const cleanupPrefix = "tenant/db/"

// cleanupOperatorBackup is the operator Backup a MariaDB Backup was made of:
// it stored to bkt under tenant/db with the tenant's creds Secret, and its
// Job succeeded.
func cleanupOperatorBackup(t *testing.T) *mariadbtypes.Backup {
	t.Helper()
	s3 := newRenderedMariaDBTemplate().Storage.S3
	s3.Prefix = "tenant/db"
	op := operatorBackupStoringTo(t, s3, cleanupCreated)
	op.Status.Conditions = []metav1.Condition{mariadbBackupSucceeded(metav1.NewTime(cleanupCompleted))}
	return op
}

// cleanupBackup is a MariaDB Backup being deleted, made of operator Backup
// op-bk, recording dumpFile when it is not empty.
func cleanupBackup(dumpFile string) *backupsv1alpha1.Backup {
	apiGroup := strategyv1alpha1.GroupVersion.Group
	md := map[string]string{
		mariadbBackupNameKey:      "op-bk",
		mariadbBackupNamespaceKey: "tenant",
	}
	if dumpFile != "" {
		md[mariadbDumpFileKey] = dumpFile
	}
	now := metav1.Now()
	return &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         "tenant",
			Name:              "bk",
			DeletionTimestamp: &now,
			Finalizers:        []string{backupFinalizer},
		},
		Spec: backupsv1alpha1.BackupSpec{
			StrategyRef: corev1.TypedLocalObjectReference{
				APIGroup: &apiGroup, Kind: strategyv1alpha1.MariaDBStrategyKind, Name: "cozy-default-mariadb",
			},
			TakenAt:        metav1.NewTime(cleanupCompleted),
			DriverMetadata: md,
		},
	}
}

func tenantNamespace() *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant"}}
}

func cleanupReconciler(t *testing.T, bucket *fakeS3Bucket, objs ...client.Object) (*BackupReconciler, *record.FakeRecorder) {
	t.Helper()
	return cleanupReconcilerWithClient(newMariaDBStrategyTestClient(t, objs...), bucket)
}

func cleanupReconcilerWithClient(c client.Client, bucket *fakeS3Bucket) (*BackupReconciler, *record.FakeRecorder) {
	rec := record.NewFakeRecorder(20)
	return &BackupReconciler{
		Client:   c,
		Scheme:   c.Scheme(),
		Recorder: rec,
		newS3ObjectLister: func(conn s3Connection) (s3ObjectLister, error) {
			bucket.conns = append(bucket.conns, conn)
			return bucket, nil
		},
		newS3ObjectRemover: func(conn s3Connection) (s3ObjectRemover, error) {
			bucket.conns = append(bucket.conns, conn)
			return bucket, nil
		},
	}, rec
}

func operatorBackupExists(t *testing.T, c client.Client) bool {
	t.Helper()
	err := c.Get(context.Background(), client.ObjectKey{Namespace: "tenant", Name: "op-bk"}, &mariadbtypes.Backup{})
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("get operator Backup: %v", err)
	}
	return err == nil
}

func requeues(res ctrl.Result) bool {
	return res.Requeue || res.RequeueAfter > 0
}

func hasEvent(events []string, prefix string) bool {
	for _, e := range events {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}

// assertReleasedUntouched checks a Backup that is let go with nothing
// deleted: no S3 delete, the operator Backup kept, and a Warning saying so.
func assertReleasedUntouched(t *testing.T, r *BackupReconciler, rec *record.FakeRecorder, bucket *fakeS3Bucket, res ctrl.Result, err error) {
	t.Helper()
	if err != nil || requeues(res) {
		t.Fatalf("got res=%+v err=%v, want the Backup released", res, err)
	}
	if len(bucket.removed) != 0 {
		t.Errorf("removed %v, want nothing deleted from the bucket", bucket.removed)
	}
	if !operatorBackupExists(t, r.Client) {
		t.Error("operator Backup deleted, want it kept")
	}
	if events := drainEvents(rec); !hasEvent(events, "Warning ArtifactNotDeleted") {
		t.Errorf("events = %v, want a Warning ArtifactNotDeleted", events)
	}
}

func TestCleanupMariaDBBackup_DeletesRecordedDumpThenOperatorBackup(t *testing.T) {
	bucket := &fakeS3Bucket{objects: []s3ObjectInfo{{Key: cleanupPrefix + cleanupDumpName, LastModified: cleanupCreated.Add(2 * time.Minute)}}}
	r, rec := cleanupReconciler(t, bucket, cleanupOperatorBackup(t), s3CredsSecret("tenant", "creds"), tenantNamespace())

	res, err := r.cleanupMariaDBBackup(context.Background(), cleanupBackup(cleanupDumpName))
	if err != nil || requeues(res) {
		t.Fatalf("got res=%+v err=%v, want the Backup released", res, err)
	}
	if want := []string{"bkt/" + cleanupPrefix + cleanupDumpName}; fmt.Sprint(bucket.removed) != fmt.Sprint(want) {
		t.Errorf("removed %v, want %v", bucket.removed, want)
	}
	if operatorBackupExists(t, r.Client) {
		t.Error("operator Backup kept, want it deleted once its dump is")
	}
	if len(bucket.conns) == 0 || bucket.conns[0].Endpoint != "s3.example:9000" || bucket.conns[0].AccessKey != "AKID" {
		t.Errorf("connections = %+v, want the operator Backup's endpoint and Secret", bucket.conns)
	}
	if events := drainEvents(rec); len(events) != 0 {
		t.Errorf("events = %v, want none", events)
	}
}

func TestCleanupMariaDBBackup_FailedDeleteKeepsOperatorBackupAndRetries(t *testing.T) {
	bucket := &fakeS3Bucket{removeErr: errors.New("503 SlowDown")}
	r, rec := cleanupReconciler(t, bucket, cleanupOperatorBackup(t), s3CredsSecret("tenant", "creds"), tenantNamespace())

	res, err := r.cleanupMariaDBBackup(context.Background(), cleanupBackup(cleanupDumpName))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !requeues(res) {
		t.Errorf("res = %+v, want the Backup held and retried", res)
	}
	if !operatorBackupExists(t, r.Client) {
		t.Error("operator Backup deleted although its dump was not")
	}
	events := drainEvents(rec)
	if !hasEvent(events, "Warning ArtifactCleanupFailed") || !strings.Contains(strings.Join(events, "\n"), mariadbSkipArtifactCleanupAnnotation) {
		t.Errorf("events = %v, want a Warning ArtifactCleanupFailed naming the skip annotation", events)
	}
}

func TestCleanupMariaDBBackup_FailedOperatorBackupDeleteRetries(t *testing.T) {
	bucket := &fakeS3Bucket{}
	c := interceptor.NewClient(newMariaDBStrategyTestClient(t, cleanupOperatorBackup(t), s3CredsSecret("tenant", "creds"), tenantNamespace()).(client.WithWatch), interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if _, ok := obj.(*mariadbtypes.Backup); ok {
				return errors.New("apiserver unavailable")
			}
			return c.Delete(ctx, obj, opts...)
		},
	})
	r, _ := cleanupReconcilerWithClient(c, bucket)

	if _, err := r.cleanupMariaDBBackup(context.Background(), cleanupBackup(cleanupDumpName)); err == nil {
		t.Fatal("err = nil, want the failed operator Backup delete returned so the Backup is held")
	}
}

// A legacy Backup records the dump it proved before deleting it, so a retry
// after a failed operator Backup delete finds the key by name instead of
// matching again against a bucket that no longer holds it.
func TestBackupReconcile_LegacyBackupRetryAfterAFailedOperatorBackupDelete(t *testing.T) {
	key := cleanupPrefix + cleanupDumpName
	bucket := &fakeS3Bucket{objects: []s3ObjectInfo{{Key: key, LastModified: cleanupCreated.Add(2 * time.Minute)}}}
	backup := cleanupBackup("")
	failDelete := true
	c := interceptor.NewClient(newMariaDBStrategyTestClient(t, backup, cleanupOperatorBackup(t), s3CredsSecret("tenant", "creds"), tenantNamespace()).(client.WithWatch), interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if _, ok := obj.(*mariadbtypes.Backup); ok && failDelete {
				return errors.New("apiserver unavailable")
			}
			return c.Delete(ctx, obj, opts...)
		},
	})
	r, rec := cleanupReconcilerWithClient(c, bucket)
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(backup)}

	if _, err := r.Reconcile(context.Background(), req); err == nil {
		t.Fatal("first pass: err = nil, want the failed operator Backup delete returned")
	}
	got := &backupsv1alpha1.Backup{}
	if err := r.Get(context.Background(), req.NamespacedName, got); err != nil {
		t.Fatalf("Backup gone after a failed operator Backup delete: %v", err)
	}
	if got.Spec.DriverMetadata[mariadbDumpFileKey] != cleanupDumpName {
		t.Errorf("driverMetadata = %v, want the proven dump recorded before its delete", got.Spec.DriverMetadata)
	}

	// The dump is gone now, as the first pass deleted it.
	bucket.objects = nil
	failDelete = false
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if operatorBackupExists(t, r.Client) {
		t.Error("operator Backup kept, want it deleted on the retry")
	}
	if err := r.Get(context.Background(), req.NamespacedName, &backupsv1alpha1.Backup{}); !apierrors.IsNotFound(err) {
		t.Errorf("Backup still present (err=%v), want its finalizer removed", err)
	}
	if events := drainEvents(rec); hasEvent(events, "Warning ArtifactNotDeleted") {
		t.Errorf("events = %v, want no claim that the deleted dump was left behind", events)
	}
}

func TestCleanupMariaDBBackup_LegacyBackupThatCannotRecordItsDumpDeletesNothing(t *testing.T) {
	key := cleanupPrefix + cleanupDumpName
	bucket := &fakeS3Bucket{objects: []s3ObjectInfo{{Key: key, LastModified: cleanupCreated.Add(2 * time.Minute)}}}
	backup := cleanupBackup("")
	c := interceptor.NewClient(newMariaDBStrategyTestClient(t, backup, cleanupOperatorBackup(t), s3CredsSecret("tenant", "creds"), tenantNamespace()).(client.WithWatch), interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			if _, ok := obj.(*backupsv1alpha1.Backup); ok {
				return errors.New("apiserver unavailable")
			}
			return c.Patch(ctx, obj, patch, opts...)
		},
	})
	r, _ := cleanupReconcilerWithClient(c, bucket)

	if _, err := r.cleanupMariaDBBackup(context.Background(), backup); err == nil {
		t.Fatal("err = nil, want the failed record returned so the Backup is held")
	}
	if len(bucket.removed) != 0 || !operatorBackupExists(t, r.Client) {
		t.Errorf("removed=%v operatorBackupExists=%v, want nothing deleted before the dump is recorded", bucket.removed, operatorBackupExists(t, r.Client))
	}
}

func TestCleanupMariaDBBackup_RejectsARecordedNameThatIsNotADump(t *testing.T) {
	for _, name := range []string{"../other/backup.2026-10-21T10:01:00Z.sql", "x.sql", "sub/" + cleanupDumpName} {
		t.Run(name, func(t *testing.T) {
			bucket := &fakeS3Bucket{}
			r, rec := cleanupReconciler(t, bucket, cleanupOperatorBackup(t), s3CredsSecret("tenant", "creds"), tenantNamespace())
			res, err := r.cleanupMariaDBBackup(context.Background(), cleanupBackup(name))
			assertReleasedUntouched(t, r, rec, bucket, res, err)
		})
	}
}

func TestCleanupMariaDBBackup_LegacyBackupDeletesTheProvenDump(t *testing.T) {
	key := cleanupPrefix + cleanupDumpName
	bucket := &fakeS3Bucket{objects: []s3ObjectInfo{
		{Key: dumpKey(cleanupPrefix, cleanupCreated.Add(-24*time.Hour), "gzip"), LastModified: cleanupCreated.Add(-24 * time.Hour)},
		{Key: key, LastModified: cleanupCreated.Add(2 * time.Minute)},
	}}
	backup := cleanupBackup("")
	r, rec := cleanupReconciler(t, bucket, backup, cleanupOperatorBackup(t), s3CredsSecret("tenant", "creds"), tenantNamespace())

	res, err := r.cleanupMariaDBBackup(context.Background(), backup)
	if err != nil || requeues(res) {
		t.Fatalf("got res=%+v err=%v, want the Backup released", res, err)
	}
	if want := []string{"bkt/" + key}; fmt.Sprint(bucket.removed) != fmt.Sprint(want) {
		t.Errorf("removed %v, want %v", bucket.removed, want)
	}
	if operatorBackupExists(t, r.Client) {
		t.Error("operator Backup kept, want it deleted once its dump is")
	}
	if events := drainEvents(rec); len(events) != 0 {
		t.Errorf("events = %v, want none", events)
	}
	got := &backupsv1alpha1.Backup{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(backup), got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.DriverMetadata[mariadbDumpFileKey] != cleanupDumpName || got.Spec.DriverMetadata[mariadbDumpTimeKey] != cleanupCreated.Add(time.Minute).Format(time.RFC3339) {
		t.Errorf("driverMetadata = %v, want the proven dump and its time recorded", got.Spec.DriverMetadata)
	}
}

// The window is the operator Backup's lifetime, closed by the Backup's
// takenAt: a dump stamped after takenAt plus the skew is not this run's.
func TestCleanupMariaDBBackup_LegacyWindowEndsAtTakenAt(t *testing.T) {
	late := cleanupCompleted.Add(mariadbSizeProbeClockSkew + time.Minute)
	bucket := &fakeS3Bucket{objects: []s3ObjectInfo{
		{Key: dumpKey(cleanupPrefix, late, "gzip"), LastModified: late},
	}}
	op := cleanupOperatorBackup(t)
	// The operator's own Complete transition is not what bounds the window.
	op.Status.Conditions = []metav1.Condition{mariadbBackupSucceeded(metav1.NewTime(late.Add(time.Hour)))}
	r, rec := cleanupReconciler(t, bucket, op, s3CredsSecret("tenant", "creds"), tenantNamespace())

	res, err := r.cleanupMariaDBBackup(context.Background(), cleanupBackup(""))
	assertReleasedUntouched(t, r, rec, bucket, res, err)
}

func TestCleanupMariaDBBackup_LegacyBackupWithoutAProvenDumpIsReleased(t *testing.T) {
	inWindow := cleanupCreated.Add(time.Minute)
	cases := map[string][]s3ObjectInfo{
		"no dump in its lifetime": {
			{Key: dumpKey(cleanupPrefix, cleanupCreated.Add(-24*time.Hour), "gzip"), LastModified: cleanupCreated.Add(-24 * time.Hour)},
		},
		"two dumps in its lifetime": {
			{Key: dumpKey(cleanupPrefix, inWindow, "gzip"), LastModified: inWindow},
			{Key: dumpKey(cleanupPrefix, inWindow.Add(30*time.Second), "gzip"), LastModified: inWindow.Add(30 * time.Second)},
		},
		"a dump placed by one clock only": {
			{Key: dumpKey(cleanupPrefix, inWindow, "gzip"), LastModified: inWindow.Add(time.Hour)},
		},
	}
	for name, objects := range cases {
		t.Run(name, func(t *testing.T) {
			bucket := &fakeS3Bucket{objects: objects}
			r, rec := cleanupReconciler(t, bucket, cleanupOperatorBackup(t), s3CredsSecret("tenant", "creds"), tenantNamespace())
			res, err := r.cleanupMariaDBBackup(context.Background(), cleanupBackup(""))
			assertReleasedUntouched(t, r, rec, bucket, res, err)
		})
	}
}

func TestCleanupMariaDBBackup_LegacyBackupWhoseDumpANeighbourCouldOwnIsReleased(t *testing.T) {
	inWindow := cleanupCreated.Add(time.Minute)
	bucket := &fakeS3Bucket{objects: []s3ObjectInfo{{Key: dumpKey(cleanupPrefix, inWindow, "gzip"), LastModified: inWindow}}}
	neighbour := cleanupOperatorBackup(t)
	neighbour.Name = "op-neighbour"
	neighbour.CreationTimestamp = metav1.NewTime(cleanupCreated.Add(30 * time.Second))
	r, rec := cleanupReconciler(t, bucket, cleanupOperatorBackup(t), neighbour, s3CredsSecret("tenant", "creds"), tenantNamespace())

	res, err := r.cleanupMariaDBBackup(context.Background(), cleanupBackup(""))
	assertReleasedUntouched(t, r, rec, bucket, res, err)
}

// A run started before this one completed can upload its dump and prune
// this run's through maxRetention before its own operator Backup completes.
// Its dump is then the only one in this run's window, and only its open
// window says it may be its own.
func TestCleanupMariaDBBackup_LegacyBackupWhoseDumpAnUnfinishedRunCouldOwnIsReleased(t *testing.T) {
	started := cleanupCreated.Add(2 * time.Minute)
	cases := map[string][]metav1.Condition{
		"running": {{
			Type: mariadbtypes.ConditionTypeComplete, Status: metav1.ConditionFalse,
			Reason: mariadbtypes.ConditionReasonJobRunning, Message: "Running", LastTransitionTime: metav1.NewTime(started),
		}},
		"not yet reconciled": nil,
	}
	for name, conditions := range cases {
		t.Run(name, func(t *testing.T) {
			bucket := &fakeS3Bucket{objects: []s3ObjectInfo{
				{Key: dumpKey(cleanupPrefix, started.Add(90*time.Second), "gzip"), LastModified: started.Add(100 * time.Second)},
			}}
			running := cleanupOperatorBackup(t)
			running.Name = "op-running"
			running.CreationTimestamp = metav1.NewTime(started)
			running.Status.Conditions = conditions
			backup := cleanupBackup("")
			r, rec := cleanupReconciler(t, bucket, backup, cleanupOperatorBackup(t), running, s3CredsSecret("tenant", "creds"), tenantNamespace())

			res, err := r.cleanupMariaDBBackup(context.Background(), backup)
			assertReleasedUntouched(t, r, rec, bucket, res, err)
		})
	}
}

// An unfinished run contests only what it could have written: a dump
// that predates it, on both clocks, is still this run's.
func TestCleanupMariaDBBackup_LegacyBackupDeletesItsDumpDespiteALaterUnfinishedRun(t *testing.T) {
	key := cleanupPrefix + cleanupDumpName
	bucket := &fakeS3Bucket{objects: []s3ObjectInfo{{Key: key, LastModified: cleanupCreated.Add(2 * time.Minute)}}}
	running := cleanupOperatorBackup(t)
	running.Name = "op-running"
	running.CreationTimestamp = metav1.NewTime(cleanupCompleted.Add(time.Hour))
	running.Status.Conditions = nil
	backup := cleanupBackup("")
	r, _ := cleanupReconciler(t, bucket, backup, cleanupOperatorBackup(t), running, s3CredsSecret("tenant", "creds"), tenantNamespace())

	res, err := r.cleanupMariaDBBackup(context.Background(), backup)
	if err != nil || requeues(res) {
		t.Fatalf("got res=%+v err=%v, want the Backup released", res, err)
	}
	if want := []string{"bkt/" + key}; fmt.Sprint(bucket.removed) != fmt.Sprint(want) {
		t.Errorf("removed %v, want %v", bucket.removed, want)
	}
}

// The size probe that records a dump lets only finished runs contest it, so
// a run still unfinished then, which uploaded its dump and pruned this
// run's through maxRetention, can have its dump recorded here. Deletion
// contests the recorded dump again, with every run as it stands now.
func TestCleanupMariaDBBackup_RecordedDumpARunUnfinishedAtTheProbeCouldOwnIsReleased(t *testing.T) {
	started := cleanupCreated.Add(time.Minute)
	theirs := dumpKey(cleanupPrefix, started.Add(30*time.Second), "gzip")
	cases := map[string][]metav1.Condition{
		"running": {{
			Type: mariadbtypes.ConditionTypeComplete, Status: metav1.ConditionFalse,
			Reason: mariadbtypes.ConditionReasonJobRunning, Message: "Running", LastTransitionTime: metav1.NewTime(started),
		}},
		"not yet reconciled": nil,
		"completed since":    {mariadbBackupSucceeded(metav1.NewTime(cleanupCompleted.Add(5 * time.Minute)))},
	}
	for name, conditions := range cases {
		t.Run(name, func(t *testing.T) {
			bucket := &fakeS3Bucket{objects: []s3ObjectInfo{{Key: theirs, LastModified: started.Add(time.Minute)}}}
			other := cleanupOperatorBackup(t)
			other.Name = "op-other"
			other.CreationTimestamp = metav1.NewTime(started)
			other.Status.Conditions = conditions
			r, rec := cleanupReconciler(t, bucket, cleanupOperatorBackup(t), other, s3CredsSecret("tenant", "creds"), tenantNamespace())

			res, err := r.cleanupMariaDBBackup(context.Background(), cleanupBackup(strings.TrimPrefix(theirs, cleanupPrefix)))
			assertReleasedUntouched(t, r, rec, bucket, res, err)
		})
	}
}

// Either clock placing the recorded dump in another run's lifetime is
// enough: the backup pod's clock can be skewed while the store's is not.
func TestCleanupMariaDBBackup_RecordedDumpAnotherRunPlacesOnOneClockIsReleased(t *testing.T) {
	stamped := cleanupCreated.Add(20 * time.Second)
	uploaded := cleanupCreated.Add(2 * time.Minute)
	theirs := dumpKey(cleanupPrefix, stamped, "gzip")
	bucket := &fakeS3Bucket{objects: []s3ObjectInfo{{Key: theirs, LastModified: uploaded}}}
	other := cleanupOperatorBackup(t)
	other.Name = "op-other"
	other.CreationTimestamp = metav1.NewTime(uploaded.Add(30 * time.Second))
	other.Status.Conditions = nil
	r, rec := cleanupReconciler(t, bucket, cleanupOperatorBackup(t), other, s3CredsSecret("tenant", "creds"), tenantNamespace())

	res, err := r.cleanupMariaDBBackup(context.Background(), cleanupBackup(strings.TrimPrefix(theirs, cleanupPrefix)))
	assertReleasedUntouched(t, r, rec, bucket, res, err)
}

// The listing is by prefix, so a longer key sharing it is not the dump and
// contests nothing.
func TestCleanupMariaDBBackup_RecordedDumpIgnoresAKeyItIsOnlyAPrefixOf(t *testing.T) {
	key := cleanupPrefix + cleanupDumpName
	bucket := &fakeS3Bucket{objects: []s3ObjectInfo{{Key: key + ".tmp", LastModified: cleanupCompleted.Add(2 * time.Hour)}}}
	running := cleanupOperatorBackup(t)
	running.Name = "op-running"
	running.CreationTimestamp = metav1.NewTime(cleanupCompleted.Add(time.Hour))
	running.Status.Conditions = nil
	r, _ := cleanupReconciler(t, bucket, cleanupOperatorBackup(t), running, s3CredsSecret("tenant", "creds"), tenantNamespace())

	res, err := r.cleanupMariaDBBackup(context.Background(), cleanupBackup(cleanupDumpName))
	if err != nil || requeues(res) {
		t.Fatalf("got res=%+v err=%v, want the Backup released", res, err)
	}
	if want := []string{"bkt/" + key}; fmt.Sprint(bucket.removed) != fmt.Sprint(want) {
		t.Errorf("removed %v, want %v", bucket.removed, want)
	}
}

// A name the dump time cannot be read from leaves one clock out of the
// contest, so it is an error rather than a contest on half the evidence.
func TestContestedMariaDBDump_RejectsANameWithoutADumpTime(t *testing.T) {
	op := cleanupOperatorBackup(t)
	name := "backup.not-a-time.sql"
	bucket := &fakeS3Bucket{objects: []s3ObjectInfo{{Key: cleanupPrefix + name, LastModified: cleanupCreated}}}
	c := newMariaDBStrategyTestClient(t, op)
	if _, _, err := contestedMariaDBDump(context.Background(), bucket, c, op, name); err == nil {
		t.Fatal("err = nil, want the unreadable dump time reported")
	}
}

// A retry after the dump was deleted but the operator Backup was not finds
// nothing to contest, whichever run's lifetime the dump's name falls in.
func TestCleanupMariaDBBackup_RecordedDumpAlreadyGoneIsNotContested(t *testing.T) {
	bucket := &fakeS3Bucket{}
	running := cleanupOperatorBackup(t)
	running.Name = "op-running"
	running.CreationTimestamp = metav1.NewTime(cleanupCreated)
	running.Status.Conditions = nil
	r, rec := cleanupReconciler(t, bucket, cleanupOperatorBackup(t), running, s3CredsSecret("tenant", "creds"), tenantNamespace())

	res, err := r.cleanupMariaDBBackup(context.Background(), cleanupBackup(cleanupDumpName))
	if err != nil || requeues(res) {
		t.Fatalf("got res=%+v err=%v, want the Backup released", res, err)
	}
	if operatorBackupExists(t, r.Client) {
		t.Error("operator Backup kept, want it deleted")
	}
	events := drainEvents(rec)
	if hasEvent(events, "Warning ArtifactNotDeleted") {
		t.Errorf("events = %v, want no ArtifactNotDeleted", events)
	}
	// The listing may have reached other storage than the dump went to, as
	// after an endpoint move, so the absence is reported with where it was
	// looked for rather than logged as a delete.
	if joined := strings.Join(events, "\n"); !hasEvent(events, "Normal ArtifactAlreadyAbsent") ||
		!strings.Contains(joined, "s3://bkt/"+cleanupPrefix+cleanupDumpName) || !strings.Contains(joined, "through s3.example:9000") {
		t.Errorf("events = %v, want a Normal ArtifactAlreadyAbsent naming the key and the endpoint listed", events)
	}
}

// A run started after the recorded dump was uploaded cannot have written it.
func TestCleanupMariaDBBackup_DeletesItsRecordedDumpDespiteALaterUnfinishedRun(t *testing.T) {
	key := cleanupPrefix + cleanupDumpName
	bucket := &fakeS3Bucket{objects: []s3ObjectInfo{{Key: key, LastModified: cleanupCreated.Add(2 * time.Minute)}}}
	running := cleanupOperatorBackup(t)
	running.Name = "op-running"
	running.CreationTimestamp = metav1.NewTime(cleanupCompleted.Add(time.Hour))
	running.Status.Conditions = nil
	r, _ := cleanupReconciler(t, bucket, cleanupOperatorBackup(t), running, s3CredsSecret("tenant", "creds"), tenantNamespace())

	res, err := r.cleanupMariaDBBackup(context.Background(), cleanupBackup(cleanupDumpName))
	if err != nil || requeues(res) {
		t.Fatalf("got res=%+v err=%v, want the Backup released", res, err)
	}
	if want := []string{"bkt/" + key}; fmt.Sprint(bucket.removed) != fmt.Sprint(want) {
		t.Errorf("removed %v, want %v", bucket.removed, want)
	}
	if operatorBackupExists(t, r.Client) {
		t.Error("operator Backup kept, want it deleted once its dump is")
	}
}

func TestCleanupMariaDBBackup_RecordedDumpListingFailureRetries(t *testing.T) {
	bucket := &fakeS3Bucket{listErr: errors.New("connection refused")}
	r, rec := cleanupReconciler(t, bucket, cleanupOperatorBackup(t), s3CredsSecret("tenant", "creds"), tenantNamespace())

	res, err := r.cleanupMariaDBBackup(context.Background(), cleanupBackup(cleanupDumpName))
	if err != nil || !requeues(res) {
		t.Fatalf("got res=%+v err=%v, want the Backup held and retried", res, err)
	}
	if len(bucket.removed) != 0 || !operatorBackupExists(t, r.Client) {
		t.Errorf("removed=%v operatorBackupExists=%v, want nothing deleted", bucket.removed, operatorBackupExists(t, r.Client))
	}
	if events := drainEvents(rec); !hasEvent(events, "Warning ArtifactCleanupFailed") {
		t.Errorf("events = %v, want a Warning ArtifactCleanupFailed", events)
	}
}

// Without the list of runs nothing is known about who else could have
// written the dump, so it is kept and the delete retried.
func TestCleanupMariaDBBackup_RecordedDumpWithUnreadableRunsRetries(t *testing.T) {
	key := cleanupPrefix + cleanupDumpName
	bucket := &fakeS3Bucket{objects: []s3ObjectInfo{{Key: key, LastModified: cleanupCreated.Add(2 * time.Minute)}}}
	c := interceptor.NewClient(newMariaDBStrategyTestClient(t, cleanupOperatorBackup(t), s3CredsSecret("tenant", "creds"), tenantNamespace()).(client.WithWatch), interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*mariadbtypes.BackupList); ok {
				return errors.New("apiserver unavailable")
			}
			return c.List(ctx, list, opts...)
		},
	})
	r, rec := cleanupReconcilerWithClient(c, bucket)

	res, err := r.cleanupMariaDBBackup(context.Background(), cleanupBackup(cleanupDumpName))
	if err != nil || !requeues(res) {
		t.Fatalf("got res=%+v err=%v, want the Backup held and retried", res, err)
	}
	if len(bucket.removed) != 0 || !operatorBackupExists(t, r.Client) {
		t.Errorf("removed=%v operatorBackupExists=%v, want nothing deleted", bucket.removed, operatorBackupExists(t, r.Client))
	}
	if events := drainEvents(rec); !hasEvent(events, "Warning ArtifactCleanupFailed") {
		t.Errorf("events = %v, want a Warning ArtifactCleanupFailed", events)
	}
}

// The whole sequence: run B uploads its dump and prunes this run's before
// B completes, this run's Backup is created and probed, then deleted. B's
// dump must survive whatever the probe recorded.
func TestMariaDBBackup_ProbeThenCleanupKeepsADumpAnUnfinishedRunUploaded(t *testing.T) {
	apps := mariadbapp.GroupName
	started := cleanupCreated.Add(time.Minute)
	theirs := s3ObjectInfo{Key: dumpKey(cleanupPrefix, started.Add(30*time.Second), "gzip"), Size: 42, LastModified: started.Add(time.Minute)}
	rendered := newRenderedMariaDBTemplate()
	rendered.Storage.S3.Prefix = "tenant/db"
	resolved := &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{Kind: "MariaDB", Name: "mariadb-strategy-default"},
		Parameters:  map[string]string{},
	}
	job := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bk"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{Kind: "MariaDB", Name: "db", APIGroup: &apps},
		},
	}
	other := func() *mariadbtypes.Backup {
		b := cleanupOperatorBackup(t)
		b.Name = "op-other"
		b.CreationTimestamp = metav1.NewTime(started)
		b.Status.Conditions = nil
		return b
	}

	prober := reconcilerWithLister(t, &fakeS3Lister{objects: []s3ObjectInfo{theirs}}, job, other(), s3CredsSecret("tenant", "creds"))
	created, err := prober.createMariaDBBackupArtifact(context.Background(), job, resolved, cleanupOperatorBackup(t), rendered)
	if err != nil {
		t.Fatalf("createMariaDBBackupArtifact: %v", err)
	}
	if got, want := created.Spec.DriverMetadata[mariadbDumpFileKey], strings.TrimPrefix(theirs.Key, cleanupPrefix); got != want {
		t.Fatalf("probe recorded dump %q, want %q: this test covers the recorded path", got, want)
	}

	backup := created.DeepCopy()
	backup.ResourceVersion = ""
	now := metav1.Now()
	backup.DeletionTimestamp = &now
	backup.Finalizers = []string{backupFinalizer}
	bucket := &fakeS3Bucket{objects: []s3ObjectInfo{theirs}}
	r, rec := cleanupReconciler(t, bucket, backup, cleanupOperatorBackup(t), other(), s3CredsSecret("tenant", "creds"), tenantNamespace())

	res, err := r.cleanupMariaDBBackup(context.Background(), backup)
	assertReleasedUntouched(t, r, rec, bucket, res, err)
}

func TestCleanupMariaDBBackup_LegacyListingFailureRetries(t *testing.T) {
	bucket := &fakeS3Bucket{listErr: errors.New("connection refused")}
	r, rec := cleanupReconciler(t, bucket, cleanupOperatorBackup(t), s3CredsSecret("tenant", "creds"), tenantNamespace())

	res, err := r.cleanupMariaDBBackup(context.Background(), cleanupBackup(""))
	if err != nil || !requeues(res) {
		t.Fatalf("got res=%+v err=%v, want the Backup held and retried", res, err)
	}
	if !operatorBackupExists(t, r.Client) {
		t.Error("operator Backup deleted, want it kept")
	}
	if events := drainEvents(rec); !hasEvent(events, "Warning ArtifactCleanupFailed") {
		t.Errorf("events = %v, want a Warning ArtifactCleanupFailed", events)
	}
}

func TestCleanupMariaDBBackup_ReleasesWhatItCannotAttribute(t *testing.T) {
	t.Run("operator Backup is gone", func(t *testing.T) {
		bucket := &fakeS3Bucket{}
		r, rec := cleanupReconciler(t, bucket, s3CredsSecret("tenant", "creds"), tenantNamespace())
		res, err := r.cleanupMariaDBBackup(context.Background(), cleanupBackup(cleanupDumpName))
		if err != nil || requeues(res) || len(bucket.removed) != 0 {
			t.Fatalf("got res=%+v err=%v removed=%v, want released with nothing deleted", res, err, bucket.removed)
		}
		if events := drainEvents(rec); !hasEvent(events, "Warning ArtifactNotDeleted") {
			t.Errorf("events = %v, want a Warning ArtifactNotDeleted", events)
		}
	})
	t.Run("Backup records no operator Backup", func(t *testing.T) {
		bucket := &fakeS3Bucket{}
		r, rec := cleanupReconciler(t, bucket, cleanupOperatorBackup(t), tenantNamespace())
		backup := cleanupBackup(cleanupDumpName)
		delete(backup.Spec.DriverMetadata, mariadbBackupNameKey)
		res, err := r.cleanupMariaDBBackup(context.Background(), backup)
		assertReleasedUntouched(t, r, rec, bucket, res, err)
	})
	t.Run("operator Backup's Job failed", func(t *testing.T) {
		bucket := &fakeS3Bucket{}
		op := cleanupOperatorBackup(t)
		op.Status.Conditions[0].Reason = mariadbtypes.ConditionReasonJobFailed
		r, rec := cleanupReconciler(t, bucket, op, s3CredsSecret("tenant", "creds"), tenantNamespace())
		res, err := r.cleanupMariaDBBackup(context.Background(), cleanupBackup(cleanupDumpName))
		assertReleasedUntouched(t, r, rec, bucket, res, err)
	})
	t.Run("operator Backup has no Complete condition", func(t *testing.T) {
		bucket := &fakeS3Bucket{}
		op := cleanupOperatorBackup(t)
		op.Status.Conditions = nil
		r, rec := cleanupReconciler(t, bucket, op, s3CredsSecret("tenant", "creds"), tenantNamespace())
		res, err := r.cleanupMariaDBBackup(context.Background(), cleanupBackup(cleanupDumpName))
		assertReleasedUntouched(t, r, rec, bucket, res, err)
	})
	t.Run("operator Backup never completed", func(t *testing.T) {
		bucket := &fakeS3Bucket{}
		op := cleanupOperatorBackup(t)
		op.Status.Conditions[0].Status = metav1.ConditionFalse
		r, rec := cleanupReconciler(t, bucket, op, s3CredsSecret("tenant", "creds"), tenantNamespace())
		res, err := r.cleanupMariaDBBackup(context.Background(), cleanupBackup(cleanupDumpName))
		assertReleasedUntouched(t, r, rec, bucket, res, err)
	})
	t.Run("dump is on a volume", func(t *testing.T) {
		bucket := &fakeS3Bucket{}
		op := cleanupOperatorBackup(t)
		op.Spec.Storage = mariadbtypes.BackupStorage{}
		r, rec := cleanupReconciler(t, bucket, op, tenantNamespace())
		res, err := r.cleanupMariaDBBackup(context.Background(), cleanupBackup(cleanupDumpName))
		assertReleasedUntouched(t, r, rec, bucket, res, err)
	})
}

func TestCleanupMariaDBBackup_SkipAnnotationDeletesNothing(t *testing.T) {
	bucket := &fakeS3Bucket{}
	r, _ := cleanupReconciler(t, bucket, cleanupOperatorBackup(t), s3CredsSecret("tenant", "creds"), tenantNamespace())
	backup := cleanupBackup(cleanupDumpName)
	backup.Annotations = map[string]string{mariadbSkipArtifactCleanupAnnotation: "true"}

	res, err := r.cleanupMariaDBBackup(context.Background(), backup)
	if err != nil || requeues(res) {
		t.Fatalf("got res=%+v err=%v, want the Backup released", res, err)
	}
	if len(bucket.removed) != 0 || !operatorBackupExists(t, r.Client) {
		t.Errorf("removed=%v operatorBackupExists=%v, want nothing deleted", bucket.removed, operatorBackupExists(t, r.Client))
	}
}

func restoreJobOf(name, backupName string, phase backupsv1alpha1.RestoreJobPhase) *backupsv1alpha1.RestoreJob {
	return &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: name},
		Spec:       backupsv1alpha1.RestoreJobSpec{BackupRef: corev1.LocalObjectReference{Name: backupName}},
		Status:     backupsv1alpha1.RestoreJobStatus{Phase: phase},
	}
}

func TestCleanupMariaDBBackup_WaitsForARestoreFromIt(t *testing.T) {
	for _, phase := range []backupsv1alpha1.RestoreJobPhase{backupsv1alpha1.RestoreJobPhaseEmpty, backupsv1alpha1.RestoreJobPhasePending, backupsv1alpha1.RestoreJobPhaseRunning} {
		t.Run(string(phase), func(t *testing.T) {
			bucket := &fakeS3Bucket{}
			r, rec := cleanupReconciler(t, bucket, cleanupOperatorBackup(t), s3CredsSecret("tenant", "creds"), tenantNamespace(),
				restoreJobOf("rj", "bk", phase))
			res, err := r.cleanupMariaDBBackup(context.Background(), cleanupBackup(cleanupDumpName))
			if err != nil || !requeues(res) {
				t.Fatalf("got res=%+v err=%v, want the Backup held while the restore runs", res, err)
			}
			if len(bucket.removed) != 0 || !operatorBackupExists(t, r.Client) {
				t.Errorf("removed=%v operatorBackupExists=%v, want nothing deleted during the restore", bucket.removed, operatorBackupExists(t, r.Client))
			}
			if events := drainEvents(rec); !hasEvent(events, "Normal ArtifactCleanupWaiting") || !strings.Contains(strings.Join(events, "\n"), mariadbSkipArtifactCleanupAnnotation) {
				t.Errorf("events = %v, want a Normal ArtifactCleanupWaiting naming the skip annotation", events)
			}
		})
	}
	// A restore checks that its Backup is not being deleted before it
	// starts, so one that started first is on the API server before the
	// delete, though not necessarily in this controller's cache yet.
	t.Run("a restore the cache has not seen yet holds it", func(t *testing.T) {
		bucket := &fakeS3Bucket{}
		r, _ := cleanupReconciler(t, bucket, cleanupOperatorBackup(t), s3CredsSecret("tenant", "creds"), tenantNamespace())
		r.APIReader = newMariaDBStrategyTestClient(t, restoreJobOf("rj", "bk", backupsv1alpha1.RestoreJobPhaseRunning))
		res, err := r.cleanupMariaDBBackup(context.Background(), cleanupBackup(cleanupDumpName))
		if err != nil || !requeues(res) || len(bucket.removed) != 0 {
			t.Fatalf("got res=%+v err=%v removed=%v, want the Backup held while the restore runs", res, err, bucket.removed)
		}
	})
	t.Run("finished restores and restores of other Backups do not hold it", func(t *testing.T) {
		bucket := &fakeS3Bucket{}
		r, _ := cleanupReconciler(t, bucket, cleanupOperatorBackup(t), s3CredsSecret("tenant", "creds"), tenantNamespace(),
			restoreJobOf("done", "bk", backupsv1alpha1.RestoreJobPhaseSucceeded),
			restoreJobOf("failed", "bk", backupsv1alpha1.RestoreJobPhaseFailed),
			restoreJobOf("other", "other-bk", backupsv1alpha1.RestoreJobPhaseRunning))
		res, err := r.cleanupMariaDBBackup(context.Background(), cleanupBackup(cleanupDumpName))
		if err != nil || requeues(res) || len(bucket.removed) != 1 {
			t.Fatalf("got res=%+v err=%v removed=%v, want the dump deleted", res, err, bucket.removed)
		}
	})
}

// platformCredsConfig points the reconciler at the platform's credentials
// Secret, the source the per-tenant Secret is projected from. The projected
// Secret is "creds", the one cleanupOperatorBackup names, as the default
// strategy names cozy-backups-creds.
func platformCredsConfig(endpoint string) BackupCredentialsConfig {
	return BackupCredentialsConfig{
		SourceNamespace:  "cozy-system",
		SourceSecretName: "cozy-backups-credentials",
		TargetSecretName: "creds",
		Endpoint:         endpoint,
		Region:           "us-east-1",
	}
}

func platformCredsSecret(bucketName, endpoint string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "cozy-system", Name: "cozy-backups-credentials"},
		Data: map[string][]byte{
			"accessKey":  []byte("PLATFORM"),
			"secretKey":  []byte("platform-secret"),
			"endpoint":   []byte(endpoint),
			"bucketName": []byte(bucketName),
		},
	}
}

func TestCleanupMariaDBBackup_SystemBucketUsesTheCurrentEndpoint(t *testing.T) {
	bucket := &fakeS3Bucket{}
	r, _ := cleanupReconciler(t, bucket, cleanupOperatorBackup(t), s3CredsSecret("tenant", "creds"), tenantNamespace(),
		platformCredsSecret("bkt", "s3.new.example"))
	r.CredentialsConfig = platformCredsConfig("https://s3.new.example")

	res, err := r.cleanupMariaDBBackup(context.Background(), cleanupBackup(cleanupDumpName))
	if err != nil || requeues(res) || len(bucket.removed) != 1 {
		t.Fatalf("got res=%+v err=%v removed=%v, want the dump deleted", res, err, bucket.removed)
	}
	for _, conn := range bucket.conns {
		if conn.Endpoint != "s3.new.example" || !conn.Secure || conn.AccessKey != "PLATFORM" || conn.Region != "us-east-1" {
			t.Errorf("connection = %+v, want the platform's current endpoint over TLS with its keys", conn)
		}
	}
}

func TestCleanupMariaDBBackup_OtherBucketUsesTheOperatorBackupsEndpoint(t *testing.T) {
	bucket := &fakeS3Bucket{}
	r, _ := cleanupReconciler(t, bucket, cleanupOperatorBackup(t), s3CredsSecret("tenant", "creds"), tenantNamespace(),
		platformCredsSecret("another-bucket", "s3.new.example"))
	r.CredentialsConfig = platformCredsConfig("https://s3.new.example")

	res, err := r.cleanupMariaDBBackup(context.Background(), cleanupBackup(cleanupDumpName))
	if err != nil || requeues(res) || len(bucket.removed) != 1 {
		t.Fatalf("got res=%+v err=%v removed=%v, want the dump deleted", res, err, bucket.removed)
	}
	for _, conn := range bucket.conns {
		if conn.Endpoint != "s3.example:9000" || conn.AccessKey != "AKID" {
			t.Errorf("connection = %+v, want the operator Backup's endpoint and Secret", conn)
		}
	}
}

func TestCleanupMariaDBBackup_UnreadablePlatformCredentialsFallBackToTheOperatorBackup(t *testing.T) {
	for _, bucketName := range []string{"bkt", "tenant-own-bucket"} {
		t.Run(bucketName, func(t *testing.T) {
			bucket := &fakeS3Bucket{}
			op := cleanupOperatorBackup(t)
			op.Spec.Storage.S3.Bucket = bucketName
			// No platform Secret: the projection source is missing.
			r, _ := cleanupReconciler(t, bucket, op, s3CredsSecret("tenant", "creds"), tenantNamespace())
			r.CredentialsConfig = platformCredsConfig("https://s3.new.example")

			res, err := r.cleanupMariaDBBackup(context.Background(), cleanupBackup(cleanupDumpName))
			if err != nil || requeues(res) || len(bucket.removed) != 1 {
				t.Fatalf("got res=%+v err=%v removed=%v, want the dump deleted with the operator Backup's own credentials", res, err, bucket.removed)
			}
			if bucket.conns[0].Endpoint != "s3.example:9000" || bucket.conns[0].AccessKey != "AKID" {
				t.Errorf("connection = %+v, want the operator Backup's endpoint and Secret", bucket.conns[0])
			}
		})
	}
}

// A missing or malformed platform Secret is a standing state, and the
// operator Backup's own credentials are the best left. A failed read says
// nothing about the Secret: falling back then can list a moved endpoint's
// old address, find no dump there and report it deleted.
func TestCleanupMariaDBBackup_TransientPlatformCredentialsReadIsRetried(t *testing.T) {
	bucket := &fakeS3Bucket{objects: []s3ObjectInfo{{Key: cleanupPrefix + cleanupDumpName, LastModified: cleanupCreated.Add(2 * time.Minute)}}}
	c := interceptor.NewClient(newMariaDBStrategyTestClient(t, cleanupOperatorBackup(t), s3CredsSecret("tenant", "creds"), tenantNamespace(),
		platformCredsSecret("bkt", "s3.new.example")).(client.WithWatch), interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.Secret); ok && key.Name == "cozy-backups-credentials" {
				return errors.New("apiserver unavailable")
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})
	r, rec := cleanupReconcilerWithClient(c, bucket)
	r.CredentialsConfig = platformCredsConfig("https://s3.new.example")

	res, err := r.cleanupMariaDBBackup(context.Background(), cleanupBackup(cleanupDumpName))
	if err != nil || !requeues(res) {
		t.Fatalf("got res=%+v err=%v, want the Backup held and retried", res, err)
	}
	if len(bucket.conns) != 0 || len(bucket.removed) != 0 || !operatorBackupExists(t, r.Client) {
		t.Errorf("conns=%v removed=%v operatorBackupExists=%v, want the bucket untouched", bucket.conns, bucket.removed, operatorBackupExists(t, r.Client))
	}
	if events := drainEvents(rec); !hasEvent(events, "Warning ArtifactCleanupFailed") {
		t.Errorf("events = %v, want a Warning ArtifactCleanupFailed", events)
	}
}

func TestCleanupMariaDBBackup_UnreadablePlatformCredentialsAndAFailingDeleteHold(t *testing.T) {
	bucket := &fakeS3Bucket{removeErr: errors.New("dial tcp: no such host")}
	r, rec := cleanupReconciler(t, bucket, cleanupOperatorBackup(t), s3CredsSecret("tenant", "creds"), tenantNamespace())
	r.CredentialsConfig = platformCredsConfig("https://s3.new.example")

	res, err := r.cleanupMariaDBBackup(context.Background(), cleanupBackup(cleanupDumpName))
	if err != nil || !requeues(res) {
		t.Fatalf("got res=%+v err=%v, want the Backup held", res, err)
	}
	if !operatorBackupExists(t, r.Client) {
		t.Error("operator Backup deleted although its dump was not")
	}
	events := drainEvents(rec)
	if hasEvent(events, "Warning ArtifactNotDeleted") || !hasEvent(events, "Warning ArtifactCleanupFailed") {
		t.Errorf("events = %v, want a Warning ArtifactCleanupFailed and the Backup not released", events)
	}
}

func TestCleanupMariaDBBackup_UnreadablePlatformCredentialsAndAFailingDeleteReleaseInATerminatingNamespace(t *testing.T) {
	bucket := &fakeS3Bucket{removeErr: errors.New("dial tcp: no such host")}
	ns := tenantNamespace()
	now := metav1.Now()
	ns.DeletionTimestamp = &now
	ns.Finalizers = []string{"kubernetes"}
	r, rec := cleanupReconciler(t, bucket, cleanupOperatorBackup(t), s3CredsSecret("tenant", "creds"), ns)
	r.CredentialsConfig = platformCredsConfig("https://s3.new.example")

	res, err := r.cleanupMariaDBBackup(context.Background(), cleanupBackup(cleanupDumpName))
	assertReleasedUntouched(t, r, rec, bucket, res, err)
}

func TestCleanupMariaDBBackup_SystemBucketKeepsTheOperatorBackupsCA(t *testing.T) {
	bucket := &fakeS3Bucket{}
	op := cleanupOperatorBackup(t)
	op.Spec.Storage.S3.TLS = &mariadbtypes.S3TLS{Enabled: true, CASecretKeyRef: &mariadbtypes.SecretKeySelector{Name: "ca", Key: "ca.crt"}}
	ca := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "ca"}, Data: map[string][]byte{"ca.crt": []byte("PEM")}}
	r, _ := cleanupReconciler(t, bucket, op, ca, s3CredsSecret("tenant", "creds"), tenantNamespace(), platformCredsSecret("bkt", "s3.new.example"))
	r.CredentialsConfig = platformCredsConfig("https://s3.new.example")

	res, err := r.cleanupMariaDBBackup(context.Background(), cleanupBackup(cleanupDumpName))
	if err != nil || requeues(res) || len(bucket.removed) != 1 {
		t.Fatalf("got res=%+v err=%v removed=%v, want the dump deleted", res, err, bucket.removed)
	}
	if string(bucket.conns[0].CAPEM) != "PEM" || bucket.conns[0].Endpoint != "s3.new.example" {
		t.Errorf("connection = %+v, want the current endpoint trusting the operator Backup's CA", bucket.conns[0])
	}
}

func TestCleanupMariaDBBackup_SystemBucketWithAnUnreadableCAIsHeld(t *testing.T) {
	bucket := &fakeS3Bucket{}
	op := cleanupOperatorBackup(t)
	op.Spec.Storage.S3.TLS = &mariadbtypes.S3TLS{Enabled: true, CASecretKeyRef: &mariadbtypes.SecretKeySelector{Name: "ca", Key: "ca.crt"}}
	r, rec := cleanupReconciler(t, bucket, op, s3CredsSecret("tenant", "creds"), tenantNamespace(), platformCredsSecret("bkt", "s3.new.example"))
	r.CredentialsConfig = platformCredsConfig("https://s3.new.example")

	res, err := r.cleanupMariaDBBackup(context.Background(), cleanupBackup(cleanupDumpName))
	if err != nil || !requeues(res) || len(bucket.removed) != 0 {
		t.Fatalf("got res=%+v err=%v removed=%v, want the Backup held with nothing deleted", res, err, bucket.removed)
	}
	if events := drainEvents(rec); !hasEvent(events, "Warning ArtifactCleanupFailed") {
		t.Errorf("events = %v, want a Warning ArtifactCleanupFailed", events)
	}
}

func TestCleanupMariaDBBackup_PlainHTTPPlatformEndpointNeedsNoCA(t *testing.T) {
	bucket := &fakeS3Bucket{}
	op := cleanupOperatorBackup(t)
	op.Spec.Storage.S3.TLS = &mariadbtypes.S3TLS{Enabled: true, CASecretKeyRef: &mariadbtypes.SecretKeySelector{Name: "ca", Key: "ca.crt"}}
	r, _ := cleanupReconciler(t, bucket, op, s3CredsSecret("tenant", "creds"), tenantNamespace(), platformCredsSecret("bkt", "s3.new.example"))
	r.CredentialsConfig = platformCredsConfig("http://s3.new.example")

	res, err := r.cleanupMariaDBBackup(context.Background(), cleanupBackup(cleanupDumpName))
	if err != nil || requeues(res) || len(bucket.removed) != 1 {
		t.Fatalf("got res=%+v err=%v removed=%v, want the dump deleted without reading a CA plain HTTP does not use", res, err, bucket.removed)
	}
}

func TestCleanupMariaDBBackup_PlatformEndpointFallsBackToTheConfiguredOne(t *testing.T) {
	bucket := &fakeS3Bucket{}
	platform := platformCredsSecret("bkt", "")
	delete(platform.Data, "endpoint")
	r, _ := cleanupReconciler(t, bucket, cleanupOperatorBackup(t), s3CredsSecret("tenant", "creds"), tenantNamespace(), platform)
	r.CredentialsConfig = platformCredsConfig("https://s3.configured.example")

	if _, err := r.cleanupMariaDBBackup(context.Background(), cleanupBackup(cleanupDumpName)); err != nil {
		t.Fatal(err)
	}
	if bucket.conns[0].Endpoint != "s3.configured.example" {
		t.Errorf("connection = %+v, want BACKUP_STORAGE_ENDPOINT without its scheme", bucket.conns[0])
	}
}

// blockingBucket lists the dump it holds and answers the call named by
// blockOn only when its context ends, as an unreachable endpoint does.
type blockingBucket struct {
	fakeS3Bucket
	blockOn string
	entered chan struct{}
}

func (b *blockingBucket) block(ctx context.Context, call string) bool {
	if b.blockOn != call {
		return false
	}
	close(b.entered)
	<-ctx.Done()
	return true
}

func (b *blockingBucket) ListObjects(ctx context.Context, bucket, prefix string, recursive bool) ([]s3ObjectInfo, error) {
	if b.block(ctx, "list") {
		return nil, ctx.Err()
	}
	return b.fakeS3Bucket.ListObjects(ctx, bucket, prefix, recursive)
}

func (b *blockingBucket) RemoveObject(ctx context.Context, bucket, key string) error {
	if b.block(ctx, "remove") {
		return ctx.Err()
	}
	return b.fakeS3Bucket.RemoveObject(ctx, bucket, key)
}

func TestCleanupMariaDBBackup_BoundsAnUnreachableEndpoint(t *testing.T) {
	prev := mariadbDumpCleanupTimeout
	mariadbDumpCleanupTimeout = 50 * time.Millisecond
	t.Cleanup(func() { mariadbDumpCleanupTimeout = prev })
	for _, call := range []string{"list", "remove"} {
		t.Run(call, func(t *testing.T) {
			bucket := &blockingBucket{blockOn: call, entered: make(chan struct{})}
			bucket.objects = []s3ObjectInfo{{Key: cleanupPrefix + cleanupDumpName, LastModified: cleanupCreated.Add(2 * time.Minute)}}
			c := newMariaDBStrategyTestClient(t, cleanupOperatorBackup(t), s3CredsSecret("tenant", "creds"), tenantNamespace())
			r := &BackupReconciler{Client: c, Scheme: c.Scheme(), Recorder: record.NewFakeRecorder(20),
				newS3ObjectLister:  func(s3Connection) (s3ObjectLister, error) { return bucket, nil },
				newS3ObjectRemover: func(s3Connection) (s3ObjectRemover, error) { return bucket, nil }}

			done := make(chan ctrl.Result, 1)
			go func() {
				res, _ := r.cleanupMariaDBBackup(context.Background(), cleanupBackup(cleanupDumpName))
				done <- res
			}()
			select {
			case res := <-done:
				select {
				case <-bucket.entered:
				default:
					t.Fatalf("cleanup returned without reaching the blocking %s", call)
				}
				if !requeues(res) {
					t.Errorf("res = %+v, want the Backup held and retried", res)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("cleanup did not give up on an unreachable endpoint blocking %s", call)
			}
		})
	}
}

func TestCleanupMariaDBBackup_ReleasesWhenTheOperatorCRDIsGone(t *testing.T) {
	bucket := &fakeS3Bucket{}
	c := interceptor.NewClient(newMariaDBStrategyTestClient(t, tenantNamespace()).(client.WithWatch), interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*mariadbtypes.Backup); ok {
				return &apimeta.NoKindMatchError{GroupKind: mariadbtypes.GroupVersion.WithKind("Backup").GroupKind()}
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})
	r, rec := cleanupReconcilerWithClient(c, bucket)
	res, err := r.cleanupMariaDBBackup(context.Background(), cleanupBackup(cleanupDumpName))
	if err != nil || requeues(res) || len(bucket.removed) != 0 {
		t.Fatalf("got res=%+v err=%v removed=%v, want released with nothing deleted", res, err, bucket.removed)
	}
	if events := drainEvents(rec); !hasEvent(events, "Warning ArtifactNotDeleted") {
		t.Errorf("events = %v, want a Warning ArtifactNotDeleted", events)
	}
}

func TestCleanupMariaDBBackup_OperatorBackupAlreadyDeletedIsDone(t *testing.T) {
	bucket := &fakeS3Bucket{}
	c := interceptor.NewClient(newMariaDBStrategyTestClient(t, cleanupOperatorBackup(t), s3CredsSecret("tenant", "creds"), tenantNamespace()).(client.WithWatch), interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if _, ok := obj.(*mariadbtypes.Backup); ok {
				return apierrors.NewNotFound(mariadbtypes.GroupVersion.WithResource("backups").GroupResource(), obj.GetName())
			}
			return c.Delete(ctx, obj, opts...)
		},
	})
	r, _ := cleanupReconcilerWithClient(c, bucket)
	res, err := r.cleanupMariaDBBackup(context.Background(), cleanupBackup(cleanupDumpName))
	if err != nil || requeues(res) {
		t.Fatalf("got res=%+v err=%v, want the Backup released", res, err)
	}
}

// A strategy of its own that happens to use the platform bucket's name, as
// an admin-chosen name on external S3 can, is reached as recorded: only the
// projected Secret marks a Backup as written to the platform bucket.
func TestCleanupMariaDBBackup_SameBucketNameWithOwnCredentialsUsesTheOperatorBackupsEndpoint(t *testing.T) {
	bucket := &fakeS3Bucket{}
	op := cleanupOperatorBackup(t)
	op.Spec.Storage.S3.AccessKeyIdSecretKeyRef.Name = "own-creds"
	op.Spec.Storage.S3.SecretAccessKeySecretKeyRef.Name = "own-creds"
	r, _ := cleanupReconciler(t, bucket, op, s3CredsSecret("tenant", "own-creds"), tenantNamespace(),
		platformCredsSecret("bkt", "s3.new.example"))
	r.CredentialsConfig = platformCredsConfig("https://s3.new.example")

	res, err := r.cleanupMariaDBBackup(context.Background(), cleanupBackup(cleanupDumpName))
	if err != nil || requeues(res) || len(bucket.removed) != 1 {
		t.Fatalf("got res=%+v err=%v removed=%v, want the dump deleted", res, err, bucket.removed)
	}
	if bucket.conns[0].Endpoint != "s3.example:9000" || bucket.conns[0].AccessKey != "AKID" {
		t.Errorf("connection = %+v, want the operator Backup's endpoint and Secret", bucket.conns[0])
	}
}

// With no endpoint anywhere the platform credentials are unusable, which
// falls back like any other unreadable credentials.
func TestCleanupMariaDBBackup_PlatformCredentialsWithoutAnEndpointFallBack(t *testing.T) {
	bucket := &fakeS3Bucket{}
	platform := platformCredsSecret("bkt", "")
	delete(platform.Data, "endpoint")
	r, _ := cleanupReconciler(t, bucket, cleanupOperatorBackup(t), s3CredsSecret("tenant", "creds"), tenantNamespace(), platform)
	r.CredentialsConfig = platformCredsConfig("")

	res, err := r.cleanupMariaDBBackup(context.Background(), cleanupBackup(cleanupDumpName))
	if err != nil || requeues(res) || len(bucket.removed) != 1 {
		t.Fatalf("got res=%+v err=%v removed=%v, want the dump deleted", res, err, bucket.removed)
	}
	if bucket.conns[0].Endpoint != "s3.example:9000" {
		t.Errorf("connection = %+v, want the operator Backup's own endpoint", bucket.conns[0])
	}
}

func TestCleanupMariaDBBackup_MalformedPlatformCredentialsFallBackToTheOperatorBackup(t *testing.T) {
	bucket := &fakeS3Bucket{}
	malformed := platformCredsSecret("bkt", "s3.new.example")
	delete(malformed.Data, "secretKey")
	r, _ := cleanupReconciler(t, bucket, cleanupOperatorBackup(t), s3CredsSecret("tenant", "creds"), tenantNamespace(), malformed)
	r.CredentialsConfig = platformCredsConfig("https://s3.new.example")

	res, err := r.cleanupMariaDBBackup(context.Background(), cleanupBackup(cleanupDumpName))
	if err != nil || requeues(res) || len(bucket.removed) != 1 {
		t.Fatalf("got res=%+v err=%v removed=%v, want the dump deleted", res, err, bucket.removed)
	}
	if bucket.conns[0].Endpoint != "s3.example:9000" {
		t.Errorf("connection = %+v, want the operator Backup's own endpoint", bucket.conns[0])
	}
}

func TestCleanupMariaDBBackup_TerminatingNamespaceReleasesOnS3Failure(t *testing.T) {
	bucket := &fakeS3Bucket{removeErr: errors.New("connection refused")}
	ns := tenantNamespace()
	now := metav1.Now()
	ns.DeletionTimestamp = &now
	ns.Finalizers = []string{"kubernetes"}
	r, rec := cleanupReconciler(t, bucket, cleanupOperatorBackup(t), s3CredsSecret("tenant", "creds"), ns)

	res, err := r.cleanupMariaDBBackup(context.Background(), cleanupBackup(cleanupDumpName))
	assertReleasedUntouched(t, r, rec, bucket, res, err)
}

// Reconcile keeps the finalizer while the dump cannot be deleted, and drops
// it once both the dump and the operator Backup are gone.
func TestBackupReconcile_MariaDBFinalizerFollowsTheDumpDelete(t *testing.T) {
	bucket := &fakeS3Bucket{removeErr: errors.New("503")}
	backup := cleanupBackup(cleanupDumpName)
	r, _ := cleanupReconciler(t, bucket, backup, cleanupOperatorBackup(t), s3CredsSecret("tenant", "creds"), tenantNamespace())
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(backup)}

	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := r.Get(context.Background(), req.NamespacedName, &backupsv1alpha1.Backup{}); err != nil {
		t.Fatalf("Backup gone while its dump could not be deleted: %v", err)
	}

	bucket.removeErr = nil
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := r.Get(context.Background(), req.NamespacedName, &backupsv1alpha1.Backup{}); !apierrors.IsNotFound(err) {
		t.Errorf("Backup still present (err=%v), want its finalizer removed", err)
	}
}

// The minio adapter deletes the exact key, with the colons of the dump's
// timestamp escaped in the path; SeaweedFS refuses them raw.
func TestMinioObjectRemover_DeletesTheEscapedKey(t *testing.T) {
	var method, rawPath, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		method, rawPath, auth = req.Method, req.URL.EscapedPath(), req.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	remover, err := newMinioObjectLister(s3Connection{Endpoint: strings.TrimPrefix(srv.URL, "http://"), Region: "us-east-1", AccessKey: "AKID", SecretKey: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if err := remover.(s3ObjectRemover).RemoveObject(context.Background(), "bkt", "tenant#a/db/backup.2026-10-21T10:01:00Z.gzip.sql"); err != nil {
		t.Fatalf("RemoveObject: %v", err)
	}
	if method != http.MethodDelete || rawPath != "/bkt/tenant%23a/db/backup.2026-10-21T10%3A01%3A00Z.gzip.sql" {
		t.Errorf("request %s %s, want DELETE of the escaped key", method, rawPath)
	}
	if !strings.Contains(auth, "Credential=AKID/") {
		t.Errorf("request not signed with the access key: %q", auth)
	}
}

func TestMinioObjectRemover_ReturnsTheServersError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>AccessDenied</Code><Message>Access Denied</Message></Error>`)
	}))
	defer srv.Close()

	remover, err := newMinioObjectLister(s3Connection{Endpoint: strings.TrimPrefix(srv.URL, "http://"), Region: "us-east-1", AccessKey: "AKID", SecretKey: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if err := remover.(s3ObjectRemover).RemoveObject(context.Background(), "bkt", "k"); err == nil {
		t.Fatal("RemoveObject = nil, want the AccessDenied error")
	}
}
