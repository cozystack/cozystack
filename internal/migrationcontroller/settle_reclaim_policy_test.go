// SPDX-License-Identifier: Apache-2.0

package migrationcontroller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	migrationv1alpha1 "github.com/cozystack/cozystack/api/migration/v1alpha1"
)

const (
	settleNS     = "tenant-foo"
	settleDisk   = "web-01-disk-0"
	settleTarget = vmDiskReleasePrefix + settleDisk
	settlePV     = "pv-1"
	settleDVUID  = types.UID("vmdisk-dv-uid")
	settleClaimU = types.UID("replacement-claim-uid")
)

func classWithPolicy(name string, policy *corev1.PersistentVolumeReclaimPolicy) *storagev1.StorageClass {
	sc := storageClass(name, storagev1.VolumeBindingImmediate, false)
	sc.ReclaimPolicy = policy
	return sc
}

// settledHandoff is the cluster once the swap is over: the replacement claim
// and the volume are bound to each other, CDI has adopted the claim into the
// VMDisk's DataVolume, and the VMDisk exists. Only the reclaim policy is still
// the one the swap needed.
func settledHandoff(tk *migrationv1alpha1.VMImportTask) (*corev1.PersistentVolume, *corev1.PersistentVolumeClaim, *unstructured.Unstructured, *unstructured.Unstructured) {
	controller := true
	claim := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      settleTarget,
			Namespace: settleNS,
			UID:       settleClaimU,
			Labels:    outputMarkers(tk, "vm-1"),
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "cdi.kubevirt.io/v1beta1",
				Kind:       "DataVolume",
				Name:       settleTarget,
				UID:        settleDVUID,
				Controller: &controller,
			}},
		},
		Spec:   corev1.PersistentVolumeClaimSpec{VolumeName: settlePV},
		Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
	}
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: settlePV},
		Spec: corev1.PersistentVolumeSpec{
			StorageClassName:              "replicated",
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			ClaimRef: &corev1.ObjectReference{
				Kind: "PersistentVolumeClaim", APIVersion: "v1",
				Namespace: settleNS, Name: settleTarget, UID: settleClaimU,
			},
		},
		Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound},
	}
	dv := newObject(dataVolumeGVK)
	dv.SetName(settleTarget)
	dv.SetNamespace(settleNS)
	dv.SetUID(settleDVUID)

	disk := newObject(vmDiskGVK)
	disk.SetName(settleDisk)
	disk.SetNamespace(settleNS)
	stampOutput(disk, tk, "vm-1")
	return pv, claim, dv, disk
}

func reclaimPolicyOf(t *testing.T, c client.Client) corev1.PersistentVolumeReclaimPolicy {
	t.Helper()
	pv := &corev1.PersistentVolume{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: settlePV}, pv); err != nil {
		t.Fatalf("get pv: %v", err)
	}
	return pv.Spec.PersistentVolumeReclaimPolicy
}

// The leak this closes: an imported disk kept Retain for good, so deleting its
// VMDisk left the volume Released with its backing storage, billed and
// invisible to the tenant. Once the handoff is over the volume must carry what
// its class says, like any disk created directly.
func TestSettleRestoresTheClassReclaimPolicy(t *testing.T) {
	deletePolicy := corev1.PersistentVolumeReclaimDelete
	retainPolicy := corev1.PersistentVolumeReclaimRetain
	cases := []struct {
		name  string
		class *storagev1.StorageClass
		want  corev1.PersistentVolumeReclaimPolicy
	}{
		{"class says Delete", classWithPolicy("replicated", &deletePolicy), corev1.PersistentVolumeReclaimDelete},
		// A class that asks for Retain keeps it: the policy restored is the
		// class's, not an opinion of this controller.
		{"class says Retain", classWithPolicy("replicated", &retainPolicy), corev1.PersistentVolumeReclaimRetain},
		{"class names no policy", classWithPolicy("replicated", nil), corev1.PersistentVolumeReclaimDelete},
		{"class is gone", nil, corev1.PersistentVolumeReclaimDelete},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := testScheme(t)
			tk := task("import", settleNS, "vcenter", "replicated")
			pv, claim, dv, disk := settledHandoff(tk)
			objs := []client.Object{tk, pv, claim, dv, disk}
			if tc.class != nil {
				objs = append(objs, tc.class)
			}
			c := clientfake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
			r := &VMImportTaskReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(10)}

			settled, err := r.settleReclaimPolicy(context.Background(), tk, "vm-1", settleDisk, "forklift-claim")
			if err != nil {
				t.Fatalf("settleReclaimPolicy: %v", err)
			}
			if !settled {
				t.Fatal("a finished handoff was reported as unsettled; the task would never complete")
			}
			if got := reclaimPolicyOf(t, c); got != tc.want {
				t.Errorf("reclaim policy = %q, want %q", got, tc.want)
			}
		})
	}
}

// The controller is re-entered on every requeue and after every restart; a
// settled volume must not be rewritten on each pass.
func TestSettleIsIdempotent(t *testing.T) {
	s := testScheme(t)
	tk := task("import", settleNS, "vcenter", "replicated")
	pv, claim, dv, disk := settledHandoff(tk)
	deletePolicy := corev1.PersistentVolumeReclaimDelete
	c := clientfake.NewClientBuilder().WithScheme(s).
		WithObjects(tk, pv, claim, dv, disk, classWithPolicy("replicated", &deletePolicy)).Build()
	r := &VMImportTaskReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(10)}

	if _, err := r.settleReclaimPolicy(context.Background(), tk, "vm-1", settleDisk, ""); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	first := &corev1.PersistentVolume{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: settlePV}, first); err != nil {
		t.Fatalf("get pv: %v", err)
	}

	settled, err := r.settleReclaimPolicy(context.Background(), tk, "vm-1", settleDisk, "")
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if !settled {
		t.Error("a volume already settled was reported as unsettled on the next pass")
	}
	second := &corev1.PersistentVolume{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: settlePV}, second); err != nil {
		t.Fatalf("get pv: %v", err)
	}
	if second.ResourceVersion != first.ResourceVersion {
		t.Error("the second pass rewrote a volume that was already settled")
	}
}

// Every state in which something other than the VMDisk could still release the
// volume must keep Retain. Each case breaks exactly one condition of a finished
// handoff; restoring Delete in any of them is the data loss Retain exists for.
func TestSettleKeepsRetainWhileTheHandoffIsOpen(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(pv *corev1.PersistentVolume, claim *corev1.PersistentVolumeClaim) []client.Object
		skipDV   bool
		skipDisk bool
	}{
		{
			name: "replacement claim not bound yet",
			mutate: func(_ *corev1.PersistentVolume, claim *corev1.PersistentVolumeClaim) []client.Object {
				claim.Status.Phase = corev1.ClaimPending
				return nil
			},
		},
		{
			// claimRef was written, but the PV controller has not caught up:
			// a stale view of the claim is exactly when it would reclaim.
			name: "volume still Released",
			mutate: func(pv *corev1.PersistentVolume, _ *corev1.PersistentVolumeClaim) []client.Object {
				pv.Status.Phase = corev1.VolumeReleased
				return nil
			},
		},
		{
			name: "claimRef still names the transferred claim",
			mutate: func(pv *corev1.PersistentVolume, _ *corev1.PersistentVolumeClaim) []client.Object {
				pv.Spec.ClaimRef.Name = "forklift-claim"
				pv.Spec.ClaimRef.UID = "forklift-claim-uid"
				return nil
			},
		},
		{
			name: "transferred claim still exists",
			mutate: func(_ *corev1.PersistentVolume, _ *corev1.PersistentVolumeClaim) []client.Object {
				return []client.Object{&corev1.PersistentVolumeClaim{
					ObjectMeta: metav1.ObjectMeta{Name: "forklift-claim", Namespace: settleNS},
					Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: settlePV},
				}}
			},
		},
		{
			name: "CDI has not adopted the claim yet",
			mutate: func(_ *corev1.PersistentVolume, claim *corev1.PersistentVolumeClaim) []client.Object {
				claim.OwnerReferences = nil
				return nil
			},
		},
		{
			// An owner besides the VMDisk's DataVolume is a deletion that is
			// not the tenant's — the scaffolding VM being the obvious one.
			name: "claim has another owner",
			mutate: func(_ *corev1.PersistentVolume, claim *corev1.PersistentVolumeClaim) []client.Object {
				claim.OwnerReferences = append(claim.OwnerReferences, metav1.OwnerReference{
					APIVersion: "kubevirt.io/v1", Kind: "VirtualMachine", Name: "test-vm", UID: "vm-uid",
				})
				return nil
			},
		},
		{
			name: "owner reference points at a previous DataVolume",
			mutate: func(_ *corev1.PersistentVolume, claim *corev1.PersistentVolumeClaim) []client.Object {
				claim.OwnerReferences[0].UID = "an-older-dv-uid"
				return nil
			},
		},
		{
			name:   "VMDisk DataVolume missing",
			mutate: func(*corev1.PersistentVolume, *corev1.PersistentVolumeClaim) []client.Object { return nil },
			skipDV: true,
		},
		{
			name:     "VMDisk missing",
			mutate:   func(*corev1.PersistentVolume, *corev1.PersistentVolumeClaim) []client.Object { return nil },
			skipDisk: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := testScheme(t)
			tk := task("import", settleNS, "vcenter", "replicated")
			pv, claim, dv, disk := settledHandoff(tk)
			extra := tc.mutate(pv, claim)
			deletePolicy := corev1.PersistentVolumeReclaimDelete
			objs := []client.Object{tk, pv, claim, classWithPolicy("replicated", &deletePolicy)}
			if !tc.skipDV {
				objs = append(objs, dv)
			}
			if !tc.skipDisk {
				objs = append(objs, disk)
			}
			objs = append(objs, extra...)
			c := clientfake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
			r := &VMImportTaskReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(10)}

			settled, err := r.settleReclaimPolicy(context.Background(), tk, "vm-1", settleDisk, "forklift-claim")
			if err != nil {
				t.Fatalf("settleReclaimPolicy: %v", err)
			}
			if settled {
				t.Error("reported settled while the handoff is still open")
			}
			if got := reclaimPolicyOf(t, c); got != corev1.PersistentVolumeReclaimRetain {
				t.Errorf("reclaim policy = %q while the handoff is open, want Retain", got)
			}
		})
	}
}

// End to end over fulfill: the first pass performs the swap and must leave the
// volume on Retain, with no instance yet and the task still Creating. Once the
// PV controller and CDI have done their part, the next pass restores the class
// policy and only then finishes the import, so a VMDisk deleted afterwards
// takes its volume with it.
func TestFulfillRestoresTheReclaimPolicyBeforeFinishing(t *testing.T) {
	ctx := context.Background()
	s := testScheme(t)
	req := migrationv1alpha1.VMImportRequest{ID: "vm-1", Name: "web-01"}
	tk := task("import", settleNS, "vcenter", "replicated", req)

	plan := newObject(planGVK)
	plan.SetName(planName("import", "vm-1"))
	plan.SetNamespace(settleNS)
	plan.SetUID("plan-uid")

	vm := newObject(virtualMachineGVK)
	vm.SetName("test-vm")
	vm.SetNamespace(settleNS)
	vm.SetLabels(map[string]string{"plan": "plan-uid"})
	if err := unstructured.SetNestedSlice(vm.Object, []interface{}{
		map[string]interface{}{"name": "disk0", "dataVolume": map[string]interface{}{"name": "forklift-claim"}},
	}, "spec", "template", "spec", "volumes"); err != nil {
		t.Fatalf("build vm: %v", err)
	}

	transferred := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "forklift-claim", Namespace: settleNS},
		Spec: corev1.PersistentVolumeClaimSpec{
			VolumeName: settlePV,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("16Gi")},
			},
		},
		Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
	}
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: settlePV},
		Spec: corev1.PersistentVolumeSpec{
			StorageClassName:              "replicated",
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
			Capacity:                      corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("16Gi")},
			ClaimRef: &corev1.ObjectReference{
				Kind: "PersistentVolumeClaim", APIVersion: "v1",
				Namespace: settleNS, Name: "forklift-claim",
			},
		},
		Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound},
	}
	deletePolicy := corev1.PersistentVolumeReclaimDelete
	c := clientfake.NewClientBuilder().WithScheme(s).
		WithObjects(tk, plan, vm, transferred, pv, classWithPolicy("replicated", &deletePolicy)).Build()
	r := &VMImportTaskReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(10)}

	out, err := r.fulfill(ctx, tk, &req)
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if out != nil {
		t.Fatal("the first pass finished the import with the volume still on the swap's Retain")
	}
	if got := reclaimPolicyOf(t, c); got != corev1.PersistentVolumeReclaimRetain {
		t.Fatalf("reclaim policy during the swap = %q, want Retain", got)
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: settleNS, Name: "web-01"}, newObject(vmInstanceGVK)); !apierrors.IsNotFound(err) {
		t.Errorf("a VMInstance exists over a disk that is not settled (err %v)", err)
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: settleNS, Name: "test-vm"}, newObject(virtualMachineGVK)); err != nil {
		t.Errorf("the scaffolding VM was removed before the disks settled: %v", err)
	}

	// What the PV controller and CDI do between the two passes.
	claim := &corev1.PersistentVolumeClaim{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: settleNS, Name: settleTarget}, claim); err != nil {
		t.Fatalf("get replacement claim: %v", err)
	}
	dv := newObject(dataVolumeGVK)
	if err := c.Get(ctx, types.NamespacedName{Namespace: settleNS, Name: settleTarget}, dv); err != nil {
		t.Fatalf("get VMDisk DataVolume: %v", err)
	}
	controller := true
	claim.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: "cdi.kubevirt.io/v1beta1", Kind: "DataVolume",
		Name: settleTarget, UID: dv.GetUID(), Controller: &controller,
	}}
	if err := c.Update(ctx, claim); err != nil {
		t.Fatalf("adopt replacement claim: %v", err)
	}
	claim.Status.Phase = corev1.ClaimBound
	if err := c.Status().Update(ctx, claim); err != nil {
		t.Fatalf("bind replacement claim: %v", err)
	}
	gotPV := &corev1.PersistentVolume{}
	if err := c.Get(ctx, types.NamespacedName{Name: settlePV}, gotPV); err != nil {
		t.Fatalf("get pv: %v", err)
	}
	gotPV.Status.Phase = corev1.VolumeBound
	if err := c.Status().Update(ctx, gotPV); err != nil {
		t.Fatalf("bind pv: %v", err)
	}

	out, err = r.fulfill(ctx, tk, &req)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if out == nil {
		t.Fatal("the second pass did not finish an import whose handoff is over")
	}
	if got := reclaimPolicyOf(t, c); got != corev1.PersistentVolumeReclaimDelete {
		t.Errorf("reclaim policy after the import = %q, want the class's Delete — deleting the VMDisk would leak the volume", got)
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: settleNS, Name: "web-01"}, newObject(vmInstanceGVK)); err != nil {
		t.Errorf("VMInstance was not created once the disks settled: %v", err)
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: settleNS, Name: "test-vm"}, newObject(virtualMachineGVK)); !apierrors.IsNotFound(err) {
		t.Errorf("the scaffolding VM survived a finished import (err %v)", err)
	}
}
