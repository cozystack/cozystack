package main

import (
	"context"
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/record"
	kubevirtv1 "kubevirt.io/api/core/v1"
)

var (
	plansGVR       = schema.GroupVersionResource{Group: "forklift.konveyor.io", Version: "v1beta1", Resource: "plans"}
	migrationsGVR  = schema.GroupVersionResource{Group: "forklift.konveyor.io", Version: "v1beta1", Resource: "migrations"}
	vmsGVR         = schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"}
	vmInstancesGVR = schema.GroupVersionResource{Group: vmInstanceGroup, Version: vmInstanceVersion, Resource: "vminstances"}
	vmDisksGVR     = schema.GroupVersionResource{Group: vmDiskGroup, Version: vmDiskVersion, Resource: "vmdisks"}
	dataVolumesGVR = schema.GroupVersionResource{Group: "cdi.kubevirt.io", Version: "v1beta1", Resource: "datavolumes"}
	pvcsGVR        = schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}
)

func newForkliftObj(kind, namespace, name, uid string, ann map[string]string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]interface{}{}}
	u.SetAPIVersion("forklift.konveyor.io/v1beta1")
	u.SetKind(kind)
	u.SetNamespace(namespace)
	u.SetName(name)
	if uid != "" {
		u.SetUID(types.UID(uid))
	}
	if ann != nil {
		u.SetAnnotations(ann)
	}
	return u
}

func newMigration(namespace, name string, succeeded bool) *unstructured.Unstructured {
	u := newForkliftObj("Migration", namespace, name, "", nil)
	status := "False"
	if succeeded {
		status = "True"
	}
	_ = unstructured.SetNestedSlice(u.Object, []interface{}{
		map[string]interface{}{"type": "Succeeded", "status": status},
	}, "status", "conditions")
	return u
}

func fakeClient(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			plansGVR:       "PlanList",
			migrationsGVR:  "MigrationList",
			vmsGVR:         "VirtualMachineList",
			vmInstancesGVR: "VMInstanceList",
			vmDisksGVR:     "VMDiskList",
			dataVolumesGVR: "DataVolumeList",
			pvcsGVR:        "PersistentVolumeClaimList",
		},
		objs...,
	)
}

// newPVC builds an imported disk the way Forklift leaves it: a bare PVC owned by
// the VirtualMachine Forklift created, with blockOwnerDeletion set.
func newPVC(namespace, name, ownerVM string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]interface{}{}}
	u.SetAPIVersion("v1")
	u.SetKind("PersistentVolumeClaim")
	u.SetNamespace(namespace)
	u.SetName(name)
	if ownerVM != "" {
		block := true
		u.SetOwnerReferences([]metav1.OwnerReference{{
			APIVersion:         "kubevirt.io/v1",
			Kind:               "VirtualMachine",
			Name:               ownerVM,
			UID:                types.UID("vm-uid"),
			BlockOwnerDeletion: &block,
		}})
	}
	_ = unstructured.SetNestedField(u.Object, "16Gi", "spec", "resources", "requests", "storage")
	_ = unstructured.SetNestedField(u.Object, "replicated", "spec", "storageClassName")
	return u
}

func newVM(namespace, name string, labels map[string]string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]interface{}{}}
	u.SetAPIVersion("kubevirt.io/v1")
	u.SetKind("VirtualMachine")
	u.SetNamespace(namespace)
	u.SetName(name)
	u.SetLabels(labels)
	return u
}

// getTargetNamespace must apply the same cross-tenant guard end-to-end via the
// Plan annotation as the pure resolveTargetNamespace: a tenant-scoped Plan is
// confined to its own namespace regardless of the requested target.
func TestGetTargetNamespace(t *testing.T) {
	const annKey = "vm-import.cozystack.io/target-namespace"
	cases := []struct {
		name   string
		planNs string
		ann    map[string]string
		want   string
	}{
		{"tenant plan confined despite foreign target", "tenant-a", map[string]string{annKey: "tenant-b"}, "tenant-a"},
		{"tenant plan without annotation stays local", "tenant-a", nil, "tenant-a"},
		{"admin plan honors requested tenant", "cozy-forklift", map[string]string{annKey: "tenant-b"}, "tenant-b"},
		{"admin plan without annotation stays local", "cozy-forklift", nil, "cozy-forklift"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := newForkliftObj("Plan", tc.planNs, "p", "uid-1", tc.ann)
			if got := getTargetNamespace(plan); got != tc.want {
				t.Errorf("getTargetNamespace(%q) = %q, want %q", tc.planNs, got, tc.want)
			}
		})
	}
}

func TestResolvePlan(t *testing.T) {
	c := &AdoptionController{dynamicClient: fakeClient(
		newForkliftObj("Plan", "tenant-a", "import-1", "uid-aaa", nil),
		newForkliftObj("Plan", "cozy-forklift", "import-2", "uid-bbb", nil),
	)}
	if plan, ok := c.resolvePlan(context.Background(), "uid-bbb"); !ok || plan.GetName() != "import-2" || plan.GetNamespace() != "cozy-forklift" {
		t.Errorf("resolvePlan(uid-bbb) = %v ok=%v, want cozy-forklift/import-2 ok=true", plan, ok)
	}
	if _, ok := c.resolvePlan(context.Background(), "uid-unknown"); ok {
		t.Errorf("resolvePlan(unknown) ok=true, want false")
	}
}

// A tenant super-admin can create raw kubevirt VMs, so a VM carrying the UID of
// another tenant's Plan must never reach adoption: the target namespace is
// derived from the resolved Plan, so honoring the label would have this
// cluster-privileged controller write into the victim's namespace.
func TestGetForkliftVMsRejectsForeignPlanClaim(t *testing.T) {
	victimPlan := newForkliftObj("Plan", "tenant-victim", "import-1", "uid-victim", nil)
	_ = unstructured.SetNestedSlice(victimPlan.Object, []interface{}{
		map[string]interface{}{"id": "vm-100"},
	}, "spec", "vms")

	c := &AdoptionController{
		dynamicClient: fakeClient(
			victimPlan,
			newMigration("tenant-victim", "import-1", true),
			newVM("tenant-victim", "legit", map[string]string{"plan": "uid-victim", "vmID": "vm-100"}),
			newVM("tenant-attacker", "forged", map[string]string{"plan": "uid-victim", "vmID": "vm-100"}),
			newVM("tenant-victim", "unlisted", map[string]string{"plan": "uid-victim", "vmID": "vm-999"}),
		),
		planCache: make(map[string]*PlanCacheEntry),
		recorder:  record.NewFakeRecorder(10),
	}

	vms, err := c.getForkliftVMs(context.Background())
	if err != nil {
		t.Fatalf("getForkliftVMs: %v", err)
	}
	if len(vms) != 1 {
		t.Fatalf("got %d adoptable VMs, want 1: %+v", len(vms), vms)
	}
	if vms[0].Namespace != "tenant-victim" || vms[0].Name != "legit" {
		t.Errorf("adopted %s/%s, want tenant-victim/legit", vms[0].Namespace, vms[0].Name)
	}
}

// Same-namespace adoption must never release the source VM before the
// VMInstance exists: getForkliftVMs keys reconciliation on that VM, so a
// rejected create would leave the imported VM destroyed with nothing left to
// retry from.
func TestAdoptVMViaVMDisksKeepsSourceVMWhenCreateFails(t *testing.T) {
	client := fakeClient(newVM("tenant-a", "web", map[string]string{"plan": "uid-1"}))
	client.PrependReactor("create", "vminstances", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(vmInstancesGVR.GroupResource(), "web", errors.New("exceeded quota"))
	})
	c := &AdoptionController{dynamicClient: client, recorder: record.NewFakeRecorder(10)}

	vm := kubevirtv1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-a", Name: "web"}}
	if err := c.adoptVMViaVMDisks(context.Background(), vm, "tenant-a", "web", nil, nil, nil, "u1.medium", "ubuntu", "Always", "import-1"); err == nil {
		t.Fatal("adoptVMViaVMDisks succeeded, want the create error")
	}

	if _, err := client.Resource(vmsGVR).Namespace("tenant-a").Get(context.Background(), "web", metav1.GetOptions{}); err != nil {
		t.Errorf("source VM was released despite the failed create: %v", err)
	}
}

func TestAdoptVMViaVMDisksRemovesSourceVMAfterCreate(t *testing.T) {
	client := fakeClient(newVM("tenant-a", "web", map[string]string{"plan": "uid-1"}))
	c := &AdoptionController{dynamicClient: client, recorder: record.NewFakeRecorder(10)}

	vm := kubevirtv1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-a", Name: "web"}}
	if err := c.adoptVMViaVMDisks(context.Background(), vm, "tenant-a", "web", nil, nil, nil, "u1.medium", "ubuntu", "Always", "import-1"); err != nil {
		t.Fatalf("adoptVMViaVMDisks: %v", err)
	}

	if _, err := client.Resource(vmInstancesGVR).Namespace("tenant-a").Get(context.Background(), "web", metav1.GetOptions{}); err != nil {
		t.Errorf("VMInstance was not created: %v", err)
	}
	if _, err := client.Resource(vmsGVR).Namespace("tenant-a").Get(context.Background(), "web", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("source VM still present after adoption, got err %v", err)
	}
}

// A delete that failed on an earlier pass must be replayed, otherwise the source
// VM is re-listed forever while the VMInstance already exists. The pre-existing
// VMInstance carries this import's identity annotations, so it is recognised as
// our own earlier success.
func TestAdoptVMViaVMDisksReplaysDeleteWhenVMInstanceExists(t *testing.T) {
	existing := &unstructured.Unstructured{Object: map[string]interface{}{}}
	existing.SetAPIVersion(vmInstanceGroup + "/" + vmInstanceVersion)
	existing.SetKind(vmInstanceKind)
	existing.SetNamespace("tenant-a")
	existing.SetName("web")
	existing.SetAnnotations(map[string]string{
		"vm-import.cozystack.io/original-vm-name":      "web",
		"vm-import.cozystack.io/original-vm-namespace": "tenant-a",
	})

	client := fakeClient(existing, newVM("tenant-a", "web", map[string]string{"plan": "uid-1"}))
	c := &AdoptionController{dynamicClient: client, recorder: record.NewFakeRecorder(10)}

	vm := kubevirtv1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-a", Name: "web"}}
	if err := c.adoptVMViaVMDisks(context.Background(), vm, "tenant-a", "web", nil, nil, nil, "u1.medium", "ubuntu", "Always", "import-1"); err != nil {
		t.Fatalf("adoptVMViaVMDisks: %v", err)
	}
	if _, err := client.Resource(vmsGVR).Namespace("tenant-a").Get(context.Background(), "web", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("source VM still present, got err %v", err)
	}
}

// A pre-existing VMInstance that only shares the target name but was not created
// by this import (no matching identity annotations) must not be mistaken for an
// earlier success: releasing the source VM against a stranger's VMInstance would
// delete the imported VM and orphan its disks. Adoption fails and the source VM
// is left intact for the operator to resolve the collision.
func TestAdoptVMViaVMDisksRejectsForeignVMInstanceNameCollision(t *testing.T) {
	foreign := &unstructured.Unstructured{Object: map[string]interface{}{}}
	foreign.SetAPIVersion(vmInstanceGroup + "/" + vmInstanceVersion)
	foreign.SetKind(vmInstanceKind)
	foreign.SetNamespace("tenant-a")
	foreign.SetName("web") // no vm-import identity annotations: an unrelated tenant VMInstance

	client := fakeClient(foreign, newVM("tenant-a", "web", map[string]string{"plan": "uid-1"}))
	c := &AdoptionController{dynamicClient: client, recorder: record.NewFakeRecorder(10)}

	vm := kubevirtv1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-a", Name: "web"}}
	if err := c.adoptVMViaVMDisks(context.Background(), vm, "tenant-a", "web", nil, nil, nil, "u1.medium", "ubuntu", "Always", "import-1"); err == nil {
		t.Fatal("adoptVMViaVMDisks succeeded against a foreign VMInstance, want a refusal")
	}
	if _, err := client.Resource(vmsGVR).Namespace("tenant-a").Get(context.Background(), "web", metav1.GetOptions{}); err != nil {
		t.Errorf("source VM was released despite the name collision: %v", err)
	}
}

// Plans are resolved cluster-wide by UID, so an unindexed lookup costs a List
// of every Plan in the cluster per candidate VM, on every 15s tick.
func TestResolvePlanListsPlansOncePerPass(t *testing.T) {
	plan := newForkliftObj("Plan", "tenant-a", "import-1", "uid-1", nil)
	_ = unstructured.SetNestedSlice(plan.Object, []interface{}{
		map[string]interface{}{"id": "vm-100"},
	}, "spec", "vms")

	client := fakeClient(
		plan,
		newMigration("tenant-a", "import-1", true),
		newVM("tenant-a", "web-1", map[string]string{"plan": "uid-1", "vmID": "vm-100"}),
		newVM("tenant-a", "web-2", map[string]string{"plan": "uid-1", "vmID": "vm-100"}),
		newVM("tenant-a", "web-3", map[string]string{"plan": "uid-1", "vmID": "vm-100"}),
	)
	var planLists int
	client.PrependReactor("list", "plans", func(k8stesting.Action) (bool, runtime.Object, error) {
		planLists++
		return false, nil, nil
	})
	c := &AdoptionController{
		dynamicClient: client,
		planCache:     make(map[string]*PlanCacheEntry),
		recorder:      record.NewFakeRecorder(10),
	}

	vms, err := c.getForkliftVMs(context.Background())
	if err != nil {
		t.Fatalf("getForkliftVMs: %v", err)
	}
	if len(vms) != 3 {
		t.Fatalf("got %d adoptable VMs, want 3", len(vms))
	}
	if planLists != 1 {
		t.Errorf("listed Plans %d times for 3 VMs, want 1", planLists)
	}

	// A new pass must see Plans changed since the last one.
	c.planIndex = nil
	if _, err := c.getForkliftVMs(context.Background()); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if planLists != 2 {
		t.Errorf("listed Plans %d times over two passes, want 2", planLists)
	}
}

func TestIsMigrationComplete(t *testing.T) {
	cases := []struct {
		name string
		objs []runtime.Object
		want bool
	}{
		{"succeeded", []runtime.Object{newMigration("tenant-a", "p", true)}, true},
		{"not succeeded", []runtime.Object{newMigration("tenant-a", "p", false)}, false},
		{"missing migration", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &AdoptionController{dynamicClient: fakeClient(tc.objs...)}
			if got := c.isMigrationComplete(context.Background(), "tenant-a", "p"); got != tc.want {
				t.Errorf("isMigrationComplete = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsAdoptionEnabled(t *testing.T) {
	const annKey = "vm-import.cozystack.io/adoption-enabled"
	cases := []struct {
		name string
		ann  map[string]string
		plan bool
		want bool
	}{
		{"explicitly enabled", map[string]string{annKey: "true"}, true, true},
		{"explicitly disabled", map[string]string{annKey: "false"}, true, false},
		{"default when unset", nil, true, true},
		{"missing plan defaults disabled", nil, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var objs []runtime.Object
			if tc.plan {
				objs = append(objs, newForkliftObj("Plan", "tenant-a", "p", "uid-1", tc.ann))
			}
			c := &AdoptionController{
				dynamicClient: fakeClient(objs...),
				planCache:     make(map[string]*PlanCacheEntry),
			}
			if got := c.isAdoptionEnabled(context.Background(), "tenant-a", "p"); got != tc.want {
				t.Errorf("isAdoptionEnabled = %v, want %v", got, tc.want)
			}
		})
	}
}

// On the raw-copy path Forklift already wrote the disk into the target
// namespace. Cloning it into a VMDisk there would copy the whole disk a second
// time and leave two full copies behind for good, so the adoption must keep
// referencing the imported PVC instead.
func TestWrapDisksKeepsImportedPVCWhenAdoptingInPlace(t *testing.T) {
	client := fakeClient(newPVC("tenant-a", "web-disk-0", "web"))
	c := &AdoptionController{dynamicClient: client, recorder: record.NewFakeRecorder(10)}

	disks := []interface{}{map[string]interface{}{"name": "imported-0", "dvName": "web-disk-0"}}
	if err := c.wrapDisksAsVMDisks(context.Background(), "tenant-a", "tenant-a", "web", disks); err != nil {
		t.Fatalf("wrapDisksAsVMDisks: %v", err)
	}

	if got := disks[0].(map[string]interface{})["dvName"]; got != "web-disk-0" {
		t.Errorf("dvName = %v, want the imported PVC web-disk-0 (a clone was substituted)", got)
	}
	list, err := client.Resource(vmDisksGVR).Namespace("tenant-a").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing VMDisks: %v", err)
	}
	if len(list.Items) != 0 {
		t.Errorf("created %d VMDisk(s) for an in-place adoption, want none", len(list.Items))
	}
}

// The virt-v2v path still has to move the disk: conversion runs in a privileged
// namespace the tenant cannot read from, so there the clone is the point.
func TestWrapDisksClonesWhenCrossingNamespaces(t *testing.T) {
	client := fakeClient(newPVC("cozy-forklift", "web-disk-0", "web"))
	c := &AdoptionController{dynamicClient: client, recorder: record.NewFakeRecorder(10)}

	disks := []interface{}{map[string]interface{}{"name": "imported-0", "dvName": "web-disk-0"}}
	if err := c.wrapDisksAsVMDisks(context.Background(), "cozy-forklift", "tenant-a", "web", disks); err != nil {
		t.Fatalf("wrapDisksAsVMDisks: %v", err)
	}

	if got := disks[0].(map[string]interface{})["dvName"]; got != "vm-disk-web-imported-0" {
		t.Errorf("dvName = %v, want the VMDisk DataVolume vm-disk-web-imported-0", got)
	}
	if _, err := client.Resource(vmDisksGVR).Namespace("tenant-a").Get(context.Background(), "web-imported-0", metav1.GetOptions{}); err != nil {
		t.Errorf("VMDisk was not created for the cross-namespace adoption: %v", err)
	}
}

// The imported PVC must be detached from the Forklift VM BEFORE that VM is
// deleted. Forklift owns the PVC by the VM with blockOwnerDeletion precisely so
// the two are collected together, so if the order slips the garbage collector
// destroys a finished transfer -- the observed failure was a completed 16 GiB
// disk removed 357ms into adoption. The fake client runs no garbage collector,
// so the ordering itself is what this asserts.
func TestReleaseSourceVMDetachesImportedPVCBeforeDeletingVM(t *testing.T) {
	client := fakeClient(
		newVM("tenant-a", "web", map[string]string{"plan": "uid-1"}),
		newPVC("tenant-a", "web-disk-0", "web"),
	)

	c := &AdoptionController{dynamicClient: client, recorder: record.NewFakeRecorder(10)}
	vm := kubevirtv1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-a", Name: "web"}}
	disks := []interface{}{map[string]interface{}{"name": "imported-0", "dvName": "web-disk-0"}}

	if err := c.releaseSourceVM(context.Background(), vm, "tenant-a", "web", disks); err != nil {
		t.Fatalf("releaseSourceVM: %v", err)
	}

	detachedAt, deletedAt := -1, -1
	for i, a := range client.Actions() {
		if a.Matches("update", "persistentvolumeclaims") && detachedAt < 0 {
			detachedAt = i
		}
		if a.Matches("delete", "virtualmachines") && deletedAt < 0 {
			deletedAt = i
		}
	}
	if detachedAt < 0 {
		t.Fatal("imported PVC was never detached from the source VM")
	}
	if deletedAt < 0 {
		t.Fatal("source VM was never deleted")
	}
	if detachedAt > deletedAt {
		t.Errorf("PVC detached at action %d but VM deleted at %d: the garbage collector would take the migrated disk with the VM", detachedAt, deletedAt)
	}

	cur, err := client.Resource(pvcsGVR).Namespace("tenant-a").Get(context.Background(), "web-disk-0", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("imported PVC did not survive adoption: %v", err)
	}
	for _, ref := range cur.GetOwnerReferences() {
		if ref.Kind == "VirtualMachine" {
			t.Errorf("imported PVC still owned by VirtualMachine %s after adoption", ref.Name)
		}
	}
}

// Detaching is narrow on purpose: only ownerReferences pointing at the VM being
// released are dropped, so any other owner keeps its claim on the volume.
func TestReleaseImportedVolumesLeavesForeignOwnersAlone(t *testing.T) {
	pvc := newPVC("tenant-a", "web-disk-0", "web")
	pvc.SetOwnerReferences(append(pvc.GetOwnerReferences(), metav1.OwnerReference{
		APIVersion: "cdi.kubevirt.io/v1beta1",
		Kind:       "DataVolume",
		Name:       "someone-else",
		UID:        types.UID("dv-uid"),
	}))
	client := fakeClient(pvc)
	c := &AdoptionController{dynamicClient: client, recorder: record.NewFakeRecorder(10)}

	vm := kubevirtv1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-a", Name: "web"}}
	disks := []interface{}{map[string]interface{}{"name": "imported-0", "dvName": "web-disk-0"}}
	if err := c.releaseImportedVolumes(context.Background(), vm, disks); err != nil {
		t.Fatalf("releaseImportedVolumes: %v", err)
	}

	cur, err := client.Resource(pvcsGVR).Namespace("tenant-a").Get(context.Background(), "web-disk-0", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading PVC: %v", err)
	}
	refs := cur.GetOwnerReferences()
	if len(refs) != 1 || refs[0].Kind != "DataVolume" {
		t.Errorf("ownerReferences = %+v, want only the unrelated DataVolume owner", refs)
	}
}
