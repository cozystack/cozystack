// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
)

var (
	// errJobFinished: the apiserver already holds the job as finished.
	// Whoever finished it wrote the verdict, so the pass ends without
	// touching the job's status.
	errJobFinished = errors.New("job already finished on the apiserver")
	// errJobRecheck: the re-read itself failed. The pass is retried rather
	// than the job failed, since the job's state is exactly what is unknown.
	errJobRecheck = errors.New("re-read job")
)

// ensureJobNotFinished re-reads job through reader, past the informer cache,
// and returns errJobFinished when it is Succeeded, Failed, being deleted or
// gone.
//
// The phase every driver checks comes from the cache at the top of the pass,
// so a job another actor marked Failed meanwhile would still receive its
// Velero Backup, operator dump or restore — an object nothing tracks once
// the job is terminal, because the reconcilers skip terminal jobs. Call this
// right before creating a driver object, and before any destructive step a
// restore takes on the application.
//
// It narrows the window, it does not close it: the job can still turn
// terminal between this Get and the Create that follows, since no apiserver
// precondition ties the creation of one object to the state of another.
func ensureJobNotFinished(ctx context.Context, reader client.Reader, job client.Object) error {
	var fresh client.Object
	switch job.(type) {
	case *backupsv1alpha1.BackupJob:
		fresh = &backupsv1alpha1.BackupJob{}
	case *backupsv1alpha1.RestoreJob:
		fresh = &backupsv1alpha1.RestoreJob{}
	default:
		return fmt.Errorf("%w: unsupported job type %T", errJobRecheck, job)
	}
	if err := reader.Get(ctx, client.ObjectKeyFromObject(job), fresh); err != nil {
		if apierrors.IsNotFound(err) {
			return logJobFinished(ctx, job, "deleted")
		}
		return fmt.Errorf("%w %s/%s: %w", errJobRecheck, job.GetNamespace(), job.GetName(), err)
	}
	if !fresh.GetDeletionTimestamp().IsZero() {
		return logJobFinished(ctx, job, "being deleted")
	}
	switch f := fresh.(type) {
	case *backupsv1alpha1.BackupJob:
		if f.Status.Phase == backupsv1alpha1.BackupJobPhaseSucceeded || f.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
			return logJobFinished(ctx, job, string(f.Status.Phase))
		}
	case *backupsv1alpha1.RestoreJob:
		if f.Status.Phase == backupsv1alpha1.RestoreJobPhaseSucceeded || f.Status.Phase == backupsv1alpha1.RestoreJobPhaseFailed {
			return logJobFinished(ctx, job, string(f.Status.Phase))
		}
	}
	return nil
}

func logJobFinished(ctx context.Context, job client.Object, state string) error {
	getLogger(ctx).Info("job finished before its driver object was created, creating nothing",
		"namespace", job.GetNamespace(), "name", job.GetName(), "state", state)
	return errJobFinished
}

// stoppedByJobGuard tells a caller whether err, possibly returned through a
// driver's create helper, came from ensureJobNotFinished, and if so what the
// pass returns. Any other error is left to the caller's own handling, which
// usually fails the job.
func stoppedByJobGuard(err error) (bool, ctrl.Result, error) {
	switch {
	case errors.Is(err, errJobFinished):
		return true, ctrl.Result{}, nil
	case errors.Is(err, errJobRecheck):
		return true, ctrl.Result{}, err
	}
	return false, ctrl.Result{}, nil
}

// apiReader returns the uncached reader the guard needs. Reconcilers built
// by hand in unit tests have none and fall back to their fake Client, which
// has no cache to bypass.
func (r *BackupJobReconciler) apiReader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

func (r *RestoreJobReconciler) apiReader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}
