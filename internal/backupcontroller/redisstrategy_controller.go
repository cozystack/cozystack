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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	"github.com/cozystack/cozystack/internal/template"
)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const (
	redisAppKind = "Redis"

	// Mode label / values applied to the rendered batch/v1.Job so RBAC and
	// debugging tools can distinguish backup vs restore runs at a glance.
	// Mirrors the Altinity driver's altinityLabelMode convention.
	redisLabelMode   = "redis.strategy.backups.cozystack.io/mode"
	redisModeBackup  = "backup"
	redisModeRestore = "restore"
	// redisModeCleanup renders the strategy as a one-shot object-delete Job that
	// removes a deleted Backup's object from the bucket. It never contacts Redis,
	// so it works after the source app is gone.
	redisModeCleanup = "cleanup"

	// redisSkipArtifactCleanupAnnotation, set to "true" on a Backup, releases it
	// from Terminating without deleting its object — the escape hatch for a
	// Backup wedged on an unreachable bucket. The object is left behind.
	redisSkipArtifactCleanupAnnotation = "backups.cozystack.io/skip-artifact-cleanup"

	// Driver-metadata key prefix used to round-trip BackupClassStrategy
	// parameters through the Backup artifact, so a later RestoreJob can
	// re-render the strategy template with the same parameter values that
	// were in effect at backup time. Mirrors altinityParamPrefix.
	redisParamPrefix = "redis.strategy.backups.cozystack.io/parameter/"

	// redisObjectMetaKey records, on the Backup, the exact object-storage key
	// the dump was written to. Each BackupJob mints a distinct key (see
	// redisObjectKey), so a Plan keeps one object per run and a restore reads
	// back the object its Backup names rather than whatever last overwrote a
	// shared path.
	redisObjectMetaKey = "redis.strategy.backups.cozystack.io/object"

	// Polling cadence for the Job lifecycle. Mirrors the Altinity / Job /
	// CNPG strategy cadence so behaviour is uniform across drivers.
	redisPollInterval = 5 * time.Second

	// redisDefaultBackupDeadline bounds how long the driver will wait on the
	// app-readiness precondition before failing the run terminally, so a
	// RedisFailover that reports Ready=False forever surfaces a legible
	// failure instead of requeuing silently. Matches the etcd deadline.
	redisDefaultBackupDeadline = 20 * time.Minute
)

// validateRedisApplicationRef rejects ApplicationRefs that are not
// apps.cozystack.io/Redis. Without this gate the dispatcher would route
// non-Redis refs to the Redis driver and the templated redis-dump invocation
// would fail with confusing runtime errors. Mirrors
// validateAltinityApplicationRef; empty APIGroup is accepted and treated as
// the default (apps.cozystack.io).
func validateRedisApplicationRef(ref corev1.TypedLocalObjectReference) error {
	if ref.Kind != redisAppKind {
		return fmt.Errorf("redis strategy supports applicationRef.kind=%q, got %q", redisAppKind, ref.Kind)
	}
	apiGroup := ""
	if ref.APIGroup != nil {
		apiGroup = *ref.APIGroup
	}
	if apiGroup != "" && apiGroup != backupsv1alpha1.DefaultApplicationAPIGroup {
		return fmt.Errorf("redis strategy supports applicationRef.apiGroup=%q, got %q", backupsv1alpha1.DefaultApplicationAPIGroup, apiGroup)
	}
	return nil
}

// redisAppReady is the backup/restore precondition. It reports whether the
// driver may proceed to launch the dump/restore Job against the live Redis
// application object.
//
// It blocks ONLY on an explicit Ready=False condition (returning its reason and
// message so the BackupJob surfaces a precise cause), and proceeds on Ready=True,
// Ready=Unknown, or the absence of a Ready condition. Proceeding on absence is
// deliberate: not every apps.cozystack.io object surfaces a Ready condition, and
// a driver that blocked on a missing condition would wedge backups for those
// apps until the deadline. When the app is genuinely unreachable, the Job itself
// fails with the connection error rather than the driver hanging.
func redisAppReady(app map[string]interface{}) (proceed bool, reason, message string) {
	conditions, found, err := unstructured.NestedSlice(app, "status", "conditions")
	if err != nil || !found {
		return true, "", ""
	}
	for _, raw := range conditions {
		cond, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		condType, _, _ := unstructured.NestedString(cond, "type")
		if condType != "Ready" {
			continue
		}
		status, _, _ := unstructured.NestedString(cond, "status")
		if status == string(metav1.ConditionFalse) {
			reason, _, _ = unstructured.NestedString(cond, "reason")
			message, _, _ = unstructured.NestedString(cond, "message")
			return false, reason, message
		}
		return true, "", ""
	}
	return true, "", ""
}

// redisBackupDeadlineExceeded reports whether the precondition wait has
// exhausted its budget. Returns false when StartedAt is nil so the first
// reconcile (which sets it) never trips the gate. Mirrors
// cnpgBackupDeadlineExceeded.
func redisBackupDeadlineExceeded(startedAt *metav1.Time) bool {
	if startedAt == nil {
		return false
	}
	return time.Since(startedAt.Time) > redisDefaultBackupDeadline
}

// redisStrategyParameters extracts BackupClassStrategy parameters from a
// Backup's DriverMetadata. Round-trips the values written by reconcileRedis at
// backup time so a later RestoreJob can render the strategy template with the
// parameter snapshot in effect when the backup was taken. Mirrors
// backupParameters from the Altinity driver.
func redisStrategyParameters(b *backupsv1alpha1.Backup) map[string]string {
	out := map[string]string{}
	for k, v := range b.Spec.DriverMetadata {
		if !strings.HasPrefix(k, redisParamPrefix) {
			continue
		}
		paramKey := strings.TrimPrefix(k, redisParamPrefix)
		if paramKey == "" {
			continue
		}
		out[paramKey] = v
	}
	return out
}

// redisObjectKey mints the object-storage key for one backup. Including the
// BackupJob name (unique per Plan tick) makes every backup a distinct object, so
// a later run cannot overwrite an earlier recovery point.
func redisObjectKey(namespace, appName, backupName string) string {
	return fmt.Sprintf("%s/%s/%s.rdb", namespace, appName, backupName)
}

// redisRestoreObjectKey resolves which object a restore must read: the key
// recorded on the Backup at backup time. A Backup written before object keys
// were recorded (none exist in a released cluster, but be defensive) falls back
// to the legacy fixed name so it can still be restored.
func redisRestoreObjectKey(b *backupsv1alpha1.Backup) string {
	if key := b.Spec.DriverMetadata[redisObjectMetaKey]; key != "" {
		return key
	}
	return fmt.Sprintf("%s/%s/dump.rdb", b.Namespace, b.Spec.ApplicationRef.Name)
}

// renderRedisTemplate runs the strategy's PodTemplateSpec through the
// repository's text/template engine with the same context shape as the Altinity
// / Job drivers: every string field is templated against the application
// object, the release shorthand, the run mode, and the resolved parameters. On
// restore it also exposes .Backup.ApplicationRef so a to-copy restore can read
// the SOURCE release's object-storage prefix while writing into a differently-
// named target. Mirrors renderAltinityTemplate.
func renderRedisTemplate(
	tmpl corev1.PodTemplateSpec,
	app map[string]interface{},
	releaseName, releaseNamespace, mode, objectKey string,
	parameters map[string]string,
	backup *backupsv1alpha1.Backup,
) (*corev1.PodTemplateSpec, error) {
	ctxMap := map[string]interface{}{
		"Application": app,
		"Release": map[string]string{
			"Name":      releaseName,
			"Namespace": releaseNamespace,
		},
		"Mode":       mode,
		"ObjectKey":  objectKey,
		"Parameters": parameters,
	}
	if backup != nil {
		sourceAPIGroup := ""
		if backup.Spec.ApplicationRef.APIGroup != nil {
			sourceAPIGroup = *backup.Spec.ApplicationRef.APIGroup
		}
		ctxMap["Backup"] = map[string]interface{}{
			"Name":      backup.Name,
			"Namespace": backup.Namespace,
			"ApplicationRef": map[string]string{
				"APIGroup": sourceAPIGroup,
				"Kind":     backup.Spec.ApplicationRef.Kind,
				"Name":     backup.Spec.ApplicationRef.Name,
			},
		}
	}
	return template.Template(&tmpl, ctxMap)
}

// ---------------------------------------------------------------------------
// BackupJob path
// ---------------------------------------------------------------------------

// reconcileRedis drives a BackupJob whose resolved strategy is a
// strategy.backups.cozystack.io/Redis. The spotahome operator ships no backup
// CRD, so the driver renders the strategy's PodTemplateSpec against the live
// Redis application object and runs it once as a batch/v1.Job that dumps an RDB
// snapshot to object storage; Job completion is backup success. Structurally a
// specialisation of the Job strategy with a Redis applicationRef gate and an
// app-readiness precondition, mirroring reconcileAltinity.
func (r *BackupJobReconciler) reconcileRedis(ctx context.Context, j *backupsv1alpha1.BackupJob, resolved *ResolvedBackupConfig) (ctrl.Result, error) {
	logger := getLogger(ctx)
	logger.Debug("reconciling Redis strategy", "backupjob", j.Name, "phase", j.Status.Phase)

	if j.Status.Phase == backupsv1alpha1.BackupJobPhaseSucceeded ||
		j.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
		return ctrl.Result{}, nil
	}

	if err := validateRedisApplicationRef(j.Spec.ApplicationRef); err != nil {
		return r.markBackupJobFailed(ctx, j, err.Error())
	}

	// First-reconcile bookkeeping. Refetch before writing StartedAt so a
	// stale informer cache cannot silently slide the timestamp forward
	// across reconciles, mirroring reconcileAltinity.
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
			// ResourceVersion. See reconcileAltinity for the rationale.
			return ctrl.Result{RequeueAfter: redisPollInterval}, nil
		}
	}

	strategy := &strategyv1alpha1.Redis{}
	if err := r.Get(ctx, client.ObjectKey{Name: resolved.StrategyRef.Name}, strategy); err != nil {
		if apierrors.IsNotFound(err) {
			return r.requeueStrategyNotReady(ctx, j, resolved.StrategyRef.Name)
		}
		return ctrl.Result{}, err
	}

	app, err := r.getApplicationUnstructured(ctx, j.Namespace, j.Spec.ApplicationRef)
	if err != nil {
		if apierrors.IsNotFound(err) || apimeta.IsNoMatchError(err) {
			return r.markBackupJobFailed(ctx, j, fmt.Sprintf("Redis application not found or kind not registered: %s/%s", j.Namespace, j.Spec.ApplicationRef.Name))
		}
		return ctrl.Result{}, err
	}

	// Gate on application readiness only before the Job exists. Once it has been
	// created, observe it directly (via the switch below): re-checking readiness
	// here would mark a run Failed whose Job may already have completed, if the
	// app's Ready flips False after launch and stays so past the deadline (an
	// unrelated HelmRelease blip). Only a definite NotFound means "not created
	// yet"; any other Get error is undecidable, so requeue rather than re-gate
	// readiness on it (a transient error must not reintroduce the spurious fail).
	// The deadline still bounds the pre-launch wait.
	existingBackupJob := &batchv1.Job{}
	switch err := r.Get(ctx, client.ObjectKey{Namespace: j.Namespace, Name: jobNameForBackupJob(j)}, existingBackupJob); {
	case err == nil:
		// Job exists; skip the readiness re-gate and observe it below.
	case apierrors.IsNotFound(err):
		if proceed, reason, message := redisAppReady(app); !proceed {
			if redisBackupDeadlineExceeded(j.Status.StartedAt) {
				return r.markBackupJobFailed(ctx, j, fmt.Sprintf(
					"Redis application %s/%s not ready within %s (%s: %s)",
					j.Namespace, j.Spec.ApplicationRef.Name, redisDefaultBackupDeadline, reason, message))
			}
			apimeta.SetStatusCondition(&j.Status.Conditions, metav1.Condition{
				Type:    "Ready",
				Status:  metav1.ConditionFalse,
				Reason:  "ApplicationNotReady",
				Message: fmt.Sprintf("Redis application %s/%s is not ready (%s: %s)", j.Namespace, j.Spec.ApplicationRef.Name, reason, message),
			})
			if updateErr := r.Status().Update(ctx, j); updateErr != nil {
				return ctrl.Result{}, updateErr
			}
			return ctrl.Result{RequeueAfter: redisPollInterval}, nil
		}
	default:
		return ctrl.Result{}, err
	}

	objectKey := redisObjectKey(j.Namespace, j.Spec.ApplicationRef.Name, j.Name)
	rendered, err := renderRedisTemplate(
		strategy.Spec.Template,
		app,
		j.Spec.ApplicationRef.Name,
		j.Namespace,
		redisModeBackup,
		objectKey,
		resolved.Parameters,
		nil,
	)
	if err != nil {
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf("failed to template Redis strategy: %v", err))
	}

	batchJob, err := r.ensureRedisJob(ctx, j, j.Namespace, jobNameForBackupJob(j),
		redisModeBackup,
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
		artifact, err := r.createRedisBackupArtifact(ctx, j, resolved, objectKey)
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
			Message: "redis backup Job completed",
		})
		if err := r.Status().Update(ctx, j); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil

	case batchv1.JobFailed:
		message := jobFailureMessage(batchJob)
		if message == "" {
			message = "redis backup Job reported Failed"
		}
		return r.markBackupJobFailed(ctx, j, message)

	default:
		return ctrl.Result{RequeueAfter: redisPollInterval}, nil
	}
}

// ensureBackupBatchJob idempotently materialises a batch/v1.Job from the
// rendered PodTemplateSpec: a deterministic name makes a retried reconcile
// observe the existing Job rather than create a duplicate, and the controllerRef
// lets kube-gc collect the Job (and its Pod) when the owner is deleted. The
// backup and restore paths of the Job-executed drivers share this shape (see
// ensureAltinityJob / ensureJobStrategyJob); the Redis driver factors it out
// rather than copy it a fifth time.
func ensureBackupBatchJob(
	ctx context.Context,
	c client.Client,
	scheme *runtime.Scheme,
	owner client.Object,
	namespace, name string,
	labels map[string]string,
	rendered *corev1.PodTemplateSpec,
) (*batchv1.Job, error) {
	existing := &batchv1.Job{}
	err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, existing)
	if err == nil {
		return existing, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}

	desired := buildJobStrategyBatchJob(namespace, name, labels, rendered)
	// Promote the strategy's Pod-level activeDeadlineSeconds to the Job so it
	// bounds the whole run: buildJobStrategyBatchJob sets backoffLimit=2, and a
	// Pod-level deadline alone would let a wedged run burn (backoffLimit+1) x the
	// deadline before the Job reports Failed, widening the window a cron Plan
	// stacks behind. A Job-level deadline caps the aggregate regardless of retries.
	if d := desired.Spec.Template.Spec.ActiveDeadlineSeconds; d != nil && desired.Spec.ActiveDeadlineSeconds == nil {
		desired.Spec.ActiveDeadlineSeconds = d
	}
	if err := controllerutil.SetControllerReference(owner, desired, scheme); err != nil {
		return nil, fmt.Errorf("set controller reference on backup Job: %w", err)
	}
	if err := c.Create(ctx, desired); err != nil {
		if apierrors.IsAlreadyExists(err) {
			if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, existing); err != nil {
				return nil, err
			}
			return existing, nil
		}
		return nil, err
	}
	return desired, nil
}

// ensureRedisJob wraps ensureBackupBatchJob with the Redis mode label, mirroring
// ensureAltinityJob.
func (r *BackupJobReconciler) ensureRedisJob(
	ctx context.Context,
	owner client.Object,
	namespace, name, mode string,
	ownerLabels map[string]string,
	rendered *corev1.PodTemplateSpec,
) (*batchv1.Job, error) {
	labels := map[string]string{redisLabelMode: mode}
	for k, v := range ownerLabels {
		labels[k] = v
	}
	return ensureBackupBatchJob(ctx, r.Client, r.Scheme, owner, namespace, name, labels, rendered)
}

// createRedisBackupArtifact materialises a Cozystack Backup carrying the
// strategy reference and the BackupClassStrategy parameters in effect at backup
// time. Parameters round-trip via DriverMetadata under redisParamPrefix so a
// later RestoreJob can re-render the strategy template against the same values.
// The Redis driver keeps no operator-side restore state, so there is no
// underlyingResources snapshot — mirrors createAltinityBackupArtifact.
func (r *BackupJobReconciler) createRedisBackupArtifact(
	ctx context.Context,
	j *backupsv1alpha1.BackupJob,
	resolved *ResolvedBackupConfig,
	objectKey string,
) (*backupsv1alpha1.Backup, error) {
	driverMD := map[string]string{redisObjectMetaKey: objectKey}
	for k, v := range resolved.Parameters {
		driverMD[redisParamPrefix+k] = v
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

// reconcileRedisRestore drives a RestoreJob whose source Backup was produced by
// the Redis strategy. It re-renders the same PodTemplateSpec with Mode=restore
// and runs it once as a batch/v1.Job that replays the RDB snapshot into the
// target's master; Job completion is restore success. in-place (target defaults
// to the source app) and to-copy (targetApplicationRef overrides the name) share
// this path, mirroring reconcileAltinityRestore.
//
// Cross-namespace restore is intentionally unsupported: RestoreJob.spec.
// targetApplicationRef is corev1.TypedLocalObjectReference, so the restore Job
// always runs in restoreJob.Namespace.
func (r *RestoreJobReconciler) reconcileRedisRestore(ctx context.Context, restoreJob *backupsv1alpha1.RestoreJob, backup *backupsv1alpha1.Backup) (ctrl.Result, error) {
	logger := getLogger(ctx)
	logger.Debug("reconciling Redis restore", "restorejob", restoreJob.Name, "backup", backup.Name)

	if restoreJob.Status.Phase == backupsv1alpha1.RestoreJobPhaseSucceeded ||
		restoreJob.Status.Phase == backupsv1alpha1.RestoreJobPhaseFailed {
		return ctrl.Result{}, nil
	}

	if err := validateRedisApplicationRef(backup.Spec.ApplicationRef); err != nil {
		return r.markRestoreJobFailed(ctx, restoreJob, err.Error())
	}

	// Resolve the effective restore target. Defaults to the source application
	// recorded on the Backup; targetApplicationRef overrides any of
	// name/kind/apiGroup when set (to-copy into a differently-named app in the
	// same namespace).
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
	if err := validateRedisApplicationRef(targetRef); err != nil {
		return r.markRestoreJobFailed(ctx, restoreJob, err.Error())
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
			return ctrl.Result{RequeueAfter: redisPollInterval}, nil
		}
	}

	strategy := &strategyv1alpha1.Redis{}
	if err := r.Get(ctx, client.ObjectKey{Name: backup.Spec.StrategyRef.Name}, strategy); err != nil {
		if apierrors.IsNotFound(err) {
			return r.requeueRestoreStrategyNotReady(ctx, restoreJob, backup.Spec.StrategyRef.Name)
		}
		return ctrl.Result{}, err
	}

	app, err := r.getApplicationUnstructured(ctx, targetNamespace, targetRef)
	if err != nil {
		if apierrors.IsNotFound(err) || apimeta.IsNoMatchError(err) {
			return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf(
				"target Redis application not found or kind not registered: %s/%s (deploy it before requesting a restore)",
				targetNamespace, targetAppName))
		}
		return ctrl.Result{}, err
	}

	// Gate on target readiness only before the restore Job exists (see the
	// backup path): once created, the Job is observed directly, so a post-launch
	// Ready flip cannot spuriously fail a restore whose Job may have completed.
	// Only a definite NotFound means "not created yet"; any other Get error is
	// undecidable, so requeue rather than re-gate readiness on it.
	existingRestoreJob := &batchv1.Job{}
	switch err := r.Get(ctx, client.ObjectKey{Namespace: targetNamespace, Name: jobNameForRestoreJob(restoreJob)}, existingRestoreJob); {
	case err == nil:
		// Job exists; skip the readiness re-gate and observe it below.
	case apierrors.IsNotFound(err):
		if proceed, reason, message := redisAppReady(app); !proceed {
			if redisBackupDeadlineExceeded(restoreJob.Status.StartedAt) {
				return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf(
					"target Redis application %s/%s not ready within %s (%s: %s)",
					targetNamespace, targetAppName, redisDefaultBackupDeadline, reason, message))
			}
			apimeta.SetStatusCondition(&restoreJob.Status.Conditions, metav1.Condition{
				Type:    "Ready",
				Status:  metav1.ConditionFalse,
				Reason:  "ApplicationNotReady",
				Message: fmt.Sprintf("target Redis application %s/%s is not ready (%s: %s)", targetNamespace, targetAppName, reason, message),
			})
			if updateErr := r.Status().Update(ctx, restoreJob); updateErr != nil {
				return ctrl.Result{}, updateErr
			}
			return ctrl.Result{RequeueAfter: redisPollInterval}, nil
		}
	default:
		return ctrl.Result{}, err
	}

	rendered, err := renderRedisTemplate(
		strategy.Spec.Template,
		app,
		targetAppName,
		targetNamespace,
		redisModeRestore,
		redisRestoreObjectKey(backup),
		redisStrategyParameters(backup),
		backup,
	)
	if err != nil {
		return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf("failed to template Redis strategy: %v", err))
	}

	batchJob, err := r.ensureRedisRestoreJob(ctx, restoreJob, targetNamespace, jobNameForRestoreJob(restoreJob),
		redisModeRestore,
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
			Message: "redis restore Job completed",
		})
		if err := r.Status().Update(ctx, restoreJob); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil

	case batchv1.JobFailed:
		message := jobFailureMessage(batchJob)
		if message == "" {
			message = "redis restore Job reported Failed"
		}
		return r.markRestoreJobFailed(ctx, restoreJob, message)

	default:
		return ctrl.Result{RequeueAfter: redisPollInterval}, nil
	}
}

// ensureRedisRestoreJob is the RestoreJob-side mirror of ensureRedisJob. The Job
// lives in the RestoreJob's namespace and gets a controllerRef on the
// RestoreJob. Mirrors ensureAltinityRestoreJob.
func (r *RestoreJobReconciler) ensureRedisRestoreJob(
	ctx context.Context,
	owner client.Object,
	namespace, name, mode string,
	ownerLabels map[string]string,
	rendered *corev1.PodTemplateSpec,
) (*batchv1.Job, error) {
	labels := map[string]string{redisLabelMode: mode}
	for k, v := range ownerLabels {
		labels[k] = v
	}
	return ensureBackupBatchJob(ctx, r.Client, r.Scheme, owner, namespace, name, labels, rendered)
}

// ---------------------------------------------------------------------------
// Backup deletion cleanup
// ---------------------------------------------------------------------------

// cleanupRedisBackup deletes the object a Backup names when the Backup is
// removed. Unlike the operator-backed drivers, the Redis driver OWNS its
// artifact - redisObjectKey mints one object per BackupJob and no engine
// retention prunes it - so without this a retention-pruned Plan would grow the
// bucket unboundedly and the object's key would be lost with the CR. It spawns
// a one-shot cleanup-mode Job (which never contacts Redis, only S3) and WAITS
// for it before releasing the finalizer, so no object is orphaned. Mirrors
// cleanupRabbitmqBackup; the object key comes from the Backup's DriverMetadata
// rather than a status.artifact.URI.
func (r *BackupReconciler) cleanupRedisBackup(ctx context.Context, backup *backupsv1alpha1.Backup) (ctrl.Result, error) {
	logger := getLogger(ctx)

	// Escape hatch: release a Backup stuck Terminating (bucket unreachable) by
	// setting the annotation. Reap any in-flight delete Job and let it go,
	// leaving the object in the bucket.
	if backup.Annotations[redisSkipArtifactCleanupAnnotation] == "true" {
		existing := &batchv1.Job{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: backup.Namespace, Name: backup.Name + "-cleanup"}, existing); err == nil {
			_ = r.deleteRedisCleanupJob(ctx, existing)
		}
		logger.Debug("skipping Redis artifact cleanup per annotation; object left in bucket", "backup", backup.Name, "annotation", redisSkipArtifactCleanupAnnotation)
		return ctrl.Result{}, nil
	}

	objectKey := backup.Spec.DriverMetadata[redisObjectMetaKey]
	if objectKey == "" {
		// A Backup written before object keys were recorded names no object to
		// delete; nothing to do.
		return ctrl.Result{}, nil
	}

	strategy := &strategyv1alpha1.Redis{}
	if err := r.Get(ctx, client.ObjectKey{Name: backup.Spec.StrategyRef.Name}, strategy); err != nil {
		if apierrors.IsNotFound(err) {
			// No strategy to render the delete Job from (the shipped one is gated
			// on a resolved bucket name and stops rendering if that lookup fails).
			// Release rather than wedge the Backup forever; the object is left.
			return r.releaseRedisCleanup(ctx, backup, objectKey, "strategy CR is gone"), nil
		}
		return ctrl.Result{}, err
	}

	jobName := backup.Name + "-cleanup"
	job := &batchv1.Job{}
	err := r.Get(ctx, types.NamespacedName{Namespace: backup.Namespace, Name: jobName}, job)
	switch {
	case apierrors.IsNotFound(err):
		// Spawning the delete Job and projecting the credentials Secret it needs
		// are CREATEs, which NamespaceLifecycle admission forbids in a Terminating
		// namespace. When the namespace is going away, release the Backup so
		// teardown can finish; the object cannot be deleted from a namespace that
		// no longer exists, so it is left behind.
		terminating, nsErr := r.namespaceTerminating(ctx, backup.Namespace)
		if nsErr != nil {
			return ctrl.Result{}, nsErr
		}
		if terminating {
			return r.releaseRedisCleanup(ctx, backup, objectKey, "namespace is terminating"), nil
		}
		if perr := ProjectBackupCredentials(ctx, r.Client, r.CredentialsConfig, backup.Namespace); perr != nil {
			return r.releaseRedisCleanup(ctx, backup, objectKey, fmt.Sprintf("cannot project credentials: %v", perr)), nil
		}
		// Cleanup mode reads only .Release, .Mode and .ObjectKey (never
		// .Application), so the source app being already gone does not matter.
		rendered, rerr := renderRedisTemplate(
			strategy.Spec.Template,
			nil,
			backup.Spec.ApplicationRef.Name,
			backup.Namespace,
			redisModeCleanup,
			objectKey,
			redisStrategyParameters(backup),
			nil,
		)
		if rerr != nil {
			return r.releaseRedisCleanup(ctx, backup, objectKey, fmt.Sprintf("cleanup template render failed: %v", rerr)), nil
		}
		if cerr := r.Create(ctx, buildRedisCleanupJob(backup.Namespace, jobName, rendered)); cerr != nil && !apierrors.IsAlreadyExists(cerr) {
			// Forbidden (namespace went Terminating after the check) or Invalid
			// (a >55-char Backup name makes <name>-cleanup exceed 63 chars) cannot
			// be fixed by retrying; release. Other errors are transient - requeue.
			if apierrors.IsForbidden(cerr) || apierrors.IsInvalid(cerr) {
				return r.releaseRedisCleanup(ctx, backup, objectKey, fmt.Sprintf("cannot create cleanup Job: %v", cerr)), nil
			}
			return ctrl.Result{}, cerr
		}
		return ctrl.Result{RequeueAfter: redisPollInterval}, nil
	case err != nil:
		return ctrl.Result{}, err
	}

	if !job.DeletionTimestamp.IsZero() {
		// A prior failed attempt is being collected; wait, then the NotFound
		// branch recreates a fresh one.
		return ctrl.Result{RequeueAfter: redisPollInterval}, nil
	}

	switch jobConditionState(job) {
	case batchv1.JobComplete:
		// Object deleted (a missing object is success in the script): drop the
		// Job and let the Backup go.
		_ = r.deleteRedisCleanupJob(ctx, job)
		logger.Debug("Redis backup object deleted", "backup", backup.Name, "object", objectKey)
		return ctrl.Result{}, nil
	case batchv1.JobFailed:
		// The delete genuinely failed. Collect the failed Job so the NotFound
		// branch recreates a fresh one, and keep the Backup Terminating.
		logger.Debug("Redis cleanup Job failed; retrying", "backup", backup.Name, "job", jobName)
		_ = r.deleteRedisCleanupJob(ctx, job)
		return ctrl.Result{RequeueAfter: redisPollInterval}, nil
	default:
		return ctrl.Result{RequeueAfter: redisPollInterval}, nil
	}
}

// deleteRedisCleanupJob removes a cleanup Job and its Pod. Mirrors
// deleteRabbitmqCleanupJob.
func (r *BackupReconciler) deleteRedisCleanupJob(ctx context.Context, job *batchv1.Job) error {
	policy := metav1.DeletePropagationBackground
	return client.IgnoreNotFound(r.Delete(ctx, job, &client.DeleteOptions{PropagationPolicy: &policy}))
}

// releaseRedisCleanup gives up deleting the object and lets the Backup be
// removed, recording a Warning Event naming the object left behind so the
// give-up is visible. So the delete is best-effort, not guaranteed. Mirrors
// releaseRabbitmqCleanup.
func (r *BackupReconciler) releaseRedisCleanup(ctx context.Context, backup *backupsv1alpha1.Backup, objectKey, reason string) ctrl.Result {
	getLogger(ctx).Info("releasing Backup without deleting its object", "backup", backup.Name, "object", objectKey, "reason", reason)
	if r.Recorder != nil {
		r.Recorder.Eventf(backup, corev1.EventTypeWarning, "ArtifactNotDeleted", "left object %s in the bucket: %s", objectKey, reason)
	}
	return ctrl.Result{}
}

// buildRedisCleanupJob wraps the cleanup-mode pod in a one-shot, ownerless Job.
// Ownerless because cleanupRedisBackup manages its lifecycle explicitly and it
// must outlive the Backup being deleted; activeDeadlineSeconds + TTL are
// backstops if the controller stops mid-wait. Mirrors buildRabbitmqCleanupJob.
func buildRedisCleanupJob(namespace, name string, rendered *corev1.PodTemplateSpec) *batchv1.Job {
	pod := *rendered.DeepCopy()
	if pod.Spec.RestartPolicy == "" {
		pod.Spec.RestartPolicy = corev1.RestartPolicyNever
	}
	if pod.Labels == nil {
		pod.Labels = map[string]string{}
	}
	pod.Labels[redisLabelMode] = redisModeCleanup
	backoffLimit := int32(1)
	activeDeadline := int64(300)
	ttl := int32(300)
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
			Labels:    map[string]string{redisLabelMode: redisModeCleanup},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoffLimit,
			ActiveDeadlineSeconds:   &activeDeadline,
			TTLSecondsAfterFinished: &ttl,
			Template:                pod,
		},
	}
}
