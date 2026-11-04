// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	"github.com/cozystack/cozystack/internal/backupcontroller/mariadbapp"
	"github.com/cozystack/cozystack/internal/backupcontroller/mariadbtypes"
)

// The source run of the restore fixture: operator Backup op-bk of
// application src, alive from mdbRestoreT0 to mdbRestoreT0+40s, writing
// under tenant/src/ with mdbRestoreDump as its dump.
var mdbRestoreT0 = time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)

const (
	mdbRestorePrefix = "tenant/src/"
	mdbRestoreDump   = "backup.2026-10-21T10:00:05Z.gzip.sql"
	mdbRestoreTime   = "2026-10-21T10:00:05Z"
	mdbNewerDump     = "backup.2026-10-21T11:00:05Z.gzip.sql"
)

type mariadbRestoreFixture struct {
	c      client.Client
	r      *RestoreJobReconciler
	rj     *backupsv1alpha1.RestoreJob
	backup *backupsv1alpha1.Backup
	src    *mariadbtypes.Backup
	lister *fakeS3Lister
	events *record.FakeRecorder
}

// newMariaDBRestoreFixture seeds a RestoreJob of Backup src-bk, whose
// driverMetadata is driverMD plus the operator Backup's name, into the
// existing application target. extra objects are seeded too.
func newMariaDBRestoreFixture(t *testing.T, driverMD map[string]string, lister *fakeS3Lister, extra ...client.Object) *mariadbRestoreFixture {
	t.Helper()
	apps := mariadbapp.GroupName
	s3 := newRenderedMariaDBTemplate().Storage.S3
	s3.Prefix = "tenant/src"
	src := operatorBackupStoringTo(t, s3, mdbRestoreT0)
	src.Status.Conditions = []metav1.Condition{mariadbBackupSucceeded(metav1.NewTime(mdbRestoreT0.Add(40 * time.Second)))}

	md := map[string]string{mariadbBackupNameKey: src.Name}
	for k, v := range driverMD {
		md[k] = v
	}
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "src-bk"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{Kind: "MariaDB", Name: "src", APIGroup: &apps},
			TakenAt:        metav1.NewTime(mdbRestoreT0.Add(40 * time.Second)),
			DriverMetadata: md,
		},
	}
	started := metav1.Now()
	rj := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "rj"},
		Spec: backupsv1alpha1.RestoreJobSpec{
			BackupRef:            corev1.LocalObjectReference{Name: backup.Name},
			TargetApplicationRef: &corev1.TypedLocalObjectReference{Kind: "MariaDB", Name: "target", APIGroup: &apps},
		},
		Status: backupsv1alpha1.RestoreJobStatus{StartedAt: &started, Phase: backupsv1alpha1.RestoreJobPhaseRunning},
	}
	target := &mariadbtypes.MariaDB{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: mariadbNameForApp("target")}}
	objs := append([]client.Object{src, backup, rj, target}, extra...)
	c := newMariaDBStrategyTestClient(t, objs...)
	fetched := &backupsv1alpha1.RestoreJob{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(rj), fetched); err != nil {
		t.Fatalf("get RestoreJob: %v", err)
	}
	events := record.NewFakeRecorder(10)
	return &mariadbRestoreFixture{
		c:      c,
		rj:     fetched,
		backup: backup,
		src:    src,
		lister: lister,
		events: events,
		r: &RestoreJobReconciler{
			Client:   c,
			Recorder: events,
			newS3ObjectLister: func(conn s3Connection) (s3ObjectLister, error) {
				lister.conn = conn
				return lister, nil
			},
		},
	}
}

func (f *mariadbRestoreFixture) reconcile(t *testing.T) ctrl.Result {
	t.Helper()
	res, err := f.r.reconcileMariaDBRestore(context.Background(), f.rj, f.backup)
	if err != nil {
		t.Fatalf("reconcileMariaDBRestore: %v", err)
	}
	return res
}

func (f *mariadbRestoreFixture) restores(t *testing.T) []mariadbtypes.Restore {
	t.Helper()
	list := &mariadbtypes.RestoreList{}
	if err := f.c.List(context.Background(), list, client.InNamespace("tenant")); err != nil {
		t.Fatalf("list operator Restores: %v", err)
	}
	return list.Items
}

// wantFailed asserts the RestoreJob failed with a message holding every
// fragment, and that no operator Restore was created.
func (f *mariadbRestoreFixture) wantFailed(t *testing.T, fragments ...string) {
	t.Helper()
	if f.rj.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
		t.Fatalf("phase = %q, want Failed", f.rj.Status.Phase)
	}
	for _, s := range fragments {
		if !strings.Contains(f.rj.Status.Message, s) {
			t.Errorf("message %q does not contain %q", f.rj.Status.Message, s)
		}
	}
	if got := f.restores(t); len(got) != 0 {
		t.Errorf("created %d operator Restores; want none", len(got))
	}
}

// wantWaiting asserts the RestoreJob is kept waiting on the dump check
// with Ready=False saying why, and that no operator Restore was created.
func (f *mariadbRestoreFixture) wantWaiting(t *testing.T, res ctrl.Result, fragments ...string) {
	t.Helper()
	if f.rj.Status.Phase == backupsv1alpha1.RestoreJobPhaseFailed {
		t.Fatalf("RestoreJob failed: %s", f.rj.Status.Message)
	}
	if res.RequeueAfter != mariadbDumpCheckRetryInterval {
		t.Errorf("RequeueAfter = %s, want %s", res.RequeueAfter, mariadbDumpCheckRetryInterval)
	}
	persisted := &backupsv1alpha1.RestoreJob{}
	if err := f.c.Get(context.Background(), client.ObjectKeyFromObject(f.rj), persisted); err != nil {
		t.Fatalf("get RestoreJob: %v", err)
	}
	cond := apimeta.FindStatusCondition(persisted.Status.Conditions, "Ready")
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "DumpCheckPending" {
		t.Fatalf("Ready = %+v, want False/DumpCheckPending", cond)
	}
	for _, s := range fragments {
		if !strings.Contains(cond.Message, s) {
			t.Errorf("Ready message %q does not contain %q", cond.Message, s)
		}
	}
	if got := f.restores(t); len(got) != 0 {
		t.Errorf("created %d operator Restores; want none", len(got))
	}
}

// wantRestore asserts exactly one operator Restore of op-bk into target,
// asking for targetTime.
func (f *mariadbRestoreFixture) wantRestore(t *testing.T, targetTime string) {
	t.Helper()
	got := f.restores(t)
	if len(got) != 1 {
		t.Fatalf("created %d operator Restores; want one", len(got))
	}
	spec := got[0].Spec
	if spec.BackupRef == nil || spec.BackupRef.Name != f.src.Name || spec.MariaDBRef.Name != mariadbNameForApp("target") {
		t.Errorf("Restore spec = %+v, want backupRef %s into %s", spec, f.src.Name, mariadbNameForApp("target"))
	}
	if spec.TargetRecoveryTime != targetTime {
		t.Errorf("targetRecoveryTime = %q, want %q", spec.TargetRecoveryTime, targetTime)
	}
}

func recordedDump(name string) map[string]string {
	return map[string]string{mariadbDumpFileKey: name, mariadbDumpTimeKey: mdbRestoreTime}
}

func TestReconcileMariaDBRestore_RecordedDump(t *testing.T) {
	creds := s3CredsSecret("tenant", "creds")

	t.Run("restores the recorded dump by its own timestamp", func(t *testing.T) {
		l := &fakeS3Lister{objects: []s3ObjectInfo{
			{Key: mdbRestorePrefix + mdbRestoreDump, LastModified: mdbRestoreT0.Add(20 * time.Second)},
			{Key: mdbRestorePrefix + mdbNewerDump, LastModified: mdbRestoreT0.Add(time.Hour)},
		}}
		f := newMariaDBRestoreFixture(t, recordedDump(mdbRestoreDump), l, creds)
		f.reconcile(t)
		f.wantRestore(t, mdbRestoreTime)
		if l.bucket != "bkt" || l.prefix != mdbRestorePrefix+mdbRestoreDump || l.conn.AccessKey != "AKID" {
			t.Errorf("checked %s/%s as %q, want bkt/%s%s with the operator Backup's credentials", l.bucket, l.prefix, l.conn.AccessKey, mdbRestorePrefix, mdbRestoreDump)
		}
	})

	t.Run("a recorded dump gone from storage fails the RestoreJob", func(t *testing.T) {
		l := &fakeS3Lister{objects: []s3ObjectInfo{{Key: mdbRestorePrefix + mdbNewerDump, LastModified: mdbRestoreT0.Add(time.Hour)}}}
		f := newMariaDBRestoreFixture(t, recordedDump(mdbRestoreDump), l, creds)
		f.reconcile(t)
		f.wantFailed(t, mdbRestoreDump, "no longer exists in storage", "s3://bkt/"+mdbRestorePrefix)
	})

	t.Run("a storage that cannot be read keeps the RestoreJob waiting", func(t *testing.T) {
		f := newMariaDBRestoreFixture(t, recordedDump(mdbRestoreDump), &fakeS3Lister{err: errors.New("Access Denied.")}, creds)
		f.wantWaiting(t, f.reconcile(t), mdbRestoreDump, "Access Denied.")
	})

	t.Run("missing credentials keep the RestoreJob waiting", func(t *testing.T) {
		l := &fakeS3Lister{}
		f := newMariaDBRestoreFixture(t, recordedDump(mdbRestoreDump), l)
		f.wantWaiting(t, f.reconcile(t), mdbRestoreDump, "resolve S3 credentials")
		if l.calls != 0 {
			t.Errorf("S3 listed %d times without credentials", l.calls)
		}
	})

	t.Run("a hanging endpoint is given up at the check timeout", func(t *testing.T) {
		f := newMariaDBRestoreFixture(t, recordedDump(mdbRestoreDump), &fakeS3Lister{}, creds)
		f.r.newS3ObjectLister = func(s3Connection) (s3ObjectLister, error) { return blockingLister{}, nil }
		var res ctrl.Result
		var err error
		withShortDumpCheckTimeout(t, func() { res, err = f.r.reconcileMariaDBRestore(context.Background(), f.rj, f.backup) })
		if err != nil {
			t.Fatalf("reconcileMariaDBRestore: %v", err)
		}
		f.wantWaiting(t, res, mdbRestoreDump, context.DeadlineExceeded.Error())
	})

	t.Run("a storage still unreadable at the deadline fails the RestoreJob with the cause", func(t *testing.T) {
		f := newMariaDBRestoreFixture(t, recordedDump(mdbRestoreDump), &fakeS3Lister{err: errors.New("Access Denied.")}, creds)
		long := metav1.NewTime(time.Now().Add(-mariadbDefaultRestoreDeadline - time.Minute))
		f.rj.Status.StartedAt = &long
		f.reconcile(t)
		f.wantFailed(t, mdbRestoreDump, "Access Denied.", "gave up after")
	})

	t.Run("a recorded name that is not a dump fails the RestoreJob unchecked", func(t *testing.T) {
		l := &fakeS3Lister{}
		f := newMariaDBRestoreFixture(t, recordedDump("dump.sql"), l, creds)
		f.reconcile(t)
		f.wantFailed(t, `"dump.sql"`)
		if l.calls != 0 {
			t.Errorf("S3 listed %d times for a name that is not a dump", l.calls)
		}
	})

	t.Run("an operator Restore created earlier is followed without a check", func(t *testing.T) {
		l := &fakeS3Lister{err: errors.New("Access Denied.")}
		existing := &mariadbtypes.Restore{ObjectMeta: metav1.ObjectMeta{
			Namespace: "tenant", Name: "rj-abcde",
			Labels: map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      "rj",
				backupsv1alpha1.OwningJobNamespaceLabel: "tenant",
			},
		}}
		f := newMariaDBRestoreFixture(t, recordedDump(mdbRestoreDump), l, creds, existing)
		res := f.reconcile(t)
		if l.calls != 0 {
			t.Errorf("S3 listed %d times for a RestoreJob whose operator Restore exists", l.calls)
		}
		if f.rj.Status.Phase == backupsv1alpha1.RestoreJobPhaseFailed || res.RequeueAfter != mariadbPollInterval {
			t.Errorf("phase %q, requeue %s; want the existing Restore polled", f.rj.Status.Phase, res.RequeueAfter)
		}
		if got := f.restores(t); len(got) != 1 {
			t.Errorf("%d operator Restores; want only the existing one", len(got))
		}
	})
}

func TestReconcileMariaDBRestore_NoRecordedDump(t *testing.T) {
	l := &fakeS3Lister{err: errors.New("Access Denied.")}
	f := newMariaDBRestoreFixture(t, nil, l, s3CredsSecret("tenant", "creds"))
	f.reconcile(t)
	f.wantRestore(t, "")
	if l.calls != 0 {
		t.Errorf("S3 listed %d times for a Backup that records no dump", l.calls)
	}
}

// withShortDumpCheckTimeout shortens the restore's dump check timeout and
// fails the test if fn is not back well before the default would expire.
func withShortDumpCheckTimeout(t *testing.T, fn func()) {
	t.Helper()
	saved := mariadbRestoreDumpCheckTimeout
	mariadbRestoreDumpCheckTimeout = 50 * time.Millisecond
	defer func() { mariadbRestoreDumpCheckTimeout = saved }()
	done := make(chan struct{})
	go func() {
		fn()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the dump check did not give up at its timeout")
	}
}
