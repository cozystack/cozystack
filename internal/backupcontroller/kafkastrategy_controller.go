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
// It adds three things over the Job strategy, mirroring the Rabbitmq strategy:
// an applicationRef Kind gate, a Ready precondition on the Strimzi Kafka
// cluster, and a per-run object key via .BackupName + ArtifactURITemplate.

const (
	kafkaStrategyLabelMode   = "kafka.strategy.backups.cozystack.io/mode"
	kafkaStrategyModeBackup  = "backup"
	kafkaStrategyModeRestore = "restore"

	// Round-trips BackupClass parameters through the Backup's DriverMetadata so
	// a later RestoreJob re-renders with the backup-time values.
	kafkaStrategyParamPrefix = "kafka.strategy.backups.cozystack.io/parameter/"

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
// a legible reason. Mirrors rabbitmqNotReadyMessage.
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
	releaseName, releaseNamespace, mode, backupName string,
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
		"Parameters": parameters,
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
// same context. Empty template yields "" (no artifact recorded). The single
// field is wrapped so the shared string-walking template engine can render it.
func renderKafkaArtifactURI(tmplStr string, ctxMap map[string]any) (string, error) {
	if strings.TrimSpace(tmplStr) == "" {
		return "", nil
	}
	wrapper := struct{ URI string }{URI: tmplStr}
	out, err := template.Template(&wrapper, ctxMap)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out.URI), nil
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

	ctxMap := kafkaRenderContext(app, appName, j.Namespace, kafkaStrategyModeBackup, j.Name, resolved.Parameters, nil)
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
		artifactURI, err := renderKafkaArtifactURI(strategy.Spec.ArtifactURITemplate, ctxMap)
		if err != nil {
			return r.markBackupJobFailed(ctx, j, fmt.Sprintf("failed to render artifact URI: %v", err))
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
			Message: "Kafka metadata backup Job completed",
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
	driverMD := map[string]string{}
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

	ctxMap := kafkaRenderContext(app, targetAppName, targetNamespace, kafkaStrategyModeRestore, backup.Name, kafkaStrategyParameters(backup), backup)
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
			Message: "Kafka metadata restore Job completed",
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
