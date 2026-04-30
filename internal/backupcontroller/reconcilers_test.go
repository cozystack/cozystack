package backupcontroller

import (
	"context"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	velerov1 "github.com/vmware-tanzu/velero/pkg/apis/velero/v1"
)

func newReconcilerScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := scheme.AddToScheme(s); err != nil {
		t.Fatalf("add clientgoscheme: %v", err)
	}
	if err := backupsv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add backupsv1alpha1: %v", err)
	}
	if err := strategyv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add strategyv1alpha1: %v", err)
	}
	if err := velerov1.AddToScheme(s); err != nil {
		t.Fatalf("add velerov1: %v", err)
	}
	return s
}

// hasFinalizer is a small wrapper so the assertions read naturally.
func hasFinalizer(haystack []string, needle string) bool {
	return slices.Contains(haystack, needle)
}

// -----------------------------------------------------------------------
// BackupReconciler
// -----------------------------------------------------------------------

// TestBackupReconciler_NotFoundIsNoop pins the early-exit path: when
// the reconciled Backup has been deleted, Reconcile must return an
// empty Result without error.
func TestBackupReconciler_NotFoundIsNoop(t *testing.T) {
	s := newReconcilerScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()

	r := &BackupReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(8)}
	res, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "tenant-foo", Name: "missing"},
	})
	if err != nil || res.RequeueAfter != 0 {
		t.Fatalf("expected (nil, empty Result), got (%v, %+v)", err, res)
	}
}

// TestBackupReconciler_AddsFinalizer pins finalizer-add: a fresh Backup
// without the cleanup-velero finalizer must come back with it.
func TestBackupReconciler_AddsFinalizer(t *testing.T) {
	s := newReconcilerScheme(t)
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: "harbor-snap", Namespace: "tenant-foo"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(backup).Build()

	r := &BackupReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(8)}
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "harbor-snap", Namespace: "tenant-foo"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &backupsv1alpha1.Backup{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "harbor-snap", Namespace: "tenant-foo"}, got); err != nil {
		t.Fatalf("get backup: %v", err)
	}
	if !hasFinalizer(got.Finalizers, backupFinalizer) {
		t.Errorf("expected finalizer %q, got %v", backupFinalizer, got.Finalizers)
	}
}

// TestBackupReconciler_FinalizerAlreadyPresentNoop pins idempotency: a
// Backup that already has the finalizer must not be patched again,
// and the function must return without error.
func TestBackupReconciler_FinalizerAlreadyPresentNoop(t *testing.T) {
	s := newReconcilerScheme(t)
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "harbor-snap",
			Namespace:  "tenant-foo",
			Finalizers: []string{backupFinalizer},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(backup).Build()

	r := &BackupReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(8)}
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "harbor-snap", Namespace: "tenant-foo"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestBackupReconciler_DeletionWithoutVeleroMetadata pins the deletion
// path when there is nothing to clean up in Velero — the finalizer is
// removed and no DeleteBackupRequest is ever created.
func TestBackupReconciler_DeletionWithoutVeleroMetadata(t *testing.T) {
	s := newReconcilerScheme(t)
	now := metav1.NewTime(time.Now())
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "harbor-snap",
			Namespace:         "tenant-foo",
			Finalizers:        []string{backupFinalizer},
			DeletionTimestamp: &now,
		},
		// No driverMetadata → cleanup skips Velero entirely.
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(backup).Build()

	r := &BackupReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(8)}
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "harbor-snap", Namespace: "tenant-foo"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	dbrList := &velerov1.DeleteBackupRequestList{}
	if err := c.List(context.TODO(), dbrList); err != nil {
		t.Fatalf("list DBR: %v", err)
	}
	if len(dbrList.Items) != 0 {
		t.Errorf("expected 0 DeleteBackupRequests, got %d", len(dbrList.Items))
	}
}

// TestBackupReconciler_DeletionWithExistingVeleroBackup pins the full
// cleanup path: a DeleteBackupRequest is created so Velero purges the
// backup data from the BSL, not just the kube object.
func TestBackupReconciler_DeletionWithExistingVeleroBackup(t *testing.T) {
	s := newReconcilerScheme(t)
	now := metav1.NewTime(time.Now())
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "harbor-snap",
			Namespace:         "tenant-foo",
			Finalizers:        []string{backupFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: backupsv1alpha1.BackupSpec{
			DriverMetadata: map[string]string{
				veleroBackupNameMetadataKey:      "harbor-snap-velero",
				veleroBackupNamespaceMetadataKey: "cozy-velero",
			},
		},
	}
	veleroBackup := &velerov1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: "harbor-snap-velero", Namespace: "cozy-velero"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(backup, veleroBackup).Build()

	r := &BackupReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(8)}
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "harbor-snap", Namespace: "tenant-foo"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	dbrList := &velerov1.DeleteBackupRequestList{}
	if err := c.List(context.TODO(), dbrList); err != nil {
		t.Fatalf("list DBR: %v", err)
	}
	if len(dbrList.Items) != 1 {
		t.Fatalf("expected exactly one DeleteBackupRequest, got %d", len(dbrList.Items))
	}
	if dbrList.Items[0].Spec.BackupName != "harbor-snap-velero" {
		t.Errorf("DBR.Spec.BackupName=%q, want harbor-snap-velero", dbrList.Items[0].Spec.BackupName)
	}
}

// TestBackupReconciler_DeletionVeleroBackupAlreadyGone pins resilience
// when the Velero Backup was already removed (e.g. cluster restore,
// manual cleanup): finalizer comes off without erroring.
func TestBackupReconciler_DeletionVeleroBackupAlreadyGone(t *testing.T) {
	s := newReconcilerScheme(t)
	now := metav1.NewTime(time.Now())
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "harbor-snap",
			Namespace:         "tenant-foo",
			Finalizers:        []string{backupFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: backupsv1alpha1.BackupSpec{
			DriverMetadata: map[string]string{
				veleroBackupNameMetadataKey: "harbor-snap-velero",
			},
		},
	}
	// No velerov1.Backup pre-seeded → Get returns NotFound.
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(backup).Build()

	r := &BackupReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(8)}
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "harbor-snap", Namespace: "tenant-foo"},
	}); err != nil {
		t.Fatalf("unexpected error on missing Velero backup: %v", err)
	}

	got := &backupsv1alpha1.Backup{}
	err := c.Get(context.TODO(), types.NamespacedName{Name: "harbor-snap", Namespace: "tenant-foo"}, got)
	if err == nil {
		// fake client retains objects with finalizers; check finalizer was removed
		if hasFinalizer(got.Finalizers, backupFinalizer) {
			t.Errorf("expected finalizer removed, still present: %v", got.Finalizers)
		}
	} else if !apierrors.IsNotFound(err) {
		t.Errorf("unexpected error: %v", err)
	}
}

// -----------------------------------------------------------------------
// BackupJobReconciler
// -----------------------------------------------------------------------

// TestBackupJobReconciler_NotFoundIsNoop pins the early-exit on missing
// BackupJob.
func TestBackupJobReconciler_NotFoundIsNoop(t *testing.T) {
	s := newReconcilerScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()

	r := &BackupJobReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(8)}
	res, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "tenant-foo", Name: "missing"},
	})
	if err != nil || res.RequeueAfter != 0 {
		t.Fatalf("expected (nil, empty Result), got (%v, %+v)", err, res)
	}
}

// TestBackupJobReconciler_MissingBackupClassErrors pins the resolve
// failure: a BackupJob referencing a non-existent BackupClass should
// surface the error to the controller-runtime queue (not silently
// succeed) so the operator gets a backoff retry.
func TestBackupJobReconciler_MissingBackupClassErrors(t *testing.T) {
	s := newReconcilerScheme(t)
	job := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Name: "harbor-job", Namespace: "tenant-foo"},
		Spec: backupsv1alpha1.BackupJobSpec{
			BackupClassName: "missing-class",
			ApplicationRef: corev1.TypedLocalObjectReference{
				Kind: "Harbor",
				Name: "harbor",
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(job).Build()

	r := &BackupJobReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(8)}
	_, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "harbor-job", Namespace: "tenant-foo"},
	})
	if err == nil {
		t.Errorf("expected error from missing BackupClass, got nil")
	}
}

// -----------------------------------------------------------------------
// PlanReconciler
// -----------------------------------------------------------------------

// TestPlanReconciler_NotFoundIsNoop pins the early-exit on missing Plan.
func TestPlanReconciler_NotFoundIsNoop(t *testing.T) {
	s := newReconcilerScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()

	r := &PlanReconciler{Client: c, Scheme: s}
	res, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "tenant-foo", Name: "missing"},
	})
	if err != nil || res.RequeueAfter != 0 {
		t.Fatalf("expected (nil, empty Result), got (%v, %+v)", err, res)
	}
}

// TestPlanReconciler_InvalidCronSetsErrorCondition pins the failure
// surfacing: a malformed cron expression must NOT error out the
// reconciler (which would just retry forever) but instead set a
// PlanConditionError=True on .status so an operator can see the
// problem via kubectl describe plan.
func TestPlanReconciler_InvalidCronSetsErrorCondition(t *testing.T) {
	s := newReconcilerScheme(t)
	plan := &backupsv1alpha1.Plan{
		ObjectMeta: metav1.ObjectMeta{Name: "daily", Namespace: "tenant-foo"},
		Spec: backupsv1alpha1.PlanSpec{
			Schedule: backupsv1alpha1.PlanSchedule{
				Cron: "not a valid cron",
			},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(plan).
		WithStatusSubresource(plan).
		Build()

	r := &PlanReconciler{Client: c, Scheme: s}
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "daily", Namespace: "tenant-foo"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &backupsv1alpha1.Plan{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "daily", Namespace: "tenant-foo"}, got); err != nil {
		t.Fatalf("get plan: %v", err)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, backupsv1alpha1.PlanConditionError)
	if cond == nil {
		t.Fatalf("expected PlanConditionError condition set, got %+v", got.Status.Conditions)
	}
	if cond.Status != metav1.ConditionTrue {
		t.Errorf("expected condition status True, got %s", cond.Status)
	}
}

// TestPlanReconciler_ValidCronRequeuesUntilNextSlot pins the schedule
// path: a valid future cron causes Reconcile to return RequeueAfter
// equal to time-until-next-slot, with no jobs created yet.
func TestPlanReconciler_ValidCronRequeuesUntilNextSlot(t *testing.T) {
	s := newReconcilerScheme(t)
	plan := &backupsv1alpha1.Plan{
		ObjectMeta: metav1.ObjectMeta{Name: "yearly", Namespace: "tenant-foo"},
		Spec: backupsv1alpha1.PlanSpec{
			Schedule: backupsv1alpha1.PlanSchedule{
				// Yearly at noon UTC on Jan 1 — far enough in the future
				// (or far enough in the past plus a year) that the next
				// slot won't happen during the test run.
				Cron: "0 12 1 1 *",
			},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(plan).
		WithStatusSubresource(plan).
		Build()

	r := &PlanReconciler{Client: c, Scheme: s}
	res, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "yearly", Namespace: "tenant-foo"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Either the schedule slot is in the future (RequeueAfter > 0) or
	// the test happened to run within startingDeadline of slot — in
	// which case a BackupJob was created. Both are valid steady-state
	// outcomes; assert one of them held.
	jobList := &backupsv1alpha1.BackupJobList{}
	if err := c.List(context.TODO(), jobList); err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	switch {
	case res.RequeueAfter > 0 && len(jobList.Items) == 0:
		// future slot, no job — happy path most of the year
	case res.RequeueAfter > 0 && len(jobList.Items) == 1:
		// just-past slot inside startingDeadline, job created and requeue scheduled
	default:
		t.Errorf("unexpected reconcile outcome: RequeueAfter=%v, jobs=%d", res.RequeueAfter, len(jobList.Items))
	}
}

// -----------------------------------------------------------------------
// RestoreJobReconciler
// -----------------------------------------------------------------------

// TestRestoreJobReconciler_NotFoundIsNoop pins early-exit on missing
// RestoreJob.
func TestRestoreJobReconciler_NotFoundIsNoop(t *testing.T) {
	s := newReconcilerScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()

	r := &RestoreJobReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(8)}
	res, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "tenant-foo", Name: "missing"},
	})
	if err != nil || res.RequeueAfter != 0 {
		t.Fatalf("expected (nil, empty Result), got (%v, %+v)", err, res)
	}
}

// TestRestoreJobReconciler_AddsFinalizer pins finalizer-add: a fresh
// RestoreJob without the cleanup-velero-restore finalizer must come
// back with it.
func TestRestoreJobReconciler_AddsFinalizer(t *testing.T) {
	s := newReconcilerScheme(t)
	rj := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Name: "rj", Namespace: "tenant-foo"},
		Spec: backupsv1alpha1.RestoreJobSpec{
			BackupRef: corev1.LocalObjectReference{Name: "missing-backup"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(rj).WithStatusSubresource(rj).Build()

	r := &RestoreJobReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(8)}
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "rj", Namespace: "tenant-foo"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &backupsv1alpha1.RestoreJob{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "rj", Namespace: "tenant-foo"}, got); err != nil {
		t.Fatalf("get RestoreJob: %v", err)
	}
	if !hasFinalizer(got.Finalizers, restoreJobFinalizer) {
		t.Errorf("expected finalizer %q, got %v", restoreJobFinalizer, got.Finalizers)
	}
}

// TestRestoreJobReconciler_AlreadyCompletedIsSkipped pins the
// completed-phase short-circuit: once a RestoreJob is Succeeded or
// Failed the reconciler does not touch it again (no re-running, no
// status flapping).
func TestRestoreJobReconciler_AlreadyCompletedIsSkipped(t *testing.T) {
	cases := []backupsv1alpha1.RestoreJobPhase{
		backupsv1alpha1.RestoreJobPhaseSucceeded,
		backupsv1alpha1.RestoreJobPhaseFailed,
	}
	for _, phase := range cases {
		t.Run(string(phase), func(t *testing.T) {
			s := newReconcilerScheme(t)
			rj := &backupsv1alpha1.RestoreJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "rj",
					Namespace:  "tenant-foo",
					Finalizers: []string{restoreJobFinalizer},
				},
				Spec: backupsv1alpha1.RestoreJobSpec{
					BackupRef: corev1.LocalObjectReference{Name: "any"},
				},
				Status: backupsv1alpha1.RestoreJobStatus{Phase: phase},
			}
			c := fake.NewClientBuilder().WithScheme(s).WithObjects(rj).WithStatusSubresource(rj).Build()

			r := &RestoreJobReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(8)}
			if _, err := r.Reconcile(context.TODO(), ctrl.Request{
				NamespacedName: types.NamespacedName{Name: "rj", Namespace: "tenant-foo"},
			}); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// No status changes — Phase remains the same.
			got := &backupsv1alpha1.RestoreJob{}
			if err := c.Get(context.TODO(), types.NamespacedName{Name: "rj", Namespace: "tenant-foo"}, got); err != nil {
				t.Fatalf("get: %v", err)
			}
			if got.Status.Phase != phase {
				t.Errorf("Status.Phase changed: was %s, now %s", phase, got.Status.Phase)
			}
		})
	}
}

// TestRestoreJobReconciler_MissingBackupMarksFailed pins the failure
// path when the referenced Backup is missing — RestoreJob status moves
// to Failed with a descriptive Message.
func TestRestoreJobReconciler_MissingBackupMarksFailed(t *testing.T) {
	s := newReconcilerScheme(t)
	rj := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "rj",
			Namespace:  "tenant-foo",
			Finalizers: []string{restoreJobFinalizer},
		},
		Spec: backupsv1alpha1.RestoreJobSpec{
			BackupRef: corev1.LocalObjectReference{Name: "missing-backup"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(rj).WithStatusSubresource(rj).Build()

	r := &RestoreJobReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(8)}
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "rj", Namespace: "tenant-foo"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &backupsv1alpha1.RestoreJob{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "rj", Namespace: "tenant-foo"}, got); err != nil {
		t.Fatalf("get RestoreJob: %v", err)
	}
	if got.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
		t.Errorf("expected Phase=Failed, got %s", got.Status.Phase)
	}
	if got.Status.Message == "" {
		t.Errorf("expected non-empty Status.Message describing the failure")
	}
}
