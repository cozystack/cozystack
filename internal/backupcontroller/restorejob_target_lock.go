// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
)

// ConditionReasonConcurrentRestore is the Failed reason of a RestoreJob that
// was refused because another restore into the same application goes first.
const ConditionReasonConcurrentRestore = "ConcurrentRestore"

// restoreAhead returns the name of the first non-terminal RestoreJob in rj's
// namespace that writes into the same application as rj and goes before it,
// or "" when there is none.
//
// Two restores into one application must not interleave: each rewrites the
// target from its own Backup, and the CNPG driver cannot tell which of them
// produced the live Cluster when both run. The one that goes second fails
// instead of waiting, so the application is never restored a second time,
// later, by a job its author may no longer expect to run.
//
// A restore that has started goes before one that has not; between two that
// have not, the older goes first, by creation time and then by name. Creation
// times have one-second precision, so two jobs created in the same second are
// ordered by name. The order is the same from either side.
//
// The list comes from reader, which must not be the informer cache: a job can
// be reconciled before the cache holds a peer's StartedAt, see that peer as
// not started, and proceed alongside it. Backups are read through c.
func restoreAhead(ctx context.Context, reader client.Reader, c client.Client, rj *backupsv1alpha1.RestoreJob) (string, error) {
	target, ok, err := restoreJobTarget(ctx, c, rj)
	if err != nil || !ok {
		return "", err
	}
	list := &backupsv1alpha1.RestoreJobList{}
	if err := reader.List(ctx, list, client.InNamespace(rj.Namespace)); err != nil {
		return "", fmt.Errorf("list RestoreJobs: %w", err)
	}
	var first *backupsv1alpha1.RestoreJob
	for i := range list.Items {
		other := &list.Items[i]
		if other.Name == rj.Name {
			continue
		}
		if other.Status.Phase == backupsv1alpha1.RestoreJobPhaseSucceeded || other.Status.Phase == backupsv1alpha1.RestoreJobPhaseFailed {
			continue
		}
		if !other.DeletionTimestamp.IsZero() || !restoreGoesFirst(other, rj) {
			continue
		}
		otherTarget, ok, err := restoreJobTarget(ctx, c, other)
		if err != nil {
			return "", err
		}
		if ok && sameApplication(otherTarget, target) && (first == nil || restoreGoesFirst(other, first)) {
			first = other
		}
	}
	if first == nil {
		return "", nil
	}
	return first.Name, nil
}

func restoreJobStarted(rj *backupsv1alpha1.RestoreJob) bool {
	return rj.Status.StartedAt != nil || rj.Status.Phase == backupsv1alpha1.RestoreJobPhaseRunning
}

// restoreGoesFirst reports whether a runs before b; see restoreAhead.
func restoreGoesFirst(a, b *backupsv1alpha1.RestoreJob) bool {
	if sa, sb := restoreJobStarted(a), restoreJobStarted(b); sa != sb {
		return sa
	}
	if !a.CreationTimestamp.Equal(&b.CreationTimestamp) {
		return a.CreationTimestamp.Before(&b.CreationTimestamp)
	}
	return a.Name < b.Name
}
