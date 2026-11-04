// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

	// Driver-metadata keys naming the remote archive a Backup owns and the
	// bucket flow it was written to. The archive name is minted here rather
	// than in the strategy Pod so the Backup can identify its own archive.
	altinityArchiveNameKey     = "altinity.strategy.backups.cozystack.io/archive-name"
	altinityUseSystemBucketKey = "altinity.strategy.backups.cozystack.io/use-system-bucket"

	altinityModeCleanup                   = "cleanup"
	altinitySkipArtifactCleanupAnnotation = "backups.cozystack.io/skip-artifact-cleanup"

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

// altinityArchiveName is the remote archive name for a BackupJob. The
// "clickhouse-<app>-" prefix is what the restore branch of the shipped
// strategy filters list/remote on. The UID prefix keeps a BackupJob recreated
// under an earlier name off that one's archive: clickhouse-backup resumes an
// upload into an existing name, and the earlier Backup's cleanup deletes it.
func altinityArchiveName(j *backupsv1alpha1.BackupJob) string {
	uid := string(j.UID)
	if len(uid) > 8 {
		uid = uid[:8]
	}
	return "clickhouse-" + j.Spec.ApplicationRef.Name + "-" + j.Name + "-" + uid
}

// altinityPodCarriesArchiveName reports whether the Pod a backup Job ran was
// given the archive name. A Pod rendered from a template that does not read
// .ArchiveName (a custom strategy, or a Job created before an upgrade) named
// its archive itself, and recording the driver's name for it would point
// cleanup at nothing.
func altinityPodCarriesArchiveName(pod corev1.PodTemplateSpec, archive string) (bool, error) {
	b, err := json.Marshal(pod)
	if err != nil {
		return false, err
	}
	return strings.Contains(string(b), archive), nil
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
	app map[string]any,
	releaseName, releaseNamespace, mode string,
	parameters map[string]string,
	backup *backupsv1alpha1.Backup,
	archiveName string,
) (*corev1.PodTemplateSpec, error) {
	ctxMap := map[string]any{
		"Application": app,
		"Release": map[string]string{
			"Name":      releaseName,
			"Namespace": releaseNamespace,
		},
		"Mode":        mode,
		"Parameters":  parameters,
		"ArchiveName": archiveName,
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
		altinityArchiveName(j),
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
		archiveMD, err := altinityArchiveMetadata(batchJob.Spec.Template, app, j)
		if err != nil {
			return r.markBackupJobFailed(ctx, j, fmt.Sprintf("failed to record the archive: %v", err))
		}
		artifact, err := r.createAltinityBackupArtifact(ctx, j, resolved, archiveMD)
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
	maps.Copy(labels, ownerLabels)

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
	maps.Copy(labels, ownerLabels)

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
	maps.Copy(pod.Labels, labels)
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
	archiveMD map[string]string,
) (*backupsv1alpha1.Backup, error) {
	driverMD := map[string]string{}
	for k, v := range resolved.Parameters {
		driverMD[altinityParamPrefix+k] = v
	}
	maps.Copy(driverMD, archiveMD)

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

// altinityArchiveMetadata records the archive a BackupJob produced and the
// bucket flow the application wrote it to, read from the application spec at
// backup time. Cleanup deletes the archive only on the system bucket flow; an
// application on its own bucket keeps the archive's lifecycle.
func altinityArchiveMetadata(pod corev1.PodTemplateSpec, app map[string]any, j *backupsv1alpha1.BackupJob) (map[string]string, error) {
	useSystemBucket, _, err := unstructured.NestedBool(app, "spec", "backup", "useSystemBucket")
	if err != nil {
		return nil, fmt.Errorf("read spec.backup.useSystemBucket: %w", err)
	}
	md := map[string]string{altinityUseSystemBucketKey: fmt.Sprint(useSystemBucket)}
	archive := altinityArchiveName(j)
	carries, err := altinityPodCarriesArchiveName(pod, archive)
	if err != nil {
		return nil, err
	}
	if carries {
		md[altinityArchiveNameKey] = archive
	}
	return md, nil
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
func (r *BackupJobReconciler) getApplicationUnstructured(ctx context.Context, namespace string, ref corev1.TypedLocalObjectReference) (map[string]any, error) {
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
func (r *RestoreJobReconciler) getApplicationUnstructured(ctx context.Context, namespace string, ref corev1.TypedLocalObjectReference) (map[string]any, error) {
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
		APIGroup: new(targetAPIGroup),
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
		"",
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

// ---------------------------------------------------------------------------
// Backup deletion path
// ---------------------------------------------------------------------------

// altinityArchiveNamePattern keeps the recorded archive name a single URL
// path segment; the name is interpolated into the sidecar's delete route.
var altinityArchiveNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// altinityCleanupScript deletes one remote archive through the
// clickhouse-backup sidecar (POST /backup/delete/{where}/{name} in 2.7.x).
// The sidecar answers 500 with "is not found on remote storage" for an
// archive that is already gone, which is the outcome cleanup wants; every
// other non-200 answer (423 while another operation runs, a ClickHouse or S3
// error) fails the Pod so the controller retries.
const altinityCleanupScript = `set -eu
resp=$(curl -sS --max-time 540 -u "${API_USERNAME}:${API_PASSWORD}" -w '\n%{http_code}' -X POST "http://${API_HOST}:7171/backup/delete/remote/${ARCHIVE_NAME}")
code=$(printf '%s' "${resp}" | tail -n 1)
body=$(printf '%s' "${resp}" | sed '$d')
if [ "${code}" = "200" ]; then
  echo "deleted ${ARCHIVE_NAME}"
  exit 0
fi
case "${body}" in
  *"is not found on remote storage"*)
    echo "${ARCHIVE_NAME} is already absent"
    exit 0
    ;;
esac
echo "delete ${ARCHIVE_NAME} failed: HTTP ${code}: ${body}" >&2
exit 1
`

// cleanupAltinityBackup deletes the remote archive of a system-bucket Backup
// and holds the Backup until the delete finished. Nothing else prunes that
// bucket, so a Backup removed without this leaks its archive for good. An
// application on its own bucket keeps the archive's lifecycle, the same split
// as the MongoDB driver. The delete runs through the application's sidecar,
// so it needs the application; when the application or the namespace is
// gone, the Backup is released with a Warning Event naming the archive left.
//
// The cleanup Pod is built here, not rendered from the strategy template: the
// shipped and the example templates run restore_remote for every mode other
// than backup, so a template rendered in a delete mode would overwrite live
// data. Only the strategy container's image is taken from the template.
func (r *BackupReconciler) cleanupAltinityBackup(ctx context.Context, backup *backupsv1alpha1.Backup) (ctrl.Result, error) {
	logger := getLogger(ctx)
	jobKey := types.NamespacedName{Namespace: backup.Namespace, Name: backup.Name + "-cleanup"}

	if backup.Annotations[altinitySkipArtifactCleanupAnnotation] == "true" {
		existing := &batchv1.Job{}
		err := r.Get(ctx, jobKey, existing)
		switch {
		case err == nil:
			if derr := r.deleteAltinityCleanupJob(ctx, existing); derr != nil {
				return ctrl.Result{}, derr
			}
		case !apierrors.IsNotFound(err):
			return ctrl.Result{}, err
		}
		logger.Debug("skipping Altinity archive cleanup per annotation", "backup", backup.Name)
		return ctrl.Result{}, nil
	}

	archive := backup.Spec.DriverMetadata[altinityArchiveNameKey]
	if archive == "" || backup.Spec.DriverMetadata[altinityUseSystemBucketKey] != "true" {
		return ctrl.Result{}, nil
	}
	if !altinityArchiveNamePattern.MatchString(archive) {
		return r.releaseAltinityCleanup(ctx, backup, archive, "the recorded archive name is not a valid archive name"), nil
	}

	strategy := &strategyv1alpha1.Altinity{}
	if err := r.Get(ctx, client.ObjectKey{Name: backup.Spec.StrategyRef.Name}, strategy); err != nil {
		if apierrors.IsNotFound(err) {
			return r.releaseAltinityCleanup(ctx, backup, archive, "strategy CR is gone"), nil
		}
		return ctrl.Result{}, err
	}
	containers := strategy.Spec.Template.Spec.Containers
	if len(containers) == 0 || containers[0].Image == "" {
		return r.releaseAltinityCleanup(ctx, backup, archive, "strategy template has no container image"), nil
	}

	job := &batchv1.Job{}
	err := r.Get(ctx, jobKey, job)
	switch {
	case apierrors.IsNotFound(err):
		terminating, nsErr := r.namespaceTerminating(ctx, backup.Namespace)
		if nsErr != nil {
			return ctrl.Result{}, nsErr
		}
		if terminating {
			return r.releaseAltinityCleanup(ctx, backup, archive, "namespace is terminating"), nil
		}
		// The delete goes through the application's current sidecar. One that
		// has since moved to its own bucket reports the archive as not found
		// there, which the script would take for a completed delete.
		onSystemBucket, found, aerr := r.altinityAppOnSystemBucket(ctx, backup)
		if aerr != nil {
			return ctrl.Result{}, aerr
		}
		if !found {
			return r.releaseAltinityCleanup(ctx, backup, archive, "the ClickHouse application is gone"), nil
		}
		if !onSystemBucket {
			return r.releaseAltinityCleanup(ctx, backup, archive, "the ClickHouse application no longer uses the system bucket"), nil
		}
		release := backup.Spec.ApplicationRef.Name
		authSecret := "clickhouse-" + release + "-backup-api-auth"
		if serr := r.Get(ctx, types.NamespacedName{Namespace: backup.Namespace, Name: authSecret}, &corev1.Secret{}); serr != nil {
			if apierrors.IsNotFound(serr) {
				return r.releaseAltinityCleanup(ctx, backup, archive,
					fmt.Sprintf("Secret %s is gone, so the application's backup sidecar cannot be called", authSecret)), nil
			}
			return ctrl.Result{}, serr
		}
		desired := buildAltinityCleanupJob(jobKey.Namespace, jobKey.Name, containers[0].Image,
			fmt.Sprintf("chi-clickhouse-%s-clickhouse-0-0.%s.svc", release, backup.Namespace), authSecret, archive)
		if cerr := r.Create(ctx, desired); cerr != nil && !apierrors.IsAlreadyExists(cerr) {
			if apierrors.IsForbidden(cerr) || apierrors.IsInvalid(cerr) {
				return r.releaseAltinityCleanup(ctx, backup, archive, fmt.Sprintf("cannot create cleanup Job: %v", cerr)), nil
			}
			return ctrl.Result{}, cerr
		}
		return ctrl.Result{RequeueAfter: altinityPollInterval}, nil
	case err != nil:
		return ctrl.Result{}, err
	}

	if !job.DeletionTimestamp.IsZero() {
		return ctrl.Result{RequeueAfter: altinityPollInterval}, nil
	}
	if altinityCleanupJobArchive(job) != archive {
		// Left by an earlier Backup of the same name that was released while
		// its Job ran; its outcome is about another archive.
		if derr := r.deleteAltinityCleanupJob(ctx, job); derr != nil {
			return ctrl.Result{}, derr
		}
		return ctrl.Result{RequeueAfter: altinityPollInterval}, nil
	}
	switch jobConditionState(job) {
	case batchv1.JobComplete:
		if derr := r.deleteAltinityCleanupJob(ctx, job); derr != nil {
			return ctrl.Result{}, derr
		}
		logger.Debug("Altinity archive deleted", "backup", backup.Name, "archive", archive)
		return ctrl.Result{}, nil
	case batchv1.JobFailed:
		logger.Debug("Altinity cleanup Job failed; retrying", "backup", backup.Name, "job", job.Name)
		if r.Recorder != nil {
			r.Recorder.Eventf(backup, corev1.EventTypeWarning, "ArtifactCleanupFailed",
				"cleanup Job %s did not delete archive %s (see its Pod log); retrying", job.Name, archive)
		}
		if derr := r.deleteAltinityCleanupJob(ctx, job); derr != nil {
			return ctrl.Result{}, derr
		}
		return ctrl.Result{RequeueAfter: altinityPollInterval}, nil
	default:
		return ctrl.Result{RequeueAfter: altinityPollInterval}, nil
	}
}

// altinityAppOnSystemBucket reads the Backup's application and reports
// whether it exists and still has spec.backup.useSystemBucket set.
func (r *BackupReconciler) altinityAppOnSystemBucket(ctx context.Context, backup *backupsv1alpha1.Backup) (onSystemBucket, found bool, err error) {
	group := backupsv1alpha1.DefaultApplicationAPIGroup
	if ref := backup.Spec.ApplicationRef; ref.APIGroup != nil && *ref.APIGroup != "" {
		group = *ref.APIGroup
	}
	mapping, err := r.RESTMapper().RESTMapping(schema.GroupKind{Group: group, Kind: backup.Spec.ApplicationRef.Kind})
	if err != nil {
		if apimeta.IsNoMatchError(err) {
			return false, false, nil
		}
		return false, false, err
	}
	app := &unstructured.Unstructured{}
	app.SetGroupVersionKind(mapping.GroupVersionKind)
	if err := r.Get(ctx, types.NamespacedName{Namespace: backup.Namespace, Name: backup.Spec.ApplicationRef.Name}, app); err != nil {
		if apierrors.IsNotFound(err) {
			return false, false, nil
		}
		return false, false, err
	}
	v, _, err := unstructured.NestedBool(app.Object, "spec", "backup", "useSystemBucket")
	if err != nil {
		return false, true, fmt.Errorf("read spec.backup.useSystemBucket: %w", err)
	}
	return v, true, nil
}

func altinityCleanupJobArchive(job *batchv1.Job) string {
	for _, c := range job.Spec.Template.Spec.Containers {
		for _, e := range c.Env {
			if e.Name == "ARCHIVE_NAME" {
				return e.Value
			}
		}
	}
	return ""
}

func (r *BackupReconciler) deleteAltinityCleanupJob(ctx context.Context, job *batchv1.Job) error {
	policy := metav1.DeletePropagationBackground
	return client.IgnoreNotFound(r.Delete(ctx, job, &client.DeleteOptions{PropagationPolicy: &policy}))
}

// releaseAltinityCleanup gives up deleting the archive and lets the Backup be
// removed, with a Warning Event naming the archive left in the bucket.
func (r *BackupReconciler) releaseAltinityCleanup(ctx context.Context, backup *backupsv1alpha1.Backup, archive, reason string) ctrl.Result {
	getLogger(ctx).Info("releasing Backup without deleting its archive", "backup", backup.Name, "archive", archive, "reason", reason)
	if r.Recorder != nil {
		r.Recorder.Eventf(backup, corev1.EventTypeWarning, "ArtifactNotDeleted", "left archive %s in the bucket: %s", archive, reason)
	}
	return ctrl.Result{}
}

// buildAltinityCleanupJob is ownerless because cleanupAltinityBackup manages
// its lifecycle and it must outlive the Backup being deleted;
// activeDeadlineSeconds and the TTL are backstops if the controller stops
// mid-wait.
func buildAltinityCleanupJob(namespace, name, image, apiHost, authSecret, archive string) *batchv1.Job {
	labels := map[string]string{altinityLabelMode: altinityModeCleanup}
	backoffLimit := int32(1)
	activeDeadline := int64(600)
	ttl := int32(300)
	authEnv := func(envName, key string) corev1.EnvVar {
		return corev1.EnvVar{Name: envName, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: authSecret},
			Key:                  key,
		}}}
	}
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Labels: labels},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoffLimit,
			ActiveDeadlineSeconds:   &activeDeadline,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:    "ch-backup-cleanup",
						Image:   image,
						Command: []string{"/bin/sh", "-c", altinityCleanupScript},
						Env: []corev1.EnvVar{
							{Name: "API_HOST", Value: apiHost},
							{Name: "ARCHIVE_NAME", Value: archive},
							authEnv("API_USERNAME", "username"),
							authEnv("API_PASSWORD", "password"),
						},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("10m"),
								corev1.ResourceMemory: resource.MustParse("32Mi"),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("200m"),
								corev1.ResourceMemory: resource.MustParse("128Mi"),
							},
						},
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: new(false),
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
							SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
						},
					}},
				},
			},
		},
	}
}
