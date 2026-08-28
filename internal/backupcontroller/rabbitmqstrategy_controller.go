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
	"github.com/cozystack/cozystack/internal/backupcontroller/rabbitmqtypes"
	"github.com/cozystack/cozystack/internal/template"
)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const (
	rabbitmqAppKind = "RabbitMQ"

	// rabbitmqReleasePrefix is the release.prefix the rabbitmq-rd
	// ApplicationDefinition prepends, so an application named <app> is served by
	// a rabbitmq.com/RabbitmqCluster (and its Service / default-user Secret)
	// named rabbitmq-<app>. The driver reads that cluster for its readiness
	// gate.
	rabbitmqReleasePrefix = "rabbitmq-"

	rabbitmqLabelMode   = "rabbitmq.strategy.backups.cozystack.io/mode"
	rabbitmqModeBackup  = "backup"
	rabbitmqModeRestore = "restore"
	rabbitmqModeCleanup = "cleanup"

	rabbitmqParamPrefix = "rabbitmq.strategy.backups.cozystack.io/parameter/"

	// rabbitmqSkipArtifactCleanupAnnotation, set to "true" on a Backup, is the
	// operator escape hatch: cleanup then releases the Backup without deleting
	// (or waiting to delete) its S3 object. It exists to unstick a Backup left
	// Terminating because object storage is unreachable - the object is left in
	// the bucket to be reclaimed manually or by a bucket lifecycle policy.
	rabbitmqSkipArtifactCleanupAnnotation = "backups.cozystack.io/skip-artifact-cleanup"

	rabbitmqPollInterval = 5 * time.Second

	// rabbitmqBackupDeadline bounds the AllReplicasReady precondition wait: past
	// it a BackupJob/RestoreJob whose broker never becomes ready fails with the
	// last not-ready message instead of requeuing forever in phase=Running.
	// Mirrors psmdbDefaultBackupDeadline.
	rabbitmqBackupDeadline = 30 * time.Minute
)

// rabbitmqBackupDeadlineExceeded reports whether the precondition wait has run
// past rabbitmqBackupDeadline since StartedAt. Mirrors psmdbBackupDeadlineExceeded.
func rabbitmqBackupDeadlineExceeded(startedAt *metav1.Time) bool {
	return startedAt != nil && time.Since(startedAt.Time) > rabbitmqBackupDeadline
}

// rabbitmqClusterName maps an apps.cozystack.io/RabbitMQ application name to the
// rabbitmq.com/RabbitmqCluster the operator creates for it.
func rabbitmqClusterName(appName string) string {
	return rabbitmqReleasePrefix + appName
}

// validateRabbitmqApplicationRef rejects ApplicationRefs that are not
// apps.cozystack.io/RabbitMQ, so the dispatcher cannot route a foreign ref into
// this driver. Empty APIGroup is accepted as the documented default, matching
// the other drivers and the BackupClass resolver.
func validateRabbitmqApplicationRef(ref corev1.TypedLocalObjectReference) error {
	if ref.Kind != rabbitmqAppKind {
		return fmt.Errorf("rabbitmq strategy supports applicationRef.kind=%q, got %q", rabbitmqAppKind, ref.Kind)
	}
	apiGroup := ""
	if ref.APIGroup != nil {
		apiGroup = *ref.APIGroup
	}
	if apiGroup != "" && apiGroup != backupsv1alpha1.DefaultApplicationAPIGroup {
		return fmt.Errorf("rabbitmq strategy supports applicationRef.apiGroup=%q, got %q", backupsv1alpha1.DefaultApplicationAPIGroup, apiGroup)
	}
	return nil
}

// rabbitmqBackupParameters round-trips BackupClassStrategy parameters through
// the Backup's DriverMetadata so a later RestoreJob re-renders the strategy
// template with the same values in effect at backup time.
func rabbitmqBackupParameters(b *backupsv1alpha1.Backup) map[string]string {
	out := map[string]string{}
	for k, v := range b.Spec.DriverMetadata {
		if !strings.HasPrefix(k, rabbitmqParamPrefix) {
			continue
		}
		paramKey := strings.TrimPrefix(k, rabbitmqParamPrefix)
		if paramKey == "" {
			continue
		}
		out[paramKey] = v
	}
	return out
}

// rabbitmqRenderContext builds the context every Rabbitmq strategy string field
// is rendered against: the live application object, the release shorthand, the
// run mode, the resolved parameters, and (restore only) the source Backup. The
// pod template and the artifact URI render against this one context.
func rabbitmqRenderContext(
	app map[string]interface{},
	releaseName, releaseNamespace, mode, backupName, artifactURI string,
	parameters map[string]string,
	backup *backupsv1alpha1.Backup,
) map[string]any {
	ctxMap := map[string]interface{}{
		"Application": app,
		"Release": map[string]string{
			"Name":      releaseName,
			"Namespace": releaseNamespace,
		},
		"Mode": mode,
		// BackupName is the per-run identity used to scope the object key so
		// distinct backups of one application do not overwrite each other. On
		// backup it is the BackupJob name (which the produced Backup is named
		// after); on restore it is the source Backup name.
		"BackupName": backupName,
		// ArtifactURI is the single source of truth for the stored object's
		// location. On backup it is the rendered artifactURITemplate (also
		// recorded on the Backup); on restore it is that recorded value read
		// back from the Backup, so restore fetches the exact object the backup
		// wrote and a later change to the key layout cannot orphan old backups.
		"ArtifactURI": artifactURI,
		"Parameters":  parameters,
	}
	if backup != nil {
		// Expose ApplicationRef so a to-copy restore can address the SOURCE
		// release's object key even when restoring into a differently-named
		// target. Mirrors renderAltinityTemplate's rationale.
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
	return ctxMap
}

func renderRabbitmqTemplate(tmpl corev1.PodTemplateSpec, renderCtx map[string]any) (*corev1.PodTemplateSpec, error) {
	return template.Template(&tmpl, renderCtx)
}

// renderRabbitmqArtifactURI renders the strategy's artifact URI template,
// surfacing template errors so a broken URI fails the backup rather than
// recording a half-rendered location.
func renderRabbitmqArtifactURI(tmplStr string, renderCtx map[string]any) (string, error) {
	uri, err := template.String(tmplStr, renderCtx)
	if err != nil {
		return "", fmt.Errorf("artifact URI template: %w", err)
	}
	return strings.TrimSpace(uri), nil
}

// rabbitmqNotReadyMessage reports why the RabbitmqCluster is not ready to
// service a definitions export/import, or "" when it is. The cluster-operator
// only serves the management API once every replica is up, so the driver gates
// on AllReplicasReady and surfaces a precise Ready=False message instead of a
// Job that starts and fails against an unreachable API.
func rabbitmqNotReadyMessage(cluster *rabbitmqtypes.RabbitmqCluster) string {
	for _, c := range cluster.Status.Conditions {
		if c.Type != rabbitmqtypes.ConditionAllReplicasReady {
			continue
		}
		if c.Status == metav1.ConditionTrue {
			return ""
		}
		msg := fmt.Sprintf("RabbitmqCluster %s is %s=%s", cluster.Name, c.Type, c.Status)
		if c.Message != "" {
			msg += ": " + c.Message
		}
		return msg
	}
	return fmt.Sprintf("RabbitmqCluster %s has no %s condition yet", cluster.Name, rabbitmqtypes.ConditionAllReplicasReady)
}

// ---------------------------------------------------------------------------
// BackupJob path
// ---------------------------------------------------------------------------

func (r *BackupJobReconciler) reconcileRabbitmq(ctx context.Context, j *backupsv1alpha1.BackupJob, resolved *ResolvedBackupConfig) (ctrl.Result, error) {
	logger := getLogger(ctx)
	logger.Debug("reconciling Rabbitmq strategy", "backupjob", j.Name, "phase", j.Status.Phase)

	if j.Status.Phase == backupsv1alpha1.BackupJobPhaseSucceeded ||
		j.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
		return ctrl.Result{}, nil
	}

	if err := validateRabbitmqApplicationRef(j.Spec.ApplicationRef); err != nil {
		return r.markBackupJobFailed(ctx, j, err.Error())
	}

	// First-reconcile bookkeeping. Refetch before writing StartedAt so a stale
	// informer cache cannot slide the timestamp forward across reconciles.
	// Mirrors reconcileAltinity / reconcileCNPG.
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
			return ctrl.Result{RequeueAfter: rabbitmqPollInterval}, nil
		}
	}

	strategy := &strategyv1alpha1.Rabbitmq{}
	if err := r.Get(ctx, client.ObjectKey{Name: resolved.StrategyRef.Name}, strategy); err != nil {
		if apierrors.IsNotFound(err) {
			return r.requeueStrategyNotReady(ctx, j, resolved.StrategyRef.Name)
		}
		return ctrl.Result{}, err
	}

	app, err := r.getApplicationUnstructured(ctx, j.Namespace, j.Spec.ApplicationRef)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.markBackupJobFailed(ctx, j, fmt.Sprintf("RabbitMQ application not found: %s/%s", j.Namespace, j.Spec.ApplicationRef.Name))
		}
		return ctrl.Result{}, err
	}

	// Precondition: defer until the RabbitmqCluster reports every replica ready,
	// so an export against an unreachable management API surfaces a precise
	// Ready=False wait instead of a Job that starts and fails.
	cluster := &rabbitmqtypes.RabbitmqCluster{}
	clusterName := rabbitmqClusterName(j.Spec.ApplicationRef.Name)
	notReady := ""
	if err := r.Get(ctx, client.ObjectKey{Namespace: j.Namespace, Name: clusterName}, cluster); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		notReady = fmt.Sprintf("RabbitmqCluster %s/%s not found yet", j.Namespace, clusterName)
	} else {
		notReady = rabbitmqNotReadyMessage(cluster)
	}
	if notReady != "" {
		// Bounded: a broker that never reaches AllReplicasReady (PVC stuck
		// Pending, OOMKilled, undersized preset) must fail the BackupJob rather
		// than requeue in phase=Running forever.
		if rabbitmqBackupDeadlineExceeded(j.Status.StartedAt) {
			return r.markBackupJobFailed(ctx, j, fmt.Sprintf("RabbitmqCluster did not become ready within %s: %s", rabbitmqBackupDeadline, notReady))
		}
		return r.requeueRabbitmqBackupWaiting(ctx, j, "RabbitMQClusterNotReady", notReady)
	}

	// The artifact URI is the single source of truth for where the export is
	// stored: render it once, inject it into the Job (which writes exactly that
	// object), and record the same value on the Backup so restore reads it back
	// rather than reconstructing a key that a later layout change could orphan.
	// A strategy that stores backups must set artifactURITemplate.
	if strategy.Spec.ArtifactURITemplate == "" {
		return r.markBackupJobFailed(ctx, j, "Rabbitmq strategy has no spec.artifactURITemplate; nothing records where the export is stored")
	}
	artifactURI, err := renderRabbitmqArtifactURI(strategy.Spec.ArtifactURITemplate,
		rabbitmqRenderContext(app, j.Spec.ApplicationRef.Name, j.Namespace, rabbitmqModeBackup, j.Name, "", resolved.Parameters, nil))
	if err != nil {
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf("failed to render Rabbitmq strategy artifact URI: %v", err))
	}
	renderCtx := rabbitmqRenderContext(app, j.Spec.ApplicationRef.Name, j.Namespace, rabbitmqModeBackup, j.Name, artifactURI, resolved.Parameters, nil)
	rendered, err := renderRabbitmqTemplate(strategy.Spec.Template, renderCtx)
	if err != nil {
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf("failed to template Rabbitmq strategy: %v", err))
	}

	batchJob, err := r.ensureRabbitmqJob(ctx, j, j.Namespace, jobNameForBackupJob(j),
		rabbitmqModeBackup,
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
		artifact, err := r.createRabbitmqBackupArtifact(ctx, j, resolved, artifactURI)
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
			Message: "RabbitMQ definitions backup Job completed",
		})
		if err := r.Status().Update(ctx, j); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil

	case batchv1.JobFailed:
		message := jobFailureMessage(batchJob)
		if message == "" {
			message = "RabbitMQ definitions backup Job reported Failed"
		}
		return r.markBackupJobFailed(ctx, j, message)

	default:
		return ctrl.Result{RequeueAfter: rabbitmqPollInterval}, nil
	}
}

// requeueRabbitmqBackupWaiting records a transient Ready=False condition on the
// BackupJob and requeues, so an unmet cluster-readiness precondition surfaces a
// precise wait instead of a Job that starts and fails. Mirrors
// requeueMongoDBBackupWaiting.
func (r *BackupJobReconciler) requeueRabbitmqBackupWaiting(ctx context.Context, j *backupsv1alpha1.BackupJob, reason, message string) (ctrl.Result, error) {
	apimeta.SetStatusCondition(&j.Status.Conditions, metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionFalse,
		Reason:  reason,
		Message: message,
	})
	if err := r.Status().Update(ctx, j); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: rabbitmqPollInterval}, nil
}

// ensureRabbitmqJob materialises a batch/v1.Job from the rendered
// PodTemplateSpec, idempotently and with a controllerRef so kube-gc collects it
// with the owning BackupJob. Mirrors ensureAltinityJob.
func (r *BackupJobReconciler) ensureRabbitmqJob(
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

	labels := map[string]string{rabbitmqLabelMode: mode}
	for k, v := range ownerLabels {
		labels[k] = v
	}

	desired := buildRabbitmqBatchJob(namespace, name, labels, rendered)
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

// ensureRabbitmqRestoreJob is the RestoreJob-side mirror of ensureRabbitmqJob.
func (r *RestoreJobReconciler) ensureRabbitmqRestoreJob(
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

	labels := map[string]string{rabbitmqLabelMode: mode}
	for k, v := range ownerLabels {
		labels[k] = v
	}

	desired := buildRabbitmqBatchJob(namespace, name, labels, rendered)
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

// buildRabbitmqBatchJob wraps the rendered PodTemplateSpec in a one-shot Job.
// RestartPolicyNever and a small backoff cap match a dump-style backup - the
// export/import either succeeds or fails. Mirrors buildAltinityBatchJob.
func buildRabbitmqBatchJob(namespace, name string, labels map[string]string, rendered *corev1.PodTemplateSpec) *batchv1.Job {
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

// createRabbitmqBackupArtifact materialises a Cozystack Backup carrying the
// strategy reference, the parameter snapshot (via DriverMetadata) and, when the
// strategy renders one, the artifact URI. The Backup CRD has no status
// subresource, so status.artifact persists on Create (as in createEtcdBackupArtifact).
func (r *BackupJobReconciler) createRabbitmqBackupArtifact(
	ctx context.Context,
	j *backupsv1alpha1.BackupJob,
	resolved *ResolvedBackupConfig,
	artifactURI string,
) (*backupsv1alpha1.Backup, error) {
	driverMD := map[string]string{}
	for k, v := range resolved.Parameters {
		driverMD[rabbitmqParamPrefix+k] = v
	}

	status := backupsv1alpha1.BackupStatus{
		Phase: backupsv1alpha1.BackupPhaseReady,
	}
	if artifactURI != "" {
		status.Artifact = &backupsv1alpha1.BackupArtifact{URI: artifactURI}
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
		Status: status,
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
		// Adopt an existing Backup only when it describes the same application.
		// A retained Backup whose BackupJob was reaped, then reused for a
		// different application of the same name, would otherwise be reported as
		// this run's success while describing something else.
		if existing.Spec.ApplicationRef.Kind != backup.Spec.ApplicationRef.Kind ||
			existing.Spec.ApplicationRef.Name != backup.Spec.ApplicationRef.Name {
			return nil, fmt.Errorf("Backup %s/%s already exists for a different application (%s/%s, not %s/%s)",
				backup.Namespace, backup.Name,
				existing.Spec.ApplicationRef.Kind, existing.Spec.ApplicationRef.Name,
				backup.Spec.ApplicationRef.Kind, backup.Spec.ApplicationRef.Name)
		}
		return existing, nil
	}
	return backup, nil
}

// ---------------------------------------------------------------------------
// RestoreJob path
// ---------------------------------------------------------------------------

func (r *RestoreJobReconciler) reconcileRabbitmqRestore(ctx context.Context, restoreJob *backupsv1alpha1.RestoreJob, backup *backupsv1alpha1.Backup) (ctrl.Result, error) {
	logger := getLogger(ctx)
	logger.Debug("reconciling Rabbitmq restore", "restorejob", restoreJob.Name, "backup", backup.Name)

	if restoreJob.Status.Phase == backupsv1alpha1.RestoreJobPhaseSucceeded ||
		restoreJob.Status.Phase == backupsv1alpha1.RestoreJobPhaseFailed {
		return ctrl.Result{}, nil
	}

	if err := validateRabbitmqApplicationRef(backup.Spec.ApplicationRef); err != nil {
		return r.markRestoreJobFailed(ctx, restoreJob, err.Error())
	}

	// Restore reads the object the Backup recorded, not a reconstruction, so a
	// later change to the key layout cannot make older backups unrestorable.
	if backup.Status.Artifact == nil || backup.Status.Artifact.URI == "" {
		return r.markRestoreJobFailed(ctx, restoreJob, "Backup has no recorded artifact URI (status.artifact.uri); cannot locate the definitions object to restore")
	}
	artifactURI := backup.Status.Artifact.URI

	// Resolve the effective restore target. Defaults to the source application;
	// targetApplicationRef overrides for a to-copy restore into a differently
	// named RabbitMQ in the same namespace (TypedLocalObjectReference has no
	// namespace field, so cross-namespace restore is not representable).
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
	if err := validateRabbitmqApplicationRef(targetRef); err != nil {
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
			return ctrl.Result{RequeueAfter: rabbitmqPollInterval}, nil
		}
	}

	strategy := &strategyv1alpha1.Rabbitmq{}
	if err := r.Get(ctx, client.ObjectKey{Name: backup.Spec.StrategyRef.Name}, strategy); err != nil {
		if apierrors.IsNotFound(err) {
			return r.requeueRestoreStrategyNotReady(ctx, restoreJob, backup.Spec.StrategyRef.Name)
		}
		return ctrl.Result{}, err
	}

	app, err := r.getApplicationUnstructured(ctx, targetNamespace, targetRef)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf(
				"target RabbitMQ application not found: %s/%s (deploy it before requesting a copy restore)",
				targetNamespace, targetAppName))
		}
		return ctrl.Result{}, err
	}

	// Precondition: the TARGET broker must be ready to accept a definitions
	// import. Same gate as backup, on the restore target's cluster.
	cluster := &rabbitmqtypes.RabbitmqCluster{}
	clusterName := rabbitmqClusterName(targetAppName)
	notReady := ""
	if err := r.Get(ctx, client.ObjectKey{Namespace: targetNamespace, Name: clusterName}, cluster); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		notReady = fmt.Sprintf("target RabbitmqCluster %s/%s not found yet", targetNamespace, clusterName)
	} else {
		notReady = rabbitmqNotReadyMessage(cluster)
	}
	if notReady != "" {
		// Bounded, as on the backup path: a target broker that never becomes
		// ready fails the RestoreJob rather than requeuing forever.
		if rabbitmqBackupDeadlineExceeded(restoreJob.Status.StartedAt) {
			return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf("target RabbitmqCluster did not become ready within %s: %s", rabbitmqBackupDeadline, notReady))
		}
		return r.requeueRabbitmqRestoreWaiting(ctx, restoreJob, "RabbitMQClusterNotReady", notReady)
	}

	renderCtx := rabbitmqRenderContext(app, targetAppName, targetNamespace, rabbitmqModeRestore, backup.Name, artifactURI, rabbitmqBackupParameters(backup), backup)
	rendered, err := renderRabbitmqTemplate(strategy.Spec.Template, renderCtx)
	if err != nil {
		return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf("failed to template Rabbitmq strategy: %v", err))
	}

	batchJob, err := r.ensureRabbitmqRestoreJob(ctx, restoreJob, targetNamespace, jobNameForRestoreJob(restoreJob),
		rabbitmqModeRestore,
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
			Message: "RabbitMQ definitions restore Job completed",
		})
		if err := r.Status().Update(ctx, restoreJob); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil

	case batchv1.JobFailed:
		message := jobFailureMessage(batchJob)
		if message == "" {
			message = "RabbitMQ definitions restore Job reported Failed"
		}
		return r.markRestoreJobFailed(ctx, restoreJob, message)

	default:
		return ctrl.Result{RequeueAfter: rabbitmqPollInterval}, nil
	}
}

// requeueRabbitmqRestoreWaiting is the RestoreJob mirror of
// requeueRabbitmqBackupWaiting.
func (r *RestoreJobReconciler) requeueRabbitmqRestoreWaiting(ctx context.Context, restoreJob *backupsv1alpha1.RestoreJob, reason, message string) (ctrl.Result, error) {
	apimeta.SetStatusCondition(&restoreJob.Status.Conditions, metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionFalse,
		Reason:  reason,
		Message: message,
	})
	if err := r.Status().Update(ctx, restoreJob); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: rabbitmqPollInterval}, nil
}

// ---------------------------------------------------------------------------
// Backup cleanup path (best-effort object deletion on Backup delete)
// ---------------------------------------------------------------------------

// cleanupRabbitmqBackup deletes the definitions object a Backup recorded, and
// WAITS for that delete to finish before the Backup is removed, so no object is
// orphaned. This driver uniquely owns its artifact (a plain object it wrote,
// with the exact key on status.artifact.uri) and no engine retention prunes it.
// The controller has no S3 client, so the delete runs as a one-shot Job through
// the strategy's curl image; this returns a requeue (non-zero Result) until the
// Job succeeds. The script treats "object already gone" as success; a genuine
// failure is retried and the Backup stays Terminating - a visible signal an
// operator can act on - rather than the object being silently orphaned.
func (r *BackupReconciler) cleanupRabbitmqBackup(ctx context.Context, backup *backupsv1alpha1.Backup) (ctrl.Result, error) {
	logger := getLogger(ctx)

	// Escape hatch: an operator releases a Backup stuck Terminating (object
	// storage unreachable) by setting this annotation. Cleanup then skips the
	// delete - reaping any in-flight delete Job - and lets the Backup go,
	// leaving the object in the bucket.
	if backup.Annotations[rabbitmqSkipArtifactCleanupAnnotation] == "true" {
		existing := &batchv1.Job{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: backup.Namespace, Name: backup.Name + "-cleanup"}, existing); err == nil {
			_ = r.deleteRabbitmqCleanupJob(ctx, existing)
		}
		logger.Debug("skipping Rabbitmq artifact cleanup per annotation; object left in bucket", "backup", backup.Name, "annotation", rabbitmqSkipArtifactCleanupAnnotation)
		return ctrl.Result{}, nil
	}

	if backup.Status.Artifact == nil || backup.Status.Artifact.URI == "" {
		return ctrl.Result{}, nil
	}
	uri := backup.Status.Artifact.URI

	strategy := &strategyv1alpha1.Rabbitmq{}
	if err := r.Get(ctx, client.ObjectKey{Name: backup.Spec.StrategyRef.Name}, strategy); err != nil {
		if apierrors.IsNotFound(err) {
			// No strategy to render the delete Job from (rare: the shipped
			// strategy is normally always present, but it is gated on a resolved
			// bucket name and stops rendering if that lookup fails). Release the
			// Backup rather than wedge it forever; the object is left behind.
			return r.releaseRabbitmqCleanup(ctx, backup, uri, "strategy CR is gone"), nil
		}
		return ctrl.Result{}, err
	}

	jobName := backup.Name + "-cleanup"
	job := &batchv1.Job{}
	err := r.Get(ctx, types.NamespacedName{Namespace: backup.Namespace, Name: jobName}, job)
	switch {
	case apierrors.IsNotFound(err):
		// First pass (or a fresh retry after a failed attempt was collected).
		// Spawning the delete Job - and projecting the credentials Secret it
		// needs - are CREATEs, which NamespaceLifecycle admission forbids in a
		// Terminating namespace. When the namespace is going away, release the
		// Backup so teardown can finish; the object cannot be deleted from a
		// namespace that no longer exists, so it is left behind.
		terminating, nsErr := r.namespaceTerminating(ctx, backup.Namespace)
		if nsErr != nil {
			return ctrl.Result{}, nsErr
		}
		if terminating {
			return r.releaseRabbitmqCleanup(ctx, backup, uri, "namespace is terminating"), nil
		}
		if perr := ProjectBackupCredentials(ctx, r.Client, r.CredentialsConfig, backup.Namespace); perr != nil {
			// Cannot obtain credentials to run the delete (source Secret gone,
			// ownership guard tripped, ...). Release rather than wedge.
			return r.releaseRabbitmqCleanup(ctx, backup, uri, fmt.Sprintf("cannot project credentials: %v", perr)), nil
		}
		// The template reads only .Release, .Mode and .ArtifactURI in cleanup
		// mode (never .Application), so the source app being already gone does
		// not matter.
		renderCtx := rabbitmqRenderContext(nil, backup.Spec.ApplicationRef.Name, backup.Namespace, rabbitmqModeCleanup, backup.Name, uri, nil, nil)
		rendered, rerr := renderRabbitmqTemplate(strategy.Spec.Template, renderCtx)
		if rerr != nil {
			// A broken template cannot be fixed by retrying; release.
			return r.releaseRabbitmqCleanup(ctx, backup, uri, fmt.Sprintf("cleanup template render failed: %v", rerr)), nil
		}
		if cerr := r.Create(ctx, buildRabbitmqCleanupJob(backup.Namespace, jobName, rendered)); cerr != nil && !apierrors.IsAlreadyExists(cerr) {
			// Forbidden (the namespace went Terminating after the check above)
			// or Invalid (e.g. a >55-char Backup name makes <name>-cleanup
			// exceed the 63-char Job-name limit) cannot be fixed by retrying;
			// release rather than wedge. Other errors are transient - requeue.
			if apierrors.IsForbidden(cerr) || apierrors.IsInvalid(cerr) {
				return r.releaseRabbitmqCleanup(ctx, backup, uri, fmt.Sprintf("cannot create cleanup Job: %v", cerr)), nil
			}
			return ctrl.Result{}, cerr
		}
		return ctrl.Result{RequeueAfter: rabbitmqPollInterval}, nil
	case err != nil:
		return ctrl.Result{}, err
	}

	if !job.DeletionTimestamp.IsZero() {
		// A prior failed attempt is being collected; wait, then the NotFound
		// branch recreates a fresh one.
		return ctrl.Result{RequeueAfter: rabbitmqPollInterval}, nil
	}

	switch jobConditionState(job) {
	case batchv1.JobComplete:
		// Object deleted (or already gone): drop the Job and let the Backup go.
		_ = r.deleteRabbitmqCleanupJob(ctx, job)
		logger.Debug("Rabbitmq backup object deleted", "backup", backup.Name, "uri", uri)
		return ctrl.Result{}, nil
	case batchv1.JobFailed:
		// The delete genuinely failed (a missing object is success in the
		// script, not a failure). Collect the failed Job so the NotFound branch
		// recreates a fresh one, and keep the Backup Terminating.
		logger.Debug("Rabbitmq cleanup Job failed; retrying", "backup", backup.Name, "job", jobName)
		_ = r.deleteRabbitmqCleanupJob(ctx, job)
		return ctrl.Result{RequeueAfter: rabbitmqPollInterval}, nil
	default:
		return ctrl.Result{RequeueAfter: rabbitmqPollInterval}, nil
	}
}

// deleteRabbitmqCleanupJob removes a finished cleanup Job together with its Pod.
func (r *BackupReconciler) deleteRabbitmqCleanupJob(ctx context.Context, job *batchv1.Job) error {
	policy := metav1.DeletePropagationBackground
	return client.IgnoreNotFound(r.Delete(ctx, job, &client.DeleteOptions{PropagationPolicy: &policy}))
}

// namespaceTerminating reports whether the namespace is gone or carries a
// deletion timestamp - in which case the API server forbids CREATE (the delete
// Job, the projected credentials Secret), so cleanup must not attempt it and
// must instead release the Backup.
func (r *BackupReconciler) namespaceTerminating(ctx context.Context, name string) (bool, error) {
	ns := &corev1.Namespace{}
	if err := r.Get(ctx, client.ObjectKey{Name: name}, ns); err != nil {
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}
	return !ns.DeletionTimestamp.IsZero(), nil
}

// releaseRabbitmqCleanup gives up on deleting the backup object and lets the
// Backup be removed - for the conditions under which the delete cannot run here
// (namespace terminating, credentials/permissions unavailable, an unrenderable
// or absent strategy). It logs and records a Warning Event naming the object
// left behind so the give-up is visible, then returns a zero Result so the
// finalizer is released and the Backup (and any enclosing namespace) can finish
// deleting. So the delete is best-effort, not guaranteed.
func (r *BackupReconciler) releaseRabbitmqCleanup(ctx context.Context, backup *backupsv1alpha1.Backup, uri, reason string) ctrl.Result {
	getLogger(ctx).Info("releasing Backup without deleting its object", "backup", backup.Name, "uri", uri, "reason", reason)
	if r.Recorder != nil {
		r.Recorder.Eventf(backup, corev1.EventTypeWarning, "ArtifactNotDeleted", "left object %s in the bucket: %s", uri, reason)
	}
	return ctrl.Result{}
}

// buildRabbitmqCleanupJob wraps the cleanup-mode pod in a one-shot, ownerless
// Job. Ownerless because cleanupRabbitmqBackup manages its lifecycle explicitly
// (it deletes the Job on completion) and it must outlive the Backup being
// deleted; activeDeadlineSeconds + TTL are backstops if the controller stops
// mid-wait.
func buildRabbitmqCleanupJob(namespace, name string, rendered *corev1.PodTemplateSpec) *batchv1.Job {
	pod := *rendered.DeepCopy()
	if pod.Spec.RestartPolicy == "" {
		pod.Spec.RestartPolicy = corev1.RestartPolicyNever
	}
	if pod.Labels == nil {
		pod.Labels = map[string]string{}
	}
	pod.Labels[rabbitmqLabelMode] = rabbitmqModeCleanup
	backoffLimit := int32(1)
	activeDeadline := int64(300)
	ttl := int32(300)
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
			Labels:    map[string]string{rabbitmqLabelMode: rabbitmqModeCleanup},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoffLimit,
			ActiveDeadlineSeconds:   &activeDeadline,
			TTLSecondsAfterFinished: &ttl,
			Template:                pod,
		},
	}
}
