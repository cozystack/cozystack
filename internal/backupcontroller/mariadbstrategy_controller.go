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
	"github.com/cozystack/cozystack/internal/backupcontroller/mariadbapp"
	"github.com/cozystack/cozystack/internal/backupcontroller/mariadbtypes"
	"github.com/cozystack/cozystack/internal/template"
)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const (
	mariadbFieldManager = "cozystack-mariadb-backup-driver"

	// mariadb-operator's MariaDB ApplicationDefinition (release.prefix). The
	// cozystack mariadb-rd renders the HelmRelease with releaseName =
	// "mariadb-" + appName (see packages/system/mariadb-rd/cozyrds/mariadb.yaml,
	// release.prefix). The chart's templates/mariadb.yaml then materialises
	// the operator-side k8s.mariadb.com/MariaDB CR with metadata.name set to
	// .Release.Name, so the driver must look up by the prefixed name.
	// Mirrors the CNPG driver's postgresAppPrefix / cnpgClusterNameForApp.
	mariadbAppKind   = "MariaDB"
	mariadbAppPrefix = "mariadb-"

	// Driver-metadata keys persisted on Cozystack Backup artifacts. Restore
	// path reads these to drive the operator Restore CR.
	mariadbBackupNameKey      = "k8s.mariadb.com/backup-name"
	mariadbBackupNamespaceKey = "k8s.mariadb.com/backup-namespace"
	mariadbStorageURIKey      = "k8s.mariadb.com/storage-uri"

	// Polling cadence for the operator Backup/Restore lifecycle.
	mariadbPollInterval = 5 * time.Second

	// Wall-clock cap on a BackupJob waiting for the operator Backup to
	// flip Complete. CNPG uses the same number for the same reason: a
	// permanently-stuck Backup must not pin the BackupJob in Running and
	// wedge the Plan-controller queue.
	mariadbDefaultBackupDeadline = 30 * time.Minute

	// Default deadline on a RestoreJob waiting for the operator Restore
	// to terminate. Tenants override via spec.options.restoreTimeoutSeconds.
	mariadbDefaultRestoreDeadline = 30 * time.Minute

	// mariadbBackupSnapshotKind is the Kind stamped onto the snapshot
	// persisted in Backup.status.underlyingResources. The same shape carries
	// the storage descriptor and the rendered strategy parameters used at
	// backup time, so restore-time re-rendering produces deterministic
	// values.
	mariadbBackupSnapshotKind = "MariaDBBackupSnapshot"
)

// mariadbBackupSnapshotAPIVersion is the apiVersion stamped onto the snapshot.
// Borrows the Cozystack backups group so the field is self-typed within the
// existing API surface.
var mariadbBackupSnapshotAPIVersion = backupsv1alpha1.GroupVersion.String()

// mariadbNameForApp returns the k8s.mariadb.com/MariaDB CR name for a
// cozystack MariaDB application instance. The mapping mirrors the mariadb
// ApplicationDefinition (release.prefix=mariadb-).
func mariadbNameForApp(appName string) string {
	return mariadbAppPrefix + appName
}

// validateMariaDBApplicationRef rejects ApplicationRefs that name a
// Kind/APIGroup the MariaDB driver does not own. The driver assumes
// apps.cozystack.io/MariaDB; without this gate a ref like
// other.example.com/MariaDB would be accepted by the Kind check alone and
// then reconciled against the wrong CRD via the typed client.
func validateMariaDBApplicationRef(ref corev1.TypedLocalObjectReference) error {
	if ref.Kind != mariadbAppKind {
		return fmt.Errorf("MariaDB strategy supports applicationRef.kind=%q, got %q", mariadbAppKind, ref.Kind)
	}
	apiGroup := ""
	if ref.APIGroup != nil {
		apiGroup = *ref.APIGroup
	}
	if apiGroup != "" && apiGroup != mariadbapp.GroupName {
		return fmt.Errorf("MariaDB strategy supports applicationRef.apiGroup=%q, got %q", mariadbapp.GroupName, apiGroup)
	}
	return nil
}

// ---------------------------------------------------------------------------
// BackupJob path
// ---------------------------------------------------------------------------

func (r *BackupJobReconciler) reconcileMariaDB(ctx context.Context, j *backupsv1alpha1.BackupJob, resolved *ResolvedBackupConfig) (ctrl.Result, error) {
	logger := getLogger(ctx)
	logger.Debug("reconciling MariaDB strategy", "backupjob", j.Name, "phase", j.Status.Phase)

	if j.Status.Phase == backupsv1alpha1.BackupJobPhaseSucceeded ||
		j.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
		return ctrl.Result{}, nil
	}

	if err := validateMariaDBApplicationRef(j.Spec.ApplicationRef); err != nil {
		return r.markBackupJobFailed(ctx, j, err.Error())
	}

	if j.Status.StartedAt == nil {
		// Refetch the latest persisted state before writing StartedAt: a
		// stale informer cache that returns StartedAt==nil after we already
		// persisted it would otherwise let the deadline gate slide forward
		// on every poll. Same idempotency pattern as the CNPG driver.
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
			return ctrl.Result{RequeueAfter: mariadbPollInterval}, nil
		}
	}

	strategy := &strategyv1alpha1.MariaDB{}
	if err := r.Get(ctx, client.ObjectKey{Name: resolved.StrategyRef.Name}, strategy); err != nil {
		if apierrors.IsNotFound(err) {
			return r.markBackupJobFailed(ctx, j, fmt.Sprintf("MariaDB strategy not found: %s", resolved.StrategyRef.Name))
		}
		return ctrl.Result{}, err
	}

	app, err := r.getMariaDBApp(ctx, j.Namespace, j.Spec.ApplicationRef.Name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.markBackupJobFailed(ctx, j, fmt.Sprintf("MariaDB application not found: %s/%s", j.Namespace, j.Spec.ApplicationRef.Name))
		}
		return ctrl.Result{}, err
	}

	rendered, err := renderMariaDBTemplate(strategy.Spec.Template, app, resolved.Parameters)
	if err != nil {
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf("failed to template MariaDB strategy: %v", err))
	}

	// Operator-side MariaDB CR carries the prefixed release name (see
	// mariadbNameForApp); verify it exists before we ask the operator to
	// back it up. Returning a NotFound surfaces a transient condition rather
	// than a terminal failure: the chart may still be rendering on a fresh
	// app.
	mdbName := mariadbNameForApp(j.Spec.ApplicationRef.Name)
	if err := r.Get(ctx, types.NamespacedName{Namespace: j.Namespace, Name: mdbName},
		&mariadbtypes.MariaDB{}); err != nil {
		if apierrors.IsNotFound(err) {
			apimeta.SetStatusCondition(&j.Status.Conditions, metav1.Condition{
				Type:    "Ready",
				Status:  metav1.ConditionFalse,
				Reason:  "MariaDBNotReady",
				Message: fmt.Sprintf("waiting for k8s.mariadb.com/MariaDB %s/%s to exist", j.Namespace, mdbName),
			})
			if err := r.Status().Update(ctx, j); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: mariadbPollInterval}, nil
		}
		return ctrl.Result{}, err
	}

	mdbBackup, err := r.ensureMariaDBBackup(ctx, j, rendered)
	if err != nil {
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf("failed to ensure k8s.mariadb.com/Backup: %v", err))
	}

	if j.Status.Phase != backupsv1alpha1.BackupJobPhaseRunning {
		j.Status.Phase = backupsv1alpha1.BackupJobPhaseRunning
		if err := r.Status().Update(ctx, j); err != nil {
			return ctrl.Result{}, err
		}
	}

	cond := apimeta.FindStatusCondition(mdbBackup.Status.Conditions, mariadbtypes.ConditionTypeComplete)

	switch {
	case cond != nil && cond.Status == metav1.ConditionTrue:
		// Backup completed successfully. Materialise the Cozystack artifact
		// and finalise the BackupJob. Idempotent: if the artifact already
		// exists (a previous reconcile created it and then raced on the
		// status update), reuse the existing object.
		if j.Status.BackupRef != nil {
			return ctrl.Result{}, nil
		}
		artifact, err := r.createMariaDBBackupArtifact(ctx, j, resolved, mdbBackup, rendered, app)
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
			Message: "k8s.mariadb.com Backup completed",
		})
		if err := r.Status().Update(ctx, j); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil

	case cond != nil && cond.Status == metav1.ConditionFalse && cond.Reason == mariadbtypes.ConditionReasonJobFailed:
		// Terminal failure. The operator marks Complete=False/JobFailed only
		// after the underlying Job exhausts its backoff budget, so we can
		// fail the BackupJob immediately rather than waiting for the
		// driver-side deadline.
		message := cond.Message
		if message == "" {
			message = "k8s.mariadb.com Backup terminal failure"
		}
		return r.markBackupJobFailed(ctx, j, message)

	default:
		// Still running (Complete=False/JobRunning, Complete=False/JobNotComplete,
		// JobSuspended, or no Complete condition yet). Apply a wall-clock
		// deadline so a permanently-stuck Backup eventually fails the
		// BackupJob instead of pinning it Running forever.
		if mariadbBackupDeadlineExceeded(j.Status.StartedAt) {
			detail := "no Complete condition observed"
			if cond != nil {
				detail = fmt.Sprintf("Complete=%s/%s", cond.Status, cond.Reason)
			}
			return r.markBackupJobFailed(ctx, j, fmt.Sprintf(
				"k8s.mariadb.com Backup did not complete within %s (%s)", mariadbDefaultBackupDeadline, detail))
		}
		return ctrl.Result{RequeueAfter: mariadbPollInterval}, nil
	}
}

// mariadbBackupDeadlineExceeded reports whether enough wall-clock time has
// elapsed since the BackupJob started that we should give up on a stuck
// operator Backup. Returns false when StartedAt is nil so the very first
// reconcile (which sets StartedAt) does not trip the gate.
func mariadbBackupDeadlineExceeded(startedAt *metav1.Time) bool {
	if startedAt == nil {
		return false
	}
	return time.Since(startedAt.Time) > mariadbDefaultBackupDeadline
}

// ensureMariaDBBackup creates a one-shot k8s.mariadb.com/Backup CR labelled
// with the BackupJob, or returns the existing one if a previous reconcile
// already created it. Idempotency relies on the OwningJob labels.
func (r *BackupJobReconciler) ensureMariaDBBackup(ctx context.Context, j *backupsv1alpha1.BackupJob, rendered *strategyv1alpha1.MariaDBTemplate) (*mariadbtypes.Backup, error) {
	existing, err := r.findMariaDBBackupForJob(ctx, j)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	storage, err := buildMariaDBBackupStorage(rendered.Storage)
	if err != nil {
		return nil, err
	}

	obj := &mariadbtypes.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:    j.Namespace,
			GenerateName: fmt.Sprintf("%s-", j.Name),
			Labels: map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      j.Name,
				backupsv1alpha1.OwningJobNamespaceLabel: j.Namespace,
			},
		},
		Spec: mariadbtypes.BackupSpec{
			MariaDBRef:   mariadbtypes.MariaDBObjectRef{Name: mariadbNameForApp(j.Spec.ApplicationRef.Name)},
			Storage:      storage,
			Databases:    append([]string(nil), rendered.Databases...),
			Compression:  rendered.Compression,
			LogLevel:     rendered.LogLevel,
			MaxRetention: rendered.MaxRetention.DeepCopy(),
		},
	}

	if err := r.Create(ctx, obj); err != nil {
		return nil, err
	}
	return obj, nil
}

// findMariaDBBackupForJob returns the k8s.mariadb.com/Backup labelled with
// the BackupJob's OwningJob{Name,Namespace}, if any. Returns (nil, nil)
// when no match is found.
//
// In steady state at most one Backup CR carries a given OwningJob label
// pair (ensureMariaDBBackup is idempotent by this label). The list path
// can in theory observe two if two controller replicas raced Create
// between the first list returning empty and the cache catching up. The
// driver picks list.Items[0] deterministically; the V(1) breadcrumb on
// duplicates exists so an operator triaging "wrong Backup got reused"
// has a log line to grep for instead of staring at silent reuse.
func (r *BackupJobReconciler) findMariaDBBackupForJob(ctx context.Context, j *backupsv1alpha1.BackupJob) (*mariadbtypes.Backup, error) {
	list := &mariadbtypes.BackupList{}
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
		getLogger(ctx).Debug("multiple k8s.mariadb.com/Backup CRs match BackupJob OwningJob labels; reusing first",
			"backupjob", j.Name, "namespace", j.Namespace, "matches", names, "picked", names[0])
	}
	return &list.Items[0], nil
}

// createMariaDBBackupArtifact materialises a Cozystack Backup resource
// carrying the metadata callers need to drive a future restore. The source
// app's spec snapshot plus the rendered strategy parameters are persisted in
// status.underlyingResources so restore-time templating produces the same
// values the backup ran with.
func (r *BackupJobReconciler) createMariaDBBackupArtifact(
	ctx context.Context,
	j *backupsv1alpha1.BackupJob,
	resolved *ResolvedBackupConfig,
	mdbBackup *mariadbtypes.Backup,
	rendered *strategyv1alpha1.MariaDBTemplate,
	sourceApp *mariadbapp.MariaDB,
) (*backupsv1alpha1.Backup, error) {
	takenAt := metav1.Now()
	if cond := apimeta.FindStatusCondition(mdbBackup.Status.Conditions, mariadbtypes.ConditionTypeComplete); cond != nil && !cond.LastTransitionTime.IsZero() {
		takenAt = cond.LastTransitionTime
	}

	driverMD := map[string]string{
		mariadbBackupNameKey:      mdbBackup.Name,
		mariadbBackupNamespaceKey: mdbBackup.Namespace,
	}
	if uri := mariadbBackupURI(rendered, mdbBackup); uri != "" {
		driverMD[mariadbStorageURIKey] = uri
	}

	underlyingResources, err := marshalMariaDBBackupSnapshot(sourceApp, rendered, resolved.Parameters)
	if err != nil {
		return nil, fmt.Errorf("encode source snapshot for Backup.status.underlyingResources: %w", err)
	}

	status := backupsv1alpha1.BackupStatus{
		Phase:               backupsv1alpha1.BackupPhaseReady,
		UnderlyingResources: underlyingResources,
	}
	// Status.Artifact carries a "this backup has an artefact at URI X"
	// signal. PVC/Volume storage has no stable URI (the location is the
	// operator's PVC ID), so an Artifact{URI: ""} is a worse signal than
	// no Artifact at all - leave it nil instead.
	if uri := driverMD[mariadbStorageURIKey]; uri != "" {
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

// mariadbBackupURI synthesises a human-readable URI for the Cozystack
// Backup artifact. Empty when the storage shape doesn't resolve to a stable
// URI (PVC/Volume, where the location is the operator's PVC ID).
func mariadbBackupURI(rendered *strategyv1alpha1.MariaDBTemplate, mdbBackup *mariadbtypes.Backup) string {
	if rendered.Storage.S3 == nil {
		return ""
	}
	s := rendered.Storage.S3
	prefix := s.Prefix
	if prefix != "" && prefix[len(prefix)-1] != '/' {
		prefix += "/"
	}
	return fmt.Sprintf("s3://%s/%s%s", s.Bucket, prefix, mdbBackup.Name)
}

// ---------------------------------------------------------------------------
// RestoreJob path
// ---------------------------------------------------------------------------

func (r *RestoreJobReconciler) reconcileMariaDBRestore(ctx context.Context, restoreJob *backupsv1alpha1.RestoreJob, backup *backupsv1alpha1.Backup) (ctrl.Result, error) {
	logger := getLogger(ctx)
	logger.Debug("reconciling MariaDB restore", "restorejob", restoreJob.Name, "backup", backup.Name)

	if err := validateMariaDBApplicationRef(backup.Spec.ApplicationRef); err != nil {
		return r.markRestoreJobFailed(ctx, restoreJob, err.Error())
	}

	// Validate the resolved target shape before any apiserver call. This is
	// cheap (in-memory state only) and lets a malformed TargetApplicationRef
	// fail fast instead of after parseMariaDBRestoreOptions + the srcBackup
	// Get round-trip.
	target := r.resolveMariaDBRestoreTarget(restoreJob, backup)
	if target.Kind != mariadbAppKind {
		return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf(
			"target applicationRef.kind=%q is not supported by the MariaDB driver", target.Kind))
	}
	if target.APIGroup != "" && target.APIGroup != mariadbapp.GroupName {
		return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf(
			"target applicationRef.apiGroup=%q is not supported by the MariaDB driver", target.APIGroup))
	}

	if restoreJob.Status.StartedAt == nil {
		// Refetch the latest persisted state before writing StartedAt; same
		// stale-cache idempotency pattern the BackupJob path uses.
		fresh := &backupsv1alpha1.RestoreJob{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: restoreJob.Namespace, Name: restoreJob.Name}, fresh); err != nil {
			return ctrl.Result{}, err
		}
		if fresh.Status.StartedAt != nil {
			// A previous reconcile (or another replica) already persisted
			// StartedAt - adopt it locally and continue inline so we don't
			// waste a reconcile cycle bouncing through the cache. Mirrors
			// the BackupJob path above.
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
			// Requeue so the patched status round-trips via the informer
			// cache before subsequent reconciles read it back.
			return ctrl.Result{RequeueAfter: mariadbPollInterval}, nil
		}
	}

	// parseMariaDBRestoreOptions is intentionally permissive (event + log,
	// proceed with defaults). This diverges from the structural failures
	// below (missing driverMetadata, MariaDB CR genuinely absent) because a
	// bad spec.options is best-effort tenant-supplied override, not a
	// load-bearing piece of the restore contract - the defaults are still
	// the right behaviour, so surfacing a transient breadcrumb beats
	// failing the restore on a typo.
	options, err := parseMariaDBRestoreOptions(restoreJob.Spec.Options)
	if err != nil {
		logger.Info("malformed restoreJob.spec.options; falling back to defaults", "error", err)
		r.Recorder.Eventf(restoreJob, corev1.EventTypeWarning, "MalformedOptions",
			"spec.options is not valid JSON; falling back to defaults: %v", err)
	}

	sourceBackupName := backup.Spec.DriverMetadata[mariadbBackupNameKey]
	if sourceBackupName == "" {
		return r.markRestoreJobFailed(ctx, restoreJob,
			"Backup driverMetadata is missing k8s.mariadb.com/backup-name (re-take the backup with a controller version that persists the operator Backup name)")
	}

	// Verify the operator Backup CR still exists. The operator's Restore CR
	// only resolves backupRef in the same namespace as the Restore itself,
	// so cross-namespace restores aren't supported here. NotFound surfaces
	// as a terminal failure with a clear migration path (re-run the
	// BackupJob to materialise a fresh artifact).
	srcBackup := &mariadbtypes.Backup{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: backup.Namespace, Name: sourceBackupName}, srcBackup); err != nil {
		if apierrors.IsNotFound(err) {
			return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf(
				"k8s.mariadb.com/Backup %s/%s not found; the operator-side artifact has been reaped",
				backup.Namespace, sourceBackupName))
		}
		return ctrl.Result{}, err
	}

	// The operator Restore CR replays the dump against an existing live
	// MariaDB; for to-copy this requires the target MariaDB to exist. A
	// tenant who fires a RestoreJob seconds before their target
	// HelmRelease materialises the MariaDB CR shouldn't have to recreate
	// the RestoreJob - mirror the backup path's transient handling and let
	// the operator-side StartedAt + effectiveRestoreDeadline guard against
	// a target that never shows up.
	targetMDBName := mariadbNameForApp(target.AppName)
	if err := r.Get(ctx, types.NamespacedName{Namespace: target.Namespace, Name: targetMDBName},
		&mariadbtypes.MariaDB{}); err != nil {
		if apierrors.IsNotFound(err) {
			deadline := options.effectiveRestoreDeadline()
			if restoreJob.Status.StartedAt != nil && time.Since(restoreJob.Status.StartedAt.Time) > deadline {
				return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf(
					"target k8s.mariadb.com/MariaDB %s/%s not found within %s (deploy the target MariaDB application before requesting the restore; override via spec.options.restoreTimeoutSeconds)",
					target.Namespace, targetMDBName, deadline))
			}
			apimeta.SetStatusCondition(&restoreJob.Status.Conditions, metav1.Condition{
				Type:   "Ready",
				Status: metav1.ConditionFalse,
				Reason: "TargetMariaDBNotReady",
				Message: fmt.Sprintf("waiting for target k8s.mariadb.com/MariaDB %s/%s to exist",
					target.Namespace, targetMDBName),
			})
			if err := r.Status().Update(ctx, restoreJob); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: mariadbPollInterval}, nil
		}
		return ctrl.Result{}, err
	}

	mdbRestore, err := r.ensureMariaDBRestore(ctx, restoreJob, sourceBackupName, targetMDBName)
	if err != nil {
		return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf("failed to ensure k8s.mariadb.com/Restore: %v", err))
	}

	cond := apimeta.FindStatusCondition(mdbRestore.Status.Conditions, mariadbtypes.ConditionTypeComplete)
	switch {
	case cond != nil && cond.Status == metav1.ConditionTrue:
		now := metav1.Now()
		restoreJob.Status.CompletedAt = &now
		restoreJob.Status.Phase = backupsv1alpha1.RestoreJobPhaseSucceeded
		apimeta.SetStatusCondition(&restoreJob.Status.Conditions, metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionTrue,
			Reason:  "RestoreCompleted",
			Message: fmt.Sprintf("k8s.mariadb.com/Restore %s/%s completed", mdbRestore.Namespace, mdbRestore.Name),
		})
		if err := r.Status().Update(ctx, restoreJob); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil

	case cond != nil && cond.Status == metav1.ConditionFalse && cond.Reason == mariadbtypes.ConditionReasonJobFailed:
		message := cond.Message
		if message == "" {
			message = fmt.Sprintf("k8s.mariadb.com/Restore %s/%s terminal failure", mdbRestore.Namespace, mdbRestore.Name)
		}
		return r.markRestoreJobFailed(ctx, restoreJob, message)

	default:
		deadline := options.effectiveRestoreDeadline()
		if restoreJob.Status.StartedAt != nil && time.Since(restoreJob.Status.StartedAt.Time) > deadline {
			detail := "no Complete condition observed"
			if cond != nil {
				detail = fmt.Sprintf("Complete=%s/%s", cond.Status, cond.Reason)
			}
			return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf(
				"k8s.mariadb.com/Restore did not complete within %s (%s; override via spec.options.restoreTimeoutSeconds)",
				deadline, detail))
		}
		return ctrl.Result{RequeueAfter: mariadbPollInterval}, nil
	}
}

// ensureMariaDBRestore creates a k8s.mariadb.com/Restore CR labelled with
// the RestoreJob, or returns the existing one if a previous reconcile
// already created it.
func (r *RestoreJobReconciler) ensureMariaDBRestore(ctx context.Context, rj *backupsv1alpha1.RestoreJob, sourceBackupName, targetMariaDBName string) (*mariadbtypes.Restore, error) {
	list := &mariadbtypes.RestoreList{}
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

	obj := &mariadbtypes.Restore{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:    rj.Namespace,
			GenerateName: fmt.Sprintf("%s-", rj.Name),
			Labels: map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      rj.Name,
				backupsv1alpha1.OwningJobNamespaceLabel: rj.Namespace,
			},
		},
		Spec: mariadbtypes.RestoreSpec{
			MariaDBRef: mariadbtypes.MariaDBObjectRef{Name: targetMariaDBName},
			BackupRef: &mariadbtypes.BackupReference{
				Kind: mariadbtypes.BackupKind,
				Name: sourceBackupName,
			},
		},
	}
	if err := r.Create(ctx, obj); err != nil {
		return nil, err
	}
	return obj, nil
}

// mariadbRestoreTarget captures the resolved target for a MariaDB restore.
// The reconcile path treats in-place and to-copy identically: both create a
// k8s.mariadb.com/Restore CR pointing at the named target MariaDB. Callers
// infer the mode from AppName != backup.Spec.ApplicationRef.Name.
type mariadbRestoreTarget struct {
	Namespace string
	AppName   string
	Kind      string
	APIGroup  string
}

func (r *RestoreJobReconciler) resolveMariaDBRestoreTarget(restoreJob *backupsv1alpha1.RestoreJob, backup *backupsv1alpha1.Backup) mariadbRestoreTarget {
	t := mariadbRestoreTarget{
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

// mariadbBackupSnapshot is the MariaDB-specific payload persisted in
// Backup.status.underlyingResources at backup time. Carries the rendered
// storage descriptor and BackupClass parameters so a future RestoreJob can
// reproduce them exactly when the operator-side Backup CR has been pruned.
type mariadbBackupSnapshot struct {
	Kind       string                              `json:"kind"`
	APIVersion string                              `json:"apiVersion"`
	Storage    *strategyv1alpha1.MariaDBStorageTemplate `json:"storage,omitempty"`
	Databases  []string                            `json:"databases,omitempty"`
	Parameters map[string]string                   `json:"parameters,omitempty"`
}

func marshalMariaDBBackupSnapshot(_ *mariadbapp.MariaDB, rendered *strategyv1alpha1.MariaDBTemplate, parameters map[string]string) (*runtime.RawExtension, error) {
	storage := rendered.Storage.DeepCopy()
	snap := mariadbBackupSnapshot{
		Kind:       mariadbBackupSnapshotKind,
		APIVersion: mariadbBackupSnapshotAPIVersion,
		Storage:    storage,
		Databases:  append([]string(nil), rendered.Databases...),
		Parameters: parameters,
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		return nil, err
	}
	return &runtime.RawExtension{Raw: raw}, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// renderMariaDBTemplate templates the strategy template against a context
// containing the live application object and the BackupClass parameters.
// Reuses the same templating helper as the CNPG / Velero strategies.
func renderMariaDBTemplate(t strategyv1alpha1.MariaDBTemplate, app *mariadbapp.MariaDB, parameters map[string]string) (*strategyv1alpha1.MariaDBTemplate, error) {
	appAsMap, err := toJSONMapMariaDB(app)
	if err != nil {
		return nil, fmt.Errorf("encode application for templating: %w", err)
	}
	templateContext := map[string]interface{}{
		"Application": appAsMap,
		"Parameters":  parameters,
	}
	return template.Template(&t, templateContext)
}

// toJSONMapMariaDB converts a typed object to a generic map via JSON tags
// so user-authored go-templates address fields by their JSON names (e.g.
// .Application.metadata.name). Mirrors the helper in the CNPG controller;
// scoped here to avoid cross-strategy import.
func toJSONMapMariaDB(obj interface{}) (map[string]interface{}, error) {
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

// getMariaDBApp fetches the apps.cozystack.io MariaDB instance via the
// shared typed client. The MariaDB scheme is registered in main.go so the
// controller-runtime cache serves it directly.
func (r *BackupJobReconciler) getMariaDBApp(ctx context.Context, namespace, name string) (*mariadbapp.MariaDB, error) {
	app := &mariadbapp.MariaDB{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, app); err != nil {
		return nil, err
	}
	return app, nil
}

// buildMariaDBBackupStorage translates the typed strategy template into the
// k8s.mariadb.com/v1alpha1 Backup.spec.storage shape. Either S3, PVC or
// Volume must be set; the operator's OpenAPI rejects a Backup with an empty
// storage block, so we surface that as a strategy validation error.
func buildMariaDBBackupStorage(in strategyv1alpha1.MariaDBStorageTemplate) (mariadbtypes.BackupStorage, error) {
	switch {
	case in.S3 != nil:
		out := &mariadbtypes.S3Storage{
			Bucket:   in.S3.Bucket,
			Endpoint: in.S3.Endpoint,
			Prefix:   in.S3.Prefix,
			Region:   in.S3.Region,
			AccessKeyIdSecretKeyRef: mariadbtypes.SecretKeySelector{
				Name: in.S3.AccessKeyIDSecretKeyRef.Name,
				Key:  in.S3.AccessKeyIDSecretKeyRef.Key,
			},
			SecretAccessKeySecretKeyRef: mariadbtypes.SecretKeySelector{
				Name: in.S3.SecretAccessKeySecretKeyRef.Name,
				Key:  in.S3.SecretAccessKeySecretKeyRef.Key,
			},
		}
		if in.S3.SessionTokenSecretKeyRef != nil {
			out.SessionTokenSecretKeyRef = &mariadbtypes.SecretKeySelector{
				Name: in.S3.SessionTokenSecretKeyRef.Name,
				Key:  in.S3.SessionTokenSecretKeyRef.Key,
			}
		}
		if in.S3.TLS != nil {
			tls := &mariadbtypes.S3TLS{Enabled: in.S3.TLS.Enabled}
			if in.S3.TLS.CASecretKeyRef != nil {
				tls.CASecretKeyRef = &mariadbtypes.SecretKeySelector{
					Name: in.S3.TLS.CASecretKeyRef.Name,
					Key:  in.S3.TLS.CASecretKeyRef.Key,
				}
			}
			out.TLS = tls
		}
		return mariadbtypes.BackupStorage{S3: out}, nil
	case in.PersistentVolumeClaim != nil:
		return mariadbtypes.BackupStorage{PersistentVolumeClaim: in.PersistentVolumeClaim.DeepCopy()}, nil
	case in.Volume != nil:
		return mariadbtypes.BackupStorage{Volume: in.Volume.DeepCopy()}, nil
	default:
		return mariadbtypes.BackupStorage{}, fmt.Errorf("strategy.spec.template.storage requires exactly one of s3, persistentVolumeClaim or volume")
	}
}

// MariaDBRestoreOptions is the typed shape of RestoreJob.Spec.Options for
// the MariaDB driver. Mirrors the CNPG strategy's RestoreOptions pattern
// so the boundary parses lazily and keeps behaviour permissive.
type MariaDBRestoreOptions struct {
	// RestoreTimeoutSeconds caps the time the driver waits for the
	// k8s.mariadb.com/Restore to terminate before it marks the RestoreJob
	// Failed. Zero or unset falls back to mariadbDefaultRestoreDeadline.
	// +optional
	RestoreTimeoutSeconds int64 `json:"restoreTimeoutSeconds,omitempty"`
}

func parseMariaDBRestoreOptions(opts *runtime.RawExtension) (MariaDBRestoreOptions, error) {
	var out MariaDBRestoreOptions
	if opts == nil || len(opts.Raw) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(opts.Raw, &out); err != nil {
		return MariaDBRestoreOptions{}, fmt.Errorf("decode restoreJob.spec.options: %w", err)
	}
	return out, nil
}

func (o MariaDBRestoreOptions) effectiveRestoreDeadline() time.Duration {
	if o.RestoreTimeoutSeconds > 0 {
		return time.Duration(o.RestoreTimeoutSeconds) * time.Second
	}
	return mariadbDefaultRestoreDeadline
}
