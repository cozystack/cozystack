package backupcontroller

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
)

// BackupVeleroStrategyReconciler reconciles BackupJob with a strategy referencing
// Velero.strategy.backups.cozystack.io objects.
type BackupJobReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *BackupJobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)
	j := &backupsv1alpha1.BackupJob{}
	err := r.Get(ctx, types.NamespacedName{Namespace: req.Namespace, Name: req.Name}, j)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if j.Spec.StrategyRef.APIGroup == nil || *j.Spec.StrategyRef.APIGroup != strategyv1alpha1.GroupVersion.Group {
		return ctrl.Result{}, nil
	}
	switch j.Spec.StrategyRef.Kind {
	case strategyv1alpha1.JobStrategyKind:
		return r.reconcileJob(ctx, j)
	case strategyv1alpha1.VeleroStrategyKind:
		return r.reconcileVelero(ctx, j)
	default:
		return ctrl.Result{}, nil
	}

}

// SetupWithManager registers our controller with the Manager and sets up watches.
func (r *BackupJobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&backupsv1alpha1.BackupJob{}).
		Complete(r)
}
