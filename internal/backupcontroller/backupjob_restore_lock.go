// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
)

// RestoreInProgressRequeue is how often a BackupJob held by a restore checks
// again. Restores run for minutes, so there is nothing to gain from polling
// faster.
const RestoreInProgressRequeue = 30 * time.Second

// ConditionReasonRestoreInProgress is the Ready=False reason of a BackupJob
// held back because its application is being restored.
const ConditionReasonRestoreInProgress = "RestoreInProgress"

// activeRestoreTargeting returns the name of a non-terminal RestoreJob in
// namespace that writes into app, or "" when there is none.
//
// A backup must not start against an application a restore is rewriting. It
// would capture a half-restored state at best, and on the CNPG path it does
// worse: every BackupJob reconcile re-applies the Cluster's barman plugin, and
// one landing between the restore re-rendering the Cluster with its new
// serverName and the plugin being attached can put the restored cluster back
// on the source's WAL prefix, where its recovery fails with "Expected empty
// archive". Plan-created runs are the ones that hit this, since nothing else
// looks at restores before creating a BackupJob.
func activeRestoreTargeting(ctx context.Context, c client.Client, namespace string, app corev1.TypedLocalObjectReference) (string, error) {
	app = NormalizeApplicationRef(app)
	list := &backupsv1alpha1.RestoreJobList{}
	if err := c.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return "", fmt.Errorf("list RestoreJobs: %w", err)
	}
	var holding []string
	for i := range list.Items {
		rj := &list.Items[i]
		if rj.Status.Phase == backupsv1alpha1.RestoreJobPhaseSucceeded || rj.Status.Phase == backupsv1alpha1.RestoreJobPhaseFailed {
			continue
		}
		target, ok, err := restoreJobTarget(ctx, c, rj)
		if err != nil {
			return "", err
		}
		if ok && sameApplication(target, app) {
			holding = append(holding, rj.Name)
		}
	}
	if len(holding) == 0 {
		return "", nil
	}
	sort.Strings(holding)
	return holding[0], nil
}

// restoreJobTarget resolves the application a RestoreJob writes into in its
// own namespace, as the drivers resolve it (resolveCNPGRestoreTarget and the
// Job-based paths): the Backup's applicationRef, with the target's kind and
// name when a target is set — the CRD requires both — and the target's API
// group only when it names one. A target without an API group therefore stays
// in the Backup's group, which is not always the default one. The Velero
// driver drops the target's API group (resolveRestoreTarget) and restores the
// Backup's resources, so a Velero restore always stays in the Backup's group.
//
// It reports false for a restore that writes nothing here. One whose Backup is
// gone is failed by the restore controller on its next pass. A Velero restore
// whose options name another namespace writes there: only the Velero driver
// reads options.targetNamespace, so the option is looked at only when the
// Backup's strategy is Velero — the CNPG and Job-based drivers ignore it and
// restore in place.
func restoreJobTarget(ctx context.Context, c client.Client, rj *backupsv1alpha1.RestoreJob) (corev1.TypedLocalObjectReference, bool, error) {
	backup := &backupsv1alpha1.Backup{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: rj.Namespace, Name: rj.Spec.BackupRef.Name}, backup); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return corev1.TypedLocalObjectReference{}, false, nil
		}
		return corev1.TypedLocalObjectReference{}, false, fmt.Errorf("get Backup %s of RestoreJob %s: %w", rj.Spec.BackupRef.Name, rj.Name, err)
	}
	if backup.Spec.StrategyRef.Kind == strategyv1alpha1.VeleroStrategyKind && restoresIntoAnotherNamespace(rj, backup.Namespace) {
		return corev1.TypedLocalObjectReference{}, false, nil
	}
	ref := backup.Spec.ApplicationRef
	if t := rj.Spec.TargetApplicationRef; t != nil {
		ref.Kind, ref.Name = t.Kind, t.Name
		if t.APIGroup != nil && backup.Spec.StrategyRef.Kind != strategyv1alpha1.VeleroStrategyKind {
			ref.APIGroup = t.APIGroup
		}
	}
	return NormalizeApplicationRef(ref), true, nil
}

// restoresIntoAnotherNamespace reports whether a Velero RestoreJob's
// options.targetNamespace sends it away from namespace, compared as the Velero
// driver compares it. Options that do not parse are read as none.
func restoresIntoAnotherNamespace(rj *backupsv1alpha1.RestoreJob, namespace string) bool {
	if rj.Spec.Options == nil || len(rj.Spec.Options.Raw) == 0 {
		return false
	}
	var opts struct {
		TargetNamespace string `json:"targetNamespace"`
	}
	if err := json.Unmarshal(rj.Spec.Options.Raw, &opts); err != nil {
		return false
	}
	return opts.TargetNamespace != "" && opts.TargetNamespace != namespace
}

// sameApplication expects both references normalized: an API group left
// unset compares equal only once it has been defaulted on both sides.
func sameApplication(a, b corev1.TypedLocalObjectReference) bool {
	return a.Kind == b.Kind && a.Name == b.Name && ptrString(a.APIGroup) == ptrString(b.APIGroup)
}

func ptrString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// holdForRestore keeps a BackupJob that has not started yet waiting while a
// restore writes into its application. It stays non-terminal, so a scheduled
// run still happens once the restore ends, only later than its slot.
//
// A failed status write is returned so the manager retries it with backoff.
// The job is held either way, since nothing past the gate ran, but a hold
// nobody can see reads as a run stuck for no reason.
func (r *BackupJobReconciler) holdForRestore(ctx context.Context, j *backupsv1alpha1.BackupJob, restore string) (ctrl.Result, error) {
	if j.Status.Phase == backupsv1alpha1.BackupJobPhaseEmpty {
		j.Status.Phase = backupsv1alpha1.BackupJobPhasePending
	}
	meta.SetStatusCondition(&j.Status.Conditions, metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionFalse,
		Reason:  ConditionReasonRestoreInProgress,
		Message: fmt.Sprintf("waiting for RestoreJob %q to finish: application %s/%s is being restored", restore, j.Spec.ApplicationRef.Kind, j.Spec.ApplicationRef.Name),
	})
	if err := r.Status().Update(ctx, j); err != nil {
		return ctrl.Result{}, fmt.Errorf("record RestoreInProgress on BackupJob %s: %w", j.Name, err)
	}
	return ctrl.Result{RequeueAfter: RestoreInProgressRequeue}, nil
}

// releaseRestoreHold removes the condition holdForRestore left, once the
// restore has ended and the job goes on to start. Left in place it would read
// "waiting for RestoreJob" until the driver next writes Ready, which for a
// healthy run is when the backup completes.
func (r *BackupJobReconciler) releaseRestoreHold(ctx context.Context, j *backupsv1alpha1.BackupJob) error {
	cond := meta.FindStatusCondition(j.Status.Conditions, "Ready")
	if cond == nil || cond.Reason != ConditionReasonRestoreInProgress {
		return nil
	}
	meta.RemoveStatusCondition(&j.Status.Conditions, "Ready")
	if err := r.Status().Update(ctx, j); err != nil {
		return fmt.Errorf("clear RestoreInProgress on BackupJob %s: %w", j.Name, err)
	}
	return nil
}
