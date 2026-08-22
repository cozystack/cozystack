// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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
	"github.com/cozystack/cozystack/internal/backupcontroller/mongodbapp"
	"github.com/cozystack/cozystack/internal/backupcontroller/psmdbtypes"
	"github.com/cozystack/cozystack/internal/template"
)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const (
	// mongodbAppKind / mongodbAppPrefix map a cozystack MongoDB application
	// instance to its operator-side psmdb.percona.com/PerconaServerMongoDB CR.
	// The mongodb-rd ApplicationDefinition renders the HelmRelease with
	// releaseName = "mongodb-" + appName (release.prefix in
	// packages/system/mongodb-rd/cozyrds/mongodb.yaml), and the chart's
	// templates/mongodb.yaml sets the PerconaServerMongoDB metadata.name to
	// .Release.Name — so the driver looks up by the prefixed name. Mirrors the
	// MariaDB driver's mariadbAppPrefix.
	mongodbAppKind   = "MongoDB"
	mongodbAppPrefix = "mongodb-"

	// psmdbDefaultStorageName is the storage the mongodb chart declares in the
	// PerconaServerMongoDB CR's spec.backup.storages when backup.enabled=true
	// (see packages/apps/mongodb/templates/mongodb.yaml). The strategy names a
	// storage; when it leaves StorageName empty the driver falls back to this.
	psmdbDefaultStorageName = "s3-storage"

	// Driver-metadata keys persisted on Cozystack Backup artifacts. The
	// restore path reads Destination (+ the S3 snapshot) to drive the operator
	// Restore CR's backupSource, so a restore-to-differently-named instance
	// works without the source PerconaServerMongoDBBackup CR still existing.
	psmdbBackupNameKey      = "psmdb.percona.com/backup-name"
	psmdbBackupNamespaceKey = "psmdb.percona.com/backup-namespace"
	psmdbDestinationKey     = "psmdb.percona.com/destination"

	// Polling cadence for the operator Backup/Restore lifecycle.
	psmdbPollInterval = 5 * time.Second

	// Wall-clock cap on a BackupJob waiting for the operator Backup to reach
	// state=ready. A permanently-stuck backup (e.g. the cluster never brings
	// up its pbm agents) must not pin the BackupJob in Running and wedge the
	// Plan-controller queue. Mirrors the MariaDB/CNPG deadline.
	psmdbDefaultBackupDeadline = 30 * time.Minute

	// Default deadline on a RestoreJob waiting for the operator Restore to
	// terminate. Tenants override via spec.options.restoreTimeoutSeconds.
	psmdbDefaultRestoreDeadline = 30 * time.Minute

	// psmdbBackupSnapshotKind is the Kind stamped onto the snapshot persisted
	// in Backup.status.underlyingResources. It carries the S3 storage
	// descriptor (by reference — bucket/endpoint/credentialsSecret NAME, never
	// a raw credential) and the backup destination read back from the operator
	// Backup status, so the restore path can rebuild backupSource even after
	// the operator-side Backup CR has been reaped.
	psmdbBackupSnapshotKind = "MongoDBBackupSnapshot"
)

// psmdbBackupSnapshotAPIVersion is the apiVersion stamped onto the snapshot.
// Borrows the Cozystack backups group so the field is self-typed within the
// existing API surface. Mirrors the MariaDB driver.
var psmdbBackupSnapshotAPIVersion = backupsv1alpha1.GroupVersion.String()

// mongodbNameForApp returns the psmdb.percona.com/PerconaServerMongoDB CR name
// for a cozystack MongoDB application instance.
func mongodbNameForApp(appName string) string {
	return mongodbAppPrefix + appName
}

// validateMongoDBApplicationRef rejects ApplicationRefs that name a
// Kind/APIGroup the MongoDB driver does not own. Empty APIGroup is accepted and
// treated as the default (apps.cozystack.io), matching the BackupClass
// resolution helpers and the Plan/BackupJob CRD docs.
func validateMongoDBApplicationRef(ref corev1.TypedLocalObjectReference) error {
	if ref.Kind != mongodbAppKind {
		return fmt.Errorf("MongoDB strategy supports applicationRef.kind=%q, got %q", mongodbAppKind, ref.Kind)
	}
	apiGroup := ""
	if ref.APIGroup != nil {
		apiGroup = *ref.APIGroup
	}
	if apiGroup != "" && apiGroup != mongodbapp.GroupName {
		return fmt.Errorf("MongoDB strategy supports applicationRef.apiGroup=%q, got %q", mongodbapp.GroupName, apiGroup)
	}
	return nil
}

// psmdbBackupTypeOrDefault returns the rendered backup type, defaulting to
// logical. Only logical is supported (the CRD enum on MongoDBTemplate.Type
// enforces this at admission; this keeps a pre-admission strategy well-defined).
func psmdbBackupTypeOrDefault(t string) string {
	if t == "" {
		return psmdbtypes.BackupTypeLogical
	}
	return t
}

// psmdbStorageNameOrDefault resolves the storage name the operator Backup CR
// references, defaulting to the chart's storage.
func psmdbStorageNameOrDefault(name string) string {
	if name == "" {
		return psmdbDefaultStorageName
	}
	return name
}

// ---------------------------------------------------------------------------
// BackupJob path
// ---------------------------------------------------------------------------

func (r *BackupJobReconciler) reconcileMongoDB(ctx context.Context, j *backupsv1alpha1.BackupJob, resolved *ResolvedBackupConfig) (ctrl.Result, error) {
	logger := getLogger(ctx)
	logger.Debug("reconciling MongoDB strategy", "backupjob", j.Name, "phase", j.Status.Phase)

	if j.Status.Phase == backupsv1alpha1.BackupJobPhaseSucceeded ||
		j.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
		return ctrl.Result{}, nil
	}

	if err := validateMongoDBApplicationRef(j.Spec.ApplicationRef); err != nil {
		return r.markBackupJobFailed(ctx, j, err.Error())
	}

	if j.Status.StartedAt == nil {
		// Refetch the latest persisted state before writing StartedAt: a stale
		// informer cache that returns StartedAt==nil after we already persisted
		// it would otherwise let the deadline gate slide forward on every poll.
		// Same idempotency pattern as the MariaDB/CNPG drivers.
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
			return ctrl.Result{RequeueAfter: psmdbPollInterval}, nil
		}
	}

	strategy := &strategyv1alpha1.MongoDB{}
	if err := r.Get(ctx, client.ObjectKey{Name: resolved.StrategyRef.Name}, strategy); err != nil {
		if apierrors.IsNotFound(err) {
			return r.requeueStrategyNotReady(ctx, j, resolved.StrategyRef.Name)
		}
		return ctrl.Result{}, err
	}

	app, err := r.getMongoDBApp(ctx, j.Namespace, j.Spec.ApplicationRef.Name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// A GitOps apply that lands the MongoDB app and the BackupJob in one
			// commit can reconcile the BackupJob before the app CR is visible in
			// the cache. Tolerate a not-yet-present app with the same bounded
			// grace the psmdb-CR existence gate below uses, instead of failing
			// terminally on a transient ordering race. StartedAt was persisted
			// above, so the deadline clock is already running.
			if psmdbBackupDeadlineExceeded(j.Status.StartedAt) {
				return r.markBackupJobFailed(ctx, j, fmt.Sprintf(
					"MongoDB application %s/%s not found within %s",
					j.Namespace, j.Spec.ApplicationRef.Name, psmdbDefaultBackupDeadline))
			}
			return r.requeueMongoDBBackupWaiting(ctx, j, "MongoDBApplicationNotReady",
				fmt.Sprintf("waiting for MongoDB application %s/%s to exist", j.Namespace, j.Spec.ApplicationRef.Name))
		}
		return ctrl.Result{}, err
	}

	rendered, err := renderMongoDBTemplate(strategy.Spec.Template, app, resolved.Parameters)
	if err != nil {
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf("failed to template MongoDB strategy: %v", err))
	}
	storageName := psmdbStorageNameOrDefault(rendered.StorageName)

	// The operator-side PerconaServerMongoDB CR carries the prefixed release
	// name. Verify it exists and has backups wired up before we ask the
	// operator to snapshot it. psmdb only services PerconaServerMongoDBBackup
	// CRs when spec.backup.enabled=true and the named storage is declared;
	// without that the Backup would sit in waiting/error forever, so surface a
	// precise precondition instead. Bounded by psmdbDefaultBackupDeadline —
	// StartedAt was persisted above, so the clock is already running.
	psmdbName := mongodbNameForApp(j.Spec.ApplicationRef.Name)
	cluster := &psmdbtypes.PerconaServerMongoDB{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: j.Namespace, Name: psmdbName}, cluster); err != nil {
		if apierrors.IsNotFound(err) {
			if psmdbBackupDeadlineExceeded(j.Status.StartedAt) {
				return r.markBackupJobFailed(ctx, j, fmt.Sprintf(
					"psmdb.percona.com/PerconaServerMongoDB %s/%s never reached existence within %s",
					j.Namespace, psmdbName, psmdbDefaultBackupDeadline))
			}
			return r.requeueMongoDBBackupWaiting(ctx, j, "PerconaServerMongoDBNotReady",
				fmt.Sprintf("waiting for psmdb.percona.com/PerconaServerMongoDB %s/%s to exist", j.Namespace, psmdbName))
		}
		return ctrl.Result{}, err
	}
	if msg := psmdbBackupPrecondition(cluster, storageName); msg != "" {
		if psmdbBackupDeadlineExceeded(j.Status.StartedAt) {
			return r.markBackupJobFailed(ctx, j, fmt.Sprintf(
				"psmdb.percona.com/PerconaServerMongoDB %s/%s not ready for backups within %s: %s (set backup.enabled=true on the MongoDB application)",
				j.Namespace, psmdbName, psmdbDefaultBackupDeadline, msg))
		}
		return r.requeueMongoDBBackupWaiting(ctx, j, "PerconaServerMongoDBBackupsDisabled", msg)
	}

	mdbBackup, err := r.ensureMongoDBBackup(ctx, j, psmdbName, storageName, rendered)
	if err != nil {
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf("failed to ensure psmdb.percona.com/PerconaServerMongoDBBackup: %v", err))
	}

	if j.Status.Phase != backupsv1alpha1.BackupJobPhaseRunning {
		j.Status.Phase = backupsv1alpha1.BackupJobPhaseRunning
		if err := r.Status().Update(ctx, j); err != nil {
			return ctrl.Result{}, err
		}
	}

	switch state := mdbBackup.Status.State; {
	case state == psmdbtypes.StateReady:
		// Backup completed successfully. Materialise the Cozystack artifact and
		// finalise the BackupJob. Idempotent: reuse the existing artifact if a
		// previous reconcile created it and then raced on the status update.
		if j.Status.BackupRef != nil {
			return ctrl.Result{}, nil
		}
		artifact, err := r.createMongoDBBackupArtifact(ctx, j, resolved, mdbBackup, rendered, storageName)
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
			Message: "psmdb.percona.com PerconaServerMongoDBBackup completed",
		})
		if err := r.Status().Update(ctx, j); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil

	case state == psmdbtypes.StateError || state == psmdbtypes.StateRejected:
		// Terminal failure. The operator sets state=error/rejected once the pbm
		// backup fails or is refused, so fail the BackupJob immediately rather
		// than waiting for the driver-side deadline.
		message := mdbBackup.Status.Error
		if message == "" {
			message = fmt.Sprintf("psmdb.percona.com PerconaServerMongoDBBackup reported state=%s", state)
		}
		return r.markBackupJobFailed(ctx, j, message)

	default:
		// Still in progress ("", requested, running, waiting). Apply a
		// wall-clock deadline so a permanently-stuck backup eventually fails
		// the BackupJob instead of pinning it Running forever.
		if psmdbBackupDeadlineExceeded(j.Status.StartedAt) {
			detail := "no state observed"
			if state != "" {
				detail = fmt.Sprintf("state=%s", state)
			}
			return r.markBackupJobFailed(ctx, j, fmt.Sprintf(
				"psmdb.percona.com PerconaServerMongoDBBackup did not complete within %s (%s)", psmdbDefaultBackupDeadline, detail))
		}
		return ctrl.Result{RequeueAfter: psmdbPollInterval}, nil
	}
}

// requeueMongoDBBackupWaiting records a transient Ready=False condition on the
// BackupJob and requeues. Factored out because the backup path has two
// waiting-for-precondition branches (cluster absent, backups disabled) that
// share the same shape.
func (r *BackupJobReconciler) requeueMongoDBBackupWaiting(ctx context.Context, j *backupsv1alpha1.BackupJob, reason, message string) (ctrl.Result, error) {
	apimeta.SetStatusCondition(&j.Status.Conditions, metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionFalse,
		Reason:  reason,
		Message: message,
	})
	if err := r.Status().Update(ctx, j); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: psmdbPollInterval}, nil
}

// psmdbBackupPrecondition reports why a cluster is not ready to service an
// on-demand backup, or "" when it is. The operator only runs pbm agents (and
// therefore only completes PerconaServerMongoDBBackup CRs) when backups are
// enabled and the named storage is declared.
func psmdbBackupPrecondition(cluster *psmdbtypes.PerconaServerMongoDB, storageName string) string {
	if !cluster.Spec.Backup.Enabled {
		return "spec.backup.enabled is false; on-demand backups need the percona-backup-mongodb agents running"
	}
	if _, ok := cluster.Spec.Backup.Storages[storageName]; !ok {
		return fmt.Sprintf("spec.backup.storages does not declare storage %q", storageName)
	}
	return ""
}

// psmdbTargetCredentialsSecret returns the s3 credentialsSecret name the target
// cluster declares for its backup storage, so a restore authenticates with the
// TARGET's own credentials rather than the (possibly deleted) source's. It
// prefers the storage whose name the source backup used, then the chart's
// default storage, then — only when the cluster declares exactly one storage —
// that sole storage. Returns "" when nothing is resolvable, in which case the
// caller keeps the source reference. spec.backup.storages is decoded on demand
// from runtime.RawExtension (the partial type keeps only the key set for the
// precondition gate), so no upstream storage shape is mirrored here.
func psmdbTargetCredentialsSecret(cluster *psmdbtypes.PerconaServerMongoDB, preferredStorage string) string {
	storages := cluster.Spec.Backup.Storages
	if len(storages) == 0 {
		return ""
	}
	if preferredStorage != "" {
		if cred := psmdbStorageS3CredentialsSecret(storages[preferredStorage]); cred != "" {
			return cred
		}
	}
	if cred := psmdbStorageS3CredentialsSecret(storages[psmdbDefaultStorageName]); cred != "" {
		return cred
	}
	if len(storages) == 1 {
		for _, raw := range storages {
			return psmdbStorageS3CredentialsSecret(raw)
		}
	}
	return ""
}

// psmdbStorageS3CredentialsSecret extracts .s3.credentialsSecret from one
// psmdb spec.backup.storages entry. Returns "" for an empty/non-object entry,
// a non-s3 storage, or an entry that names no secret.
func psmdbStorageS3CredentialsSecret(raw runtime.RawExtension) string {
	if len(raw.Raw) == 0 {
		return ""
	}
	var st struct {
		S3 struct {
			CredentialsSecret string `json:"credentialsSecret"`
		} `json:"s3"`
	}
	if err := json.Unmarshal(raw.Raw, &st); err != nil {
		return ""
	}
	return st.S3.CredentialsSecret
}

// psmdbBackupDeadlineExceeded reports whether enough wall-clock time elapsed
// since the BackupJob started that we should give up on a stuck operator
// backup. Returns false when StartedAt is nil so the first reconcile does not
// trip the gate.
func psmdbBackupDeadlineExceeded(startedAt *metav1.Time) bool {
	if startedAt == nil {
		return false
	}
	return time.Since(startedAt.Time) > psmdbDefaultBackupDeadline
}

// ensureMongoDBBackup creates a one-shot PerconaServerMongoDBBackup CR labelled
// with the BackupJob, or returns the existing one if a previous reconcile
// already created it. Idempotency relies on the OwningJob labels.
func (r *BackupJobReconciler) ensureMongoDBBackup(ctx context.Context, j *backupsv1alpha1.BackupJob, clusterName, storageName string, rendered *strategyv1alpha1.MongoDBTemplate) (*psmdbtypes.PerconaServerMongoDBBackup, error) {
	existing, err := r.findMongoDBBackupForJob(ctx, j)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	obj := &psmdbtypes.PerconaServerMongoDBBackup{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:    j.Namespace,
			GenerateName: fmt.Sprintf("%s-", j.Name),
			Labels: map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      j.Name,
				backupsv1alpha1.OwningJobNamespaceLabel: j.Namespace,
			},
		},
		Spec: psmdbtypes.PerconaServerMongoDBBackupSpec{
			ClusterName:     clusterName,
			StorageName:     storageName,
			Type:            psmdbBackupTypeOrDefault(rendered.Type),
			CompressionType: rendered.CompressionType,
		},
	}
	if rendered.CompressionLevel != nil {
		lvl := *rendered.CompressionLevel
		obj.Spec.CompressionLevel = &lvl
	}

	if err := r.Create(ctx, obj); err != nil {
		return nil, err
	}
	return obj, nil
}

// findMongoDBBackupForJob returns the PerconaServerMongoDBBackup labelled with
// the BackupJob's OwningJob{Name,Namespace}, if any. Returns (nil, nil) when no
// match is found. See the MariaDB driver's findMariaDBBackupForJob for the
// duplicate-observation rationale (list race across replicas → pick [0]
// deterministically with a breadcrumb).
func (r *BackupJobReconciler) findMongoDBBackupForJob(ctx context.Context, j *backupsv1alpha1.BackupJob) (*psmdbtypes.PerconaServerMongoDBBackup, error) {
	list := &psmdbtypes.PerconaServerMongoDBBackupList{}
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
		getLogger(ctx).Debug("multiple PerconaServerMongoDBBackup CRs match BackupJob OwningJob labels; reusing first",
			"backupjob", j.Name, "namespace", j.Namespace, "matches", names, "picked", names[0])
	}
	return &list.Items[0], nil
}

// createMongoDBBackupArtifact materialises a Cozystack Backup resource carrying
// the metadata callers need to drive a future restore: the operator backup
// name/namespace, the S3 destination, and a snapshot of the storage descriptor
// (persisted in status.underlyingResources) so a restore can rebuild
// backupSource even once the operator-side Backup CR is reaped.
func (r *BackupJobReconciler) createMongoDBBackupArtifact(
	ctx context.Context,
	j *backupsv1alpha1.BackupJob,
	resolved *ResolvedBackupConfig,
	mdbBackup *psmdbtypes.PerconaServerMongoDBBackup,
	rendered *strategyv1alpha1.MongoDBTemplate,
	storageName string,
) (*backupsv1alpha1.Backup, error) {
	takenAt := metav1.Now()
	if mdbBackup.Status.Completed != nil && !mdbBackup.Status.Completed.IsZero() {
		takenAt = *mdbBackup.Status.Completed
	}

	driverMD := map[string]string{
		psmdbBackupNameKey:      mdbBackup.Name,
		psmdbBackupNamespaceKey: mdbBackup.Namespace,
	}
	if mdbBackup.Status.Destination != "" {
		driverMD[psmdbDestinationKey] = mdbBackup.Status.Destination
	}

	underlyingResources, err := marshalMongoDBBackupSnapshot(mdbBackup, rendered, storageName, resolved.Parameters)
	if err != nil {
		return nil, fmt.Errorf("encode source snapshot for Backup.status.underlyingResources: %w", err)
	}

	status := backupsv1alpha1.BackupStatus{
		Phase:               backupsv1alpha1.BackupPhaseReady,
		UnderlyingResources: underlyingResources,
	}
	if mdbBackup.Status.Destination != "" {
		status.Artifact = &backupsv1alpha1.BackupArtifact{URI: mdbBackup.Status.Destination}
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

// ---------------------------------------------------------------------------
// RestoreJob path
// ---------------------------------------------------------------------------

func (r *RestoreJobReconciler) reconcileMongoDBRestore(ctx context.Context, restoreJob *backupsv1alpha1.RestoreJob, backup *backupsv1alpha1.Backup) (ctrl.Result, error) {
	logger := getLogger(ctx)
	logger.Debug("reconciling MongoDB restore", "restorejob", restoreJob.Name, "backup", backup.Name)

	if restoreJob.Status.Phase == backupsv1alpha1.RestoreJobPhaseSucceeded ||
		restoreJob.Status.Phase == backupsv1alpha1.RestoreJobPhaseFailed {
		return ctrl.Result{}, nil
	}

	if err := validateMongoDBApplicationRef(backup.Spec.ApplicationRef); err != nil {
		return r.markRestoreJobFailed(ctx, restoreJob, err.Error())
	}

	// Validate the resolved target shape before any apiserver call.
	target := r.resolveMongoDBRestoreTarget(restoreJob, backup)
	if target.Kind != mongodbAppKind {
		return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf(
			"target applicationRef.kind=%q is not supported by the MongoDB driver", target.Kind))
	}
	if target.APIGroup != "" && target.APIGroup != mongodbapp.GroupName {
		return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf(
			"target applicationRef.apiGroup=%q is not supported by the MongoDB driver", target.APIGroup))
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
			return ctrl.Result{RequeueAfter: psmdbPollInterval}, nil
		}
	}

	// parseMongoDBRestoreOptions is intentionally permissive (event + log,
	// proceed with defaults) for a malformed blob, but a malformed recoveryTime
	// is load-bearing — restoring to the wrong point silently would be worse
	// than failing — so a bad recoveryTime fails terminally below.
	options, unknownKeys, err := parseMongoDBRestoreOptions(restoreJob.Spec.Options)
	if err != nil {
		// Falling back to defaults is safe for most malformed options, but not
		// when the blob carries a recoveryTime key: silently degrading a PITR
		// request to a plain snapshot restore is exactly the wrong-point restore
		// pitrSpec fails terminally to prevent. spec.options is free-form
		// runtime.RawExtension, so a non-string recoveryTime (e.g. {"recoveryTime":
		// 20260805}) passes admission yet fails the typed decode here — fail
		// terminally rather than restore to the snapshot and report Succeeded.
		if restoreOptionsCarryRecoveryTimeKey(restoreJob.Spec.Options) {
			return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf(
				"restoreJob.spec.options carries a recoveryTime but is malformed (%v); refusing to fall back to a plain snapshot restore, which would silently ignore the requested point-in-time", err))
		}
		logger.Info("malformed restoreJob.spec.options; falling back to defaults", "error", err)
		r.Recorder.Eventf(restoreJob, corev1.EventTypeWarning, "MalformedOptions",
			"spec.options is not valid JSON; falling back to defaults: %v", err)
	}
	// Warn (don't fail) on keys the MongoDB driver doesn't recognise: a typo
	// like "recoverytime" would otherwise be silently ignored and the restore
	// would run to the snapshot instead of the intended point. A Warning event
	// gives the tenant a breadcrumb without rejecting an otherwise-valid restore.
	for _, k := range unknownKeys {
		logger.Info("ignoring unknown restoreJob.spec.options key", "key", k, "known", mongodbKnownRestoreOptionKeys)
		r.Recorder.Eventf(restoreJob, corev1.EventTypeWarning, "UnknownRestoreOption",
			"spec.options.%s is not a recognised MongoDB restore option and was ignored (known: %s)",
			k, strings.Join(mongodbKnownRestoreOptionKeys, ", "))
	}
	pitr, perr := options.pitrSpec()
	if perr != nil {
		return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf("invalid restoreJob.spec.options.recoveryTime: %v", perr))
	}

	// Resolve the backup source (destination + S3 config). Prefer the live
	// operator Backup CR's status (freshest), fall back to the snapshot
	// persisted on the Cozystack Backup so a reaped operator CR still restores.
	source, err := r.resolveMongoDBBackupSource(ctx, backup)
	if err != nil {
		// A read error on the live operator Backup CR is retryable (cache not
		// yet synced, apiserver hiccup); requeue rather than fail the
		// RestoreJob, matching the backup path's handling of the symmetric
		// read error. Only a genuinely unresolvable source (no destination)
		// is terminal.
		if errors.Is(err, errTransientRestoreSource) {
			return ctrl.Result{}, err
		}
		return r.markRestoreJobFailed(ctx, restoreJob, err.Error())
	}

	// The operator Restore replays into a live cluster, so the target
	// PerconaServerMongoDB must exist (and, for the pbm agents to run the
	// restore, have backups enabled). Mirror the backup path's transient
	// handling and let the deadline guard a target that never shows up.
	targetPSMDBName := mongodbNameForApp(target.AppName)
	targetCluster := &psmdbtypes.PerconaServerMongoDB{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: target.Namespace, Name: targetPSMDBName}, targetCluster); err != nil {
		if apierrors.IsNotFound(err) {
			deadline := options.effectiveRestoreDeadline()
			if restoreJob.Status.StartedAt != nil && time.Since(restoreJob.Status.StartedAt.Time) > deadline {
				return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf(
					"target psmdb.percona.com/PerconaServerMongoDB %s/%s not found within %s (deploy the target MongoDB application with backup.enabled=true before requesting the restore; override via spec.options.restoreTimeoutSeconds)",
					target.Namespace, targetPSMDBName, deadline))
			}
			return r.requeueMongoDBRestoreWaiting(ctx, restoreJob, "TargetPerconaServerMongoDBNotReady",
				fmt.Sprintf("waiting for target psmdb.percona.com/PerconaServerMongoDB %s/%s to exist", target.Namespace, targetPSMDBName))
		}
		return ctrl.Result{}, err
	}
	if !targetCluster.Spec.Backup.Enabled {
		deadline := options.effectiveRestoreDeadline()
		if restoreJob.Status.StartedAt != nil && time.Since(restoreJob.Status.StartedAt.Time) > deadline {
			return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf(
				"target psmdb.percona.com/PerconaServerMongoDB %s/%s has spec.backup.enabled=false within %s; the percona-backup-mongodb agents must run to restore (set backup.enabled=true on the target MongoDB application)",
				target.Namespace, targetPSMDBName, deadline))
		}
		return r.requeueMongoDBRestoreWaiting(ctx, restoreJob, "TargetPerconaServerMongoDBBackupsDisabled",
			fmt.Sprintf("waiting for target psmdb.percona.com/PerconaServerMongoDB %s/%s to enable backups", target.Namespace, targetPSMDBName))
	}

	// Re-point the restore at the TARGET cluster's own S3 credentials. The
	// backupSource carries the SOURCE cluster's credentialsSecret (read back from
	// the source backup status / snapshot), which names the source release's
	// -s3-creds Secret. That Secret is deleted together with the source app —
	// exactly the disaster-recovery case backups exist for — so a restore into a
	// fresh, differently-named target must not depend on it. The restore replays
	// into the target, whose own -s3-creds Secret is guaranteed present
	// (backup.enabled was gated just above) and grants access to the backup's
	// bucket. The backupSource's bucket/prefix/endpoint still come from the
	// source (that is where the archive lives); only the credential reference is
	// swapped. Falls back to the source reference when the target declares no
	// discoverable s3 credentials (in-place restore into the still-present source
	// resolves to the same Secret, so this is a no-op there).
	if source.S3 != nil {
		if cred := psmdbTargetCredentialsSecret(targetCluster, source.StorageName); cred != "" && cred != source.S3.CredentialsSecret {
			logger.Debug("re-pointing MongoDB restore credentials at target cluster storage",
				"restorejob", restoreJob.Name, "from", source.S3.CredentialsSecret, "to", cred)
			source.S3.CredentialsSecret = cred
		}
	}

	mdbRestore, err := r.ensureMongoDBRestore(ctx, restoreJob, targetPSMDBName, source, pitr)
	if err != nil {
		return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf("failed to ensure psmdb.percona.com/PerconaServerMongoDBRestore: %v", err))
	}

	switch state := mdbRestore.Status.State; {
	case state == psmdbtypes.StateReady:
		now := metav1.Now()
		restoreJob.Status.CompletedAt = &now
		restoreJob.Status.Phase = backupsv1alpha1.RestoreJobPhaseSucceeded
		apimeta.SetStatusCondition(&restoreJob.Status.Conditions, metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionTrue,
			Reason:  "RestoreCompleted",
			Message: fmt.Sprintf("psmdb.percona.com/PerconaServerMongoDBRestore %s/%s completed", mdbRestore.Namespace, mdbRestore.Name),
		})
		if err := r.Status().Update(ctx, restoreJob); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil

	case state == psmdbtypes.StateError || state == psmdbtypes.StateRejected:
		message := mdbRestore.Status.Error
		if message == "" {
			message = fmt.Sprintf("psmdb.percona.com/PerconaServerMongoDBRestore %s/%s reported state=%s", mdbRestore.Namespace, mdbRestore.Name, state)
		}
		return r.markRestoreJobFailed(ctx, restoreJob, message)

	default:
		deadline := options.effectiveRestoreDeadline()
		if restoreJob.Status.StartedAt != nil && time.Since(restoreJob.Status.StartedAt.Time) > deadline {
			detail := "no state observed"
			if state != "" {
				detail = fmt.Sprintf("state=%s", state)
			}
			return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf(
				"psmdb.percona.com/PerconaServerMongoDBRestore did not complete within %s (%s; override via spec.options.restoreTimeoutSeconds)",
				deadline, detail))
		}
		return ctrl.Result{RequeueAfter: psmdbPollInterval}, nil
	}
}

// requeueMongoDBRestoreWaiting records a transient Ready=False condition on the
// RestoreJob and requeues.
func (r *RestoreJobReconciler) requeueMongoDBRestoreWaiting(ctx context.Context, rj *backupsv1alpha1.RestoreJob, reason, message string) (ctrl.Result, error) {
	apimeta.SetStatusCondition(&rj.Status.Conditions, metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionFalse,
		Reason:  reason,
		Message: message,
	})
	if err := r.Status().Update(ctx, rj); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: psmdbPollInterval}, nil
}

// errTransientRestoreSource marks a backup-source resolution failure that a
// retry can clear — an API read error (cache not yet synced, apiserver hiccup)
// on the live operator Backup CR — as opposed to a terminal one (no restorable
// destination). The RestoreJob reconciler requeues on it rather than failing
// the job, mirroring the backup path, which requeues the symmetric live-CR read
// error (return ctrl.Result{}, err) instead of failing the BackupJob.
var errTransientRestoreSource = errors.New("transient backup-source resolution error")

// resolveMongoDBBackupSource builds the restore backupSource from the freshest
// available source: the live operator Backup CR's status when it still exists,
// otherwise the snapshot persisted on the Cozystack Backup at backup time.
// Fails terminally when neither yields a destination — without one there is
// nothing to restore from. A transient read error on the live Backup CR is
// wrapped in errTransientRestoreSource so the caller can requeue instead.
func (r *RestoreJobReconciler) resolveMongoDBBackupSource(ctx context.Context, backup *backupsv1alpha1.Backup) (*psmdbtypes.BackupSource, error) {
	// Live operator Backup CR (same namespace as the Cozystack Backup).
	sourceBackupName := backup.Spec.DriverMetadata[psmdbBackupNameKey]
	if sourceBackupName != "" {
		live := &psmdbtypes.PerconaServerMongoDBBackup{}
		err := r.Get(ctx, types.NamespacedName{Namespace: backup.Namespace, Name: sourceBackupName}, live)
		if err == nil {
			if live.Status.Destination != "" {
				return &psmdbtypes.BackupSource{
					Type:        psmdbBackupTypeOrDefault(live.Status.Type),
					Destination: live.Status.Destination,
					StorageName: live.Status.StorageName,
					S3:          live.Status.S3.DeepCopy(),
				}, nil
			}
		} else if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: get source PerconaServerMongoDBBackup %s/%s: %v", errTransientRestoreSource, backup.Namespace, sourceBackupName, err)
		}
	}

	// Snapshot fallback.
	snap, err := unmarshalMongoDBBackupSnapshot(backup.Status.UnderlyingResources)
	if err != nil {
		return nil, fmt.Errorf("decode Backup snapshot: %v", err)
	}
	destination := backup.Spec.DriverMetadata[psmdbDestinationKey]
	if snap != nil && snap.Destination != "" {
		destination = snap.Destination
	}
	if destination == "" {
		return nil, fmt.Errorf(
			"Backup has no restorable destination (the operator PerconaServerMongoDBBackup was reaped and no snapshot destination is persisted); re-run the BackupJob")
	}
	src := &psmdbtypes.BackupSource{
		Type:        psmdbtypes.BackupTypeLogical,
		Destination: destination,
	}
	if snap != nil {
		if snap.Type != "" {
			src.Type = snap.Type
		}
		src.StorageName = snap.StorageName
		src.S3 = snap.S3.DeepCopy()
	}
	return src, nil
}

// ensureMongoDBRestore creates a PerconaServerMongoDBRestore CR labelled with
// the RestoreJob, or returns the existing one if a previous reconcile already
// created it.
func (r *RestoreJobReconciler) ensureMongoDBRestore(ctx context.Context, rj *backupsv1alpha1.RestoreJob, targetClusterName string, source *psmdbtypes.BackupSource, pitr *psmdbtypes.PITRSpec) (*psmdbtypes.PerconaServerMongoDBRestore, error) {
	list := &psmdbtypes.PerconaServerMongoDBRestoreList{}
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

	obj := &psmdbtypes.PerconaServerMongoDBRestore{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:    rj.Namespace,
			GenerateName: fmt.Sprintf("%s-", rj.Name),
			Labels: map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      rj.Name,
				backupsv1alpha1.OwningJobNamespaceLabel: rj.Namespace,
			},
		},
		Spec: psmdbtypes.PerconaServerMongoDBRestoreSpec{
			ClusterName:  targetClusterName,
			BackupSource: source,
			PITR:         pitr,
		},
	}
	if err := r.Create(ctx, obj); err != nil {
		return nil, err
	}
	return obj, nil
}

// mongodbRestoreTarget captures the resolved target for a MongoDB restore.
// In-place and to-copy are treated identically: both create a
// PerconaServerMongoDBRestore CR pointing (via clusterName) at the named target
// cluster. Callers infer the mode from AppName != backup.Spec.ApplicationRef.Name.
type mongodbRestoreTarget struct {
	Namespace string
	AppName   string
	Kind      string
	APIGroup  string
}

func (r *RestoreJobReconciler) resolveMongoDBRestoreTarget(restoreJob *backupsv1alpha1.RestoreJob, backup *backupsv1alpha1.Backup) mongodbRestoreTarget {
	t := mongodbRestoreTarget{
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

// mongodbBackupSnapshot is the MongoDB-specific payload persisted in
// Backup.status.underlyingResources at backup time. It carries the S3
// destination and storage descriptor read back from the operator Backup status
// so the restore path can rebuild backupSource even after the operator-side
// PerconaServerMongoDBBackup CR is reaped.
//
// SECRET-HANDLING CONTRACT (plaintext-readable to anyone with read access to
// backups.cozystack.io/Backups):
//
//   - S3 carries only a Secret NAME (CredentialsSecret) plus non-secret
//     coordinates (bucket, endpoint, prefix, region) — the psmdb storage shape
//     has no field for a raw credential, so the snapshot can never contain an
//     access key. The operator dereferences CredentialsSecret at restore time
//     against the apiserver's Secret cache in the restore namespace.
//   - Parameters lands here verbatim from BackupClassStrategy.parameters.
//     Callers MUST NOT put credentials in Parameters.
type mongodbBackupSnapshot struct {
	Kind        string                      `json:"kind"`
	APIVersion  string                      `json:"apiVersion"`
	Destination string                      `json:"destination,omitempty"`
	Type        string                      `json:"type,omitempty"`
	StorageName string                      `json:"storageName,omitempty"`
	S3          *psmdbtypes.BackupStorageS3 `json:"s3,omitempty"`
	Parameters  map[string]string           `json:"parameters,omitempty"`
}

func marshalMongoDBBackupSnapshot(mdbBackup *psmdbtypes.PerconaServerMongoDBBackup, rendered *strategyv1alpha1.MongoDBTemplate, storageName string, parameters map[string]string) (*runtime.RawExtension, error) {
	snap := mongodbBackupSnapshot{
		Kind:        psmdbBackupSnapshotKind,
		APIVersion:  psmdbBackupSnapshotAPIVersion,
		Destination: mdbBackup.Status.Destination,
		Type:        psmdbBackupTypeOrDefault(rendered.Type),
		StorageName: storageName,
		S3:          mdbBackup.Status.S3.DeepCopy(),
		Parameters:  parameters,
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		return nil, err
	}
	return &runtime.RawExtension{Raw: raw}, nil
}

func unmarshalMongoDBBackupSnapshot(raw *runtime.RawExtension) (*mongodbBackupSnapshot, error) {
	if raw == nil || len(raw.Raw) == 0 {
		return nil, nil
	}
	var snap mongodbBackupSnapshot
	if err := json.Unmarshal(raw.Raw, &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// renderMongoDBTemplate templates the strategy template against a context
// containing the live application object and the BackupClass parameters.
func renderMongoDBTemplate(t strategyv1alpha1.MongoDBTemplate, app *mongodbapp.MongoDB, parameters map[string]string) (*strategyv1alpha1.MongoDBTemplate, error) {
	appAsMap, err := toJSONMapMongoDB(app)
	if err != nil {
		return nil, fmt.Errorf("encode application for templating: %w", err)
	}
	templateContext := map[string]interface{}{
		"Application": appAsMap,
		"Parameters":  parameters,
	}
	return template.Template(&t, templateContext)
}

// toJSONMapMongoDB converts a typed object to a generic map via JSON tags so
// user-authored go-templates address fields by their JSON names (e.g.
// .Application.metadata.name). Mirrors the MariaDB controller's helper; scoped
// here to avoid cross-strategy import.
func toJSONMapMongoDB(obj interface{}) (map[string]interface{}, error) {
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

// getMongoDBApp fetches the apps.cozystack.io MongoDB instance via the shared
// typed client. The MongoDB scheme is registered in main.go so the
// controller-runtime cache serves it directly.
func (r *BackupJobReconciler) getMongoDBApp(ctx context.Context, namespace, name string) (*mongodbapp.MongoDB, error) {
	app := &mongodbapp.MongoDB{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, app); err != nil {
		return nil, err
	}
	return app, nil
}

// MongoDBRestoreOptions is the typed shape of RestoreJob.Spec.Options for the
// MongoDB driver. The option surface is kept uniform with the CNPG strategy
// (CNPGRestoreOptions): point-in-time recovery is expressed as a single
// RFC3339 recoveryTime string, and restoreTimeoutSeconds caps the wait.
type MongoDBRestoreOptions struct {
	// RecoveryTime is an optional RFC3339 timestamp for point-in-time recovery,
	// mirroring the CNPG strategy's spec.options.recoveryTime. When set, the
	// driver maps it onto the psmdb Restore's pitr {type: date, date} (converted
	// to the operator's "YYYY-MM-DD HH:MM:SS" UTC format). Empty restores the
	// backup snapshot as taken (no oplog replay) — note this differs from CNPG,
	// whose empty recoveryTime replays WAL to the latest archived point; a psmdb
	// logical backup is already a consistent snapshot, so "restore this backup"
	// is the safe, always-available default here.
	// +optional
	RecoveryTime string `json:"recoveryTime,omitempty"`

	// RestoreTimeoutSeconds caps the time the driver waits for the
	// PerconaServerMongoDBRestore to terminate before it marks the RestoreJob
	// Failed. Zero or unset falls back to psmdbDefaultRestoreDeadline. Same knob
	// and semantics as the CNPG strategy.
	// +optional
	RestoreTimeoutSeconds int64 `json:"restoreTimeoutSeconds,omitempty"`
}

// mongodbKnownRestoreOptionKeys enumerates the JSON keys parseMongoDBRestoreOptions
// recognises. Any other key in spec.options is surfaced as a Warning event
// (see reconcileMongoDBRestore) so a typo like "recoverytime" is not silently
// dropped. Keep in sync with the MongoDBRestoreOptions json tags.
var mongodbKnownRestoreOptionKeys = []string{"recoveryTime", "restoreTimeoutSeconds"}

// parseMongoDBRestoreOptions decodes RestoreJob.Spec.Options into the typed
// shape and additionally reports any keys the driver does not recognise. The
// typed decode stays permissive (unknown keys are ignored, not rejected), so
// the returned options are always usable; the unknownKeys slice lets the caller
// warn without failing. A malformed blob returns the zero options + a decode
// error the caller surfaces as an event. Mirrors parseCNPGRestoreOptions, plus
// the unknown-key detection.
func parseMongoDBRestoreOptions(opts *runtime.RawExtension) (MongoDBRestoreOptions, []string, error) {
	var out MongoDBRestoreOptions
	if opts == nil || len(opts.Raw) == 0 {
		return out, nil, nil
	}
	if err := json.Unmarshal(opts.Raw, &out); err != nil {
		return MongoDBRestoreOptions{}, nil, fmt.Errorf("decode restoreJob.spec.options: %w", err)
	}
	// Second pass into a generic map to diff keys against the known set. Done
	// separately from the typed decode so the options stay usable even when an
	// unknown key is present.
	unknown := unknownJSONKeys(opts.Raw, mongodbKnownRestoreOptionKeys)
	return out, unknown, nil
}

// restoreOptionsCarryRecoveryTimeKey reports whether spec.options parses as a
// JSON object with a "recoveryTime" key present, regardless of that value's
// type. The caller uses it to decide whether a typed-decode failure is
// load-bearing (a malformed recoveryTime must fail terminally) or benign (a
// malformed unrelated field can fall back to defaults). A blob that is not a
// JSON object at all returns false — there is no recoveryTime to honour, so the
// permissive fall-back applies.
func restoreOptionsCarryRecoveryTimeKey(opts *runtime.RawExtension) bool {
	if opts == nil || len(opts.Raw) == 0 {
		return false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(opts.Raw, &obj); err != nil {
		return false
	}
	_, ok := obj["recoveryTime"]
	return ok
}

// unknownJSONKeys returns the top-level object keys in raw that are not in
// known. Returns nil when raw is not a JSON object (a malformed blob is handled
// by the typed decode's error path, not here).
func unknownJSONKeys(raw []byte, known []string) []string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	knownSet := make(map[string]struct{}, len(known))
	for _, k := range known {
		knownSet[k] = struct{}{}
	}
	var out []string
	for k := range obj {
		if _, ok := knownSet[k]; !ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func (o MongoDBRestoreOptions) effectiveRestoreDeadline() time.Duration {
	if o.RestoreTimeoutSeconds > 0 {
		return time.Duration(o.RestoreTimeoutSeconds) * time.Second
	}
	return psmdbDefaultRestoreDeadline
}

// pitrSpec translates the RFC3339 recoveryTime into the operator restore's
// pitr {type: date, date} shape, converting to psmdb's "YYYY-MM-DD HH:MM:SS"
// UTC format (the operator CRD's XValidation regex). Returns (nil, nil) when no
// recoveryTime is requested (plain snapshot restore). A recoveryTime that does
// not parse as RFC3339 is a terminal error — a silent wrong-point restore is
// worse than failing.
func (o MongoDBRestoreOptions) pitrSpec() (*psmdbtypes.PITRSpec, error) {
	if o.RecoveryTime == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, o.RecoveryTime)
	if err != nil {
		return nil, fmt.Errorf("recoveryTime %q is not a valid RFC3339 timestamp: %w", o.RecoveryTime, err)
	}
	return &psmdbtypes.PITRSpec{Type: "date", Date: t.UTC().Format("2006-01-02 15:04:05")}, nil
}
