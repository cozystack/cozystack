package backupcontroller

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	cron "github.com/robfig/cron/v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	"github.com/cozystack/cozystack/internal/backupcontroller/factory"
)

// planTestNow is the clock of the Plan reconciles in these tests, half a
// minute past a tick and far from the real clock, so the run a test derives
// is the one the Plan sees due only if the Plan reads the injected clock.
var planTestNow = time.Date(2026, 1, 1, 12, 0, 30, 0, time.UTC)

// planEveryMinute is due on every reconcile: its last tick is always within
// startingDeadline of now.
func planEveryMinute() *backupsv1alpha1.Plan {
	return &backupsv1alpha1.Plan{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: "tenant-foo", UID: "plan-uid"},
		Spec: backupsv1alpha1.PlanSpec{
			Schedule: backupsv1alpha1.PlanSchedule{Cron: "* * * * *"},
		},
	}
}

// dueJobName is the name of the BackupJob the Plan creates for its current
// tick.
func dueJobName(t *testing.T, p *backupsv1alpha1.Plan) string {
	t.Helper()
	sch, err := cron.ParseStandard(p.Spec.Schedule.Cron)
	if err != nil {
		t.Fatalf("parse cron: %v", err)
	}
	return factory.BackupJob(p, sch.Next(planTestNow.Add(-startingDeadline))).Name
}

func planRun(p *backupsv1alpha1.Plan, name string, phase backupsv1alpha1.BackupJobPhase, owned bool) *backupsv1alpha1.BackupJob {
	j := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: p.Namespace},
		Status:     backupsv1alpha1.BackupJobStatus{Phase: phase},
	}
	if owned {
		controller := true
		j.OwnerReferences = []metav1.OwnerReference{{
			APIVersion: backupsv1alpha1.GroupVersion.String(),
			Kind:       "Plan",
			Name:       p.Name,
			UID:        p.UID,
			Controller: &controller,
		}}
	}
	return j
}

// reconcilePlan runs one reconcile and returns the result, the persisted Plan
// and the names of the BackupJobs that exist afterwards.
func reconcilePlan(t *testing.T, p *backupsv1alpha1.Plan, objs ...client.Object) (ctrl.Result, *backupsv1alpha1.Plan, []string) {
	t.Helper()
	res, got, jobs, _ := reconcilePlanCountingCreates(t, p, objs...)
	return res, got, jobs
}

// reconcilePlanCountingCreates is reconcilePlan that also reports how many
// objects the reconcile asked the apiserver to create.
func reconcilePlanCountingCreates(t *testing.T, p *backupsv1alpha1.Plan, objs ...client.Object) (ctrl.Result, *backupsv1alpha1.Plan, []string, int) {
	t.Helper()
	s := newReconcilerScheme(t)
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(append([]client.Object{p}, objs...)...).
		WithStatusSubresource(p).
		Build()
	creates := 0
	r := &PlanReconciler{Scheme: s, now: func() time.Time { return planTestNow }, Client: interceptor.NewClient(c, interceptor.Funcs{
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			creates++
			return cl.Create(ctx, obj, opts...)
		},
	})}
	key := types.NamespacedName{Namespace: p.Namespace, Name: p.Name}
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := &backupsv1alpha1.Plan{}
	if err := c.Get(context.Background(), key, got); err != nil {
		t.Fatalf("get Plan: %v", err)
	}
	jobs := &backupsv1alpha1.BackupJobList{}
	if err := c.List(context.Background(), jobs, client.InNamespace(p.Namespace)); err != nil {
		t.Fatalf("list BackupJobs: %v", err)
	}
	names := make([]string, 0, len(jobs.Items))
	for _, j := range jobs.Items {
		names = append(names, j.Name)
	}
	return res, got, names, creates
}

// TestPlanReconciler_HoldsRunWhilePreviousRuns: a Plan does not start a run
// while a BackupJob it created earlier has not finished, so a backup that
// takes longer than the schedule interval does not queue the next one behind
// it. A job no backup controller has started has not finished either.
func TestPlanReconciler_HoldsRunWhilePreviousRuns(t *testing.T) {
	for _, phase := range []backupsv1alpha1.BackupJobPhase{
		backupsv1alpha1.BackupJobPhaseEmpty,
		backupsv1alpha1.BackupJobPhasePending,
		backupsv1alpha1.BackupJobPhaseRunning,
	} {
		t.Run("phase "+strconv.Quote(string(phase)), func(t *testing.T) {
			p := planEveryMinute()
			res, got, jobs := reconcilePlan(t, p, planRun(p, "nightly-previous", phase, true))
			if len(jobs) != 1 {
				t.Fatalf("BackupJobs = %v, want only the previous run", jobs)
			}
			cond := meta.FindStatusCondition(got.Status.Conditions, planConditionRunSkipped)
			if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != "PreviousRunActive" {
				t.Fatalf("%s condition = %+v, want True/PreviousRunActive", planConditionRunSkipped, cond)
			}
			if !strings.Contains(cond.Message, "nightly-previous") {
				t.Errorf("%s message %q does not name the run it waits for", planConditionRunSkipped, cond.Message)
			}
			if phase == backupsv1alpha1.BackupJobPhaseEmpty && !strings.Contains(cond.Message, "see its Ready condition") {
				t.Errorf("%s message %q does not point at the Ready condition of the run it waits for, which carries why it has no phase", planConditionRunSkipped, cond.Message)
			}
			if phase != backupsv1alpha1.BackupJobPhaseEmpty && !strings.Contains(cond.Message, "phase "+string(phase)) {
				t.Errorf("%s message %q does not give the phase of the run it waits for", planConditionRunSkipped, cond.Message)
			}
			// The due tick is the earliest one inside the starting deadline,
			// and the run is skipped once that deadline after it has passed.
			tick := time.Date(2026, 1, 1, 11, 56, 0, 0, time.UTC)
			for _, want := range []string{
				"the run scheduled for " + tick.Format(time.RFC3339),
				"unfinished by " + tick.Add(startingDeadline).Format(time.RFC3339),
			} {
				if !strings.Contains(cond.Message, want) {
					t.Errorf("%s message %q does not say %q", planConditionRunSkipped, cond.Message, want)
				}
			}
			if res.RequeueAfter <= 0 || res.RequeueAfter > minRequeueDelay {
				t.Errorf("RequeueAfter = %s, want a recheck within %s so the run still starts if no run of the Plan is unfinished by the starting deadline", res.RequeueAfter, minRequeueDelay)
			}
		})
	}
}

// TestPlanReconciler_RunsOnceThePreviousRunFinished: a finished previous run,
// whether it succeeded or failed, does not hold the next one, and the
// condition left by a held run is cleared once a run starts.
func TestPlanReconciler_RunsOnceThePreviousRunFinished(t *testing.T) {
	for _, phase := range []backupsv1alpha1.BackupJobPhase{
		backupsv1alpha1.BackupJobPhaseSucceeded,
		backupsv1alpha1.BackupJobPhaseFailed,
	} {
		t.Run(string(phase), func(t *testing.T) {
			p := planEveryMinute()
			p.Status.Conditions = []metav1.Condition{{
				Type:               planConditionRunSkipped,
				Status:             metav1.ConditionTrue,
				Reason:             "PreviousRunActive",
				LastTransitionTime: metav1.NewTime(time.Now().Add(-time.Hour)),
			}}
			_, got, jobs := reconcilePlan(t, p, planRun(p, "nightly-previous", phase, true))
			if len(jobs) != 2 {
				t.Fatalf("BackupJobs = %v, want the previous run and a new one", jobs)
			}
			if cond := meta.FindStatusCondition(got.Status.Conditions, planConditionRunSkipped); cond != nil {
				t.Errorf("%s condition = %+v after a run started, want it cleared", planConditionRunSkipped, cond)
			}
		})
	}
}

// TestPlanReconciler_OnlyItsOwnRunsHoldIt: a running BackupJob the Plan did
// not create, made by hand or by another Plan, does not hold its schedule.
func TestPlanReconciler_OnlyItsOwnRunsHoldIt(t *testing.T) {
	p := planEveryMinute()
	other := planRun(p, "manual", backupsv1alpha1.BackupJobPhaseRunning, true)
	other.OwnerReferences[0].UID = "other-plan-uid"
	_, got, jobs := reconcilePlan(t, p,
		planRun(p, "adhoc", backupsv1alpha1.BackupJobPhaseRunning, false),
		other,
	)
	if len(jobs) != 3 {
		t.Fatalf("BackupJobs = %v, want a new run besides the two foreign ones", jobs)
	}
	if cond := meta.FindStatusCondition(got.Status.Conditions, planConditionRunSkipped); cond != nil {
		t.Errorf("%s condition = %+v from runs the Plan does not own", planConditionRunSkipped, cond)
	}
}

// TestPlanReconciler_CurrentRunIsNotHeldByItself: once the run for the
// current tick exists, a later pass inside the starting deadline does not
// report it as held, even with an older run of the Plan still going, as on a
// cluster upgraded while two runs overlapped.
func TestPlanReconciler_CurrentRunIsNotHeldByItself(t *testing.T) {
	for _, phase := range []backupsv1alpha1.BackupJobPhase{
		backupsv1alpha1.BackupJobPhaseRunning,
		backupsv1alpha1.BackupJobPhaseSucceeded,
		backupsv1alpha1.BackupJobPhaseFailed,
	} {
		t.Run(string(phase), func(t *testing.T) {
			p := planEveryMinute()
			current := dueJobName(t, p)
			_, got, jobs := reconcilePlan(t, p,
				planRun(p, current, phase, true),
				// Named to list before the current run, so the scan meets the
				// unfinished one first and must still not report a hold.
				planRun(p, "nightly-0older", backupsv1alpha1.BackupJobPhaseRunning, true),
			)
			if len(jobs) != 2 {
				t.Fatalf("BackupJobs = %v, want no new run", jobs)
			}
			if cond := meta.FindStatusCondition(got.Status.Conditions, planConditionRunSkipped); cond != nil {
				t.Errorf("%s condition = %+v while the current run exists", planConditionRunSkipped, cond)
			}
		})
	}
}

// TestPlanReconciler_CurrentRunCreatedElsewhere: a cache that has not seen
// the current run yet lets the Plan try to create it again. The apiserver's
// AlreadyExists means the run started, so the held condition is cleared.
func TestPlanReconciler_CurrentRunCreatedElsewhere(t *testing.T) {
	p := planEveryMinute()
	p.Status.Conditions = []metav1.Condition{{
		Type:               planConditionRunSkipped,
		Status:             metav1.ConditionTrue,
		Reason:             "PreviousRunActive",
		LastTransitionTime: metav1.NewTime(time.Now().Add(-time.Hour)),
	}}
	s := newReconcilerScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(p).WithStatusSubresource(p).Build()
	r := &PlanReconciler{Scheme: s, now: func() time.Time { return planTestNow }, Client: interceptor.NewClient(c, interceptor.Funcs{
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			return apierrors.NewAlreadyExists(backupsv1alpha1.GroupVersion.WithResource("backupjobs").GroupResource(), obj.GetName())
		},
	})}
	key := types.NamespacedName{Namespace: p.Namespace, Name: p.Name}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := &backupsv1alpha1.Plan{}
	if err := c.Get(context.Background(), key, got); err != nil {
		t.Fatalf("get Plan: %v", err)
	}
	if cond := meta.FindStatusCondition(got.Status.Conditions, planConditionRunSkipped); cond != nil {
		t.Errorf("%s condition = %+v after the current run turned out to exist", planConditionRunSkipped, cond)
	}
}

// TestPlanReconciler_ExistingCurrentRunClearsHold: a hold the Plan recorded
// earlier is cleared once the current run exists, also when the pass that
// created it lost its status write or read the Plan before the hold landed.
func TestPlanReconciler_ExistingCurrentRunClearsHold(t *testing.T) {
	p := planEveryMinute()
	p.Status.Conditions = []metav1.Condition{{
		Type:               planConditionRunSkipped,
		Status:             metav1.ConditionTrue,
		Reason:             "PreviousRunActive",
		LastTransitionTime: metav1.NewTime(time.Now().Add(-time.Hour)),
	}}
	_, got, jobs, creates := reconcilePlanCountingCreates(t, p, planRun(p, dueJobName(t, p), backupsv1alpha1.BackupJobPhaseRunning, true))
	if len(jobs) != 1 {
		t.Fatalf("BackupJobs = %v, want only the current run", jobs)
	}
	if creates != 0 {
		t.Errorf("the reconcile tried to create %d object(s) although the current run exists", creates)
	}
	if cond := meta.FindStatusCondition(got.Status.Conditions, planConditionRunSkipped); cond != nil {
		t.Errorf("%s condition = %+v while the current run exists, want it cleared", planConditionRunSkipped, cond)
	}
}

// TestPlanReconciler_UnreadableRunsHoldTheRun: without the list of its runs
// the Plan cannot tell whether one is still going, so it creates nothing and
// retries rather than starting a run on top of an unfinished one.
func TestPlanReconciler_UnreadableRunsHoldTheRun(t *testing.T) {
	p := planEveryMinute()
	s := newReconcilerScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(p).WithStatusSubresource(p).Build()
	listErr := errors.New("cache not synced")
	r := &PlanReconciler{Scheme: s, now: func() time.Time { return planTestNow }, Client: interceptor.NewClient(c, interceptor.Funcs{
		List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*backupsv1alpha1.BackupJobList); ok {
				return listErr
			}
			return cl.List(ctx, list, opts...)
		},
	})}
	key := types.NamespacedName{Namespace: p.Namespace, Name: p.Name}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); !errors.Is(err, listErr) {
		t.Fatalf("Reconcile error = %v, want the list error", err)
	}
	jobs := &backupsv1alpha1.BackupJobList{}
	if err := c.List(context.Background(), jobs); err != nil {
		t.Fatalf("list BackupJobs: %v", err)
	}
	if len(jobs.Items) != 0 {
		t.Errorf("a BackupJob was created although the Plan's runs could not be read: %s", jobs.Items[0].Name)
	}
}

// TestPlanReconciler_HoldsBehindTheRunItCreated: the run the Plan creates is
// controlled by it, so that run, once started, holds the Plan's next tick.
func TestPlanReconciler_HoldsBehindTheRunItCreated(t *testing.T) {
	p := planEveryMinute()
	s := newReconcilerScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(p).WithStatusSubresource(p).Build()
	now := planTestNow
	r := &PlanReconciler{Client: c, Scheme: s, now: func() time.Time { return now }}
	ctx := context.Background()
	key := types.NamespacedName{Namespace: p.Namespace, Name: p.Name}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	jobs := &backupsv1alpha1.BackupJobList{}
	if err := c.List(ctx, jobs, client.InNamespace(p.Namespace)); err != nil || len(jobs.Items) != 1 {
		t.Fatalf("BackupJobs after the first tick = %d (%v), want 1", len(jobs.Items), err)
	}
	first := jobs.Items[0]
	first.Status.Phase = backupsv1alpha1.BackupJobPhaseRunning
	if err := c.Update(ctx, &first); err != nil {
		t.Fatalf("mark the first run Running: %v", err)
	}

	now = now.Add(time.Minute)
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if err := c.List(ctx, jobs, client.InNamespace(p.Namespace)); err != nil || len(jobs.Items) != 1 {
		t.Fatalf("BackupJobs after the next tick = %d (%v), want the first run only", len(jobs.Items), err)
	}
	got := &backupsv1alpha1.Plan{}
	if err := c.Get(ctx, key, got); err != nil {
		t.Fatalf("get Plan: %v", err)
	}
	if cond := meta.FindStatusCondition(got.Status.Conditions, planConditionRunSkipped); cond == nil || cond.Status != metav1.ConditionTrue || !strings.Contains(cond.Message, first.Name) {
		t.Fatalf("%s condition = %+v, want True naming %s", planConditionRunSkipped, cond, first.Name)
	}
}

// TestPlanReconciler_RequeuesUntilTheNextTickOnItsClock: before the next tick
// the Plan waits exactly until it, measured on the same clock it decided by.
func TestPlanReconciler_RequeuesUntilTheNextTickOnItsClock(t *testing.T) {
	p := planEveryMinute()
	p.Spec.Schedule.Cron = "0 13 * * *"
	res, _, jobs := reconcilePlan(t, p)
	if len(jobs) != 0 {
		t.Fatalf("BackupJobs = %v before the tick, want none", jobs)
	}
	if want := 59*time.Minute + 30*time.Second; res.RequeueAfter != want {
		t.Errorf("RequeueAfter = %s, want %s from %s to the 13:00 tick", res.RequeueAfter, want, planTestNow.Format(time.Kitchen))
	}
}

// TestPlanReconciler_StatusWriteErrorsAreReturned: a failed write of the hold,
// or of its removal, is returned for a retry. A swallowed removal would not
// be retried by this pass's own requeue, which lands past the tick.
func TestPlanReconciler_StatusWriteErrorsAreReturned(t *testing.T) {
	writeErr := errors.New("apiserver unavailable")
	for _, tc := range []struct {
		name string
		objs func(p *backupsv1alpha1.Plan) []client.Object
	}{
		{name: "hold", objs: func(p *backupsv1alpha1.Plan) []client.Object {
			return []client.Object{planRun(p, "nightly-previous", backupsv1alpha1.BackupJobPhaseRunning, true)}
		}},
		{name: "removal", objs: func(p *backupsv1alpha1.Plan) []client.Object {
			p.Status.Conditions = []metav1.Condition{{
				Type:               planConditionRunSkipped,
				Status:             metav1.ConditionTrue,
				Reason:             "PreviousRunActive",
				LastTransitionTime: metav1.NewTime(planTestNow.Add(-time.Hour)),
			}}
			return nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := planEveryMinute()
			objs := append([]client.Object{p}, tc.objs(p)...)
			s := newReconcilerScheme(t)
			c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(p).Build()
			r := &PlanReconciler{Scheme: s, now: func() time.Time { return planTestNow }, Client: interceptor.NewClient(c, interceptor.Funcs{
				SubResourceUpdate: func(ctx context.Context, cl client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
					if _, ok := obj.(*backupsv1alpha1.Plan); ok {
						return writeErr
					}
					return cl.SubResource(sub).Update(ctx, obj, opts...)
				},
			})}
			key := types.NamespacedName{Namespace: p.Namespace, Name: p.Name}
			if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); !errors.Is(err, writeErr) {
				t.Fatalf("Reconcile error = %v, want the status write error", err)
			}
		})
	}
}

// TestPlanReconciler_HoldNamesTheOldestUnfinishedRun: the cache lists a
// namespace in no fixed order, so with several unfinished runs the hold names
// the oldest one, and passes that list them differently write nothing new.
func TestPlanReconciler_HoldNamesTheOldestUnfinishedRun(t *testing.T) {
	for _, tc := range []struct {
		name       string
		aAge, bAge time.Duration
		wantInHold string
	}{
		{name: "oldest by creation", aAge: time.Hour, bAge: 2 * time.Hour, wantInHold: "nightly-b"},
		{name: "same second, first by name", aAge: time.Hour, bAge: time.Hour, wantInHold: "nightly-a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			holdNamesTheOldestUnfinishedRun(t, tc.aAge, tc.bAge, tc.wantInHold)
		})
	}
}

func holdNamesTheOldestUnfinishedRun(t *testing.T, aAge, bAge time.Duration, want string) {
	t.Helper()
	p := planEveryMinute()
	older := planRun(p, "nightly-b", backupsv1alpha1.BackupJobPhaseRunning, true)
	older.CreationTimestamp = metav1.NewTime(planTestNow.Add(-bAge))
	newer := planRun(p, "nightly-a", backupsv1alpha1.BackupJobPhaseRunning, true)
	newer.CreationTimestamp = metav1.NewTime(planTestNow.Add(-aAge))
	s := newReconcilerScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(p, older, newer).WithStatusSubresource(p).Build()
	pass, writes := 0, 0
	r := &PlanReconciler{Scheme: s, now: func() time.Time { return planTestNow }, Client: interceptor.NewClient(c, interceptor.Funcs{
		List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if err := cl.List(ctx, list, opts...); err != nil {
				return err
			}
			if jobs, ok := list.(*backupsv1alpha1.BackupJobList); ok && pass%2 == 1 {
				slices.Reverse(jobs.Items)
			}
			return nil
		},
		SubResourceUpdate: func(ctx context.Context, cl client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			writes++
			return cl.SubResource(sub).Update(ctx, obj, opts...)
		},
	})}
	key := types.NamespacedName{Namespace: p.Namespace, Name: p.Name}
	for pass = 0; pass < 4; pass++ {
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatalf("Reconcile pass %d: %v", pass, err)
		}
	}
	got := &backupsv1alpha1.Plan{}
	if err := c.Get(context.Background(), key, got); err != nil {
		t.Fatalf("get Plan: %v", err)
	}
	if cond := meta.FindStatusCondition(got.Status.Conditions, planConditionRunSkipped); cond == nil || !strings.Contains(cond.Message, "BackupJob "+want+" ") {
		t.Fatalf("%s condition = %+v, want it to name %s", planConditionRunSkipped, cond, want)
	}
	if writes != 1 {
		t.Errorf("Plan status written %d times over 4 passes that differ only in list order, want 1", writes)
	}
}

// TestPlanReconciler_CreateErrorKeepsTheHold: a failed create of the due run
// is returned for a retry and leaves a recorded hold in place, since no run
// started.
func TestPlanReconciler_CreateErrorKeepsTheHold(t *testing.T) {
	p := planEveryMinute()
	p.Status.Conditions = []metav1.Condition{{
		Type:               planConditionRunSkipped,
		Status:             metav1.ConditionTrue,
		Reason:             "PreviousRunActive",
		LastTransitionTime: metav1.NewTime(planTestNow.Add(-time.Hour)),
	}}
	s := newReconcilerScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(p).WithStatusSubresource(p).Build()
	createErr := errors.New("admission webhook unavailable")
	r := &PlanReconciler{Scheme: s, now: func() time.Time { return planTestNow }, Client: interceptor.NewClient(c, interceptor.Funcs{
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			return createErr
		},
	})}
	key := types.NamespacedName{Namespace: p.Namespace, Name: p.Name}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); !errors.Is(err, createErr) {
		t.Fatalf("Reconcile error = %v, want the create error", err)
	}
	got := &backupsv1alpha1.Plan{}
	if err := c.Get(context.Background(), key, got); err != nil {
		t.Fatalf("get Plan: %v", err)
	}
	if !meta.IsStatusConditionTrue(got.Status.Conditions, planConditionRunSkipped) {
		t.Errorf("%s was cleared although no run was created", planConditionRunSkipped)
	}
}

// TestPlanReconciler_HoldOfAStartedRunWithoutPhase: some drivers record the
// start of a run before they set its phase, and leave it so while their
// strategy is missing. The hold then points at the run's own conditions.
func TestPlanReconciler_HoldOfAStartedRunWithoutPhase(t *testing.T) {
	p := planEveryMinute()
	run := planRun(p, "nightly-previous", backupsv1alpha1.BackupJobPhaseEmpty, true)
	started := metav1.NewTime(planTestNow.Add(-time.Hour))
	run.Status.StartedAt = &started
	_, got, _ := reconcilePlan(t, p, run)
	cond := meta.FindStatusCondition(got.Status.Conditions, planConditionRunSkipped)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Fatalf("%s condition = %+v, want True", planConditionRunSkipped, cond)
	}
	if !strings.Contains(cond.Message, "Ready condition") {
		t.Errorf("%s message %q does not point at the run's Ready condition", planConditionRunSkipped, cond.Message)
	}
}
