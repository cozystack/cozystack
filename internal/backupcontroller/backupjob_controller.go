package backupcontroller

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
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
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

func (r *BackupJobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("reconciling BackupJob", "namespace", req.Namespace, "name", req.Name)

	j := &backupsv1alpha1.BackupJob{}
	err := r.Get(ctx, types.NamespacedName{Namespace: req.Namespace, Name: req.Name}, j)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(1).Info("BackupJob not found, skipping")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "failed to get BackupJob")
		return ctrl.Result{}, err
	}

	if j.Spec.StrategyRef.APIGroup == nil {
		logger.V(1).Info("BackupJob has nil StrategyRef.APIGroup, skipping", "backupjob", j.Name)
		return ctrl.Result{}, nil
	}

	if *j.Spec.StrategyRef.APIGroup != strategyv1alpha1.GroupVersion.Group {
		logger.V(1).Info("BackupJob StrategyRef.APIGroup doesn't match, skipping",
			"backupjob", j.Name,
			"expected", strategyv1alpha1.GroupVersion.Group,
			"got", *j.Spec.StrategyRef.APIGroup)
		return ctrl.Result{}, nil
	}

	logger.Info("processing BackupJob", "backupjob", j.Name, "strategyKind", j.Spec.StrategyRef.Kind)
	switch j.Spec.StrategyRef.Kind {
	case strategyv1alpha1.JobStrategyKind:
		return r.reconcileJob(ctx, j)
	case strategyv1alpha1.VeleroStrategyKind:
		return r.reconcileVelero(ctx, j)
	default:
		logger.V(1).Info("BackupJob StrategyRef.Kind not supported, skipping",
			"backupjob", j.Name,
			"kind", j.Spec.StrategyRef.Kind,
			"supported", []string{strategyv1alpha1.JobStrategyKind, strategyv1alpha1.VeleroStrategyKind})
		return ctrl.Result{}, nil
	}
}

// SetupWithManager registers our controller with the Manager and sets up watches.
func (r *BackupJobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&backupsv1alpha1.BackupJob{}).
		Complete(r)
}
