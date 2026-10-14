// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	"github.com/cozystack/cozystack/internal/backupcontroller/foundationdbapp"
	"github.com/cozystack/cozystack/internal/backupcontroller/foundationdbtypes"
)

const (
	// foundationdbOwnsDirectoryKey marks a Backup whose blob-store directory
	// the driver named itself, one per BackupJob, so deleting the Backup may
	// delete the directory. A strategy that pins backupName shares one
	// directory between runs, and those Backups leave it alone.
	foundationdbOwnsDirectoryKey = "apps.foundationdb.org/owns-directory"
	// foundationdbAgentVersionKey records the FoundationDB version the
	// backup_agent ran, which the delete Job reuses so it can reach the same
	// endpoint (an SNI-routed one needs an SNI-capable client).
	foundationdbAgentVersionKey = "apps.foundationdb.org/agent-version"

	foundationdbSkipArtifactCleanupAnnotation = "backups.cozystack.io/skip-artifact-cleanup"
	foundationdbCleanupLabel                  = "backups.cozystack.io/foundationdb-cleanup"
	// The operator's default image for the split image type; it ships bash
	// and fdbbackup.
	foundationdbCleanupBaseImage = "foundationdb/foundationdb"

	// fdbbackup exits non-zero for a directory that is already gone; a
	// retry after a partial delete must still count as success.
	foundationdbCleanupScript = `out=$(fdbbackup delete -d "$FDB_BACKUP_URL" 2>&1); rc=$?
printf '%s\n' "$out"
if [ "$rc" -ne 0 ] && printf '%s' "$out" | grep -q 'Backup does not exist'; then exit 0; fi
exit "$rc"`
)

// cleanupFoundationDBBackup deletes the blob-store directory of a Backup the
// driver owns. The operator-side FoundationDBBackup is stopped and removed
// first, so its agents stop writing and a Stopped CR is not left to
// discontinue a later backup of the same cluster (the operator reads backup
// state for the whole cluster). The directory itself goes through a Job
// running `fdbbackup delete` with the strategy's pod template, which carries
// the credentials and CA the agents used.
func (r *BackupReconciler) cleanupFoundationDBBackup(ctx context.Context, backup *backupsv1alpha1.Backup) (ctrl.Result, error) {
	md := backup.Spec.DriverMetadata
	if md[foundationdbOwnsDirectoryKey] != "true" {
		return ctrl.Result{}, nil
	}
	url := md[foundationdbStorageURIKey]
	jobName := foundationdbCleanupJobName(backup.Name)

	if backup.Annotations[foundationdbSkipArtifactCleanupAnnotation] == "true" {
		existing := &batchv1.Job{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: backup.Namespace, Name: jobName}, existing); err == nil {
			_ = r.deleteFoundationDBCleanupJob(ctx, existing)
		}
		// The data stays, but the agents must not keep writing into a
		// directory nothing owns any more. Best-effort and without waiting
		// on the operator: the annotation is the way out of a stuck delete.
		r.dropFoundationDBOperatorBackup(ctx, backup)
		getLogger(ctx).Debug("skipping FoundationDB artifact cleanup per annotation; directory left in bucket", "backup", backup.Name, "url", url)
		return ctrl.Result{}, nil
	}

	// A restore may still be reading the directory.
	if restoring, err := r.foundationdbBackupBeingRestored(ctx, backup); err != nil || restoring {
		return ctrl.Result{RequeueAfter: foundationdbPollInterval}, err
	}

	if done, err := r.removeFoundationDBOperatorBackup(ctx, backup); err != nil || !done {
		return ctrl.Result{RequeueAfter: foundationdbPollInterval}, err
	}

	if !strings.HasPrefix(url, "blobstore://") {
		return r.releaseFoundationDBCleanup(ctx, backup, url, "no blob-store URL recorded"), nil
	}

	job := &batchv1.Job{}
	err := r.Get(ctx, types.NamespacedName{Namespace: backup.Namespace, Name: jobName}, job)
	switch {
	case apierrors.IsNotFound(err):
		return r.startFoundationDBCleanupJob(ctx, backup, jobName, url)
	case err != nil:
		return ctrl.Result{}, err
	}

	if !job.DeletionTimestamp.IsZero() {
		return ctrl.Result{RequeueAfter: foundationdbPollInterval}, nil
	}
	switch jobConditionState(job) {
	case batchv1.JobComplete:
		_ = r.deleteFoundationDBCleanupJob(ctx, job)
		getLogger(ctx).Debug("FoundationDB backup directory deleted", "backup", backup.Name, "url", url)
		return ctrl.Result{}, nil
	case batchv1.JobFailed:
		// Collect the failed Job so the next pass starts a fresh one; the
		// Backup stays Terminating, a visible signal, until the directory is
		// gone or the skip annotation releases it.
		if r.Recorder != nil {
			r.Recorder.Eventf(backup, corev1.EventTypeWarning, "ArtifactDeleteFailed",
				"deleting FoundationDB backup %s failed (see Job %s/%s); retrying. Annotate the Backup %s=true to release it and leave the data",
				url, job.Namespace, job.Name, foundationdbSkipArtifactCleanupAnnotation)
		}
		_ = r.deleteFoundationDBCleanupJob(ctx, job)
		return ctrl.Result{RequeueAfter: foundationdbPollInterval}, nil
	default:
		return ctrl.Result{RequeueAfter: foundationdbPollInterval}, nil
	}
}

// removeFoundationDBOperatorBackup stops the Backup's operator-side
// FoundationDBBackup, waits for the operator to act on it, then deletes it.
// It reports true once the CR is gone.
func (r *BackupReconciler) removeFoundationDBOperatorBackup(ctx context.Context, backup *backupsv1alpha1.Backup) (bool, error) {
	md := backup.Spec.DriverMetadata
	name := md[foundationdbBackupNameKey]
	if name == "" {
		return true, nil
	}
	namespace := md[foundationdbBackupNamespaceKey]
	if namespace == "" {
		namespace = backup.Namespace
	}
	fdbBackup := &foundationdbtypes.FoundationDBBackup{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, fdbBackup); err != nil {
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}
	if !fdbBackup.DeletionTimestamp.IsZero() {
		return false, nil
	}
	if fdbBackup.Spec.BackupState != foundationdbtypes.BackupStateStopped {
		base := fdbBackup.DeepCopy()
		fdbBackup.Spec.BackupState = foundationdbtypes.BackupStateStopped
		return false, r.Patch(ctx, fdbBackup, client.MergeFrom(base))
	}
	if fdbBackup.Status.Generations != nil && fdbBackup.Status.Generations.Reconciled < fdbBackup.Generation {
		// The operator acts on the stop only while the cluster exists. With
		// the cluster gone there is nothing to stop, so the CR goes now.
		err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: fdbBackup.Spec.ClusterName}, &foundationdbtypes.FoundationDBCluster{})
		if err == nil || !apierrors.IsNotFound(err) {
			return false, err
		}
	}
	return false, client.IgnoreNotFound(r.Delete(ctx, fdbBackup))
}

// dropFoundationDBOperatorBackup stops and deletes the Backup's operator-side
// FoundationDBBackup without waiting for the operator. Errors are logged.
func (r *BackupReconciler) dropFoundationDBOperatorBackup(ctx context.Context, backup *backupsv1alpha1.Backup) {
	md := backup.Spec.DriverMetadata
	name := md[foundationdbBackupNameKey]
	if name == "" {
		return
	}
	namespace := md[foundationdbBackupNamespaceKey]
	if namespace == "" {
		namespace = backup.Namespace
	}
	fdbBackup := &foundationdbtypes.FoundationDBBackup{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, fdbBackup); err != nil {
		return
	}
	if fdbBackup.Spec.BackupState != foundationdbtypes.BackupStateStopped {
		base := fdbBackup.DeepCopy()
		fdbBackup.Spec.BackupState = foundationdbtypes.BackupStateStopped
		if err := r.Patch(ctx, fdbBackup, client.MergeFrom(base)); err != nil {
			getLogger(ctx).Debug("failed to stop FoundationDBBackup on skipped cleanup", "fdbBackup", name, "error", err.Error())
		}
	}
	if err := r.Delete(ctx, fdbBackup); err != nil && !apierrors.IsNotFound(err) {
		getLogger(ctx).Debug("failed to delete FoundationDBBackup on skipped cleanup", "fdbBackup", name, "error", err.Error())
	}
}

// foundationdbBackupBeingRestored reports whether a RestoreJob that reads this
// Backup has not finished yet.
func (r *BackupReconciler) foundationdbBackupBeingRestored(ctx context.Context, backup *backupsv1alpha1.Backup) (bool, error) {
	list := &backupsv1alpha1.RestoreJobList{}
	if err := r.List(ctx, list, client.InNamespace(backup.Namespace)); err != nil {
		return false, err
	}
	for i := range list.Items {
		rj := &list.Items[i]
		if rj.Spec.BackupRef.Name != backup.Name {
			continue
		}
		if rj.Status.Phase != backupsv1alpha1.RestoreJobPhaseSucceeded && rj.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
			return true, nil
		}
	}
	return false, nil
}

// foundationdbCleanupJobName keeps the Job name within the 63 characters a
// Job's pod label allows: a long Backup name is cut and made unique again
// with a hash of the full name.
func foundationdbCleanupJobName(backupName string) string {
	const suffix = "-cleanup"
	if len(backupName)+len(suffix) <= 63 {
		return backupName + suffix
	}
	sum := sha256.Sum256([]byte(backupName))
	hash := hex.EncodeToString(sum[:])[:8]
	return backupName[:63-len(suffix)-len(hash)-1] + "-" + hash + suffix
}

func (r *BackupReconciler) startFoundationDBCleanupJob(ctx context.Context, backup *backupsv1alpha1.Backup, jobName, url string) (ctrl.Result, error) {
	// Creating the Job and projecting its credentials are CREATEs, which a
	// Terminating namespace refuses; the directory cannot be reached from
	// there, so the Backup is released.
	terminating, err := r.namespaceTerminating(ctx, backup.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	if terminating {
		return r.releaseFoundationDBCleanup(ctx, backup, url, "namespace is terminating"), nil
	}
	version := backup.Spec.DriverMetadata[foundationdbAgentVersionKey]
	if version == "" {
		return r.releaseFoundationDBCleanup(ctx, backup, url, "no agent version recorded"), nil
	}

	strategy := &strategyv1alpha1.FoundationDB{}
	if err := r.Get(ctx, client.ObjectKey{Name: backup.Spec.StrategyRef.Name}, strategy); err != nil {
		if apierrors.IsNotFound(err) {
			return r.releaseFoundationDBCleanup(ctx, backup, url, "strategy CR is gone"), nil
		}
		return ctrl.Result{}, err
	}
	// The source application may already be deleted; the pod template only
	// needs its name and namespace to resolve per-application Secret names.
	app := &foundationdbapp.FoundationDB{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: backup.Namespace, Name: backup.Spec.ApplicationRef.Name}, app); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		app = &foundationdbapp.FoundationDB{ObjectMeta: metav1.ObjectMeta{Namespace: backup.Namespace, Name: backup.Spec.ApplicationRef.Name}}
	}
	// Render with the parameters the backup ran with, so a BackupClass
	// parameter that feeds the pod template yields the same credentials.
	var parameters map[string]string
	if snap, err := decodeFoundationDBBackupSnapshot(backup.Status.UnderlyingResources); err == nil && snap != nil {
		parameters = snap.Parameters
	}
	rendered, err := renderFoundationDBTemplate(strategy.Spec.Template, app, parameters)
	if err != nil {
		return r.releaseFoundationDBCleanup(ctx, backup, url, fmt.Sprintf("strategy render failed: %v", err)), nil
	}
	if perr := ProjectBackupCredentials(ctx, r.Client, r.CredentialsConfig, backup.Namespace); perr != nil {
		if IsTransient(perr) {
			return ctrl.Result{}, perr
		}
		return r.releaseFoundationDBCleanup(ctx, backup, url, fmt.Sprintf("cannot project credentials: %v", perr)), nil
	}

	job := buildFoundationDBCleanupJob(backup, jobName, url, foundationdbCleanupBaseImage+":"+version, rendered.BackupDeploymentPodTemplateSpec)
	if err := r.Create(ctx, job); err != nil && !apierrors.IsAlreadyExists(err) {
		// Forbidden (namespace went Terminating after the check) or Invalid
		// cannot be fixed by retrying.
		if apierrors.IsForbidden(err) || apierrors.IsInvalid(err) {
			return r.releaseFoundationDBCleanup(ctx, backup, url, fmt.Sprintf("cannot create cleanup Job: %v", err)), nil
		}
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: foundationdbPollInterval}, nil
}

// buildFoundationDBCleanupJob runs `fdbbackup delete` in the strategy's agent
// pod template: its volumes and environment carry the credentials file and CA
// bundle, and only the foundationdb container's image and command change.
func buildFoundationDBCleanupJob(backup *backupsv1alpha1.Backup, name, url, image string, tmpl *corev1.PodTemplateSpec) *batchv1.Job {
	pod := corev1.PodTemplateSpec{}
	if tmpl != nil {
		pod = *tmpl.DeepCopy()
	}
	idx := -1
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == "foundationdb" {
			idx = i
		}
	}
	if idx < 0 {
		pod.Spec.Containers = append(pod.Spec.Containers, corev1.Container{Name: "foundationdb"})
		idx = len(pod.Spec.Containers) - 1
	}
	c := &pod.Spec.Containers[idx]
	if c.Image == "" {
		c.Image = image
	}
	c.Command = []string{"/bin/bash", "-c", foundationdbCleanupScript}
	c.Args = nil
	c.Env = append(c.Env, corev1.EnvVar{Name: "FDB_BACKUP_URL", Value: url})
	pod.Spec.RestartPolicy = corev1.RestartPolicyNever
	if pod.Labels == nil {
		pod.Labels = map[string]string{}
	}
	pod.Labels[foundationdbCleanupLabel] = backup.Name

	backoffLimit := int32(1)
	activeDeadline := int64(600)
	ttl := int32(300)
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: backup.Namespace,
			Name:      name,
			Labels:    map[string]string{foundationdbCleanupLabel: backup.Name},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoffLimit,
			ActiveDeadlineSeconds:   &activeDeadline,
			TTLSecondsAfterFinished: &ttl,
			Template:                pod,
		},
	}
}

func (r *BackupReconciler) deleteFoundationDBCleanupJob(ctx context.Context, job *batchv1.Job) error {
	policy := metav1.DeletePropagationBackground
	return client.IgnoreNotFound(r.Delete(ctx, job, &client.DeleteOptions{PropagationPolicy: &policy}))
}

func (r *BackupReconciler) releaseFoundationDBCleanup(ctx context.Context, backup *backupsv1alpha1.Backup, url, reason string) ctrl.Result {
	getLogger(ctx).Info("releasing Backup without deleting its directory", "backup", backup.Name, "url", url, "reason", reason)
	if r.Recorder != nil {
		r.Recorder.Eventf(backup, corev1.EventTypeWarning, "ArtifactNotDeleted", "left FoundationDB backup %s in the bucket: %s", url, reason)
	}
	return ctrl.Result{}
}
