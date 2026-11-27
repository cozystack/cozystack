package backupcontroller

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
)


// TestPlanReconciler_NotFoundIsNoop pins the early-exit on missing Plan.
func TestPlanReconciler_NotFoundIsNoop(t *testing.T) {
	s := dispatchScheme(t)
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
	s := dispatchScheme(t)
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
	s := dispatchScheme(t)
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

