package backupcontroller

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	"github.com/cozystack/cozystack/pkg/backups"
)

const (
	// DriverInstallGrace is how long a job may wait for the API of its
	// strategy to appear before it is failed. A Plan can fire while the
	// package that ships the strategy is still installing, and a HelmRelease
	// retries on a ten-minute interval, so the grace spans a few of those.
	DriverInstallGrace = 30 * time.Minute

	dispatchRequeue = 30 * time.Second

	conditionReady = "Ready"

	reasonBackupClassNotFound = "BackupClassNotFound"
	reasonDriverNotInstalled  = "DriverNotInstalled"
	reasonBackupFailed        = "BackupFailed"
	reasonRestoreFailed       = "RestoreFailed"
)

// The dispatchers below settle the jobs that no strategy driver can: a
// driver acts only on jobs whose strategy is of its own group and kind, so a
// job that resolves to no strategy, or to one whose API nothing serves, would
// otherwise wait forever with an empty status. They never run a job and never
// touch one a driver has started.

// BackupJobDispatcher fails the BackupJobs that have no driver to go to.
type BackupJobDispatcher struct {
	client.Client
	RESTMapper meta.RESTMapper

	now func() time.Time
}

func (r *BackupJobDispatcher) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	j := &backupsv1alpha1.BackupJob{}
	if err := r.Get(ctx, req.NamespacedName, j); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if j.Status.StartedAt != nil || j.Status.CompletedAt != nil ||
		j.Status.Phase == backupsv1alpha1.BackupJobPhaseSucceeded || j.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
		return ctrl.Result{}, nil
	}

	fail := func(message string) (ctrl.Result, error) {
		completed := metav1.NewTime(clock(r.now))
		j.Status.CompletedAt = &completed
		j.Status.Phase = backupsv1alpha1.BackupJobPhaseFailed
		j.Status.Message = message
		meta.SetStatusCondition(&j.Status.Conditions, metav1.Condition{
			Type: conditionReady, Status: metav1.ConditionFalse, Reason: reasonBackupFailed, Message: message,
		})
		return ctrl.Result{}, r.Status().Update(ctx, j)
	}
	wait := func(reason, message string) (ctrl.Result, error) {
		if meta.SetStatusCondition(&j.Status.Conditions, metav1.Condition{
			Type: conditionReady, Status: metav1.ConditionFalse, Reason: reason, Message: message,
		}) {
			if err := r.Status().Update(ctx, j); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{RequeueAfter: dispatchRequeue}, nil
	}

	resolved, err := backups.ResolveBackupClass(ctx, r.Client, j.Spec.BackupClassName, j.Spec.ApplicationRef)
	switch {
	case errors.Is(err, backups.ErrNoMatchingStrategy):
		kind := backupsv1alpha1.NormalizeApplicationRef(j.Spec.ApplicationRef).Kind
		return fail(fmt.Sprintf("BackupClass %q does not bind a strategy for Kind %q; see https://cozystack.io/docs/v1/operations/services/backup-classes/", j.Spec.BackupClassName, kind))
	case apierrors.IsNotFound(err):
		return wait(reasonBackupClassNotFound, fmt.Sprintf("BackupClass %q not found; the platform may still be bootstrapping", j.Spec.BackupClassName))
	case err != nil:
		return ctrl.Result{}, err
	}

	served, err := strategyServed(r.RESTMapper, resolved.StrategyRef)
	if err != nil {
		return ctrl.Result{}, err
	}
	if served {
		return ctrl.Result{}, nil
	}
	message := driverNotInstalledMessage(resolved.StrategyRef)
	if clock(r.now).Sub(j.CreationTimestamp.Time) > DriverInstallGrace {
		logger.Info("failing BackupJob with no driver", "strategy", resolved.StrategyRef)
		return fail(message)
	}
	return wait(reasonDriverNotInstalled, message)
}

func (r *BackupJobDispatcher) SetupWithManager(mgr ctrl.Manager) error {
	r.RESTMapper = mgr.GetRESTMapper()
	return ctrl.NewControllerManagedBy(mgr).
		Named("backupjob-dispatch").
		For(&backupsv1alpha1.BackupJob{}).
		Complete(r)
}

// RestoreJobDispatcher fails the RestoreJobs that have no driver to go to.
type RestoreJobDispatcher struct {
	client.Client
	RESTMapper meta.RESTMapper

	now func() time.Time
}

func (r *RestoreJobDispatcher) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	rj := &backupsv1alpha1.RestoreJob{}
	if err := r.Get(ctx, req.NamespacedName, rj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !rj.DeletionTimestamp.IsZero() || rj.Status.StartedAt != nil || rj.Status.CompletedAt != nil ||
		rj.Status.Phase == backupsv1alpha1.RestoreJobPhaseSucceeded || rj.Status.Phase == backupsv1alpha1.RestoreJobPhaseFailed {
		return ctrl.Result{}, nil
	}

	fail := func(message string) (ctrl.Result, error) {
		completed := metav1.NewTime(clock(r.now))
		rj.Status.CompletedAt = &completed
		rj.Status.Phase = backupsv1alpha1.RestoreJobPhaseFailed
		rj.Status.Message = message
		meta.SetStatusCondition(&rj.Status.Conditions, metav1.Condition{
			Type: conditionReady, Status: metav1.ConditionFalse, Reason: reasonRestoreFailed, Message: message,
		})
		return ctrl.Result{}, r.Status().Update(ctx, rj)
	}

	// Only the Backup says which driver a restore belongs to, so without it
	// no driver can claim the job.
	backup := &backupsv1alpha1.Backup{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: rj.Namespace, Name: rj.Spec.BackupRef.Name}, backup); err != nil {
		if apierrors.IsNotFound(err) {
			return fail(fmt.Sprintf("failed to get Backup: %v", err))
		}
		return ctrl.Result{}, err
	}

	served, err := strategyServed(r.RESTMapper, backup.Spec.StrategyRef)
	if err != nil {
		return ctrl.Result{}, err
	}
	if served {
		return ctrl.Result{}, nil
	}
	message := driverNotInstalledMessage(backup.Spec.StrategyRef)
	if clock(r.now).Sub(rj.CreationTimestamp.Time) > DriverInstallGrace {
		logger.Info("failing RestoreJob with no driver", "strategy", backup.Spec.StrategyRef)
		return fail(message)
	}
	if meta.SetStatusCondition(&rj.Status.Conditions, metav1.Condition{
		Type: conditionReady, Status: metav1.ConditionFalse, Reason: reasonDriverNotInstalled, Message: message,
	}) {
		if err := r.Status().Update(ctx, rj); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: dispatchRequeue}, nil
}

func (r *RestoreJobDispatcher) SetupWithManager(mgr ctrl.Manager) error {
	r.RESTMapper = mgr.GetRESTMapper()
	return ctrl.NewControllerManagedBy(mgr).
		Named("restorejob-dispatch").
		For(&backupsv1alpha1.RestoreJob{}).
		Complete(r)
}

// strategyServed reports whether the API server serves the kind a strategy
// reference names. A driver ships the CRD of its strategy, so an unserved
// kind means the driver is not installed.
func strategyServed(mapper meta.RESTMapper, ref corev1.TypedLocalObjectReference) (bool, error) {
	if ref.APIGroup == nil || *ref.APIGroup == "" || ref.Kind == "" {
		return false, nil
	}
	_, err := mapper.RESTMapping(schema.GroupKind{Group: *ref.APIGroup, Kind: ref.Kind})
	if meta.IsNoMatchError(err) {
		return false, nil
	}
	return err == nil, err
}

func driverNotInstalledMessage(ref corev1.TypedLocalObjectReference) string {
	group := ""
	if ref.APIGroup != nil {
		group = *ref.APIGroup
	}
	return fmt.Sprintf("no backup driver is installed for strategy %s/%s %q: the API server does not serve that kind", group, ref.Kind, ref.Name)
}

func clock(now func() time.Time) time.Time {
	if now != nil {
		return now()
	}
	return time.Now()
}
