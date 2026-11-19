package backupcontroller

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"

	velerov1 "github.com/vmware-tanzu/velero/pkg/apis/velero/v1"
)

// veleroDeletionRequest is what requestVeleroBackupDeletion did.
type veleroDeletionRequest struct {
	created bool
	// replaced is a request of ours that Velero had processed and that this
	// call deleted before creating a new one.
	replaced *velerov1.DeleteBackupRequest
}

// requestVeleroBackupDeletion asks Velero to delete a backup together with its
// data in the object store. Deleting the backup.velero.io object directly only
// removes the Kubernetes object, and the BSL sync recreates it from the object
// store. Callers call it only for a backup that still exists.
//
// The request is named after the backup, so repeated calls converge on one
// request. Nothing is created while any request for the backup is
// unprocessed. A request of ours that Velero has processed is deleted and
// created again: the backup still exists, so the deletion failed, or the BSL
// sync brought the backup back, and Velero keeps a processed request for a
// day, during which its name would block a new attempt. Velero does not retry
// a processed request, so a caller that needs the deletion to happen must
// call again later.
func requestVeleroBackupDeletion(ctx context.Context, c client.Client, namespace, name string) (veleroDeletionRequest, error) {
	var res veleroDeletionRequest
	requests := &velerov1.DeleteBackupRequestList{}
	if err := c.List(ctx, requests, client.InNamespace(namespace)); err != nil {
		return res, fmt.Errorf("failed to list DeleteBackupRequests in %s: %w", namespace, err)
	}
	var own *velerov1.DeleteBackupRequest
	for i := range requests.Items {
		dbr := &requests.Items[i]
		if dbr.Spec.BackupName != name {
			continue
		}
		if dbr.Status.Phase != velerov1.DeleteBackupRequestPhaseProcessed {
			return res, nil
		}
		if dbr.Name == name {
			own = dbr
		}
	}
	if own != nil {
		// The UID precondition keeps a stale cache from deleting a request
		// that has already been replaced.
		err := c.Delete(ctx, own, client.Preconditions{UID: &own.UID})
		switch {
		case err == nil:
			res.replaced = own
		case apierrors.IsNotFound(err), apierrors.IsConflict(err):
		default:
			return res, fmt.Errorf("failed to delete processed DeleteBackupRequest %s/%s: %w", namespace, name, err)
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
			return res, nil
		}
		return res, fmt.Errorf("failed to create DeleteBackupRequest for %s/%s: %w", namespace, name, err)
	}
	res.created = true
	return res, nil
}

const (
	// failedJobSweepRequeue is how often a Failed BackupJob is reconciled
	// again while one of its Velero backups still waits for deletion. Nothing
	// watches DeleteBackupRequests, so a request Velero processed is only
	// noticed, and replaced, on this requeue. Every replacement doubles it, up
	// to failedJobSweepMaxRequeue, so a deletion Velero can never carry out
	// (a missing, read-only or unavailable BSL) is retried rarely.
	failedJobSweepRequeue    = 5 * time.Minute
	failedJobSweepMaxRequeue = time.Hour
	// failedJobRunningBackupRequeue is how often a Failed BackupJob is
	// reconciled again while one of its Velero backups is still running.
	// Velero rejects a deletion request for a running backup.
	failedJobRunningBackupRequeue = time.Minute

	// veleroDeletionRetriesAnnotation counts, on a Failed BackupJob, the
	// deletion requests that Velero processed while the backup survived.
	veleroDeletionRetriesAnnotation = "backups.cozystack.io/velero-deletion-retries"
)

// deleteFailedJobVeleroBackups requests deletion of the failed Velero backups
// a Failed BackupJob left behind. A failed run creates no Backup, and the
// Backup finalizer is the only other path that deletes Velero data, so without
// this the backup and its objects stay in the bucket until Velero's TTL
// expires them. It runs on every reconcile of a Failed job, which also clears
// backups left by controller versions that did not request the deletion, and
// it requests the deletion only once the Failed phase is persisted: a request
// sent before that write would let a retried run start over with the failure
// already gone.
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

	var referenced map[client.ObjectKey]bool
	var requeueAfter time.Duration
	var waiting, replaced bool
	for i := range veleroBackups.Items {
		vb := &veleroBackups.Items[i]
		switch vb.Status.Phase {
		case velerov1.BackupPhasePartiallyFailed, velerov1.BackupPhaseFailed, velerov1.BackupPhaseFailedValidation,
			// Velero marks the backup Deleting before it removes the data and
			// leaves it so when that fails, so the request may need replacing.
			velerov1.BackupPhaseDeleting:
		case velerov1.BackupPhaseCompleted:
			// A job can fail after Velero completed, when creating or
			// recording its Backup fails. That Velero backup may be the only
			// copy of good data, so a job-level failure never removes it: a
			// Backup that references it deletes it with the Backup, and one
			// that no Backup references stays until Velero's TTL expires it.
			continue
		default:
			requeueAfter = shorterRequeue(requeueAfter, failedJobRunningBackupRequeue)
			continue
		}

		if referenced == nil {
			var err error
			if referenced, err = r.veleroBackupsReferencedByBackups(ctx, j.Namespace); err != nil {
				return ctrl.Result{}, err
			}
		}
		if referenced[client.ObjectKeyFromObject(vb)] {
			continue
		}

		req, err := requestVeleroBackupDeletion(ctx, r.Client, vb.Namespace, vb.Name)
		if err != nil {
			return ctrl.Result{}, err
		}
		waiting = true
		if req.replaced != nil {
			replaced = true
			r.warnVeleroDeletionRetried(j, vb.Name, req.replaced)
		} else if req.created {
			logger.Info("requested deletion of the Velero Backup of a failed BackupJob",
				"backupjob", j.Name, "namespace", j.Namespace, "veleroBackup", vb.Name)
		}
	}
	if !waiting {
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}

	retries := veleroDeletionRetries(j)
	if replaced {
		retries++
		patch := client.MergeFrom(j.DeepCopy())
		if j.Annotations == nil {
			j.Annotations = map[string]string{}
		}
		j.Annotations[veleroDeletionRetriesAnnotation] = strconv.Itoa(retries)
		if err := r.Patch(ctx, j, patch); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to record Velero deletion retries on BackupJob %s/%s: %w", j.Namespace, j.Name, err)
		}
	}
	return ctrl.Result{RequeueAfter: shorterRequeue(requeueAfter, failedJobSweepBackoff(retries))}, nil
}

func (r *BackupJobReconciler) warnVeleroDeletionRetried(j *backupsv1alpha1.BackupJob, veleroBackup string, processed *velerov1.DeleteBackupRequest) {
	if r.Recorder == nil {
		return
	}
	reason := "Velero reported no error, but the backup still exists"
	if len(processed.Status.Errors) > 0 {
		reason = strings.Join(processed.Status.Errors, "; ")
	}
	r.Recorder.Eventf(j, corev1.EventTypeWarning, "VeleroBackupDeletionFailed",
		"Velero did not delete backup %s/%s: %s; requesting the deletion again",
		veleroNamespace, veleroBackup, reason)
}

func veleroDeletionRetries(j *backupsv1alpha1.BackupJob) int {
	n, err := strconv.Atoi(j.Annotations[veleroDeletionRetriesAnnotation])
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func failedJobSweepBackoff(retries int) time.Duration {
	d := failedJobSweepRequeue
	for i := 0; i < retries && d < failedJobSweepMaxRequeue; i++ {
		d *= 2
	}
	return min(d, failedJobSweepMaxRequeue)
}

func shorterRequeue(current, next time.Duration) time.Duration {
	if current == 0 || next < current {
		return next
	}
	return current
}

// veleroBackupsReferencedByBackups reads Backups past the cache: a Backup the
// cache has not seen yet must still protect its Velero backup from deletion.
func (r *BackupJobReconciler) veleroBackupsReferencedByBackups(ctx context.Context, namespace string) (map[client.ObjectKey]bool, error) {
	backups := &backupsv1alpha1.BackupList{}
	if err := r.apiReader().List(ctx, backups, client.InNamespace(namespace)); err != nil {
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
