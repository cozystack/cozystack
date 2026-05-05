package backupcontroller

import (
	"context"
	"fmt"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	velerov1 "github.com/vmware-tanzu/velero/pkg/apis/velero/v1"
)

const restoreJobFinalizer = "backups.cozystack.io/cleanup"

// RestoreJobReconciler reconciles RestoreJob objects.
// It routes RestoreJobs to strategy-specific handlers based on the strategy
// referenced in the Backup that the RestoreJob is restoring from.
type RestoreJobReconciler struct {
	client.Client
	dynamic.Interface
	meta.RESTMapper
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

func (r *RestoreJobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("reconciling RestoreJob", "namespace", req.Namespace, "name", req.Name)

	restoreJob := &backupsv1alpha1.RestoreJob{}
	err := r.Get(ctx, types.NamespacedName{Namespace: req.Namespace, Name: req.Name}, restoreJob)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(1).Info("RestoreJob not found, skipping")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "failed to get RestoreJob")
		return ctrl.Result{}, err
	}

	// Handle deletion: dispatch cleanup by strategy. The Velero strategy
	// owns side-state (Velero Restore + ConfigMap in cozy-velero); the CNPG
	// and Job strategies own no namespaced artifacts that survive RestoreJob
	// deletion, so their cleanup is a no-op. Reading the referenced Backup
	// to identify the strategy is best-effort - a missing/unreadable Backup
	// must not block finalizer removal, otherwise the RestoreJob would be
	// stuck in Terminating forever.
	if !restoreJob.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(restoreJob, restoreJobFinalizer) {
			r.cleanupOnDelete(ctx, restoreJob)
			controllerutil.RemoveFinalizer(restoreJob, restoreJobFinalizer)
			if err := r.Update(ctx, restoreJob); err != nil {
				return ctrl.Result{}, err
			}
			logger.V(1).Info("removed finalizer and cleaned up strategy-owned side state", "restoreJob", restoreJob.Name)
		}
		return ctrl.Result{}, nil
	}

	// Ensure finalizer is present
	if !controllerutil.ContainsFinalizer(restoreJob, restoreJobFinalizer) {
		controllerutil.AddFinalizer(restoreJob, restoreJobFinalizer)
		if err := r.Update(ctx, restoreJob); err != nil {
			return ctrl.Result{}, err
		}
	}

	// If already completed, no need to reconcile
	if restoreJob.Status.Phase == backupsv1alpha1.RestoreJobPhaseSucceeded ||
		restoreJob.Status.Phase == backupsv1alpha1.RestoreJobPhaseFailed {
		logger.V(1).Info("RestoreJob already completed, skipping", "phase", restoreJob.Status.Phase)
		return ctrl.Result{}, nil
	}

	// Step 1: Fetch the referenced Backup
	backup := &backupsv1alpha1.Backup{}
	backupKey := types.NamespacedName{Namespace: req.Namespace, Name: restoreJob.Spec.BackupRef.Name}
	if err := r.Get(ctx, backupKey, backup); err != nil {
		return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf("failed to get Backup: %v", err))
	}

	// Step 2: Determine effective strategy from backup.spec.strategyRef
	if backup.Spec.StrategyRef.APIGroup == nil {
		return r.markRestoreJobFailed(ctx, restoreJob, "Backup has nil StrategyRef.APIGroup")
	}

	if *backup.Spec.StrategyRef.APIGroup != strategyv1alpha1.GroupVersion.Group {
		return r.markRestoreJobFailed(ctx, restoreJob,
			fmt.Sprintf("StrategyRef.APIGroup doesn't match: %s", *backup.Spec.StrategyRef.APIGroup))
	}

	logger.Info("processing RestoreJob", "restorejob", restoreJob.Name, "backup", backup.Name, "strategyKind", backup.Spec.StrategyRef.Kind)
	switch backup.Spec.StrategyRef.Kind {
	case strategyv1alpha1.JobStrategyKind:
		return r.reconcileJobRestore(ctx, restoreJob, backup)
	case strategyv1alpha1.VeleroStrategyKind:
		return r.reconcileVeleroRestore(ctx, restoreJob, backup)
	case strategyv1alpha1.CNPGStrategyKind:
		return r.reconcileCNPGRestore(ctx, restoreJob, backup)
	default:
		return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf("StrategyRef.Kind not supported: %s", backup.Spec.StrategyRef.Kind))
	}
}

// SetupWithManager registers our controller with the Manager and sets up watches.
func (r *RestoreJobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	cfg := mgr.GetConfig()
	var err error
	if r.Interface, err = dynamic.NewForConfig(cfg); err != nil {
		return err
	}
	var h *http.Client
	if h, err = rest.HTTPClientFor(cfg); err != nil {
		return err
	}
	if r.RESTMapper, err = apiutil.NewDynamicRESTMapper(cfg, h); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&backupsv1alpha1.RestoreJob{}).
		Complete(r)
}

// markRestoreJobFailed updates the RestoreJob status to Failed with the given message.
func (r *RestoreJobReconciler) markRestoreJobFailed(ctx context.Context, restoreJob *backupsv1alpha1.RestoreJob, message string) (ctrl.Result, error) {
	logger := getLogger(ctx)
	now := metav1.Now()
	restoreJob.Status.CompletedAt = &now
	restoreJob.Status.Phase = backupsv1alpha1.RestoreJobPhaseFailed
	restoreJob.Status.Message = message

	// Add condition
	restoreJob.Status.Conditions = append(restoreJob.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             "RestoreFailed",
		Message:            message,
		LastTransitionTime: now,
	})

	if err := r.Status().Update(ctx, restoreJob); err != nil {
		logger.Error(err, "failed to update RestoreJob status to Failed")
		return ctrl.Result{}, err
	}
	logger.Debug("RestoreJob failed", "message", message)
	return ctrl.Result{}, nil
}

// cleanupResourceModifierConfigMaps deletes resource modifier ConfigMaps owned
// by this RestoreJob. Called on completion (success or failure) to avoid leaking
// ConfigMaps in cozy-velero when RestoreJobs are not immediately deleted.
func (r *RestoreJobReconciler) cleanupResourceModifierConfigMaps(ctx context.Context, restoreJob *backupsv1alpha1.RestoreJob) {
	logger := log.FromContext(ctx)
	opts := []client.DeleteAllOfOption{
		client.InNamespace(veleroNamespace),
		client.MatchingLabels{
			backupsv1alpha1.OwningJobNameLabel:      restoreJob.Name,
			backupsv1alpha1.OwningJobNamespaceLabel: restoreJob.Namespace,
		},
	}
	if err := r.DeleteAllOf(ctx, &corev1.ConfigMap{}, opts...); err != nil {
		logger.Error(err, "failed to clean up resourceModifiers ConfigMap(s)")
	}
}

// cleanupOnDelete dispatches RestoreJob deletion cleanup to the strategy
// driver that produced the side state. Velero RestoreJobs created Velero
// Restore CRs and resourceModifiers ConfigMaps in cozy-velero; CNPG and
// Job RestoreJobs do not. Dispatching avoids issuing pointless DeleteAllOf
// calls against cozy-velero for non-Velero RestoreJobs and prevents the
// "finalizer name says velero but the job is CNPG" UX papercut.
func (r *RestoreJobReconciler) cleanupOnDelete(ctx context.Context, restoreJob *backupsv1alpha1.RestoreJob) {
	logger := log.FromContext(ctx)
	kind := strategyKindForRestoreJob(ctx, r.Client, restoreJob)
	logger.V(1).Info("dispatching RestoreJob cleanup", "restoreJob", restoreJob.Name, "strategy", kind)
	switch kind {
	case strategyv1alpha1.VeleroStrategyKind:
		r.cleanupVeleroRestore(ctx, restoreJob)
	case strategyv1alpha1.CNPGStrategyKind, strategyv1alpha1.JobStrategyKind:
		// Nothing to clean up: these drivers don't materialise namespaced
		// artifacts that outlive the RestoreJob.
	default:
		// Unknown strategy or Backup unreadable. Conservative path: try
		// the Velero cleanup since it's idempotent (DeleteAllOf with
		// label selector returns 0 deletes when nothing matches), so a
		// stray Velero Restore from an old RestoreJob still gets reaped.
		r.cleanupVeleroRestore(ctx, restoreJob)
	}
}

// strategyKindForRestoreJob looks up the strategy kind via the referenced
// Backup. Returns "" if the Backup is missing or unreadable; callers must
// treat that as "unknown" and fall back to a safe default.
func strategyKindForRestoreJob(ctx context.Context, c client.Client, restoreJob *backupsv1alpha1.RestoreJob) string {
	backup := &backupsv1alpha1.Backup{}
	key := types.NamespacedName{Namespace: restoreJob.Namespace, Name: restoreJob.Spec.BackupRef.Name}
	if err := c.Get(ctx, key, backup); err != nil {
		return ""
	}
	return backup.Spec.StrategyRef.Kind
}

// cleanupVeleroRestore deletes all Velero Restores and resourceModifier
// ConfigMaps owned by this RestoreJob (identified by labels).
func (r *RestoreJobReconciler) cleanupVeleroRestore(ctx context.Context, restoreJob *backupsv1alpha1.RestoreJob) {
	logger := log.FromContext(ctx)
	opts := []client.DeleteAllOfOption{
		client.InNamespace(veleroNamespace),
		client.MatchingLabels{
			backupsv1alpha1.OwningJobNameLabel:      restoreJob.Name,
			backupsv1alpha1.OwningJobNamespaceLabel: restoreJob.Namespace,
		},
	}

	if err := r.DeleteAllOf(ctx, &velerov1.Restore{}, opts...); err != nil {
		logger.Error(err, "failed to delete Velero Restore(s)")
		r.Recorder.Event(restoreJob, corev1.EventTypeWarning, "CleanupFailed",
			fmt.Sprintf("Failed to delete Velero Restore: %v", err))
	}

	if err := r.DeleteAllOf(ctx, &corev1.ConfigMap{}, opts...); err != nil {
		logger.Error(err, "failed to delete resourceModifiers ConfigMap(s)")
	}
}
