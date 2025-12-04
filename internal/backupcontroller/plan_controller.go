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
	minRequeueDelay         = 30 * time.Second
	startingDeadlineSeconds = 300 * time.Second
)

// PlanReconciler reconciles a Plan object
type PlanReconciler struct {
	client.Client
	Scheme *runtime.Scheme
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

	tCheck := time.Now().Add(-startingDeadlineSeconds)
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

	if time.Now().Before(tNext) {
		return ctrl.Result{RequeueAfter: tNext.Sub(time.Now())}, nil
	}

	job := factory.BackupJob(p, tNext)
	if err := controllerutil.SetControllerReference(p, job, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.Create(ctx, job); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return ctrl.Result{RequeueAfter: startingDeadlineSeconds}, nil
		}
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: startingDeadlineSeconds}, nil
}

// SetupWithManager registers our controller with the Manager and sets up watches.
func (r *PlanReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&backupsv1alpha1.Plan{}).
		Complete(r)
}
