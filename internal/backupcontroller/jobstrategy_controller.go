package backupcontroller

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
)

// BackupJobStrategyReconciler reconciles BackupJob with a strategy referencing
// Job.strategy.backups.cozystack.io objects.
type BackupJobStrategyReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *BackupJobStrategyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)
	return ctrl.Result{}, nil
}

// SetupWithManager registers our controller with the Manager and sets up watches.
func (r *BackupJobStrategyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&backupsv1alpha1.BackupJob{}).
		Complete(r)
}
