// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/cozystack/cozystack/internal/backupcontroller/cnpgtypes"
	"github.com/cozystack/cozystack/internal/backupcontroller/postgresapp"
	"github.com/cozystack/cozystack/internal/template"
)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const (
	cnpgFieldManager = "cozystack-cnpg-backup-driver"

	cnpgClusterLabel        = "cnpg.io/cluster"
	cnpgBackupMethodBarman  = "barmanObjectStore"
	cnpgBackupPhaseComplete = "completed"
	cnpgBackupPhaseFailed   = "failed"

	postgresAppKind   = "Postgres"
	postgresAppPrefix = "postgres-"

	defaultS3AccessKeyIDKey     = "AWS_ACCESS_KEY_ID"
	defaultS3SecretAccessKeyKey = "AWS_SECRET_ACCESS_KEY"

	// Driver metadata keys persisted on Cozystack Backup artifacts.
	cnpgBackupNameKey      = "cnpg.io/backup-name"
	cnpgServerNameKey      = "cnpg.io/server-name"
	cnpgDestinationPathKey = "cnpg.io/destination-path"
	cnpgEndpointURLKey     = "cnpg.io/endpoint-url"
	cnpgClusterNameKey     = "cnpg.io/cluster-name"
	cnpgS3SecretRefKey     = "cnpg.io/s3-secret-ref"

	// Polling cadence for the CNPG backup/restore lifecycle. Mirrors the
	// Velero strategy's defaults so behaviour is uniform across drivers.
	cnpgPollInterval = 5 * time.Second

	// Condition Type recorded on a RestoreJob once we've purged the target
	// Cluster + PVCs for it. Subsequent reconciles skip the purge step.
	restoreCondTargetPurged = "TargetPurged"

	// Default deadline on the time a RestoreJob can spend waiting for the
	// target Cluster to reach a healthy state. Tenants override this via
	// RestoreJob.spec.options.restoreTimeoutSeconds when the source DB is
	// large enough that 30 minutes isn't enough.
	cnpgDefaultRestoreDeadline = 30 * time.Minute

	// Cap on the wall-clock time a BackupJob can spend observing a
	// cnpg.io/Backup stuck in phase=failed before the driver gives up and
	// marks the BackupJob Failed. CNPG can transition out of "failed" on
	// internal retries (e.g. transient instance-manager restart), so we
	// don't fail on the first observation. But without this cap, a
	// permanently-broken Backup would pin the BackupJob in Running forever,
	// blocking later runs and wedging the Plan-controller queue.
	cnpgDefaultBackupDeadline = 30 * time.Minute

	cnpgClusterHealthyPhase = "Cluster in healthy state"
)

// cnpgClusterNameForApp returns the cnpg.io Cluster name for a Postgres
// application instance. The mapping mirrors the postgres ApplicationDefinition
// (release.prefix=postgres-).
func cnpgClusterNameForApp(appName string) string {
	return postgresAppPrefix + appName
}

// validateCNPGApplicationRef rejects ApplicationRefs that name a Kind/APIGroup
// the CNPG driver does not own. The driver assumes apps.cozystack.io/Postgres;
// without this gate a ref like other.example.com/Postgres would be accepted
// by the Kind check alone and then reconciled against the wrong CRD via the
// hard-wired apps.cozystack.io typed client.
func validateCNPGApplicationRef(ref corev1.TypedLocalObjectReference) error {
	if ref.Kind != postgresAppKind {
		return fmt.Errorf("CNPG strategy supports applicationRef.kind=%q, got %q", postgresAppKind, ref.Kind)
	}
	apiGroup := ""
	if ref.APIGroup != nil {
		apiGroup = *ref.APIGroup
	}
	if apiGroup != "" && apiGroup != postgresapp.GroupName {
		return fmt.Errorf("CNPG strategy supports applicationRef.apiGroup=%q, got %q", postgresapp.GroupName, apiGroup)
	}
	return nil
}

// ---------------------------------------------------------------------------
// BackupJob path
// ---------------------------------------------------------------------------

func (r *BackupJobReconciler) reconcileCNPG(ctx context.Context, j *backupsv1alpha1.BackupJob, resolved *ResolvedBackupConfig) (ctrl.Result, error) {
	logger := getLogger(ctx)
	logger.Debug("reconciling CNPG strategy", "backupjob", j.Name, "phase", j.Status.Phase)

	if j.Status.Phase == backupsv1alpha1.BackupJobPhaseSucceeded ||
		j.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
		return ctrl.Result{}, nil
	}

	if err := validateCNPGApplicationRef(j.Spec.ApplicationRef); err != nil {
		return r.markBackupJobFailed(ctx, j, err.Error())
	}

	if j.Status.StartedAt == nil {
		// Refetch the latest persisted state before writing StartedAt:
		// without this, a stale informer cache that returns StartedAt==nil
		// would let us write a fresh timestamp on top of one we already
		// persisted on a previous reconcile, sliding the deadline gate
		// forward on every poll. MergeFrom on the freshly-fetched object
		// then makes the write idempotent under concurrent edits to other
		// status subfields.
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
			j.Status.StartedAt = fresh.Status.StartedAt
		}
	}

	strategy := &strategyv1alpha1.CNPG{}
	if err := r.Get(ctx, client.ObjectKey{Name: resolved.StrategyRef.Name}, strategy); err != nil {
		if apierrors.IsNotFound(err) {
			return r.markBackupJobFailed(ctx, j, fmt.Sprintf("CNPG strategy not found: %s", resolved.StrategyRef.Name))
		}
		return ctrl.Result{}, err
	}

	app, err := r.getPostgresApp(ctx, j.Namespace, j.Spec.ApplicationRef.Name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.markBackupJobFailed(ctx, j, fmt.Sprintf("Postgres application not found: %s/%s", j.Namespace, j.Spec.ApplicationRef.Name))
		}
		return ctrl.Result{}, err
	}

	rendered, err := renderCNPGTemplate(strategy.Spec.Template, app, resolved.Parameters)
	if err != nil {
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf("failed to template CNPG strategy: %v", err))
	}

	clusterName := cnpgClusterNameForApp(j.Spec.ApplicationRef.Name)
	serverName := rendered.ServerName
	if serverName == "" {
		serverName = clusterName
	}

	if err := r.applyClusterBarmanObjectStore(ctx, j.Namespace, clusterName, rendered, serverName); err != nil {
		if apierrors.IsNotFound(err) {
			// HelmRelease has not yet rendered the Cluster (fresh app, or
			// the operator restart wiped its informer cache). Surface the
			// situation as a transient condition and back off; failing the
			// BackupJob would force the tenant to recreate it once the
			// chart catches up.
			apimeta.SetStatusCondition(&j.Status.Conditions, metav1.Condition{
				Type:    "Ready",
				Status:  metav1.ConditionFalse,
				Reason:  "ClusterNotReady",
				Message: fmt.Sprintf("waiting for cnpg.io/Cluster %s/%s to exist", j.Namespace, clusterName),
			})
			if err := r.Status().Update(ctx, j); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: cnpgPollInterval}, nil
		}
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf("failed to apply barmanObjectStore to Cluster: %v", err))
	}

	cnpgBackup, err := r.ensureCNPGBackup(ctx, j, clusterName)
	if err != nil {
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf("failed to ensure cnpg.io/Backup: %v", err))
	}

	if j.Status.Phase != backupsv1alpha1.BackupJobPhaseRunning {
		j.Status.Phase = backupsv1alpha1.BackupJobPhaseRunning
		if err := r.Status().Update(ctx, j); err != nil {
			return ctrl.Result{}, err
		}
	}

	phase, message := cnpgBackupPhase(cnpgBackup)
	logger.Debug("cnpg.io/Backup status", "phase", phase, "message", message)

	switch phase {
	case cnpgBackupPhaseComplete:
		if j.Status.BackupRef != nil {
			return ctrl.Result{}, nil
		}
		artifact, err := r.createCNPGBackupArtifact(ctx, j, resolved, cnpgBackup, clusterName, serverName, rendered, app)
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
			Message: "cnpg.io Backup completed",
		})
		if err := r.Status().Update(ctx, j); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil

	case cnpgBackupPhaseFailed:
		// CNPG can transition the same Backup CR back out of "failed" when it
		// retries internally (e.g., after a transient instance-manager
		// restart), so we don't fail the BackupJob on the first observation.
		// But without a cap a permanently-broken Backup would pin the
		// BackupJob in Running forever; once StartedAt + cnpgDefaultBackupDeadline
		// has elapsed and the cnpg.io/Backup is still failed, we give up.
		if cnpgBackupDeadlineExceeded(j.Status.StartedAt) {
			final := message
			if final == "" {
				final = fmt.Sprintf(
					"cnpg.io Backup remained in phase=failed past %s deadline",
					cnpgDefaultBackupDeadline)
			}
			return r.markBackupJobFailed(ctx, j, final)
		}
		if message == "" {
			message = "cnpg.io Backup reported phase=failed; awaiting recovery or retry"
		}
		apimeta.SetStatusCondition(&j.Status.Conditions, metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionFalse,
			Reason:  "CNPGBackupTransientFailure",
			Message: message,
		})
		if err := r.Status().Update(ctx, j); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: cnpgPollInterval}, nil

	default:
		return ctrl.Result{RequeueAfter: cnpgPollInterval}, nil
	}
}

// cnpgBackupDeadlineExceeded reports whether enough wall-clock time has
// elapsed since the BackupJob started that we should stop polling a stuck
// cnpg.io/Backup and fail the run. Returns false when StartedAt is nil so
// the very first reconcile (which sets StartedAt) does not trip the gate.
func cnpgBackupDeadlineExceeded(startedAt *metav1.Time) bool {
	if startedAt == nil {
		return false
	}
	return time.Since(startedAt.Time) > cnpgDefaultBackupDeadline
}

// cnpgPurgeNeeded decides whether the controller should delete the live
// CNPG Cluster + PVCs as part of a CNPG restore. Two conditions skip the
// destructive step:
//  1. The RestoreJob already records that we've purged
//     (restoreCondTargetPurged=True). Normal idempotent path.
//  2. The live Cluster already has spec.bootstrap.recovery populated. This
//     only happens when an earlier reconcile purged successfully but the
//     status-condition write failed; the chart has since re-rendered the
//     Cluster with our restore-shaped values. Re-purging here would delete
//     the Cluster CNPG is actively bootstrapping from S3.
func cnpgPurgeNeeded(purgedCondition, liveClusterHasRecovery bool) bool {
	if purgedCondition {
		return false
	}
	return !liveClusterHasRecovery
}

// applyClusterBarmanObjectStore SSA-patches the live CNPG Cluster's
// spec.backup from the templated strategy. The driver owns the fields via
// its own field manager so the chart - which only emits spec.backup when
// backup.enabled=true - does not contend.
//
// Returns an apierrors.IsNotFound error when the Cluster has not yet been
// rendered by the HelmRelease. The SSA path on its own would fail with a
// hard validation error (CNPG's Cluster CRD has many required fields the
// driver does not set), so we surface the precondition explicitly and let
// the caller treat it as a retryable wait.
func (r *BackupJobReconciler) applyClusterBarmanObjectStore(ctx context.Context, namespace, clusterName string, t *strategyv1alpha1.CNPGTemplate, serverName string) error {
	existing := &cnpgtypes.Cluster{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: clusterName}, existing); err != nil {
		return err
	}
	patch := newCNPGClusterPatch(namespace, clusterName)
	patch.Spec.Backup = &cnpgtypes.BackupConfiguration{
		BarmanObjectStore: buildBarmanObjectStore(t.BarmanObjectStore, serverName),
		RetentionPolicy:   t.BarmanObjectStore.RetentionPolicy,
	}
	return r.Patch(ctx, patch, client.Apply, client.FieldOwner(cnpgFieldManager), client.ForceOwnership)
}

// ensureCNPGBackup creates a one-shot postgresql.cnpg.io/Backup CR labelled
// with the BackupJob, or returns the existing one if a previous reconcile
// already created it. Idempotency relies on the OwningJob labels.
func (r *BackupJobReconciler) ensureCNPGBackup(ctx context.Context, j *backupsv1alpha1.BackupJob, clusterName string) (*cnpgtypes.Backup, error) {
	existing, err := r.findCNPGBackupForJob(ctx, j)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	obj := &cnpgtypes.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:    j.Namespace,
			GenerateName: fmt.Sprintf("%s-", j.Name),
			Labels: map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      j.Name,
				backupsv1alpha1.OwningJobNamespaceLabel: j.Namespace,
				cnpgClusterLabel:                        clusterName,
			},
		},
		Spec: cnpgtypes.BackupSpec{
			Method:  cnpgBackupMethodBarman,
			Cluster: cnpgtypes.ClusterReference{Name: clusterName},
		},
	}

	if err := r.Create(ctx, obj); err != nil {
		return nil, err
	}
	return obj, nil
}

// findCNPGBackupForJob returns the cnpg.io/Backup labelled with the
// BackupJob's OwningJob{Name,Namespace}, if any. Returns (nil, nil) when
// no match is found.
func (r *BackupJobReconciler) findCNPGBackupForJob(ctx context.Context, j *backupsv1alpha1.BackupJob) (*cnpgtypes.Backup, error) {
	list := &cnpgtypes.BackupList{}
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
	return &list.Items[0], nil
}

// createCNPGBackupArtifact materialises a Cozystack Backup resource carrying
// the metadata callers need to drive a future restore. The source app's
// spec.databases / spec.users are snapshotted into Status.UnderlyingResources
// so a future RestoreJob can mirror them onto the target without re-reading
// the source CR (which may have been deleted by the time the restore fires).
func (r *BackupJobReconciler) createCNPGBackupArtifact(
	ctx context.Context,
	j *backupsv1alpha1.BackupJob,
	resolved *ResolvedBackupConfig,
	cnpgBackup *cnpgtypes.Backup,
	clusterName, serverName string,
	rendered *strategyv1alpha1.CNPGTemplate,
	sourceApp *postgresapp.Postgres,
) (*backupsv1alpha1.Backup, error) {
	takenAt := metav1.Now()
	if cnpgBackup.Status.StartedAt != nil && !cnpgBackup.Status.StartedAt.IsZero() {
		takenAt = *cnpgBackup.Status.StartedAt
	}

	driverMD := map[string]string{
		cnpgBackupNameKey:      cnpgBackup.Name,
		cnpgServerNameKey:      serverName,
		cnpgDestinationPathKey: rendered.BarmanObjectStore.DestinationPath,
		cnpgEndpointURLKey:     rendered.BarmanObjectStore.EndpointURL,
		cnpgClusterNameKey:     clusterName,
	}
	if rendered.BarmanObjectStore.S3Credentials != nil {
		driverMD[cnpgS3SecretRefKey] = rendered.BarmanObjectStore.S3Credentials.SecretRef.Name
	}

	underlyingResources, err := marshalCNPGBackupSnapshot(sourceApp)
	if err != nil {
		return nil, fmt.Errorf("encode source snapshot for Backup.status.underlyingResources: %w", err)
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
		Status: backupsv1alpha1.BackupStatus{
			Phase: backupsv1alpha1.BackupPhaseReady,
			Artifact: &backupsv1alpha1.BackupArtifact{
				URI: fmt.Sprintf("cnpg://%s/%s", serverName, cnpgBackup.Name),
			},
			UnderlyingResources: underlyingResources,
		},
	}
	if j.Spec.PlanRef != nil {
		backup.Spec.PlanRef = j.Spec.PlanRef
	}
	if err := r.Create(ctx, backup); err != nil {
		// AlreadyExists means a previous reconcile created the artifact and
		// then raced on the BackupJob Status().Update. Returning the error
		// would flip the next reconcile to Failed even though the artifact
		// is in place. Fetch and return the existing object instead.
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

func (r *RestoreJobReconciler) reconcileCNPGRestore(ctx context.Context, restoreJob *backupsv1alpha1.RestoreJob, backup *backupsv1alpha1.Backup) (ctrl.Result, error) {
	logger := getLogger(ctx)
	logger.Debug("reconciling CNPG restore", "restorejob", restoreJob.Name, "backup", backup.Name)

	if err := validateCNPGApplicationRef(backup.Spec.ApplicationRef); err != nil {
		return r.markRestoreJobFailed(ctx, restoreJob, err.Error())
	}

	if restoreJob.Status.StartedAt == nil {
		// Refetch before writing StartedAt: a stale informer cache that
		// returns StartedAt==nil after we already persisted it on a
		// previous reconcile would otherwise let us slide the timestamp
		// forward, advancing the deadline gate on every poll.
		fresh := &backupsv1alpha1.RestoreJob{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: restoreJob.Namespace, Name: restoreJob.Name}, fresh); err != nil {
			return ctrl.Result{}, err
		}
		if fresh.Status.StartedAt != nil {
			restoreJob.Status.StartedAt = fresh.Status.StartedAt
			return ctrl.Result{RequeueAfter: cnpgPollInterval}, nil
		}
		base := fresh.DeepCopy()
		now := metav1.Now()
		fresh.Status.StartedAt = &now
		fresh.Status.Phase = backupsv1alpha1.RestoreJobPhaseRunning
		if err := r.Status().Patch(ctx, fresh, client.MergeFrom(base)); err != nil {
			return ctrl.Result{}, err
		}
		restoreJob.Status.StartedAt = fresh.Status.StartedAt
		restoreJob.Status.Phase = fresh.Status.Phase
		return ctrl.Result{RequeueAfter: cnpgPollInterval}, nil
	}

	target := r.resolveCNPGRestoreTarget(restoreJob, backup)
	if target.Kind != postgresAppKind {
		return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf(
			"target applicationRef.kind=%q is not supported by the CNPG driver", target.Kind))
	}
	if target.APIGroup != "" && target.APIGroup != postgresapp.GroupName {
		return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf(
			"target applicationRef.apiGroup=%q is not supported by the CNPG driver", target.APIGroup))
	}

	strategy := &strategyv1alpha1.CNPG{}
	if err := r.Get(ctx, client.ObjectKey{Name: backup.Spec.StrategyRef.Name}, strategy); err != nil {
		if apierrors.IsNotFound(err) {
			return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf("CNPG strategy not found: %s", backup.Spec.StrategyRef.Name))
		}
		return ctrl.Result{}, err
	}

	targetApp, err := r.getPostgresApp(ctx, target.Namespace, target.AppName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf(
				"target Postgres application not found: %s/%s (deploy it before requesting a copy restore)",
				target.Namespace, target.AppName))
		}
		return ctrl.Result{}, err
	}

	rendered, err := renderCNPGTemplate(strategy.Spec.Template, targetApp, nil)
	if err != nil {
		return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf("failed to template CNPG strategy: %v", err))
	}

	sourceServerName := backup.Spec.DriverMetadata[cnpgServerNameKey]
	sourceDestinationPath := backup.Spec.DriverMetadata[cnpgDestinationPathKey]
	sourceEndpointURL := backup.Spec.DriverMetadata[cnpgEndpointURLKey]
	if sourceServerName == "" || sourceDestinationPath == "" {
		return r.markRestoreJobFailed(ctx, restoreJob, "Backup driverMetadata is missing required CNPG fields")
	}

	options, err := parseCNPGRestoreOptions(restoreJob.Spec.Options)
	if err != nil {
		// Don't fail the RestoreJob on a parse error - we want behaviour to
		// stay permissive against future field additions in the typed
		// CNPGRestoreOptions struct. But log the error and surface a
		// transient condition so a tenant who wonders why their
		// recoveryTime didn't apply has a breadcrumb to follow.
		logger.Info("malformed restoreJob.spec.options; falling back to defaults", "error", err)
		r.Recorder.Eventf(restoreJob, corev1.EventTypeWarning, "MalformedOptions",
			"spec.options is not valid JSON; falling back to defaults: %v", err)
	}

	// The chart's init-job runs post-install and DROPs any database/role on
	// the recovered cluster that isn't declared in the target app spec, so
	// the patched target MUST carry source's spec.databases / spec.users.
	// We read those from the Backup artifact's status.underlyingResources
	// snapshot taken at backup time - never from the live source app:
	// (a) the source app may have been deleted before the restore fires,
	//     and we still need the snapshot to drive a safe restore;
	// (b) source spec drift between backup time and restore time would
	//     otherwise silently re-shape the recovered roles/databases.
	sourceDatabases, sourceUsers, err := unmarshalCNPGBackupSnapshot(backup)
	if err != nil {
		return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf(
			"Backup %s/%s carries no usable source-spec snapshot in status.underlyingResources: %v "+
				"(re-take the backup with a controller version that persists source spec)",
			backup.Namespace, backup.Name, err))
	}

	if err := r.patchPostgresAppForRestore(ctx, targetApp, sourceServerName, sourceDestinationPath, sourceEndpointURL, options.RecoveryTime, rendered.BarmanObjectStore.S3Credentials, sourceDatabases, sourceUsers); err != nil {
		return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf("failed to patch Postgres app spec: %v", err))
	}

	clusterName := cnpgClusterNameForApp(target.AppName)

	// CNPG forbids changing bootstrap on an initialized Cluster ("Only one
	// bootstrap" CRD validation), so the only way to make it consume
	// bootstrap.recovery is to delete the live Cluster + PVCs. The chart
	// re-renders with the patched values producing a fresh Cluster. The same
	// applies to in-place AND to-copy: in to-copy the target Postgres app
	// was likely already initdb-bootstrapped, so we must purge it too.
	//
	// Idempotency: once we've purged for this RestoreJob, record it on a
	// status condition so subsequent reconciles don't loop on the purge
	// step every time the new Cluster comes up with bootstrap.recovery.
	// Without this guard, a retried RestoreJob targeting an already-restored
	// app would skip the purge and report success against stale data.
	//
	// Status writes can race (informer-stale conflicts, transient etcd
	// errors). If Status().Update fails after a successful purge, the next
	// reconcile would re-enter this block with purgedCondition=false. To
	// avoid re-purging the freshly-recovered Cluster in that case, we also
	// check the live Cluster for bootstrap.recovery: if present, the chart
	// has already re-rendered after a previous purge, and we must NOT delete
	// it again.
	hasRecovery, err := r.clusterHasRecoveryBootstrap(ctx, target.Namespace, clusterName)
	if err != nil {
		return ctrl.Result{}, err
	}
	purgedCondition := apimeta.IsStatusConditionTrue(restoreJob.Status.Conditions, restoreCondTargetPurged)
	if cnpgPurgeNeeded(purgedCondition, hasRecovery) {
		if err := r.purgeExistingCluster(ctx, target.Namespace, clusterName); err != nil {
			return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf("failed to purge existing cluster: %v", err))
		}
	}
	if !purgedCondition {
		apimeta.SetStatusCondition(&restoreJob.Status.Conditions, metav1.Condition{
			Type:    restoreCondTargetPurged,
			Status:  metav1.ConditionTrue,
			Reason:  "ClusterPurged",
			Message: fmt.Sprintf("Cluster %s/%s deleted; awaiting chart-rendered re-bootstrap", target.Namespace, clusterName),
		})
		if err := r.Status().Update(ctx, restoreJob); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: cnpgPollInterval}, nil
	}

	deadline := options.effectiveRestoreDeadline()
	if restoreJob.Status.StartedAt != nil && time.Since(restoreJob.Status.StartedAt.Time) > deadline {
		return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf(
			"RestoreJob exceeded %s deadline before target Cluster reached a healthy state (override via spec.options.restoreTimeoutSeconds)",
			deadline))
	}

	if !hasRecovery {
		return ctrl.Result{RequeueAfter: cnpgPollInterval}, nil
	}

	healthy, err := r.cnpgClusterHealthy(ctx, target.Namespace, clusterName)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !healthy {
		return ctrl.Result{RequeueAfter: cnpgPollInterval}, nil
	}

	now := metav1.Now()
	restoreJob.Status.CompletedAt = &now
	restoreJob.Status.Phase = backupsv1alpha1.RestoreJobPhaseSucceeded
	apimeta.SetStatusCondition(&restoreJob.Status.Conditions, metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionTrue,
		Reason:  "RestoreCompleted",
		Message: "target cnpg.io Cluster reached healthy state",
	})
	if err := r.Status().Update(ctx, restoreJob); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// cnpgRestoreTarget captures the resolved target for a CNPG restore.
type cnpgRestoreTarget struct {
	Namespace string
	AppName   string
	Kind      string
	APIGroup  string
	IsCopy    bool
}

func (r *RestoreJobReconciler) resolveCNPGRestoreTarget(restoreJob *backupsv1alpha1.RestoreJob, backup *backupsv1alpha1.Backup) cnpgRestoreTarget {
	t := cnpgRestoreTarget{
		Namespace: backup.Namespace,
		AppName:   backup.Spec.ApplicationRef.Name,
		Kind:      backup.Spec.ApplicationRef.Kind,
	}
	if backup.Spec.ApplicationRef.APIGroup != nil {
		t.APIGroup = *backup.Spec.ApplicationRef.APIGroup
	}
	if restoreJob.Spec.TargetApplicationRef != nil {
		if restoreJob.Spec.TargetApplicationRef.Name != "" && restoreJob.Spec.TargetApplicationRef.Name != t.AppName {
			t.AppName = restoreJob.Spec.TargetApplicationRef.Name
			t.IsCopy = true
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

// patchPostgresAppForRestore writes the restore-related fields into the
// target Postgres app instance spec. The chart already exposes these knobs;
// once the HelmRelease re-renders, the cnpg.io Cluster picks up
// bootstrap.recovery and externalClusters[].barmanObjectStore.
//
// Credentials are forwarded as a Secret reference (spec.backup.s3CredentialsSecret),
// not as cleartext keys. The controller never reads the Secret itself; the
// chart wires the named Secret straight into barmanObjectStore.s3Credentials.
// This keeps S3 access keys out of the Postgres CR .spec, etcd, audit logs,
// and any tenant-readable copies.
//
// Mirrors source's spec.databases and spec.users so the chart's post-install
// init-job (which DROPs any database/role not in chart values) doesn't
// nuke the restored data right after recovery. Source keys are overlaid on
// top of target keys: the restored cluster's data wins, but target-only
// databases/users (added intentionally on the target app) are preserved.
func (r *RestoreJobReconciler) patchPostgresAppForRestore(
	ctx context.Context,
	app *postgresapp.Postgres,
	sourceServerName, sourceDestinationPath, sourceEndpointURL, recoveryTime string,
	credsRef *strategyv1alpha1.S3CredentialsTemplate,
	sourceDatabases map[string]postgresapp.Database,
	sourceUsers map[string]postgresapp.User,
) error {
	patched := buildPostgresAppRestorePatch(app, sourceServerName, sourceDestinationPath, sourceEndpointURL, recoveryTime, credsRef, sourceDatabases, sourceUsers)
	return r.Patch(ctx, patched, client.MergeFrom(app), client.FieldOwner(cnpgFieldManager))
}

// buildPostgresAppRestorePatch returns a deep-copied Postgres application
// instance with restore-related fields overlaid. Pure: no cluster I/O. The
// wrapper above invokes the Kubernetes Patch.
//
// Stale state hygiene: app.DeepCopy() carries any prior-restore values
// (recoveryTime from a different PITR, an old s3CredentialsSecret, an
// inline s3AccessKey/s3SecretKey, an outdated databases/users map). Each of
// those is cleared before the new values are applied so a re-restore into
// the same target does not replay or leak old configuration. Databases
// and users are authoritative-from-source: the recovered cluster carries
// the source's catalog and roles, so the chart's init-job (which DROPs
// anything not in spec) must see the source's exact map.
func buildPostgresAppRestorePatch(
	app *postgresapp.Postgres,
	sourceServerName, sourceDestinationPath, sourceEndpointURL, recoveryTime string,
	credsRef *strategyv1alpha1.S3CredentialsTemplate,
	sourceDatabases map[string]postgresapp.Database,
	sourceUsers map[string]postgresapp.User,
) *postgresapp.Postgres {
	patched := app.DeepCopy()
	patched.Spec.Bootstrap.Enabled = true
	patched.Spec.Bootstrap.OldName = sourceServerName
	patched.Spec.Bootstrap.ServerName = sourceServerName
	patched.Spec.Bootstrap.RecoveryTime = recoveryTime

	patched.Spec.Backup.DestinationPath = sourceDestinationPath
	patched.Spec.Backup.EndpointURL = sourceEndpointURL
	// Switching to s3CredentialsSecret means inline keys must not survive
	// on the CR .spec; otherwise tenants who switch credential modes leave
	// cleartext keys behind in etcd and audit logs.
	patched.Spec.Backup.S3AccessKey = ""
	patched.Spec.Backup.S3SecretKey = ""
	patched.Spec.Backup.S3CredentialsSecret = postgresapp.S3CredentialsSecret{}
	if credsRef != nil && credsRef.SecretRef.Name != "" {
		patched.Spec.Backup.S3CredentialsSecret = postgresapp.S3CredentialsSecret{
			Name:               credsRef.SecretRef.Name,
			AccessKeyIDKey:     credsRef.AccessKeyIDKey,
			SecretAccessKeyKey: credsRef.SecretAccessKeyKey,
		}
	}
	// Replace, do not merge: the recovered cluster's data and role catalog
	// match source's spec exactly. Merging would let stale entries from a
	// prior restore (or operator-added drift) ride along and the chart's
	// init-job would either re-create those roles against source's data or
	// fail to drop ones the source doesn't have.
	patched.Spec.Databases = sourceDatabases
	patched.Spec.Users = sourceUsers
	return patched
}

// cnpgBackupSnapshot is the postgresql.cnpg.io-specific payload persisted in
// Backup.status.underlyingResources at backup time. It snapshots the source
// app's spec.databases and spec.users so a future RestoreJob can mirror them
// onto the target without re-reading the live source app (which may have
// been deleted by the time the restore fires).
type cnpgBackupSnapshot struct {
	Kind       string                          `json:"kind"`
	APIVersion string                          `json:"apiVersion"`
	Databases  map[string]postgresapp.Database `json:"databases,omitempty"`
	Users      map[string]postgresapp.User     `json:"users,omitempty"`
}

const cnpgBackupSnapshotKind = "CNPGBackupSnapshot"

// cnpgBackupSnapshotAPIVersion is the apiVersion stamped onto the snapshot.
// Borrows the Cozystack backups group so the field is self-typed within the
// existing API surface.
var cnpgBackupSnapshotAPIVersion = backupsv1alpha1.GroupVersion.String()

// marshalCNPGBackupSnapshot serializes the source app's spec.databases /
// spec.users into a runtime.RawExtension suitable for
// Backup.Status.UnderlyingResources.
func marshalCNPGBackupSnapshot(app *postgresapp.Postgres) (*runtime.RawExtension, error) {
	if app == nil {
		return nil, nil
	}
	snap := cnpgBackupSnapshot{
		Kind:       cnpgBackupSnapshotKind,
		APIVersion: cnpgBackupSnapshotAPIVersion,
		Databases:  app.Spec.Databases,
		Users:      app.Spec.Users,
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		return nil, err
	}
	return &runtime.RawExtension{Raw: raw}, nil
}

// unmarshalCNPGBackupSnapshot reads the snapshot persisted at backup time
// from Backup.status.underlyingResources. Returns an error when the field
// is missing or carries the wrong Kind/APIVersion - in either case the
// caller must fail the restore rather than silently disable database/user
// mirroring (which would let the chart's init-job DROP recovered roles).
func unmarshalCNPGBackupSnapshot(backup *backupsv1alpha1.Backup) (map[string]postgresapp.Database, map[string]postgresapp.User, error) {
	if backup == nil || backup.Status.UnderlyingResources == nil || len(backup.Status.UnderlyingResources.Raw) == 0 {
		return nil, nil, fmt.Errorf("status.underlyingResources is empty")
	}
	var snap cnpgBackupSnapshot
	if err := json.Unmarshal(backup.Status.UnderlyingResources.Raw, &snap); err != nil {
		return nil, nil, fmt.Errorf("decode snapshot: %w", err)
	}
	if snap.Kind != cnpgBackupSnapshotKind {
		return nil, nil, fmt.Errorf("unexpected snapshot kind %q (want %q)", snap.Kind, cnpgBackupSnapshotKind)
	}
	return snap.Databases, snap.Users, nil
}

// purgeExistingCluster deletes the live cnpg.io Cluster and its PVCs so the
// chart-rendered Cluster can re-bootstrap from S3. Used only by the in-place
// restore variant.
func (r *RestoreJobReconciler) purgeExistingCluster(ctx context.Context, namespace, clusterName string) error {
	logger := getLogger(ctx)

	cluster := &cnpgtypes.Cluster{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: clusterName},
	}
	if err := r.Delete(ctx, cluster); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete cnpg Cluster %s/%s: %w", namespace, clusterName, err)
	}
	logger.Debug("deleted cnpg Cluster (or already absent)", "namespace", namespace, "name", clusterName)

	pvcList := &corev1.PersistentVolumeClaimList{}
	if err := r.List(ctx, pvcList,
		client.InNamespace(namespace),
		client.MatchingLabels{cnpgClusterLabel: clusterName},
	); err != nil {
		return fmt.Errorf("list PVCs for cluster %s/%s: %w", namespace, clusterName, err)
	}
	for i := range pvcList.Items {
		pvc := &pvcList.Items[i]
		if err := r.Delete(ctx, pvc); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete PVC %s/%s: %w", pvc.Namespace, pvc.Name, err)
		}
	}
	logger.Debug("deleted cnpg cluster PVCs", "count", len(pvcList.Items))
	return nil
}

// cnpgClusterHealthy returns true once the named cnpg.io Cluster reports its
// healthy phase. Treats a missing Cluster as not-yet-healthy.
func (r *RestoreJobReconciler) cnpgClusterHealthy(ctx context.Context, namespace, clusterName string) (bool, error) {
	cluster := &cnpgtypes.Cluster{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: clusterName}, cluster); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return cluster.Status.Phase == cnpgClusterHealthyPhase, nil
}

// clusterHasRecoveryBootstrap returns true when the live cnpg.io Cluster's
// spec.bootstrap.recovery is populated - the signal that the chart has
// re-rendered with our restore-shaped values and the operator is using the
// recovery bootstrap path. Treats a missing Cluster as "not yet" rather
// than an error so the caller can keep polling while HelmRelease catches up.
func (r *RestoreJobReconciler) clusterHasRecoveryBootstrap(ctx context.Context, namespace, clusterName string) (bool, error) {
	cluster := &cnpgtypes.Cluster{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: clusterName}, cluster); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return cluster.Spec.Bootstrap != nil && cluster.Spec.Bootstrap.Recovery != nil, nil
}

// ---------------------------------------------------------------------------
// Helpers shared between Backup and Restore
// ---------------------------------------------------------------------------

// renderCNPGTemplate templates the strategy template against a context
// containing the live application object and the BackupClass parameters.
// Reuses the same templating helper as the Velero strategy. The Application
// is exposed as JSON-tagged map so user templates keep working with paths
// like {{ .Application.metadata.name }}.
func renderCNPGTemplate(t strategyv1alpha1.CNPGTemplate, app *postgresapp.Postgres, parameters map[string]string) (*strategyv1alpha1.CNPGTemplate, error) {
	appAsMap, err := toJSONMap(app)
	if err != nil {
		return nil, fmt.Errorf("encode application for templating: %w", err)
	}
	templateContext := map[string]interface{}{
		"Application": appAsMap,
		"Parameters":  parameters,
	}
	return template.Template(&t, templateContext)
}

// toJSONMap converts a typed object to a generic map via JSON tags. Used
// so user-authored go-templates continue to address fields by their JSON
// names (e.g. .Application.metadata.name) without leaking the Go struct
// hierarchy to user-facing strategy templates.
func toJSONMap(obj interface{}) (map[string]interface{}, error) {
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

// getPostgresApp fetches the apps.cozystack.io Postgres instance via the
// shared typed client. The Postgres scheme is registered in main.go so the
// controller-runtime cache serves it directly.
func (r *BackupJobReconciler) getPostgresApp(ctx context.Context, namespace, name string) (*postgresapp.Postgres, error) {
	app := &postgresapp.Postgres{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, app); err != nil {
		return nil, err
	}
	return app, nil
}

func (r *RestoreJobReconciler) getPostgresApp(ctx context.Context, namespace, name string) (*postgresapp.Postgres, error) {
	app := &postgresapp.Postgres{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, app); err != nil {
		return nil, err
	}
	return app, nil
}

// newCNPGClusterPatch returns an empty typed Cluster object addressed by
// (namespace, name), ready to receive spec mutations. TypeMeta is set so
// the SSA Apply path can identify the kind without consulting the scheme.
func newCNPGClusterPatch(namespace, name string) *cnpgtypes.Cluster {
	return &cnpgtypes.Cluster{
		TypeMeta: metav1.TypeMeta{
			APIVersion: cnpgtypes.GroupVersion.String(),
			Kind:       "Cluster",
		},
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
	}
}

// buildBarmanObjectStore translates the typed strategy template into the
// shape postgresql.cnpg.io expects for spec.backup.barmanObjectStore.
func buildBarmanObjectStore(t strategyv1alpha1.BarmanObjectStoreTemplate, serverName string) *cnpgtypes.BarmanObjectStoreConfiguration {
	out := &cnpgtypes.BarmanObjectStoreConfiguration{
		DestinationPath: t.DestinationPath,
		ServerName:      serverName,
		EndpointURL:     t.EndpointURL,
	}
	if t.S3Credentials != nil {
		accessKeyKey := t.S3Credentials.AccessKeyIDKey
		if accessKeyKey == "" {
			accessKeyKey = defaultS3AccessKeyIDKey
		}
		secretKeyKey := t.S3Credentials.SecretAccessKeyKey
		if secretKeyKey == "" {
			secretKeyKey = defaultS3SecretAccessKeyKey
		}
		out.S3Credentials = &cnpgtypes.S3Credentials{
			AccessKeyID: &cnpgtypes.SecretKeySelector{
				Name: t.S3Credentials.SecretRef.Name,
				Key:  accessKeyKey,
			},
			SecretAccessKey: &cnpgtypes.SecretKeySelector{
				Name: t.S3Credentials.SecretRef.Name,
				Key:  secretKeyKey,
			},
		}
	}
	if t.Wal != nil && (t.Wal.Compression != "" || t.Wal.Encryption != "") {
		out.Wal = &cnpgtypes.WalBackupConfiguration{
			Compression: t.Wal.Compression,
			Encryption:  t.Wal.Encryption,
		}
	}
	if t.Data != nil && (t.Data.Compression != "" || t.Data.Encryption != "" || t.Data.Jobs != nil) {
		out.Data = &cnpgtypes.DataBackupConfiguration{
			Compression: t.Data.Compression,
			Encryption:  t.Data.Encryption,
			Jobs:        t.Data.Jobs,
		}
	}
	return out
}

// cnpgBackupPhase extracts the lowercase phase + message from a
// postgresql.cnpg.io/Backup status block.
func cnpgBackupPhase(b *cnpgtypes.Backup) (string, string) {
	if b == nil {
		return "", ""
	}
	return b.Status.Phase, b.Status.Error
}

// CNPGRestoreOptions is the typed shape of RestoreJob.Spec.Options for the
// CNPG driver. Mirrors the Velero strategy's RestoreOptions pattern - one
// shared opaque blob, parsed lazily at the dispatch boundary.
type CNPGRestoreOptions struct {
	// RecoveryTime is an optional RFC3339 timestamp the chart maps onto
	// CNPG's bootstrap.recovery.recoveryTarget.targetTime. Empty means
	// recover to the end of the latest WAL in the archive (chart default).
	// +optional
	RecoveryTime string `json:"recoveryTime,omitempty"`

	// RestoreTimeoutSeconds caps the time the driver waits for the target
	// Cluster to reach the healthy phase before it marks the RestoreJob
	// Failed. Zero or unset falls back to cnpgDefaultRestoreDeadline.
	// +optional
	RestoreTimeoutSeconds int64 `json:"restoreTimeoutSeconds,omitempty"`
}

// parseCNPGRestoreOptions decodes RestoreJob.Spec.Options into the typed
// shape. Returns the zero value plus a parse error when the blob is malformed
// so the caller can surface it. Callers keep behaviour permissive against
// future field additions by ignoring the error and proceeding with the zero
// value, but they have the option to log it for tenant-debuggability.
func parseCNPGRestoreOptions(opts *runtime.RawExtension) (CNPGRestoreOptions, error) {
	var out CNPGRestoreOptions
	if opts == nil || len(opts.Raw) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(opts.Raw, &out); err != nil {
		return CNPGRestoreOptions{}, fmt.Errorf("decode restoreJob.spec.options: %w", err)
	}
	return out, nil
}

// effectiveRestoreDeadline returns the configured deadline, applying the
// driver's default when the option is unset or non-positive.
func (o CNPGRestoreOptions) effectiveRestoreDeadline() time.Duration {
	if o.RestoreTimeoutSeconds > 0 {
		return time.Duration(o.RestoreTimeoutSeconds) * time.Second
	}
	return cnpgDefaultRestoreDeadline
}
