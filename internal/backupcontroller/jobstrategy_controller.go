// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"fmt"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const (
	// Mode label / values applied to the rendered batch/v1.Job so RBAC and
	// debugging tools can distinguish backup vs restore runs at a glance.
	// Mirrors the Altinity driver's altinityLabelMode convention.
	jobStrategyLabelMode   = "job.strategy.backups.cozystack.io/mode"
	jobStrategyModeBackup  = "backup"
	jobStrategyModeRestore = "restore"

	// Driver-metadata key prefix used to round-trip BackupClassStrategy
	// parameters through the Backup artifact, so a later RestoreJob can
	// re-render the strategy template with the same parameter values that
	// were in effect at backup time.
	jobStrategyParamPrefix = "job.strategy.backups.cozystack.io/parameter/"

	// Polling cadence for the Job lifecycle. Mirrors the Altinity / CNPG /
	// Velero strategy cadence so behaviour is uniform across drivers.
	jobStrategyPollInterval = 5 * time.Second
)

// jobStrategyParameters extracts BackupClassStrategy parameters from a Backup's
// DriverMetadata. Round-trips the values written by reconcileJob at backup time
// so a later RestoreJob can render the strategy template with the parameter
// snapshot in effect when the backup was taken. Mirrors backupParameters from
// the Altinity driver.
func jobStrategyParameters(b *backupsv1alpha1.Backup) map[string]string {
	out := map[string]string{}
	for k, v := range b.Spec.DriverMetadata {
		if !strings.HasPrefix(k, jobStrategyParamPrefix) {
			continue
		}
		paramKey := strings.TrimPrefix(k, jobStrategyParamPrefix)
		if paramKey == "" {
			continue
		}
		out[paramKey] = v
	}
	return out
}

// ---------------------------------------------------------------------------
// BackupJob path
// ---------------------------------------------------------------------------

// reconcileJob drives a BackupJob whose resolved strategy is a
// strategy.backups.cozystack.io/Job. The Job strategy is the generic, app-
// agnostic driver: it renders the strategy's PodTemplateSpec against the live
// application object and runs it once as a batch/v1.Job, treating Job
// completion as backup success. The Altinity driver is a specialisation of
// this flow with a ClickHouse-specific applicationRef gate; the Job driver
// applies no such gate and runs for any applicationRef.
func (r *BackupJobReconciler) reconcileJob(ctx context.Context, j *backupsv1alpha1.BackupJob, resolved *ResolvedBackupConfig) (ctrl.Result, error) {
	logger := getLogger(ctx)
	logger.Debug("reconciling Job strategy", "backupjob", j.Name, "phase", j.Status.Phase)

	if j.Status.Phase == backupsv1alpha1.BackupJobPhaseSucceeded ||
		j.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
		return ctrl.Result{}, nil
	}

	// First-reconcile bookkeeping. Refetch before writing StartedAt so a
	// stale informer cache cannot silently slide the timestamp forward
	// across reconciles, mirroring reconcileAltinity / reconcileCNPG.
	if j.Status.StartedAt == nil {
		fresh := &backupsv1alpha1.BackupJob{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: j.Namespace, Name: j.Name}, fresh); err != nil {
			return ctrl.Result{}, err
		}
		if fresh.Status.StartedAt != nil {
			j.Status.StartedAt = fresh.Status.StartedAt
			j.Status.Phase = fresh.Status.Phase
		} else {
			base := fresh.DeepCopy()
			now := metav1.Now()
			fresh.Status.StartedAt = &now
			fresh.Status.Phase = backupsv1alpha1.BackupJobPhaseRunning
			if err := r.Status().Patch(ctx, fresh, client.MergeFrom(base)); err != nil {
				return ctrl.Result{}, err
			}
			// Requeue so the next reconcile re-Gets j with the post-patch
			// ResourceVersion. Copying StartedAt/Phase back into the local j
			// here would leave any subsequent r.Status().Update calls in the
			// same reconcile carrying the pre-patch ResourceVersion and
			// failing with Conflict. Mirrors reconcileAltinity / reconcileCNPG.
			return ctrl.Result{RequeueAfter: jobStrategyPollInterval}, nil
		}
	}

	strategy := &strategyv1alpha1.Job{}
	if err := r.Get(ctx, client.ObjectKey{Name: resolved.StrategyRef.Name}, strategy); err != nil {
		if apierrors.IsNotFound(err) {
			return r.markBackupJobFailed(ctx, j, fmt.Sprintf("Job strategy not found: %s", resolved.StrategyRef.Name))
		}
		return ctrl.Result{}, err
	}

	app, err := r.getApplicationUnstructured(ctx, j.Namespace, j.Spec.ApplicationRef)
	if err != nil {
		// A missing object (IsNotFound) and an unmappable kind
		// (IsNoMatchError - a typo in applicationRef.kind, or a CRD that
		// isn't installed) are both terminal: retrying cannot fix them.
		// The Job driver, unlike the specialised drivers, has no
		// applicationRef.kind gate, so it is the path that forwards
		// arbitrary kinds into RESTMapping and must classify NoKindMatch
		// itself - otherwise the BackupJob requeues forever in Running.
		if apierrors.IsNotFound(err) || apimeta.IsNoMatchError(err) {
			return r.markBackupJobFailed(ctx, j, fmt.Sprintf("application not found or kind not registered: %s/%s (kind=%q)", j.Namespace, j.Spec.ApplicationRef.Name, j.Spec.ApplicationRef.Kind))
		}
		return ctrl.Result{}, err
	}

	rendered, err := renderJobTemplate(
		strategy.Spec.Template,
		app,
		j.Spec.ApplicationRef.Name,
		j.Namespace,
		jobStrategyModeBackup,
		resolved.Parameters,
		nil,
	)
	if err != nil {
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf("failed to template Job strategy: %v", err))
	}

	batchJob, err := r.ensureJobStrategyJob(ctx, j, j.Namespace, jobNameForBackupJob(j),
		jobStrategyModeBackup,
		map[string]string{
			backupsv1alpha1.OwningJobNameLabel:      j.Name,
			backupsv1alpha1.OwningJobNamespaceLabel: j.Namespace,
		},
		rendered,
	)
	if err != nil {
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf("failed to ensure batch/v1.Job: %v", err))
	}

	switch jobConditionState(batchJob) {
	case batchv1.JobComplete:
		if j.Status.BackupRef != nil {
			return ctrl.Result{}, nil
		}
		artifact, err := r.createJobBackupArtifact(ctx, j, resolved)
		if err != nil {
			return r.markBackupJobFailed(ctx, j, fmt.Sprintf("failed to create Backup artifact: %v", err))
		}
		now := metav1.Now()
		j.Status.BackupRef = &corev1.LocalObjectReference{Name: artifact.Name}
		j.Status.CompletedAt = &now
		j.Status.Phase = backupsv1alpha1.BackupJobPhaseSucceeded
		apimeta.SetStatusCondition(&j.Status.Conditions, metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionTrue,
			Reason:  "BackupCompleted",
			Message: "backup Job completed",
		})
		if err := r.Status().Update(ctx, j); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil

	case batchv1.JobFailed:
		message := jobFailureMessage(batchJob)
		if message == "" {
			message = "backup Job reported Failed"
		}
		return r.markBackupJobFailed(ctx, j, message)

	default:
		return ctrl.Result{RequeueAfter: jobStrategyPollInterval}, nil
	}
}

// ensureJobStrategyJob materialises a batch/v1.Job from the rendered
// PodTemplateSpec, idempotently. The Job is named deterministically per owning
// BackupJob, so a retried reconcile observes the existing Job rather than
// creating a duplicate, and carries a controllerRef so kube-gc collects it
// (and the running Pod) when the BackupJob is deleted. Mirrors
// ensureAltinityJob.
func (r *BackupJobReconciler) ensureJobStrategyJob(
	ctx context.Context,
	owner client.Object,
	namespace, name, mode string,
	ownerLabels map[string]string,
	rendered *corev1.PodTemplateSpec,
) (*batchv1.Job, error) {
	existing := &batchv1.Job{}
	err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, existing)
	if err == nil {
		return existing, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}

	labels := map[string]string{jobStrategyLabelMode: mode}
	for k, v := range ownerLabels {
		labels[k] = v
	}

	desired := buildJobStrategyBatchJob(namespace, name, labels, rendered)
	if err := controllerutil.SetControllerReference(owner, desired, r.Scheme); err != nil {
		return nil, fmt.Errorf("set controller reference on backup Job: %w", err)
	}
	if err := r.Create(ctx, desired); err != nil {
		if apierrors.IsAlreadyExists(err) {
			if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, existing); err != nil {
				return nil, err
			}
			return existing, nil
		}
		return nil, err
	}
	return desired, nil
}

// buildJobStrategyBatchJob assembles the typed batch/v1.Job that wraps the
// rendered PodTemplateSpec. RestartPolicyNever and a small backoff cap match
// what a one-shot backup needs - the tool either succeeds or fails, retries
// inside the same Pod don't add value. Mirrors buildAltinityBatchJob.
func buildJobStrategyBatchJob(namespace, name string, labels map[string]string, rendered *corev1.PodTemplateSpec) *batchv1.Job {
	pod := *rendered.DeepCopy()
	if pod.Spec.RestartPolicy == "" {
		pod.Spec.RestartPolicy = corev1.RestartPolicyNever
	}
	if pod.Labels == nil {
		pod.Labels = map[string]string{}
	}
	for k, v := range labels {
		pod.Labels[k] = v
	}
	backoffLimit := int32(2)
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
			Labels:    labels,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoffLimit,
			Template:     pod,
		},
	}
}

// createJobBackupArtifact materialises a Cozystack Backup carrying the strategy
// reference and the BackupClassStrategy parameters in effect at backup time.
// Parameters round-trip via DriverMetadata under jobStrategyParamPrefix so a
// later RestoreJob can re-render the strategy template against the same values.
// Mirrors createAltinityBackupArtifact - see that function for why Status is
// populated on the struct but not promoted from this driver.
func (r *BackupJobReconciler) createJobBackupArtifact(
	ctx context.Context,
	j *backupsv1alpha1.BackupJob,
	resolved *ResolvedBackupConfig,
) (*backupsv1alpha1.Backup, error) {
	driverMD := map[string]string{}
	for k, v := range resolved.Parameters {
		driverMD[jobStrategyParamPrefix+k] = v
	}

	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      j.Name,
			Namespace: j.Namespace,
		},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: j.Spec.ApplicationRef,
			StrategyRef:    resolved.StrategyRef,
			TakenAt:        metav1.Now(),
			DriverMetadata: driverMD,
		},
		Status: backupsv1alpha1.BackupStatus{
			Phase: backupsv1alpha1.BackupPhaseReady,
		},
	}
	if j.Spec.PlanRef != nil {
		backup.Spec.PlanRef = j.Spec.PlanRef
	}
	if err := r.Create(ctx, backup); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return nil, err
		}
		existing := &backupsv1alpha1.Backup{}
		if getErr := r.Get(ctx, types.NamespacedName{Namespace: backup.Namespace, Name: backup.Name}, existing); getErr != nil {
			return nil, getErr
		}
		return existing, nil
	}
	return backup, nil
}

// ---------------------------------------------------------------------------
// RestoreJob path
// ---------------------------------------------------------------------------

// reconcileJobRestore drives a RestoreJob whose source Backup was produced by
// the Job strategy. It re-renders the same PodTemplateSpec with Mode=restore
// and runs it once as a batch/v1.Job, treating Job completion as restore
// success. Symmetric with reconcileJob, mirroring the Altinity restore path.
//
// Cross-namespace restore is intentionally unsupported: RestoreJob.spec.
// targetApplicationRef is corev1.TypedLocalObjectReference ("Local" => same
// namespace as the RestoreJob and the source Backup), so the restore Job
// always runs in restoreJob.Namespace.
func (r *RestoreJobReconciler) reconcileJobRestore(ctx context.Context, restoreJob *backupsv1alpha1.RestoreJob, backup *backupsv1alpha1.Backup) (ctrl.Result, error) {
	logger := getLogger(ctx)
	logger.Debug("reconciling Job restore", "restorejob", restoreJob.Name, "backup", backup.Name)

	if restoreJob.Status.Phase == backupsv1alpha1.RestoreJobPhaseSucceeded ||
		restoreJob.Status.Phase == backupsv1alpha1.RestoreJobPhaseFailed {
		return ctrl.Result{}, nil
	}

	// Resolve the effective restore target. Defaults to the source
	// application recorded on the Backup; targetApplicationRef overrides any
	// of name/kind/apiGroup when set (e.g. restore-as-copy into a differently
	// named application in the same namespace).
	targetNamespace := restoreJob.Namespace
	targetAppName := backup.Spec.ApplicationRef.Name
	targetAppKind := backup.Spec.ApplicationRef.Kind
	targetAPIGroup := ""
	if backup.Spec.ApplicationRef.APIGroup != nil {
		targetAPIGroup = *backup.Spec.ApplicationRef.APIGroup
	}
	if restoreJob.Spec.TargetApplicationRef != nil {
		if restoreJob.Spec.TargetApplicationRef.Name != "" {
			targetAppName = restoreJob.Spec.TargetApplicationRef.Name
		}
		if restoreJob.Spec.TargetApplicationRef.Kind != "" {
			targetAppKind = restoreJob.Spec.TargetApplicationRef.Kind
		}
		if restoreJob.Spec.TargetApplicationRef.APIGroup != nil {
			targetAPIGroup = *restoreJob.Spec.TargetApplicationRef.APIGroup
		}
	}
	targetRef := corev1.TypedLocalObjectReference{
		APIGroup: stringPtr(targetAPIGroup),
		Kind:     targetAppKind,
		Name:     targetAppName,
	}

	if restoreJob.Status.StartedAt == nil {
		fresh := &backupsv1alpha1.RestoreJob{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: restoreJob.Namespace, Name: restoreJob.Name}, fresh); err != nil {
			return ctrl.Result{}, err
		}
		if fresh.Status.StartedAt != nil {
			restoreJob.Status.StartedAt = fresh.Status.StartedAt
			restoreJob.Status.Phase = fresh.Status.Phase
		} else {
			base := fresh.DeepCopy()
			now := metav1.Now()
			fresh.Status.StartedAt = &now
			fresh.Status.Phase = backupsv1alpha1.RestoreJobPhaseRunning
			if err := r.Status().Patch(ctx, fresh, client.MergeFrom(base)); err != nil {
				return ctrl.Result{}, err
			}
			// Requeue so the next reconcile re-Gets restoreJob with the
			// post-patch ResourceVersion. See reconcileJob for the rationale.
			return ctrl.Result{RequeueAfter: jobStrategyPollInterval}, nil
		}
	}

	strategy := &strategyv1alpha1.Job{}
	if err := r.Get(ctx, client.ObjectKey{Name: backup.Spec.StrategyRef.Name}, strategy); err != nil {
		if apierrors.IsNotFound(err) {
			return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf("Job strategy not found: %s", backup.Spec.StrategyRef.Name))
		}
		return ctrl.Result{}, err
	}

	app, err := r.getApplicationUnstructured(ctx, targetNamespace, targetRef)
	if err != nil {
		// IsNotFound (target app not deployed) and IsNoMatchError
		// (unmappable target kind) are both terminal - see the matching
		// note on the BackupJob path in reconcileJob.
		if apierrors.IsNotFound(err) || apimeta.IsNoMatchError(err) {
			return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf(
				"target application not found or kind not registered: %s/%s (kind=%q; deploy it before requesting a restore)",
				targetNamespace, targetAppName, targetAppKind))
		}
		return ctrl.Result{}, err
	}

	rendered, err := renderJobTemplate(
		strategy.Spec.Template,
		app,
		targetAppName,
		targetNamespace,
		jobStrategyModeRestore,
		jobStrategyParameters(backup),
		backup,
	)
	if err != nil {
		return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf("failed to template Job strategy: %v", err))
	}

	batchJob, err := r.ensureJobStrategyRestoreJob(ctx, restoreJob, targetNamespace, jobNameForRestoreJob(restoreJob),
		jobStrategyModeRestore,
		map[string]string{
			backupsv1alpha1.OwningJobNameLabel:      restoreJob.Name,
			backupsv1alpha1.OwningJobNamespaceLabel: restoreJob.Namespace,
		},
		rendered,
	)
	if err != nil {
		return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf("failed to ensure batch/v1.Job: %v", err))
	}

	switch jobConditionState(batchJob) {
	case batchv1.JobComplete:
		now := metav1.Now()
		restoreJob.Status.CompletedAt = &now
		restoreJob.Status.Phase = backupsv1alpha1.RestoreJobPhaseSucceeded
		apimeta.SetStatusCondition(&restoreJob.Status.Conditions, metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionTrue,
			Reason:  "RestoreCompleted",
			Message: "restore Job completed",
		})
		if err := r.Status().Update(ctx, restoreJob); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil

	case batchv1.JobFailed:
		message := jobFailureMessage(batchJob)
		if message == "" {
			message = "restore Job reported Failed"
		}
		return r.markRestoreJobFailed(ctx, restoreJob, message)

	default:
		return ctrl.Result{RequeueAfter: jobStrategyPollInterval}, nil
	}
}

// ensureJobStrategyRestoreJob is the RestoreJob-side mirror of
// ensureJobStrategyJob. The Job lives in the RestoreJob's namespace and gets a
// controllerRef on the RestoreJob. Mirrors ensureAltinityRestoreJob.
func (r *RestoreJobReconciler) ensureJobStrategyRestoreJob(
	ctx context.Context,
	owner client.Object,
	namespace, name, mode string,
	ownerLabels map[string]string,
	rendered *corev1.PodTemplateSpec,
) (*batchv1.Job, error) {
	existing := &batchv1.Job{}
	err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, existing)
	if err == nil {
		return existing, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}

	labels := map[string]string{jobStrategyLabelMode: mode}
	for k, v := range ownerLabels {
		labels[k] = v
	}

	desired := buildJobStrategyBatchJob(namespace, name, labels, rendered)
	if err := controllerutil.SetControllerReference(owner, desired, r.Scheme); err != nil {
		return nil, fmt.Errorf("set controller reference on restore Job: %w", err)
	}
	if err := r.Create(ctx, desired); err != nil {
		if apierrors.IsAlreadyExists(err) {
			if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, existing); err != nil {
				return nil, err
			}
			return existing, nil
		}
		return nil, err
	}
	return desired, nil
}
