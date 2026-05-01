// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	"github.com/cozystack/cozystack/internal/backupcontroller/cnpgtypes"
	velerov1 "github.com/vmware-tanzu/velero/pkg/apis/velero/v1"
)

// TestBackupCleanup_CNPG_DeletesUnderlyingCNPGBackup locks in the bug fix
// for review Blocker 2: deleting a CNPG-strategy Backup must also delete
// the postgresql.cnpg.io/Backup CR the driver created. Before the fix,
// cleanupVeleroBackup was the only cleanup path and it short-circuited on
// CNPG Backups (no velero metadata key), leaking the cnpg.io/Backup CR.
func TestBackupCleanup_CNPG_DeletesUnderlyingCNPGBackup(t *testing.T) {
	apiGroup := strategyv1alpha1.GroupVersion.Group
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bk"},
		Spec: backupsv1alpha1.BackupSpec{
			StrategyRef: corev1.TypedLocalObjectReference{
				APIGroup: &apiGroup, Kind: strategyv1alpha1.CNPGStrategyKind, Name: "s",
			},
			DriverMetadata: map[string]string{
				cnpgBackupNameKey: "bk-abc123",
			},
		},
	}
	cnpgBk := &cnpgtypes.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bk-abc123"},
	}
	c := newBackupTestClient(t, backup, cnpgBk)
	r := &BackupReconciler{Client: c, Recorder: record.NewFakeRecorder(10)}

	if err := r.cleanupOnDelete(context.Background(), backup); err != nil {
		t.Fatalf("cleanupOnDelete returned %v", err)
	}

	got := &cnpgtypes.Backup{}
	err := c.Get(context.Background(), client.ObjectKey{Namespace: "tenant", Name: "bk-abc123"}, got)
	if err == nil {
		t.Fatalf("expected cnpg.io/Backup to be deleted, but it still exists")
	}
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected NotFound, got %v", err)
	}
}

// TestBackupCleanup_CNPG_NoMetadataIsNoOp asserts the dispatcher does not
// fail when a CNPG-strategy Backup carries no driver metadata - e.g. the
// BackupJob marked itself succeeded but never wrote the cnpg.io/Backup
// name. Cleanup must silently no-op rather than block finalizer removal.
func TestBackupCleanup_CNPG_NoMetadataIsNoOp(t *testing.T) {
	apiGroup := strategyv1alpha1.GroupVersion.Group
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bk"},
		Spec: backupsv1alpha1.BackupSpec{
			StrategyRef: corev1.TypedLocalObjectReference{
				APIGroup: &apiGroup, Kind: strategyv1alpha1.CNPGStrategyKind, Name: "s",
			},
		},
	}
	c := newBackupTestClient(t, backup)
	r := &BackupReconciler{Client: c, Recorder: record.NewFakeRecorder(10)}

	if err := r.cleanupOnDelete(context.Background(), backup); err != nil {
		t.Fatalf("cleanupOnDelete returned unexpected error %v", err)
	}
}

// TestBackupCleanup_Velero_DispatchUnchanged guards the Velero path: the
// dispatcher refactor must still route Velero-strategy Backups through
// cleanupVeleroBackup and create a DeleteBackupRequest.
func TestBackupCleanup_Velero_DispatchUnchanged(t *testing.T) {
	apiGroup := strategyv1alpha1.GroupVersion.Group
	veleroBk := &velerov1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: veleroNamespace, Name: "vb-1"},
	}
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bk"},
		Spec: backupsv1alpha1.BackupSpec{
			StrategyRef: corev1.TypedLocalObjectReference{
				APIGroup: &apiGroup, Kind: strategyv1alpha1.VeleroStrategyKind, Name: "s",
			},
			DriverMetadata: map[string]string{
				veleroBackupNameMetadataKey:      "vb-1",
				veleroBackupNamespaceMetadataKey: veleroNamespace,
			},
		},
	}
	c := newBackupTestClient(t, backup, veleroBk)
	r := &BackupReconciler{Client: c, Recorder: record.NewFakeRecorder(10)}

	if err := r.cleanupOnDelete(context.Background(), backup); err != nil {
		t.Fatalf("cleanupOnDelete returned %v", err)
	}

	dbrList := &velerov1.DeleteBackupRequestList{}
	if err := c.List(context.Background(), dbrList, client.InNamespace(veleroNamespace)); err != nil {
		t.Fatalf("list DeleteBackupRequests: %v", err)
	}
	if len(dbrList.Items) != 1 {
		t.Fatalf("expected 1 DeleteBackupRequest, got %d", len(dbrList.Items))
	}
	if dbrList.Items[0].Spec.BackupName != "vb-1" {
		t.Errorf("DeleteBackupRequest targets unexpected backup: %q", dbrList.Items[0].Spec.BackupName)
	}
}

// TestStrategyKindForBackup mirrors the dispatcher's strategy lookup. Useful
// in isolation when reasoning about edge cases (empty strategyRef, etc.).
func TestStrategyKindForBackup(t *testing.T) {
	apiGroup := strategyv1alpha1.GroupVersion.Group
	cases := []struct {
		name string
		kind string
		want string
	}{
		{"velero", strategyv1alpha1.VeleroStrategyKind, strategyv1alpha1.VeleroStrategyKind},
		{"cnpg", strategyv1alpha1.CNPGStrategyKind, strategyv1alpha1.CNPGStrategyKind},
		{"job", strategyv1alpha1.JobStrategyKind, strategyv1alpha1.JobStrategyKind},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := &backupsv1alpha1.Backup{
				Spec: backupsv1alpha1.BackupSpec{
					StrategyRef: corev1.TypedLocalObjectReference{
						APIGroup: &apiGroup, Kind: tc.kind, Name: "s",
					},
				},
			}
			if got := strategyKindForBackup(b); got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}

func newBackupTestClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	_ = backupsv1alpha1.AddToScheme(s)
	_ = velerov1.AddToScheme(s)
	_ = cnpgtypes.AddToScheme(s)
	return clientfake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
}
