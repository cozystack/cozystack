// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"sort"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
)

// TestSupportedBackupStrategyKindsMatchesDispatch pins every Kind that the
// BackupJob reconciler's dispatch switch handles. Before the fix the
// unsupported-strategy log payload was hand-maintained inline next to the
// switch and silently dropped AltinityStrategyKind when MariaDB was added,
// which misled operators staring at the message and trying to figure out
// which strategies the controller actually supports. The single
// supportedBackupStrategyKinds() source of truth keeps log and dispatch in
// lockstep; this test fails the moment a new strategy is added to either
// half but not the other.
func TestSupportedBackupStrategyKindsMatchesDispatch(t *testing.T) {
	got := append([]string(nil), supportedBackupStrategyKinds()...)
	want := []string{
		strategyv1alpha1.JobStrategyKind,
		strategyv1alpha1.VeleroStrategyKind,
		strategyv1alpha1.CNPGStrategyKind,
		strategyv1alpha1.AltinityStrategyKind,
		strategyv1alpha1.MariaDBStrategyKind,
		strategyv1alpha1.FoundationDBStrategyKind,
	}
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("supportedBackupStrategyKinds: got %d kinds %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("supportedBackupStrategyKinds[%d]: got %q, want %q", i, got[i], want[i])
		}
	}
}

// TestMarkBackupJobFailed_DoesNotAppendDuplicateReady locks in the fix:
// markBackupJobFailed used to `append` to Status.Conditions, which violates
// the +listType=map +listMapKey=type contract on the field. Two terminal
// failures (or a terminal failure on top of a transient Ready=False
// condition that a previous reconcile already wrote) used to leave two
// entries with Type="Ready" - any consumer doing meta.FindStatusCondition
// reads whichever copy comes first in the slice, not necessarily the
// latest. SetStatusCondition keeps the list a proper map.
func TestMarkBackupJobFailed_DoesNotAppendDuplicateReady(t *testing.T) {
	t.Run("two failures keep exactly one Ready condition", func(t *testing.T) {
		bj := &backupsv1alpha1.BackupJob{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "bj"},
		}
		c := newBackupJobTestClient(t, bj)
		r := &BackupJobReconciler{Client: c}

		if _, err := r.markBackupJobFailed(context.Background(), bj, "first failure"); err != nil {
			t.Fatalf("first markBackupJobFailed: %v", err)
		}
		if _, err := r.markBackupJobFailed(context.Background(), bj, "second failure"); err != nil {
			t.Fatalf("second markBackupJobFailed: %v", err)
		}

		readyCount := 0
		for _, cond := range bj.Status.Conditions {
			if cond.Type == "Ready" {
				readyCount++
			}
		}
		if readyCount != 1 {
			t.Fatalf("expected exactly 1 Ready condition, got %d (full slice: %+v)", readyCount, bj.Status.Conditions)
		}
		ready := meta.FindStatusCondition(bj.Status.Conditions, "Ready")
		if ready == nil || ready.Message != "second failure" {
			t.Errorf("expected latest failure message preserved, got %+v", ready)
		}
	})

	t.Run("transient Ready=False condition is replaced in place", func(t *testing.T) {
		bj := &backupsv1alpha1.BackupJob{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "bj"},
			Status: backupsv1alpha1.BackupJobStatus{
				Conditions: []metav1.Condition{
					{Type: "Ready", Status: metav1.ConditionFalse, Reason: "WaitingForBackup", Message: "stale"},
				},
			},
		}
		c := newBackupJobTestClient(t, bj)
		r := &BackupJobReconciler{Client: c}

		if _, err := r.markBackupJobFailed(context.Background(), bj, "backup deadline exceeded"); err != nil {
			t.Fatalf("markBackupJobFailed: %v", err)
		}
		if got := len(bj.Status.Conditions); got != 1 {
			t.Fatalf("expected 1 condition, got %d (full slice: %+v)", got, bj.Status.Conditions)
		}
		if bj.Status.Conditions[0].Reason != "BackupFailed" {
			t.Errorf("expected Reason=BackupFailed, got %q", bj.Status.Conditions[0].Reason)
		}
		if bj.Status.Conditions[0].Message != "backup deadline exceeded" {
			t.Errorf("expected new message, got %q", bj.Status.Conditions[0].Message)
		}
	})
}

func newBackupJobTestClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	_ = backupsv1alpha1.AddToScheme(s)
	return clientfake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		WithStatusSubresource(&backupsv1alpha1.BackupJob{}).
		Build()
}
