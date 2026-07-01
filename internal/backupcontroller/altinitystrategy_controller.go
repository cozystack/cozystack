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
	"k8s.io/apimachinery/pkg/runtime/schema"
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
	altinityAppKind = "ClickHouse"

	// Mode label / values applied to the rendered batch/v1.Job so RBAC and
	// debugging tools can distinguish backup vs restore runs at a glance.
	altinityLabelMode   = "altinity.strategy.backups.cozystack.io/mode"
	altinityModeBackup  = "backup"
	altinityModeRestore = "restore"

	// Driver-metadata key prefix used to round-trip BackupClassStrategy
	// parameters through the Backup artifact. Lets a future RestoreJob
	// re-render the strategy template with the same parameter values that
	// were in effect at backup time.
	altinityParamPrefix = "altinity.strategy.backups.cozystack.io/parameter/"

	// Polling cadence for the Altinity Job lifecycle. Mirrors the CNPG /
	// Velero strategy cadence so behaviour is uniform across drivers.
	altinityPollInterval = 5 * time.Second
)

// validateAltinityApplicationRef rejects ApplicationRefs that are not
// apps.cozystack.io/ClickHouse. Without this gate the dispatcher would route
// non-ClickHouse refs to the Altinity driver and the templated clickhouse-
// backup invocation would fail with confusing runtime errors.
//
// Empty APIGroup is accepted and treated as the default
// (apps.cozystack.io) - this matches the BackupClass-resolution helpers in
// backupclass_resolver.go and the Plan/BackupJob CRD docs which both default
// an unset apiGroup to apps.cozystack.io.
func validateAltinityApplicationRef(ref corev1.TypedLocalObjectReference) error {
	if ref.Kind != altinityAppKind {
		return fmt.Errorf("altinity strategy supports applicationRef.kind=%q, got %q", altinityAppKind, ref.Kind)
	}
	apiGroup := ""
	if ref.APIGroup != nil {
		apiGroup = *ref.APIGroup
	}
	if apiGroup != "" && apiGroup != backupsv1alpha1.DefaultApplicationAPIGroup {
		return fmt.Errorf("altinity strategy supports applicationRef.apiGroup=%q, got %q", backupsv1alpha1.DefaultApplicationAPIGroup, apiGroup)
	}
	return nil
}

// jobNameForBackupJob returns the deterministic batch/v1.Job name used for
// the backup pod that drives a BackupJob. Idempotency relies on this being
// pure: a retried reconcile must hit the same Job name.
func jobNameForBackupJob(j *backupsv1alpha1.BackupJob) string {
	return j.Name + "-backup"
}

// jobNameForRestoreJob mirrors jobNameForBackupJob for the restore path.
func jobNameForRestoreJob(rj *backupsv1alpha1.RestoreJob) string {
	return rj.Name + "-restore"
}

// backupParameters extracts BackupClassStrategy parameters from a Backup's
// DriverMetadata. Round-trips the values written by reconcileAltinity at
// backup time so a later RestoreJob can render the strategy template with
// the parameter snapshot in effect when the backup was taken.
func backupParameters(b *backupsv1alpha1.Backup) map[string]string {
	out := map[string]string{}
	for k, v := range b.Spec.DriverMetadata {
		if !strings.HasPrefix(k, altinityParamPrefix) {
			continue
		}
		paramKey := strings.TrimPrefix(k, altinityParamPrefix)
		if paramKey == "" {
			continue
		}
		out[paramKey] = v
	}
	return out
}

// renderAltinityTemplate runs the strategy's PodTemplateSpec through the
// repository's text/template engine with the same context shape as the CNPG
// driver: every string field is templated against the application object,
// the release shorthand, the run mode, and the resolved parameters.
func renderAltinityTemplate(
	tmpl corev1.PodTemplateSpec,
	app map[string]interface{},
	releaseName, releaseNamespace, mode string,
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
		"Parameters": parameters,
	}
	if backup != nil {
		// Expose ApplicationRef so restore strategies can reference the
		// SOURCE release name even when restoring into a differently-
		// named target (to-copy). The destination release name is in
		// .Release.Name (set above to targetAppName); without
		// .Backup.ApplicationRef.Name there is no way for the strategy
		// template to filter "list/remote" results by source-release
		// prefix when the sidecar's S3_PATH points at the source's
		// prefix via backup.s3PathOverride.
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

func (r *BackupJobReconciler) reconcileAltinity(ctx context.Context, j *backupsv1alpha1.BackupJob, resolved *ResolvedBackupConfig) (ctrl.Result, error) {
	logger := getLogger(ctx)
	logger.Debug("reconciling Altinity strategy", "backupjob", j.Name, "phase", j.Status.Phase)

	if j.Status.Phase == backupsv1alpha1.BackupJobPhaseSucceeded ||
		j.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
		return ctrl.Result{}, nil
	}

	if err := validateAltinityApplicationRef(j.Spec.ApplicationRef); err != nil {
		return r.markBackupJobFailed(ctx, j, err.Error())
	}

	// First-reconcile bookkeeping. Refetch before writing StartedAt so a
	// stale informer cache cannot silently slide the timestamp forward
	// across reconciles, mirroring reconcileCNPG's pattern.
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
			// ResourceVersion. Copying StartedAt/Phase back into the local
			// j here would leave any subsequent r.Status().Update calls in
			// the same reconcile carrying the pre-patch ResourceVersion
			// and failing with Conflict. Mirrors reconcileCNPG.
			return ctrl.Result{RequeueAfter: altinityPollInterval}, nil
		}
	}

	strategy := &strategyv1alpha1.Altinity{}
	if err := r.Get(ctx, client.ObjectKey{Name: resolved.StrategyRef.Name}, strategy); err != nil {
		if apierrors.IsNotFound(err) {
			return r.requeueStrategyNotReady(ctx, j, resolved.StrategyRef.Name)
		}
		return ctrl.Result{}, err
	}

	app, err := r.getApplicationUnstructured(ctx, j.Namespace, j.Spec.ApplicationRef)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.markBackupJobFailed(ctx, j, fmt.Sprintf("ClickHouse application not found: %s/%s", j.Namespace, j.Spec.ApplicationRef.Name))
		}
		return ctrl.Result{}, err
	}

	rendered, err := renderAltinityTemplate(
		strategy.Spec.Template,
		app,
		j.Spec.ApplicationRef.Name,
		j.Namespace,
		altinityModeBackup,
		resolved.Parameters,
		nil,
	)
	if err != nil {
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf("failed to template Altinity strategy: %v", err))
	}

	batchJob, err := r.ensureAltinityJob(ctx, j, j.Namespace, jobNameForBackupJob(j),
		altinityModeBackup,
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
		artifact, err := r.createAltinityBackupArtifact(ctx, j, resolved)
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
			Message: "clickhouse-backup Job completed",
		})
		if err := r.Status().Update(ctx, j); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil

	case batchv1.JobFailed:
		message := jobFailureMessage(batchJob)
		if message == "" {
			message = "clickhouse-backup Job reported Failed"
		}
		return r.markBackupJobFailed(ctx, j, message)

	default:
		return ctrl.Result{RequeueAfter: altinityPollInterval}, nil
	}
}

// ensureAltinityJob materialises a batch/v1.Job from the rendered
// PodTemplateSpec, idempotently. The Job is named deterministically per
// owning BackupJob/RestoreJob, so a retried reconcile observes the existing
// Job rather than creating a duplicate. The Job is rendered in the owning
// BackupJob's namespace (the only supported case - BackupJob.Spec.
// ApplicationRef is corev1.TypedLocalObjectReference) and carries a
// controllerRef so kube-gc collects it (and the running Pod) when the
// BackupJob is deleted.
func (r *BackupJobReconciler) ensureAltinityJob(
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

	labels := map[string]string{altinityLabelMode: mode}
	for k, v := range ownerLabels {
		labels[k] = v
	}

	desired := buildAltinityBatchJob(namespace, name, labels, rendered)
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

// ensureAltinityRestoreJob mirrors ensureAltinityJob from the RestoreJob
// reconciler. The Job lives in the RestoreJob's namespace - both
// BackupRef and TargetApplicationRef on RestoreJob are
// corev1.TypedLocalObjectReference (no Namespace field), so cross-namespace
// restore is not representable in the current API and the Job always gets
// a controllerRef on the RestoreJob.
func (r *RestoreJobReconciler) ensureAltinityRestoreJob(
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

	labels := map[string]string{altinityLabelMode: mode}
	for k, v := range ownerLabels {
		labels[k] = v
	}

	desired := buildAltinityBatchJob(namespace, name, labels, rendered)
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

// buildAltinityBatchJob assembles the typed batch/v1.Job that wraps the
// rendered PodTemplateSpec. RestartPolicyNever and a small backoff cap match
// what a one-shot dump-style backup needs - the tool either succeeds or
// fails, retries inside the same Pod don't add value.
func buildAltinityBatchJob(namespace, name string, labels map[string]string, rendered *corev1.PodTemplateSpec) *batchv1.Job {
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

// jobConditionState reports the terminal condition (Complete/Failed) for a
// batch/v1.Job, or the empty string while the Job is still running.
// apimeta-style helpers don't ship a batch-specific finder, so we walk the
// typed slice directly - no string matching, no re-implementation of
// generic condition logic.
func jobConditionState(j *batchv1.Job) batchv1.JobConditionType {
	for _, c := range j.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		if c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed {
			return c.Type
		}
	}
	return ""
}

// jobFailureMessage returns the message of the JobFailed condition, if any.
// Used to surface the underlying clickhouse-backup error on the BackupJob
// status without re-templating it.
func jobFailureMessage(j *batchv1.Job) string {
	for _, c := range j.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			return c.Message
		}
	}
	return ""
}

// createAltinityBackupArtifact materialises a Cozystack Backup carrying the
// strategy reference and the BackupClassStrategy parameters in effect at
// backup time. Parameters round-trip via DriverMetadata under
// altinityParamPrefix so a later RestoreJob can re-render the strategy
// template against the same values.
func (r *BackupJobReconciler) createAltinityBackupArtifact(
	ctx context.Context,
	j *backupsv1alpha1.BackupJob,
	resolved *ResolvedBackupConfig,
) (*backupsv1alpha1.Backup, error) {
	driverMD := map[string]string{}
	for k, v := range resolved.Parameters {
		driverMD[altinityParamPrefix+k] = v
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
	// Status fields set on the struct above are dropped server-side because
	// Backup has the status subresource, but populating them here keeps the
	// struct in step with what an out-of-band reconciler will fill in (the
	// owning BackupReconciler watches Backup and is the natural place to
	// promote phase=Ready). Mirrors the CNPG/Velero pattern - issuing a
	// follow-up Status().Patch from this driver races with that reconciler
	// adding the cleanup finalizer and surfaces as a "not found" 404 even
	// though the Backup is on disk in Ready state.
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

// getApplicationUnstructured fetches the application object via the dynamic
// client. The strategy template needs the live object to allow expressions
// like {{ .Application.spec.shards }} - a typed client would force the
// driver to import every supported app's Go types.
//
// validateAltinityApplicationRef accepts an empty APIGroup as the documented
// default ("apps.cozystack.io"); RESTMapping must apply the same default so
// a BackupJob/RestoreJob with apiGroup omitted does not blow up at lookup
// time with a misleading "no matches for /ClickHouse" error.
func (r *BackupJobReconciler) getApplicationUnstructured(ctx context.Context, namespace string, ref corev1.TypedLocalObjectReference) (map[string]interface{}, error) {
	group := backupsv1alpha1.DefaultApplicationAPIGroup
	if ref.APIGroup != nil && *ref.APIGroup != "" {
		group = *ref.APIGroup
	}
	mapping, err := r.RESTMapping(schema.GroupKind{Group: group, Kind: ref.Kind})
	if err != nil {
		return nil, err
	}
	ns := namespace
	if mapping.Scope.Name() != apimeta.RESTScopeNameNamespace {
		ns = ""
	}
	obj, err := r.Resource(mapping.Resource).Namespace(ns).Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return obj.Object, nil
}

// getApplicationUnstructured is the RestoreJob-side mirror. Same APIGroup
// default applies (see the BackupJob variant for the rationale).
func (r *RestoreJobReconciler) getApplicationUnstructured(ctx context.Context, namespace string, ref corev1.TypedLocalObjectReference) (map[string]interface{}, error) {
	group := backupsv1alpha1.DefaultApplicationAPIGroup
	if ref.APIGroup != nil && *ref.APIGroup != "" {
		group = *ref.APIGroup
	}
	mapping, err := r.RESTMapping(schema.GroupKind{Group: group, Kind: ref.Kind})
	if err != nil {
		return nil, err
	}
	ns := namespace
	if mapping.Scope.Name() != apimeta.RESTScopeNameNamespace {
		ns = ""
	}
	obj, err := r.Resource(mapping.Resource).Namespace(ns).Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return obj.Object, nil
}

// ---------------------------------------------------------------------------
// RestoreJob path
// ---------------------------------------------------------------------------

func (r *RestoreJobReconciler) reconcileAltinityRestore(ctx context.Context, restoreJob *backupsv1alpha1.RestoreJob, backup *backupsv1alpha1.Backup) (ctrl.Result, error) {
	logger := getLogger(ctx)
	logger.Debug("reconciling Altinity restore", "restorejob", restoreJob.Name, "backup", backup.Name)

	if restoreJob.Status.Phase == backupsv1alpha1.RestoreJobPhaseSucceeded ||
		restoreJob.Status.Phase == backupsv1alpha1.RestoreJobPhaseFailed {
		return ctrl.Result{}, nil
	}

	if err := validateAltinityApplicationRef(backup.Spec.ApplicationRef); err != nil {
		return r.markRestoreJobFailed(ctx, restoreJob, err.Error())
	}

	// Resolve the effective restore target.
	//
	// RestoreJob.spec.targetApplicationRef is corev1.TypedLocalObjectReference
	// by design - "Local" means the referenced object lives in the same
	// namespace as the RestoreJob (and, since backupRef is also a
	// LocalObjectReference, the same namespace as the source Backup). The
	// driver therefore restores into restoreJob.Namespace; cross-namespace
	// restore is intentionally unsupported until the Cozystack RestoreJob
	// API gains an explicit cross-namespace target type. For documentation
	// see examples/backups/clickhouse/92-scenario-user-restore.md.
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
	if err := validateAltinityApplicationRef(targetRef); err != nil {
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
			// Requeue so the next reconcile re-Gets restoreJob with the
			// post-patch ResourceVersion. Mirrors the BackupJob path above
			// and reconcileCNPG. See the comment there for the rationale.
			return ctrl.Result{RequeueAfter: altinityPollInterval}, nil
		}
	}

	strategy := &strategyv1alpha1.Altinity{}
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
				"target ClickHouse application not found: %s/%s (deploy it before requesting a copy restore)",
				targetNamespace, targetAppName))
		}
		return ctrl.Result{}, err
	}

	rendered, err := renderAltinityTemplate(
		strategy.Spec.Template,
		app,
		targetAppName,
		targetNamespace,
		altinityModeRestore,
		backupParameters(backup),
		backup,
	)
	if err != nil {
		return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf("failed to template Altinity strategy: %v", err))
	}

	batchJob, err := r.ensureAltinityRestoreJob(ctx, restoreJob, targetNamespace, jobNameForRestoreJob(restoreJob),
		altinityModeRestore,
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
			Message: "clickhouse-backup Job completed",
		})
		if err := r.Status().Update(ctx, restoreJob); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil

	case batchv1.JobFailed:
		message := jobFailureMessage(batchJob)
		if message == "" {
			message = "clickhouse-backup restore Job reported Failed"
		}
		return r.markRestoreJobFailed(ctx, restoreJob, message)

	default:
		return ctrl.Result{RequeueAfter: altinityPollInterval}, nil
	}
}
