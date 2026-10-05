// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
)

func TestRestoreAhead(t *testing.T) {
	t0 := metav1.NewTime(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	t1 := metav1.NewTime(t0.Add(time.Minute))
	started := metav1.NewTime(t0.Add(30 * time.Second))
	other := pgRef("other")

	restore := func(name string, created metav1.Time, phase backupsv1alpha1.RestoreJobPhase, target *corev1.TypedLocalObjectReference) *backupsv1alpha1.RestoreJob {
		rj := lockRestore(name, "bk", phase, target, "")
		rj.CreationTimestamp = created
		return rj
	}

	for _, tc := range []struct {
		name   string
		self   *backupsv1alpha1.RestoreJob
		others []client.Object
		want   string
	}{
		{
			name: "no other restore",
			self: restore("second", t1, "", nil),
			want: "",
		},
		{
			name:   "an older restore that has not started goes first",
			self:   restore("second", t1, "", nil),
			others: []client.Object{restore("first", t0, backupsv1alpha1.RestoreJobPhasePending, nil)},
			want:   "first",
		},
		{
			name:   "a newer restore that has not started does not go first",
			self:   restore("first", t0, "", nil),
			others: []client.Object{restore("second", t1, "", nil)},
			want:   "",
		},
		{
			name:   "a running restore goes first even when created later",
			self:   restore("first", t0, "", nil),
			others: []client.Object{restore("second", t1, backupsv1alpha1.RestoreJobPhaseRunning, nil)},
			want:   "second",
		},
		{
			name: "a restore with StartedAt goes first even when created later",
			self: restore("first", t0, "", nil),
			others: []client.Object{func() client.Object {
				rj := restore("second", t1, "", nil)
				rj.Status.StartedAt = &started
				return rj
			}()},
			want: "second",
		},
		{
			name:   "created at the same time: the lower name goes first",
			self:   restore("b", t0, "", nil),
			others: []client.Object{restore("a", t0, "", nil)},
			want:   "a",
		},
		{
			name:   "created at the same time: a higher name does not go first",
			self:   restore("a", t0, "", nil),
			others: []client.Object{restore("b", t0, "", nil)},
			want:   "",
		},
		{
			name: "finished restores do not hold",
			self: restore("third", t1, "", nil),
			others: []client.Object{
				restore("done", t0, backupsv1alpha1.RestoreJobPhaseSucceeded, nil),
				restore("failed", t0, backupsv1alpha1.RestoreJobPhaseFailed, nil),
			},
			want: "",
		},
		{
			name: "a restore being deleted does not hold",
			self: restore("second", t1, "", nil),
			others: []client.Object{func() client.Object {
				rj := restore("first", t0, backupsv1alpha1.RestoreJobPhaseRunning, nil)
				rj.DeletionTimestamp = &t1
				rj.Finalizers = []string{restoreJobFinalizer}
				return rj
			}()},
			want: "",
		},
		{
			name:   "a restore into another application does not hold",
			self:   restore("second", t1, "", nil),
			others: []client.Object{restore("first", t0, backupsv1alpha1.RestoreJobPhaseRunning, &other)},
			want:   "",
		},
		{
			name:   "a restore retargeted onto this job's application holds",
			self:   restore("second", t1, "", &other),
			others: []client.Object{restore("first", t0, backupsv1alpha1.RestoreJobPhaseRunning, &other)},
			want:   "first",
		},
		{
			name:   "the lowest name is reported when several hold",
			self:   restore("third", t1, "", nil),
			others: []client.Object{restore("zeta", t0, backupsv1alpha1.RestoreJobPhaseRunning, nil), restore("alpha", t0, backupsv1alpha1.RestoreJobPhaseRunning, nil)},
			want:   "alpha",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objs := append([]client.Object{lockBackup("bk", pgRef("pg")), tc.self}, tc.others...)
			c := lockTestClient(t, objs...)
			got, err := restoreAhead(context.Background(), c, c, tc.self)
			if err != nil {
				t.Fatalf("restoreAhead: %v", err)
			}
			if got != tc.want {
				t.Fatalf("restoreAhead = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRestoreAhead_VeleroIntoAnotherNamespaceIsNotHeld(t *testing.T) {
	t0 := metav1.NewTime(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	vm := corev1.TypedLocalObjectReference{APIGroup: new("apps.cozystack.io"), Kind: "VMInstance", Name: "vm"}
	first := lockRestore("first", "bk", backupsv1alpha1.RestoreJobPhaseRunning, nil, "")
	first.CreationTimestamp = t0
	second := lockRestore("second", "bk", "", nil, `{"targetNamespace":"tenant-copy"}`)
	second.CreationTimestamp = metav1.NewTime(t0.Add(time.Minute))
	c := lockTestClient(t, lockBackupBy("bk", vm, strategyv1alpha1.VeleroStrategyKind), first, second)
	got, err := restoreAhead(context.Background(), c, c, second)
	if err != nil {
		t.Fatalf("restoreAhead: %v", err)
	}
	if got != "" {
		t.Fatalf("restoreAhead = %q, want none: the job writes into another namespace", got)
	}
}

func TestReconcileRestore_ConcurrentRestoreIsRefused(t *testing.T) {
	t0 := metav1.NewTime(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	first := lockRestore("first", "bk", backupsv1alpha1.RestoreJobPhaseRunning, nil, "")
	first.CreationTimestamp = t0
	first.Status.StartedAt = &t0
	second := lockRestore("second", "bk", "", nil, "")
	second.CreationTimestamp = metav1.NewTime(t0.Add(time.Minute))
	c := lockTestClient(t, lockBackup("bk", pgRef("pg")), first, second)
	r := &RestoreJobReconciler{Client: c, Recorder: record.NewFakeRecorder(10)}

	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: lockNS, Name: "second"}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := &backupsv1alpha1.RestoreJob{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: lockNS, Name: "second"}, got); err != nil {
		t.Fatalf("get RestoreJob: %v", err)
	}
	if got.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
		t.Fatalf("Phase = %q, want Failed", got.Status.Phase)
	}
	ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
	if ready == nil || ready.Reason != ConditionReasonConcurrentRestore {
		t.Fatalf("Ready condition = %+v, want reason %s", ready, ConditionReasonConcurrentRestore)
	}
	if !strings.Contains(got.Status.Message, "first") {
		t.Fatalf("message %q does not name the restore that goes first", got.Status.Message)
	}

	running := &backupsv1alpha1.RestoreJob{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: lockNS, Name: "first"}, running); err != nil {
		t.Fatalf("get RestoreJob: %v", err)
	}
	if running.Status.Phase != backupsv1alpha1.RestoreJobPhaseRunning {
		t.Fatalf("the running restore changed phase to %q", running.Status.Phase)
	}
}

// The informer cache can lag a peer's status: here it still holds zeta as not
// started, while the API server already has its StartedAt. Read from the cache,
// alpha would see zeta behind it by name and proceed alongside it.
func TestReconcileRestore_ConcurrentRestoreReadsPastTheCache(t *testing.T) {
	t0 := metav1.NewTime(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	staleZeta := lockRestore("zeta", "bk", "", nil, "")
	staleZeta.CreationTimestamp = t0
	alpha := lockRestore("alpha", "bk", "", nil, "")
	alpha.CreationTimestamp = t0
	alpha.Finalizers = []string{restoreJobFinalizer}
	startedZeta := staleZeta.DeepCopy()
	startedZeta.Status.Phase = backupsv1alpha1.RestoreJobPhaseRunning
	startedZeta.Status.StartedAt = &t0

	cached := lockTestClient(t, lockBackup("bk", pgRef("pg")), staleZeta, alpha)
	live := lockTestClient(t, lockBackup("bk", pgRef("pg")), startedZeta, alpha.DeepCopy())
	r := &RestoreJobReconciler{Client: cached, APIReader: live, Recorder: record.NewFakeRecorder(10)}

	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: lockNS, Name: "alpha"}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := &backupsv1alpha1.RestoreJob{}
	if err := cached.Get(context.Background(), types.NamespacedName{Namespace: lockNS, Name: "alpha"}, got); err != nil {
		t.Fatalf("get RestoreJob: %v", err)
	}
	ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
	if got.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed || ready == nil || ready.Reason != ConditionReasonConcurrentRestore {
		t.Fatalf("alpha: phase %q, Ready %+v; want Failed with reason %s, since zeta has started", got.Status.Phase, ready, ConditionReasonConcurrentRestore)
	}
}
