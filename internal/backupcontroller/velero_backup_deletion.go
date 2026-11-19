package backupcontroller

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"

	velerov1 "github.com/vmware-tanzu/velero/pkg/apis/velero/v1"
)

// requestVeleroBackupDeletion asks Velero to delete a backup together with its
// data in the object store. Deleting the backup.velero.io object directly only
// removes the Kubernetes object, and the BSL sync recreates it from the object
// store.
//
// The request is named after the backup, so repeated calls converge on one
// request. Nothing is created while any request for the backup is unprocessed,
// or once one was processed without errors (Velero then removes the backup and
// the remaining requests). A request of ours that Velero processed with errors
// is replaced: Velero keeps it for a day, and until then its name would block
// the retry.
func requestVeleroBackupDeletion(ctx context.Context, c client.Client, namespace, name string) (bool, error) {
	requests := &velerov1.DeleteBackupRequestList{}
	if err := c.List(ctx, requests, client.InNamespace(namespace)); err != nil {
		return false, fmt.Errorf("failed to list DeleteBackupRequests in %s: %w", namespace, err)
	}
	var failed *velerov1.DeleteBackupRequest
	for i := range requests.Items {
		dbr := &requests.Items[i]
		if dbr.Spec.BackupName != name {
			continue
		}
		if dbr.Status.Phase != velerov1.DeleteBackupRequestPhaseProcessed || len(dbr.Status.Errors) == 0 {
			return false, nil
		}
		if dbr.Name == name {
			failed = dbr
		}
	}
	if failed != nil {
		// The UID precondition keeps a stale cache from deleting a request
		// that has already been replaced.
		err := c.Delete(ctx, failed, client.Preconditions{UID: &failed.UID})
		if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
			return false, fmt.Errorf("failed to delete failed DeleteBackupRequest %s/%s: %w", namespace, name, err)
		}
	}

	dbr := &velerov1.DeleteBackupRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: velerov1.DeleteBackupRequestSpec{
			BackupName: name,
		},
	}
	if err := c.Create(ctx, dbr); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to create DeleteBackupRequest for %s/%s: %w", namespace, name, err)
	}
	return true, nil
}

// deleteFailedJobVeleroBackups requests deletion of the Velero backups a Failed
// BackupJob left behind. A failed run creates no Backup, and the Backup
// finalizer is the only other path that deletes Velero data, so without this
// the backup and its objects stay in the bucket until Velero's TTL expires
// them. It runs on every reconcile of a Failed job, which also clears backups
// left by controller versions that did not request the deletion.
func (r *BackupJobReconciler) deleteFailedJobVeleroBackups(ctx context.Context, j *backupsv1alpha1.BackupJob) (ctrl.Result, error) {
	logger := getLogger(ctx)

	veleroBackups := &velerov1.BackupList{}
	if err := r.List(ctx, veleroBackups, client.InNamespace(veleroNamespace), client.MatchingLabels{
		backupsv1alpha1.OwningJobNamespaceLabel: j.Namespace,
		backupsv1alpha1.OwningJobNameLabel:      j.Name,
	}); err != nil {
		if meta.IsNoMatchError(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to list Velero Backups of BackupJob %s/%s: %w", j.Namespace, j.Name, err)
	}
	if len(veleroBackups.Items) == 0 {
		return ctrl.Result{}, nil
	}

	// A job can fail after its Backup was created: the status update that
	// records the Backup is lost, and the retried create hits AlreadyExists.
	// That Backup owns the Velero backup and deletes it itself.
	referenced, err := r.veleroBackupsReferencedByBackups(ctx, j.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}

	var running bool
	for i := range veleroBackups.Items {
		vb := &veleroBackups.Items[i]
		if referenced[client.ObjectKeyFromObject(vb)] {
			continue
		}
		switch vb.Status.Phase {
		case velerov1.BackupPhaseCompleted, velerov1.BackupPhasePartiallyFailed,
			velerov1.BackupPhaseFailed, velerov1.BackupPhaseFailedValidation:
		case velerov1.BackupPhaseDeleting:
			continue
		default:
			// Velero rejects a request for a backup that is still running.
			running = true
			continue
		}
		created, err := requestVeleroBackupDeletion(ctx, r.Client, vb.Namespace, vb.Name)
		if err != nil {
			return ctrl.Result{}, err
		}
		if created {
			logger.Info("requested deletion of the Velero Backup of a failed BackupJob",
				"backupjob", j.Name, "namespace", j.Namespace, "veleroBackup", vb.Name)
		}
	}
	if running {
		return ctrl.Result{RequeueAfter: defaultActiveJobPollingInterval}, nil
	}
	return ctrl.Result{}, nil
}

func (r *BackupJobReconciler) veleroBackupsReferencedByBackups(ctx context.Context, namespace string) (map[client.ObjectKey]bool, error) {
	backups := &backupsv1alpha1.BackupList{}
	if err := r.List(ctx, backups, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("failed to list Backups in %s: %w", namespace, err)
	}
	referenced := map[client.ObjectKey]bool{}
	for _, b := range backups.Items {
		name := b.Spec.DriverMetadata[veleroBackupNameMetadataKey]
		if name == "" {
			continue
		}
		ns := b.Spec.DriverMetadata[veleroBackupNamespaceMetadataKey]
		if ns == "" {
			ns = veleroNamespace
		}
		referenced[client.ObjectKey{Namespace: ns, Name: name}] = true
	}
	return referenced, nil
}
