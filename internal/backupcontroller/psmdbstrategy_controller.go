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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

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

	// psmdbDefaultCredentialsSecret is the Secret the useSystemBucket flow
	// authenticates against: the BackupJob reconciler projects it into the
	// namespace before dispatch. Both the storage injection and the snapshot
	// fallback default to it when the strategy names none.
	psmdbDefaultCredentialsSecret = "cozy-backups-creds"

	// psmdbFieldManager is the server-side-apply field owner for the driver's
	// storage injection on the useSystemBucket flow. A dedicated manager (with
	// ForceOwnership) keeps the injected spec.backup.storages entry from being
	// reverted by the app's Helm/Flux field manager on re-render.
	psmdbFieldManager = "cozystack-psmdb-backup-driver"

	// Driver-metadata keys persisted on Cozystack Backup artifacts. The
	// restore path reads Destination (+ the S3 snapshot) to drive the operator
	// Restore CR's backupSource, so a restore-to-differently-named instance
	// works without the source PerconaServerMongoDBBackup CR still existing.
	psmdbBackupNameKey      = "psmdb.percona.com/backup-name"
	psmdbBackupNamespaceKey = "psmdb.percona.com/backup-namespace"
	psmdbDestinationKey     = "psmdb.percona.com/destination"

	// psmdbDeleteBackupFinalizer is the psmdb-operator finalizer that removes the
	// pbm archive from object storage when its PerconaServerMongoDBBackup CR is
	// deleted. The driver stamps it onto the CRs it mints on the useSystemBucket
	// flow so that pruning a Cozystack Backup (Plan retention or manual delete)
	// also frees the object it wrote to the shared bucket. Without it a
	// retention-pruned Plan would grow cozy-backups unboundedly for every tenant
	// at once, since the system-bucket flow ships no psmdb task retention (the
	// chart's tasks[].keep is gated out) to prune it.
	psmdbDeleteBackupFinalizer = "percona.com/delete-backup"

	// psmdbSkipArtifactCleanupAnnotation, on a Cozystack Backup, releases it from
	// Terminating without waiting for the operator to prune the archive — an
	// escape hatch for a wedged delete (operator down, storage unreachable). The
	// object is then left in the bucket. Mirrors the Redis/Rabbitmq drivers.
	psmdbSkipArtifactCleanupAnnotation = "backups.cozystack.io/skip-artifact-cleanup"

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

// psmdbBackupGVR addresses PerconaServerMongoDBBackup through the dynamic client
// for an uncached, live status read (see psmdbBackupLiveState).
var psmdbBackupGVR = schema.GroupVersionResource{
	Group:    psmdbtypes.GroupVersion.Group,
	Version:  psmdbtypes.GroupVersion.Version,
	Resource: "perconaservermongodbbackups",
}

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

	useSystemBucket := app.Spec.Backup.UseSystemBucket

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
	// System-bucket flow: the app chart leaves spec.backup.storages unset (it
	// cannot know the platform bucket/endpoint at render time), so SSA-inject
	// the storage from the strategy's coordinates before the precondition looks
	// for it. Owned by a dedicated field manager with ForceOwnership so a Flux
	// re-render of the app never reverts it (mirrors the CNPG driver's
	// spec.plugins patch). Keyed off the app's useSystemBucket flag, not off
	// whatever storage is on the live cluster: when the flag is set the apply
	// runs on every BackupJob so a later change to the strategy coordinates
	// (endpoint, region, bucket re-provision) catches up rather than being
	// frozen at the first backup; when it is unset the driver never touches the
	// cluster, so a legacy app that ships its own static storage is left alone.
	// The path prefix is deterministic (<namespace>/<application>), so
	// re-applying the whole entry never splits the archive the way CNPG's
	// serverName would. Gated additionally on backups being enabled: a cluster
	// that can never service a backup should not be mutated just to fail the
	// precondition below on the enabled check anyway.
	if cluster.Spec.Backup.Enabled && shouldInjectMongoDBSystemStorage(useSystemBucket, rendered) {
		_, storageDeclared := cluster.Spec.Backup.Storages[storageName]
		injected, err := r.applyMongoDBSystemStorage(ctx, j.Namespace, psmdbName, storageName, rendered.S3)
		if err != nil {
			// A server-side apply can fail transiently — an apiserver hiccup, or a
			// conflict while the psmdb operator writes the same cluster — so requeue
			// with backoff rather than failing the BackupJob outright. The deadline
			// still turns a permanently-failing apply terminal, matching how the
			// cluster/app read errors above are handled.
			if psmdbBackupDeadlineExceeded(j.Status.StartedAt) {
				return r.markBackupJobFailed(ctx, j, fmt.Sprintf(
					"failed to inject system-bucket storage onto psmdb.percona.com/PerconaServerMongoDB %s/%s within %s: %v",
					j.Namespace, psmdbName, psmdbDefaultBackupDeadline, err))
			}
			return ctrl.Result{}, err
		}
		cluster = injected
		if !storageDeclared {
			// First injection for this app: the storage is now applied, but the
			// psmdb operator resolves spec.backup.storages from a CACHED cluster
			// read when it services the PerconaServerMongoDBBackup, and its cache
			// may not have observed the apply yet. A miss there latches the CR at
			// State=error with nothing to re-drive it (the operator returns for a
			// terminal-state CR and watches only the Backup CR and Pods), and the
			// driver reads that as a terminal failure. So requeue instead of
			// minting the CR in the same pass: on the next reconcile this driver's
			// own cache reflects the storage (storageDeclared is true), which is a
			// good proxy that the operator's does too, and only then do we mint.
			// Later BackupJobs find the storage already declared and skip through.
			//
			// Bounded and observable like the other waits: consult the deadline so
			// a cluster whose cache never reflects the storage cannot poll forever,
			// and write Ready=False so the wait is named in kubectl describe.
			if psmdbBackupDeadlineExceeded(j.Status.StartedAt) {
				return r.markBackupJobFailed(ctx, j, fmt.Sprintf(
					"psmdb.percona.com/PerconaServerMongoDB %s/%s did not reflect the injected storage %q within %s",
					j.Namespace, psmdbName, storageName, psmdbDefaultBackupDeadline))
			}
			return r.requeueMongoDBBackupWaiting(ctx, j, "PerconaServerMongoDBStorageInjected",
				fmt.Sprintf("injected storage %q; waiting for the cluster to reflect it before starting the backup", storageName))
		}
	}
	if msg := psmdbBackupPrecondition(cluster, storageName); msg != "" {
		if psmdbBackupDeadlineExceeded(j.Status.StartedAt) {
			return r.markBackupJobFailed(ctx, j, fmt.Sprintf(
				"psmdb.percona.com/PerconaServerMongoDB %s/%s not ready for backups within %s: %s (set backup.enabled=true on the MongoDB application)",
				j.Namespace, psmdbName, psmdbDefaultBackupDeadline, msg))
		}
		return r.requeueMongoDBBackupWaiting(ctx, j, "PerconaServerMongoDBBackupsDisabled", msg)
	}

	// Opting out (useSystemBucket true→false) stops the injection but leaves the
	// driver's storage entry on the live cluster: the chart never owned it and
	// does not prune it, so it stays until removed by hand (backup-classes.md). A
	// BackupJob started in that state passes the presence-only precondition and
	// would mint a CR without the delete-backup finalizer, landing the dump in
	// the platform bucket as an object nothing owns or prunes. Refuse to mint.
	// Two points of shape: this is a mint-time gate, so it applies only while
	// the job has no CR — a CR minted while the flag was true is handled by the
	// state switch below, which reads the flow off the CR's own finalizer, so a
	// flag flipped mid-dump cannot fail a streaming backup here; and the injected
	// entry is identified by its credentialsSecret, the one field on it the
	// driver chose — bucket names collide with an admin's external bucket and
	// drift when the platform bucket is re-provisioned.
	existing, err := r.findMongoDBBackupForJob(ctx, j)
	if err != nil {
		return ctrl.Result{}, err
	}
	if existing == nil && !useSystemBucket && rendered.S3 != nil {
		if _, cred := psmdbStorageS3(cluster.Spec.Backup.Storages[storageName]); cred != "" && cred == psmdbInjectedCredentialsSecret(rendered.S3) {
			if psmdbBackupDeadlineExceeded(j.Status.StartedAt) {
				return r.markBackupJobFailed(ctx, j, fmt.Sprintf(
					"psmdb.percona.com/PerconaServerMongoDB %s/%s storage %q is still the entry injected for the system bucket (credentialsSecret %q) while backup.useSystemBucket=false, %s after the job started; delete that storage entry from the PerconaServerMongoDB by hand or set backup.useSystemBucket=true again (see docs/operations/backup-classes.md)",
					j.Namespace, psmdbName, storageName, cred, psmdbDefaultBackupDeadline))
			}
			return r.requeueMongoDBBackupWaiting(ctx, j, "PerconaServerMongoDBStorageStale",
				fmt.Sprintf("storage %q is still the entry injected for the system bucket (credentialsSecret %q) while backup.useSystemBucket=false; delete it from the PerconaServerMongoDB by hand or set backup.useSystemBucket=true again", storageName, cred))
		}
	}

	mdbBackup, err := r.ensureMongoDBBackup(ctx, j, psmdbName, storageName, rendered, useSystemBucket)
	if err != nil {
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf("failed to ensure psmdb.percona.com/PerconaServerMongoDBBackup: %v", err))
	}
	// The flow a CR belongs to is fixed at mint time by its finalizer, not by the
	// app flag a tenant can flip while the dump streams: a CR carrying
	// delete-backup writes into the platform bucket whatever the flag reads now,
	// and is handled as system-bucket for the rest of its life.
	flowSystemBucket := controllerutil.ContainsFinalizer(mdbBackup, psmdbDeleteBackupFinalizer)

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
		artifact, err := r.createMongoDBBackupArtifact(ctx, j, resolved, mdbBackup, rendered, storageName, useSystemBucket)
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
		message := mdbBackup.Status.Error
		// The residual half of the injection race: on the useSystemBucket flow the
		// operator can latch state=error because it resolved the injected storage
		// from a cluster cache that had not yet observed the apply. That is the
		// driver's own race, not the tenant's failure, so retry it (bounded by the
		// deadline) — delete the errored CR so a fresh one resolves against a
		// caught-up cache — rather than failing the BackupJob terminally.
		if flowSystemBucket && psmdbErrorIsUnresolvedStorage(message, storageName) && !psmdbBackupDeadlineExceeded(j.Status.StartedAt) {
			if mdbBackup.DeletionTimestamp.IsZero() {
				if derr := r.Delete(ctx, mdbBackup); derr != nil && !apierrors.IsNotFound(derr) {
					return ctrl.Result{}, derr
				}
			}
			return r.requeueMongoDBBackupWaiting(ctx, j, "PerconaServerMongoDBStorageRace",
				fmt.Sprintf("operator has not observed the injected storage %q yet; retrying the backup", storageName))
		}
		// Terminal failure. The operator sets state=error/rejected once the pbm
		// backup fails or is refused, so fail the BackupJob immediately rather
		// than waiting for the driver-side deadline.
		if message == "" {
			message = fmt.Sprintf("psmdb.percona.com PerconaServerMongoDBBackup reported state=%s", state)
		}
		return r.markBackupJobFailed(ctx, j, message)

	default:
		// Still in progress ("", requested, running, waiting).
		if state == psmdbtypes.StateRunning {
			if !psmdbBackupTimedOut(state, j.Status.StartedAt, flowSystemBucket) {
				// The wait is long by nature (a real dataset routinely outruns any
				// wall-clock window) and the CR carries nothing to tell a slow dump
				// from a wedged one: the pinned operator persists status only when the
				// state string or error changes, so status.lastTransition freezes at
				// the instant the dump entered `running`. Name the wait on the
				// BackupJob instead of judging it by a timer — Ready=False with the
				// start time, so `kubectl describe backupjob` shows what it is waiting
				// on — and let the operator move the CR to ready/error.
				since := "an unknown start time"
				if j.Status.StartedAt != nil {
					since = j.Status.StartedAt.UTC().Format(time.RFC3339)
				}
				return r.requeueMongoDBBackupWaiting(ctx, j, "PerconaServerMongoDBBackupRunning",
					fmt.Sprintf("psmdb.percona.com PerconaServerMongoDBBackup %s has been streaming since %s; a running dump is left to finish", mdbBackup.Name, since))
			}
			// Legacy flow past the deadline: the archive is in the tenant's own bucket
			// and is theirs to reclaim — no platform retention covers it (the
			// operator prunes only its own scheduled backups, selected by ancestor
			// label, which a driver-minted CR does not carry), so failing the job
			// strands nothing the platform owns. The operator CR is left alone, as
			// it was before this driver existed.
			return r.markBackupJobFailed(ctx, j, fmt.Sprintf(
				"psmdb.percona.com PerconaServerMongoDBBackup did not complete within %s (state=running)", psmdbDefaultBackupDeadline))
		}
		// Not-yet-started states ("", requested, waiting). A wall-clock deadline
		// fails a backup the operator never starts, so it cannot pin the BackupJob
		// Running forever.
		if !psmdbBackupTimedOut(state, j.Status.StartedAt, flowSystemBucket) {
			return ctrl.Result{RequeueAfter: psmdbPollInterval}, nil
		}
		detail := "no state observed"
		if state != "" {
			detail = fmt.Sprintf("state=%s", state)
		}
		if !flowSystemBucket {
			// Legacy flow: the CR and whatever it may still write are the tenant's,
			// in the tenant's bucket. Fail the job and leave the CR, as before this
			// driver existed; only a system-bucket CR is ever cancelled below.
			return r.markBackupJobFailed(ctx, j, fmt.Sprintf(
				"psmdb.percona.com PerconaServerMongoDBBackup did not start within %s (%s)", psmdbDefaultBackupDeadline, detail))
		}
		// The deadline hit for a system-bucket backup that has not started. A late
		// run would land in the shared bucket with no Backup object to represent
		// it, so the CR is cancelled — but only on a positive answer that nothing
		// has been handed to pbm yet. mdbBackup came from the cache, so re-read it
		// live first: the CR carries delete-backup and the operator prunes storage
		// on that finalizer once the CR is ready, so deleting anything that has
		// moved on risks taking a running dump's partial or a completed archive
		// with it. A read error ("" is indistinguishable, so the helper returns an
		// error) defers rather than deletes blind.
		liveState, lerr := r.psmdbBackupLiveState(ctx, mdbBackup.Namespace, mdbBackup.Name)
		if lerr != nil {
			getLogger(ctx).Debug("could not re-read live backup state before cancelling; deferring rather than deleting blind",
				"backupjob", j.Name, "sourceBackup", mdbBackup.Name, "error", lerr)
			return ctrl.Result{RequeueAfter: psmdbPollInterval}, nil
		}
		switch liveState {
		case "", psmdbtypes.StateWaiting:
			// Not handed to pbm: "" means the operator has not observed the CR,
			// waiting means it is queued behind another backup. At v1.22.0 the
			// operator returns for a CR with a deletion timestamp before it
			// dispatches, so the delete below prevents the late run.
		case psmdbtypes.StateRequested:
			// Dispatched: at v1.22.0 `requested` is set right after the command is
			// sent to pbm, so pbm already holds it and deleting the CR cannot recall
			// it — a late dump would then land with no CR to own it. Leave the CR
			// (its finalizer stays the handle to whatever pbm writes) and fail.
			return r.markBackupJobFailed(ctx, j, fmt.Sprintf(
				"psmdb.percona.com PerconaServerMongoDBBackup did not start within %s (state=requested: already dispatched to pbm, so the operator backup is left in place)", psmdbDefaultBackupDeadline))
		default:
			// running/ready/error/rejected — the CR moved on; let the state switch
			// handle it on the next reconcile instead of deleting it here.
			return ctrl.Result{RequeueAfter: psmdbPollInterval}, nil
		}
		// Best-effort: a delete failure must not stop the job from failing (a
		// leftover CR is the pre-existing behaviour, not a regression). The terminal
		// Phase=Failed makes reconcileMongoDB return early next time, so
		// ensureMongoDBBackup never re-creates it.
		if mdbBackup.DeletionTimestamp.IsZero() {
			if derr := r.Delete(ctx, mdbBackup); derr != nil && !apierrors.IsNotFound(derr) {
				getLogger(ctx).Debug("could not cancel the timed-out operator backup before failing",
					"backupjob", j.Name, "sourceBackup", mdbBackup.Name, "error", derr)
			}
		}
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf(
			"psmdb.percona.com PerconaServerMongoDBBackup did not start within %s (%s)", psmdbDefaultBackupDeadline, detail))
	}
}

// requeueMongoDBBackupWaiting records a transient Ready=False condition on the
// BackupJob and requeues. Factored out because the backup path has two
// waiting-for-precondition branches (cluster absent, backups disabled) that
// share the same shape.
func (r *BackupJobReconciler) requeueMongoDBBackupWaiting(ctx context.Context, j *backupsv1alpha1.BackupJob, reason, message string) (ctrl.Result, error) {
	// Write only when the condition actually changes: a running dump polls
	// through here every few seconds for as long as it streams, and rewriting an
	// identical condition each time would be a status write per poll for hours.
	changed := apimeta.SetStatusCondition(&j.Status.Conditions, metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionFalse,
		Reason:  reason,
		Message: message,
	})
	if changed {
		if err := r.Status().Update(ctx, j); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: psmdbPollInterval}, nil
}

// psmdbBackupPrecondition reports why a cluster is not ready to service an
// on-demand backup, or "" when it is. Two independent conditions must hold, for
// two different reasons: the operator runs the pbm agents only when
// spec.backup.enabled=true, and a PerconaServerMongoDBBackup that names a
// storage absent from spec.backup.storages fails resolution rather than
// running. (Agent startup itself is gated on Enabled alone; the storage is
// required by the backup CR, not by the agents.)
func psmdbBackupPrecondition(cluster *psmdbtypes.PerconaServerMongoDB, storageName string) string {
	if !cluster.Spec.Backup.Enabled {
		return "spec.backup.enabled is false; on-demand backups need the percona-backup-mongodb agents running"
	}
	if _, ok := cluster.Spec.Backup.Storages[storageName]; !ok {
		return fmt.Sprintf("spec.backup.storages does not declare storage %q", storageName)
	}
	return ""
}

// psmdbTargetCredentialsSecret returns the s3 credentialsSecret the target
// cluster declares for a storage on the SAME bucket (wantBucket) as the source
// archive, so a restore authenticates with the TARGET's own credentials rather
// than the (possibly deleted) source's — but only when the target actually has
// access to that bucket. Requiring the bucket to match keeps a legacy→legacy
// restore on a shared bucket working while refusing to hand a cross-flow restore
// a credential for the wrong bucket (a system-bucket target's cozy-backups-creds
// against a legacy tenant bucket, or vice versa). It prefers the storage the
// source backup named, then the chart default, then — only when the cluster
// declares exactly one storage — that sole storage. Returns "" when nothing on
// wantBucket is resolvable (including when wantBucket is empty), in which case
// the caller keeps the source reference. spec.backup.storages is decoded on
// demand from runtime.RawExtension, so no upstream storage shape is mirrored.
func psmdbTargetCredentialsSecret(cluster *psmdbtypes.PerconaServerMongoDB, preferredStorage, wantBucket string) string {
	storages := cluster.Spec.Backup.Storages
	if len(storages) == 0 || wantBucket == "" {
		return ""
	}
	credOnWantBucket := func(raw runtime.RawExtension) string {
		bucket, cred := psmdbStorageS3(raw)
		if cred != "" && bucket == wantBucket {
			return cred
		}
		return ""
	}
	if preferredStorage != "" {
		if cred := credOnWantBucket(storages[preferredStorage]); cred != "" {
			return cred
		}
	}
	if cred := credOnWantBucket(storages[psmdbDefaultStorageName]); cred != "" {
		return cred
	}
	if len(storages) == 1 {
		for _, raw := range storages {
			return credOnWantBucket(raw)
		}
	}
	return ""
}

// mongodbRestoreCredentialsSecret picks the s3 credentialsSecret a restore into
// targetCluster should authenticate with. It returns the source's own reference
// unless a safe swap applies, so the caller can compare and re-point only when
// the value actually changes. Two guards make the swap correct across the legacy
// and useSystemBucket flows:
//   - When the source already names the platform-projected cozy-backups-creds
//     (useSystemBucket), keep it: that Secret is projected into the restore
//     namespace and outlives the source app, so there is nothing to repair, and
//     repointing it at a legacy target's own creds would send a platform-bucket
//     restore at the wrong credentials.
//   - Otherwise adopt a target credential only when its storage is on the SAME
//     bucket as the archive (source.S3.Bucket): the backupSource's bucket stays
//     the source's, so a credential for a different bucket (a system-bucket
//     target's cozy-backups-creds against a legacy tenant bucket) would fail the
//     restore with AccessDenied. When no same-bucket target credential is
//     discoverable the source reference is kept (in-place restore into the
//     still-present source resolves to the same Secret, so this is a no-op).
//
// Returns "" only when the source carries no s3 reference at all.
func mongodbRestoreCredentialsSecret(source *psmdbtypes.BackupSource, targetCluster *psmdbtypes.PerconaServerMongoDB) string {
	if source == nil || source.S3 == nil {
		return ""
	}
	current := source.S3.CredentialsSecret
	if current == psmdbDefaultCredentialsSecret {
		return current
	}
	if cred := psmdbTargetCredentialsSecret(targetCluster, source.StorageName, source.S3.Bucket); cred != "" {
		return cred
	}
	return current
}

// psmdbStorageS3 extracts .s3.bucket and .s3.credentialsSecret from one psmdb
// spec.backup.storages entry. Returns ("","") for an empty/non-object entry or
// a non-s3 storage. The bucket lets the restore path refuse to hand a restore a
// credential for a storage on a different bucket than the archive.
func psmdbStorageS3(raw runtime.RawExtension) (bucket, credentialsSecret string) {
	if len(raw.Raw) == 0 {
		return "", ""
	}
	var st struct {
		S3 struct {
			Bucket            string `json:"bucket"`
			CredentialsSecret string `json:"credentialsSecret"`
		} `json:"s3"`
	}
	if err := json.Unmarshal(raw.Raw, &st); err != nil {
		return "", ""
	}
	return st.S3.Bucket, st.S3.CredentialsSecret
}

// psmdbInjectedCredentialsSecret is the Secret name the injected storage entry
// carries: the strategy's, or the platform-projected default when the strategy
// leaves it empty. It is the one field on the entry the driver chose (bucket and
// endpoint are whatever the platform bucket happens to be named or hosted at),
// so it is what identifies the driver's entry on a live cluster.
func psmdbInjectedCredentialsSecret(s3 *strategyv1alpha1.MongoDBStorageS3) string {
	if s3.CredentialsSecret != "" {
		return s3.CredentialsSecret
	}
	return psmdbDefaultCredentialsSecret
}

// shouldInjectMongoDBSystemStorage reports whether the driver must SSA-inject
// the system-bucket storage onto the live cluster: only when the app opted in
// via backup.useSystemBucket AND the strategy actually carries S3 coordinates
// to inject. Keying off the app flag (not the live cluster's storage set) is
// the load-bearing guarantee: a legacy app leaves the flag false and is never
// touched, so injection never clobbers a manually configured bucket; a
// useSystemBucket app is injected on every BackupJob, so a later change to the
// strategy coordinates catches up instead of being frozen at the first backup.
func shouldInjectMongoDBSystemStorage(useSystemBucket bool, rendered *strategyv1alpha1.MongoDBTemplate) bool {
	return useSystemBucket && rendered.S3 != nil
}

// buildMongoDBSystemStorageEntry constructs the psmdb spec.backup.storages
// entry the driver injects on the useSystemBucket flow. Factored out of the
// SSA path so the defaulting (empty credentialsSecret → cozy-backups-creds),
// the forcePathStyle omit-when-nil (a nil pointer must not surface as a
// forcePathStyle:false the app never chose), and the {"type":"s3","s3":{…}}
// shape are exercised without a live apiserver.
func buildMongoDBSystemStorageEntry(s3 *strategyv1alpha1.MongoDBStorageS3) map[string]interface{} {
	cred := psmdbInjectedCredentialsSecret(s3)
	s3Cfg := map[string]interface{}{
		"bucket":                s3.Bucket,
		"endpointUrl":           s3.EndpointURL,
		"region":                s3.Region,
		"prefix":                s3.Prefix,
		"credentialsSecret":     cred,
		"insecureSkipTLSVerify": s3.InsecureSkipTLSVerify,
	}
	if s3.ForcePathStyle != nil {
		s3Cfg["forcePathStyle"] = *s3.ForcePathStyle
	}
	return map[string]interface{}{"type": "s3", "s3": s3Cfg}
}

// applyMongoDBSystemStorage SSA-injects the system-bucket S3 storage onto the
// live PerconaServerMongoDB's spec.backup.storages[storageName] and returns the
// server's post-apply view of the cluster. On the useSystemBucket flow the app
// chart omits the storage (it cannot know the platform bucket/endpoint at render
// time), so the driver owns this subtree via a dedicated field manager +
// ForceOwnership - the app's Helm/Flux manager never sets it, so re-renders
// leave the injected storage intact (mirrors the CNPG driver's spec.plugins
// patch). CredentialsSecret defaults to cozy-backups-creds, which the BackupJob
// reconciler projects into the namespace before dispatch. The typed Patch
// decodes the apiserver's response (the full merged object, storage included)
// back into the patch object, so the caller reads the injected storage straight
// from the return value instead of a follow-up Get, which would hit the
// informer cache and see the pre-patch cluster.
func (r *BackupJobReconciler) applyMongoDBSystemStorage(ctx context.Context, namespace, psmdbName, storageName string, s3 *strategyv1alpha1.MongoDBStorageS3) (*psmdbtypes.PerconaServerMongoDB, error) {
	entry, err := json.Marshal(buildMongoDBSystemStorageEntry(s3))
	if err != nil {
		return nil, err
	}
	patch := &psmdbtypes.PerconaServerMongoDB{
		TypeMeta:   metav1.TypeMeta{APIVersion: psmdbtypes.GroupVersion.String(), Kind: "PerconaServerMongoDB"},
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: psmdbName},
	}
	patch.Spec.Backup.Storages = map[string]runtime.RawExtension{
		storageName: {Raw: entry},
	}
	if err := r.Patch(ctx, patch, client.Apply, client.FieldOwner(psmdbFieldManager), client.ForceOwnership); err != nil {
		return nil, err
	}
	return patch, nil
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

// psmdbBackupTimedOut reports whether an in-progress operator backup should be
// failed on the wall-clock deadline. On the useSystemBucket flow a backup that
// has begun streaming (state=running) is exempt: pbm keeps writing into the
// shared bucket after the driver gives up, and the BackupJob's terminal-phase
// guard means the artifact-creating branch never runs again, so failing it
// would strand an archive that no Backup object represents and no retention
// can reach (the operator CR carries no ownerRef either). A real dataset
// routinely outruns 30m, and a genuinely broken dump terminates through the
// operator's own error/rejected state, so it is left to finish. On the legacy
// flow the deadline applies to `running` too: the archive is in the tenant's
// own bucket and is theirs to reclaim (no platform retention covers it), so
// nothing the platform owns is stranded, and the pre-existing contract failed
// the job on the deadline while leaving the operator CR alone. The deadline bounds the not-yet-`running` states ("",
// requested, waiting) on both flows: a backup the operator has not begun
// streaming can still be advanced later — a `waiting` backup runs when the slot
// frees, and `requested` is set as the operator dispatches — so the caller
// cancels the operator CR when this trips, rather than leaving it to run into
// the shared bucket after the BackupJob is already Failed.
func psmdbBackupTimedOut(state string, startedAt *metav1.Time, useSystemBucket bool) bool {
	if state == psmdbtypes.StateRunning && useSystemBucket {
		return false
	}
	return psmdbBackupDeadlineExceeded(startedAt)
}

// psmdbErrorIsUnresolvedStorage reports whether a PerconaServerMongoDBBackup
// error is the operator failing to resolve the named storage — the message the
// v1.22.0 backup reconciler sets ("unable to get storage '<name>'") when it
// reads a cluster cache that has not yet observed the driver's storage
// injection. That is the residual half of the injection race, retryable rather
// than a tenant failure. The match is against upstream's wording at the
// operator version the chart pins (packages/apps/mongodb/templates/mongodb.yaml,
// spec.crVersion): a bump that rewords this error silently turns the retry
// off, so re-check pkg/controller/perconaservermongodbbackup/backup.go there.
func psmdbErrorIsUnresolvedStorage(errMsg, storageName string) bool {
	return strings.Contains(errMsg, "unable to get storage") && strings.Contains(errMsg, storageName)
}

// psmdbBackupLiveState reads the operator backup CR straight from the apiserver
// (bypassing the informer cache) and returns its status.state. It returns an
// error whenever the live state could not be established — the dynamic client is
// unset, the read failed (throttle, 403, removed CRD), or the object is gone —
// so the caller can tell "could not read" apart from a genuine empty state and
// never treat an unanswerable read as a not-started answer. A successful read of
// a CR that carries no status.state yet returns ("", nil).
func (r *BackupJobReconciler) psmdbBackupLiveState(ctx context.Context, namespace, name string) (string, error) {
	if r.Interface == nil {
		return "", fmt.Errorf("dynamic client not configured")
	}
	u, err := r.Interface.Resource(psmdbBackupGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	state, _, _ := unstructured.NestedString(u.Object, "status", "state")
	return state, nil
}

// ensureMongoDBBackup creates a one-shot PerconaServerMongoDBBackup CR labelled
// with the BackupJob, or returns the existing one if a previous reconcile
// already created it. Idempotency relies on the OwningJob labels.
func (r *BackupJobReconciler) ensureMongoDBBackup(ctx context.Context, j *backupsv1alpha1.BackupJob, clusterName, storageName string, rendered *strategyv1alpha1.MongoDBTemplate, useSystemBucket bool) (*psmdbtypes.PerconaServerMongoDBBackup, error) {
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
	if useSystemBucket {
		// Own the archive's lifecycle on the shared bucket: the operator's
		// delete-backup finalizer prunes the pbm object from storage when this CR
		// is deleted, which is how the cleanup path (Plan retention) reclaims
		// cozy-backups. A legacy backup writes to the tenant's own bucket and is
		// left unfinalized, so cleanup reads it as unowned. That finalizer check
		// is only ever run against the one CR this function minted — cleanup
		// resolves it by the backup-name recorded in driverMetadata, never by
		// label or by scanning the namespace — so a task-minted CR that the
		// operator itself finalizes on the legacy flow (spec.backup.tasks with a
		// delete-from-storage retention) is never in scope and never mistaken for
		// ours.
		controllerutil.AddFinalizer(obj, psmdbDeleteBackupFinalizer)
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
	// Skip CRs already being deleted. The storage-race retry deletes an errored
	// CR so a fresh one resolves against a caught-up cache, but the release-lock
	// finalizer keeps it briefly Terminating; treating that lingering CR as the
	// current one would re-observe the same error and requeue without ever
	// re-minting. The mint uses GenerateName, so a new CR never collides with the
	// Terminating one.
	live := make([]*psmdbtypes.PerconaServerMongoDBBackup, 0, len(list.Items))
	for i := range list.Items {
		if list.Items[i].DeletionTimestamp.IsZero() {
			live = append(live, &list.Items[i])
		}
	}
	if len(live) == 0 {
		return nil, nil
	}
	if len(live) > 1 {
		names := make([]string, 0, len(live))
		for _, b := range live {
			names = append(names, b.Name)
		}
		getLogger(ctx).Debug("multiple PerconaServerMongoDBBackup CRs match BackupJob OwningJob labels; reusing first",
			"backupjob", j.Name, "namespace", j.Namespace, "matches", names, "picked", names[0])
	}
	return live[0], nil
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
	useSystemBucket bool,
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

	underlyingResources, err := marshalMongoDBBackupSnapshot(mdbBackup, rendered, storageName, resolved.Parameters, useSystemBucket)
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

// cleanupMongoDBBackup prunes the pbm archive a system-bucket Backup wrote to
// the shared cozy-backups bucket when that Backup is deleted (Plan retention or
// manual). Ownership is read from the delete-backup finalizer on the ONE
// operator CR named in driverMetadata (the one ensureMongoDBBackup minted for
// this Backup) — resolved by name, never by label or scan, so a task-minted CR
// the operator finalizes on the legacy flow is out of scope. ensureMongoDBBackup
// stamps that finalizer only on the useSystemBucket flow, so its presence on the
// named CR is the "the driver owns this archive" marker (and there is no separate
// driverMetadata flag that could disagree if the app's useSystemBucket flag
// flipped mid-backup). When the driver owns it, this deletes the operator CR and
// waits for the finalizer to remove the object before releasing the Cozystack
// Backup, so nothing is orphaned — the "own the artifact, wait for the delete"
// contract the Redis/Rabbitmq branches use. A legacy backup (no such finalizer)
// is left untouched, the pre-existing no-op contract for the operator-backed
// drivers.
//
// The wait has give-up conditions so a wedged operator does not, on its own,
// keep this driver holding the Backup in Terminating: the escape-hatch
// annotation (honoured before any apiserver read, so it still frees a Backup
// whose operator CR has become unreadable) and a going-away namespace (the psmdb
// delete-backup exec cannot run there) each make the driver strip its own
// finalizer and release, leaving the object for a bucket lifecycle policy. This
// bounds only what the driver contributes: a Get that fails with something other
// than NotFound/NoMatchError still requeues (below), and the psmdb operator's own
// release-lock finalizer keeps the CR Terminating while the operator is down,
// whatever the driver strips. A down operator in a live namespace is likewise
// not auto-released: the Backup stays Terminating as a visible signal until an
// operator recovers or the annotation is set.
func (r *BackupReconciler) cleanupMongoDBBackup(ctx context.Context, backup *backupsv1alpha1.Backup) (ctrl.Result, error) {
	sourceBackupName := backup.Spec.DriverMetadata[psmdbBackupNameKey]
	if sourceBackupName == "" {
		// Nothing recorded to delete (Backup written before the name was tracked).
		return ctrl.Result{}, nil
	}

	// Escape hatch first, before any apiserver read: the Backup must be releasable
	// even when the operator CR cannot be read at all — its CRD removed, or the
	// verb revoked — which is exactly the situation someone sets it in. Mirrors
	// the Redis cleanup, which reads its skip annotation before it touches the
	// apiserver.
	if backup.Annotations[psmdbSkipArtifactCleanupAnnotation] == "true" {
		// No object in hand (this runs before any read); releaseMongoDBCleanup
		// does the best-effort fetch.
		return r.releaseMongoDBCleanup(ctx, backup, sourceBackupName, nil, "skip-artifact-cleanup annotation set")
	}

	live := &psmdbtypes.PerconaServerMongoDBBackup{}
	err := r.Get(ctx, types.NamespacedName{Namespace: backup.Namespace, Name: sourceBackupName}, live)
	if apierrors.IsNotFound(err) || apimeta.IsNoMatchError(err) {
		// The operator CR is gone (a driver-owned archive was pruned with it, and
		// a legacy CR that is gone was nothing of ours), or its CRD is no longer
		// served (nothing to strip when the kind itself is unmapped). Release.
		// Classifying NoMatchError alongside NotFound matches the Job/Redis
		// cleanups.
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}

	if !controllerutil.ContainsFinalizer(live, psmdbDeleteBackupFinalizer) {
		// Legacy backup: the driver never stamped the prune finalizer, so the
		// archive is the tenant's (its own bucket) and its lifecycle is not ours.
		return ctrl.Result{}, nil
	}

	terminating, nsErr := r.namespaceTerminating(ctx, backup.Namespace)
	if nsErr != nil {
		return ctrl.Result{}, nsErr
	}
	if terminating {
		// Hand down the object already read and confirmed owned above, so the
		// release strips its finalizer without a second Get that a transient
		// throttle/Forbidden could fold into "nothing of ours" — leaving the CR
		// finalized in a namespace being torn down, the exact wedge this prevents.
		return r.releaseMongoDBCleanup(ctx, backup, sourceBackupName, live, "namespace is terminating")
	}

	if live.DeletionTimestamp.IsZero() {
		if derr := r.Delete(ctx, live); derr != nil && !apierrors.IsNotFound(derr) {
			return ctrl.Result{}, derr
		}
	}
	// Requeue until the operator finalizer has removed the CR (and the archive
	// with it).
	getLogger(ctx).Debug("waiting for psmdb operator to prune system-bucket archive",
		"backup", backup.Name, "sourceBackup", sourceBackupName)
	return ctrl.Result{RequeueAfter: psmdbPollInterval}, nil
}

// releaseMongoDBCleanup lets the Cozystack Backup (and any enclosing namespace)
// finish deleting when the archive cannot be pruned in place. It only ever
// touches an operator CR the driver OWNS — one carrying the delete-backup
// finalizer: it strips that finalizer so the CR no longer blocks teardown and
// deletes it. A legacy CR (no finalizer) or one that cannot be read at all is
// left entirely intact, so the never-deleted contract for legacy backups holds
// even though the annotation reaches this path before the ownership check, and
// an unreadable CR still releases. The finalizer strip is best-effort — the
// release must proceed even against an unreachable CR — but a failed strip is
// surfaced (log + Event), because the CR then keeps the finalizer and can hold
// its namespace in Terminating, and the operator needs to know which one.
//
// `live` is the object the caller already read and confirmed owned; the
// terminating path passes it so the strip is not gated on a second Get whose
// transient failure would silently drop it. The annotation path passes nil (it
// runs before any read), and this does the best-effort fetch itself — an
// unreadable or unowned CR is then left intact and the Backup still releases.
func (r *BackupReconciler) releaseMongoDBCleanup(ctx context.Context, backup *backupsv1alpha1.Backup, sourceBackupName string, live *psmdbtypes.PerconaServerMongoDBBackup, reason string) (ctrl.Result, error) {
	logger := getLogger(ctx)
	if live == nil {
		fetched := &psmdbtypes.PerconaServerMongoDBBackup{}
		err := r.Get(ctx, types.NamespacedName{Namespace: backup.Namespace, Name: sourceBackupName}, fetched)
		switch {
		case err != nil:
			// The CR could not be read (its CRD removed, the verb revoked, a
			// throttle). It may still carry the driver's delete-backup finalizer, so
			// the annotation releases the Backup regardless — but this is the same
			// class of leftover as a failed strip below, so surface which object may
			// be left finalized rather than a Debug line nobody sees.
			logger.Info("releasing MongoDB Backup; could not read the operator CR to strip its finalizer",
				"backup", backup.Name, "sourceBackup", sourceBackupName, "reason", reason, "error", err)
			if r.Recorder != nil {
				r.Recorder.Eventf(backup, corev1.EventTypeWarning, "FinalizerNotStripped",
					"released Backup but could not read PerconaServerMongoDBBackup %s to strip its delete-backup finalizer (%v); if it carries the finalizer it may hold its namespace in Terminating", sourceBackupName, err)
			}
			return ctrl.Result{}, nil
		case !controllerutil.ContainsFinalizer(fetched, psmdbDeleteBackupFinalizer):
			// Legacy backup (no delete-backup finalizer): genuinely not the
			// driver's, so leave the operator CR untouched and release quietly.
			logger.Debug("releasing MongoDB Backup; no driver-owned archive to prune",
				"backup", backup.Name, "sourceBackup", sourceBackupName, "reason", reason)
			return ctrl.Result{}, nil
		}
		live = fetched
	}

	base := live.DeepCopy()
	controllerutil.RemoveFinalizer(live, psmdbDeleteBackupFinalizer)
	if perr := r.Patch(ctx, live, client.MergeFrom(base)); perr != nil {
		// The release still proceeds (an unreachable CR must not wedge the
		// Backup), but the CR keeps the finalizer and can pin its namespace in
		// Terminating — surface which object so an operator can find it.
		logger.Info("released MongoDB Backup but could not strip the delete-backup finalizer; the operator CR may hold its namespace in Terminating",
			"backup", backup.Name, "sourceBackup", sourceBackupName, "reason", reason, "error", perr)
		if r.Recorder != nil {
			r.Recorder.Eventf(backup, corev1.EventTypeWarning, "FinalizerNotStripped",
				"released Backup but could not strip the delete-backup finalizer from PerconaServerMongoDBBackup %s (%v); it may keep the namespace in Terminating", sourceBackupName, perr)
		}
		return ctrl.Result{}, nil
	}
	if live.DeletionTimestamp.IsZero() {
		_ = r.Delete(ctx, live)
	}
	logger.Info("releasing MongoDB Backup, leaving its shared-bucket object for a lifecycle policy",
		"backup", backup.Name, "sourceBackup", sourceBackupName, "reason", reason)
	if r.Recorder != nil {
		r.Recorder.Eventf(backup, corev1.EventTypeWarning, "ArtifactNotDeleted",
			"left MongoDB archive in the bucket: %s", reason)
	}
	return ctrl.Result{}, nil
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

	// Point-in-time recovery needs an oplog stream, which the useSystemBucket flow
	// never captures — the chart leaves spec.backup.pitr unset there (see
	// backup-classes.md). Refuse a recoveryTime restore of a system-bucket backup
	// with a named error rather than hand the operator a PITR target it cannot
	// serve. Identify the flow from the flag recorded on the snapshot at backup
	// time, not from the resolved credentialsSecret: a BackupClass may name any
	// Secret, so the Secret name is only a proxy, while the snapshot flag is what
	// the backup actually ran on.
	if pitr != nil {
		snap, serr := unmarshalMongoDBBackupSnapshot(backup.Status.UnderlyingResources)
		if serr != nil {
			// Flow unknown: refuse rather than fall open. resolveMongoDBBackupSource
			// treats the same decode failure as fatal only on its snapshot-fallback
			// path and returns from the live-CR path first, so this is the one check
			// that still sees it while the operator CR is alive.
			return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf(
				"restoreJob.spec.options.recoveryTime is set, but the Backup snapshot cannot be decoded (%v), so the flow the backup was taken on is unknown; refusing point-in-time recovery rather than handing the operator a target it may not serve", serr))
		}
		if snap != nil && snap.UseSystemBucket {
			return r.markRestoreJobFailed(ctx, restoreJob,
				"restoreJob.spec.options.recoveryTime is set, but point-in-time recovery is not available for a backup taken on the useSystemBucket flow (no oplog is captured there); omit recoveryTime to restore the backup as taken. See docs/operations/backup-classes.md.")
		}
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

	// Re-point the restore at the TARGET cluster's own S3 credentials when a safe
	// swap applies. On the legacy flow the backupSource carries the SOURCE
	// release's -s3-creds Secret, which is deleted together with the source app —
	// exactly the disaster-recovery case backups exist for — so a restore into a
	// fresh, differently-named target must not depend on it. mongodbRestoreCreden-
	// tialsSecret encodes the cross-flow guards (skip when the source already
	// names the projected cozy-backups-creds; adopt a target credential only on
	// the same bucket as the archive); it is unit-tested across the four flow
	// combinations.
	if source.S3 != nil {
		if cred := mongodbRestoreCredentialsSecret(source, targetCluster); cred != "" && cred != source.S3.CredentialsSecret {
			logger.Debug("re-pointing MongoDB restore credentials at target cluster storage",
				"restorejob", restoreJob.Name, "from", source.S3.CredentialsSecret, "to", cred)
			source.S3.CredentialsSecret = cred
		}
		// The reference must resolve before the operator sees it. After an app opts
		// into useSystemBucket the chart prunes the legacy <release>-s3-creds while
		// older Backups still name it, and with no same-bucket target credential to
		// adopt the restore would reach the operator naming a Secret that is gone
		// and die on a pbm-level message. Like the other preconditions here (target
		// absent, backups disabled) this waits until the restore deadline and only
		// then fails — the Secret can be re-created. It is a hand-over check: once
		// the operator restore exists the reference is the operator's, and a Secret
		// pruned mid-replay is its error to report, not grounds to mark a restore
		// still in progress Failed.
		if source.S3.CredentialsSecret != "" {
			existingRestore, err := r.findMongoDBRestoreForJob(ctx, restoreJob)
			if err != nil {
				return ctrl.Result{}, err
			}
			if existingRestore == nil {
				if err := r.Get(ctx, types.NamespacedName{Namespace: target.Namespace, Name: source.S3.CredentialsSecret}, &corev1.Secret{}); err != nil {
					if !apierrors.IsNotFound(err) {
						return ctrl.Result{}, err
					}
					msg := fmt.Sprintf(
						"credentialsSecret %q for bucket %q does not exist in namespace %s (the chart prunes it when the application opts into useSystemBucket); re-create a Secret by that name holding credentials for that bucket, or restore into a target whose storage declares that bucket",
						source.S3.CredentialsSecret, source.S3.Bucket, target.Namespace)
					deadline := options.effectiveRestoreDeadline()
					if restoreJob.Status.StartedAt != nil && time.Since(restoreJob.Status.StartedAt.Time) > deadline {
						return r.markRestoreJobFailedReason(ctx, restoreJob, "RestoreCredentialsMissing", fmt.Sprintf("%s; not resolved within %s", msg, deadline))
					}
					return r.requeueMongoDBRestoreWaiting(ctx, restoreJob, "RestoreCredentialsMissing", msg)
				}
			}
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
	// Snapshot persisted at backup time. Decoded up front so the live path can
	// backfill an S3 block the operator status omits. The pinned psmdb operator
	// (v1.22.0) always echoes status.s3 once a backup reaches a destination
	// (reconcilePBMConfig resolves the storage), so on that operator the live
	// path already carries coordinates and this backfill is a defensive guard —
	// an operator that reported a bare destination would otherwise strand a
	// system-bucket restore, whose target has no chart-declared storage to
	// resolve from (injection runs only on the BackupJob path). It keeps the
	// snapshot the single fallback source, matching the reaped-CR path below.
	snap, snapErr := unmarshalMongoDBBackupSnapshot(backup.Status.UnderlyingResources)

	// Live operator Backup CR (same namespace as the Cozystack Backup).
	sourceBackupName := backup.Spec.DriverMetadata[psmdbBackupNameKey]
	if sourceBackupName != "" {
		live := &psmdbtypes.PerconaServerMongoDBBackup{}
		err := r.Get(ctx, types.NamespacedName{Namespace: backup.Namespace, Name: sourceBackupName}, live)
		if err == nil {
			if live.Status.Destination != "" {
				src := &psmdbtypes.BackupSource{
					Type:        psmdbBackupTypeOrDefault(live.Status.Type),
					Destination: live.Status.Destination,
					StorageName: live.Status.StorageName,
					S3:          live.Status.S3.DeepCopy(),
				}
				if src.S3 == nil && snap != nil {
					src.S3 = snap.S3.DeepCopy()
					if src.StorageName == "" {
						src.StorageName = snap.StorageName
					}
				}
				return src, nil
			}
		} else if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: get source PerconaServerMongoDBBackup %s/%s: %v", errTransientRestoreSource, backup.Namespace, sourceBackupName, err)
		}
	}

	// Snapshot fallback (operator CR reaped or never reported a destination).
	if snapErr != nil {
		return nil, fmt.Errorf("decode Backup snapshot: %v", snapErr)
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

// findMongoDBRestoreForJob returns the PerconaServerMongoDBRestore labelled with
// the RestoreJob's OwningJob{Name,Namespace}, or (nil, nil) when none exists.
func (r *RestoreJobReconciler) findMongoDBRestoreForJob(ctx context.Context, rj *backupsv1alpha1.RestoreJob) (*psmdbtypes.PerconaServerMongoDBRestore, error) {
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
	if len(list.Items) == 0 {
		return nil, nil
	}
	return &list.Items[0], nil
}

// ensureMongoDBRestore creates a PerconaServerMongoDBRestore CR labelled with
// the RestoreJob, or returns the existing one if a previous reconcile already
// created it.
func (r *RestoreJobReconciler) ensureMongoDBRestore(ctx context.Context, rj *backupsv1alpha1.RestoreJob, targetClusterName string, source *psmdbtypes.BackupSource, pitr *psmdbtypes.PITRSpec) (*psmdbtypes.PerconaServerMongoDBRestore, error) {
	if existing, err := r.findMongoDBRestoreForJob(ctx, rj); err != nil || existing != nil {
		return existing, err
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
	// UseSystemBucket records the flow the backup was taken on, so the restore
	// path can identify a system-bucket backup directly rather than inferring it
	// from the resolved credentialsSecret name (which a BackupClass may set to
	// anything). Point-in-time recovery is refused on this flow.
	UseSystemBucket bool `json:"useSystemBucket,omitempty"`
}

func marshalMongoDBBackupSnapshot(mdbBackup *psmdbtypes.PerconaServerMongoDBBackup, rendered *strategyv1alpha1.MongoDBTemplate, storageName string, parameters map[string]string, useSystemBucket bool) (*runtime.RawExtension, error) {
	snap := mongodbBackupSnapshot{
		Kind:            psmdbBackupSnapshotKind,
		APIVersion:      psmdbBackupSnapshotAPIVersion,
		Destination:     mdbBackup.Status.Destination,
		Type:            psmdbBackupTypeOrDefault(rendered.Type),
		StorageName:     storageName,
		S3:              mdbBackup.Status.S3.DeepCopy(),
		Parameters:      parameters,
		UseSystemBucket: useSystemBucket,
	}
	// On the useSystemBucket flow the driver injected the storage, so fall back
	// to those coordinates when the operator's status echo is empty - the
	// restore path rebuilds backupSource from this snapshot and needs the
	// endpoint + credentialsSecret (cozy-backups-creds) to be present. Gated on
	// the app's flag, not on rendered.S3 alone: the coordinates now live on the
	// shared cozy-default strategy, so rendered.S3 is non-nil for a legacy app
	// too, and recording the platform bucket for an archive written to the
	// tenant's own bucket would send restore to the wrong place.
	if useSystemBucket && snap.S3 == nil && rendered.S3 != nil {
		cred := rendered.S3.CredentialsSecret
		if cred == "" {
			cred = psmdbDefaultCredentialsSecret
		}
		snap.S3 = &psmdbtypes.BackupStorageS3{
			Bucket:                rendered.S3.Bucket,
			CredentialsSecret:     cred,
			EndpointURL:           rendered.S3.EndpointURL,
			Prefix:                rendered.S3.Prefix,
			Region:                rendered.S3.Region,
			ForcePathStyle:        rendered.S3.ForcePathStyle,
			InsecureSkipTLSVerify: rendered.S3.InsecureSkipTLSVerify,
		}
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
