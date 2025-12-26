package backupcontroller

import (
	"context"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
)

func (r *BackupJobReconciler) reconcileVelero(ctx context.Context, j *backupsv1alpha1.BackupJob) (ctrl.Result, error) {
	_ = log.FromContext(ctx)
	return ctrl.Result{}, nil
}
