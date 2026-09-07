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
	"github.com/cozystack/cozystack/internal/backupcontroller/kafkatypes"
	"github.com/cozystack/cozystack/internal/template"
)

// The Kafka strategy is a typed variant of the generic Job strategy for the
// topic METADATA of a Cozystack Kafka application: the driver renders
// spec.template into a one-shot batch/v1.Job that talks to the Kafka Admin API
// (export topic definitions + configs on backup, recreate them on restore).
// It adds three things on top of the Job strategy's render-a-Job flow: an
// applicationRef Kind gate, a Ready precondition on the Strimzi Kafka cluster,
// and a per-run object key via .BackupName + ArtifactURITemplate.

const (
	kafkaStrategyLabelMode   = "kafka.strategy.backups.cozystack.io/mode"
	kafkaStrategyModeBackup  = "backup"
	kafkaStrategyModeRestore = "restore"
	kafkaStrategyModeCleanup = "cleanup"

	// kafkaSkipArtifactCleanupAnnotation, set to "true" on a Backup, releases it
	// without deleting (or waiting to delete) its S3 object - the operator escape
	// hatch for a Backup left Terminating because object storage is unreachable.
	// Shares the platform-wide key with the RabbitMQ driver's identical hatch.
	kafkaSkipArtifactCleanupAnnotation = "backups.cozystack.io/skip-artifact-cleanup"

	// Round-trips BackupClass parameters through the Backup's DriverMetadata so
	// a later RestoreJob re-renders with the backup-time values.
	kafkaStrategyParamPrefix = "kafka.strategy.backups.cozystack.io/parameter/"

	// Stamped on every produced Backup so `kubectl describe backup` and any
	// consumer make the narrow scope explicit: this strategy captures topic
	// definitions/configs, not message data, offsets, ACLs or users.
	kafkaScopeKey   = "strategy.backups.cozystack.io/scope"
	kafkaScopeValue = "topic-metadata"

	kafkaStrategyPollInterval = 5 * time.Second

	// Cap on the Ready-precondition wait so a cluster that never comes up fails
	// the BackupJob/RestoreJob with a legible message instead of requeuing
	// forever.
	kafkaDefaultBackupDeadline = 30 * time.Minute

	// The Cozystack chart names the Strimzi Kafka cluster "kafka-<app>".
	kafkaClusterNamePrefix = "kafka-"

	kafkaApplicationKind = "Kafka"
)

// kafkaClusterName maps an apps.cozystack.io/Kafka name to the Strimzi Kafka
// cluster CR name the chart creates.
func kafkaClusterName(appName string) string {
	return kafkaClusterNamePrefix + appName
}

// validateKafkaApplicationRef enforces that the driver only ever acts on an
// apps.cozystack.io/Kafka application.
func validateKafkaApplicationRef(ref corev1.TypedLocalObjectReference) error {
	if ref.Kind != kafkaApplicationKind {
		return fmt.Errorf("Kafka strategy only supports applicationRef.kind=%q, got %q", kafkaApplicationKind, ref.Kind)
	}
	if ref.APIGroup != nil && *ref.APIGroup != "" && *ref.APIGroup != backupsv1alpha1.DefaultApplicationAPIGroup {
		return fmt.Errorf("Kafka strategy only supports applicationRef.apiGroup=%q, got %q", backupsv1alpha1.DefaultApplicationAPIGroup, *ref.APIGroup)
	}
	return nil
}

// kafkaNotReadyMessage returns "" when the cluster reports Ready=True, otherwise
// a legible reason for the BackupJob/RestoreJob precondition.
func kafkaNotReadyMessage(cluster *kafkatypes.Kafka) string {
	cond := apimeta.FindStatusCondition(cluster.Status.Conditions, kafkatypes.ConditionTypeReady)
	if cond == nil {
		return fmt.Sprintf("Kafka cluster %s has no Ready condition yet", cluster.Name)
	}
	if cond.Status == metav1.ConditionTrue {
		return ""
	}
	msg := fmt.Sprintf("Kafka cluster %s is Ready=%s", cluster.Name, cond.Status)
	if cond.Message != "" {
		msg += ": " + cond.Message
	}
	return msg
}

// kafkaStrategyParameters extracts the round-tripped BackupClass parameters from
// a Backup's DriverMetadata. Mirrors jobStrategyParameters.
func kafkaStrategyParameters(b *backupsv1alpha1.Backup) map[string]string {
	out := map[string]string{}
	for k, v := range b.Spec.DriverMetadata {
		if !strings.HasPrefix(k, kafkaStrategyParamPrefix) {
			continue
		}
		paramKey := strings.TrimPrefix(k, kafkaStrategyParamPrefix)
		if paramKey == "" {
			continue
		}
		out[paramKey] = v
	}
	return out
}

// kafkaRenderContext builds the template context. Unlike the Job strategy it
// carries .BackupName (the per-run identity) so the strategy can scope the S3
// object key and a restore reads the exact object its Backup wrote.
func kafkaRenderContext(
	app map[string]interface{},
	releaseName, releaseNamespace, mode, backupName, artifactURI string,
	parameters map[string]string,
	backup *backupsv1alpha1.Backup,
) map[string]any {
	ctxMap := map[string]any{
		"Application": app,
		"Release": map[string]string{
			"Name":      releaseName,
			"Namespace": releaseNamespace,
		},
		"Mode":       mode,
		"BackupName": backupName,
		// ArtifactURI is the single source of truth for the stored object's
		// location. On backup it is the rendered artifactURITemplate (also
		// recorded on the Backup); on restore and cleanup it is that recorded
		// value read back from the Backup, so both act on the exact object the
		// backup wrote and a later key-layout change cannot orphan old backups.
		"ArtifactURI": artifactURI,
		"Parameters":  parameters,
	}
	if backup != nil {
		sourceAPIGroup := ""
		if backup.Spec.ApplicationRef.APIGroup != nil {
			sourceAPIGroup = *backup.Spec.ApplicationRef.APIGroup
		}
		ctxMap["Backup"] = map[string]any{
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

func renderKafkaTemplate(tmpl corev1.PodTemplateSpec, ctxMap map[string]any) (*corev1.PodTemplateSpec, error) {
	return template.Template(&tmpl, ctxMap)
}

// renderKafkaArtifactURI renders the strategy's ArtifactURITemplate against the
// same context, surfacing template errors so a broken URI fails the backup
// rather than recording a half-rendered location.
func renderKafkaArtifactURI(tmplStr string, ctxMap map[string]any) (string, error) {
	uri, err := template.String(tmplStr, ctxMap)
	if err != nil {
		return "", fmt.Errorf("artifact URI template: %w", err)
	}
	return strings.TrimSpace(uri), nil
}

// ---------------------------------------------------------------------------
// BackupJob path
// ---------------------------------------------------------------------------

func (r *BackupJobReconciler) reconcileKafka(ctx context.Context, j *backupsv1alpha1.BackupJob, resolved *ResolvedBackupConfig) (ctrl.Result, error) {
	logger := getLogger(ctx)
	logger.Debug("reconciling Kafka strategy", "backupjob", j.Name, "phase", j.Status.Phase)

	if j.Status.Phase == backupsv1alpha1.BackupJobPhaseSucceeded ||
		j.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
		return ctrl.Result{}, nil
	}

	if err := validateKafkaApplicationRef(j.Spec.ApplicationRef); err != nil {
		return r.markBackupJobFailed(ctx, j, err.Error())
	}

	// First-reconcile bookkeeping (refetch guards against a stale informer
	// sliding StartedAt forward). Mirrors reconcileJob.
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
			return ctrl.Result{RequeueAfter: kafkaStrategyPollInterval}, nil
		}
	}

	strategy := &strategyv1alpha1.Kafka{}
	if err := r.Get(ctx, client.ObjectKey{Name: resolved.StrategyRef.Name}, strategy); err != nil {
		if apierrors.IsNotFound(err) {
			return r.requeueStrategyNotReady(ctx, j, resolved.StrategyRef.Name)
		}
		return ctrl.Result{}, err
	}

	app, err := r.getApplicationUnstructured(ctx, j.Namespace, j.Spec.ApplicationRef)
	if err != nil {
		if apierrors.IsNotFound(err) || apimeta.IsNoMatchError(err) {
			return r.markBackupJobFailed(ctx, j, fmt.Sprintf("application not found or kind not registered: %s/%s (kind=%q)", j.Namespace, j.Spec.ApplicationRef.Name, j.Spec.ApplicationRef.Kind))
		}
		return ctrl.Result{}, err
	}

	// Ready precondition on the Strimzi Kafka cluster: the Admin API is only
	// reachable once it is up. Deadline-gate the wait so a cluster that never
	// becomes Ready fails legibly.
	appName := j.Spec.ApplicationRef.Name
	cluster := &kafkatypes.Kafka{}
	clusterErr := r.Get(ctx, types.NamespacedName{Namespace: j.Namespace, Name: kafkaClusterName(appName)}, cluster)
	if clusterErr != nil {
		if !apierrors.IsNotFound(clusterErr) {
			return ctrl.Result{}, clusterErr
		}
		return r.kafkaBackupNotReady(ctx, j, "KafkaClusterNotReady",
			fmt.Sprintf("Kafka cluster %s not found yet", kafkaClusterName(appName)))
	}
	if msg := kafkaNotReadyMessage(cluster); msg != "" {
		return r.kafkaBackupNotReady(ctx, j, "KafkaClusterNotReady", msg)
	}

	// The artifact URI is the single source of truth for where the export is
	// stored: render it once, inject it into the Job (which writes exactly that
	// object), and record the same value on the Backup so restore/cleanup act on
	// it rather than reconstructing a key a later layout change could orphan.
	if strategy.Spec.ArtifactURITemplate == "" {
		return r.markBackupJobFailed(ctx, j, "Kafka strategy has no spec.artifactURITemplate; nothing records where the export is stored")
	}
	artifactURI, err := renderKafkaArtifactURI(strategy.Spec.ArtifactURITemplate,
		kafkaRenderContext(app, appName, j.Namespace, kafkaStrategyModeBackup, j.Name, "", resolved.Parameters, nil))
	if err != nil {
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf("failed to render Kafka strategy artifact URI: %v", err))
	}
	ctxMap := kafkaRenderContext(app, appName, j.Namespace, kafkaStrategyModeBackup, j.Name, artifactURI, resolved.Parameters, nil)
	rendered, err := renderKafkaTemplate(strategy.Spec.Template, ctxMap)
	if err != nil {
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf("failed to template Kafka strategy: %v", err))
	}

	batchJob, err := r.ensureKafkaJob(ctx, j, j.Namespace, jobNameForBackupJob(j),
		kafkaStrategyModeBackup,
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
		artifact, err := r.createKafkaBackupArtifact(ctx, j, resolved, artifactURI)
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
			Message: "Kafka topic-metadata backup completed (topic definitions and configs only; no message data, offsets, ACLs or users)",
		})
		if err := r.Status().Update(ctx, j); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil

	case batchv1.JobFailed:
		message := jobFailureMessage(batchJob)
		if message == "" {
			message = "Kafka metadata backup Job reported Failed"
		}
		return r.markBackupJobFailed(ctx, j, message)

	default:
		return ctrl.Result{RequeueAfter: kafkaStrategyPollInterval}, nil
	}
}

// kafkaBackupNotReady surfaces a precise Ready=False on the BackupJob and
// requeues, or fails once the deadline since StartedAt is exceeded.
func (r *BackupJobReconciler) kafkaBackupNotReady(ctx context.Context, j *backupsv1alpha1.BackupJob, reason, message string) (ctrl.Result, error) {
	if j.Status.StartedAt != nil && time.Since(j.Status.StartedAt.Time) > kafkaDefaultBackupDeadline {
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf("timed out waiting for Kafka cluster to become Ready: %s", message))
	}
	apimeta.SetStatusCondition(&j.Status.Conditions, metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionFalse,
		Reason:  reason,
		Message: message,
	})
	if err := r.Status().Update(ctx, j); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: kafkaStrategyPollInterval}, nil
}

func (r *BackupJobReconciler) ensureKafkaJob(
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

	labels := map[string]string{kafkaStrategyLabelMode: mode}
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

func (r *BackupJobReconciler) createKafkaBackupArtifact(
	ctx context.Context,
	j *backupsv1alpha1.BackupJob,
	resolved *ResolvedBackupConfig,
	artifactURI string,
) (*backupsv1alpha1.Backup, error) {
	driverMD := map[string]string{kafkaScopeKey: kafkaScopeValue}
	for k, v := range resolved.Parameters {
		driverMD[kafkaStrategyParamPrefix+k] = v
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
	if artifactURI != "" {
		backup.Status.Artifact = &backupsv1alpha1.BackupArtifact{URI: artifactURI}
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
		// this run's success while describing something else (mirrors the
		// RabbitMQ driver's guard).
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

func (r *RestoreJobReconciler) reconcileKafkaRestore(ctx context.Context, restoreJob *backupsv1alpha1.RestoreJob, backup *backupsv1alpha1.Backup) (ctrl.Result, error) {
	logger := getLogger(ctx)
	logger.Debug("reconciling Kafka restore", "restorejob", restoreJob.Name, "backup", backup.Name)

	if restoreJob.Status.Phase == backupsv1alpha1.RestoreJobPhaseSucceeded ||
		restoreJob.Status.Phase == backupsv1alpha1.RestoreJobPhaseFailed {
		return ctrl.Result{}, nil
	}

	if err := validateKafkaApplicationRef(backup.Spec.ApplicationRef); err != nil {
		return r.markRestoreJobFailed(ctx, restoreJob, err.Error())
	}

	// Resolve the effective target: source app by default, overridden per-field
	// by targetApplicationRef for a to-copy restore.
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
	if err := validateKafkaApplicationRef(targetRef); err != nil {
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
			return ctrl.Result{RequeueAfter: kafkaStrategyPollInterval}, nil
		}
	}

	strategy := &strategyv1alpha1.Kafka{}
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
				"target Kafka application not found or kind not registered: %s/%s (kind=%q; deploy it before requesting a restore)",
				targetNamespace, targetAppName, targetAppKind))
		}
		return ctrl.Result{}, err
	}

	// Ready precondition on the TARGET cluster.
	cluster := &kafkatypes.Kafka{}
	clusterErr := r.Get(ctx, types.NamespacedName{Namespace: targetNamespace, Name: kafkaClusterName(targetAppName)}, cluster)
	if clusterErr != nil {
		if !apierrors.IsNotFound(clusterErr) {
			return ctrl.Result{}, clusterErr
		}
		return r.kafkaRestoreNotReady(ctx, restoreJob, "KafkaClusterNotReady",
			fmt.Sprintf("target Kafka cluster %s not found yet", kafkaClusterName(targetAppName)))
	}
	if msg := kafkaNotReadyMessage(cluster); msg != "" {
		return r.kafkaRestoreNotReady(ctx, restoreJob, "KafkaClusterNotReady", msg)
	}

	// Restore reads the object the Backup recorded, not a reconstruction, so a
	// later change to the key layout cannot make older backups unrestorable.
	if backup.Status.Artifact == nil || backup.Status.Artifact.URI == "" {
		return r.markRestoreJobFailed(ctx, restoreJob, "Backup has no recorded artifact URI (status.artifact.uri); cannot locate the metadata object to restore")
	}
	artifactURI := backup.Status.Artifact.URI
	ctxMap := kafkaRenderContext(app, targetAppName, targetNamespace, kafkaStrategyModeRestore, backup.Name, artifactURI, kafkaStrategyParameters(backup), backup)
	rendered, err := renderKafkaTemplate(strategy.Spec.Template, ctxMap)
	if err != nil {
		return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf("failed to template Kafka strategy: %v", err))
	}

	batchJob, err := r.ensureKafkaRestoreJob(ctx, restoreJob, targetNamespace, jobNameForRestoreJob(restoreJob),
		kafkaStrategyModeRestore,
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
			Message: "Kafka topic-metadata restore completed (topic definitions and configs only)",
		})
		if err := r.Status().Update(ctx, restoreJob); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil

	case batchv1.JobFailed:
		message := jobFailureMessage(batchJob)
		if message == "" {
			message = "Kafka metadata restore Job reported Failed"
		}
		return r.markRestoreJobFailed(ctx, restoreJob, message)

	default:
		return ctrl.Result{RequeueAfter: kafkaStrategyPollInterval}, nil
	}
}

func (r *RestoreJobReconciler) kafkaRestoreNotReady(ctx context.Context, rj *backupsv1alpha1.RestoreJob, reason, message string) (ctrl.Result, error) {
	if rj.Status.StartedAt != nil && time.Since(rj.Status.StartedAt.Time) > kafkaDefaultBackupDeadline {
		return r.markRestoreJobFailed(ctx, rj, fmt.Sprintf("timed out waiting for Kafka cluster to become Ready: %s", message))
	}
	apimeta.SetStatusCondition(&rj.Status.Conditions, metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionFalse,
		Reason:  reason,
		Message: message,
	})
	if err := r.Status().Update(ctx, rj); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: kafkaStrategyPollInterval}, nil
}

func (r *RestoreJobReconciler) ensureKafkaRestoreJob(
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

	labels := map[string]string{kafkaStrategyLabelMode: mode}
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

// ---------------------------------------------------------------------------
// Backup cleanup path (best-effort object deletion on Backup delete)
// ---------------------------------------------------------------------------

// cleanupKafkaBackup deletes the metadata object a Backup recorded, and WAITS
// for that delete to finish before the Backup is removed, so no object is
// orphaned. Like the RabbitMQ driver, this one uniquely owns its artifact (a
// plain object it wrote with curl, keyed on status.artifact.uri) and no engine
// retention prunes it. The controller has no S3 client, so the delete runs as a
// one-shot Job through the strategy's image; this returns a requeue until the
// Job succeeds. The script treats "object already gone" as success; a genuine
// failure keeps the Backup Terminating rather than orphaning the object.
func (r *BackupReconciler) cleanupKafkaBackup(ctx context.Context, backup *backupsv1alpha1.Backup) (ctrl.Result, error) {
	logger := getLogger(ctx)

	// Escape hatch: an operator releases a Backup stuck Terminating (object
	// storage unreachable) by setting this annotation. Cleanup then skips the
	// delete - reaping any in-flight delete Job - and lets the Backup go.
	if backup.Annotations[kafkaSkipArtifactCleanupAnnotation] == "true" {
		existing := &batchv1.Job{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: backup.Namespace, Name: backup.Name + "-cleanup"}, existing); err == nil {
			_ = r.deleteKafkaCleanupJob(ctx, existing)
		}
		logger.Debug("skipping Kafka artifact cleanup per annotation; object left in bucket", "backup", backup.Name, "annotation", kafkaSkipArtifactCleanupAnnotation)
		return ctrl.Result{}, nil
	}

	if backup.Status.Artifact == nil || backup.Status.Artifact.URI == "" {
		return ctrl.Result{}, nil
	}
	uri := backup.Status.Artifact.URI

	strategy := &strategyv1alpha1.Kafka{}
	if err := r.Get(ctx, client.ObjectKey{Name: backup.Spec.StrategyRef.Name}, strategy); err != nil {
		if apierrors.IsNotFound(err) {
			// No strategy to render the delete Job from (the shipped strategy is
			// gated on a resolved bucket name and stops rendering if that lookup
			// fails). Release the Backup rather than wedge it forever.
			return r.releaseKafkaCleanup(ctx, backup, uri, "strategy CR is gone"), nil
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
		// namespace. Release the Backup then, so teardown can finish; the object
		// cannot be deleted from a namespace that no longer exists.
		terminating, nsErr := r.namespaceTerminating(ctx, backup.Namespace)
		if nsErr != nil {
			return ctrl.Result{}, nsErr
		}
		if terminating {
			return r.releaseKafkaCleanup(ctx, backup, uri, "namespace is terminating"), nil
		}
		if perr := ProjectBackupCredentials(ctx, r.Client, r.CredentialsConfig, backup.Namespace); perr != nil {
			return r.releaseKafkaCleanup(ctx, backup, uri, fmt.Sprintf("cannot project credentials: %v", perr)), nil
		}
		// The template reads only .Mode and .ArtifactURI in cleanup mode (never
		// .Application), so the source app being gone does not matter.
		renderCtx := kafkaRenderContext(nil, backup.Spec.ApplicationRef.Name, backup.Namespace, kafkaStrategyModeCleanup, backup.Name, uri, nil, nil)
		rendered, rerr := renderKafkaTemplate(strategy.Spec.Template, renderCtx)
		if rerr != nil {
			return r.releaseKafkaCleanup(ctx, backup, uri, fmt.Sprintf("cleanup template render failed: %v", rerr)), nil
		}
		if cerr := r.Create(ctx, buildKafkaCleanupJob(backup.Namespace, jobName, rendered)); cerr != nil && !apierrors.IsAlreadyExists(cerr) {
			// Forbidden (namespace went Terminating after the check) or Invalid
			// (an over-long <name>-cleanup Job name) cannot be fixed by retrying;
			// release. Other errors are transient - requeue.
			if apierrors.IsForbidden(cerr) || apierrors.IsInvalid(cerr) {
				return r.releaseKafkaCleanup(ctx, backup, uri, fmt.Sprintf("cannot create cleanup Job: %v", cerr)), nil
			}
			return ctrl.Result{}, cerr
		}
		return ctrl.Result{RequeueAfter: kafkaStrategyPollInterval}, nil
	case err != nil:
		return ctrl.Result{}, err
	}

	if !job.DeletionTimestamp.IsZero() {
		// A prior failed attempt is being collected; wait, then the NotFound
		// branch recreates a fresh one.
		return ctrl.Result{RequeueAfter: kafkaStrategyPollInterval}, nil
	}

	switch jobConditionState(job) {
	case batchv1.JobComplete:
		_ = r.deleteKafkaCleanupJob(ctx, job)
		logger.Debug("Kafka backup object deleted", "backup", backup.Name, "uri", uri)
		return ctrl.Result{}, nil
	case batchv1.JobFailed:
		// A genuine delete failure (a missing object is success in the script).
		// Collect the failed Job so the NotFound branch recreates a fresh one,
		// and keep the Backup Terminating.
		logger.Debug("Kafka cleanup Job failed; retrying", "backup", backup.Name, "job", jobName)
		_ = r.deleteKafkaCleanupJob(ctx, job)
		return ctrl.Result{RequeueAfter: kafkaStrategyPollInterval}, nil
	default:
		return ctrl.Result{RequeueAfter: kafkaStrategyPollInterval}, nil
	}
}

// deleteKafkaCleanupJob removes a finished cleanup Job together with its Pod.
func (r *BackupReconciler) deleteKafkaCleanupJob(ctx context.Context, job *batchv1.Job) error {
	policy := metav1.DeletePropagationBackground
	return client.IgnoreNotFound(r.Delete(ctx, job, &client.DeleteOptions{PropagationPolicy: &policy}))
}

// releaseKafkaCleanup gives up on deleting the backup object and lets the Backup
// be removed - for the conditions under which the delete cannot run here
// (namespace terminating, credentials/permissions unavailable, an unrenderable
// or absent strategy). It records a Warning Event naming the object left behind
// so the give-up is visible, then returns a zero Result so the finalizer is
// released. The delete is best-effort, not guaranteed.
func (r *BackupReconciler) releaseKafkaCleanup(ctx context.Context, backup *backupsv1alpha1.Backup, uri, reason string) ctrl.Result {
	getLogger(ctx).Info("releasing Backup without deleting its object", "backup", backup.Name, "uri", uri, "reason", reason)
	if r.Recorder != nil {
		r.Recorder.Eventf(backup, corev1.EventTypeWarning, "ArtifactNotDeleted", "left object %s in the bucket: %s", uri, reason)
	}
	return ctrl.Result{}
}

// buildKafkaCleanupJob wraps the cleanup-mode pod in a one-shot, ownerless Job.
// Ownerless because cleanupKafkaBackup manages its lifecycle explicitly and it
// must outlive the Backup being deleted; activeDeadlineSeconds + TTL are
// backstops if the controller stops mid-wait.
func buildKafkaCleanupJob(namespace, name string, rendered *corev1.PodTemplateSpec) *batchv1.Job {
	pod := *rendered.DeepCopy()
	if pod.Spec.RestartPolicy == "" {
		pod.Spec.RestartPolicy = corev1.RestartPolicyNever
	}
	if pod.Labels == nil {
		pod.Labels = map[string]string{}
	}
	pod.Labels[kafkaStrategyLabelMode] = kafkaStrategyModeCleanup
	backoffLimit := int32(1)
	activeDeadline := int64(300)
	ttl := int32(300)
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
			Labels:    map[string]string{kafkaStrategyLabelMode: kafkaStrategyModeCleanup},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoffLimit,
			ActiveDeadlineSeconds:   &activeDeadline,
			TTLSecondsAfterFinished: &ttl,
			Template:                pod,
		},
	}
}
