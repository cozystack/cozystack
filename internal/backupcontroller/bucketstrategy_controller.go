// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"encoding/json"
	"errors"
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
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

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
	bucketModeCleanup = "cleanup"

	// bucketAllowEmptySourceAnnotation on a RestoreJob opts past the empty-source
	// refusal, letting an operator restore a legitimately empty snapshot (which
	// would otherwise be indistinguishable from a retention-pruned one).
	bucketAllowEmptySourceAnnotation = "backups.cozystack.io/allow-empty-source"

	// bucketRestoreBackupLabel stamps a restore mirror Job with the Backup name
	// it reads, so cleanupBucketBackup can hold the purge while a restore Job for
	// that Backup still has active Pods - even after its RestoreJob has gone
	// terminal or been deleted, whose Pods a background GC has not yet stopped.
	bucketRestoreBackupLabel = "backups.cozystack.io/restore-backup"

	// Driver-metadata key prefix used to round-trip BackupClassStrategy
	// parameters through the Backup artifact, mirroring the Job driver.
	bucketParamPrefix = "bucket.strategy.backups.cozystack.io/parameter/"

	// bucketPollInterval matches the Job / Altinity cadence.
	bucketPollInterval = 5 * time.Second

	// bucketMirrorDeadlineSeconds bounds a single mirror Pod attempt. Without it
	// a wedged copy (a hung S3 connection, a never-completing list) keeps the
	// mirror Pod Running forever and the BackupJob polling it never terminates;
	// the Pod deadline plus backoffLimit caps the whole run at a finite multiple.
	// Generous so a large legitimate bucket still finishes; the point is a
	// finite ceiling, not a tight SLA.
	bucketMirrorDeadlineSeconds int64 = 2 * 60 * 60

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
	// Repo credentials are referenced, never inlined. RepoCredentialsSecret is
	// the access-key Secret name; RepoCredentialsSecretKeySecret is the
	// secret-key Secret name when the two live in different Secrets, and is
	// empty when they share one (the default) - a restore then falls back to
	// RepoCredentialsSecret for both. The keys default to the AWS_* entries the
	// projector writes.
	RepoCredentialsSecret          string `json:"repoCredentialsSecret"`
	RepoCredentialsSecretKeySecret string `json:"repoCredentialsSecretKeySecret,omitempty"`
	RepoCredentialsAccessKeyKey    string `json:"repoCredentialsAccessKeyKey"`
	RepoCredentialsSecretKeyKey    string `json:"repoCredentialsSecretKeyKey"`
	// CA bundle the mirror trusts, referenced when the destination declares a
	// private CA. Empty when none was configured; a restore then rebuilds TLS
	// from InsecureSkipVerify alone.
	RepoCACertSecret   string `json:"repoCaCertSecret,omitempty"`
	RepoCACertKey      string `json:"repoCaCertKey,omitempty"`
	InsecureSkipVerify bool   `json:"insecureSkipVerify,omitempty"`
	ServerSideCopy     bool   `json:"serverSideCopy,omitempty"`
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

// isInPlaceBucketRestore reports whether a restore targets the backup's own
// source application. An in-place restore is a true mirror and PURGES objects
// the snapshot does not name; a restore into a differently-named copy target
// only merges the snapshot in. This is the single line separating the
// destructive path from the non-destructive one, so it is kept testable.
func isInPlaceBucketRestore(targetAppName string, backup *backupsv1alpha1.Backup) bool {
	return targetAppName == backup.Spec.ApplicationRef.Name
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

	// Refuse to back the repo bucket up into itself. The platform cozy-backups
	// bucket is an ordinary apps.cozystack.io/Bucket, and the default BackupClass
	// routes every Bucket to this strategy, so a BackupJob for it would mirror the
	// whole shared repo (every tenant's backups) into a prefix inside the same
	// bucket, doubling it each run.
	if claim.Status.BucketName == rendered.Destination.Bucket {
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf("source bucket %q is the backup repository bucket; refusing to back it up into itself", claim.Status.BucketName))
	}

	// Provision (idempotently) the read-only BucketAccess the mirror reads
	// the source bucket through, then gate on the COSI grant.
	access, err := r.ensureBucketAccess(ctx, j.Namespace, backupAccessName(release), claim, deriveAccessClassName(claim.Spec.BucketClassName, true))
	if err != nil {
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf("failed to provision source BucketAccess: %v", err))
	}
	if !access.Status.AccessGranted {
		return r.requeueBucketWaiting(ctx, j, "SourceAccessPending", fmt.Sprintf("source BucketAccess %q not granted yet", access.Name))
	}

	// Scope the repo prefix by the BackupJob UID, not just its name: an ad-hoc
	// BackupJob can reuse a deleted one's name, and a name-only prefix would let a
	// new run write into (and its failure-reclaim purge) an older, still-Ready
	// Backup's objects. The UID makes every run's prefix its own.
	repoPrefix := joinRepoPrefix(rendered.Destination.Prefix, j.Name+"-"+string(j.UID))
	pod := buildBucketMirrorPod(bucketModeBackup, *rendered, backupAccessName(release), repoPrefix, false, false)

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
			// A collision with a prior run's Backup is terminal - retrying cannot
			// resolve it, and this run must not report success against the stale
			// snapshot.
			if errors.Is(err, errBucketArtifactNameCollision) {
				return r.markBackupJobFailed(ctx, j, err.Error())
			}
			// Otherwise the mirror already wrote the full copy, so failing here
			// would strand it with no Backup for cleanup to key off: requeue the
			// transient artifact write (BackupRef gates re-create), bounded by the
			// same deadline as the wait helpers and surfaced through a condition
			// and Event so a persistently-rejected write is not silent.
			if strategyNotReadyDeadlineExceeded(j.Status.StartedAt) {
				return r.markBackupJobFailed(ctx, j, fmt.Sprintf("mirror completed but recording the Backup artifact did not succeed within %s: %v", StrategyNotReadyDeadline, err))
			}
			if r.Recorder != nil {
				r.Recorder.Eventf(j, corev1.EventTypeWarning, "ArtifactWritePending", "mirror completed but the Backup artifact is not yet recorded: %v", err)
			}
			apimeta.SetStatusCondition(&j.Status.Conditions, metav1.Condition{
				Type:    "Ready",
				Status:  metav1.ConditionFalse,
				Reason:  "ArtifactWritePending",
				Message: fmt.Sprintf("mirror completed; recording Backup artifact: %v", err),
			})
			if updateErr := r.Status().Update(ctx, j); updateErr != nil {
				getLogger(ctx).Error(updateErr, "failed to update BackupJob status after artifact write error")
			}
			return ctrl.Result{RequeueAfter: bucketPollInterval}, nil
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
		// streamCopy writes objects one at a time, so a mirror that dies partway
		// (deadline, OOM, S3 error) has already put a prefix's worth into
		// cozy-backups, and no Backup artifact is created on failure for
		// cleanupBucketBackup to key off. Reclaim that partial prefix best-effort
		// before the BackupJob goes terminal.
		r.reclaimFailedBucketBackup(ctx, j, *rendered, repoPrefix)
		return r.markBackupJobFailed(ctx, j, message)

	default:
		return ctrl.Result{RequeueAfter: bucketPollInterval}, nil
	}
}

// reclaimFailedBucketBackup best-effort deletes the partial copy a failed backup
// mirror may have left under repoPrefix. It runs an s3-mirror --mode=delete Job
// owner-referenced to the BackupJob (so it is GC'd with it) and does not block
// the BackupJob's terminal transition; the repo credentials the projector wrote
// for the backup are still present in the namespace.
func (r *BackupJobReconciler) reclaimFailedBucketBackup(ctx context.Context, j *backupsv1alpha1.BackupJob, rendered strategyv1alpha1.BucketTemplate, repoPrefix string) {
	logger := getLogger(ctx)
	snap := &bucketBackupSnapshot{
		RepoBucket:                  rendered.Destination.Bucket,
		RepoEndpoint:                rendered.Destination.Endpoint,
		RepoPrefix:                  repoPrefix,
		RepoRegion:                  rendered.Destination.Region,
		RepoCredentialsSecret:       rendered.Destination.AccessKeyIDSecretKeyRef.Name,
		RepoCredentialsAccessKeyKey: rendered.Destination.AccessKeyIDSecretKeyRef.Key,
		RepoCredentialsSecretKeyKey: rendered.Destination.SecretAccessKeySecretKeyRef.Key,
	}
	if sk := rendered.Destination.SecretAccessKeySecretKeyRef.Name; sk != snap.RepoCredentialsSecret {
		snap.RepoCredentialsSecretKeySecret = sk
	}
	if tls := rendered.Destination.TLS; tls != nil {
		snap.InsecureSkipVerify = tls.InsecureSkipVerify
		if ca := tls.CASecretKeyRef; ca != nil {
			snap.RepoCACertSecret = ca.Name
			snap.RepoCACertKey = ca.Key
		}
	}
	pod := buildBucketCleanupPod(snap, rendered.Image)
	desired := buildJobStrategyBatchJob(j.Namespace, j.Name+"-reclaim", map[string]string{bucketLabelMode: bucketModeCleanup}, pod)
	if err := controllerutil.SetControllerReference(j, desired, r.Scheme); err != nil {
		r.warnReclaimNotStarted(j, repoPrefix, err)
		logger.Debug("skipping partial-backup reclaim: cannot set controller reference", "backupjob", j.Name, "err", err)
		return
	}
	if err := r.Create(ctx, desired); err != nil && !apierrors.IsAlreadyExists(err) {
		r.warnReclaimNotStarted(j, repoPrefix, err)
		logger.Debug("partial-backup reclaim Job not created (best-effort)", "backupjob", j.Name, "err", err)
	}
}

// warnReclaimNotStarted surfaces a failed partial-backup reclaim on the
// BackupJob, matching releaseBucketCleanup's ArtifactNotDeleted signal so a
// leaked partial copy is visible rather than only logged at debug level.
func (r *BackupJobReconciler) warnReclaimNotStarted(j *backupsv1alpha1.BackupJob, repoPrefix string, err error) {
	if r.Recorder != nil {
		r.Recorder.Eventf(j, corev1.EventTypeWarning, "ReclaimNotStarted", "partial backup copy under %s left in the repo bucket: %v", repoPrefix, err)
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

// reconcileBucketAccess provisions the deterministic-named COSI BucketAccess the
// mirror consumes, via server-side apply. An existing object is reused only when
// it carries the driver's managed-by label AND its spec still matches: a
// same-named object without the label belongs to someone else and is refused
// (COSI would otherwise populate a Secret for a claim/class the driver never
// asked for, while the mirror reads the deterministic Secret), and an
// owned-but-drifted object is re-applied back to the desired spec. It is
// owner-referenced to the app's BucketClaim so garbage collection reaps it when
// the app is deleted, rather than leaking a Terminating orphan whose stale
// credentials a same-named app would silently reuse.
func reconcileBucketAccess(ctx context.Context, c client.Client, namespace, name string, claim *buckettypes.BucketClaim, accessClass string) (*buckettypes.BucketAccess, error) {
	owner := metav1.OwnerReference{
		APIVersion: buckettypes.GroupVersion.String(),
		Kind:       "BucketClaim",
		Name:       claim.Name,
		UID:        claim.UID,
	}
	desired := &buckettypes.BucketAccess{
		TypeMeta: metav1.TypeMeta{
			APIVersion: buckettypes.GroupVersion.String(),
			Kind:       "BucketAccess",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       namespace,
			Name:            name,
			Labels:          map[string]string{managedByLabel: managedByValue},
			OwnerReferences: []metav1.OwnerReference{owner},
		},
		Spec: buckettypes.BucketAccessSpec{
			BucketClaimName:       claim.Name,
			BucketAccessClassName: accessClass,
			Protocol:              bucketProtocolS3,
			CredentialsSecretName: name,
		},
	}

	existing := &buckettypes.BucketAccess{}
	err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, existing)
	switch {
	case err == nil:
		if existing.Labels[managedByLabel] != managedByValue {
			return nil, fmt.Errorf("BucketAccess %s/%s exists but is not managed by the backup driver (missing %s=%s); refusing to reuse it", namespace, name, managedByLabel, managedByValue)
		}
		if existing.Spec == desired.Spec && hasOwnerUID(existing, claim.UID) {
			return existing, nil
		}
		// Owned but drifted, or created before the ownerRef existed: fall through
		// and re-apply the desired spec (which backfills the ownerRef).
	case apierrors.IsNotFound(err):
		// Fall through and create via apply.
	default:
		return nil, err
	}

	if err := c.Patch(ctx, desired, client.Apply, client.FieldOwner(bucketFieldManager), client.ForceOwnership); err != nil {
		return nil, err
	}
	return desired, nil
}

func hasOwnerUID(obj metav1.Object, uid types.UID) bool {
	for _, o := range obj.GetOwnerReferences() {
		if o.UID == uid {
			return true
		}
	}
	return false
}

// ensureBucketAccess is the BackupJob-side entry point to reconcileBucketAccess.
func (r *BackupJobReconciler) ensureBucketAccess(ctx context.Context, namespace, name string, claim *buckettypes.BucketClaim, accessClass string) (*buckettypes.BucketAccess, error) {
	return reconcileBucketAccess(ctx, r.Client, namespace, name, claim, accessClass)
}

// createBucketBackupArtifact materialises a Cozystack Backup with the restore
// snapshot on status.underlyingResources (self-contained repo coordinates +
// credentials Secret name) and the strategy parameters on driverMetadata.
// Follows the MongoDB artifact pattern - Create persists status because the
// core Backup type carries no status subresource.
// errBucketArtifactNameCollision marks a Backup whose name already belongs to a
// different run. A Backup carries no ownerRef to its BackupJob and outlives it,
// so a reused BackupJob name finds a stale same-named Backup; the run must fail
// rather than report success against that older snapshot.
var errBucketArtifactNameCollision = errors.New("a Backup of this name already exists for a different run")

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
	// Record the secret-key Secret name only when it differs from the
	// access-key one, so old readers that ignore the field lose nothing.
	if sk := rendered.Destination.SecretAccessKeySecretKeyRef.Name; sk != snapshot.RepoCredentialsSecret {
		snapshot.RepoCredentialsSecretKeySecret = sk
	}
	if rendered.Destination.TLS != nil {
		snapshot.InsecureSkipVerify = rendered.Destination.TLS.InsecureSkipVerify
		if ca := rendered.Destination.TLS.CASecretKeyRef; ca != nil {
			snapshot.RepoCACertSecret = ca.Name
			snapshot.RepoCACertKey = ca.Key
		}
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
		// The repo prefix is scoped by BackupJob UID, so a Backup this same run
		// created points at repoPrefix (a genuine idempotent retry) while a stale
		// one from a prior run with the reused name points elsewhere. Returning
		// the latter would report Succeeded against the old snapshot and leave the
		// fresh copy this run wrote unreferenced in the shared repo bucket.
		existingPrefix := ""
		if snap, decErr := unmarshalBucketSnapshot(existing.Status.UnderlyingResources); decErr == nil && snap != nil {
			existingPrefix = snap.RepoPrefix
		}
		if existingPrefix != repoPrefix {
			return nil, fmt.Errorf("%w: Backup %q points at repo prefix %q, this run wrote %q", errBucketArtifactNameCollision, backup.Name, existingPrefix, repoPrefix)
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

	// Refuse to START a restore from a Backup that is being deleted: its repo
	// objects may be mid-purge (cleanupBucketBackup), and an in-place restore
	// listing a prefix emptied underneath it would mirror a subset and then
	// delete the rest of the live bucket. A restore whose mirror Job already
	// exists is left to run to a terminal state instead of being abandoned mid-
	// write; cleanupBucketBackup holds the purge (via the restore-backup label)
	// while that Job still has active Pods, so the two never overlap.
	if !backup.DeletionTimestamp.IsZero() {
		mirrorJob := &batchv1.Job{}
		err := r.Get(ctx, types.NamespacedName{Namespace: restoreJob.Namespace, Name: jobNameForRestoreJob(restoreJob)}, mirrorJob)
		if apierrors.IsNotFound(err) {
			return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf("Backup %q is being deleted; its objects may be mid-reclaim, so a restore is refused", backup.Name))
		}
		if err != nil {
			return ctrl.Result{}, err
		}
		// else: the mirror is already running; fall through to poll it to terminal.
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
	access, err := r.ensureBucketAccess(ctx, targetNamespace, restoreAccessName(targetAppName), claim, deriveAccessClassName(claim.Spec.BucketClassName, false))
	if err != nil {
		return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf("failed to provision target BucketAccess: %v", err))
	}
	if !access.Status.AccessGranted {
		return r.requeueRestoreBucketWaiting(ctx, restoreJob, "TargetAccessPending", fmt.Sprintf("target BucketAccess %q not granted yet", access.Name))
	}

	// Reconstruct the destination (repo) coordinates from the self-contained
	// snapshot; take the execution knobs (image, resources) from the live
	// strategy so a strategy upgrade takes effect. Restore reads from the
	// repo and writes to the target. An in-place restore (target == the
	// backup's source app) purges objects the snapshot does not contain so it
	// is a true mirror; a restore-as-copy into a different app merges the
	// snapshot in without deleting objects the copy target already holds.
	inPlace := isInPlaceBucketRestore(targetAppName, backup)
	// The secret-key Secret name falls back to the access-key one for snapshots
	// that predate the separate field (and for the common single-Secret case).
	secretKeySecret := snapshot.RepoCredentialsSecretKeySecret
	if secretKeySecret == "" {
		secretKeySecret = snapshot.RepoCredentialsSecret
	}
	repoDest := strategyv1alpha1.BucketDestination{
		Bucket:                      snapshot.RepoBucket,
		Endpoint:                    snapshot.RepoEndpoint,
		Prefix:                      snapshot.RepoPrefix,
		Region:                      snapshot.RepoRegion,
		AccessKeyIDSecretKeyRef:     strategyv1alpha1.BucketSecretKeySelector{Name: snapshot.RepoCredentialsSecret, Key: snapshot.RepoCredentialsAccessKeyKey},
		SecretAccessKeySecretKeyRef: strategyv1alpha1.BucketSecretKeySelector{Name: secretKeySecret, Key: snapshot.RepoCredentialsSecretKeyKey},
	}
	if snapshot.InsecureSkipVerify || snapshot.RepoCACertSecret != "" {
		repoDest.TLS = &strategyv1alpha1.BucketTLS{InsecureSkipVerify: snapshot.InsecureSkipVerify}
		if snapshot.RepoCACertSecret != "" {
			repoDest.TLS.CASecretKeyRef = &strategyv1alpha1.BucketSecretKeySelector{
				Name: snapshot.RepoCACertSecret,
				Key:  snapshot.RepoCACertKey,
			}
		}
	}
	restoreTemplate := strategyv1alpha1.BucketTemplate{
		Destination:         repoDest,
		Image:               rendered.Image,
		AppEndpointOverride: rendered.AppEndpointOverride,
		ServerSideCopy:      snapshot.ServerSideCopy,
		Resources:           rendered.Resources,
		TimeoutSeconds:      rendered.TimeoutSeconds,
	}
	// An operator restoring a legitimately empty snapshot opts past the
	// empty-source refusal with an annotation on the RestoreJob (the only
	// supported producer of --allow-empty-source).
	allowEmptySource := restoreJob.Annotations[bucketAllowEmptySourceAnnotation] == "true"
	pod := buildBucketMirrorPod(bucketModeRestore, restoreTemplate, restoreAccessName(targetAppName), snapshot.RepoPrefix, inPlace, allowEmptySource)

	batchJob, err := r.ensureJobStrategyRestoreJob(ctx, restoreJob, targetNamespace, jobNameForRestoreJob(restoreJob),
		bucketModeRestore,
		map[string]string{
			bucketLabelMode:                         bucketModeRestore,
			bucketRestoreBackupLabel:                backup.Name,
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

// ensureBucketAccess is the RestoreJob-side entry point to
// reconcileBucketAccess. The BucketAccess lives in the target application's
// namespace and is owner-referenced to its BucketClaim so a re-restore reuses
// the grant while app deletion still GCs it.
func (r *RestoreJobReconciler) ensureBucketAccess(ctx context.Context, namespace, name string, claim *buckettypes.BucketClaim, accessClass string) (*buckettypes.BucketAccess, error) {
	return reconcileBucketAccess(ctx, r.Client, namespace, name, claim, accessClass)
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
func buildBucketMirrorPod(mode string, tmpl strategyv1alpha1.BucketTemplate, appAccessSecret, repoPrefix string, deleteExtraneous, allowEmptySource bool) *corev1.PodTemplateSpec {
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
	if allowEmptySource {
		args = append(args, "--allow-empty-source")
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
		Name:            "mirror",
		Image:           tmpl.Image,
		Args:            args,
		Env:             env,
		SecurityContext: restrictedSecurityContext(),
	}
	if tmpl.Resources != nil {
		container.Resources = *tmpl.Resources
	}

	var volumes []corev1.Volume
	if dest.TLS != nil {
		// The two TLS knobs are independent: an admin may point at a private CA
		// AND opt out of verification. The CA mount is optional and unprojected,
		// so gating --insecure behind its absence would silently ignore the
		// explicit opt-out; emit both when both are set.
		if dest.TLS.CASecretKeyRef != nil {
			container.Args = append(container.Args, "--ca-file="+bucketCAMountPath+"/"+bucketCAFileName)
			container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
				Name:      "s3-ca",
				MountPath: bucketCAMountPath,
				ReadOnly:  true,
			})
			volumes = append(volumes, bucketCAVolume(dest.TLS.CASecretKeyRef))
		}
		if dest.TLS.InsecureSkipVerify {
			container.Args = append(container.Args, "--insecure")
		}
	}

	deadline := bucketMirrorDeadlineSeconds
	if tmpl.TimeoutSeconds != nil && *tmpl.TimeoutSeconds > 0 {
		deadline = *tmpl.TimeoutSeconds
	}
	return &corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			RestartPolicy:         corev1.RestartPolicyNever,
			ActiveDeadlineSeconds: &deadline,
			Containers:            []corev1.Container{container},
			Volumes:               volumes,
		},
	}
}

// restrictedSecurityContext hardens the one-shot mirror/cleanup containers the
// way the sibling default strategies harden theirs (strategy-redis/kafka):
// no privilege escalation, every capability dropped, the runtime seccomp profile.
func restrictedSecurityContext() *corev1.SecurityContext {
	no := false
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: &no,
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}

// bucketCAVolume mounts the S3 CA bundle optionally: nothing projects an
// externally-managed CA Secret into the tenant namespace, so a required mount
// would wedge the Pod on FailedMount for the whole deadline. Absent, the mirror
// falls through to the system trust store (a private-CA endpoint then fails the
// handshake fast, which is the same signal).
func bucketCAVolume(ref *strategyv1alpha1.BucketSecretKeySelector) corev1.Volume {
	optional := true
	return corev1.Volume{
		Name: "s3-ca",
		VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
			SecretName: ref.Name,
			Optional:   &optional,
			Items:      []corev1.KeyToPath{{Key: ref.Key, Path: bucketCAFileName}},
		}},
	}
}

// buildBucketCleanupPod builds the one-shot Pod that reclaims a backup's repo
// objects (`s3-mirror --mode=delete`) when its Backup is deleted. Only the repo
// side matters to a purge, so no BucketAccess or app credentials are wired.
func buildBucketCleanupPod(snapshot *bucketBackupSnapshot, image string) *corev1.PodTemplateSpec {
	args := []string{
		"s3-mirror",
		"--mode=delete",
		"--repo-endpoint=" + snapshot.RepoEndpoint,
		"--repo-bucket=" + snapshot.RepoBucket,
		"--repo-prefix=" + snapshot.RepoPrefix,
	}
	if snapshot.RepoRegion != "" {
		args = append(args, "--repo-region="+snapshot.RepoRegion)
	}

	secretKeySecret := snapshot.RepoCredentialsSecretKeySecret
	if secretKeySecret == "" {
		secretKeySecret = snapshot.RepoCredentialsSecret
	}
	env := []corev1.EnvVar{
		{
			Name: "REPO_ACCESS_KEY",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: snapshot.RepoCredentialsSecret},
				Key:                  snapshot.RepoCredentialsAccessKeyKey,
			}},
		},
		{
			Name: "REPO_SECRET_KEY",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: secretKeySecret},
				Key:                  snapshot.RepoCredentialsSecretKeyKey,
			}},
		},
	}

	container := corev1.Container{Name: "cleanup", Image: image, Args: args, Env: env, SecurityContext: restrictedSecurityContext()}

	var volumes []corev1.Volume
	if snapshot.RepoCACertSecret != "" {
		container.Args = append(container.Args, "--ca-file="+bucketCAMountPath+"/"+bucketCAFileName)
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
			Name:      "s3-ca",
			MountPath: bucketCAMountPath,
			ReadOnly:  true,
		})
		volumes = append(volumes, bucketCAVolume(&strategyv1alpha1.BucketSecretKeySelector{Name: snapshot.RepoCACertSecret, Key: snapshot.RepoCACertKey}))
	}
	if snapshot.InsecureSkipVerify {
		container.Args = append(container.Args, "--insecure")
	}

	deadline := bucketMirrorDeadlineSeconds
	return &corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			RestartPolicy:         corev1.RestartPolicyNever,
			ActiveDeadlineSeconds: &deadline,
			Containers:            []corev1.Container{container},
			Volumes:               volumes,
		},
	}
}

// cleanupBucketBackup deletes a Backup's mirrored repo objects when the Backup
// is removed. Like the Redis / Rabbitmq drivers the Bucket driver OWNS its
// artifact (the objects under the repo prefix; no engine retention prunes them),
// so it runs a one-shot delete Job and WAITS for it before the finalizer is
// removed, leaving nothing orphaned in cozy-backups. It fails open - releasing
// the Backup and leaving the objects - when the delete cannot proceed (namespace
// terminating, strategy gone, credentials unprojectable) rather than wedging the
// Backup Terminating forever; `backups.cozystack.io/skip-artifact-cleanup` is the
// operator escape hatch.
func (r *BackupReconciler) cleanupBucketBackup(ctx context.Context, backup *backupsv1alpha1.Backup) (ctrl.Result, error) {
	logger := getLogger(ctx)
	jobName := backup.Name + "-cleanup"

	if backup.Annotations[redisSkipArtifactCleanupAnnotation] == "true" {
		existing := &batchv1.Job{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: backup.Namespace, Name: jobName}, existing); err == nil &&
			metav1.IsControlledBy(existing, backup) && existing.Labels[bucketLabelMode] == bucketModeCleanup {
			_ = r.deleteBucketCleanupJob(ctx, existing)
		}
		logger.Debug("skipping Bucket artifact cleanup per annotation; objects left in the repo bucket", "backup", backup.Name)
		return ctrl.Result{}, nil
	}

	snapshot, err := unmarshalBucketSnapshot(backup.Status.UnderlyingResources)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("decode Backup snapshot: %w", err)
	}
	if snapshot == nil || snapshot.RepoBucket == "" || snapshot.RepoPrefix == "" {
		// A Backup written before the snapshot recorded repo coordinates names
		// nothing to delete.
		return ctrl.Result{}, nil
	}

	// Refuse to purge while a RestoreJob is still reading this Backup: an in-place
	// restore lists snapshot.RepoPrefix to build its keep-set, and a concurrent
	// purge removing a key before the listing reaches it drops that key from the
	// set, so deleteUnseen then wipes the live copy too. Wait for the restore to
	// reach a terminal phase before reclaiming.
	if active, aerr := r.restoreJobActiveForBackup(ctx, backup); aerr != nil {
		return ctrl.Result{}, aerr
	} else if active {
		logger.Debug("holding Bucket artifact cleanup while a RestoreJob reads the Backup", "backup", backup.Name)
		return ctrl.Result{RequeueAfter: bucketPollInterval}, nil
	}

	strategy := &strategyv1alpha1.Bucket{}
	if err := r.Get(ctx, client.ObjectKey{Name: backup.Spec.StrategyRef.Name}, strategy); err != nil {
		if apierrors.IsNotFound(err) {
			return r.releaseBucketCleanup(ctx, backup, snapshot.RepoPrefix, "strategy CR is gone"), nil
		}
		return ctrl.Result{}, err
	}

	job := &batchv1.Job{}
	err = r.Get(ctx, types.NamespacedName{Namespace: backup.Namespace, Name: jobName}, job)
	switch {
	case apierrors.IsNotFound(err):
		terminating, nsErr := r.namespaceTerminating(ctx, backup.Namespace)
		if nsErr != nil {
			return ctrl.Result{}, nsErr
		}
		if terminating {
			return r.releaseBucketCleanup(ctx, backup, snapshot.RepoPrefix, "namespace is terminating"), nil
		}
		if perr := ProjectBackupCredentials(ctx, r.Client, r.CredentialsConfig, backup.Namespace); perr != nil {
			return r.releaseBucketCleanup(ctx, backup, snapshot.RepoPrefix, fmt.Sprintf("cannot project credentials: %v", perr)), nil
		}
		pod := buildBucketCleanupPod(snapshot, strategy.Spec.Template.Image)
		desired := buildJobStrategyBatchJob(backup.Namespace, jobName, map[string]string{bucketLabelMode: bucketModeCleanup}, pod)
		if cerr := controllerutil.SetControllerReference(backup, desired, r.Scheme); cerr != nil {
			return ctrl.Result{}, fmt.Errorf("set controller reference on cleanup Job: %w", cerr)
		}
		if cerr := r.Create(ctx, desired); cerr != nil && !apierrors.IsAlreadyExists(cerr) {
			// Forbidden (namespace went Terminating after the check) or Invalid
			// (a long Backup name overflows the 63-char Job name) cannot be fixed
			// by retrying; release. Other errors are transient - requeue.
			if apierrors.IsForbidden(cerr) || apierrors.IsInvalid(cerr) {
				return r.releaseBucketCleanup(ctx, backup, snapshot.RepoPrefix, fmt.Sprintf("cannot create cleanup Job: %v", cerr)), nil
			}
			return ctrl.Result{}, cerr
		}
		return ctrl.Result{RequeueAfter: bucketPollInterval}, nil
	case err != nil:
		return ctrl.Result{}, err
	}

	// Only a Job this Backup controls and stamped as a cleanup Job may drive the
	// release decision below: a same-named Job belonging to someone else (or a
	// stale one from a prior identically-named Backup) must not let us drop the
	// finalizer without ever purging the objects.
	if !metav1.IsControlledBy(job, backup) || job.Labels[bucketLabelMode] != bucketModeCleanup {
		return ctrl.Result{}, fmt.Errorf("Job %s/%s exists but is not this Backup's cleanup Job; refusing to act on it", backup.Namespace, jobName)
	}

	if !job.DeletionTimestamp.IsZero() {
		// A prior failed attempt is being collected; wait for it to go, then the
		// NotFound branch recreates a fresh one.
		return ctrl.Result{RequeueAfter: bucketPollInterval}, nil
	}
	switch jobConditionState(job) {
	case batchv1.JobComplete:
		_ = r.deleteBucketCleanupJob(ctx, job)
		logger.Debug("Bucket backup objects deleted", "backup", backup.Name, "prefix", snapshot.RepoPrefix)
		return ctrl.Result{}, nil
	case batchv1.JobFailed:
		_ = r.deleteBucketCleanupJob(ctx, job)
		logger.Debug("Bucket cleanup Job failed; retrying", "backup", backup.Name)
		return ctrl.Result{RequeueAfter: bucketPollInterval}, nil
	default:
		return ctrl.Result{RequeueAfter: bucketPollInterval}, nil
	}
}

func (r *BackupReconciler) deleteBucketCleanupJob(ctx context.Context, job *batchv1.Job) error {
	policy := metav1.DeletePropagationBackground
	return client.IgnoreNotFound(r.Delete(ctx, job, &client.DeleteOptions{PropagationPolicy: &policy}))
}

// restoreJobActiveForBackup reports whether a non-terminal RestoreJob in the
// Backup's namespace still references it, so its repo objects must not be
// reclaimed out from under an in-flight restore.
func (r *BackupReconciler) restoreJobActiveForBackup(ctx context.Context, backup *backupsv1alpha1.Backup) (bool, error) {
	// A non-terminal RestoreJob referencing this Backup is still going to read it.
	list := &backupsv1alpha1.RestoreJobList{}
	if err := r.List(ctx, list, client.InNamespace(backup.Namespace)); err != nil {
		return false, err
	}
	for i := range list.Items {
		rj := &list.Items[i]
		if rj.Spec.BackupRef.Name != backup.Name {
			continue
		}
		switch rj.Status.Phase {
		case backupsv1alpha1.RestoreJobPhaseSucceeded, backupsv1alpha1.RestoreJobPhaseFailed:
			continue
		default:
			return true, nil
		}
	}

	// A restore mirror Job with live Pods is still writing the target even if its
	// RestoreJob has been marked Failed or deleted (its Pods outlive the object
	// until a background GC stops them), so hold the purge while any remains.
	jobs := &batchv1.JobList{}
	if err := r.List(ctx, jobs, client.InNamespace(backup.Namespace), client.MatchingLabels{bucketRestoreBackupLabel: backup.Name}); err != nil {
		return false, err
	}
	for i := range jobs.Items {
		if jobs.Items[i].Status.Active > 0 {
			return true, nil
		}
	}
	return false, nil
}

func (r *BackupReconciler) releaseBucketCleanup(ctx context.Context, backup *backupsv1alpha1.Backup, prefix, reason string) ctrl.Result {
	getLogger(ctx).Info("releasing Backup without deleting its repo objects", "backup", backup.Name, "prefix", prefix, "reason", reason)
	if r.Recorder != nil {
		r.Recorder.Eventf(backup, corev1.EventTypeWarning, "ArtifactNotDeleted", "left objects under %s in the repo bucket: %s", prefix, reason)
	}
	return ctrl.Result{}
}
