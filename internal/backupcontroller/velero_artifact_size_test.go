// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"testing"

	velerov1 "github.com/vmware-tanzu/velero/pkg/apis/velero/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
)

func newVeleroSizeTestClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	_ = backupsv1alpha1.AddToScheme(s)
	_ = velerov1.AddToScheme(s)
	s.AddKnownTypeWithName(dataUploadListGVK.GroupVersion().WithKind("DataUpload"), &unstructured.Unstructured{})
	s.AddKnownTypeWithName(dataUploadListGVK, &unstructured.UnstructuredList{})
	return clientfake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
}

func dataUpload(name, backupUID, phase string, totalBytes int64) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(dataUploadListGVK.GroupVersion().WithKind("DataUpload"))
	u.SetNamespace(veleroNamespace)
	u.SetName(name)
	u.SetLabels(map[string]string{velerov1.BackupUIDLabel: backupUID})
	_ = unstructured.SetNestedField(u.Object, phase, "status", "phase")
	if totalBytes > 0 {
		_ = unstructured.SetNestedField(u.Object, totalBytes, "status", "progress", "totalBytes")
	}
	return u
}

func sizedVeleroBackup(name, uid string) *velerov1.Backup {
	return &velerov1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: veleroNamespace, Name: name, UID: types.UID(uid)},
		Status:     velerov1.BackupStatus{Phase: velerov1.BackupPhaseCompleted},
	}
}

func TestVeleroArtifactSizeBytes(t *testing.T) {
	vb := sizedVeleroBackup("vm-bk", "uid-1")

	t.Run("sums the backup's completed DataUploads only", func(t *testing.T) {
		c := newVeleroSizeTestClient(t,
			dataUpload("vm-bk-a", "uid-1", "Completed", 10<<30),
			dataUpload("vm-bk-b", "uid-1", "Completed", 2<<30),
			// Another backup whose name extends this one's must not be counted.
			dataUpload("vm-bk-2-a", "uid-2", "Completed", 99<<30),
		)
		r := &BackupJobReconciler{Client: c}
		if got, want := r.veleroArtifactSizeBytes(context.Background(), vb), int64(12<<30); got != want {
			t.Errorf("size = %d, want %d", got, want)
		}
	})

	unset := map[string]struct {
		backup  *velerov1.Backup
		uploads []client.Object
	}{
		"no DataUploads": {backup: vb},
		"a DataUpload without a total": {backup: vb, uploads: []client.Object{
			dataUpload("vm-bk-a", "uid-1", "Completed", 10<<30),
			dataUpload("vm-bk-b", "uid-1", "Completed", 0),
		}},
		"a DataUpload that did not complete": {backup: vb, uploads: []client.Object{
			dataUpload("vm-bk-a", "uid-1", "Completed", 10<<30),
			dataUpload("vm-bk-b", "uid-1", "Failed", 1<<30),
		}},
		"native provider snapshots": {
			backup: func() *velerov1.Backup {
				b := sizedVeleroBackup("vm-bk", "uid-1")
				b.Status.VolumeSnapshotsAttempted = 1
				return b
			}(),
			uploads: []client.Object{dataUpload("vm-bk-a", "uid-1", "Completed", 10<<30)},
		},
		"no UID": {
			backup:  sizedVeleroBackup("vm-bk", ""),
			uploads: []client.Object{dataUpload("vm-bk-a", "", "Completed", 10<<30)},
		},
	}
	for name, tc := range unset {
		t.Run(name+" leaves size unset", func(t *testing.T) {
			r := &BackupJobReconciler{Client: newVeleroSizeTestClient(t, tc.uploads...)}
			if got := r.veleroArtifactSizeBytes(context.Background(), tc.backup); got != 0 {
				t.Errorf("size = %d, want 0", got)
			}
		})
	}
}

func TestCreateBackupResource_VeleroSizeBytes(t *testing.T) {
	apiGroup := backupsv1alpha1.DefaultApplicationAPIGroup
	j := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj-vm"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{APIGroup: &apiGroup, Kind: "VMInstance", Name: "vm"},
		},
	}
	c := newVeleroSizeTestClient(t, j, dataUpload("vm-bk-a", "uid-1", "Completed", 5<<30))
	r := &BackupJobReconciler{Client: c, Scheme: c.Scheme()}

	got, err := r.createBackupResource(context.Background(), j, sizedVeleroBackup("vm-bk", "uid-1"), &ResolvedBackupConfig{})
	if err != nil {
		t.Fatalf("createBackupResource: %v", err)
	}
	if got.Status.Artifact == nil || got.Status.Artifact.SizeBytes != 5<<30 {
		t.Fatalf("Artifact = %#v, want SizeBytes %d", got.Status.Artifact, int64(5<<30))
	}
}
