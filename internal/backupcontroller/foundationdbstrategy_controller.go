// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

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
	"github.com/cozystack/cozystack/internal/backupcontroller/foundationdbapp"
	"github.com/cozystack/cozystack/internal/backupcontroller/foundationdbtypes"
	"github.com/cozystack/cozystack/internal/template"
)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const (
	// foundationdbAppKind / foundationdbAppPrefix mirror the
	// foundationdb ApplicationDefinition (release.prefix=foundationdb-).
	// The cozystack foundationdb-rd renders the HelmRelease with
	// releaseName = "foundationdb-" + appName, and the app chart's
	// templates/cluster.yaml materialises the operator-side
	// apps.foundationdb.org/FoundationDBCluster CR with
	// metadata.name set to .Release.Name. So the driver must look up
	// the operator-side cluster by the prefixed name.
	foundationdbAppKind   = "FoundationDB"
	foundationdbAppPrefix = "foundationdb-"

	// Driver-metadata keys persisted on Cozystack Backup artefacts.
	// Restore path reads these to drive the operator FoundationDBRestore CR.
	foundationdbBackupNameKey      = "apps.foundationdb.org/backup-name"
	foundationdbBackupNamespaceKey = "apps.foundationdb.org/backup-namespace"
	foundationdbAccountNameKey     = "apps.foundationdb.org/account-name"
	foundationdbBucketKey          = "apps.foundationdb.org/bucket"
	foundationdbBlobBackupNameKey  = "apps.foundationdb.org/blob-backup-name"
	foundationdbStorageURIKey      = "apps.foundationdb.org/storage-uri"

	// Polling cadence for the operator FoundationDBBackup/Restore lifecycle.
	foundationdbPollInterval = 10 * time.Second

	// Wall-clock cap on a BackupJob waiting for the operator
	// FoundationDBBackup to land its first restorable snapshot. The
	// agent has to start, contact the cluster, and write a full
	// snapshot to blob storage; a permanently-stuck backup must not
	// pin the BackupJob in Running and wedge the Plan controller's
	// queue. CNPG/MariaDB use 30m for the same reason; FDB needs a
	// touch more headroom because the first snapshot for an empty
	// cluster still has to do range-file rotation through the
	// backup_agent.
	foundationdbDefaultBackupDeadline = 45 * time.Minute

	// Default deadline on a RestoreJob waiting for the operator
	// FoundationDBRestore to terminate. Tenants override via
	// spec.options.restoreTimeoutSeconds.
	foundationdbDefaultRestoreDeadline = 30 * time.Minute

	// foundationdbBackupSnapshotKind is the Kind stamped onto the snapshot
	// persisted in Backup.status.underlyingResources. Carries the rendered
	// strategy parameters and the per-run BackupName so restore-time
	// re-rendering produces deterministic values even after the operator-side
	// FoundationDBBackup has been reaped.
	//
	// Snapshot schema v1. Bump the apiVersion (or introduce a Version field
	// on foundationdbBackupSnapshot) the moment the on-disk shape changes
	// in a way that breaks older readers - decoders deployed against a
	// mismatched apiVersion must be able to detect the mismatch and refuse
	// to use the snapshot rather than silently mis-interpret it.
	foundationdbBackupSnapshotKind = "FoundationDBBackupSnapshot"
)

// foundationdbBackupSnapshotAPIVersion is the apiVersion stamped onto the
// snapshot. Borrows the Cozystack backups group so the field is self-typed
// within the existing API surface. See foundationdbBackupSnapshotKind for
// the schema-versioning contract.
var foundationdbBackupSnapshotAPIVersion = backupsv1alpha1.GroupVersion.String()

// foundationdbClusterNameForApp returns the
// apps.foundationdb.org/FoundationDBCluster CR name for a cozystack
// FoundationDB application instance. The mapping mirrors the foundationdb
// ApplicationDefinition (release.prefix=foundationdb-).
func foundationdbClusterNameForApp(appName string) string {
	return foundationdbAppPrefix + appName
}

// validateFoundationDBApplicationRef rejects ApplicationRefs that name a
// Kind/APIGroup the FoundationDB driver does not own. The driver assumes
// apps.cozystack.io/FoundationDB; without this gate a ref like
// other.example.com/FoundationDB would be accepted by the Kind check alone
// and then reconciled against the wrong CRD via the typed client.
func validateFoundationDBApplicationRef(ref corev1.TypedLocalObjectReference) error {
	if ref.Kind != foundationdbAppKind {
		return fmt.Errorf("FoundationDB strategy supports applicationRef.kind=%q, got %q", foundationdbAppKind, ref.Kind)
	}
	apiGroup := ""
	if ref.APIGroup != nil {
		apiGroup = *ref.APIGroup
	}
	if apiGroup != "" && apiGroup != foundationdbapp.GroupName {
		return fmt.Errorf("FoundationDB strategy supports applicationRef.apiGroup=%q, got %q", foundationdbapp.GroupName, apiGroup)
	}
	return nil
}

// ---------------------------------------------------------------------------
// BackupJob path
// ---------------------------------------------------------------------------

func (r *BackupJobReconciler) reconcileFoundationDB(ctx context.Context, j *backupsv1alpha1.BackupJob, resolved *ResolvedBackupConfig) (ctrl.Result, error) {
	logger := getLogger(ctx)
	logger.Debug("reconciling FoundationDB strategy", "backupjob", j.Name, "phase", j.Status.Phase)

	if j.Status.Phase == backupsv1alpha1.BackupJobPhaseSucceeded ||
		j.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
		return ctrl.Result{}, nil
	}

	if err := validateFoundationDBApplicationRef(j.Spec.ApplicationRef); err != nil {
		return r.markBackupJobFailed(ctx, j, err.Error())
	}

	if j.Status.StartedAt == nil {
		// Refetch the latest persisted state before writing StartedAt: a
		// stale informer cache that returns StartedAt==nil after we already
		// persisted it would otherwise let the deadline gate slide forward
		// on every poll. Same idempotency pattern as the CNPG/MariaDB
		// drivers.
		fresh := &backupsv1alpha1.BackupJob{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: j.Namespace, Name: j.Name}, fresh); err != nil {
			return ctrl.Result{}, err
		}
		if fresh.Status.StartedAt != nil {
			j.Status.StartedAt = fresh.Status.StartedAt
		} else {
			base := fresh.DeepCopy()
			now := metav1.Now()
			fresh.Status.StartedAt = &now
			if err := r.Status().Patch(ctx, fresh, client.MergeFrom(base)); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: foundationdbPollInterval}, nil
		}
	}

	strategy := &strategyv1alpha1.FoundationDB{}
	if err := r.Get(ctx, client.ObjectKey{Name: resolved.StrategyRef.Name}, strategy); err != nil {
		if apierrors.IsNotFound(err) {
			return r.requeueStrategyNotReady(ctx, j, resolved.StrategyRef.Name)
		}
		return ctrl.Result{}, err
	}

	app, err := r.getFoundationDBApp(ctx, j.Namespace, j.Spec.ApplicationRef.Name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.markBackupJobFailed(ctx, j, fmt.Sprintf("FoundationDB application not found: %s/%s", j.Namespace, j.Spec.ApplicationRef.Name))
		}
		return ctrl.Result{}, err
	}

	rendered, err := renderFoundationDBTemplate(strategy.Spec.Template, app, resolved.Parameters)
	if err != nil {
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf("failed to template FoundationDB strategy: %v", err))
	}

	// Operator-side cluster carries the prefixed release name (see
	// foundationdbClusterNameForApp); verify it exists and is healthy
	// before we ask the operator to back it up. Returning a transient
	// "NotReady" surfaces a transient condition rather than a terminal
	// failure: the chart may still be rendering on a fresh app.
	clusterName := foundationdbClusterNameForApp(j.Spec.ApplicationRef.Name)
	cluster := &foundationdbtypes.FoundationDBCluster{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: j.Namespace, Name: clusterName}, cluster); err != nil {
		if apierrors.IsNotFound(err) {
			return r.requeueWithReason(ctx, j, "FoundationDBClusterNotReady",
				fmt.Sprintf("waiting for apps.foundationdb.org/FoundationDBCluster %s/%s to exist", j.Namespace, clusterName))
		}
		return ctrl.Result{}, err
	}
	if !cluster.Status.Health.Available {
		return r.requeueWithReason(ctx, j, "FoundationDBClusterNotAvailable",
			fmt.Sprintf("waiting for apps.foundationdb.org/FoundationDBCluster %s/%s to become available", j.Namespace, clusterName))
	}

	// Per BackupJob the driver materialises a discrete FoundationDBBackup
	// with backupName = j.Name; the operator only permits one running
	// backup directory per cluster, so any prior running backups for the
	// same cluster must be stopped first.
	if cluster.Spec.Version == "" {
		return r.requeueWithReason(ctx, j, "FoundationDBClusterVersionUnknown",
			fmt.Sprintf("apps.foundationdb.org/FoundationDBCluster %s/%s does not yet report spec.version", j.Namespace, clusterName))
	}

	// Fast-fail when a chart-rendered FoundationDBBackup is still
	// Running against the same cluster (legacy `backup.enabled=true`
	// flow). The operator only permits one running backup directory
	// per cluster, and the chart-rendered CR carries no driver labels
	// for stopOtherFoundationDBBackups to find, so a silent migration
	// would split the backup stream and corrupt both. Diagnostic is
	// terminal because letting the BackupJob spin for 45 minutes would
	// mask the chart-side cleanup tenants need to do.
	if conflicting, err := r.findConflictingInChartBackup(ctx, j.Namespace, clusterName); err != nil {
		return ctrl.Result{}, err
	} else if conflicting != "" {
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf(
			"conflicting in-chart FoundationDBBackup %s/%s is Running against cluster %q; disable the chart's backup.enabled and delete that CR before using the BackupClass flow",
			j.Namespace, conflicting, clusterName))
	}

	// Resolve every Secret the rendered PodTemplateSpec references before
	// the backup_agent Deployment starts, so a tenant who forgot to
	// pre-create the per-app blob-credentials Secret fails fast with a
	// diagnostic instead of spinning until the 45-minute deadline.
	if missing := podTemplateSpecMissingSecrets(rendered.BackupDeploymentPodTemplateSpec); len(missing) > 0 {
		for _, name := range missing {
			err := r.Get(ctx, types.NamespacedName{Namespace: j.Namespace, Name: name}, &corev1.Secret{})
			if err == nil {
				continue
			}
			if !apierrors.IsNotFound(err) {
				return ctrl.Result{}, err
			}
			return r.requeueWithReason(ctx, j, "MissingBlobCredentialsSecret",
				fmt.Sprintf("Secret %s/%s referenced by the FoundationDB strategy's backupDeploymentPodTemplateSpec does not exist; create it before submitting the BackupJob", j.Namespace, name))
		}
	}

	fdbBackup, err := r.ensureFoundationDBBackup(ctx, j, clusterName, cluster.Spec.Version, rendered)
	if err != nil {
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf("failed to ensure apps.foundationdb.org/FoundationDBBackup: %v", err))
	}

	if j.Status.Phase != backupsv1alpha1.BackupJobPhaseRunning {
		j.Status.Phase = backupsv1alpha1.BackupJobPhaseRunning
		if err := r.Status().Update(ctx, j); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Ready when the operator has reconciled the spec, the deployment is
	// configured, and the backup_agent has written at least one full
	// snapshot (SnapshotTime > 0).
	if !foundationdbBackupReady(fdbBackup) {
		if foundationdbBackupDeadlineExceeded(j.Status.StartedAt) {
			detail := foundationdbBackupNotReadyDetail(fdbBackup)
			// Stop the operator-side CR before marking the job
			// Failed. Otherwise the backup_agent Deployment keeps
			// retrying against a broken S3 endpoint indefinitely
			// (one Deployment leaked per failed BackupJob) and the
			// FDB operator continues reconciling the running CR
			// for no reason. Best-effort: if the Stop patch fails,
			// the BackupJob still terminates so the deadline gate
			// stays trustworthy.
			if stopErr := r.stopFoundationDBBackupForJob(ctx, fdbBackup); stopErr != nil {
				getLogger(ctx).Debug("failed to stop FoundationDBBackup on deadline expiry; continuing with BackupJob failure",
					"backupjob", j.Name, "fdbBackup", fdbBackup.Name, "error", stopErr.Error())
			}
			return r.markBackupJobFailed(ctx, j, fmt.Sprintf(
				"apps.foundationdb.org/FoundationDBBackup did not reach a restorable snapshot within %s (%s)",
				foundationdbDefaultBackupDeadline, detail))
		}
		return r.requeueWithReason(ctx, j, "FoundationDBBackupRunning",
			foundationdbBackupNotReadyDetail(fdbBackup))
	}

	if j.Status.BackupRef != nil {
		// Already finalised; nothing more to do.
		return ctrl.Result{}, nil
	}

	artifact, err := r.createFoundationDBBackupArtifact(ctx, j, resolved, fdbBackup, rendered, app)
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
		Message: "apps.foundationdb.org/FoundationDBBackup reached a restorable snapshot",
	})
	if err := r.Status().Update(ctx, j); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// foundationdbBackupReady reports whether the operator-side
// FoundationDBBackup has reconciled its spec and the backup_agent has
// written at least one full snapshot (i.e. is restorable).
func foundationdbBackupReady(b *foundationdbtypes.FoundationDBBackup) bool {
	if b == nil || b.Status.BackupDetails == nil {
		return false
	}
	if !b.Status.BackupDetails.Running {
		return false
	}
	if b.Status.BackupDetails.SnapshotTime <= 0 {
		return false
	}
	if b.Status.Generations != nil && b.Status.Generations.Reconciled < b.Generation {
		return false
	}
	return true
}

// foundationdbBackupNotReadyDetail returns a human-readable reason string
// for the not-yet-ready FoundationDBBackup.
func foundationdbBackupNotReadyDetail(b *foundationdbtypes.FoundationDBBackup) string {
	if b == nil {
		return "no FoundationDBBackup observed"
	}
	if b.Status.BackupDetails == nil {
		return "no backupDetails observed"
	}
	if b.Status.Generations != nil && b.Status.Generations.Reconciled < b.Generation {
		return fmt.Sprintf("operator has not reconciled the spec yet (reconciled=%d, generation=%d)",
			b.Status.Generations.Reconciled, b.Generation)
	}
	if !b.Status.BackupDetails.Running {
		return "backup_agent has not reported running=true"
	}
	if b.Status.BackupDetails.SnapshotTime <= 0 {
		return "waiting for first full snapshot (snapshotTime is 0)"
	}
	return "waiting"
}

// foundationdbBackupDeadlineExceeded reports whether enough wall-clock time
// has elapsed since the BackupJob started that we should give up on a stuck
// operator backup. Returns false when StartedAt is nil so the very first
// reconcile (which sets StartedAt) does not trip the gate.
func foundationdbBackupDeadlineExceeded(startedAt *metav1.Time) bool {
	if startedAt == nil {
		return false
	}
	return time.Since(startedAt.Time) > foundationdbDefaultBackupDeadline
}

// requeueWithReason stamps a transient Ready=False/<reason> condition and
// requeues after foundationdbPollInterval. Mirrors the MariaDB driver's
// transient-error helper.
//
// Retries on 409 Conflict: most call sites fire on the cold-start
// reconcile path where the informer cache is most likely to be stale,
// so a bare propagation of the 409 to controller-runtime can pin a
// busy-spin for the duration of the resync. patchConflictRetries-1
// retries plus the initial attempt is enough to absorb a stale-RV
// race without burning the reconcile budget. If the Conflict persists
// past the budget the bare error propagates - the parent reconciler
// will pick up the next change anyway.
func (r *BackupJobReconciler) requeueWithReason(ctx context.Context, j *backupsv1alpha1.BackupJob, reason, message string) (ctrl.Result, error) {
	cond := metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionFalse,
		Reason:  reason,
		Message: message,
	}
	apimeta.SetStatusCondition(&j.Status.Conditions, cond)
	for attempt := 0; attempt < foundationdbStopConflictRetries; attempt++ {
		err := r.Status().Update(ctx, j)
		if err == nil {
			return ctrl.Result{RequeueAfter: foundationdbPollInterval}, nil
		}
		if !apierrors.IsConflict(err) {
			return ctrl.Result{}, err
		}
		if attempt == foundationdbStopConflictRetries-1 {
			return ctrl.Result{}, err
		}
		// Refetch and re-apply the condition on the fresh handle.
		fresh := &backupsv1alpha1.BackupJob{}
		if getErr := r.Get(ctx, types.NamespacedName{Namespace: j.Namespace, Name: j.Name}, fresh); getErr != nil {
			if apierrors.IsNotFound(getErr) {
				return ctrl.Result{}, nil
			}
			return ctrl.Result{}, getErr
		}
		// Carry over any prior status fields the caller already set
		// (Phase, StartedAt) so the retry doesn't accidentally clobber
		// them while the in-memory j is being mutated by the caller.
		fresh.Status.Phase = j.Status.Phase
		if j.Status.StartedAt != nil {
			fresh.Status.StartedAt = j.Status.StartedAt
		}
		if j.Status.BackupRef != nil {
			fresh.Status.BackupRef = j.Status.BackupRef
		}
		apimeta.SetStatusCondition(&fresh.Status.Conditions, cond)
		*j = *fresh
	}
	return ctrl.Result{RequeueAfter: foundationdbPollInterval}, nil
}

// ensureFoundationDBBackup materialises a per-BackupJob FoundationDBBackup
// CR, or returns the existing one if a previous reconcile already created
// it. Stops any prior Running FoundationDBBackup attached to the same
// FDB cluster so the operator's "one running backup per cluster" invariant
// stays satisfied. Idempotency relies on the OwningJob labels.
//
// IMPORTANT: the FoundationDBBackup CR is intentionally NOT linked back to
// the BackupJob via metav1.OwnerReference. That deliberate omission is
// what makes the ensure-by-label semantics work across BackupJob
// recreates with the same name (e.g. a tenant runs `kubectl delete &&
// kubectl apply` to retry): a fresh BackupJob with the same name would
// find the prior operator CR by its OwningJob label and reuse it
// instead of leaking a duplicate Deployment. Adding an OwnerReference
// would make Kubernetes GC reap the operator CR when the parent
// BackupJob is deleted, defeating the reuse contract.
//
// Cleanup of the operator-side CR is therefore the framework's
// responsibility, not Kubernetes ownerRef GC. See:
//   - examples/backups/foundationdb/cleanup.sh (label-based reap),
//   - hack/e2e-apps/backup-foundationdb.bats teardown(),
//   - stopFoundationDBBackupForJob (deadline-expiry cleanup in this file).
//
// The `TestEnsureFoundationDBBackup_DoesNotSetOwnerReference` test pins
// the contract so a future patch that adds an OwnerReference "for
// safety" trips the test before silently breaking the reuse path.
func (r *BackupJobReconciler) ensureFoundationDBBackup(ctx context.Context, j *backupsv1alpha1.BackupJob, clusterName, clusterVersion string, rendered *strategyv1alpha1.FoundationDBTemplate) (*foundationdbtypes.FoundationDBBackup, error) {
	existing, err := r.findFoundationDBBackupForJob(ctx, j)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	if err := r.stopOtherFoundationDBBackups(ctx, j, clusterName); err != nil {
		return nil, err
	}

	blobName := rendered.BlobStoreConfiguration.BackupName
	if blobName == "" {
		blobName = j.Name
	}

	obj := &foundationdbtypes.FoundationDBBackup{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:    j.Namespace,
			GenerateName: fmt.Sprintf("%s-", j.Name),
			Labels: map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      j.Name,
				backupsv1alpha1.OwningJobNamespaceLabel: j.Namespace,
				foundationdbClusterLabel:                clusterName,
			},
		},
		Spec: foundationdbtypes.FoundationDBBackupSpec{
			ClusterName: clusterName,
			Version:     clusterVersion,
			BackupState: foundationdbtypes.BackupStateRunning,
			BlobStoreConfiguration: foundationdbtypes.BlobStoreConfiguration{
				AccountName:   rendered.BlobStoreConfiguration.AccountName,
				Bucket:        rendered.BlobStoreConfiguration.Bucket,
				BackupName:    blobName,
				URLParameters: append([]string(nil), rendered.BlobStoreConfiguration.URLParameters...),
			},
			AgentCount:            int32PtrCopy(rendered.AgentCount),
			SnapshotPeriodSeconds: int32PtrCopy(rendered.SnapshotPeriodSeconds),
			CustomParameters:      append([]string(nil), rendered.CustomParameters...),
			EncryptionKeyPath:     rendered.EncryptionKeyPath,
		},
	}
	if rendered.BackupDeploymentPodTemplateSpec != nil {
		obj.Spec.BackupDeploymentSpec = &foundationdbtypes.BackupDeploymentSpec{
			PodTemplateSpec: rendered.BackupDeploymentPodTemplateSpec.DeepCopy(),
		}
	}

	if err := r.Create(ctx, obj); err != nil {
		return nil, err
	}
	return obj, nil
}

// stopOtherFoundationDBBackups flips any FoundationDBBackup CRs in the
// BackupJob namespace that target the same FDB cluster to backupState=Stopped.
// The operator only permits one running backup directory per cluster, so a
// prior BackupJob's CR has to stop before the new one can start streaming.
//
// The operator reconciles spec.backupState concurrently, so the patch can
// race with an operator update and surface as a 409 Conflict on a stale
// resourceVersion. Retry-on-Conflict by refetching the CR and re-applying
// the Stop intent; if the refetch shows the CR is already Stopped, we're
// done.
func (r *BackupJobReconciler) stopOtherFoundationDBBackups(ctx context.Context, j *backupsv1alpha1.BackupJob, clusterName string) error {
	list := &foundationdbtypes.FoundationDBBackupList{}
	if err := r.List(ctx, list,
		client.InNamespace(j.Namespace),
		client.MatchingLabels{foundationdbClusterLabel: clusterName},
	); err != nil {
		return err
	}
	for i := range list.Items {
		b := &list.Items[i]
		if b.Spec.BackupState == foundationdbtypes.BackupStateStopped {
			continue
		}
		// Don't stop the BackupJob's own backup (shouldn't be in the list at
		// this point - ensureFoundationDBBackup checks for it first - but be
		// defensive against a race between two reconciles).
		if b.Labels[backupsv1alpha1.OwningJobNameLabel] == j.Name &&
			b.Labels[backupsv1alpha1.OwningJobNamespaceLabel] == j.Namespace {
			continue
		}
		if err := r.patchStopWithRetry(ctx, b); err != nil {
			return fmt.Errorf("stop prior FoundationDBBackup %s/%s: %w", b.Namespace, b.Name, err)
		}
	}
	return nil
}

// stopFoundationDBBackupForJob is the single-CR variant of
// stopOtherFoundationDBBackups, used on terminal BackupJob failure to keep
// the operator-side backup_agent Deployment from leaking past the
// BackupJob's lifetime. Same Conflict-retry semantics.
func (r *BackupJobReconciler) stopFoundationDBBackupForJob(ctx context.Context, b *foundationdbtypes.FoundationDBBackup) error {
	if b == nil {
		return nil
	}
	if b.Spec.BackupState == foundationdbtypes.BackupStateStopped {
		return nil
	}
	if err := r.patchStopWithRetry(ctx, b); err != nil {
		return fmt.Errorf("stop FoundationDBBackup %s/%s: %w", b.Namespace, b.Name, err)
	}
	return nil
}

// foundationdbStopConflictRetries caps the patch-retry budget so a
// pathologically-fast concurrent writer (operator reconciling against the
// same CR) cannot pin a single BackupJob reconcile forever.
const foundationdbStopConflictRetries = 5

// patchStopWithRetry sets spec.backupState=Stopped on the given CR,
// retrying on 409 Conflict by refetching and re-applying the change. 404
// during refetch is treated as success (the CR is gone — Stopped is
// reached). The function mutates *b to the final observed state on
// success.
func (r *BackupJobReconciler) patchStopWithRetry(ctx context.Context, b *foundationdbtypes.FoundationDBBackup) error {
	for attempt := 0; attempt < foundationdbStopConflictRetries; attempt++ {
		base := b.DeepCopy()
		b.Spec.BackupState = foundationdbtypes.BackupStateStopped
		err := r.Patch(ctx, b, client.MergeFrom(base))
		if err == nil {
			return nil
		}
		if apierrors.IsNotFound(err) {
			return nil
		}
		if !apierrors.IsConflict(err) {
			return err
		}
		// Refetch and try again with the latest resourceVersion. A
		// Conflict on the last attempt falls through to the caller so
		// the surrounding reconcile can retry the whole sequence
		// rather than burn the entire budget here.
		if attempt == foundationdbStopConflictRetries-1 {
			return err
		}
		if getErr := r.Get(ctx, client.ObjectKeyFromObject(b), b); getErr != nil {
			if apierrors.IsNotFound(getErr) {
				return nil
			}
			return fmt.Errorf("refetch on conflict: %w", getErr)
		}
		if b.Spec.BackupState == foundationdbtypes.BackupStateStopped {
			return nil
		}
	}
	return nil
}

// foundationdbClusterLabel groups FoundationDBBackup CRs by the FDB
// cluster they target so the driver can list them in O(1).
const foundationdbClusterLabel = "backups.cozystack.io/foundationdb-cluster"

// findConflictingInChartBackup returns the name of any FoundationDBBackup
// in the given namespace that targets the same cluster, is currently
// Running, and does NOT carry the driver's cluster label - i.e. a
// chart-rendered CR from the legacy `backup.enabled=true` flow. Returns
// "" when no conflict is found.
//
// The FDB operator only permits one running backup directory per cluster.
// Silently stopping the chart-rendered CR would split the in-chart user's
// backup stream and break their tenancy contract; a terminal failure
// surfaces the migration step they need to take instead.
func (r *BackupJobReconciler) findConflictingInChartBackup(ctx context.Context, namespace, clusterName string) (string, error) {
	list := &foundationdbtypes.FoundationDBBackupList{}
	if err := r.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return "", err
	}
	for i := range list.Items {
		b := &list.Items[i]
		if b.Spec.ClusterName != clusterName {
			continue
		}
		if b.Spec.BackupState != foundationdbtypes.BackupStateRunning {
			continue
		}
		if _, ok := b.Labels[foundationdbClusterLabel]; ok {
			// Driver-managed CR. The dispatch path
			// (stopOtherFoundationDBBackups +
			// ensureFoundationDBBackup) handles transitions
			// between driver-managed CRs; only chart-rendered
			// strangers need the loud diagnostic.
			continue
		}
		return b.Name, nil
	}
	return "", nil
}

// podTemplateSpecMissingSecrets returns the de-duplicated names of all
// Secrets referenced by the PodTemplateSpec's volumes/envFrom/env that
// the driver must verify exist in the BackupJob namespace before the
// backup_agent Deployment starts. The list is "potentially missing"
// because the caller does the actual Get against the apiserver; the
// helper is purely template traversal so it stays testable without a
// fake client. An optional reference (e.g. volumes[].secret.optional=true,
// env[].valueFrom.secretKeyRef.optional=true) is skipped - the operator
// is supposed to tolerate its absence.
func podTemplateSpecMissingSecrets(tmpl *corev1.PodTemplateSpec) []string {
	if tmpl == nil {
		return nil
	}
	seen := map[string]struct{}{}
	out := []string{}
	add := func(name string) {
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	for _, v := range tmpl.Spec.Volumes {
		if v.Secret == nil || v.Secret.SecretName == "" {
			continue
		}
		if v.Secret.Optional != nil && *v.Secret.Optional {
			continue
		}
		add(v.Secret.SecretName)
	}
	for _, allContainers := range [][]corev1.Container{tmpl.Spec.InitContainers, tmpl.Spec.Containers} {
		for _, ctr := range allContainers {
			for _, ef := range ctr.EnvFrom {
				if ef.SecretRef == nil {
					continue
				}
				if ef.SecretRef.Optional != nil && *ef.SecretRef.Optional {
					continue
				}
				add(ef.SecretRef.Name)
			}
			for _, e := range ctr.Env {
				if e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil {
					continue
				}
				if e.ValueFrom.SecretKeyRef.Optional != nil && *e.ValueFrom.SecretKeyRef.Optional {
					continue
				}
				add(e.ValueFrom.SecretKeyRef.Name)
			}
		}
	}
	return out
}

// findFoundationDBBackupForJob returns the FoundationDBBackup labelled
// with the BackupJob's OwningJob{Name,Namespace}, if any. Returns
// (nil, nil) when no match is found. Mirrors the MariaDB driver's
// idempotent ensure-by-label pattern.
func (r *BackupJobReconciler) findFoundationDBBackupForJob(ctx context.Context, j *backupsv1alpha1.BackupJob) (*foundationdbtypes.FoundationDBBackup, error) {
	list := &foundationdbtypes.FoundationDBBackupList{}
	if err := r.List(ctx, list,
		client.InNamespace(j.Namespace),
		client.MatchingLabels{
			backupsv1alpha1.OwningJobNameLabel:      j.Name,
			backupsv1alpha1.OwningJobNamespaceLabel: j.Namespace,
		},
	); err != nil {
		return nil, err
	}
	if len(list.Items) == 0 {
		return nil, nil
	}
	if len(list.Items) > 1 {
		names := make([]string, 0, len(list.Items))
		for i := range list.Items {
			names = append(names, list.Items[i].Name)
		}
		getLogger(ctx).Debug("multiple apps.foundationdb.org/FoundationDBBackup CRs match BackupJob OwningJob labels; reusing first",
			"backupjob", j.Name, "namespace", j.Namespace, "matches", names, "picked", names[0])
	}
	return &list.Items[0], nil
}

// createFoundationDBBackupArtifact materialises a Cozystack Backup resource
// carrying the metadata callers need to drive a future restore. The source
// app's spec snapshot plus the rendered strategy parameters are persisted
// in status.underlyingResources so restore-time templating produces the
// same values the backup ran with.
func (r *BackupJobReconciler) createFoundationDBBackupArtifact(
	ctx context.Context,
	j *backupsv1alpha1.BackupJob,
	resolved *ResolvedBackupConfig,
	fdbBackup *foundationdbtypes.FoundationDBBackup,
	rendered *strategyv1alpha1.FoundationDBTemplate,
	sourceApp *foundationdbapp.FoundationDB,
) (*backupsv1alpha1.Backup, error) {
	// NOTE: FoundationDBBackup.status.backupDetails.snapshotTime is the FDB
	// read-version at snapshot time (an FDB-internal integer counter, not a
	// wall-clock timestamp), so it cannot be plugged into Backup.spec.takenAt
	// directly. We observed the snapshot ready in the BackupJob reconcile
	// that called this function, so wall-clock "now" is close enough for
	// human consumption.
	takenAt := metav1.Now()

	driverMD := map[string]string{
		foundationdbBackupNameKey:      fdbBackup.Name,
		foundationdbBackupNamespaceKey: fdbBackup.Namespace,
		foundationdbAccountNameKey:     fdbBackup.Spec.BlobStoreConfiguration.AccountName,
		foundationdbBucketKey:          fdbBackup.Spec.BlobStoreConfiguration.Bucket,
		foundationdbBlobBackupNameKey:  fdbBackup.Spec.BlobStoreConfiguration.BackupName,
	}
	if uri := foundationdbBackupURI(fdbBackup); uri != "" {
		driverMD[foundationdbStorageURIKey] = uri
	}

	underlyingResources, err := marshalFoundationDBBackupSnapshot(sourceApp, rendered, resolved.Parameters, fdbBackup)
	if err != nil {
		return nil, fmt.Errorf("encode source snapshot for Backup.status.underlyingResources: %w", err)
	}

	status := backupsv1alpha1.BackupStatus{
		Phase:               backupsv1alpha1.BackupPhaseReady,
		UnderlyingResources: underlyingResources,
	}
	if uri := driverMD[foundationdbStorageURIKey]; uri != "" {
		status.Artifact = &backupsv1alpha1.BackupArtifact{URI: uri}
	}
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      j.Name,
			Namespace: j.Namespace,
		},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: j.Spec.ApplicationRef,
			StrategyRef:    resolved.StrategyRef,
			TakenAt:        takenAt,
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
		return existing, nil
	}
	return backup, nil
}

// foundationdbBackupURI synthesises a human-readable URI for the Cozystack
// Backup artifact. The URI is informational (Backup.status.driverMetadata
// carries the load-bearing coordinates); when bucket is unset we return ""
// rather than guess a path that uses AccountName, since AccountName looks
// like "<api_key>@<host:port>" and grafting it into a bucket slot produces
// a misleading URL that doesn't match anything the operator would write.
func foundationdbBackupURI(fdbBackup *foundationdbtypes.FoundationDBBackup) string {
	if fdbBackup == nil {
		return ""
	}
	// Prefer the operator-reported URL when present: it's the canonical
	// path the agent is actually writing to.
	if fdbBackup.Status.BackupDetails != nil && fdbBackup.Status.BackupDetails.URL != "" {
		return fdbBackup.Status.BackupDetails.URL
	}
	cfg := fdbBackup.Spec.BlobStoreConfiguration
	if cfg.Bucket == "" || cfg.BackupName == "" {
		return ""
	}
	return fmt.Sprintf("blobstore://%s/%s", cfg.Bucket, cfg.BackupName)
}

// ---------------------------------------------------------------------------
// RestoreJob path
// ---------------------------------------------------------------------------

func (r *RestoreJobReconciler) reconcileFoundationDBRestore(ctx context.Context, restoreJob *backupsv1alpha1.RestoreJob, backup *backupsv1alpha1.Backup) (ctrl.Result, error) {
	logger := getLogger(ctx)
	logger.Debug("reconciling FoundationDB restore", "restorejob", restoreJob.Name, "backup", backup.Name)

	if err := validateFoundationDBApplicationRef(backup.Spec.ApplicationRef); err != nil {
		return r.markRestoreJobFailed(ctx, restoreJob, err.Error())
	}

	// Validate the resolved target shape before any apiserver call.
	target := r.resolveFoundationDBRestoreTarget(restoreJob, backup)
	if target.Kind != foundationdbAppKind {
		return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf(
			"target applicationRef.kind=%q is not supported by the FoundationDB driver", target.Kind))
	}
	if target.APIGroup != "" && target.APIGroup != foundationdbapp.GroupName {
		return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf(
			"target applicationRef.apiGroup=%q is not supported by the FoundationDB driver", target.APIGroup))
	}

	if restoreJob.Status.StartedAt == nil {
		fresh := &backupsv1alpha1.RestoreJob{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: restoreJob.Namespace, Name: restoreJob.Name}, fresh); err != nil {
			return ctrl.Result{}, err
		}
		if fresh.Status.StartedAt != nil {
			restoreJob.Status.StartedAt = fresh.Status.StartedAt
			if fresh.Status.Phase != "" {
				restoreJob.Status.Phase = fresh.Status.Phase
			}
		} else {
			base := fresh.DeepCopy()
			now := metav1.Now()
			fresh.Status.StartedAt = &now
			fresh.Status.Phase = backupsv1alpha1.RestoreJobPhaseRunning
			if err := r.Status().Patch(ctx, fresh, client.MergeFrom(base)); err != nil {
				return ctrl.Result{}, err
			}
			restoreJob.Status.StartedAt = fresh.Status.StartedAt
			restoreJob.Status.Phase = fresh.Status.Phase
			return ctrl.Result{RequeueAfter: foundationdbPollInterval}, nil
		}
	}

	options, err := parseFoundationDBRestoreOptions(restoreJob.Spec.Options)
	if err != nil {
		// Treat malformed spec.options as a terminal failure. The
		// previous behaviour silently fell back to defaults, which is
		// hostile: a tenant who set `restoreTimeoutSeconds: "5h"`
		// (string instead of int64) would see the default 30-minute
		// deadline take effect with no Ready=False signal on the
		// RestoreJob, and the misconfiguration would persist until a
		// restore ran longer than 30 minutes. Other strategies treat
		// option-parse errors as terminal; do the same here.
		r.Recorder.Eventf(restoreJob, corev1.EventTypeWarning, "MalformedOptions",
			"spec.options is not valid JSON: %v", err)
		return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf(
			"malformed restoreJob.spec.options: %v (clear the field or supply a valid FoundationDBRestoreOptions JSON object)", err))
	}

	// Restore needs the blob-store address the backup ran with. Prefer
	// the persisted snapshot on the Cozystack Backup
	// (status.underlyingResources) - it is durable beyond the operator
	// Backup CR's lifetime - and fall back to driverMetadata when the
	// snapshot has been dropped from an old artefact.
	//
	// Decode-error handling honours the contract documented next to
	// foundationdbBackupSnapshotKind:
	//   - errSnapshotUnrecognised (kind/apiVersion mismatch): terminal
	//     failure. A future v2 snapshot with extra fields the v1 reader
	//     cannot interpret must not be silently overridden by
	//     driverMetadata, because the snapshot is the authoritative
	//     source for fields driverMetadata doesn't carry
	//     (CustomParameters / EncryptionKeyPath).
	//   - any other decode failure (malformed JSON): log + emit a
	//     MalformedSnapshot event, then fall through to driverMetadata.
	//     The error is recoverable - driverMetadata carries the load-
	//     bearing blob coordinates - and the breadcrumb gives operators
	//     something to grep for.
	snap, err := decodeFoundationDBBackupSnapshot(backup.Status.UnderlyingResources)
	if err != nil {
		if errors.Is(err, errSnapshotUnrecognised) {
			return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf(
				"%v (re-take the backup with a controller version compatible with this snapshot schema, or clear Backup.status.underlyingResources to opt into the driverMetadata fallback)", err))
		}
		logger.Info("malformed Backup.status.underlyingResources; falling back to driverMetadata", "error", err)
		r.Recorder.Eventf(restoreJob, corev1.EventTypeWarning, "MalformedSnapshot",
			"Backup.status.underlyingResources is not a valid FoundationDB snapshot; falling back to driverMetadata: %v", err)
		snap = nil
	}
	blobCfg, blobOK := resolveFoundationDBRestoreBlob(backup, snap)
	if !blobOK {
		return r.markRestoreJobFailed(ctx, restoreJob,
			"Backup driverMetadata/snapshot is missing FoundationDB blob-store configuration (re-take the backup with a controller version that persists it)")
	}

	// Verify the operator-side destination cluster exists. NotFound is
	// transient: a tenant who fires a RestoreJob seconds before their
	// target HelmRelease materialises the cluster shouldn't have to
	// recreate the RestoreJob - mirror the backup path's transient
	// handling and let restoreTimeoutSeconds guard against a target
	// that never shows up.
	targetClusterName := foundationdbClusterNameForApp(target.AppName)
	if err := r.Get(ctx, types.NamespacedName{Namespace: target.Namespace, Name: targetClusterName},
		&foundationdbtypes.FoundationDBCluster{}); err != nil {
		if apierrors.IsNotFound(err) {
			deadline := options.effectiveRestoreDeadline()
			if restoreJob.Status.StartedAt != nil && time.Since(restoreJob.Status.StartedAt.Time) > deadline {
				return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf(
					"target apps.foundationdb.org/FoundationDBCluster %s/%s not found within %s (deploy the target FoundationDB application before requesting the restore; override via spec.options.restoreTimeoutSeconds)",
					target.Namespace, targetClusterName, deadline))
			}
			apimeta.SetStatusCondition(&restoreJob.Status.Conditions, metav1.Condition{
				Type:   "Ready",
				Status: metav1.ConditionFalse,
				Reason: "TargetFoundationDBClusterNotReady",
				Message: fmt.Sprintf("waiting for target apps.foundationdb.org/FoundationDBCluster %s/%s to exist",
					target.Namespace, targetClusterName),
			})
			if err := r.Status().Update(ctx, restoreJob); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: foundationdbPollInterval}, nil
		}
		return ctrl.Result{}, err
	}

	customParams := []string(nil)
	if snap != nil {
		customParams = append(customParams, snap.CustomParameters...)
	}
	fdbRestore, err := r.ensureFoundationDBRestore(ctx, restoreJob, targetClusterName, blobCfg, customParams, snap)
	if err != nil {
		return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf("failed to ensure apps.foundationdb.org/FoundationDBRestore: %v", err))
	}

	switch {
	case strings.EqualFold(fdbRestore.Status.State, foundationdbtypes.RestoreStateCompleted):
		now := metav1.Now()
		restoreJob.Status.CompletedAt = &now
		restoreJob.Status.Phase = backupsv1alpha1.RestoreJobPhaseSucceeded
		apimeta.SetStatusCondition(&restoreJob.Status.Conditions, metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionTrue,
			Reason:  "RestoreCompleted",
			Message: fmt.Sprintf("apps.foundationdb.org/FoundationDBRestore %s/%s completed", fdbRestore.Namespace, fdbRestore.Name),
		})
		if err := r.Status().Update(ctx, restoreJob); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil

	default:
		deadline := options.effectiveRestoreDeadline()
		if restoreJob.Status.StartedAt != nil && time.Since(restoreJob.Status.StartedAt.Time) > deadline {
			detail := fmt.Sprintf("state=%q running=%t", fdbRestore.Status.State, fdbRestore.Status.Running)
			return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf(
				"apps.foundationdb.org/FoundationDBRestore did not complete within %s (%s; override via spec.options.restoreTimeoutSeconds)",
				deadline, detail))
		}
		apimeta.SetStatusCondition(&restoreJob.Status.Conditions, metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionFalse,
			Reason:  "FoundationDBRestoreRunning",
			Message: fmt.Sprintf("apps.foundationdb.org/FoundationDBRestore %s/%s state=%q running=%t", fdbRestore.Namespace, fdbRestore.Name, fdbRestore.Status.State, fdbRestore.Status.Running),
		})
		if err := r.Status().Update(ctx, restoreJob); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: foundationdbPollInterval}, nil
	}
}

// resolveFoundationDBRestoreBlob picks the blob-store config to feed into
// FoundationDBRestore. The persisted snapshot
// (backup.status.underlyingResources) is the authoritative source; the
// driverMetadata-only fallback covers older artefacts that pre-date the
// snapshot field.
func resolveFoundationDBRestoreBlob(backup *backupsv1alpha1.Backup, snap *foundationdbBackupSnapshot) (foundationdbtypes.BlobStoreConfiguration, bool) {
	if snap != nil && (snap.Storage.AccountName != "" || snap.Storage.Bucket != "" || snap.Storage.BackupName != "") {
		return foundationdbtypes.BlobStoreConfiguration{
			AccountName:   snap.Storage.AccountName,
			Bucket:        snap.Storage.Bucket,
			BackupName:    snap.Storage.BackupName,
			URLParameters: append([]string(nil), snap.Storage.URLParameters...),
		}, true
	}
	md := backup.Spec.DriverMetadata
	cfg := foundationdbtypes.BlobStoreConfiguration{
		AccountName: md[foundationdbAccountNameKey],
		Bucket:      md[foundationdbBucketKey],
		BackupName:  md[foundationdbBlobBackupNameKey],
	}
	if cfg.AccountName == "" && cfg.Bucket == "" && cfg.BackupName == "" {
		return cfg, false
	}
	return cfg, true
}

// ensureFoundationDBRestore creates a FoundationDBRestore CR labelled with
// the RestoreJob, or returns the existing one if a previous reconcile
// already created it.
func (r *RestoreJobReconciler) ensureFoundationDBRestore(
	ctx context.Context,
	rj *backupsv1alpha1.RestoreJob,
	targetClusterName string,
	blob foundationdbtypes.BlobStoreConfiguration,
	customParameters []string,
	snap *foundationdbBackupSnapshot,
) (*foundationdbtypes.FoundationDBRestore, error) {
	list := &foundationdbtypes.FoundationDBRestoreList{}
	if err := r.List(ctx, list,
		client.InNamespace(rj.Namespace),
		client.MatchingLabels{
			backupsv1alpha1.OwningJobNameLabel:      rj.Name,
			backupsv1alpha1.OwningJobNamespaceLabel: rj.Namespace,
		},
	); err != nil {
		return nil, err
	}
	if len(list.Items) > 0 {
		return &list.Items[0], nil
	}

	encryptionKeyPath := ""
	if snap != nil {
		encryptionKeyPath = snap.EncryptionKeyPath
	}

	obj := &foundationdbtypes.FoundationDBRestore{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:    rj.Namespace,
			GenerateName: fmt.Sprintf("%s-", rj.Name),
			Labels: map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      rj.Name,
				backupsv1alpha1.OwningJobNamespaceLabel: rj.Namespace,
			},
		},
		Spec: foundationdbtypes.FoundationDBRestoreSpec{
			DestinationClusterName: targetClusterName,
			BlobStoreConfiguration: blob,
			CustomParameters:       append([]string(nil), customParameters...),
			EncryptionKeyPath:      encryptionKeyPath,
		},
	}
	if err := r.Create(ctx, obj); err != nil {
		return nil, err
	}
	return obj, nil
}

// foundationdbRestoreTarget captures the resolved target for a FoundationDB
// restore. Both in-place and to-copy create a FoundationDBRestore against
// a named target cluster; callers infer the mode from
// AppName != backup.Spec.ApplicationRef.Name.
type foundationdbRestoreTarget struct {
	Namespace string
	AppName   string
	Kind      string
	APIGroup  string
}

func (r *RestoreJobReconciler) resolveFoundationDBRestoreTarget(restoreJob *backupsv1alpha1.RestoreJob, backup *backupsv1alpha1.Backup) foundationdbRestoreTarget {
	t := foundationdbRestoreTarget{
		Namespace: backup.Namespace,
		AppName:   backup.Spec.ApplicationRef.Name,
		Kind:      backup.Spec.ApplicationRef.Kind,
	}
	if backup.Spec.ApplicationRef.APIGroup != nil {
		t.APIGroup = *backup.Spec.ApplicationRef.APIGroup
	}
	if restoreJob.Spec.TargetApplicationRef != nil {
		if restoreJob.Spec.TargetApplicationRef.Name != "" {
			t.AppName = restoreJob.Spec.TargetApplicationRef.Name
		}
		if restoreJob.Spec.TargetApplicationRef.Kind != "" {
			t.Kind = restoreJob.Spec.TargetApplicationRef.Kind
		}
		if restoreJob.Spec.TargetApplicationRef.APIGroup != nil {
			t.APIGroup = *restoreJob.Spec.TargetApplicationRef.APIGroup
		}
	}
	return t
}

// ---------------------------------------------------------------------------
// Snapshot persisted on Cozystack Backup.status.underlyingResources
// ---------------------------------------------------------------------------

// foundationdbBackupSnapshot is the FDB-specific payload persisted in
// Backup.status.underlyingResources at backup time. Carries the rendered
// blob-store target, custom parameters, and BackupClass parameters so a
// future RestoreJob can reproduce them exactly when the operator-side
// FoundationDBBackup CR has been pruned.
type foundationdbBackupSnapshot struct {
	Kind              string                                       `json:"kind"`
	APIVersion        string                                       `json:"apiVersion"`
	Storage           strategyv1alpha1.FoundationDBBlobStoreTemplate `json:"storage"`
	CustomParameters  []string                                     `json:"customParameters,omitempty"`
	EncryptionKeyPath string                                       `json:"encryptionKeyPath,omitempty"`
	Parameters        map[string]string                            `json:"parameters,omitempty"`
}

func marshalFoundationDBBackupSnapshot(
	_ *foundationdbapp.FoundationDB,
	rendered *strategyv1alpha1.FoundationDBTemplate,
	parameters map[string]string,
	fdbBackup *foundationdbtypes.FoundationDBBackup,
) (*runtime.RawExtension, error) {
	storage := rendered.BlobStoreConfiguration.DeepCopy()
	// Persist the actual per-run BackupName the operator received, even
	// when the strategy template left it empty (ensureFoundationDBBackup
	// fills it with the BackupJob name).
	if storage.BackupName == "" && fdbBackup != nil {
		storage.BackupName = fdbBackup.Spec.BlobStoreConfiguration.BackupName
	}
	snap := foundationdbBackupSnapshot{
		Kind:              foundationdbBackupSnapshotKind,
		APIVersion:        foundationdbBackupSnapshotAPIVersion,
		Storage:           *storage,
		CustomParameters:  append([]string(nil), rendered.CustomParameters...),
		EncryptionKeyPath: rendered.EncryptionKeyPath,
		Parameters:        parameters,
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		return nil, err
	}
	return &runtime.RawExtension{Raw: raw}, nil
}

// errSnapshotUnrecognised is returned by decodeFoundationDBBackupSnapshot
// when the payload at Backup.status.underlyingResources is parseable JSON
// but its self-typing fields (Kind, APIVersion) do not match the schema
// this driver understands. The contract block on
// foundationdbBackupSnapshotKind requires future readers to refuse such
// snapshots rather than silently fall back to driverMetadata, which on a
// real version-bump would corrupt the restore (a v2 snapshot may carry
// fields whose absence the v1 reader would interpret incorrectly).
//
// Call sites use errors.Is(err, errSnapshotUnrecognised) to distinguish
// "no snapshot present" (raw==nil, decoder returns nil/nil — fall back to
// driverMetadata) from "snapshot present but unrecognised" (decoder
// returns this error — restore must fail terminally).
var errSnapshotUnrecognised = errors.New("Backup.status.underlyingResources carries a snapshot of an unrecognised kind/apiVersion")

func decodeFoundationDBBackupSnapshot(raw *runtime.RawExtension) (*foundationdbBackupSnapshot, error) {
	if raw == nil || len(raw.Raw) == 0 {
		return nil, nil
	}
	snap := &foundationdbBackupSnapshot{}
	if err := json.Unmarshal(raw.Raw, snap); err != nil {
		return nil, fmt.Errorf("decode Backup.status.underlyingResources: %w", err)
	}
	if snap.Kind != foundationdbBackupSnapshotKind {
		return nil, fmt.Errorf("%w: kind=%q (want %q)",
			errSnapshotUnrecognised, snap.Kind, foundationdbBackupSnapshotKind)
	}
	if snap.APIVersion != foundationdbBackupSnapshotAPIVersion {
		return nil, fmt.Errorf("%w: apiVersion=%q (want %q)",
			errSnapshotUnrecognised, snap.APIVersion, foundationdbBackupSnapshotAPIVersion)
	}
	return snap, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// renderFoundationDBTemplate templates the strategy template against a
// context containing the live application object and the BackupClass
// parameters. Reuses the same templating helper as the CNPG / MariaDB /
// Velero strategies.
func renderFoundationDBTemplate(t strategyv1alpha1.FoundationDBTemplate, app *foundationdbapp.FoundationDB, parameters map[string]string) (*strategyv1alpha1.FoundationDBTemplate, error) {
	appAsMap, err := toJSONMapFoundationDB(app)
	if err != nil {
		return nil, fmt.Errorf("encode application for templating: %w", err)
	}
	templateContext := map[string]interface{}{
		"Application": appAsMap,
		"Parameters":  parameters,
	}
	return template.Template(&t, templateContext)
}

// toJSONMapFoundationDB converts a typed object to a generic map via JSON
// tags so user-authored go-templates address fields by their JSON names
// (e.g. .Application.metadata.name).
func toJSONMapFoundationDB(obj interface{}) (map[string]interface{}, error) {
	raw, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	out := map[string]interface{}{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// getFoundationDBApp fetches the apps.cozystack.io FoundationDB instance
// via the shared typed client. The FoundationDB scheme is registered in
// main.go so the controller-runtime cache serves it directly.
func (r *BackupJobReconciler) getFoundationDBApp(ctx context.Context, namespace, name string) (*foundationdbapp.FoundationDB, error) {
	app := &foundationdbapp.FoundationDB{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, app); err != nil {
		return nil, err
	}
	return app, nil
}

// int32PtrCopy returns a deep copy of an optional int32 pointer.
func int32PtrCopy(in *int32) *int32 {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

// FoundationDBRestoreOptions is the typed shape of RestoreJob.Spec.Options
// for the FoundationDB driver. Mirrors the MariaDB strategy's
// RestoreOptions pattern so the boundary parses lazily and keeps behaviour
// permissive.
type FoundationDBRestoreOptions struct {
	// RestoreTimeoutSeconds caps the time the driver waits for the
	// FoundationDBRestore to terminate before it marks the RestoreJob
	// Failed. Zero or unset falls back to foundationdbDefaultRestoreDeadline.
	// +optional
	RestoreTimeoutSeconds int64 `json:"restoreTimeoutSeconds,omitempty"`
}

func parseFoundationDBRestoreOptions(opts *runtime.RawExtension) (FoundationDBRestoreOptions, error) {
	var out FoundationDBRestoreOptions
	if opts == nil || len(opts.Raw) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(opts.Raw, &out); err != nil {
		return FoundationDBRestoreOptions{}, fmt.Errorf("decode restoreJob.spec.options: %w", err)
	}
	return out, nil
}

func (o FoundationDBRestoreOptions) effectiveRestoreDeadline() time.Duration {
	if o.RestoreTimeoutSeconds > 0 {
		return time.Duration(o.RestoreTimeoutSeconds) * time.Second
	}
	return foundationdbDefaultRestoreDeadline
}
