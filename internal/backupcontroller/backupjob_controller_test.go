// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"sort"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

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
		strategyv1alpha1.EtcdStrategyKind,
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

// TestReconcile_SkipsProjection_TerminalPhase covers review finding #2:
// a Succeeded/Failed BackupJob must not keep projecting Secrets on every
// requeue. Before the fix the controller would Get the source Secret and
// Get/Update the target on each reconcile of a terminal job.
func TestReconcile_SkipsProjection_TerminalPhase(t *testing.T) {
	bj := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-acme", Name: "bj"},
		Status:     backupsv1alpha1.BackupJobStatus{Phase: backupsv1alpha1.BackupJobPhaseSucceeded},
	}
	c := newBackupJobTestClient(t, bj, flatSourceSecret())
	r := &BackupJobReconciler{Client: c, CredentialsConfig: defaultCfg()}
	res, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "tenant-acme", Name: "bj"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.RequeueAfter != 0 || res.Requeue {
		t.Fatalf("terminal BackupJob must not requeue, got %+v", res)
	}
	// Projection must NOT have created the target Secret.
	got := &corev1.Secret{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant-acme", Name: "cozy-backups-creds"}, got); err == nil {
		t.Fatalf("terminal BackupJob triggered projection; target Secret should be absent")
	}
}

// TestReconcile_SkipsProjection_ForeignAPIGroup covers review finding #2
// + #7: a BackupJob whose BackupClass resolves to a strategy outside
// strategy.backups.cozystack.io must not project cozy-backups-creds (so
// third-party drivers and clusters with a manually-managed
// cozy-backups-creds Secret are unaffected). Before the fix projection
// ran before APIGroup filtering and could terminally fail unrelated
// BackupJobs via the ownership guard.
func TestReconcile_SkipsProjection_ForeignAPIGroup(t *testing.T) {
	foreignGroup := "third-party.example.com"
	bc := &backupsv1alpha1.BackupClass{
		ObjectMeta: metav1.ObjectMeta{Name: "foreign-class"},
		Spec: backupsv1alpha1.BackupClassSpec{
			Strategies: []backupsv1alpha1.BackupClassStrategy{
				{
					Application: backupsv1alpha1.ApplicationSelector{
						APIGroup: stringPtr("apps.cozystack.io"),
						Kind:     "Postgres",
					},
					StrategyRef: corev1.TypedLocalObjectReference{
						APIGroup: &foreignGroup,
						Kind:     "CustomDriver",
						Name:     "custom",
					},
				},
			},
		},
	}
	bj := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-acme", Name: "bj"},
		Spec: backupsv1alpha1.BackupJobSpec{
			BackupClassName: "foreign-class",
			ApplicationRef: corev1.TypedLocalObjectReference{
				APIGroup: stringPtr("apps.cozystack.io"),
				Kind:     "Postgres",
				Name:     "pg",
			},
		},
	}
	// Plant an unowned cozy-backups-creds: the guard would terminally
	// fail the BackupJob if projection ran. With the fix in place,
	// projection is gated on the platform APIGroup and the unowned
	// Secret is left alone.
	unowned := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-acme", Name: "cozy-backups-creds"},
		Data:       map[string][]byte{"user-data": []byte("x")},
	}
	c := newBackupJobTestClient(t, bj, bc, flatSourceSecret(), unowned)
	r := &BackupJobReconciler{Client: c, CredentialsConfig: defaultCfg()}
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "tenant-acme", Name: "bj"}}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// BackupJob must remain non-terminal: foreign APIGroup short-circuits
	// without any phase transition.
	got := &backupsv1alpha1.BackupJob{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant-acme", Name: "bj"}, got); err != nil {
		t.Fatalf("fetch BackupJob: %v", err)
	}
	if got.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
		t.Fatalf("foreign-APIGroup BackupJob was terminally failed: %+v", got.Status)
	}
	// The pre-existing unowned Secret must be untouched.
	gotSec := &corev1.Secret{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant-acme", Name: "cozy-backups-creds"}, gotSec); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(gotSec.Data["user-data"]) != "x" {
		t.Fatalf("unowned Secret mutated by foreign-APIGroup reconcile: %v", gotSec.Data)
	}
	if _, leaked := gotSec.Data["AWS_ACCESS_KEY_ID"]; leaked {
		t.Fatalf("projector ran for foreign-APIGroup BackupJob; AWS creds leaked into unowned Secret")
	}
}

// TestReconcile_NoMatchingStrategy_IsTerminal covers round-8 blocker #2:
// a BackupJob whose BackupClass does not bind a strategy for the
// applicationRef Kind (the documented FoundationDB-vs-cozy-default case)
// must transition to Phase=Failed with a clear message instead of being
// requeued forever by controller-runtime's default error-handling.
func TestReconcile_NoMatchingStrategy_IsTerminal(t *testing.T) {
	appsGroup := "apps.cozystack.io"
	bc := &backupsv1alpha1.BackupClass{
		ObjectMeta: metav1.ObjectMeta{Name: "cozy-default"},
		Spec: backupsv1alpha1.BackupClassSpec{
			Strategies: []backupsv1alpha1.BackupClassStrategy{
				// Only Postgres is bound — FoundationDB intentionally omitted.
				{
					Application: backupsv1alpha1.ApplicationSelector{APIGroup: &appsGroup, Kind: "Postgres"},
					StrategyRef: corev1.TypedLocalObjectReference{
						APIGroup: stringRef(strategyv1alpha1.GroupVersion.Group), Kind: "CNPG", Name: "cozy-default-cnpg",
					},
				},
			},
		},
	}
	bj := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-acme", Name: "fdb-bj"},
		Spec: backupsv1alpha1.BackupJobSpec{
			BackupClassName: "cozy-default",
			ApplicationRef: corev1.TypedLocalObjectReference{
				APIGroup: &appsGroup, Kind: "FoundationDB", Name: "fdb",
			},
		},
	}
	c := newBackupJobTestClient(t, bj, bc)
	r := &BackupJobReconciler{Client: c}
	res, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "tenant-acme", Name: "fdb-bj"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.RequeueAfter != 0 || res.Requeue {
		t.Fatalf("expected terminal Failed with no requeue, got %+v", res)
	}
	got := &backupsv1alpha1.BackupJob{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant-acme", Name: "fdb-bj"}, got); err != nil {
		t.Fatalf("get BackupJob: %v", err)
	}
	if got.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
		t.Fatalf("expected Phase=Failed, got %q", got.Status.Phase)
	}
	if !strings.Contains(got.Status.Message, "does not bind a strategy") {
		t.Errorf("expected misbinding message, got %q", got.Status.Message)
	}
}

func stringRef(s string) *string { return &s }

// TestReconcile_BackupClassNotFound_IsTransient covers round-12 blocker
// #3: a BackupJob referencing a BackupClass that does not exist must
// requeue with Ready=False/BackupClassNotFound, not return a bare
// apiserver error that controller-runtime will log at Error level on
// every backoff cycle. The cozy-default BackupClass is gated on a
// populated BucketClaim status, so on a fresh-cluster bootstrap the
// BackupClass legitimately does not exist for a window — tenant
// BackupJobs filed during that window need a clear status signal.
func TestReconcile_BackupClassNotFound_IsTransient(t *testing.T) {
	appsGroup := "apps.cozystack.io"
	bj := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-acme", Name: "bj"},
		Spec: backupsv1alpha1.BackupJobSpec{
			BackupClassName: "cozy-default",
			ApplicationRef: corev1.TypedLocalObjectReference{
				APIGroup: &appsGroup, Kind: "Postgres", Name: "pg",
			},
		},
	}
	// No BackupClass seeded — that's the point.
	c := newBackupJobTestClient(t, bj)
	r := &BackupJobReconciler{Client: c}
	res, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "tenant-acme", Name: "bj"}})
	if err != nil {
		t.Fatalf("expected nil error (transient handled in-band), got %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Fatalf("expected requeue, got %+v", res)
	}
	got := &backupsv1alpha1.BackupJob{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant-acme", Name: "bj"}, got); err != nil {
		t.Fatalf("get BackupJob: %v", err)
	}
	if got.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
		t.Fatalf("BackupClassNotFound must NOT be terminal; got Phase=%q", got.Status.Phase)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, "Ready")
	if cond == nil || cond.Reason != "BackupClassNotFound" {
		t.Errorf("expected Ready=False/BackupClassNotFound, got %+v", cond)
	}
}

// TestReconcile_UnsupportedKindInPlatformAPIGroup_IsTerminal covers
// round-9 blocker #6: a BackupClass that resolves to
// strategy.backups.cozystack.io/<unknown Kind> used to project
// cozy-backups-creds and then silently no-op in the dispatch switch,
// leaving the BackupJob phaseless and the tenant namespace polluted
// with a leaked Secret. The fix rejects unsupported Kinds BEFORE
// projection and marks the BackupJob terminally Failed.
func TestReconcile_UnsupportedKindInPlatformAPIGroup_IsTerminal(t *testing.T) {
	appsGroup := "apps.cozystack.io"
	platformGroup := strategyv1alpha1.GroupVersion.Group
	bc := &backupsv1alpha1.BackupClass{
		ObjectMeta: metav1.ObjectMeta{Name: "cozy-default"},
		Spec: backupsv1alpha1.BackupClassSpec{
			Strategies: []backupsv1alpha1.BackupClassStrategy{
				{
					Application: backupsv1alpha1.ApplicationSelector{APIGroup: &appsGroup, Kind: "Postgres"},
					StrategyRef: corev1.TypedLocalObjectReference{
						APIGroup: &platformGroup, Kind: "MadeUpKind", Name: "cozy-default-madeup",
					},
				},
			},
		},
	}
	bj := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-acme", Name: "bj"},
		Spec: backupsv1alpha1.BackupJobSpec{
			BackupClassName: "cozy-default",
			ApplicationRef: corev1.TypedLocalObjectReference{
				APIGroup: &appsGroup, Kind: "Postgres", Name: "pg",
			},
		},
	}
	c := newBackupJobTestClient(t, bj, bc)
	r := &BackupJobReconciler{Client: c}
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "tenant-acme", Name: "bj"}}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := &backupsv1alpha1.BackupJob{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant-acme", Name: "bj"}, got); err != nil {
		t.Fatalf("get BackupJob: %v", err)
	}
	if got.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
		t.Fatalf("expected Phase=Failed for unsupported Kind in platform APIGroup, got %q", got.Status.Phase)
	}
	if !strings.Contains(got.Status.Message, "not supported") {
		t.Errorf("expected unsupported-Kind message, got %q", got.Status.Message)
	}
	// Critical: projection must NOT have leaked a Secret into the tenant ns.
	leaked := &corev1.Secret{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant-acme", Name: "cozy-backups-creds"}, leaked); err == nil {
		t.Fatalf("unsupported-Kind BackupJob leaked cozy-backups-creds into tenant namespace")
	}
}

// TestHandleProjectionError pins the transient-vs-terminal split for
// credentials-projection failures. SourceSecretMissing and APIError must
// requeue (so the first BackupJob after a fresh install does not get
// stuck terminally Failed before the Bucket controller produces the
// source Secret); SourceSecretMalformed and TargetSecretNotOwned must
// fail terminally (they are operator-visible misconfigurations that will
// not self-heal).
func TestHandleProjectionError(t *testing.T) {
	cases := []struct {
		name        string
		err         error
		wantRequeue bool
		wantPhase   backupsv1alpha1.BackupJobPhase
		wantReason  string
	}{
		{
			name:        "source missing requeues",
			err:         &ProjectionError{Reason: ReasonSourceMissing, Message: "src/x not found"},
			wantRequeue: true,
			wantReason:  "CredentialsProjectionPending",
		},
		{
			name:        "api error requeues",
			err:         &ProjectionError{Reason: ReasonAPIError, Message: "apiserver hiccup"},
			wantRequeue: true,
			wantReason:  "CredentialsProjectionPending",
		},
		{
			name:       "malformed source is terminal",
			err:        &ProjectionError{Reason: ReasonSourceMalformed, Message: "no accessKey"},
			wantPhase:  backupsv1alpha1.BackupJobPhaseFailed,
			wantReason: "BackupFailed",
		},
		{
			name:       "unowned target is terminal",
			err:        &ProjectionError{Reason: ReasonTargetNotOwned, Message: "tenant owns it"},
			wantPhase:  backupsv1alpha1.BackupJobPhaseFailed,
			wantReason: "BackupFailed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bj := &backupsv1alpha1.BackupJob{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "bj"}}
			c := newBackupJobTestClient(t, bj)
			r := &BackupJobReconciler{Client: c}
			res, err := r.handleProjectionError(context.Background(), bj, tc.err)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantRequeue {
				if res.RequeueAfter == 0 {
					t.Fatalf("expected RequeueAfter > 0, got %+v", res)
				}
				if bj.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
					t.Fatalf("transient projection failure must not set Phase=Failed (got %q)", bj.Status.Phase)
				}
			} else {
				if res.RequeueAfter != 0 {
					t.Fatalf("expected terminal failure with no requeue, got %+v", res)
				}
				if bj.Status.Phase != tc.wantPhase {
					t.Fatalf("expected Phase=%q, got %q", tc.wantPhase, bj.Status.Phase)
				}
			}
			ready := meta.FindStatusCondition(bj.Status.Conditions, "Ready")
			if ready == nil {
				t.Fatalf("Ready condition not set: %+v", bj.Status.Conditions)
			}
			if ready.Reason != tc.wantReason {
				t.Errorf("Ready.Reason: got %q want %q", ready.Reason, tc.wantReason)
			}
		})
	}
}
