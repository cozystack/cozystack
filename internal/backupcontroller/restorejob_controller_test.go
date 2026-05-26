// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	velerov1 "github.com/vmware-tanzu/velero/pkg/apis/velero/v1"
)

// TestStrategyKindForRestoreJob asserts the dispatcher correctly identifies
// the strategy that produced a RestoreJob's side state. Locks in the bug
// fix: previously the cleanup ran Velero-only deletion regardless of the
// RestoreJob's actual strategy, papered over by the fact that DeleteAllOf
// is a no-op when nothing matches the label selector.
func TestStrategyKindForRestoreJob(t *testing.T) {
	apiGroup := strategyv1alpha1.GroupVersion.Group
	cases := []struct {
		name       string
		backupKind string
		want       string
	}{
		{"velero strategy", strategyv1alpha1.VeleroStrategyKind, strategyv1alpha1.VeleroStrategyKind},
		{"cnpg strategy", strategyv1alpha1.CNPGStrategyKind, strategyv1alpha1.CNPGStrategyKind},
		{"job strategy", strategyv1alpha1.JobStrategyKind, strategyv1alpha1.JobStrategyKind},
		{"mariadb strategy", strategyv1alpha1.MariaDBStrategyKind, strategyv1alpha1.MariaDBStrategyKind},
		{"etcd strategy", strategyv1alpha1.EtcdStrategyKind, strategyv1alpha1.EtcdStrategyKind},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rj := &backupsv1alpha1.RestoreJob{
				ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "rj"},
				Spec: backupsv1alpha1.RestoreJobSpec{
					BackupRef: corev1.LocalObjectReference{Name: "bk"},
				},
			}
			backup := &backupsv1alpha1.Backup{
				ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "bk"},
				Spec: backupsv1alpha1.BackupSpec{
					StrategyRef: corev1.TypedLocalObjectReference{
						APIGroup: &apiGroup,
						Kind:     tc.backupKind,
						Name:     "strategy",
					},
				},
			}
			c := newRestoreJobTestClient(t, rj, backup)
			got, err := strategyKindForRestoreJob(context.Background(), c, rj)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}

	t.Run("missing backup yields empty kind and an error", func(t *testing.T) {
		rj := &backupsv1alpha1.RestoreJob{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "rj"},
			Spec: backupsv1alpha1.RestoreJobSpec{
				BackupRef: corev1.LocalObjectReference{Name: "missing"},
			},
		}
		c := newRestoreJobTestClient(t, rj)
		got, err := strategyKindForRestoreJob(context.Background(), c, rj)
		if got != "" {
			t.Errorf("expected empty string for missing backup, got %q", got)
		}
		if err == nil {
			t.Errorf("expected an error for missing backup, got nil")
		}
	})
}

// TestCleanupOnDelete_CNPG_DoesNotTouchVeleroNamespace asserts the dispatch
// branch: a CNPG RestoreJob's deletion must not issue DeleteAllOf against
// cozy-velero. We seed a Velero Restore in cozy-velero that *would* match
// the CNPG RestoreJob's labels - so if the dispatcher mistakenly fell into
// the Velero cleanup branch, DeleteAllOf would reap it. The previous
// regression test seeded a different-owner stray, which left it alone
// regardless of which cleanup branch ran (DeleteAllOf with a label selector
// is a no-op against non-matching objects), so it passed even on the broken
// implementation.
func TestCleanupOnDelete_CNPG_DoesNotTouchVeleroNamespace(t *testing.T) {
	apiGroup := strategyv1alpha1.GroupVersion.Group
	rj := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "rj"},
		Spec:       backupsv1alpha1.RestoreJobSpec{BackupRef: corev1.LocalObjectReference{Name: "bk"}},
	}
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bk"},
		Spec: backupsv1alpha1.BackupSpec{
			StrategyRef: corev1.TypedLocalObjectReference{
				APIGroup: &apiGroup, Kind: strategyv1alpha1.CNPGStrategyKind, Name: "s",
			},
		},
	}
	// Pre-seed a Velero Restore that *matches* the CNPG RestoreJob's owner
	// labels. CNPG dispatch must not enter cleanupVeleroRestore, so this
	// object must survive. If the dispatcher regresses to the old
	// unconditional Velero cleanup, DeleteAllOf will reap it and the test
	// will fail.
	owned := &velerov1.Restore{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: veleroNamespace,
			Name:      "would-be-victim",
			Labels: map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      "rj",
				backupsv1alpha1.OwningJobNamespaceLabel: "tenant",
			},
		},
	}
	c := newRestoreJobTestClient(t, rj, backup, owned)
	r := &RestoreJobReconciler{Client: c, Recorder: record.NewFakeRecorder(10)}

	r.cleanupOnDelete(context.Background(), rj)

	got := &velerov1.Restore{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(owned), got); err != nil {
		t.Fatalf("CNPG cleanup incorrectly routed to Velero cleanup; matching Velero Restore was deleted (err=%v)", err)
	}
}

// TestCleanupOnDelete_FoundationDB_DoesNotTouchVeleroNamespace mirrors the
// CNPG cleanup test for the FoundationDB strategy. The dispatch switch in
// cleanupOnDelete groups CNPG / Job / Altinity / MariaDB / FoundationDB
// into the no-op cleanup branch (none of them materialise namespaced
// artefacts in cozy-velero that outlive the RestoreJob). A future
// refactor that drops FoundationDB from the no-op group would fall
// through to the conservative `default` branch which runs the Velero
// cleanup unconditionally - and silently start reaping matching
// velero.io/Restore objects in cozy-velero. Seeding a label-matching
// Velero Restore proves the FoundationDB branch stays no-op.
func TestCleanupOnDelete_FoundationDB_DoesNotTouchVeleroNamespace(t *testing.T) {
	apiGroup := strategyv1alpha1.GroupVersion.Group
	rj := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "rj"},
		Spec:       backupsv1alpha1.RestoreJobSpec{BackupRef: corev1.LocalObjectReference{Name: "bk"}},
	}
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bk"},
		Spec: backupsv1alpha1.BackupSpec{
			StrategyRef: corev1.TypedLocalObjectReference{
				APIGroup: &apiGroup, Kind: strategyv1alpha1.FoundationDBStrategyKind, Name: "s",
			},
		},
	}
	owned := &velerov1.Restore{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: veleroNamespace,
			Name:      "would-be-victim",
			Labels: map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      "rj",
				backupsv1alpha1.OwningJobNamespaceLabel: "tenant",
			},
		},
	}
	c := newRestoreJobTestClient(t, rj, backup, owned)
	r := &RestoreJobReconciler{Client: c, Recorder: record.NewFakeRecorder(10)}

	r.cleanupOnDelete(context.Background(), rj)

	got := &velerov1.Restore{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(owned), got); err != nil {
		t.Fatalf("FoundationDB cleanup incorrectly routed to Velero cleanup; matching Velero Restore was deleted (err=%v)", err)
	}
}

// TestCleanupOnDelete_Etcd_DoesNotTouchVeleroNamespace mirrors the
// CNPG / FoundationDB cleanup tests for the Etcd strategy. The Etcd
// driver does not materialise namespaced artefacts in cozy-velero
// (the destructive in-place flow's side state lives on the RestoreJob
// itself as the EtcdClusterSpecCaptured + TargetPurged conditions,
// and the operator-side EtcdCluster is owned by the source
// HelmRelease). A future refactor that drops Etcd from the no-op
// group would fall through to the conservative `default` branch
// which runs the Velero cleanup unconditionally - silently reaping
// matching velero.io/Restore objects on label match and emitting a
// misleading "Failed to delete Velero Restore" event on
// non-Etcd-related apiserver errors. Seeding a label-matching Velero
// Restore proves the Etcd branch stays no-op.
func TestCleanupOnDelete_Etcd_DoesNotTouchVeleroNamespace(t *testing.T) {
	apiGroup := strategyv1alpha1.GroupVersion.Group
	rj := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "rj"},
		Spec:       backupsv1alpha1.RestoreJobSpec{BackupRef: corev1.LocalObjectReference{Name: "bk"}},
	}
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bk"},
		Spec: backupsv1alpha1.BackupSpec{
			StrategyRef: corev1.TypedLocalObjectReference{
				APIGroup: &apiGroup, Kind: strategyv1alpha1.EtcdStrategyKind, Name: "s",
			},
		},
	}
	owned := &velerov1.Restore{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: veleroNamespace,
			Name:      "would-be-victim",
			Labels: map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      "rj",
				backupsv1alpha1.OwningJobNamespaceLabel: "tenant",
			},
		},
	}
	c := newRestoreJobTestClient(t, rj, backup, owned)
	r := &RestoreJobReconciler{Client: c, Recorder: record.NewFakeRecorder(10)}

	r.cleanupOnDelete(context.Background(), rj)

	got := &velerov1.Restore{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(owned), got); err != nil {
		t.Fatalf("Etcd cleanup incorrectly routed to Velero cleanup; matching Velero Restore was deleted (err=%v)", err)
	}
}

// TestCleanupOnDelete_Velero_DeletesOwnedRestore confirms the dispatcher
// still routes Velero RestoreJob deletions to the Velero cleanup path.
func TestCleanupOnDelete_Velero_DeletesOwnedRestore(t *testing.T) {
	apiGroup := strategyv1alpha1.GroupVersion.Group
	rj := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "rj"},
		Spec:       backupsv1alpha1.RestoreJobSpec{BackupRef: corev1.LocalObjectReference{Name: "bk"}},
	}
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bk"},
		Spec: backupsv1alpha1.BackupSpec{
			StrategyRef: corev1.TypedLocalObjectReference{
				APIGroup: &apiGroup, Kind: strategyv1alpha1.VeleroStrategyKind, Name: "s",
			},
		},
	}
	owned := &velerov1.Restore{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: veleroNamespace,
			Name:      "owned",
			Labels: map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      "rj",
				backupsv1alpha1.OwningJobNamespaceLabel: "tenant",
			},
		},
	}
	c := newRestoreJobTestClient(t, rj, backup, owned)
	r := &RestoreJobReconciler{Client: c, Recorder: record.NewFakeRecorder(10)}

	r.cleanupOnDelete(context.Background(), rj)

	got := &velerov1.Restore{}
	err := c.Get(context.Background(), client.ObjectKeyFromObject(owned), got)
	if err == nil {
		t.Errorf("expected owned Velero Restore to be deleted, but it still exists")
	}
}

// TestCleanupOnDelete_BackupUnreadable_DoesNotEmitVeleroCleanupEvent asserts
// that when the referenced Backup cannot be read - so we cannot tell which
// driver produced the RestoreJob - cleanup does NOT surface a CleanupFailed
// Warning event, even on a cluster without the Velero CRDs (a supported
// configuration) where the speculative Velero List/DeleteAllOf fails.
// Regression: the old default branch ran cleanupVeleroRestore
// unconditionally and emitted CleanupFailed (and errored on every delete)
// for non-Velero / Velero-less clusters.
func TestCleanupOnDelete_BackupUnreadable_DoesNotEmitVeleroCleanupEvent(t *testing.T) {
	rj := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "rj"},
		Spec:       backupsv1alpha1.RestoreJobSpec{BackupRef: corev1.LocalObjectReference{Name: "missing"}},
	}
	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	_ = backupsv1alpha1.AddToScheme(s)
	_ = velerov1.AddToScheme(s)
	// Simulate a cluster with no Velero CRDs: every Velero Restore List /
	// DeleteAllOf fails the way it would when the kind is unregistered
	// server-side.
	veleroAbsent := errors.New(`no matches for kind "Restore" in version "velero.io/v1"`)
	c := clientfake.NewClientBuilder().
		WithScheme(s).
		WithObjects(rj).
		WithStatusSubresource(&backupsv1alpha1.RestoreJob{}, &backupsv1alpha1.BackupJob{}).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*velerov1.RestoreList); ok {
					return veleroAbsent
				}
				return cl.List(ctx, list, opts...)
			},
			DeleteAllOf: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteAllOfOption) error {
				if _, ok := obj.(*velerov1.Restore); ok {
					return veleroAbsent
				}
				return cl.DeleteAllOf(ctx, obj, opts...)
			},
		}).
		Build()
	rec := record.NewFakeRecorder(10)
	r := &RestoreJobReconciler{Client: c, Recorder: rec}

	r.cleanupOnDelete(context.Background(), rj)

	select {
	case ev := <-rec.Events:
		t.Fatalf("expected no event for unreadable-Backup cleanup, got %q", ev)
	default:
	}
}

func newRestoreJobTestClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	_ = backupsv1alpha1.AddToScheme(s)
	_ = velerov1.AddToScheme(s)
	return clientfake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		WithStatusSubresource(&backupsv1alpha1.RestoreJob{}, &backupsv1alpha1.BackupJob{}).
		Build()
}

// TestReconcileRestore_UnsupportedKindInPlatformAPIGroup_IsTerminal
// mirrors the BackupJob counterpart (round-9 blocker #6, round-10
// blocker #2): a Backup whose strategyRef points at an unknown Kind
// inside the platform APIGroup must terminally fail the RestoreJob
// BEFORE credentials projection runs, so cozy-backups-creds is not
// leaked into the tenant namespace by an unowned strategy.
func TestReconcileRestore_UnsupportedKindInPlatformAPIGroup_IsTerminal(t *testing.T) {
	platformGroup := strategyv1alpha1.GroupVersion.Group
	bk := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-acme", Name: "bk-bad"},
		Spec: backupsv1alpha1.BackupSpec{
			StrategyRef: corev1.TypedLocalObjectReference{
				APIGroup: &platformGroup, Kind: "MadeUpKind", Name: "irrelevant",
			},
		},
	}
	rj := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-acme", Name: "rj-bad"},
		Spec: backupsv1alpha1.RestoreJobSpec{
			BackupRef: corev1.LocalObjectReference{Name: "bk-bad"},
		},
	}
	c := newRestoreJobTestClient(t, rj, bk)
	r := &RestoreJobReconciler{Client: c, Recorder: record.NewFakeRecorder(10)}
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "tenant-acme", Name: "rj-bad"}}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := &backupsv1alpha1.RestoreJob{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant-acme", Name: "rj-bad"}, got); err != nil {
		t.Fatalf("get RestoreJob: %v", err)
	}
	if got.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
		t.Fatalf("expected Phase=Failed, got %q", got.Status.Phase)
	}
	leaked := &corev1.Secret{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant-acme", Name: "cozy-backups-creds"}, leaked); err == nil {
		t.Fatalf("unsupported-Kind RestoreJob leaked cozy-backups-creds into tenant namespace")
	}
}

// TestHandleRestoreProjectionError mirrors TestHandleProjectionError for
// the restore path: transient projection errors (source not yet
// propagated, apiserver hiccup) must requeue with Ready=False, terminal
// misconfig must mark Failed. Without this test the duplicated
// classification in restore could drift away from the BackupJob version.
func TestHandleRestoreProjectionError(t *testing.T) {
	cases := []struct {
		name        string
		err         error
		wantRequeue bool
		wantPhase   backupsv1alpha1.RestoreJobPhase
		wantReason  string
	}{
		{
			name:        "source missing requeues",
			err:         &ProjectionError{Reason: ReasonSourceMissing, Message: "src missing"},
			wantRequeue: true,
			wantReason:  "CredentialsProjectionPending",
		},
		{
			name:        "api error requeues",
			err:         &ProjectionError{Reason: ReasonAPIError, Message: "hiccup"},
			wantRequeue: true,
			wantReason:  "CredentialsProjectionPending",
		},
		{
			name:       "malformed source is terminal",
			err:        &ProjectionError{Reason: ReasonSourceMalformed, Message: "no accessKey"},
			wantPhase:  backupsv1alpha1.RestoreJobPhaseFailed,
			wantReason: "RestoreFailed",
		},
		{
			name:       "unowned target is terminal",
			err:        &ProjectionError{Reason: ReasonTargetNotOwned, Message: "owned by tenant"},
			wantPhase:  backupsv1alpha1.RestoreJobPhaseFailed,
			wantReason: "RestoreFailed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rj := &backupsv1alpha1.RestoreJob{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "rj"}}
			c := newRestoreJobTestClient(t, rj)
			r := &RestoreJobReconciler{Client: c, Recorder: record.NewFakeRecorder(8)}
			res, err := r.handleProjectionError(context.Background(), rj, tc.err)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantRequeue {
				if res.RequeueAfter == 0 {
					t.Fatalf("expected requeue, got %+v", res)
				}
				if rj.Status.Phase == backupsv1alpha1.RestoreJobPhaseFailed {
					t.Fatalf("transient must not set Failed, got %q", rj.Status.Phase)
				}
			} else {
				if res.RequeueAfter != 0 {
					t.Fatalf("terminal must not requeue, got %+v", res)
				}
				if rj.Status.Phase != tc.wantPhase {
					t.Fatalf("expected Phase=%q, got %q", tc.wantPhase, rj.Status.Phase)
				}
			}
			cond := meta.FindStatusCondition(rj.Status.Conditions, "Ready")
			if cond == nil {
				t.Fatalf("Ready condition not set: %+v", rj.Status.Conditions)
			}
			if cond.Reason != tc.wantReason {
				t.Errorf("Ready.Reason: got %q want %q", cond.Reason, tc.wantReason)
			}
		})
	}
}

// TestMarkRestoreJobFailed_DoesNotAppendDuplicateReady locks in the fix:
// markRestoreJobFailed used to `append` to Status.Conditions, which violates
// the +listType=map +listMapKey=type contract on the field. Two terminal
// failures (or a terminal failure on top of a transient Ready=False
// condition that a previous reconcile already wrote, e.g.
// WaitingForArchive) used to leave two entries with Type="Ready" - any
// consumer doing meta.FindStatusCondition reads whichever copy comes first
// in the slice, not necessarily the latest. SetStatusCondition keeps the
// list a proper map.
func TestMarkRestoreJobFailed_DoesNotAppendDuplicateReady(t *testing.T) {
	t.Run("two failures keep exactly one Ready condition", func(t *testing.T) {
		rj := &backupsv1alpha1.RestoreJob{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "rj"},
		}
		c := newRestoreJobTestClient(t, rj)
		r := &RestoreJobReconciler{Client: c}

		if _, err := r.markRestoreJobFailed(context.Background(), rj, "first failure"); err != nil {
			t.Fatalf("first markRestoreJobFailed: %v", err)
		}
		if _, err := r.markRestoreJobFailed(context.Background(), rj, "second failure"); err != nil {
			t.Fatalf("second markRestoreJobFailed: %v", err)
		}

		readyCount := 0
		for _, cond := range rj.Status.Conditions {
			if cond.Type == "Ready" {
				readyCount++
			}
		}
		if readyCount != 1 {
			t.Fatalf("expected exactly 1 Ready condition, got %d (full slice: %+v)", readyCount, rj.Status.Conditions)
		}
		ready := meta.FindStatusCondition(rj.Status.Conditions, "Ready")
		if ready == nil || ready.Message != "second failure" {
			t.Errorf("expected latest failure message preserved, got %+v", ready)
		}
	})

	t.Run("transient Ready=False condition is replaced in place", func(t *testing.T) {
		rj := &backupsv1alpha1.RestoreJob{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "rj"},
			Status: backupsv1alpha1.RestoreJobStatus{
				Conditions: []metav1.Condition{
					{Type: "Ready", Status: metav1.ConditionFalse, Reason: "WaitingForArchive", Message: "stale"},
				},
			},
		}
		c := newRestoreJobTestClient(t, rj)
		r := &RestoreJobReconciler{Client: c}

		if _, err := r.markRestoreJobFailed(context.Background(), rj, "archive deadline exceeded"); err != nil {
			t.Fatalf("markRestoreJobFailed: %v", err)
		}
		if got := len(rj.Status.Conditions); got != 1 {
			t.Fatalf("expected 1 condition, got %d (full slice: %+v)", got, rj.Status.Conditions)
		}
		if rj.Status.Conditions[0].Reason != "RestoreFailed" {
			t.Errorf("expected Reason=RestoreFailed, got %q", rj.Status.Conditions[0].Reason)
		}
		if rj.Status.Conditions[0].Message != "archive deadline exceeded" {
			t.Errorf("expected new message, got %q", rj.Status.Conditions[0].Message)
		}
	})
}

// TestRequeueRestoreStrategyNotReady_BoundedByDeadline mirrors the BackupJob
// BLOCKER 3 fix on the restore path: a Backup.spec.strategyRef.name that never
// resolves must fail closed after the bootstrap-window grace period rather than
// requeuing forever.
func TestRequeueRestoreStrategyNotReady_BoundedByDeadline(t *testing.T) {
	t.Run("within grace period stays transient and requeues", func(t *testing.T) {
		recent := metav1.NewTime(metav1.Now().Add(-time.Minute))
		rj := &backupsv1alpha1.RestoreJob{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "rj"},
			Status:     backupsv1alpha1.RestoreJobStatus{StartedAt: &recent},
		}
		c := newRestoreJobTestClient(t, rj)
		r := &RestoreJobReconciler{Client: c}

		res, err := r.requeueRestoreStrategyNotReady(context.Background(), rj, "cozy-default-cnpg")
		if err != nil {
			t.Fatalf("requeueRestoreStrategyNotReady: %v", err)
		}
		if res.RequeueAfter != CredentialsProjectionRequeue {
			t.Fatalf("expected requeue after %s, got %+v", CredentialsProjectionRequeue, res)
		}
		if rj.Status.Phase == backupsv1alpha1.RestoreJobPhaseFailed {
			t.Fatalf("must not be terminal within the grace period")
		}
		ready := meta.FindStatusCondition(rj.Status.Conditions, "Ready")
		if ready == nil || ready.Reason != "StrategyNotReady" {
			t.Fatalf("expected Ready=False/StrategyNotReady, got %+v", ready)
		}
	})

	t.Run("past deadline fails terminally naming the strategy", func(t *testing.T) {
		stale := metav1.NewTime(metav1.Now().Add(-StrategyNotReadyDeadline - time.Minute))
		rj := &backupsv1alpha1.RestoreJob{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "rj"},
			Status:     backupsv1alpha1.RestoreJobStatus{StartedAt: &stale},
		}
		c := newRestoreJobTestClient(t, rj)
		r := &RestoreJobReconciler{Client: c}

		res, err := r.requeueRestoreStrategyNotReady(context.Background(), rj, "cozy-default-cnpg")
		if err != nil {
			t.Fatalf("requeueRestoreStrategyNotReady: %v", err)
		}
		if res.RequeueAfter != 0 {
			t.Fatalf("terminal failure must not requeue, got %+v", res)
		}
		if rj.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
			t.Fatalf("expected Phase=Failed, got %q", rj.Status.Phase)
		}
		if !strings.Contains(rj.Status.Message, "cozy-default-cnpg") {
			t.Errorf("failure message should name the missing strategy, got %q", rj.Status.Message)
		}
	})
}
