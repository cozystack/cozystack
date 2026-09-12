// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	"github.com/cozystack/cozystack/internal/backupcontroller/buckettypes"
	"github.com/cozystack/cozystack/internal/template"
)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const (
	bucketAppKind = "Bucket"

	// Mode label / values applied to the mirror Job. Mirrors the Job /
	// Altinity mode-label convention.
	bucketLabelMode   = "bucket.strategy.backups.cozystack.io/mode"
	bucketModeBackup  = "backup"
	bucketModeRestore = "restore"

	// Driver-metadata key prefix used to round-trip BackupClassStrategy
	// parameters through the Backup artifact, mirroring the Job driver.
	bucketParamPrefix = "bucket.strategy.backups.cozystack.io/parameter/"

	// bucketPollInterval matches the Job / Altinity cadence.
	bucketPollInterval = 5 * time.Second

	// bucketFieldManager owns the COSI BucketAccess objects the driver
	// applies via server-side apply.
	bucketFieldManager = "cozystack-bucket-backup-driver"

	// COSI protocol the bucket app provisions.
	bucketProtocolS3 = "s3"

	// Kind stamped into the restore snapshot so a reader can recognise it.
	bucketSnapshotKind = "BucketBackupSource"

	// Mount path for an optional CA bundle handed to the mirror.
	bucketCAMountPath = "/etc/s3-ca"
	bucketCAFileName  = "ca.crt"
)

// bucketBackupSnapshot is the self-contained restore source persisted on
// Backup.status.underlyingResources. It carries only coordinates and Secret
// names/keys - never raw credentials - so a restore can rebuild the copy
// source even after the source Bucket and the operator-side objects are gone,
// and works to a differently-named target.
type bucketBackupSnapshot struct {
	Kind         string `json:"kind"`
	SourceBucket string `json:"sourceBucket"`
	RepoBucket   string `json:"repoBucket"`
	RepoEndpoint string `json:"repoEndpoint"`
	RepoPrefix   string `json:"repoPrefix"`
	RepoRegion   string `json:"repoRegion,omitempty"`
	// Repo credentials are referenced, never inlined. The name is the
	// projected cozy-backups-creds Secret; the keys default to the AWS_*
	// entries the projector writes.
	RepoCredentialsSecret       string `json:"repoCredentialsSecret"`
	RepoCredentialsAccessKeyKey string `json:"repoCredentialsAccessKeyKey"`
	RepoCredentialsSecretKeyKey string `json:"repoCredentialsSecretKeyKey"`
	InsecureSkipVerify          bool   `json:"insecureSkipVerify,omitempty"`
	ServerSideCopy              bool   `json:"serverSideCopy,omitempty"`
}

// validateBucketApplicationRef rejects refs that are not
// apps.cozystack.io/Bucket. An empty APIGroup is accepted as the documented
// default, matching the other drivers.
func validateBucketApplicationRef(ref corev1.TypedLocalObjectReference) error {
	if ref.Kind != bucketAppKind {
		return fmt.Errorf("bucket strategy supports applicationRef.kind=%q, got %q", bucketAppKind, ref.Kind)
	}
	apiGroup := ""
	if ref.APIGroup != nil {
		apiGroup = *ref.APIGroup
	}
	if apiGroup != "" && apiGroup != backupsv1alpha1.DefaultApplicationAPIGroup {
		return fmt.Errorf("bucket strategy supports applicationRef.apiGroup=%q, got %q", backupsv1alpha1.DefaultApplicationAPIGroup, apiGroup)
	}
	return nil
}

// deriveAccessClassName maps a BucketClaim's bucketClassName to the matching
// BucketAccessClass name. The bucket app appends "-lock" to the claim class
// for object-lock buckets but the access class carries no such suffix; the
// read-only access class carries a "-readonly" suffix. Keeping this pure lets
// the mapping be unit-tested against both branches.
func deriveAccessClassName(bucketClassName string, readonly bool) string {
	base := strings.TrimSuffix(bucketClassName, "-lock")
	if readonly {
		return base + "-readonly"
	}
	return base
}

// bucketReleasePrefix is the Helm release-name prefix the bucket-rd
// ApplicationDefinition wraps every apps.cozystack.io/Bucket in, so the app
// "web" is released as "bucket-web" and its COSI BucketClaim / BucketAccess
// objects are named after that release, not the bare app name. Matches the
// "bucket-%s" the backupstrategy-controller bucketName helper resolves through.
const bucketReleasePrefix = "bucket-"

// bucketReleaseName maps an apps.cozystack.io/Bucket name to its Helm release
// name, which is also the COSI BucketClaim name. Pure and unit-testable.
func bucketReleaseName(app string) string { return bucketReleasePrefix + app }

// backupAccessName / restoreAccessName are the deterministic COSI BucketAccess
// (and credentials Secret) names the driver provisions, keyed off the release
// name so they sit alongside the app's own chart-created accesses. Pure so a
// retried reconcile addresses the same object.
func backupAccessName(app string) string  { return bucketReleaseName(app) + "-cozy-backup" }
func restoreAccessName(app string) string { return bucketReleaseName(app) + "-cozy-restore" }

// joinRepoPrefix joins a base prefix and a per-backup segment into a
// slash-terminated object-key prefix with no leading slash.
func joinRepoPrefix(base, segment string) string {
	parts := []string{}
	if b := strings.Trim(base, "/"); b != "" {
		parts = append(parts, b)
	}
	if s := strings.Trim(segment, "/"); s != "" {
		parts = append(parts, s)
	}
	joined := strings.Join(parts, "/")
	if joined == "" {
		return ""
	}
	return joined + "/"
}

// resolveBucketRestoreTarget computes the effective restore target: the source
// application recorded on the Backup, with each of name/kind/apiGroup
// overridden by a non-empty RestoreJob.spec.targetApplicationRef field
// (restore-as-copy). Pure so the override precedence is unit-testable.
func resolveBucketRestoreTarget(restoreJob *backupsv1alpha1.RestoreJob, backup *backupsv1alpha1.Backup) corev1.TypedLocalObjectReference {
	name := backup.Spec.ApplicationRef.Name
	kind := backup.Spec.ApplicationRef.Kind
	apiGroup := ""
	if backup.Spec.ApplicationRef.APIGroup != nil {
		apiGroup = *backup.Spec.ApplicationRef.APIGroup
	}
	if restoreJob.Spec.TargetApplicationRef != nil {
		if restoreJob.Spec.TargetApplicationRef.Name != "" {
			name = restoreJob.Spec.TargetApplicationRef.Name
		}
		if restoreJob.Spec.TargetApplicationRef.Kind != "" {
			kind = restoreJob.Spec.TargetApplicationRef.Kind
		}
		if restoreJob.Spec.TargetApplicationRef.APIGroup != nil {
			apiGroup = *restoreJob.Spec.TargetApplicationRef.APIGroup
		}
	}
	return corev1.TypedLocalObjectReference{
		APIGroup: stringPtr(apiGroup),
		Kind:     kind,
		Name:     name,
	}
}

// renderBucketTemplate runs the strategy's BucketTemplate through the
// repository's template engine with the same context shape as the other
// drivers.
func renderBucketTemplate(
	tmpl strategyv1alpha1.BucketTemplate,
	app map[string]interface{},
	releaseName, releaseNamespace, mode string,
	parameters map[string]string,
) (*strategyv1alpha1.BucketTemplate, error) {
	ctxMap := map[string]interface{}{
		"Application": app,
		"Release": map[string]string{
			"Name":      releaseName,
			"Namespace": releaseNamespace,
		},
		"Mode":       mode,
		"Parameters": parameters,
	}
	return template.Template(&tmpl, ctxMap)
}

// bucketBackupPrecondition reports whether the source BucketClaim is ready to
// be backed up, or a precise reason/message to surface while it is not. Pure
// so both branches are unit-testable.
func bucketBackupPrecondition(claim *buckettypes.BucketClaim) (ready bool, reason, message string) {
	if claim == nil {
		return false, "SourceBucketNotFound", "source BucketClaim not found; the Bucket application may still be provisioning"
	}
	if !claim.Status.BucketReady {
		return false, "SourceBucketNotReady", fmt.Sprintf("source BucketClaim %q is not ready yet", claim.Name)
	}
	if claim.Status.BucketName == "" {
		return false, "SourceBucketNameUnresolved", fmt.Sprintf("source BucketClaim %q has no COSI-assigned bucketName yet", claim.Name)
	}
	return true, "", ""
}

// marshalBucketSnapshot / unmarshalBucketSnapshot round-trip the restore
// source through Backup.status.underlyingResources.
func marshalBucketSnapshot(s bucketBackupSnapshot) (*runtime.RawExtension, error) {
	return marshalUnderlyingResources(s)
}

func unmarshalBucketSnapshot(ur *runtime.RawExtension) (*bucketBackupSnapshot, error) {
	if ur == nil || len(ur.Raw) == 0 {
		return nil, nil
	}
	var s bucketBackupSnapshot
	if err := json.Unmarshal(ur.Raw, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// ---------------------------------------------------------------------------
// BackupJob path
// ---------------------------------------------------------------------------

func (r *BackupJobReconciler) reconcileBucket(ctx context.Context, j *backupsv1alpha1.BackupJob, resolved *ResolvedBackupConfig) (ctrl.Result, error) {
	logger := getLogger(ctx)
	logger.Debug("reconciling Bucket strategy", "backupjob", j.Name, "phase", j.Status.Phase)

	if j.Status.Phase == backupsv1alpha1.BackupJobPhaseSucceeded ||
		j.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
		return ctrl.Result{}, nil
	}

	if err := validateBucketApplicationRef(j.Spec.ApplicationRef); err != nil {
		return r.markBackupJobFailed(ctx, j, err.Error())
	}

	// First-reconcile bookkeeping (StartedAt then requeue) - see reconcileJob
	// for the ResourceVersion rationale.
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
			return ctrl.Result{RequeueAfter: bucketPollInterval}, nil
		}
	}

	strategy := &strategyv1alpha1.Bucket{}
	if err := r.Get(ctx, client.ObjectKey{Name: resolved.StrategyRef.Name}, strategy); err != nil {
		if apierrors.IsNotFound(err) {
			return r.requeueStrategyNotReady(ctx, j, resolved.StrategyRef.Name)
		}
		return ctrl.Result{}, err
	}

	app, err := r.getApplicationUnstructured(ctx, j.Namespace, j.Spec.ApplicationRef)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.markBackupJobFailed(ctx, j, fmt.Sprintf("Bucket application not found: %s/%s", j.Namespace, j.Spec.ApplicationRef.Name))
		}
		return ctrl.Result{}, err
	}

	release := j.Spec.ApplicationRef.Name

	// Source BucketClaim readiness precondition. The claim is named after the
	// bucket-rd Helm release (bucket-<app>), not the bare app name.
	claim := &buckettypes.BucketClaim{}
	claimErr := r.Get(ctx, types.NamespacedName{Namespace: j.Namespace, Name: bucketReleaseName(release)}, claim)
	if claimErr != nil && !apierrors.IsNotFound(claimErr) {
		return ctrl.Result{}, claimErr
	}
	if apierrors.IsNotFound(claimErr) {
		claim = nil
	}
	if ready, reason, message := bucketBackupPrecondition(claim); !ready {
		return r.requeueBucketWaiting(ctx, j, reason, message)
	}

	rendered, err := renderBucketTemplate(strategy.Spec.Template, app, release, j.Namespace, bucketModeBackup, resolved.Parameters)
	if err != nil {
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf("failed to template Bucket strategy: %v", err))
	}

	// Provision (idempotently) the read-only BucketAccess the mirror reads
	// the source bucket through, then gate on the COSI grant.
	access, err := r.ensureBucketAccess(ctx, j.Namespace, backupAccessName(release), bucketReleaseName(release), deriveAccessClassName(claim.Spec.BucketClassName, true))
	if err != nil {
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf("failed to provision source BucketAccess: %v", err))
	}
	if !access.Status.AccessGranted {
		return r.requeueBucketWaiting(ctx, j, "SourceAccessPending", fmt.Sprintf("source BucketAccess %q not granted yet", access.Name))
	}

	repoPrefix := joinRepoPrefix(rendered.Destination.Prefix, j.Name)
	pod := buildBucketMirrorPod(bucketModeBackup, *rendered, backupAccessName(release), repoPrefix, false)

	batchJob, err := r.ensureJobStrategyJob(ctx, j, j.Namespace, jobNameForBackupJob(j),
		bucketModeBackup,
		map[string]string{
			bucketLabelMode:                         bucketModeBackup,
			backupsv1alpha1.OwningJobNameLabel:      j.Name,
			backupsv1alpha1.OwningJobNamespaceLabel: j.Namespace,
		},
		pod,
	)
	if err != nil {
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf("failed to ensure mirror Job: %v", err))
	}

	switch jobConditionState(batchJob) {
	case batchv1.JobComplete:
		if j.Status.BackupRef != nil {
			return ctrl.Result{}, nil
		}
		artifact, err := r.createBucketBackupArtifact(ctx, j, resolved, *rendered, claim.Status.BucketName, repoPrefix)
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
			Message: "S3 mirror Job completed",
		})
		if err := r.Status().Update(ctx, j); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil

	case batchv1.JobFailed:
		message := jobFailureMessage(batchJob)
		if message == "" {
			message = "S3 mirror Job reported Failed"
		}
		return r.markBackupJobFailed(ctx, j, message)

	default:
		return ctrl.Result{RequeueAfter: bucketPollInterval}, nil
	}
}

// requeueBucketWaiting surfaces a transient Ready=False on the BackupJob while
// a COSI-side precondition (source bucket, access grant) is not yet met, and
// requeues. Bounded by StrategyNotReadyDeadline so a never-satisfied
// precondition fails closed instead of requeuing forever.
func (r *BackupJobReconciler) requeueBucketWaiting(ctx context.Context, j *backupsv1alpha1.BackupJob, reason, message string) (ctrl.Result, error) {
	logger := getLogger(ctx)
	if strategyNotReadyDeadlineExceeded(j.Status.StartedAt) {
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf("%s (not satisfied within %s)", message, StrategyNotReadyDeadline))
	}
	apimeta.SetStatusCondition(&j.Status.Conditions, metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionFalse,
		Reason:  reason,
		Message: message,
	})
	if updateErr := r.Status().Update(ctx, j); updateErr != nil {
		logger.Error(updateErr, "failed to update BackupJob status while waiting on bucket precondition")
	}
	return ctrl.Result{RequeueAfter: CredentialsProjectionRequeue}, nil
}

// ensureBucketAccess provisions a COSI BucketAccess (and its credentials
// Secret) idempotently via server-side apply. The write is gated
// write-only-when-absent: an existing access (created by a prior backup, or by
// the tenant) is returned untouched, so the driver never clobbers a grant it
// does not exclusively own. The object carries no controllerRef - it persists
// across BackupJobs and is reused.
func (r *BackupJobReconciler) ensureBucketAccess(ctx context.Context, namespace, name, claimName, accessClass string) (*buckettypes.BucketAccess, error) {
	existing := &buckettypes.BucketAccess{}
	err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, existing)
	if err == nil {
		return existing, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}
	desired := &buckettypes.BucketAccess{
		TypeMeta: metav1.TypeMeta{
			APIVersion: buckettypes.GroupVersion.String(),
			Kind:       "BucketAccess",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
			Labels:    map[string]string{managedByLabel: managedByValue},
		},
		Spec: buckettypes.BucketAccessSpec{
			BucketClaimName:       claimName,
			BucketAccessClassName: accessClass,
			Protocol:              bucketProtocolS3,
			CredentialsSecretName: name,
		},
	}
	if err := r.Patch(ctx, desired, client.Apply, client.FieldOwner(bucketFieldManager), client.ForceOwnership); err != nil {
		return nil, err
	}
	return desired, nil
}

// createBucketBackupArtifact materialises a Cozystack Backup with the restore
// snapshot on status.underlyingResources (self-contained repo coordinates +
// credentials Secret name) and the strategy parameters on driverMetadata.
// Follows the MongoDB artifact pattern - Create persists status because the
// core Backup type carries no status subresource.
func (r *BackupJobReconciler) createBucketBackupArtifact(
	ctx context.Context,
	j *backupsv1alpha1.BackupJob,
	resolved *ResolvedBackupConfig,
	rendered strategyv1alpha1.BucketTemplate,
	sourceBucketName, repoPrefix string,
) (*backupsv1alpha1.Backup, error) {
	driverMD := map[string]string{}
	for k, v := range resolved.Parameters {
		driverMD[bucketParamPrefix+k] = v
	}

	snapshot := bucketBackupSnapshot{
		Kind:                        bucketSnapshotKind,
		SourceBucket:                sourceBucketName,
		RepoBucket:                  rendered.Destination.Bucket,
		RepoEndpoint:                rendered.Destination.Endpoint,
		RepoPrefix:                  repoPrefix,
		RepoRegion:                  rendered.Destination.Region,
		RepoCredentialsSecret:       rendered.Destination.AccessKeyIDSecretKeyRef.Name,
		RepoCredentialsAccessKeyKey: rendered.Destination.AccessKeyIDSecretKeyRef.Key,
		RepoCredentialsSecretKeyKey: rendered.Destination.SecretAccessKeySecretKeyRef.Key,
		ServerSideCopy:              rendered.ServerSideCopy,
	}
	if rendered.Destination.TLS != nil {
		snapshot.InsecureSkipVerify = rendered.Destination.TLS.InsecureSkipVerify
	}
	underlyingResources, err := marshalBucketSnapshot(snapshot)
	if err != nil {
		return nil, fmt.Errorf("encode restore snapshot: %w", err)
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
			Phase:               backupsv1alpha1.BackupPhaseReady,
			UnderlyingResources: underlyingResources,
			Artifact: &backupsv1alpha1.BackupArtifact{
				URI: fmt.Sprintf("s3://%s/%s", snapshot.RepoBucket, snapshot.RepoPrefix),
			},
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

func (r *RestoreJobReconciler) reconcileBucketRestore(ctx context.Context, restoreJob *backupsv1alpha1.RestoreJob, backup *backupsv1alpha1.Backup) (ctrl.Result, error) {
	logger := getLogger(ctx)
	logger.Debug("reconciling Bucket restore", "restorejob", restoreJob.Name, "backup", backup.Name)

	if restoreJob.Status.Phase == backupsv1alpha1.RestoreJobPhaseSucceeded ||
		restoreJob.Status.Phase == backupsv1alpha1.RestoreJobPhaseFailed {
		return ctrl.Result{}, nil
	}

	if err := validateBucketApplicationRef(backup.Spec.ApplicationRef); err != nil {
		return r.markRestoreJobFailed(ctx, restoreJob, err.Error())
	}

	// Resolve the effective restore target (in-place = source; to-copy =
	// targetApplicationRef override), same-namespace only.
	targetNamespace := restoreJob.Namespace
	targetRef := resolveBucketRestoreTarget(restoreJob, backup)
	targetAppName := targetRef.Name
	if err := validateBucketApplicationRef(targetRef); err != nil {
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
			return ctrl.Result{RequeueAfter: bucketPollInterval}, nil
		}
	}

	strategy := &strategyv1alpha1.Bucket{}
	if err := r.Get(ctx, client.ObjectKey{Name: backup.Spec.StrategyRef.Name}, strategy); err != nil {
		if apierrors.IsNotFound(err) {
			return r.requeueRestoreStrategyNotReady(ctx, restoreJob, backup.Spec.StrategyRef.Name)
		}
		return ctrl.Result{}, err
	}

	snapshot, err := unmarshalBucketSnapshot(backup.Status.UnderlyingResources)
	if err != nil {
		return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf("decode Backup snapshot: %v", err))
	}
	if snapshot == nil || snapshot.RepoBucket == "" || snapshot.RepoPrefix == "" {
		return r.markRestoreJobFailed(ctx, restoreJob, "Backup has no restorable snapshot (repo coordinates missing); re-run the BackupJob")
	}

	app, err := r.getApplicationUnstructured(ctx, targetNamespace, targetRef)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf(
				"target Bucket application not found: %s/%s (deploy it before requesting a restore)", targetNamespace, targetAppName))
		}
		return ctrl.Result{}, err
	}

	rendered, err := renderBucketTemplate(strategy.Spec.Template, app, targetAppName, targetNamespace, bucketModeRestore, bucketRestoreParameters(backup))
	if err != nil {
		return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf("failed to template Bucket strategy: %v", err))
	}

	// Target BucketClaim readiness precondition (named bucket-<app>).
	claim := &buckettypes.BucketClaim{}
	claimErr := r.Get(ctx, types.NamespacedName{Namespace: targetNamespace, Name: bucketReleaseName(targetAppName)}, claim)
	if claimErr != nil && !apierrors.IsNotFound(claimErr) {
		return ctrl.Result{}, claimErr
	}
	if apierrors.IsNotFound(claimErr) {
		claim = nil
	}
	if ready, reason, message := bucketBackupPrecondition(claim); !ready {
		return r.requeueRestoreBucketWaiting(ctx, restoreJob, reason, "target "+message)
	}

	// Provision the read-write BucketAccess the mirror writes the target
	// through, then gate on the grant.
	access, err := r.ensureBucketAccess(ctx, targetNamespace, restoreAccessName(targetAppName), bucketReleaseName(targetAppName), deriveAccessClassName(claim.Spec.BucketClassName, false))
	if err != nil {
		return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf("failed to provision target BucketAccess: %v", err))
	}
	if !access.Status.AccessGranted {
		return r.requeueRestoreBucketWaiting(ctx, restoreJob, "TargetAccessPending", fmt.Sprintf("target BucketAccess %q not granted yet", access.Name))
	}

	// Reconstruct the destination (repo) coordinates from the self-contained
	// snapshot; take the execution knobs (image, resources) from the live
	// strategy so a strategy upgrade takes effect. Restore reads from the
	// repo and writes to the target, purging objects the snapshot does not
	// contain (--delete-extraneous), so an in-place restore is a true mirror.
	repoDest := strategyv1alpha1.BucketDestination{
		Bucket:                      snapshot.RepoBucket,
		Endpoint:                    snapshot.RepoEndpoint,
		Prefix:                      snapshot.RepoPrefix,
		Region:                      snapshot.RepoRegion,
		AccessKeyIDSecretKeyRef:     strategyv1alpha1.BucketSecretKeySelector{Name: snapshot.RepoCredentialsSecret, Key: snapshot.RepoCredentialsAccessKeyKey},
		SecretAccessKeySecretKeyRef: strategyv1alpha1.BucketSecretKeySelector{Name: snapshot.RepoCredentialsSecret, Key: snapshot.RepoCredentialsSecretKeyKey},
	}
	if snapshot.InsecureSkipVerify {
		repoDest.TLS = &strategyv1alpha1.BucketTLS{InsecureSkipVerify: true}
	}
	restoreTemplate := strategyv1alpha1.BucketTemplate{
		Destination:         repoDest,
		Image:               rendered.Image,
		AppEndpointOverride: rendered.AppEndpointOverride,
		ServerSideCopy:      snapshot.ServerSideCopy,
		Resources:           rendered.Resources,
	}
	pod := buildBucketMirrorPod(bucketModeRestore, restoreTemplate, restoreAccessName(targetAppName), snapshot.RepoPrefix, true)

	batchJob, err := r.ensureJobStrategyRestoreJob(ctx, restoreJob, targetNamespace, jobNameForRestoreJob(restoreJob),
		bucketModeRestore,
		map[string]string{
			bucketLabelMode:                         bucketModeRestore,
			backupsv1alpha1.OwningJobNameLabel:      restoreJob.Name,
			backupsv1alpha1.OwningJobNamespaceLabel: restoreJob.Namespace,
		},
		pod,
	)
	if err != nil {
		return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf("failed to ensure mirror Job: %v", err))
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
			Message: "S3 mirror restore Job completed",
		})
		if err := r.Status().Update(ctx, restoreJob); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil

	case batchv1.JobFailed:
		message := jobFailureMessage(batchJob)
		if message == "" {
			message = "S3 mirror restore Job reported Failed"
		}
		return r.markRestoreJobFailed(ctx, restoreJob, message)

	default:
		return ctrl.Result{RequeueAfter: bucketPollInterval}, nil
	}
}

// bucketRestoreParameters is the RestoreJob-side mirror of the Job driver's
// parameter round-trip: the BackupClassStrategy parameters in effect at backup
// time are re-exposed to the strategy render at restore time.
func bucketRestoreParameters(b *backupsv1alpha1.Backup) map[string]string {
	out := map[string]string{}
	for k, v := range b.Spec.DriverMetadata {
		if !strings.HasPrefix(k, bucketParamPrefix) {
			continue
		}
		if key := strings.TrimPrefix(k, bucketParamPrefix); key != "" {
			out[key] = v
		}
	}
	return out
}

// ensureBucketAccess is the RestoreJob-side mirror. The BucketAccess lives in
// the target application's namespace and persists (no controllerRef) so a
// re-restore reuses the same grant.
func (r *RestoreJobReconciler) ensureBucketAccess(ctx context.Context, namespace, name, claimName, accessClass string) (*buckettypes.BucketAccess, error) {
	existing := &buckettypes.BucketAccess{}
	err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, existing)
	if err == nil {
		return existing, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}
	desired := &buckettypes.BucketAccess{
		TypeMeta: metav1.TypeMeta{
			APIVersion: buckettypes.GroupVersion.String(),
			Kind:       "BucketAccess",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
			Labels:    map[string]string{managedByLabel: managedByValue},
		},
		Spec: buckettypes.BucketAccessSpec{
			BucketClaimName:       claimName,
			BucketAccessClassName: accessClass,
			Protocol:              bucketProtocolS3,
			CredentialsSecretName: name,
		},
	}
	if err := r.Patch(ctx, desired, client.Apply, client.FieldOwner(bucketFieldManager), client.ForceOwnership); err != nil {
		return nil, err
	}
	return desired, nil
}

// requeueRestoreBucketWaiting mirrors requeueBucketWaiting for the restore path.
func (r *RestoreJobReconciler) requeueRestoreBucketWaiting(ctx context.Context, restoreJob *backupsv1alpha1.RestoreJob, reason, message string) (ctrl.Result, error) {
	logger := getLogger(ctx)
	if strategyNotReadyDeadlineExceeded(restoreJob.Status.StartedAt) {
		return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf("%s (not satisfied within %s)", message, StrategyNotReadyDeadline))
	}
	apimeta.SetStatusCondition(&restoreJob.Status.Conditions, metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionFalse,
		Reason:  reason,
		Message: message,
	})
	if updateErr := r.Status().Update(ctx, restoreJob); updateErr != nil {
		logger.Error(updateErr, "failed to update RestoreJob status while waiting on bucket precondition")
	}
	return ctrl.Result{RequeueAfter: CredentialsProjectionRequeue}, nil
}

// ---------------------------------------------------------------------------
// Mirror Pod
// ---------------------------------------------------------------------------

// buildBucketMirrorPod assembles the PodTemplateSpec that runs the mirror. The
// container invokes the controller image's `s3-mirror` subcommand; the app-side
// bucket credentials arrive as the raw COSI BucketInfo JSON referenced from the
// provisioned BucketAccess Secret (APP_BUCKETINFO), and the repo credentials as
// AWS_* keys of the projected cozy-backups-creds Secret (REPO_*). Endpoints,
// bucket and prefix travel as non-secret args. buildJobStrategyBatchJob wraps
// the result and defaults RestartPolicy=Never.
func buildBucketMirrorPod(mode string, tmpl strategyv1alpha1.BucketTemplate, appAccessSecret, repoPrefix string, deleteExtraneous bool) *corev1.PodTemplateSpec {
	dest := tmpl.Destination
	args := []string{
		"s3-mirror",
		"--mode=" + mode,
		"--repo-endpoint=" + dest.Endpoint,
		"--repo-bucket=" + dest.Bucket,
		"--repo-prefix=" + repoPrefix,
	}
	if dest.Region != "" {
		args = append(args, "--repo-region="+dest.Region)
	}
	if tmpl.AppEndpointOverride != "" {
		args = append(args, "--app-endpoint="+tmpl.AppEndpointOverride)
	}
	if tmpl.ServerSideCopy {
		args = append(args, "--server-side")
	}
	if deleteExtraneous {
		args = append(args, "--delete-extraneous")
	}

	env := []corev1.EnvVar{
		{
			Name: "APP_BUCKETINFO",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: appAccessSecret},
				Key:                  "BucketInfo",
			}},
		},
		{
			Name: "REPO_ACCESS_KEY",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: dest.AccessKeyIDSecretKeyRef.Name},
				Key:                  dest.AccessKeyIDSecretKeyRef.Key,
			}},
		},
		{
			Name: "REPO_SECRET_KEY",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: dest.SecretAccessKeySecretKeyRef.Name},
				Key:                  dest.SecretAccessKeySecretKeyRef.Key,
			}},
		},
	}

	container := corev1.Container{
		Name:  "mirror",
		Image: tmpl.Image,
		Args:  args,
		Env:   env,
	}
	if tmpl.Resources != nil {
		container.Resources = *tmpl.Resources
	}

	var volumes []corev1.Volume
	if dest.TLS != nil {
		if dest.TLS.CASecretKeyRef != nil {
			container.Args = append(container.Args, "--ca-file="+bucketCAMountPath+"/"+bucketCAFileName)
			container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
				Name:      "s3-ca",
				MountPath: bucketCAMountPath,
				ReadOnly:  true,
			})
			volumes = append(volumes, corev1.Volume{
				Name: "s3-ca",
				VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
					SecretName: dest.TLS.CASecretKeyRef.Name,
					Items: []corev1.KeyToPath{{
						Key:  dest.TLS.CASecretKeyRef.Key,
						Path: bucketCAFileName,
					}},
				}},
			})
		} else if dest.TLS.InsecureSkipVerify {
			container.Args = append(container.Args, "--insecure")
		}
	}

	return &corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers:    []corev1.Container{container},
			Volumes:       volumes,
		},
	}
}
