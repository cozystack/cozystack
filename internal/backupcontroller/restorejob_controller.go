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
	"k8s.io/client-go/kubernetes"
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
	Scheme    *runtime.Scheme
	Recorder  record.EventRecorder
	// Clientset reads Pod logs (the controller-runtime cache client cannot);
	// the CNPG restore driver uses it to read a bootstrap-recovery pod's log
	// and tell an unreachable point-in-time target apart from a transient
	// recovery-pod failure. Wired in SetupWithManager; nil-safe at call sites.
	Clientset kubernetes.Interface
	// readPodLog is the seam the CNPG restore driver reads a recovery pod's log
	// through. Defaults to readPodContainerLog (which uses Clientset); tests
	// inject a stub so the log-classification path - including the Forbidden
	// (missing pods/log RBAC) branch - is unit-testable without a live cluster.
	readPodLog        func(ctx context.Context, namespace, podName, container string) (string, error)
	CredentialsConfig BackupCredentialsConfig
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

	// Step 2: Determine effective strategy from backup.spec.strategyRef.
	// Resolve and filter BEFORE projecting credentials so RestoreJobs
	// targeting third-party drivers do not materialise cozy-backups-creds
	// in the tenant namespace.
	if backup.Spec.StrategyRef.APIGroup == nil {
		return r.markRestoreJobFailed(ctx, restoreJob, "Backup has nil StrategyRef.APIGroup")
	}

	if *backup.Spec.StrategyRef.APIGroup != strategyv1alpha1.GroupVersion.Group {
		return r.markRestoreJobFailed(ctx, restoreJob,
			fmt.Sprintf("StrategyRef.APIGroup doesn't match: %s", *backup.Spec.StrategyRef.APIGroup))
	}

	// Reject unsupported Kinds inside the platform APIGroup BEFORE
	// credentials projection (mirrors BackupJob path). Without this guard
	// a RestoreJob against a Backup whose strategyRef.Kind is unknown to
	// this controller would leak cozy-backups-creds into the tenant
	// namespace and then fall through to markRestoreJobFailed with the
	// Secret already in place.
	{
		supported := false
		for _, k := range supportedBackupStrategyKinds() {
			if backup.Spec.StrategyRef.Kind == k {
				supported = true
				break
			}
		}
		if !supported {
			return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf("StrategyRef.Kind not supported: %s", backup.Spec.StrategyRef.Kind))
		}
	}

	// Step 3: Project the platform-managed S3 credentials into the tenant
	// namespace so default Strategy CRs (re-rendered at restore time) can
	// reference a deterministic Secret name. Idempotent / no-op when
	// projection is not configured. Transient errors requeue; terminal
	// misconfig (target owned by someone else, source malformed) is
	// surfaced as Failed.
	if err := ProjectBackupCredentials(ctx, r.Client, r.CredentialsConfig, restoreJob.Namespace); err != nil {
		return r.handleProjectionError(ctx, restoreJob, err)
	}

	logger.Info("processing RestoreJob", "restorejob", restoreJob.Name, "backup", backup.Name, "strategyKind", backup.Spec.StrategyRef.Kind)
	switch backup.Spec.StrategyRef.Kind {
	case strategyv1alpha1.JobStrategyKind:
		return r.reconcileJobRestore(ctx, restoreJob, backup)
	case strategyv1alpha1.VeleroStrategyKind:
		return r.reconcileVeleroRestore(ctx, restoreJob, backup)
	case strategyv1alpha1.CNPGStrategyKind:
		return r.reconcileCNPGRestore(ctx, restoreJob, backup)
	case strategyv1alpha1.AltinityStrategyKind:
		return r.reconcileAltinityRestore(ctx, restoreJob, backup)
	case strategyv1alpha1.MariaDBStrategyKind:
		return r.reconcileMariaDBRestore(ctx, restoreJob, backup)
	case strategyv1alpha1.FoundationDBStrategyKind:
		return r.reconcileFoundationDBRestore(ctx, restoreJob, backup)
	case strategyv1alpha1.EtcdStrategyKind:
		return r.reconcileEtcdRestore(ctx, restoreJob, backup)
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
	if r.Clientset, err = kubernetes.NewForConfig(cfg); err != nil {
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

// handleProjectionError classifies a credentials-projection error as
// transient (requeue with Ready=False/CredentialsProjectionPending) or
// terminal (markRestoreJobFailed). Mirrors the BackupJob variant — both
// rely on IsTransient + CredentialsProjectionRequeue so a single source
// of truth governs the backoff and the transient-vs-terminal split.
func (r *RestoreJobReconciler) handleProjectionError(ctx context.Context, rj *backupsv1alpha1.RestoreJob, err error) (ctrl.Result, error) {
	logger := getLogger(ctx)
	credentialsProjectionFailures.WithLabelValues(rj.Namespace, classifyReason(err)).Inc()
	if IsTransient(err) {
		meta.SetStatusCondition(&rj.Status.Conditions, metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionFalse,
			Reason:  "CredentialsProjectionPending",
			Message: err.Error(),
		})
		if updateErr := r.Status().Update(ctx, rj); updateErr != nil {
			logger.Error(updateErr, "failed to update RestoreJob status to projection-pending")
		}
		logger.Info("restore credentials projection transient failure; requeueing", "message", err.Error())
		return ctrl.Result{RequeueAfter: CredentialsProjectionRequeue}, nil
	}
	return r.markRestoreJobFailed(ctx, rj, fmt.Sprintf("failed to project backup credentials: %v", err))
}

// markRestoreJobFailed updates the RestoreJob status to Failed with the given message.
//
// Coupling note: a failure that fires before reconcileCNPG's StartedAt block
// has set restoreJob.Status.StartedAt leaves StartedAt nil. The CNPG path's
// deadline gates (cnpgWALArchiveDeadline, options.effectiveRestoreDeadline)
// only fire once StartedAt is set, so an early-failure retry that gets all
// the way through to the StartedAt block restarts the deadline budget from
// the retry's StartedAt - intentional, since retries are user-driven and a
// fresh budget is what users expect after correcting the cause of the
// previous failure.
func (r *RestoreJobReconciler) markRestoreJobFailed(ctx context.Context, restoreJob *backupsv1alpha1.RestoreJob, message string) (ctrl.Result, error) {
	return r.markRestoreJobFailedReason(ctx, restoreJob, "RestoreFailed", message)
}

// markRestoreJobFailedReason is markRestoreJobFailed with a caller-chosen
// Ready-condition Reason so a driver can distinguish failure classes a tenant
// acts on differently (e.g. RecoveryTargetUnreachable, which points at the
// recoverable window rather than at the controller).
func (r *RestoreJobReconciler) markRestoreJobFailedReason(ctx context.Context, restoreJob *backupsv1alpha1.RestoreJob, reason, message string) (ctrl.Result, error) {
	logger := getLogger(ctx)
	now := metav1.Now()
	restoreJob.Status.CompletedAt = &now
	restoreJob.Status.Phase = backupsv1alpha1.RestoreJobPhaseFailed
	restoreJob.Status.Message = message

	// SetStatusCondition keeps Conditions matching the +listType=map +listMapKey=type
	// CRD contract: a previously-set Ready condition (e.g. from a transient
	// retry path that flipped through ConditionFalse with a different
	// Reason) is updated in-place rather than appended, and LastTransitionTime
	// is preserved unless Status changes.
	meta.SetStatusCondition(&restoreJob.Status.Conditions, metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionFalse,
		Reason:  reason,
		Message: message,
	})

	if err := r.Status().Update(ctx, restoreJob); err != nil {
		logger.Error(err, "failed to update RestoreJob status to Failed")
		return ctrl.Result{}, err
	}
	logger.Debug("RestoreJob failed", "message", message)
	return ctrl.Result{}, nil
}

// requeueRestoreStrategyNotReady mirrors BackupJobReconciler.requeueStrategyNotReady
// for the restore path. Surfaces a transient Ready=False/StrategyNotReady
// when the Strategy CR referenced by Backup.spec.strategyRef.name is
// not (yet) present — same bootstrap-window self-heal contract as the
// backup path, and the same StrategyNotReadyDeadline bound so a permanently
// missing strategy fails closed instead of requeuing forever.
func (r *RestoreJobReconciler) requeueRestoreStrategyNotReady(ctx context.Context, restoreJob *backupsv1alpha1.RestoreJob, strategyName string) (ctrl.Result, error) {
	logger := getLogger(ctx)
	if strategyNotReadyDeadlineExceeded(restoreJob.Status.StartedAt) {
		return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf(
			"Strategy %q referenced by Backup was not provisioned within %s; check the strategyRef name or the platform backup-storage bootstrap",
			strategyName, StrategyNotReadyDeadline))
	}
	meta.SetStatusCondition(&restoreJob.Status.Conditions, metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionFalse,
		Reason:  "StrategyNotReady",
		Message: fmt.Sprintf("Strategy %q referenced by Backup is not yet provisioned; the platform may still be initialising backup storage", strategyName),
	})
	if updateErr := r.Status().Update(ctx, restoreJob); updateErr != nil {
		logger.Error(updateErr, "failed to update RestoreJob status to StrategyNotReady")
	}
	return ctrl.Result{RequeueAfter: CredentialsProjectionRequeue}, nil
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
	kind, err := strategyKindForRestoreJob(ctx, r.Client, restoreJob)
	if err != nil {
		// The referenced Backup is gone or unreadable, so we cannot tell
		// which driver produced this RestoreJob. Speculatively reap any
		// Velero Restore it may have created, but quietly: this is not
		// necessarily a Velero job, so a failure (or a cluster with no
		// Velero CRDs) must not surface a CleanupFailed event.
		logger.V(1).Info("RestoreJob Backup unreadable; attempting best-effort Velero cleanup",
			"restoreJob", restoreJob.Name, "error", err.Error())
		r.cleanupStrayVeleroRestore(ctx, restoreJob)
		return
	}
	logger.V(1).Info("dispatching RestoreJob cleanup", "restoreJob", restoreJob.Name, "strategy", kind)
	switch kind {
	case strategyv1alpha1.VeleroStrategyKind:
		r.cleanupVeleroRestore(ctx, restoreJob)

	case strategyv1alpha1.CNPGStrategyKind, strategyv1alpha1.JobStrategyKind, strategyv1alpha1.AltinityStrategyKind, strategyv1alpha1.MariaDBStrategyKind, strategyv1alpha1.FoundationDBStrategyKind, strategyv1alpha1.EtcdStrategyKind:
		// Nothing to clean up: these drivers don't materialise namespaced
		// artifacts that outlive the RestoreJob. (Etcd: the operator-side
		// EtcdCluster is owned by the source HelmRelease, and the
		// EtcdClusterSpecCaptured / TargetPurged conditions live on the
		// RestoreJob itself - all gone with the parent.)
	default:
		// Readable Backup, but an unrecognised strategy kind — not Velero
		// as far as we can tell. Speculatively reap a stray labelled Velero
		// Restore if one exists, quietly: don't emit a CleanupFailed event
		// for a strategy this controller doesn't own.
		logger.V(1).Info("RestoreJob has unrecognised strategy kind; attempting best-effort Velero cleanup",
			"restoreJob", restoreJob.Name, "kind", kind)
		r.cleanupStrayVeleroRestore(ctx, restoreJob)
	}
}

// strategyKindForRestoreJob looks up the strategy kind via the referenced
// Backup. Returns a non-nil error when the Backup is missing or unreadable
// so callers can distinguish "cannot tell" from a readable Backup whose
// strategy kind is simply one they don't handle.
func strategyKindForRestoreJob(ctx context.Context, c client.Client, restoreJob *backupsv1alpha1.RestoreJob) (string, error) {
	backup := &backupsv1alpha1.Backup{}
	key := types.NamespacedName{Namespace: restoreJob.Namespace, Name: restoreJob.Spec.BackupRef.Name}
	if err := c.Get(ctx, key, backup); err != nil {
		return "", err
	}
	return backup.Spec.StrategyRef.Kind, nil
}

// cleanupVeleroRestore deletes all Velero Restores and resourceModifier
// ConfigMaps owned by this RestoreJob (identified by labels). Used on the
// known-Velero path, so a failed Restore delete is surfaced as a
// CleanupFailed event.
func (r *RestoreJobReconciler) cleanupVeleroRestore(ctx context.Context, restoreJob *backupsv1alpha1.RestoreJob) {
	r.deleteVeleroRestoreArtifacts(ctx, restoreJob, true)
}

// cleanupStrayVeleroRestore is the speculative variant used when we cannot
// positively identify the RestoreJob as Velero (Backup unreadable, or a
// readable Backup with an unrecognised strategy kind). It acts only when a
// labelled Velero Restore actually exists, so on a cluster without the
// Velero CRDs — a supported configuration, see values.yaml velero.bslEnabled
// — the List fails and we skip silently. It never emits a CleanupFailed
// event, because the RestoreJob may not be a Velero job at all.
func (r *RestoreJobReconciler) cleanupStrayVeleroRestore(ctx context.Context, restoreJob *backupsv1alpha1.RestoreJob) {
	logger := log.FromContext(ctx)
	list := &velerov1.RestoreList{}
	if err := r.List(ctx, list,
		client.InNamespace(veleroNamespace),
		client.MatchingLabels{
			backupsv1alpha1.OwningJobNameLabel:      restoreJob.Name,
			backupsv1alpha1.OwningJobNamespaceLabel: restoreJob.Namespace,
		},
	); err != nil {
		logger.V(1).Info("skipping speculative Velero cleanup (Velero absent or List failed)",
			"restoreJob", restoreJob.Name, "reason", err.Error())
		return
	}
	if len(list.Items) == 0 {
		return
	}
	// A stray labelled Velero Restore exists — this RestoreJob was a Velero
	// job after all. Reap it (and the resourceModifiers ConfigMaps), still
	// best-effort and event-free.
	r.deleteVeleroRestoreArtifacts(ctx, restoreJob, false)
}

// deleteVeleroRestoreArtifacts deletes the Velero Restores and
// resourceModifier ConfigMaps owned by this RestoreJob. When emitEvent is
// true a failed Restore delete is surfaced as a CleanupFailed Warning;
// speculative callers pass false to stay quiet.
func (r *RestoreJobReconciler) deleteVeleroRestoreArtifacts(ctx context.Context, restoreJob *backupsv1alpha1.RestoreJob, emitEvent bool) {
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
		if emitEvent {
			r.Recorder.Event(restoreJob, corev1.EventTypeWarning, "CleanupFailed",
				fmt.Sprintf("Failed to delete Velero Restore: %v", err))
		}
	}

	if err := r.DeleteAllOf(ctx, &corev1.ConfigMap{}, opts...); err != nil {
		logger.Error(err, "failed to delete resourceModifiers ConfigMap(s)")
	}
}
