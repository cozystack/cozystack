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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	"github.com/cozystack/cozystack/internal/backupcontroller/etcdapp"
	"github.com/cozystack/cozystack/internal/backupcontroller/etcdtypes"
	"github.com/cozystack/cozystack/internal/template"
)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const (
	// etcdAppKind is the apps.cozystack.io Kind the driver claims.
	etcdAppKind = "Etcd"

	// etcdClusterName is the singleton etcd.aenix.io/EtcdCluster name the
	// chart renders (templates/etcd-cluster.yaml sets metadata.name: etcd
	// regardless of the application name). The chart additionally pins the
	// Helm release name to "etcd" via templates/check-release-name.yaml -
	// so per namespace there is exactly one EtcdCluster named "etcd" that
	// the driver operates on.
	etcdClusterName = "etcd"

	// Driver-metadata keys persisted on Cozystack Backup artefacts. The
	// restore path reads these to feed
	// EtcdCluster.spec.bootstrap.restore.source.s3 on the re-created
	// EtcdCluster.
	etcdBackupBucketKey         = "etcd.aenix.io/bucket"
	etcdBackupEndpointKey       = "etcd.aenix.io/endpoint"
	etcdBackupKeyKey            = "etcd.aenix.io/key"
	etcdBackupRegionKey         = "etcd.aenix.io/region"
	etcdBackupForcePathStyleKey = "etcd.aenix.io/force-path-style"
	etcdBackupCredsSecretKey    = "etcd.aenix.io/credentials-secret-name"
	etcdBackupNameKey           = "etcd.aenix.io/backup-name"

	// Polling cadence for both the EtcdBackup status loop and the restore
	// purge/Ready loop. Matches the upstream operator's reconcile beat.
	etcdPollInterval = 10 * time.Second

	// Wall-clock cap on a BackupJob waiting for the operator EtcdBackup
	// to reach phase=Complete. A single etcd snapshot rarely takes more
	// than a minute, but the Job pod may sit Pending behind image pulls
	// and PVC provisioning. CNPG/MariaDB use 30m; etcd is faster so 20m
	// is plenty without making operators tail logs forever before a
	// terminal Failed surfaces.
	etcdDefaultBackupDeadline = 20 * time.Minute

	// Wall-clock cap on a RestoreJob waiting for the new EtcdCluster to
	// reach Ready. Includes EtcdCluster deletion drain, PVC garbage
	// collection, fresh PVC provisioning, snapshot download, etcd boot,
	// peer/client TLS handshake, and member election. 30m matches the
	// CNPG/Velero pattern and gives slow CSI provisioners headroom.
	etcdDefaultRestoreDeadline = 30 * time.Minute

	// etcdBackupSnapshotKind / etcdBackupSnapshotAPIVersion tag the
	// payload persisted on Cozystack Backup.status.underlyingResources.
	// Schema v1; bump the apiVersion the moment the on-disk shape changes
	// in a way that breaks older readers - the restore path's
	// errSnapshotUnrecognised gate refuses unfamiliar snapshots rather
	// than silently falling back to driverMetadata, which on a real
	// version-bump would corrupt the restore (a v2 snapshot may carry
	// fields whose absence the v1 reader would interpret incorrectly).
	etcdBackupSnapshotKind = "EtcdBackupSnapshot"

	// Restore-state condition surfaced on the RestoreJob so a controller
	// crash mid-purge can repair on the next reconcile without
	// re-deleting an already-restored cluster.
	etcdRestoreCondTargetPurged = "TargetPurged"

	// etcdRestoreCapturedSpecMaxBytes caps the marshaled chart-rendered
	// EtcdCluster spec the driver stuffs into the EtcdClusterSpecCaptured
	// condition message. The Kubernetes Condition schema caps message at
	// 32 KiB (maxLength: 32768); writing past that triggers a CRD
	// validation rejection on r.Status().Update, which - if the
	// rejection lands after the spec snapshot was taken but before
	// TargetPurged is set - would leave the RestoreJob mid-purge with
	// the cluster already deleted and the spec irrecoverable. Fail fast
	// with a clear terminal message BEFORE any destructive step instead;
	// 24 KiB leaves ~25% headroom for the condition wrapper (type,
	// reason, timestamps) the API server adds on top of message.
	etcdRestoreCapturedSpecMaxBytes = 24 * 1024
)

var etcdBackupSnapshotAPIVersion = backupsv1alpha1.GroupVersion.String()

// etcdClusterGVR is the GroupVersionResource for the operator-side
// EtcdCluster CR. Used by the dynamic client on the restore path so the
// driver captures and re-creates the FULL chart-rendered spec (replicas,
// storage, security, options, podTemplate, ...) rather than the
// 1-field-typed subset etcdtypes.EtcdClusterSpec carries. The typed
// client is still used for reads that only need status.conditions.
var etcdClusterGVR = schema.GroupVersionResource{
	Group:    etcdtypes.GroupName,
	Version:  etcdtypes.Version,
	Resource: "etcdclusters",
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

// validateEtcdApplicationRef rejects ApplicationRefs that name a
// Kind/APIGroup the Etcd driver does not own. Mirrors the FoundationDB
// driver's defence: without this gate a ref like other.example.com/Etcd
// would be accepted by the Kind check alone and then reconciled against
// the wrong CRD via the typed client.
func validateEtcdApplicationRef(ref corev1.TypedLocalObjectReference) error {
	if ref.Kind != etcdAppKind {
		return fmt.Errorf("Etcd strategy supports applicationRef.kind=%q, got %q", etcdAppKind, ref.Kind)
	}
	apiGroup := ""
	if ref.APIGroup != nil {
		apiGroup = *ref.APIGroup
	}
	if apiGroup != "" && apiGroup != etcdapp.GroupName {
		return fmt.Errorf("Etcd strategy supports applicationRef.apiGroup=%q, got %q", etcdapp.GroupName, apiGroup)
	}
	return nil
}

// ---------------------------------------------------------------------------
// BackupJob path
// ---------------------------------------------------------------------------

func (r *BackupJobReconciler) reconcileEtcd(ctx context.Context, j *backupsv1alpha1.BackupJob, resolved *ResolvedBackupConfig) (ctrl.Result, error) {
	logger := getLogger(ctx)
	logger.Debug("reconciling Etcd strategy", "backupjob", j.Name, "phase", j.Status.Phase)

	if j.Status.Phase == backupsv1alpha1.BackupJobPhaseSucceeded ||
		j.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
		return ctrl.Result{}, nil
	}

	if err := validateEtcdApplicationRef(j.Spec.ApplicationRef); err != nil {
		return r.markBackupJobFailed(ctx, j, err.Error())
	}

	if j.Status.StartedAt == nil {
		// Refetch latest persisted state before writing StartedAt: a stale
		// informer cache that returns StartedAt==nil after we already
		// persisted it would otherwise let the deadline gate slide forward
		// on every poll. Same idempotency pattern as the FDB driver.
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
			return ctrl.Result{RequeueAfter: etcdPollInterval}, nil
		}
	}

	strategy := &strategyv1alpha1.Etcd{}
	if err := r.Get(ctx, client.ObjectKey{Name: resolved.StrategyRef.Name}, strategy); err != nil {
		if apierrors.IsNotFound(err) {
			return r.markBackupJobFailed(ctx, j, fmt.Sprintf("Etcd strategy not found: %s", resolved.StrategyRef.Name))
		}
		return ctrl.Result{}, err
	}

	app, err := r.getEtcdApp(ctx, j.Namespace, j.Spec.ApplicationRef.Name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.markBackupJobFailed(ctx, j, fmt.Sprintf("Etcd application not found: %s/%s", j.Namespace, j.Spec.ApplicationRef.Name))
		}
		return ctrl.Result{}, err
	}

	rendered, err := renderEtcdTemplate(strategy.Spec.Template, app, resolved.Parameters)
	if err != nil {
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf("failed to template Etcd strategy: %v", err))
	}
	if err := validateRenderedEtcdDestination(rendered.Destination); err != nil {
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf("rendered Etcd destination is invalid: %v", err))
	}

	// EtcdCluster is the chart-rendered, singleton-named CR. Defer the
	// EtcdBackup creation until the cluster reports Ready - the operator
	// otherwise materialises a Job that fails immediately because there
	// are no etcd members to snapshot.
	//
	// Wait-budget: the cluster-Ready wait shares the same wall-clock cap
	// (etcdDefaultBackupDeadline) as the operator-side EtcdBackup wait.
	// Without this gate, a BackupJob against a never-Ready EtcdCluster
	// (broken etcd, deleted source app, stuck PVC provisioner) requeues
	// forever and the tenant gets no terminal signal. Mirror the same
	// deadline check the post-EtcdBackup-created path uses (line ~286)
	// so the failure mode collapses to a clean phase=Failed.
	cluster := &etcdtypes.EtcdCluster{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: j.Namespace, Name: etcdClusterName}, cluster); err != nil {
		if apierrors.IsNotFound(err) {
			if j.Status.StartedAt != nil && time.Since(j.Status.StartedAt.Time) > etcdDefaultBackupDeadline {
				return r.markBackupJobFailed(ctx, j, fmt.Sprintf(
					"etcd.aenix.io/EtcdCluster %s/%s did not appear within %s (deploy the source Etcd application before requesting the backup)",
					j.Namespace, etcdClusterName, etcdDefaultBackupDeadline))
			}
			return r.requeueBackupJobWithReason(ctx, j, "EtcdClusterNotReady",
				fmt.Sprintf("waiting for etcd.aenix.io/EtcdCluster %s/%s to exist", j.Namespace, etcdClusterName))
		}
		return ctrl.Result{}, err
	}
	if !etcdClusterReady(cluster) {
		if j.Status.StartedAt != nil && time.Since(j.Status.StartedAt.Time) > etcdDefaultBackupDeadline {
			return r.markBackupJobFailed(ctx, j, fmt.Sprintf(
				"etcd.aenix.io/EtcdCluster %s/%s did not reach Ready within %s (current Ready condition: %s)",
				j.Namespace, etcdClusterName, etcdDefaultBackupDeadline, etcdClusterReadyReason(cluster)))
		}
		return r.requeueBackupJobWithReason(ctx, j, "EtcdClusterNotReady",
			fmt.Sprintf("waiting for etcd.aenix.io/EtcdCluster %s/%s to become Ready", j.Namespace, etcdClusterName))
	}

	// Intentionally skip a pre-flight Secret existence check for the
	// rendered S3 credentialsSecretRef. The shared backupstrategy-
	// controller RBAC (packages/system/backupstrategy-controller/templates/
	// rbac.yaml) does NOT grant cluster-scoped Secret list/watch -
	// reading the Secret through r.Get would force controller-runtime
	// to start a Secret informer cluster-wide, the watch is denied, and
	// the typed Get blocks indefinitely (the single BackupJob worker
	// stalls and no further reconciles happen). The FoundationDB
	// driver's analogous check gates on a non-empty backupDeployment-
	// PodTemplateSpec, which is unset for tenants that don't customise
	// the backup_agent pod - so the FDB path never exercises the broken
	// informer in practice. The etcd path would always hit it.
	//
	// Missing-Secret diagnostics still surface, just one step later:
	// the operator-side EtcdBackup Job mounts the Secret by name and
	// fails fast at pod start with a CreateContainerConfigError. The
	// BackupJob deadline (etcdDefaultBackupDeadline = 20m) catches the
	// pathological stuck case and flips the job Failed with a clear
	// message.

	eb, err := r.ensureEtcdBackup(ctx, j, rendered)
	if err != nil {
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf("failed to ensure etcd.aenix.io/EtcdBackup: %v", err))
	}

	if j.Status.Phase != backupsv1alpha1.BackupJobPhaseRunning {
		j.Status.Phase = backupsv1alpha1.BackupJobPhaseRunning
		if err := r.Status().Update(ctx, j); err != nil {
			return ctrl.Result{}, err
		}
	}

	switch eb.Status.Phase {
	case etcdtypes.BackupPhaseComplete:
		if j.Status.BackupRef != nil {
			return ctrl.Result{}, nil
		}
		artifact, err := r.createEtcdBackupArtifact(ctx, j, resolved, eb, rendered)
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
			Message: "etcd.aenix.io/EtcdBackup reached phase=Complete",
		})
		if err := r.Status().Update(ctx, j); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil

	case etcdtypes.BackupPhaseFailed:
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf(
			"etcd.aenix.io/EtcdBackup %s/%s reached phase=Failed (%s)",
			eb.Namespace, eb.Name, latestEtcdBackupConditionMessage(eb)))

	default:
		// Pending / Started / empty: still waiting on the operator.
		if j.Status.StartedAt != nil && time.Since(j.Status.StartedAt.Time) > etcdDefaultBackupDeadline {
			return r.markBackupJobFailed(ctx, j, fmt.Sprintf(
				"etcd.aenix.io/EtcdBackup %s/%s did not reach phase=Complete within %s (current phase=%q)",
				eb.Namespace, eb.Name, etcdDefaultBackupDeadline, eb.Status.Phase))
		}
		return r.requeueBackupJobWithReason(ctx, j, "EtcdBackupRunning",
			fmt.Sprintf("etcd.aenix.io/EtcdBackup %s/%s phase=%q", eb.Namespace, eb.Name, eb.Status.Phase))
	}
}

// requeueBackupJobWithReason stamps a transient Ready=False/<reason>
// condition and requeues after etcdPollInterval. Mirrors the FDB driver's
// transient-error helper but without the patch-on-Conflict retry loop
// because EtcdBackup status updates by the operator are far slower than
// FoundationDBBackup, so the 409 race the FDB driver works around does
// not surface in practice for this driver.
//
// Also flips phase to Running on first observable iteration so tenants
// tailing BackupJob.status.phase see activity instead of an empty string
// while the cluster boots. The Phase stays Running across subsequent
// requeues; only the terminal Succeeded/Failed transitions flip it.
func (r *BackupJobReconciler) requeueBackupJobWithReason(ctx context.Context, j *backupsv1alpha1.BackupJob, reason, message string) (ctrl.Result, error) {
	if j.Status.Phase != backupsv1alpha1.BackupJobPhaseRunning {
		j.Status.Phase = backupsv1alpha1.BackupJobPhaseRunning
	}
	apimeta.SetStatusCondition(&j.Status.Conditions, metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionFalse,
		Reason:  reason,
		Message: message,
	})
	if err := r.Status().Update(ctx, j); err != nil {
		if apierrors.IsConflict(err) {
			// Best-effort: let the next reconcile pick up the conflict.
			return ctrl.Result{RequeueAfter: etcdPollInterval}, nil
		}
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: etcdPollInterval}, nil
}

// etcdClusterReady checks the operator-side Ready condition.
func etcdClusterReady(c *etcdtypes.EtcdCluster) bool {
	if c == nil {
		return false
	}
	cond := apimeta.FindStatusCondition(c.Status.Conditions, etcdtypes.ClusterConditionReady)
	return cond != nil && cond.Status == metav1.ConditionTrue
}

// etcdClusterReadyReason returns a short human-readable summary of the
// operator-side Ready condition for inclusion in BackupJob terminal
// failure messages. Falls back to a sentinel when no Ready condition
// has been emitted yet.
func etcdClusterReadyReason(c *etcdtypes.EtcdCluster) string {
	if c == nil {
		return "cluster=nil"
	}
	cond := apimeta.FindStatusCondition(c.Status.Conditions, etcdtypes.ClusterConditionReady)
	if cond == nil {
		return "no Ready condition emitted yet"
	}
	if cond.Message != "" {
		return fmt.Sprintf("%s=%s/%s", cond.Type, cond.Status, cond.Message)
	}
	return fmt.Sprintf("%s=%s/%s", cond.Type, cond.Status, cond.Reason)
}

// latestEtcdBackupConditionMessage returns the message most likely to
// describe a terminal failure. The upstream operator's reconcile may
// stamp a housekeeping condition (Started, Uploading, ...) AFTER the
// failure landed, which would shadow the actual cause if we picked the
// latest by LastTransitionTime alone. Prefer a Failed-typed condition
// (or a Ready=False entry) so tenants see the upstream reason, falling
// back to the latest transition only when no failure-shaped condition
// exists.
func latestEtcdBackupConditionMessage(eb *etcdtypes.EtcdBackup) string {
	if eb == nil || len(eb.Status.Conditions) == 0 {
		return ""
	}
	var (
		latestFailure *metav1.Condition
		latestAny     *metav1.Condition
	)
	for i := range eb.Status.Conditions {
		c := &eb.Status.Conditions[i]
		if latestAny == nil || c.LastTransitionTime.After(latestAny.LastTransitionTime.Time) {
			latestAny = c
		}
		// Treat any condition whose Type spells failure, or whose
		// Reason starts with "Failed", or a Ready=False entry as a
		// failure-shaped condition. Belt-and-braces because the upstream
		// operator's exact spelling is not pinned by the typed wrapper.
		isFailure := strings.EqualFold(c.Type, "Failed") ||
			strings.HasPrefix(c.Reason, "Failed") ||
			(c.Type == etcdtypes.ClusterConditionReady && c.Status == metav1.ConditionFalse)
		if !isFailure {
			continue
		}
		if latestFailure == nil || c.LastTransitionTime.After(latestFailure.LastTransitionTime.Time) {
			latestFailure = c
		}
	}
	if latestFailure != nil {
		return latestFailure.Message
	}
	return latestAny.Message
}

// ensureEtcdBackup materialises a per-BackupJob EtcdBackup CR, or returns
// the existing one if a previous reconcile already created it.
// Idempotency relies on the OwningJob labels.
//
// IMPORTANT: the EtcdBackup CR is intentionally NOT linked back to the
// BackupJob via metav1.OwnerReference. Same reasoning as the FDB driver:
// a fresh BackupJob with the same name (e.g. tenant `kubectl delete &&
// kubectl apply`) must find the prior operator CR by its OwningJob label
// and reuse it instead of leaking a duplicate Job. Adding an OwnerRef
// would make Kubernetes GC reap the operator CR with the parent
// BackupJob, defeating the reuse contract.
func (r *BackupJobReconciler) ensureEtcdBackup(
	ctx context.Context,
	j *backupsv1alpha1.BackupJob,
	rendered *strategyv1alpha1.EtcdTemplate,
) (*etcdtypes.EtcdBackup, error) {
	existing, err := r.findEtcdBackupForJob(ctx, j)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	dest, err := strategyToEtcdBackupDestination(rendered.Destination)
	if err != nil {
		return nil, err
	}

	obj := &etcdtypes.EtcdBackup{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:    j.Namespace,
			GenerateName: fmt.Sprintf("%s-", j.Name),
			Labels: map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      j.Name,
				backupsv1alpha1.OwningJobNamespaceLabel: j.Namespace,
			},
		},
		Spec: etcdtypes.EtcdBackupSpec{
			ClusterRef:  etcdtypes.EtcdLocalObjectReference{Name: etcdClusterName},
			Destination: dest,
		},
	}
	if err := r.Create(ctx, obj); err != nil {
		return nil, err
	}
	return obj, nil
}

// findEtcdBackupForJob returns the EtcdBackup labelled with the
// BackupJob's OwningJob{Name,Namespace}, if any.
func (r *BackupJobReconciler) findEtcdBackupForJob(ctx context.Context, j *backupsv1alpha1.BackupJob) (*etcdtypes.EtcdBackup, error) {
	list := &etcdtypes.EtcdBackupList{}
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
		getLogger(ctx).Debug("multiple etcd.aenix.io/EtcdBackup CRs match BackupJob OwningJob labels; reusing first",
			"backupjob", j.Name, "namespace", j.Namespace, "matches", names, "picked", names[0])
	}
	return &list.Items[0], nil
}

// createEtcdBackupArtifact materialises a Cozystack Backup resource
// carrying the metadata callers need to drive a future restore. The
// rendered S3/PVC coordinates are persisted in
// Backup.status.underlyingResources so restore-time templating produces
// the same values the backup ran with even after the operator-side
// EtcdBackup CR has been pruned.
func (r *BackupJobReconciler) createEtcdBackupArtifact(
	ctx context.Context,
	j *backupsv1alpha1.BackupJob,
	resolved *ResolvedBackupConfig,
	eb *etcdtypes.EtcdBackup,
	rendered *strategyv1alpha1.EtcdTemplate,
) (*backupsv1alpha1.Backup, error) {
	takenAt := metav1.Now()

	driverMD := map[string]string{etcdBackupNameKey: eb.Name}
	if s := rendered.Destination.S3; s != nil {
		driverMD[etcdBackupBucketKey] = s.Bucket
		driverMD[etcdBackupEndpointKey] = s.Endpoint
		if s.Key != "" {
			driverMD[etcdBackupKeyKey] = s.Key
		}
		if s.Region != "" {
			driverMD[etcdBackupRegionKey] = s.Region
		}
		if s.ForcePathStyle != nil {
			driverMD[etcdBackupForcePathStyleKey] = fmt.Sprintf("%t", *s.ForcePathStyle)
		}
		driverMD[etcdBackupCredsSecretKey] = s.CredentialsSecretRef.Name
	}

	underlyingResources, err := marshalEtcdBackupSnapshot(rendered, resolved.Parameters)
	if err != nil {
		return nil, fmt.Errorf("encode source snapshot for Backup.status.underlyingResources: %w", err)
	}

	status := backupsv1alpha1.BackupStatus{
		Phase:               backupsv1alpha1.BackupPhaseReady,
		UnderlyingResources: underlyingResources,
	}
	// Pass through the upstream operator's reported snapshot location
	// + integrity hash. The backup-agent emits the final URI in a
	// terminal pod-log marker that the operator (v0.4.4+) parses into
	// EtcdBackup.status.snapshot — see upstream
	// internal/controller/etcdbackup_controller.go. The spec
	// destination alone is just the prefix; the agent appends the
	// backup-name (and any rev/timestamp suffix), so status.snapshot
	// is the authoritative final URI for human inspection.
	//
	// Defensive nil-check: pre-Complete reconciles and a forensic
	// downgrade to v0.4.3 (no snapshot field) leave Snapshot nil. In
	// that case we deliberately leave Backup.status.artifact unset
	// rather than synthesise a URI from the spec destination — the
	// spec destination is the prefix, not the final key, and emitting
	// a URI that points at a non-existent S3 object is worse than
	// emitting nothing. The buildEtcdRestoreS3Key helper handles
	// reconstruction at restore time for both cases.
	if s := eb.Status.Snapshot; s != nil && s.URI != "" {
		status.Artifact = &backupsv1alpha1.BackupArtifact{
			URI:       s.URI,
			SizeBytes: s.SizeBytes,
			Checksum:  s.Checksum,
		}
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
// RestoreJob path - in-place only (R2: suspend HR, snapshot live spec,
// delete + recreate EtcdCluster with bootstrap.restore, resume HR)
// ---------------------------------------------------------------------------

func (r *RestoreJobReconciler) reconcileEtcdRestore(ctx context.Context, restoreJob *backupsv1alpha1.RestoreJob, backup *backupsv1alpha1.Backup) (ctrl.Result, error) {
	logger := getLogger(ctx)
	logger.Debug("reconciling Etcd restore", "restorejob", restoreJob.Name, "backup", backup.Name)

	if err := validateEtcdApplicationRef(backup.Spec.ApplicationRef); err != nil {
		return r.markRestoreJobFailed(ctx, restoreJob, err.Error())
	}

	// To-copy isn't expressible for etcd. The PRIMARY constraint is at
	// the core API level: RestoreJob.spec.targetApplicationRef is a
	// TypedLocalObjectReference, which has no namespace field - the
	// restore target is always the SAME namespace as the source Backup,
	// so there is no API surface for a cross-namespace "fresh cluster
	// with the snapshot's data" workflow. The chart-level pin (Helm
	// release name fixed to "etcd" via
	// templates/check-release-name.yaml) is a SECONDARY constraint that
	// compounds the first: even with a hypothetical cross-namespace
	// TargetApplicationRef API, two Etcd apps could not coexist in one
	// namespace. Reject any RestoreJob that names a target different
	// from the Backup's source so tenants get a pointed message instead
	// of a silent fall-through into in-place.
	if t := restoreJob.Spec.TargetApplicationRef; t != nil {
		if t.Name != "" && t.Name != backup.Spec.ApplicationRef.Name {
			return r.markRestoreJobFailed(ctx, restoreJob,
				"Etcd driver does not support to-copy restore: RestoreJob.spec.targetApplicationRef is a same-namespace local reference, so a different target name in the same namespace cannot exist (and the chart pins release.name=\"etcd\", precluding two Etcd apps in one namespace regardless). Restore in-place by leaving spec.targetApplicationRef unset.")
		}
		if t.Kind != "" && t.Kind != etcdAppKind {
			return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf(
				"target applicationRef.kind=%q is not supported by the Etcd driver", t.Kind))
		}
		// Mirror validateEtcdApplicationRef's defence-in-depth on the
		// TARGET ref: without this gate a tenant could submit
		// {Kind: "Etcd", Name: <source>, APIGroup: "other.example.com"}
		// — the Name+Kind checks above accept (name matches the source,
		// Kind matches "Etcd"), resolveEtcdRestoreTarget then silently
		// drops the foreign APIGroup, and the restore runs against the
		// Cozystack Etcd app instead of whatever the tenant intended.
		if t.APIGroup != nil && *t.APIGroup != "" && *t.APIGroup != etcdapp.GroupName {
			return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf(
				"target applicationRef.apiGroup=%q is not supported by the Etcd driver (want %q)", *t.APIGroup, etcdapp.GroupName))
		}
	}

	target := r.resolveEtcdRestoreTarget(restoreJob, backup)

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
			return ctrl.Result{RequeueAfter: etcdPollInterval}, nil
		}
	}

	options, err := parseEtcdRestoreOptions(restoreJob.Spec.Options)
	if err != nil {
		r.Recorder.Eventf(restoreJob, corev1.EventTypeWarning, "MalformedOptions",
			"spec.options is not valid JSON: %v", err)
		return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf(
			"malformed restoreJob.spec.options: %v (clear the field or supply a valid EtcdRestoreOptions JSON object)", err))
	}

	// Resolve the snapshot destination from the persisted underlyingResources
	// (the authoritative source - durable beyond the operator EtcdBackup
	// CR's lifetime). Fall back to driverMetadata on decode failure unless
	// the snapshot identifies an unrecognised schema, in which case the
	// restore terminates - same contract as the FoundationDB driver.
	snap, err := decodeEtcdBackupSnapshot(backup.Status.UnderlyingResources)
	if err != nil {
		if errors.Is(err, errEtcdSnapshotUnrecognised) {
			return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf(
				"%v (re-take the backup with a controller version compatible with this snapshot schema, or clear Backup.status.underlyingResources to opt into the driverMetadata fallback)", err))
		}
		logger.Info("malformed Backup.status.underlyingResources; falling back to driverMetadata", "error", err)
		r.Recorder.Eventf(restoreJob, corev1.EventTypeWarning, "MalformedSnapshot",
			"Backup.status.underlyingResources is not a valid Etcd snapshot; falling back to driverMetadata: %v", err)
		snap = nil
	}
	dest, destOK := resolveEtcdRestoreDestination(backup, snap)
	if !destOK {
		return r.markRestoreJobFailed(ctx, restoreJob,
			"Backup driverMetadata/snapshot is missing the Etcd backup destination (re-take the backup with a controller version that persists it)")
	}

	// The driver must NOT race the source Etcd HelmRelease while the
	// purge/recreate window is open: helm-controller observing the live
	// EtcdCluster disappear would re-render the chart's bootstrap-less
	// EtcdCluster on the next sync and the operator would start a fresh
	// empty cluster, defeating the restore. Suspend the HR, hold it
	// suspended until the new EtcdCluster is Ready, then resume.
	hrName := target.AppName

	// First reconcile in the destructive window: snapshot the live spec
	// (via the dynamic client so all chart-rendered fields - replicas,
	// storage, security, options, podTemplate - are preserved instead of
	// being dropped by the typed-client subset projection), suspend HR,
	// delete the EtcdCluster.
	purged := apimeta.IsStatusConditionTrue(restoreJob.Status.Conditions, etcdRestoreCondTargetPurged)
	specCaptured := apimeta.IsStatusConditionTrue(restoreJob.Status.Conditions, etcdRestoreCondClusterSpecCaptured)
	if !purged {
		live, err := r.Resource(etcdClusterGVR).Namespace(target.Namespace).Get(ctx, etcdClusterName, metav1.GetOptions{})
		if err != nil {
			if !apierrors.IsNotFound(err) {
				return ctrl.Result{}, err
			}
			// NotFound disambiguation:
			//   - SpecCaptured already set: a previous iteration in this
			//     reconcile loop already deleted the EtcdCluster but
			//     the controller crashed (or got requeued) before
			//     stamping TargetPurged. Treat as "already deleted",
			//     advance the state machine.
			//   - SpecCaptured NOT set: the tenant never had a cluster
			//     to back up. Terminate the RestoreJob loudly.
			if specCaptured {
				apimeta.SetStatusCondition(&restoreJob.Status.Conditions, metav1.Condition{
					Type:    etcdRestoreCondTargetPurged,
					Status:  metav1.ConditionTrue,
					Reason:  "ClusterPurgedRecovered",
					Message: fmt.Sprintf("etcd.aenix.io/EtcdCluster %s/%s already gone on this iteration; advancing to recreate phase", target.Namespace, etcdClusterName),
				})
				if err := r.Status().Update(ctx, restoreJob); err != nil {
					return ctrl.Result{}, err
				}
				return ctrl.Result{RequeueAfter: etcdPollInterval}, nil
			}
			return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf(
				"etcd.aenix.io/EtcdCluster %s/%s not found; cannot derive the spec to re-create with bootstrap.restore (deploy the source Etcd application before requesting the restore)",
				target.Namespace, etcdClusterName))
		}

		// Persist the live spec on a status condition so a controller
		// crash between "snapshot taken" and "cluster recreated" can
		// recover without losing the chart-rendered spec to the empty
		// post-delete state. Skip when already persisted.
		if !specCaptured {
			liveSpec, _, _ := unstructured.NestedMap(live.Object, "spec")
			if len(liveSpec) == 0 {
				return r.markRestoreJobFailed(ctx, restoreJob,
					"live etcd.aenix.io/EtcdCluster has empty spec; refusing to restore against an unconfigured cluster")
			}
			specPayload, mErr := json.Marshal(liveSpec)
			if mErr != nil {
				return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf(
					"encode live EtcdCluster spec: %v", mErr))
			}
			// Fail fast BEFORE any destructive step if the spec would
			// overflow the Condition.Message cap. Hitting the cap after
			// the cluster is deleted would leave the RestoreJob stuck
			// with the spec lost.
			if len(specPayload) > etcdRestoreCapturedSpecMaxBytes {
				return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf(
					"live etcd.aenix.io/EtcdCluster spec is %d bytes which exceeds the %d-byte limit the driver can durably capture before the destructive purge; trim podTemplate / topologySpreadConstraints customisation on the source app or open an issue for a ConfigMap-backed capture path",
					len(specPayload), etcdRestoreCapturedSpecMaxBytes))
			}
			apimeta.SetStatusCondition(&restoreJob.Status.Conditions, metav1.Condition{
				Type:    etcdRestoreCondClusterSpecCaptured,
				Status:  metav1.ConditionTrue,
				Reason:  "SpecCaptured",
				Message: string(specPayload),
			})
			if err := r.Status().Update(ctx, restoreJob); err != nil {
				return ctrl.Result{}, err
			}
			// Requeue so the next pass sees the persisted condition and
			// can proceed with the destructive flow on a stable basis.
			return ctrl.Result{RequeueAfter: etcdPollInterval}, nil
		}

		if err := r.setEtcdRestoreHRSuspended(ctx, target.Namespace, hrName, true); err != nil {
			return ctrl.Result{}, err
		}

		if err := r.Resource(etcdClusterGVR).Namespace(target.Namespace).Delete(ctx, etcdClusterName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return r.markEtcdRestoreFailedAndResumeHR(ctx, restoreJob, target.Namespace, hrName, fmt.Sprintf(
				"delete etcd.aenix.io/EtcdCluster %s/%s: %v", target.Namespace, etcdClusterName, err))
		}

		gone, err := r.etcdClusterFullyGone(ctx, target.Namespace)
		if err != nil {
			// Transient apiserver errors here would loop forever with HR
			// suspended (helm-controller frozen, no manual recovery
			// possible). Resume the HR before bubbling the err - the next
			// reconcile re-suspends if needed.
			_ = r.setEtcdRestoreHRSuspended(ctx, target.Namespace, hrName, false)
			return ctrl.Result{}, err
		}
		if !gone {
			return ctrl.Result{RequeueAfter: etcdPollInterval}, nil
		}

		apimeta.SetStatusCondition(&restoreJob.Status.Conditions, metav1.Condition{
			Type:    etcdRestoreCondTargetPurged,
			Status:  metav1.ConditionTrue,
			Reason:  "ClusterPurged",
			Message: fmt.Sprintf("etcd.aenix.io/EtcdCluster %s/%s and member PVCs are gone; recreating with bootstrap.restore", target.Namespace, etcdClusterName),
		})
		if err := r.Status().Update(ctx, restoreJob); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: etcdPollInterval}, nil
	}

	// Recreate phase: ensure a fresh EtcdCluster with bootstrap.restore
	// pointing at the snapshot. Idempotent: if a prior reconcile already
	// created it (or the operator already started bootstrapping), the
	// Get returns the live CR and we fall through to the Ready poll.
	if _, err := r.Resource(etcdClusterGVR).Namespace(target.Namespace).Get(ctx, etcdClusterName, metav1.GetOptions{}); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		specMap, specErr := readCapturedEtcdClusterSpec(restoreJob)
		if specErr != nil {
			return r.markEtcdRestoreFailedAndResumeHR(ctx, restoreJob, target.Namespace, hrName, fmt.Sprintf(
				"recover captured EtcdCluster spec: %v", specErr))
		}
		// Inject bootstrap.restore.source on top of the captured spec.
		bootstrap := map[string]interface{}{
			"restore": map[string]interface{}{
				"source": etcdBackupDestinationToUnstructured(dest),
			},
		}
		specMap["bootstrap"] = bootstrap

		obj := &unstructured.Unstructured{}
		obj.SetAPIVersion(etcdtypes.GroupVersion.String())
		obj.SetKind("EtcdCluster")
		obj.SetNamespace(target.Namespace)
		obj.SetName(etcdClusterName)
		if err := unstructured.SetNestedMap(obj.Object, specMap, "spec"); err != nil {
			return r.markEtcdRestoreFailedAndResumeHR(ctx, restoreJob, target.Namespace, hrName, fmt.Sprintf(
				"assemble bootstrap.restore spec: %v", err))
		}
		if _, err := r.Resource(etcdClusterGVR).Namespace(target.Namespace).Create(ctx, obj, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return ctrl.Result{}, fmt.Errorf("create EtcdCluster with bootstrap.restore: %w", err)
		}
		return ctrl.Result{RequeueAfter: etcdPollInterval}, nil
	}

	// For the Ready poll, the typed client is enough: we only read
	// status.conditions, which etcdtypes.EtcdClusterStatus exposes.
	live := &etcdtypes.EtcdCluster{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: target.Namespace, Name: etcdClusterName}, live); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		// Just-deleted race; requeue and let the next reconcile re-create.
		return ctrl.Result{RequeueAfter: etcdPollInterval}, nil
	}
	if !etcdClusterReady(live) {
		deadline := options.effectiveRestoreDeadline()
		if restoreJob.Status.StartedAt != nil && time.Since(restoreJob.Status.StartedAt.Time) > deadline {
			// This is the most likely production failure point - the
			// recreated EtcdCluster never becomes Ready within deadline
			// (e.g. snapshot download stuck, S3 creds wrong, PVC
			// provisioner slow). Resume the HR before terminal-failing
			// so helm-controller is not left frozen on the tenant's
			// Etcd app.
			return r.markEtcdRestoreFailedAndResumeHR(ctx, restoreJob, target.Namespace, hrName, fmt.Sprintf(
				"etcd.aenix.io/EtcdCluster %s/%s did not reach Ready within %s after recreation with bootstrap.restore (override via spec.options.restoreTimeoutSeconds)",
				target.Namespace, etcdClusterName, deadline))
		}
		apimeta.SetStatusCondition(&restoreJob.Status.Conditions, metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionFalse,
			Reason:  "EtcdClusterBootstrapping",
			Message: fmt.Sprintf("waiting for etcd.aenix.io/EtcdCluster %s/%s to reach Ready after bootstrap.restore", target.Namespace, etcdClusterName),
		})
		if err := r.Status().Update(ctx, restoreJob); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: etcdPollInterval}, nil
	}

	// Cluster is Ready - resume the HR. Idempotent: a NotFound on the HR
	// (deleted between suspend and resume) is treated as success.
	if err := r.setEtcdRestoreHRSuspended(ctx, target.Namespace, hrName, false); err != nil {
		return ctrl.Result{}, err
	}

	now := metav1.Now()
	restoreJob.Status.CompletedAt = &now
	restoreJob.Status.Phase = backupsv1alpha1.RestoreJobPhaseSucceeded
	apimeta.SetStatusCondition(&restoreJob.Status.Conditions, metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionTrue,
		Reason:  "RestoreCompleted",
		Message: fmt.Sprintf("etcd.aenix.io/EtcdCluster %s/%s reached Ready after bootstrap.restore", target.Namespace, etcdClusterName),
	})
	if err := r.Status().Update(ctx, restoreJob); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

const (
	// etcdRestoreCondClusterSpecCaptured carries the JSON-encoded live
	// EtcdCluster spec as its Message, so a controller crash between
	// "snapshot taken" and "cluster recreated" recovers without losing
	// the chart-rendered spec to the empty post-delete state. Mirrors
	// the CNPG driver's TargetPurged pattern; reusing a Condition for
	// the payload sidesteps the need for a new CRD field on RestoreJob.
	// The size cap (etcdRestoreCapturedSpecMaxBytes) is enforced BEFORE
	// the destructive purge so a spec that wouldn't survive the
	// 32 KiB Condition.Message limit terminates the RestoreJob loudly
	// instead of getting stuck mid-purge with the cluster gone.
	etcdRestoreCondClusterSpecCaptured = "EtcdClusterSpecCaptured"
)

// readCapturedEtcdClusterSpec returns the EtcdCluster spec persisted on
// the EtcdClusterSpecCaptured condition by the destructive-window step
// as an unstructured map. Errors when the condition is missing/empty or
// the payload doesn't round-trip; a missing capture is itself a
// programming error - the caller only reaches this path after a
// TargetPurged condition was set, which requires the capture step to
// have written the message.
//
// Map-based instead of typed because etcdtypes.EtcdClusterSpec carries
// only the fields the driver actively mutates (currently Bootstrap), so
// a typed unmarshal would drop everything the chart populated (replicas,
// storage, security, options, podTemplate, ...) and the recreated
// EtcdCluster would be an empty shell.
func readCapturedEtcdClusterSpec(restoreJob *backupsv1alpha1.RestoreJob) (map[string]interface{}, error) {
	cond := apimeta.FindStatusCondition(restoreJob.Status.Conditions, etcdRestoreCondClusterSpecCaptured)
	if cond == nil || cond.Message == "" {
		return nil, errors.New("EtcdClusterSpecCaptured condition is missing or empty; cannot rebuild EtcdCluster spec")
	}
	specMap := map[string]interface{}{}
	if err := json.Unmarshal([]byte(cond.Message), &specMap); err != nil {
		return nil, fmt.Errorf("decode captured spec: %w", err)
	}
	if len(specMap) == 0 {
		return nil, errors.New("captured EtcdCluster spec is empty; refusing to recreate against an unconfigured cluster")
	}
	return specMap, nil
}

// s3KeyFromArtifactURI extracts the object key from an
// `s3://<bucket>/<key>` artifact URI when the bucket matches the
// expected destination bucket. The operator's backup-agent emits the
// FULL final key in this URI, including BACKUP_INCLUDE_REVISION /
// BACKUP_TIMESTAMP suffixes the agent appended at write time. When
// the URI is parseable and addresses the same bucket, the returned
// key is what the restore-agent needs in spec.bootstrap.restore.source
// .s3.key.
//
// Returns ok=false when the artifact is nil, the URI has a non-s3
// scheme (PVC destinations or future agents emitting "file://"), the
// host (bucket) doesn't match `expectedBucket`, or the path is empty.
// The bucket-match check is a safety belt: a future agent that emits
// a URI with the wrong bucket should not silently divert the restore.
func s3KeyFromArtifactURI(artifact *backupsv1alpha1.BackupArtifact, expectedBucket string) (string, bool) {
	if artifact == nil || artifact.URI == "" {
		return "", false
	}
	const scheme = "s3://"
	if !strings.HasPrefix(artifact.URI, scheme) {
		return "", false
	}
	rest := artifact.URI[len(scheme):]
	slash := strings.Index(rest, "/")
	if slash <= 0 || slash == len(rest)-1 {
		// No bucket/key separator, or trailing slash with empty key.
		return "", false
	}
	bucket := rest[:slash]
	key := rest[slash+1:]
	if bucket != expectedBucket {
		return "", false
	}
	return key, true
}

// buildEtcdRestoreS3Key mirrors the operator's backup-side filename
// convention (internal/controller/factory/backup_job.go) so the
// restore-agent's S3_KEY env points at the exact object the backup-
// agent wrote:
//
//	key == ""           => "<backupName>.db"
//	key trailing-slash  => "<prefix>/<backupName>.db"
//	otherwise           => "<key>/<backupName>.db"
//
// When backupName is empty (old artefact missing the driverMetadata
// label) we return the raw prefix verbatim - the restore will fail with
// a clear "downloaded snapshot is empty" message and the tenant knows
// to re-take the backup with the current controller version.
func buildEtcdRestoreS3Key(keyPrefix, backupName string) string {
	if backupName == "" {
		return keyPrefix
	}
	trimmed := strings.TrimRight(keyPrefix, "/")
	if trimmed == "" {
		return backupName + ".db"
	}
	return trimmed + "/" + backupName + ".db"
}

// etcdBackupDestinationToUnstructured renders an EtcdBackupDestination
// into the operator-side EtcdCluster.spec.bootstrap.restore.source shape
// as an unstructured map. The two CRDs share the same JSON shape for
// destinations, so this is a 1:1 field mapping with empty fields elided
// (the operator's MinLength validators reject zero-length bucket /
// endpoint / credentialsSecretRef.name; emitting them explicitly would
// turn a partial-config into a noisier admission error than the
// driver's own validateRenderedEtcdDestination already surfaces).
func etcdBackupDestinationToUnstructured(d etcdtypes.EtcdBackupDestination) map[string]interface{} {
	out := map[string]interface{}{}
	if s := d.S3; s != nil {
		s3 := map[string]interface{}{
			"bucket":   s.Bucket,
			"endpoint": s.Endpoint,
			"credentialsSecretRef": map[string]interface{}{
				"name": s.CredentialsSecretRef.Name,
			},
		}
		if s.Key != "" {
			s3["key"] = s.Key
		}
		if s.Region != "" {
			s3["region"] = s.Region
		}
		if s.ForcePathStyle != nil {
			s3["forcePathStyle"] = *s.ForcePathStyle
		}
		out["s3"] = s3
	}
	// PVC is intentionally not handled here: the strategy CR rejects
	// PVC destinations at admission time (see EtcdDestinationTemplate's
	// XValidation rule) because the operator's PVC backup/restore
	// paths are asymmetric and the resulting restore is unreachable.
	// d.PVC stays in the etcdtypes mirror for upstream-shape parity
	// but is unreachable from this driver.
	return out
}

// resolveEtcdRestoreDestination picks the destination block to stamp onto
// EtcdCluster.spec.bootstrap.restore.source. Resolution order:
//
//  1. Backup.status.artifact.uri (v0.4.4+) - the AUTHORITATIVE final S3
//     key written by the backup-agent, including any
//     BACKUP_INCLUDE_REVISION / BACKUP_TIMESTAMP suffix the agent
//     appended at write time. This is the only path that round-trips
//     correctly when the operator is configured to inject filename
//     suffixes.
//  2. Persisted snapshot (Backup.status.underlyingResources) - same
//     fields as the operator's destination, but the Key is the
//     prefix-only fragment the strategy rendered. Reconstruct the
//     filename via buildEtcdRestoreS3Key, which mirrors the operator's
//     default (suffix-less) convention. Used when the artifact URI is
//     unavailable (forensic downgrade, pre-Complete reconcile).
//  3. driverMetadata (oldest artifacts) - same suffix-less
//     reconstruction.
//
// For S3, the restore-agent reads S3_KEY verbatim from
// EtcdCluster.spec.bootstrap.restore.source.s3.key, so the value MUST
// be the full object key. Passing the prefix would 404 against the
// agent-written object; passing the agent's documented filename when
// the agent actually wrote a -rev<N>-suffixed file would also 404. The
// artifact-URI path sidesteps both by trusting the agent's emitted
// marker.
func resolveEtcdRestoreDestination(backup *backupsv1alpha1.Backup, snap *etcdBackupSnapshot) (etcdtypes.EtcdBackupDestination, bool) {
	if snap != nil && snap.Destination.S3 != nil {
		s := snap.Destination.S3
		fps := s.ForcePathStyle
		// Prefer the operator-reported artifact URI when present: it
		// carries the FULL final key including BACKUP_INCLUDE_REVISION
		// / BACKUP_TIMESTAMP suffixes. buildEtcdRestoreS3Key only
		// reproduces the suffix-less default; if the operator was
		// configured with BACKUP_INCLUDE_REVISION=true (as in v0.4.4
		// default), the reconstructed key would 404.
		key, ok := s3KeyFromArtifactURI(backup.Status.Artifact, s.Bucket)
		if !ok {
			key = buildEtcdRestoreS3Key(s.Key, backup.Spec.DriverMetadata[etcdBackupNameKey])
		}
		dest := etcdtypes.EtcdBackupDestination{
			S3: &etcdtypes.EtcdBackupS3{
				Bucket:               s.Bucket,
				Endpoint:             s.Endpoint,
				Key:                  key,
				Region:               s.Region,
				ForcePathStyle:       fps,
				CredentialsSecretRef: etcdtypes.EtcdLocalObjectReference{Name: s.CredentialsSecretRef.Name},
			},
		}
		return dest, true
	}
	// PVC destinations are intentionally not supported by this driver
	// - see EtcdDestinationTemplate's XValidation rule in etcd_types.go.
	// The upstream operator's PVC backup-side writes a path the
	// restore-side cannot read; until that's fixed upstream, a PVC
	// backup taken with this strategy would be unrestoreable. The CRD
	// admission gate ensures snap.Destination.PVC stays nil here.
	md := backup.Spec.DriverMetadata
	bucket := md[etcdBackupBucketKey]
	endpoint := md[etcdBackupEndpointKey]
	credsName := md[etcdBackupCredsSecretKey]
	if bucket == "" || endpoint == "" || credsName == "" {
		return etcdtypes.EtcdBackupDestination{}, false
	}
	var fps *bool
	if v, ok := md[etcdBackupForcePathStyleKey]; ok {
		b := v == "true"
		fps = &b
	}
	// Same artifact-URI preference as the snapshot path - so a backup
	// taken with BACKUP_INCLUDE_REVISION=true is restorable even when
	// the snapshot field has been wiped from Backup.status but the
	// artifact URI was preserved.
	key, ok := s3KeyFromArtifactURI(backup.Status.Artifact, bucket)
	if !ok {
		key = buildEtcdRestoreS3Key(md[etcdBackupKeyKey], md[etcdBackupNameKey])
	}
	dest := etcdtypes.EtcdBackupDestination{
		S3: &etcdtypes.EtcdBackupS3{
			Bucket:               bucket,
			Endpoint:             endpoint,
			Key:                  key,
			Region:               md[etcdBackupRegionKey],
			ForcePathStyle:       fps,
			CredentialsSecretRef: etcdtypes.EtcdLocalObjectReference{Name: credsName},
		},
	}
	return dest, true
}

// markEtcdRestoreFailedAndResumeHR is the terminal-failure exit used by
// every code path that runs AFTER setEtcdRestoreHRSuspended(true). It
// resumes the HelmRelease best-effort before marking the RestoreJob
// Failed, so a terminal failure during the destructive window does not
// leave the tenant's Etcd app with helm-controller frozen (no manual
// `kubectl apply` would reconcile until the HR is resumed by hand).
//
// For etcd we cannot do CNPG's "early resume after purge" pattern: the
// chart-rendered EtcdCluster has no bootstrap block, so resuming the
// HR before our recreate stamps bootstrap.restore would let
// helm-controller race a bootstrap-less render onto the purged
// namespace and the operator would start an empty cluster. The HR
// MUST stay suspended for the full destructive window; the only safe
// way to recover from a terminal failure mid-window is to resume the
// HR exactly here, on the way out.
//
// The resume err is intentionally swallowed: the RestoreJob is
// already failing, and any HR-side err is surfaced through
// helm-controller's own status, not the RestoreJob's. Idempotent: if
// the HR is already resumed the call is a no-op inside
// setEtcdRestoreHRSuspended.
func (r *RestoreJobReconciler) markEtcdRestoreFailedAndResumeHR(
	ctx context.Context,
	restoreJob *backupsv1alpha1.RestoreJob,
	namespace, hrName, message string,
) (ctrl.Result, error) {
	_ = r.setEtcdRestoreHRSuspended(ctx, namespace, hrName, false)
	return r.markRestoreJobFailed(ctx, restoreJob, message)
}

// setEtcdRestoreHRSuspended toggles spec.suspend on the target Etcd
// application's HelmRelease. The destructive flow needs the HR suspended
// before the driver deletes the live EtcdCluster - otherwise helm-
// controller observes the disappearance and re-renders the chart's
// bootstrap-less EtcdCluster on its next sync, defeating the restore.
// Resume only after the new EtcdCluster reaches Ready. Idempotent.
//
// NotFound is treated as success: a tenant who deletes the Etcd app
// while a RestoreJob is mid-flight has bigger problems than the
// finalizer; the controller should not get stuck on it.
func (r *RestoreJobReconciler) setEtcdRestoreHRSuspended(ctx context.Context, namespace, name string, suspend bool) error {
	hr, err := r.Resource(helmReleaseGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("get HelmRelease %s/%s: %w", namespace, name, err)
	}
	current, _, _ := unstructured.NestedBool(hr.Object, "spec", "suspend")
	if current == suspend {
		return nil
	}
	if err := unstructured.SetNestedField(hr.Object, suspend, "spec", "suspend"); err != nil {
		return fmt.Errorf("set spec.suspend on HelmRelease %s/%s: %w", namespace, name, err)
	}
	if _, err := r.Resource(helmReleaseGVR).Namespace(namespace).Update(ctx, hr, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update HelmRelease %s/%s: %w", namespace, name, err)
	}
	return nil
}

// etcdClusterFullyGone returns true when the chart-rendered EtcdCluster
// AND every PVC the etcd-operator generated for it have actually
// disappeared from the API. r.Delete returns when DeletionTimestamp
// lands but the operator's finalizers + storage drain take additional
// time - returning before that drain finishes lets the recreate step
// race the still-terminating cluster, and the new EtcdCluster Create
// fails with AlreadyExists or worse races on PVC name collision.
//
// The PVC label match mirrors the etcd-operator's StatefulSet-style
// volumeClaimTemplate behaviour: PVCs are named etcd-data-etcd-<i>
// and carry an app.kubernetes.io/instance=etcd label set by the
// operator.
func (r *RestoreJobReconciler) etcdClusterFullyGone(ctx context.Context, namespace string) (bool, error) {
	cluster := &etcdtypes.EtcdCluster{}
	switch err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: etcdClusterName}, cluster); {
	case err == nil:
		return false, nil
	case !apierrors.IsNotFound(err):
		return false, fmt.Errorf("get etcd.aenix.io/EtcdCluster %s/%s: %w", namespace, etcdClusterName, err)
	}
	pvcList := &corev1.PersistentVolumeClaimList{}
	if err := r.List(ctx, pvcList,
		client.InNamespace(namespace),
		client.MatchingLabels{"app.kubernetes.io/instance": etcdClusterName},
	); err != nil {
		return false, fmt.Errorf("list PVCs for cluster %s/%s: %w", namespace, etcdClusterName, err)
	}
	return len(pvcList.Items) == 0, nil
}

// etcdRestoreTarget captures the resolved target for an Etcd restore.
// In-place only - the to-copy path is rejected up-front in
// reconcileEtcdRestore because the chart's check-release-name.yaml
// pins exactly one Etcd release per namespace.
type etcdRestoreTarget struct {
	Namespace string
	AppName   string
}

func (r *RestoreJobReconciler) resolveEtcdRestoreTarget(restoreJob *backupsv1alpha1.RestoreJob, backup *backupsv1alpha1.Backup) etcdRestoreTarget {
	return etcdRestoreTarget{
		Namespace: backup.Namespace,
		AppName:   backup.Spec.ApplicationRef.Name,
	}
}

// ---------------------------------------------------------------------------
// Snapshot persisted on Cozystack Backup.status.underlyingResources
// ---------------------------------------------------------------------------

// etcdBackupSnapshot is the Etcd-specific payload persisted in
// Backup.status.underlyingResources at backup time. Carries the rendered
// destination and BackupClass parameters so a future RestoreJob can
// reproduce them exactly when the operator-side EtcdBackup CR has been
// pruned. The JSON tag for Destination matches the strategy CR's
// spec.template.destination so the on-disk shape is self-describing
// when an operator inspects the Backup artefact.
type etcdBackupSnapshot struct {
	Kind        string                                   `json:"kind"`
	APIVersion  string                                   `json:"apiVersion"`
	Destination strategyv1alpha1.EtcdDestinationTemplate `json:"destination"`
	Parameters  map[string]string                        `json:"parameters,omitempty"`
}

func marshalEtcdBackupSnapshot(
	rendered *strategyv1alpha1.EtcdTemplate,
	parameters map[string]string,
) (*runtime.RawExtension, error) {
	dest := rendered.Destination.DeepCopy()
	snap := etcdBackupSnapshot{
		Kind:        etcdBackupSnapshotKind,
		APIVersion:  etcdBackupSnapshotAPIVersion,
		Destination: *dest,
		Parameters:  parameters,
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		return nil, err
	}
	return &runtime.RawExtension{Raw: raw}, nil
}

// errEtcdSnapshotUnrecognised is returned when the payload at
// Backup.status.underlyingResources is parseable JSON but its
// self-typing fields (Kind, APIVersion) do not match the schema this
// driver understands. Same contract as the FoundationDB driver:
// future readers refuse such snapshots rather than silently fall back to
// driverMetadata, which on a real version-bump would corrupt the restore.
var errEtcdSnapshotUnrecognised = errors.New("Backup.status.underlyingResources carries a snapshot of an unrecognised kind/apiVersion")

func decodeEtcdBackupSnapshot(raw *runtime.RawExtension) (*etcdBackupSnapshot, error) {
	if raw == nil || len(raw.Raw) == 0 {
		return nil, nil
	}
	snap := &etcdBackupSnapshot{}
	if err := json.Unmarshal(raw.Raw, snap); err != nil {
		return nil, fmt.Errorf("decode Backup.status.underlyingResources: %w", err)
	}
	if snap.Kind != etcdBackupSnapshotKind {
		return nil, fmt.Errorf("%w: kind=%q (want %q)",
			errEtcdSnapshotUnrecognised, snap.Kind, etcdBackupSnapshotKind)
	}
	if snap.APIVersion != etcdBackupSnapshotAPIVersion {
		return nil, fmt.Errorf("%w: apiVersion=%q (want %q)",
			errEtcdSnapshotUnrecognised, snap.APIVersion, etcdBackupSnapshotAPIVersion)
	}
	return snap, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// renderEtcdTemplate templates the strategy template against a context
// containing the live application object and the BackupClass
// parameters. Reuses the same templating helper as the CNPG / MariaDB /
// Velero / FoundationDB strategies.
func renderEtcdTemplate(t strategyv1alpha1.EtcdTemplate, app *etcdapp.Etcd, parameters map[string]string) (*strategyv1alpha1.EtcdTemplate, error) {
	appAsMap, err := toJSONMapEtcd(app)
	if err != nil {
		return nil, fmt.Errorf("encode application for templating: %w", err)
	}
	templateContext := map[string]interface{}{
		"Application": appAsMap,
		"Parameters":  parameters,
	}
	return template.Template(&t, templateContext)
}

// toJSONMapEtcd converts a typed object to a generic map via JSON tags so
// user-authored go-templates address fields by their JSON names (e.g.
// .Application.metadata.name).
func toJSONMapEtcd(obj interface{}) (map[string]interface{}, error) {
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

// getEtcdApp fetches the apps.cozystack.io Etcd instance via the shared
// typed client. The Etcd scheme is registered in main.go so the
// controller-runtime cache serves it directly.
func (r *BackupJobReconciler) getEtcdApp(ctx context.Context, namespace, name string) (*etcdapp.Etcd, error) {
	app := &etcdapp.Etcd{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, app); err != nil {
		return nil, err
	}
	return app, nil
}

// validateRenderedEtcdDestination rejects destinations that the operator
// itself would reject at apply time, surfacing the failure as a clean
// terminal BackupJob status instead of letting the operator log a stack
// trace and the BackupJob spin until deadline.
//
// PVC destinations are not supported by this strategy (see the XValidation
// rule on EtcdDestinationTemplate in api/backups/strategy/v1alpha1/
// etcd_types.go): the upstream PVC backup-write path and PVC restore-read
// path use different filenames, so a PVC backup taken with this strategy
// would be unrestoreable. The CRD admission gate enforces "s3 is set" so
// this runtime path only ever validates the S3 fields. The defensive
// check on a nil S3 still lives here in case a tenant somehow bypasses
// admission (e.g. via an apiserver bug or an outdated CRD).
func validateRenderedEtcdDestination(d strategyv1alpha1.EtcdDestinationTemplate) error {
	if d.S3 == nil {
		return errors.New("destination.s3 is required (PVC destinations are not supported by this strategy)")
	}
	if d.S3.Bucket == "" {
		return errors.New("destination.s3.bucket is empty after templating")
	}
	if d.S3.Endpoint == "" {
		return errors.New("destination.s3.endpoint is empty after templating")
	}
	if d.S3.CredentialsSecretRef.Name == "" {
		return errors.New("destination.s3.credentialsSecretRef.name is empty after templating")
	}
	return nil
}

// strategyToEtcdBackupDestination converts the validated strategy-side
// destination template into the operator-side destination shape. The
// two types share JSON fields verbatim for S3, so this is a pure
// shape-cast. PVC is not produced - see validateRenderedEtcdDestination
// and the strategy CR's XValidation rule.
func strategyToEtcdBackupDestination(d strategyv1alpha1.EtcdDestinationTemplate) (etcdtypes.EtcdBackupDestination, error) {
	if err := validateRenderedEtcdDestination(d); err != nil {
		return etcdtypes.EtcdBackupDestination{}, err
	}
	var fps *bool
	if d.S3.ForcePathStyle != nil {
		v := *d.S3.ForcePathStyle
		fps = &v
	}
	return etcdtypes.EtcdBackupDestination{
		S3: &etcdtypes.EtcdBackupS3{
			Bucket:               d.S3.Bucket,
			Endpoint:             d.S3.Endpoint,
			Key:                  d.S3.Key,
			Region:               d.S3.Region,
			ForcePathStyle:       fps,
			CredentialsSecretRef: etcdtypes.EtcdLocalObjectReference{Name: d.S3.CredentialsSecretRef.Name},
		},
	}, nil
}

// EtcdRestoreOptions is the typed shape of RestoreJob.Spec.Options for
// the Etcd driver. Mirrors the FDB driver's RestoreOptions pattern so
// the boundary parses lazily and keeps behaviour permissive.
type EtcdRestoreOptions struct {
	// RestoreTimeoutSeconds caps the time the driver waits for the
	// re-created EtcdCluster to reach Ready before it marks the
	// RestoreJob Failed. Zero or unset falls back to
	// etcdDefaultRestoreDeadline.
	// +optional
	RestoreTimeoutSeconds int64 `json:"restoreTimeoutSeconds,omitempty"`
}

func parseEtcdRestoreOptions(opts *runtime.RawExtension) (EtcdRestoreOptions, error) {
	var out EtcdRestoreOptions
	if opts == nil || len(opts.Raw) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(opts.Raw, &out); err != nil {
		return EtcdRestoreOptions{}, fmt.Errorf("decode restoreJob.spec.options: %w", err)
	}
	return out, nil
}

func (o EtcdRestoreOptions) effectiveRestoreDeadline() time.Duration {
	if o.RestoreTimeoutSeconds <= 0 {
		return etcdDefaultRestoreDeadline
	}
	return time.Duration(o.RestoreTimeoutSeconds) * time.Second
}
