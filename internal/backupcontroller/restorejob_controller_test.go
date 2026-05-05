// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

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
			got := strategyKindForRestoreJob(context.Background(), c, rj)
			if got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}

	t.Run("missing backup yields empty kind", func(t *testing.T) {
		rj := &backupsv1alpha1.RestoreJob{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "rj"},
			Spec: backupsv1alpha1.RestoreJobSpec{
				BackupRef: corev1.LocalObjectReference{Name: "missing"},
			},
		}
		c := newRestoreJobTestClient(t, rj)
		if got := strategyKindForRestoreJob(context.Background(), c, rj); got != "" {
			t.Errorf("expected empty string for missing backup, got %q", got)
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

func newRestoreJobTestClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	_ = backupsv1alpha1.AddToScheme(s)
	_ = velerov1.AddToScheme(s)
	return clientfake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
}
