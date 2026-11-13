package backupcontroller

import (
	"context"
	"fmt"
	"time"

	cron "github.com/robfig/cron/v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	"github.com/cozystack/cozystack/internal/backupcontroller/factory"
)

const (
	minRequeueDelay     = 30 * time.Second
	startingDeadline    = 300 * time.Second
)

// Condition Type on a Plan whose last due run was held back because a
// BackupJob the Plan created earlier had not finished. It is removed once the
// Plan's run for a due tick exists, whether the held run starts late or the
// run of a later tick starts.
const planConditionRunSkipped = "RunSkipped"

// PlanReconciler reconciles a Plan object
type PlanReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// now overrides the clock; nil means time.Now. Set by unit tests, which
	// derive the name of the run due at a given time.
	now func() time.Time
}

func (r *PlanReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	log.V(2).Info("reconciling")

	p := &backupsv1alpha1.Plan{}

	if err := r.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: req.Name}, p); err != nil {
		if apierrors.IsNotFound(err) {
			log.V(3).Info("Plan not found")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	now := time.Now()
	if r.now != nil {
		now = r.now()
	}
	tCheck := now.Add(-startingDeadline)
	sch, err := cron.ParseStandard(p.Spec.Schedule.Cron)
	if err != nil {
		errWrapped := fmt.Errorf("could not parse cron %s: %w", p.Spec.Schedule.Cron, err)
		log.Error(err, "could not parse cron", "cron", p.Spec.Schedule.Cron)
		meta.SetStatusCondition(&p.Status.Conditions, metav1.Condition{
			Type:    backupsv1alpha1.PlanConditionError,
			Status:  metav1.ConditionTrue,
			Reason:  "Failed to parse cron spec",
			Message: errWrapped.Error(),
		})
		if err := r.Status().Update(ctx, p); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Clear error condition if cron parsing succeeds
	if condition := meta.FindStatusCondition(p.Status.Conditions, backupsv1alpha1.PlanConditionError); condition != nil && condition.Status == metav1.ConditionTrue {
		meta.SetStatusCondition(&p.Status.Conditions, metav1.Condition{
			Type:    backupsv1alpha1.PlanConditionError,
			Status:  metav1.ConditionFalse,
			Reason:  "Cron spec is valid",
			Message: "The cron schedule has been successfully parsed",
		})
		if err := r.Status().Update(ctx, p); err != nil {
			return ctrl.Result{}, err
		}
	}

	tNext := sch.Next(tCheck)

	if now.Before(tNext) {
		return ctrl.Result{RequeueAfter: tNext.Sub(now)}, nil
	}

	job := factory.BackupJob(p, tNext)
	unfinished, current, err := r.planRuns(ctx, p, job.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	if unfinished != nil {
		// Held like a CronJob with concurrencyPolicy Forbid: the tick still
		// runs if no run of the Plan is unfinished by its startingDeadline.
		state := fmt.Sprintf("phase %s", unfinished.Status.Phase)
		if unfinished.Status.Phase == backupsv1alpha1.BackupJobPhaseEmpty {
			// A run with no phase may wait on a missing BackupClass or
			// strategy, or a driver may not have set one yet; its Ready
			// condition says which.
			state = "no phase yet, see its Ready condition"
		}
		message := fmt.Sprintf("the run scheduled for %s is held back: BackupJob %s of this Plan has not finished, %s; the run is skipped unless no run of this Plan is unfinished by %s",
			tNext.UTC().Format(time.RFC3339), unfinished.Name, state, tNext.Add(startingDeadline).UTC().Format(time.RFC3339))
		if meta.SetStatusCondition(&p.Status.Conditions, metav1.Condition{
			Type:    planConditionRunSkipped,
			Status:  metav1.ConditionTrue,
			Reason:  "PreviousRunActive",
			Message: message,
		}) {
			if err := r.Status().Update(ctx, p); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{RequeueAfter: minRequeueDelay}, nil
	}

	if !current {
		if err := controllerutil.SetControllerReference(p, job, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Create(ctx, job); err != nil && !apierrors.IsAlreadyExists(err) {
			return ctrl.Result{}, err
		}
	}
	if meta.RemoveStatusCondition(&p.Status.Conditions, planConditionRunSkipped) {
		if err := r.Status().Update(ctx, p); err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{RequeueAfter: startingDeadline}, nil
}

// planRuns returns the oldest BackupJob p created that has not finished, and
// whether the run named current exists. Once it does, nothing is held: that
// run started, whatever an older one is doing. Only jobs p controls count, so
// a BackupJob made by hand or by another Plan does not hold p's schedule. The
// cache lists in no fixed order, so the oldest, ties broken by name, keeps the
// hold naming one job.
func (r *PlanReconciler) planRuns(ctx context.Context, p *backupsv1alpha1.Plan, current string) (*backupsv1alpha1.BackupJob, bool, error) {
	jobs := &backupsv1alpha1.BackupJobList{}
	if err := r.List(ctx, jobs, client.InNamespace(p.Namespace)); err != nil {
		return nil, false, err
	}
	var unfinished *backupsv1alpha1.BackupJob
	for i := range jobs.Items {
		j := &jobs.Items[i]
		if !metav1.IsControlledBy(j, p) {
			continue
		}
		if j.Name == current {
			return nil, true, nil
		}
		if j.Status.Phase == backupsv1alpha1.BackupJobPhaseSucceeded || j.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
			continue
		}
		if unfinished == nil || j.CreationTimestamp.Before(&unfinished.CreationTimestamp) ||
			(j.CreationTimestamp.Equal(&unfinished.CreationTimestamp) && j.Name < unfinished.Name) {
			unfinished = j
		}
	}
	return unfinished, false, nil
}

// SetupWithManager registers our controller with the Manager and sets up watches.
func (r *PlanReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&backupsv1alpha1.Plan{}).
		Owns(&backupsv1alpha1.BackupJob{}).
		Complete(r)
}
