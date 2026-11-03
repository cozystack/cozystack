// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	velerov1 "github.com/vmware-tanzu/velero/pkg/apis/velero/v1"
	batchv1 "k8s.io/api/batch/v1"
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
	"sigs.k8s.io/yaml"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	"github.com/cozystack/cozystack/internal/template"
)

const (
	// kubernetesCondMode records on the RestoreJob whether it rebuilds a
	// deleted cluster or rolls a live one back (Reason), and which cluster
	// (Message). It lives in status, which the creator of a RestoreJob cannot
	// set, so a RestoreJob cannot claim a live cluster is gone.
	kubernetesCondMode          = "KubernetesRestoreMode"
	kubernetesRestorePhaseLabel = "kubernetes.strategy.backups.cozystack.io/restore-phase"

	// What a restore changes on a live cluster is recorded on the objects it
	// changes rather than on the RestoreJob, so the cluster can be put back
	// after the RestoreJob is deleted, and a retry after a failed restore
	// keeps the counts from before the first one rather than the zeros.
	kubernetesReplicasBeforeAnnotation = "kubernetes.strategy.backups.cozystack.io/replicas-before-restore"
	kubernetesSuspendedByAnnotation    = "kubernetes.strategy.backups.cozystack.io/suspended-by-restore"
	kubernetesReplicasUnset            = "unset"

	kubernetesRestoreModeRecreate = "recreate"
	kubernetesRestoreModeInPlace  = "in-place"

	kubernetesPhaseSecrets = "secrets"
	kubernetesPhaseVolumes = "volumes"

	machineExcludeDrainAnnotation        = "machine.cluster.x-k8s.io/exclude-node-draining"
	machineExcludeVolumeDetachAnnotation = "machine.cluster.x-k8s.io/exclude-wait-for-node-volume-detach"

	kubernetesCondVerified        = "KubernetesBackupVerified"
	kubernetesCondRecreated       = "KubernetesClusterRecreated"
	kubernetesCondStopped         = "KubernetesClusterStopped"
	kubernetesCondSecretsRestored = "KubernetesSecretsRestored"
	kubernetesCondEtcdRestored    = "KubernetesEtcdRestored"
	kubernetesCondVolumesRestored = "KubernetesVolumesRestored"
	// kubernetesCondEtcdWriteFailed marks a restore whose etcd Job failed
	// after it started replacing the prefix. The control plane is then left
	// stopped, by a failure and by the RestoreJob's deletion alike.
	kubernetesCondEtcdWriteFailed = "KubernetesEtcdWriteFailed"
	// kubernetesCondEtcdJobStarted records that a container of the restore
	// Job was seen running, which is the only trace left once the Job's
	// deadline has deleted its Pods.
	kubernetesCondEtcdJobStarted = "KubernetesEtcdJobStarted"

	// The restore Job writes this to its termination message just before it
	// deletes the prefix, so a failed Job tells whether the prefix is still
	// intact.
	kubernetesJobWriting = "writing"
)

// KubernetesRestoreOptions is the typed shape of RestoreJob.Spec.Options for
// the Kubernetes driver. Both halves of the backup are restored unless one is
// switched off.
type KubernetesRestoreOptions struct {
	// Etcd restores the cluster's etcd prefix (its API objects).
	Etcd *bool `json:"etcd,omitempty"`
	// Volumes restores the DataVolumes backing the cluster's PersistentVolumes.
	Volumes *bool `json:"volumes,omitempty"`
}

func (o KubernetesRestoreOptions) restoreEtcd() bool    { return boolDefault(o.Etcd, true) }
func (o KubernetesRestoreOptions) restoreVolumes() bool { return boolDefault(o.Volumes, true) }

func parseKubernetesRestoreOptions(opts *runtime.RawExtension) (KubernetesRestoreOptions, error) {
	var out KubernetesRestoreOptions
	if opts == nil || len(opts.Raw) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(opts.Raw, &out); err != nil {
		return KubernetesRestoreOptions{}, fmt.Errorf("decode restoreJob.spec.options: %w", err)
	}
	if !out.restoreEtcd() && !out.restoreVolumes() {
		return KubernetesRestoreOptions{}, fmt.Errorf("restoreJob.spec.options disables both etcd and volumes; nothing to restore")
	}
	return out, nil
}

type kubernetesRestoreState struct {
	Mode    string `json:"mode"`
	Release string `json:"release"`
}

func kubernetesAutoscalerName(release string) string {
	return release + "-cluster-autoscaler"
}

func kubernetesBackupDataVolumes(b *backupsv1alpha1.Backup) []string {
	raw := b.Spec.DriverMetadata[kubernetesDataVolumesMetaKey]
	if raw == "" {
		return nil
	}
	return strings.Split(raw, ",")
}

func kubernetesBackupHelmReleases(b *backupsv1alpha1.Backup) ([]map[string]any, error) {
	var out []map[string]any
	if err := json.Unmarshal([]byte(b.Spec.DriverMetadata[kubernetesHelmReleasesMetaKey]), &out); err != nil {
		return nil, fmt.Errorf("decode recorded HelmReleases: %w", err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("the Backup records no HelmReleases")
	}
	return out, nil
}

// kubernetesPreRestoreKey is where the restore Job keeps the prefix it is
// about to replace. The Job writes it only when none is there and deletes it
// once the restore succeeds, so after a failed restore it holds the prefix
// as it was before the first failed attempt, and a retry does not overwrite
// it with a partial one. It is fixed per backup, so the Backup's cleanup
// removes it.
func kubernetesPreRestoreKey(objectKey string) string {
	return objectKey + ".pre-restore"
}

// reconcileKubernetesRestore restores a tenant Kubernetes application in
// place, under its own name and namespace.
//
// When the application is gone, the restore rebuilds it: control-plane PKI
// Secrets first (the etcd Job authenticates with one of them), then the etcd
// prefix, the volumes, and finally the HelmReleases, so the control plane
// comes up on restored state and the existing CA.
//
// When the application is live, the restore stops what the selected halves
// need stopped — the workers for volumes, so the volumes are detached, then
// the control plane for etcd, so nothing writes while the prefix is replaced
// — writes the state back, and returns the cluster to the replica counts it
// had.
//
// Nothing is changed before the backup's artifacts are confirmed to exist.
func (r *RestoreJobReconciler) reconcileKubernetesRestore(ctx context.Context, rj *backupsv1alpha1.RestoreJob, backup *backupsv1alpha1.Backup) (ctrl.Result, error) {
	if rj.Status.Phase == backupsv1alpha1.RestoreJobPhaseSucceeded ||
		rj.Status.Phase == backupsv1alpha1.RestoreJobPhaseFailed {
		return ctrl.Result{}, nil
	}
	if err := validateKubernetesApplicationRef(backup.Spec.ApplicationRef); err != nil {
		return r.markRestoreJobFailed(ctx, rj, err.Error())
	}
	appName := backup.Spec.ApplicationRef.Name
	if t := rj.Spec.TargetApplicationRef; t != nil && ((t.Name != "" && t.Name != appName) || (t.Kind != "" && t.Kind != kubernetesAppKind)) {
		return r.markRestoreJobFailed(ctx, rj, "the Kubernetes strategy restores only into the application the backup was taken from")
	}
	if rj.Namespace != backup.Namespace {
		return r.markRestoreJobFailed(ctx, rj, "the Kubernetes strategy restores only into the namespace the backup was taken from")
	}
	opts, err := parseKubernetesRestoreOptions(rj.Spec.Options)
	if err != nil {
		return r.markRestoreJobFailed(ctx, rj, err.Error())
	}

	if rj.Status.StartedAt == nil {
		base := rj.DeepCopy()
		now := metav1.Now()
		rj.Status.StartedAt = &now
		rj.Status.Phase = backupsv1alpha1.RestoreJobPhaseRunning
		if err := r.Status().Patch(ctx, rj, client.MergeFrom(base)); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: kubernetesPollInterval}, nil
	}

	strategy := &strategyv1alpha1.Kubernetes{}
	if err := r.Get(ctx, client.ObjectKey{Name: backup.Spec.StrategyRef.Name}, strategy); err != nil {
		if apierrors.IsNotFound(err) {
			return r.requeueRestoreStrategyNotReady(ctx, rj, backup.Spec.StrategyRef.Name)
		}
		return ctrl.Result{}, err
	}

	if !apimeta.IsStatusConditionTrue(rj.Status.Conditions, kubernetesCondVerified) {
		done, msg, err := r.verifyKubernetesBackup(ctx, rj, backup, strategy, opts)
		if err != nil || msg != "" || !done {
			return r.kubernetesStepResult(ctx, rj, nil, err, msg, done)
		}
		return r.setKubernetesRestoreCondition(ctx, rj, kubernetesCondVerified, "the backup's artifacts are present")
	}

	state := kubernetesStateFromRestoreJob(rj)
	if state == nil {
		return r.captureKubernetesRestoreState(ctx, rj, backup)
	}

	if state.Mode == kubernetesRestoreModeInPlace {
		return r.reconcileKubernetesInPlace(ctx, rj, backup, strategy, opts, state)
	}
	return r.reconcileKubernetesRecreate(ctx, rj, backup, strategy, opts, state)
}

func kubernetesStateFromRestoreJob(rj *backupsv1alpha1.RestoreJob) *kubernetesRestoreState {
	c := apimeta.FindStatusCondition(rj.Status.Conditions, kubernetesCondMode)
	if c == nil {
		return nil
	}
	mode := kubernetesRestoreModeInPlace
	if c.Reason == "Recreate" {
		mode = kubernetesRestoreModeRecreate
	}
	return &kubernetesRestoreState{Mode: mode, Release: c.Message}
}

// verifyKubernetesBackup confirms, before anything is stopped or deleted,
// that the Velero backup completed and still exists, and that the etcd dump
// object is in the bucket. The dump is checked by a verify-mode run of the
// strategy Job, the only component holding the bucket credentials.
func (r *RestoreJobReconciler) verifyKubernetesBackup(ctx context.Context, rj *backupsv1alpha1.RestoreJob, backup *backupsv1alpha1.Backup, strategy *strategyv1alpha1.Kubernetes, opts KubernetesRestoreOptions) (bool, string, error) {
	name := backup.Spec.DriverMetadata[veleroBackupNameMetadataKey]
	if name == "" {
		return false, "the Backup records no Velero backup", nil
	}
	vb := &velerov1.Backup{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: veleroNamespace, Name: name}, vb); err != nil {
		if apierrors.IsNotFound(err) {
			return false, fmt.Sprintf("Velero backup %s/%s no longer exists", veleroNamespace, name), nil
		}
		return false, "", err
	}
	if vb.Status.Phase != velerov1.BackupPhaseCompleted {
		return false, fmt.Sprintf("Velero backup %s/%s is %q, not Completed", veleroNamespace, name, vb.Status.Phase), nil
	}
	if !opts.restoreEtcd() {
		return true, "", nil
	}
	return r.runKubernetesJob(ctx, rj, backup, strategy, kubernetesModeVerify, rj.Name+"-verify", kubernetesBackupDatastore(backup))
}

// captureKubernetesRestoreState decides between rebuilding a deleted cluster
// and restoring a live one.
func (r *RestoreJobReconciler) captureKubernetesRestoreState(ctx context.Context, rj *backupsv1alpha1.RestoreJob, backup *backupsv1alpha1.Backup) (ctrl.Result, error) {
	release := kubernetesReleaseName(backup.Spec.ApplicationRef.Name)
	reason := "Recreate"
	_, err := r.Resource(helmReleaseGVR).Namespace(rj.Namespace).Get(ctx, release, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		return ctrl.Result{}, err
	default:
		reason = "InPlace"
	}
	apimeta.SetStatusCondition(&rj.Status.Conditions, metav1.Condition{
		Type:    kubernetesCondMode,
		Status:  metav1.ConditionTrue,
		Reason:  reason,
		Message: release,
	})
	if err := r.Status().Update(ctx, rj); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: kubernetesPollInterval}, nil
}

func (r *RestoreJobReconciler) listClusterMachineDeployments(ctx context.Context, ns, release string) ([]unstructured.Unstructured, error) {
	list, err := r.Resource(machineDeploymentGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) || apimeta.IsNoMatchError(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []unstructured.Unstructured
	for _, md := range list.Items {
		if cluster, _, _ := unstructured.NestedString(md.Object, "spec", "clusterName"); cluster == release {
			out = append(out, md)
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Rebuild a deleted cluster
// ---------------------------------------------------------------------------

// reconcileKubernetesRecreate rebuilds a deleted cluster. The control-plane
// PKI and the volumes come back first, then the cluster's own HelmRelease.
// Kamaji gives the new TenantControlPlane a fresh datastore schema (keyed on
// its UID), so the etcd half can only be written once that schema exists:
// the restore then stops the new control plane like a live one, writes the
// dump into the new prefix, and starts it again. The worker pools are
// recreated last, so their workers join the restored cluster rather than an
// empty one.
func (r *RestoreJobReconciler) reconcileKubernetesRecreate(ctx context.Context, rj *backupsv1alpha1.RestoreJob, backup *backupsv1alpha1.Backup, strategy *strategyv1alpha1.Kubernetes, opts KubernetesRestoreOptions, state *kubernetesRestoreState) (ctrl.Result, error) {
	releases, err := kubernetesBackupHelmReleases(backup)
	if err != nil {
		return r.markRestoreJobFailed(ctx, rj, err.Error())
	}

	if !apimeta.IsStatusConditionTrue(rj.Status.Conditions, kubernetesCondSecretsRestored) {
		done, msg, err := r.runKubernetesVeleroRestore(ctx, rj, backup, strategy, kubernetesPhaseSecrets, state.Release)
		if err != nil || msg != "" || !done {
			return r.kubernetesStepResult(ctx, rj, nil, err, msg, done)
		}
		return r.setKubernetesRestoreCondition(ctx, rj, kubernetesCondSecretsRestored, "control-plane Secrets restored")
	}
	if opts.restoreVolumes() && !apimeta.IsStatusConditionTrue(rj.Status.Conditions, kubernetesCondVolumesRestored) {
		done, msg, err := r.runKubernetesVeleroRestore(ctx, rj, backup, strategy, kubernetesPhaseVolumes, state.Release)
		if err != nil || msg != "" || !done {
			return r.kubernetesStepResult(ctx, rj, nil, err, msg, done)
		}
		return r.setKubernetesRestoreCondition(ctx, rj, kubernetesCondVolumesRestored, "volumes restored")
	}

	if !opts.restoreEtcd() {
		if err := r.createKubernetesHelmReleases(ctx, rj.Namespace, releases); err != nil {
			return r.markRestoreJobFailed(ctx, rj, err.Error())
		}
		return r.markKubernetesRestoreSucceeded(ctx, rj, "cluster rebuilt from backup without its etcd state")
	}

	if !apimeta.IsStatusConditionTrue(rj.Status.Conditions, kubernetesCondRecreated) {
		if err := r.createKubernetesHelmReleases(ctx, rj.Namespace, releases[:1]); err != nil {
			return r.markRestoreJobFailed(ctx, rj, err.Error())
		}
		ready, err := r.tenantControlPlaneSchemaReady(ctx, rj.Namespace, state.Release)
		if err != nil {
			return ctrl.Result{}, err
		}
		if ready {
			return r.setKubernetesRestoreCondition(ctx, rj, kubernetesCondRecreated, "the control plane is up with a datastore schema")
		}
		apimeta.SetStatusCondition(&rj.Status.Conditions, metav1.Condition{
			Type:    kubernetesCondRecreated,
			Status:  metav1.ConditionFalse,
			Reason:  "Waiting",
			Message: "waiting for TenantControlPlane " + state.Release + " to report its datastore schema",
		})
		cond := apimeta.FindStatusCondition(rj.Status.Conditions, kubernetesCondRecreated)
		if time.Since(cond.LastTransitionTime.Time) > restorePreparationDeadline {
			return r.markRestoreJobFailed(ctx, rj, fmt.Sprintf("TenantControlPlane %s/%s reported no datastore schema within %s", rj.Namespace, state.Release, restorePreparationDeadline))
		}
		if err := r.Status().Update(ctx, rj); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: kubernetesPollInterval}, nil
	}

	return r.reconcileKubernetesInPlace(ctx, rj, backup, strategy, KubernetesRestoreOptions{Etcd: new(true), Volumes: new(false)}, state)
}

func (r *RestoreJobReconciler) tenantControlPlaneSchemaReady(ctx context.Context, ns, release string) (bool, error) {
	tcp, err := r.Resource(tenantControlPlaneGVR).Namespace(ns).Get(ctx, release, metav1.GetOptions{})
	if err != nil {
		return false, client.IgnoreNotFound(err)
	}
	schema, _, _ := unstructured.NestedString(tcp.Object, "status", "storage", "setup", "schema")
	return schema != "", nil
}

// createKubernetesHelmReleases recreates the recorded HelmReleases. A worker
// pool's HelmRelease outlives the deletion of its cluster (KubernetesNodes is
// an application of its own), but its MachineDeployment was garbage-collected
// with the CAPI Cluster, and nothing re-applies an unchanged release. One
// that exists is therefore forced through a Helm upgrade, which recreates
// the objects its release is missing.
func (r *RestoreJobReconciler) createKubernetesHelmReleases(ctx context.Context, ns string, releases []map[string]any) error {
	for _, hr := range releases {
		obj := &unstructured.Unstructured{Object: hr}
		obj.SetNamespace(ns)
		_, err := r.Resource(helmReleaseGVR).Namespace(ns).Create(ctx, obj, metav1.CreateOptions{})
		if err == nil {
			continue
		}
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("recreate HelmRelease %s/%s: %w", ns, obj.GetName(), err)
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		patch, merr := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]string{
			"reconcile.fluxcd.io/requestedAt": now,
			"reconcile.fluxcd.io/forceAt":     now,
		}}})
		if merr != nil {
			return merr
		}
		if _, err := r.Resource(helmReleaseGVR).Namespace(ns).Patch(ctx, obj.GetName(), types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
			return fmt.Errorf("force HelmRelease %s/%s to reconcile: %w", ns, obj.GetName(), err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Restore a live cluster
// ---------------------------------------------------------------------------

func (r *RestoreJobReconciler) reconcileKubernetesInPlace(ctx context.Context, rj *backupsv1alpha1.RestoreJob, backup *backupsv1alpha1.Backup, strategy *strategyv1alpha1.Kubernetes, opts KubernetesRestoreOptions, state *kubernetesRestoreState) (ctrl.Result, error) {
	if !apimeta.IsStatusConditionTrue(rj.Status.Conditions, kubernetesCondStopped) {
		stopped, waiting, err := r.stopKubernetesCluster(ctx, rj, backup, opts, state.Release)
		if err != nil {
			// Retried like any other wait, under the same deadline: a single
			// apiserver blip must not abort a restore.
			stopped, waiting = false, fmt.Sprintf("a retry after an API error: %v", err)
		}
		if stopped {
			return r.setKubernetesRestoreCondition(ctx, rj, kubernetesCondStopped, "cluster stopped for restore")
		}
		// The condition's transition time marks when stopping began: it is
		// kept while the status stays False, whatever the message says.
		apimeta.SetStatusCondition(&rj.Status.Conditions, metav1.Condition{
			Type:    kubernetesCondStopped,
			Status:  metav1.ConditionFalse,
			Reason:  "Stopping",
			Message: "waiting for " + waiting,
		})
		cond := apimeta.FindStatusCondition(rj.Status.Conditions, kubernetesCondStopped)
		if time.Since(cond.LastTransitionTime.Time) > restorePreparationDeadline {
			return r.failKubernetesInPlace(ctx, rj, state, fmt.Sprintf("the cluster did not stop within %s: still waiting for %s", restorePreparationDeadline, waiting))
		}
		if err := r.Status().Update(ctx, rj); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: kubernetesPollInterval}, nil
	}

	if res, next, err := r.kubernetesRestoreData(ctx, rj, backup, strategy, opts, state); !next {
		return res, err
	}

	if err := r.resumeKubernetesCluster(ctx, rj.Namespace, state.Release); err != nil {
		return ctrl.Result{}, err
	}
	if state.Mode == kubernetesRestoreModeRecreate {
		releases, err := kubernetesBackupHelmReleases(backup)
		if err != nil {
			return r.markRestoreJobFailed(ctx, rj, err.Error())
		}
		if err := r.createKubernetesHelmReleases(ctx, rj.Namespace, releases[1:]); err != nil {
			return r.markRestoreJobFailed(ctx, rj, err.Error())
		}
		return r.markKubernetesRestoreSucceeded(ctx, rj, "cluster rebuilt from backup")
	}
	return r.markKubernetesRestoreSucceeded(ctx, rj, "cluster restored in place")
}

// stopKubernetesCluster suspends the cluster's HelmReleases so Flux does not
// undo the scale-down, then stops the workers when volumes are restored —
// deleting the data DataVolumes and PVCs the backup holds once nothing mounts
// them — and the control plane when etcd is. Workers go first: CAPI removes a
// Machine's Node through the tenant API. It reports whether all of that has
// settled and, if not, what it is waiting for.
func (r *RestoreJobReconciler) stopKubernetesCluster(ctx context.Context, rj *backupsv1alpha1.RestoreJob, backup *backupsv1alpha1.Backup, opts KubernetesRestoreOptions, release string) (bool, string, error) {
	ns := rj.Namespace
	mds, err := r.listClusterMachineDeployments(ctx, ns, release)
	if err != nil {
		return false, "", err
	}
	for _, hr := range kubernetesHelmReleaseNames(mds, release) {
		if err := r.suspendHelmReleaseForRestore(ctx, ns, hr); err != nil {
			return false, "", err
		}
	}

	if opts.restoreVolumes() {
		if err := r.stopReplicas(ctx, deploymentGVR, ns, kubernetesAutoscalerName(release)); err != nil {
			return false, "", err
		}
		for _, md := range mds {
			if err := r.stopReplicas(ctx, machineDeploymentGVR, ns, md.GetName()); err != nil {
				return false, "", err
			}
		}
		remaining, err := r.releaseClusterMachines(ctx, ns, release)
		if err != nil {
			return false, "", err
		}
		if remaining > 0 {
			return false, fmt.Sprintf("%d worker Machine(s) to be deleted", remaining), nil
		}
		var pending []string
		for _, name := range kubernetesBackupDataVolumes(backup) {
			dvGone, err := r.deleteDataVolume(ctx, ns, name)
			if err != nil {
				return false, "", err
			}
			pvcGone, err := r.deletePVC(ctx, ns, name)
			if err != nil {
				return false, "", err
			}
			if !dvGone || !pvcGone {
				pending = append(pending, name)
			}
		}
		if len(pending) > 0 {
			return false, "data volumes to be deleted: " + strings.Join(pending, ", "), nil
		}
	}

	if opts.restoreEtcd() {
		if err := r.stopReplicas(ctx, kamajiControlPlaneGVR, ns, release); err != nil {
			return false, "", err
		}
		cp, err := r.Resource(deploymentGVR).Namespace(ns).Get(ctx, release, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
		case err != nil:
			return false, "", err
		default:
			if n, _, _ := unstructured.NestedInt64(cp.Object, "status", "replicas"); n > 0 {
				return false, fmt.Sprintf("%d control-plane Pod(s) to stop", n), nil
			}
			// status.replicas leaves out Pods that are still terminating, and
			// a terminating kube-apiserver can still write to etcd.
			selector, _, _ := unstructured.NestedStringMap(cp.Object, "spec", "selector", "matchLabels")
			if len(selector) > 0 {
				pods := &corev1.PodList{}
				if err := r.List(ctx, pods, client.InNamespace(ns), client.MatchingLabels(selector)); err != nil {
					return false, "", err
				}
				if len(pods.Items) > 0 {
					return false, fmt.Sprintf("%d control-plane Pod(s) to terminate", len(pods.Items)), nil
				}
			}
		}
	}
	return true, "", nil
}

// releaseClusterMachines lets the cluster's Machines finish deleting without
// draining their Nodes or waiting for volumes to detach: the workers are
// recreated after the restore, and a drain can block on a PodDisruptionBudget.
// It returns how many Machines remain.
func (r *RestoreJobReconciler) releaseClusterMachines(ctx context.Context, ns, release string) (int, error) {
	machines, err := r.Resource(machineGVR).Namespace(ns).List(ctx, metav1.ListOptions{LabelSelector: clusterNameLabel + "=" + release})
	if err != nil {
		if apierrors.IsNotFound(err) || apimeta.IsNoMatchError(err) {
			return 0, nil
		}
		return 0, err
	}
	patch := []byte(`{"metadata":{"annotations":{"` + machineExcludeDrainAnnotation + `":"","` + machineExcludeVolumeDetachAnnotation + `":""}}}`)
	for _, m := range machines.Items {
		ann := m.GetAnnotations()
		if _, ok := ann[machineExcludeDrainAnnotation]; ok {
			if _, ok := ann[machineExcludeVolumeDetachAnnotation]; ok {
				continue
			}
		}
		if _, err := r.Resource(machineGVR).Namespace(ns).Patch(ctx, m.GetName(), types.MergePatchType, patch, metav1.PatchOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return 0, err
		}
	}
	return len(machines.Items), nil
}

// suspendHelmReleaseForRestore suspends a HelmRelease and marks it as
// suspended by the restore. One already suspended is left alone and unmarked,
// so resuming the cluster does not resume it.
func (r *RestoreJobReconciler) suspendHelmReleaseForRestore(ctx context.Context, ns, name string) error {
	hr, err := r.Resource(helmReleaseGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return client.IgnoreNotFound(err)
	}
	if suspended, _, _ := unstructured.NestedBool(hr.Object, "spec", "suspend"); suspended {
		return nil
	}
	patch := []byte(`{"metadata":{"annotations":{"` + kubernetesSuspendedByAnnotation + `":"true"}},"spec":{"suspend":true}}`)
	_, err = r.Resource(helmReleaseGVR).Namespace(ns).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	return client.IgnoreNotFound(err)
}

// stopReplicas scales an object to zero, first recording its replica count
// on it. A count already recorded is kept: it was taken before an earlier
// restore stopped the object.
func (r *RestoreJobReconciler) stopReplicas(ctx context.Context, gvr schema.GroupVersionResource, ns, name string) error {
	obj, err := r.Resource(gvr).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return client.IgnoreNotFound(err)
	}
	annotations := map[string]any{}
	if _, recorded := obj.GetAnnotations()[kubernetesReplicasBeforeAnnotation]; !recorded {
		before := kubernetesReplicasUnset
		if n, found, _ := unstructured.NestedInt64(obj.Object, "spec", "replicas"); found {
			before = strconv.FormatInt(n, 10)
		}
		annotations[kubernetesReplicasBeforeAnnotation] = before
	}
	patch, err := json.Marshal(map[string]any{
		"metadata": map[string]any{"annotations": annotations},
		"spec":     map[string]any{"replicas": 0},
	})
	if err != nil {
		return err
	}
	_, err = r.Resource(gvr).Namespace(ns).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	return client.IgnoreNotFound(err)
}

// resumeReplicas puts back the replica count stopReplicas recorded — removing
// the field when it was unset, so its owner (the autoscaler, for a
// MachineDeployment) takes it back — and drops the record.
func (r *RestoreJobReconciler) resumeReplicas(ctx context.Context, gvr schema.GroupVersionResource, ns, name string) error {
	obj, err := r.Resource(gvr).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return client.IgnoreNotFound(err)
	}
	before, recorded := obj.GetAnnotations()[kubernetesReplicasBeforeAnnotation]
	if !recorded {
		return nil
	}
	var replicas any
	if before != kubernetesReplicasUnset {
		n, err := strconv.ParseInt(before, 10, 64)
		if err != nil {
			return fmt.Errorf("%s %s/%s: bad %s %q", gvr.Resource, ns, name, kubernetesReplicasBeforeAnnotation, before)
		}
		replicas = n
	}
	patch, err := json.Marshal(map[string]any{
		"metadata": map[string]any{"annotations": map[string]any{kubernetesReplicasBeforeAnnotation: nil}},
		"spec":     map[string]any{"replicas": replicas},
	})
	if err != nil {
		return err
	}
	_, err = r.Resource(gvr).Namespace(ns).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	return client.IgnoreNotFound(err)
}

// resumeKubernetesCluster undoes stopKubernetesCluster from the records on
// the objects themselves, so it needs nothing from the RestoreJob and is safe
// to call more than once.
func (r *RestoreJobReconciler) resumeKubernetesCluster(ctx context.Context, ns, release string) error {
	var errs []string
	mds, err := r.listClusterMachineDeployments(ctx, ns, release)
	if err != nil {
		return err
	}
	if err := r.resumeReplicas(ctx, kamajiControlPlaneGVR, ns, release); err != nil {
		errs = append(errs, err.Error())
	}
	for _, md := range mds {
		if err := r.resumeReplicas(ctx, machineDeploymentGVR, ns, md.GetName()); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if err := r.resumeReplicas(ctx, deploymentGVR, ns, kubernetesAutoscalerName(release)); err != nil {
		errs = append(errs, err.Error())
	}
	for _, name := range kubernetesHelmReleaseNames(mds, release) {
		hr, err := r.Resource(helmReleaseGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if !apierrors.IsNotFound(err) {
				errs = append(errs, err.Error())
			}
			continue
		}
		if _, marked := hr.GetAnnotations()[kubernetesSuspendedByAnnotation]; !marked {
			continue
		}
		patch := []byte(`{"metadata":{"annotations":{"` + kubernetesSuspendedByAnnotation + `":null}},"spec":{"suspend":false}}`)
		if _, err := r.Resource(helmReleaseGVR).Namespace(ns).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil && !apierrors.IsNotFound(err) {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("resume cluster: %s", strings.Join(errs, "; "))
	}
	return nil
}

// failKubernetesInPlace fails a live-cluster restore. The cluster is resumed
// unless the etcd prefix may have been partly replaced: bringing a control
// plane up on that would serve a mix of old and restored objects, so it is
// left stopped for a retry or a manual repair.
func (r *RestoreJobReconciler) failKubernetesInPlace(ctx context.Context, rj *backupsv1alpha1.RestoreJob, state *kubernetesRestoreState, msg string) (ctrl.Result, error) {
	if apimeta.IsStatusConditionTrue(rj.Status.Conditions, kubernetesCondEtcdWriteFailed) {
		return r.markRestoreJobFailed(ctx, rj, msg)
	}
	if err := r.resumeKubernetesCluster(ctx, rj.Namespace, state.Release); err != nil {
		msg = fmt.Sprintf("%s; the cluster could not be fully resumed: %v", msg, err)
	}
	return r.markRestoreJobFailed(ctx, rj, msg)
}

// cleanupKubernetesRestore runs when a RestoreJob is deleted. A live cluster
// it stopped is resumed, unless its etcd prefix may be partly replaced, which
// is left for the next restore to repair and reported as a Warning event.
func (r *RestoreJobReconciler) cleanupKubernetesRestore(ctx context.Context, rj *backupsv1alpha1.RestoreJob) {
	state := kubernetesStateFromRestoreJob(rj)
	if state == nil || state.Release == "" || rj.Status.Phase == backupsv1alpha1.RestoreJobPhaseSucceeded {
		return
	}
	partial := apimeta.IsStatusConditionTrue(rj.Status.Conditions, kubernetesCondEtcdWriteFailed)
	// A restore Job still running may be rewriting the prefix right now;
	// one that failed after it started writing left it partial. Either way
	// the control plane must not come up on it. The Job is deleted so it
	// stops writing; the garbage collector would only get to it later.
	job := &batchv1.Job{}
	switch err := r.Get(ctx, client.ObjectKey{Namespace: rj.Namespace, Name: jobNameForRestoreJob(rj)}, job); {
	case err == nil && jobConditionState(job) != batchv1.JobComplete:
		wrote, werr := r.kubernetesJobReachedWrite(ctx, rj, job.Name)
		if werr != nil || wrote || jobConditionState(job) != batchv1.JobFailed {
			partial = true
		}
		policy := metav1.DeletePropagationBackground
		_ = r.Delete(ctx, job, &client.DeleteOptions{PropagationPolicy: &policy})
	case err != nil && !apierrors.IsNotFound(err):
		partial = true
	}
	if partial {
		if r.Recorder != nil {
			r.Recorder.Eventf(rj, corev1.EventTypeWarning, "ClusterLeftStopped",
				"%s/%s stays stopped: its etcd prefix may be partly restored; run a new restore from a Backup", rj.Namespace, state.Release)
		}
		return
	}
	if err := r.resumeKubernetesCluster(ctx, rj.Namespace, state.Release); err != nil && r.Recorder != nil {
		r.Recorder.Eventf(rj, corev1.EventTypeWarning, "CleanupFailed", "resume %s/%s: %v", rj.Namespace, state.Release, err)
	}
}

// ---------------------------------------------------------------------------
// Steps shared by both modes
// ---------------------------------------------------------------------------

// kubernetesRestoreData writes back the selected halves, etcd before volumes.
// next is true when both are done and the caller may move on; otherwise the
// returned result is the reconcile's.
func (r *RestoreJobReconciler) kubernetesRestoreData(ctx context.Context, rj *backupsv1alpha1.RestoreJob, backup *backupsv1alpha1.Backup, strategy *strategyv1alpha1.Kubernetes, opts KubernetesRestoreOptions, state *kubernetesRestoreState) (ctrl.Result, bool, error) {
	if opts.restoreEtcd() && !apimeta.IsStatusConditionTrue(rj.Status.Conditions, kubernetesCondEtcdRestored) {
		ds, err := resolveKubernetesDatastore(ctx, r.Interface, rj.Namespace, backup.Spec.ApplicationRef.Name)
		if err != nil {
			res, rerr := r.kubernetesStepResult(ctx, rj, state, nil, fmt.Sprintf("resolve the cluster's datastore: %v", err), false)
			return res, false, rerr
		}
		ds.SourcePrefix = backup.Spec.DriverMetadata[kubernetesDatastorePrefixMetaKey]
		done, msg, err := r.runKubernetesJob(ctx, rj, backup, strategy, kubernetesModeRestore, jobNameForRestoreJob(rj), *ds)
		if msg != "" && err == nil {
			wrote, werr := r.kubernetesJobReachedWrite(ctx, rj, jobNameForRestoreJob(rj))
			if werr != nil {
				return ctrl.Result{}, false, werr
			}
			if !wrote {
				res, rerr := r.kubernetesStepResult(ctx, rj, state, nil, "etcd restore failed before it changed the prefix: "+msg, false)
				return res, false, rerr
			}
			apimeta.SetStatusCondition(&rj.Status.Conditions, metav1.Condition{
				Type:    kubernetesCondEtcdWriteFailed,
				Status:  metav1.ConditionTrue,
				Reason:  "RestoreJobFailed",
				Message: msg,
			})
			msg = fmt.Sprintf("etcd restore failed: %s. The prefix may be partly restored, so the control plane is left stopped. "+
				"A copy of the prefix from before the first restore of this backup that did not finish is kept at %s in the backup bucket; run a new RestoreJob to retry",
				msg, kubernetesPreRestoreKey(backup.Spec.DriverMetadata[kubernetesObjectMetaKey]))
		}
		if err == nil && msg == "" && !done && !apimeta.IsStatusConditionTrue(rj.Status.Conditions, kubernetesCondEtcdJobStarted) {
			started, serr := r.kubernetesJobStarted(ctx, rj.Namespace, jobNameForRestoreJob(rj))
			if serr != nil {
				return ctrl.Result{}, false, serr
			}
			if started {
				res, cerr := r.setKubernetesRestoreCondition(ctx, rj, kubernetesCondEtcdJobStarted, "the etcd restore Job is running")
				return res, false, cerr
			}
		}
		if err != nil || msg != "" || !done {
			res, rerr := r.kubernetesStepResult(ctx, rj, state, err, msg, done)
			return res, false, rerr
		}
		res, err := r.setKubernetesRestoreCondition(ctx, rj, kubernetesCondEtcdRestored, "etcd prefix restored")
		return res, false, err
	}
	if opts.restoreVolumes() && !apimeta.IsStatusConditionTrue(rj.Status.Conditions, kubernetesCondVolumesRestored) {
		done, msg, err := r.runKubernetesVeleroRestore(ctx, rj, backup, strategy, kubernetesPhaseVolumes, kubernetesReleaseName(backup.Spec.ApplicationRef.Name))
		if err != nil || msg != "" || !done {
			res, rerr := r.kubernetesStepResult(ctx, rj, state, err, msg, done)
			return res, false, rerr
		}
		res, err := r.setKubernetesRestoreCondition(ctx, rj, kubernetesCondVolumesRestored, "volumes restored")
		return res, false, err
	}
	return ctrl.Result{}, true, nil
}

// kubernetesStepResult turns a step's outcome into the reconcile result: an
// API error is retried, a failure message fails the restore (resuming a live
// cluster first where that is safe), and an unfinished step is polled.
func (r *RestoreJobReconciler) kubernetesStepResult(ctx context.Context, rj *backupsv1alpha1.RestoreJob, state *kubernetesRestoreState, err error, failure string, done bool) (ctrl.Result, error) {
	switch {
	case err != nil:
		return ctrl.Result{}, err
	case failure != "":
		// A rebuild stops its new control plane for the etcd half the same
		// way a live restore does, so both put back whatever they stopped.
		if state != nil {
			return r.failKubernetesInPlace(ctx, rj, state, failure)
		}
		return r.markRestoreJobFailed(ctx, rj, failure)
	case !done:
		return ctrl.Result{RequeueAfter: kubernetesPollInterval}, nil
	}
	return ctrl.Result{}, nil
}

// kubernetesJobReachedWrite reports whether any Pod of the restore Job may
// have changed the prefix: one running now, or one that terminated with the
// marker the script writes just before it deletes the prefix. A container
// that never ran (an image it could not pull) wrote nothing. When the Job's
// Pods are gone (its deadline deletes them), the answer is whether any of
// them was ever seen running.
func (r *RestoreJobReconciler) kubernetesJobReachedWrite(ctx context.Context, rj *backupsv1alpha1.RestoreJob, jobName string) (bool, error) {
	pods := &corev1.PodList{}
	if err := r.List(ctx, pods, client.InNamespace(rj.Namespace), client.MatchingLabels{"job-name": jobName}); err != nil {
		return false, err
	}
	if len(pods.Items) == 0 {
		return apimeta.IsStatusConditionTrue(rj.Status.Conditions, kubernetesCondEtcdJobStarted), nil
	}
	for _, p := range pods.Items {
		for _, cs := range p.Status.ContainerStatuses {
			switch {
			case cs.State.Running != nil:
				return true, nil
			case cs.State.Terminated != nil:
				if strings.TrimSpace(cs.State.Terminated.Message) == kubernetesJobWriting {
					return true, nil
				}
			case cs.LastTerminationState.Terminated != nil:
				if strings.TrimSpace(cs.LastTerminationState.Terminated.Message) == kubernetesJobWriting {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

// kubernetesJobStarted reports whether a container of the restore Job has
// run, which is what kubernetesJobReachedWrite falls back on once the Job's
// Pods are gone.
func (r *RestoreJobReconciler) kubernetesJobStarted(ctx context.Context, ns, jobName string) (bool, error) {
	pods := &corev1.PodList{}
	if err := r.List(ctx, pods, client.InNamespace(ns), client.MatchingLabels{"job-name": jobName}); err != nil {
		return false, err
	}
	for _, p := range pods.Items {
		for _, cs := range p.Status.ContainerStatuses {
			if cs.State.Running != nil || cs.State.Terminated != nil || cs.LastTerminationState.Terminated != nil {
				return true, nil
			}
		}
	}
	return false, nil
}

// runKubernetesJob runs the strategy Job in the given mode under the given
// name against ds. It returns done once the Job completed, or a failure
// message once it failed.
func (r *RestoreJobReconciler) runKubernetesJob(ctx context.Context, rj *backupsv1alpha1.RestoreJob, backup *backupsv1alpha1.Backup, strategy *strategyv1alpha1.Kubernetes, mode, name string, ds kubernetesDatastore) (bool, string, error) {
	if ds.Prefix == "" || len(ds.Endpoints) == 0 {
		return false, "the Backup records no etcd datastore prefix or endpoints", nil
	}
	rendered, err := renderKubernetesTemplate(strategy.Spec.Template, backup.Spec.ApplicationRef.Name, rj.Namespace,
		mode, backup.Spec.DriverMetadata[kubernetesObjectMetaKey], ds, kubernetesStrategyParameters(backup))
	if err != nil {
		return false, fmt.Sprintf("failed to template Kubernetes strategy: %v", err), nil
	}
	job, err := ensureBackupBatchJob(ctx, r.Client, r.Scheme, rj, rj.Namespace, name,
		map[string]string{
			kubernetesLabelMode:                     mode,
			backupsv1alpha1.OwningJobNameLabel:      rj.Name,
			backupsv1alpha1.OwningJobNamespaceLabel: rj.Namespace,
		}, rendered)
	if err != nil {
		return false, "", err
	}
	switch jobConditionState(job) {
	case batchv1.JobComplete:
		return true, "", nil
	case batchv1.JobFailed:
		msg := jobFailureMessage(job)
		if msg == "" {
			msg = fmt.Sprintf("%s Job reported Failed", mode)
		}
		return false, msg, nil
	}
	return false, "", nil
}

// runKubernetesVeleroRestore restores one phase of the Velero backup:
// control-plane Secrets, or the cluster's data DataVolumes and PVCs. It
// returns done once the Velero Restore completed.
func (r *RestoreJobReconciler) runKubernetesVeleroRestore(ctx context.Context, rj *backupsv1alpha1.RestoreJob, backup *backupsv1alpha1.Backup, strategy *strategyv1alpha1.Kubernetes, phase, release string) (bool, string, error) {
	veleroBackup := backup.Spec.DriverMetadata[veleroBackupNameMetadataKey]
	if veleroBackup == "" {
		return false, "the Backup records no Velero backup", nil
	}
	labels := map[string]string{
		backupsv1alpha1.OwningJobNameLabel:      rj.Name,
		backupsv1alpha1.OwningJobNamespaceLabel: rj.Namespace,
		kubernetesRestorePhaseLabel:             phase,
	}
	restores := &velerov1.RestoreList{}
	if err := r.List(ctx, restores, client.InNamespace(veleroNamespace), client.MatchingLabels(labels)); err != nil {
		return false, "", err
	}
	if len(restores.Items) == 0 {
		spec, err := kubernetesVeleroRestoreSpec(strategy, backup, phase, release)
		if err != nil {
			return false, fmt.Sprintf("failed to build Velero Restore: %v", err), nil
		}
		spec.BackupName = veleroBackup
		cm, err := r.kubernetesResourceModifiers(ctx, rj, phase, labels)
		if err != nil {
			return false, "", err
		}
		spec.ResourceModifier = &corev1.TypedLocalObjectReference{APIGroup: new(""), Kind: "ConfigMap", Name: cm.Name}
		restore := &velerov1.Restore{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: fmt.Sprintf("%s.%s-%s-", rj.Namespace, rj.Name, phase),
				Namespace:    veleroNamespace,
				Labels:       labels,
			},
			Spec: *spec,
		}
		if err := r.Create(ctx, restore); err != nil {
			return false, "", err
		}
		if r.Recorder != nil {
			r.Recorder.Event(rj, corev1.EventTypeNormal, "VeleroRestoreCreated", fmt.Sprintf("Created Velero Restore %s/%s", veleroNamespace, restore.Name))
		}
		return false, "", nil
	}
	restore := &restores.Items[0]
	switch restore.Status.Phase {
	case velerov1.RestorePhaseCompleted:
		return true, "", nil
	case velerov1.RestorePhaseFailed, velerov1.RestorePhasePartiallyFailed, velerov1.RestorePhaseFailedValidation:
		msg := fmt.Sprintf("Velero Restore %s/%s finished %s", restore.Namespace, restore.Name, restore.Status.Phase)
		if restore.Status.FailureReason != "" {
			msg += ": " + restore.Status.FailureReason
		}
		return false, msg, nil
	}
	return false, "", nil
}

// kubernetesVeleroRestoreSpec starts from the strategy's restore spec and
// narrows it to one phase. The selectors mirror the ones the shipped backup
// spec uses, so a phase restores exactly its share of the backup.
func kubernetesVeleroRestoreSpec(strategy *strategyv1alpha1.Kubernetes, backup *backupsv1alpha1.Backup, phase, release string) (*velerov1.RestoreSpec, error) {
	spec := &velerov1.RestoreSpec{}
	if strategy.Spec.Velero.RestoreSpec != nil {
		rendered, err := template.Template(strategy.Spec.Velero.RestoreSpec, map[string]any{
			"Application": map[string]any{
				"metadata": map[string]any{"name": backup.Spec.ApplicationRef.Name, "namespace": backup.Namespace},
				"kind":     backup.Spec.ApplicationRef.Kind,
			},
			"Parameters": kubernetesStrategyParameters(backup),
		})
		if err != nil {
			return nil, err
		}
		spec = rendered
	}
	spec.IncludedNamespaces = []string{backup.Namespace}
	spec.LabelSelector = nil
	switch phase {
	case kubernetesPhaseSecrets:
		spec.IncludedResources = []string{"secrets"}
		spec.OrLabelSelectors = []*metav1.LabelSelector{
			{MatchLabels: map[string]string{kamajiNameLabel: release}},
			{MatchLabels: map[string]string{helmInstanceLabel: release}},
		}
	case kubernetesPhaseVolumes:
		spec.IncludedResources = []string{"persistentvolumeclaims", "datavolumes.cdi.kubevirt.io"}
		spec.OrLabelSelectors = []*metav1.LabelSelector{
			{MatchLabels: map[string]string{clusterNameLabel: release}},
		}
		spec.RestorePVs = new(true)
	default:
		return nil, fmt.Errorf("unknown restore phase %q", phase)
	}
	return spec, nil
}

// kubernetesResourceModifiers creates the ConfigMap of resource modifiers for
// a phase. Restored Secrets drop their ownerReferences, which point at
// objects of the deleted cluster and would let the garbage collector delete
// them again; restored PVCs and DataVolumes let CDI adopt the restored claim.
func (r *RestoreJobReconciler) kubernetesResourceModifiers(ctx context.Context, rj *backupsv1alpha1.RestoreJob, phase string, labels map[string]string) (*corev1.ConfigMap, error) {
	var rules []resourceModifierRule
	switch phase {
	case kubernetesPhaseSecrets:
		patch, err := marshalPatchData(map[string]any{"metadata": map[string]any{"ownerReferences": nil}})
		if err != nil {
			return nil, err
		}
		rules = append(rules, resourceModifierRule{
			Conditions:   resourceModifierConditions{GroupResource: "secrets", ResourceNameRegex: ".*", Namespaces: []string{rj.Namespace}},
			MergePatches: []mergePatch{{PatchData: patch}},
		})
	case kubernetesPhaseVolumes:
		patch, err := marshalPatchData(map[string]any{"metadata": map[string]any{"annotations": map[string]string{cdiAllowClaimAdoption: "true"}}})
		if err != nil {
			return nil, err
		}
		for _, gr := range []string{"persistentvolumeclaims", "datavolumes.cdi.kubevirt.io"} {
			rules = append(rules, resourceModifierRule{
				Conditions:   resourceModifierConditions{GroupResource: gr, ResourceNameRegex: ".*", Namespaces: []string{rj.Namespace}},
				MergePatches: []mergePatch{{PatchData: patch}},
			})
		}
	}
	data, err := yaml.Marshal(resourceModifiers{Version: "v1", ResourceModifierRules: rules})
	if err != nil {
		return nil, err
	}
	name := fmt.Sprintf("restore-modifiers-%s-%s-%s", rj.Namespace, rj.Name, phase)
	if len(name) > 253 {
		name = name[:253]
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: veleroNamespace, Labels: labels},
		Data:       map[string]string{"resource-modifier-rules.yaml": string(data)},
	}
	if err := r.Create(ctx, cm); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return nil, err
		}
		existing := &corev1.ConfigMap{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: veleroNamespace, Name: name}, existing); err != nil {
			return nil, err
		}
		existing.Data = cm.Data
		if err := r.Update(ctx, existing); err != nil {
			return nil, err
		}
		return existing, nil
	}
	return cm, nil
}

func (r *RestoreJobReconciler) setKubernetesRestoreCondition(ctx context.Context, rj *backupsv1alpha1.RestoreJob, condType, message string) (ctrl.Result, error) {
	apimeta.SetStatusCondition(&rj.Status.Conditions, metav1.Condition{
		Type:    condType,
		Status:  metav1.ConditionTrue,
		Reason:  "Done",
		Message: message,
	})
	if err := r.Status().Update(ctx, rj); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: kubernetesPollInterval}, nil
}

func (r *RestoreJobReconciler) markKubernetesRestoreSucceeded(ctx context.Context, rj *backupsv1alpha1.RestoreJob, message string) (ctrl.Result, error) {
	now := metav1.Now()
	rj.Status.CompletedAt = &now
	rj.Status.Phase = backupsv1alpha1.RestoreJobPhaseSucceeded
	apimeta.SetStatusCondition(&rj.Status.Conditions, metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionTrue,
		Reason:  "RestoreCompleted",
		Message: message,
	})
	return ctrl.Result{}, r.Status().Update(ctx, rj)
}
