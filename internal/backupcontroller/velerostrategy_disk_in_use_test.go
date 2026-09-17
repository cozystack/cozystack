// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
)

// An in-place restore of a VMDisk that a running VM mounts must be refused
// before anything changes: renaming the PVC under the VM leaves it Terminating
// behind pvc-protection, and Velero then skips the PVC as already present.
func TestPrepareForRestore_VMDiskInUse(t *testing.T) {
	const (
		ns    = "tenant-root"
		claim = "vm-disk-data"
	)
	mountingPod := func(name, claimName string, phase corev1.PodPhase) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "compute", Image: "virt-launcher"}},
				Volumes: []corev1.Volume{{
					Name:         "disk",
					VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claimName}},
				}},
			},
			Status: corev1.PodStatus{Phase: phase},
		}
	}
	urRaw, _ := json.Marshal(vmInstanceResources{
		DataVolumes: []backupsv1alpha1.DataVolumeResource{{DataVolumeName: claim, ApplicationName: "data"}},
	})
	ur := &runtime.RawExtension{Raw: urRaw}
	restoreJob := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Name: "restore-disk", Namespace: ns},
		Spec:       backupsv1alpha1.RestoreJobSpec{BackupRef: corev1.LocalObjectReference{Name: "bk"}},
	}
	backupOf := func(kind, name string) *backupsv1alpha1.Backup {
		return &backupsv1alpha1.Backup{
			ObjectMeta: metav1.ObjectMeta{Name: "bk", Namespace: ns},
			Spec: backupsv1alpha1.BackupSpec{
				ApplicationRef: corev1.TypedLocalObjectReference{APIGroup: new("apps.cozystack.io"), Kind: kind, Name: name},
			},
		}
	}
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: claim, Namespace: ns}}
	keep := RestoreOptions{KeepOriginalPVC: new(true)}

	t.Run("a running pod that mounts the disk fails the restore and changes nothing", func(t *testing.T) {
		backup := backupOf("VMDisk", "data")
		hr := makeUnstructuredHelmRelease(claim, ns, nil)
		r := newTestRestoreJobReconcilerWithDynamic(t, []runtime.Object{hr},
			restoreJob.DeepCopy(), backup, pvc.DeepCopy(), mountingPod("virt-launcher-vm-instance-web-abcde", claim, corev1.PodRunning))
		ctx := context.Background()

		ready, _, err := r.prepareForRestore(ctx, restoreJob.DeepCopy(), backup, ur, restoreTarget{Namespace: ns, AppName: "data", AppKind: "VMDisk"}, keep)
		if err == nil || ready {
			t.Fatalf("prepareForRestore = ready %v, err %v; want an error for a mounted disk", ready, err)
		}
		if !strings.Contains(err.Error(), "pod/virt-launcher-vm-instance-web-abcde") {
			t.Errorf("error %q does not name the pod that mounts the disk", err)
		}
		if err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: claim}, &corev1.PersistentVolumeClaim{}); err != nil {
			t.Errorf("the mounted PVC must keep its name, got %v", err)
		}
		got, err := r.Resource(helmReleaseGVR).Namespace(ns).Get(ctx, claim, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get HelmRelease: %v", err)
		}
		if suspended, _, _ := unstructured.NestedBool(got.Object, "spec", "suspend"); suspended {
			t.Errorf("the HelmRelease must not be suspended for a refused restore")
		}
	})

	for _, tc := range []struct {
		name string
		pod  *corev1.Pod
	}{
		{name: "a pod that has finished does not hold the disk", pod: mountingPod("importer", claim, corev1.PodSucceeded)},
		{name: "a pod that mounts another disk does not hold this one", pod: mountingPod("other", "vm-disk-other", corev1.PodRunning)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backup := backupOf("VMDisk", "data")
			r := newTestRestoreJobReconciler(t, restoreJob.DeepCopy(), backup, pvc.DeepCopy(), tc.pod)
			ready, _, err := r.prepareForRestore(context.Background(), restoreJob.DeepCopy(), backup, ur,
				restoreTarget{Namespace: ns, AppName: "data", AppKind: "VMDisk"}, RestoreOptions{KeepOriginalPVC: new(false)})
			if err != nil || !ready {
				t.Fatalf("prepareForRestore = ready %v, err %v; want ready", ready, err)
			}
		})
	}

	// A VMInstance target is halted, so its own launcher Pod is not a reason to
	// refuse; the rename waits until that Pod has let go of the disks.
	t.Run("a VMInstance target waits for its own launcher pod instead of being refused", func(t *testing.T) {
		backup := backupOf("VMInstance", "web")
		r := newTestRestoreJobReconciler(t, restoreJob.DeepCopy(), backup, pvc.DeepCopy(),
			mountingPod("virt-launcher-vm-instance-web-abcde", claim, corev1.PodRunning))
		ready, res, err := r.prepareForRestore(context.Background(), restoreJob.DeepCopy(), backup, ur,
			restoreTarget{Namespace: ns, AppName: "web", AppKind: "VMInstance"}, RestoreOptions{KeepOriginalPVC: new(true)})
		if err != nil || ready || res.RequeueAfter == 0 {
			t.Fatalf("prepareForRestore = ready %v, requeue %v, err %v; want a requeue", ready, res.RequeueAfter, err)
		}
		if err := r.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: claim}, &corev1.PersistentVolumeClaim{}); err != nil {
			t.Errorf("the PVC was renamed while the launcher still mounts it: %v", err)
		}
	})

	t.Run("a VMInstance target proceeds once no pod mounts its disks", func(t *testing.T) {
		backup := backupOf("VMInstance", "web")
		r := newTestRestoreJobReconciler(t, restoreJob.DeepCopy(), backup, pvc.DeepCopy())
		ready, _, err := r.prepareForRestore(context.Background(), restoreJob.DeepCopy(), backup, ur,
			restoreTarget{Namespace: ns, AppName: "web", AppKind: "VMInstance"}, RestoreOptions{KeepOriginalPVC: new(false)})
		if err != nil || !ready {
			t.Fatalf("prepareForRestore = ready %v, err %v; want ready", ready, err)
		}
	})

	// A launcher Pod already being deleted (the VM was just stopped) releases the
	// disk on its own, so the restore waits rather than fails.
	t.Run("a VMDisk held by a pod being deleted waits instead of failing", func(t *testing.T) {
		backup := backupOf("VMDisk", "data")
		pod := mountingPod("virt-launcher-vm-instance-web-abcde", claim, corev1.PodRunning)
		now := metav1.Now()
		pod.DeletionTimestamp = &now
		pod.Finalizers = []string{"kubernetes"}
		r := newTestRestoreJobReconciler(t, restoreJob.DeepCopy(), backup, pvc.DeepCopy(), pod)
		ready, res, err := r.prepareForRestore(context.Background(), restoreJob.DeepCopy(), backup, ur,
			restoreTarget{Namespace: ns, AppName: "data", AppKind: "VMDisk"}, keep)
		if err != nil || ready || res.RequeueAfter == 0 {
			t.Fatalf("prepareForRestore = ready %v, requeue %v, err %v; want a requeue", ready, res.RequeueAfter, err)
		}
	})

	t.Run("a pod that failed does not hold the disk", func(t *testing.T) {
		backup := backupOf("VMDisk", "data")
		r := newTestRestoreJobReconciler(t, restoreJob.DeepCopy(), backup, pvc.DeepCopy(), mountingPod("evicted", claim, corev1.PodFailed))
		ready, _, err := r.prepareForRestore(context.Background(), restoreJob.DeepCopy(), backup, ur,
			restoreTarget{Namespace: ns, AppName: "data", AppKind: "VMDisk"}, RestoreOptions{KeepOriginalPVC: new(false)})
		if err != nil || !ready {
			t.Fatalf("prepareForRestore = ready %v, err %v; want ready", ready, err)
		}
	})
}
