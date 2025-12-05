package backupcontroller

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
)

// JobStrategyReconciler reconiles
type BackupJobStrategyReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *BackupJobStrategyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	log.V(2).Info("reconciling")

	j := &backupsv1alpha1.BackupJob{}

	if err := r.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: req.Name}, j); err != nil {
		if apierrors.IsNotFound(err) {
			log.V(3).Info("BackupJob not found")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if j.Spec.StrategyRef.APIGroup != &strategyv1alpha1.GroupVersion.Group {
		return ctrl.Result{}, nil
	}
	if j.Spec.StrategyRef.Kind != "Job" {
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}

// SetupWithManager registers our controller with the Manager and sets up watches.
func (r *BackupJobStrategyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&backupsv1alpha1.BackupJob{}).
		Complete(r)
}
