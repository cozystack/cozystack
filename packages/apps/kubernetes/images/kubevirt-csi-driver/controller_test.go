package main

import (
	"context"
	"errors"
	"flag"
	"testing"
	"time"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	kubevirtv1 "kubevirt.io/api/core/v1"

	kubevirtclient "kubevirt.io/csi-driver/pkg/kubevirt"
	"kubevirt.io/csi-driver/pkg/service"
	"kubevirt.io/csi-driver/pkg/util"
)

const (
	testInfraNamespace = "tenant-test"
	testVMName         = "worker-0"
	testVolumeName     = "pvc-0"
)

// teardownClient models the CAPI/CAPK teardown race: the VM is gone while its
// VMI still reports the hot-plugged volume. The VMI drops the volume once
// RemoveVolumeFromVMI is called, or when the garbage collector deletes a
// VMI whose controlling VM is gone.
type teardownClient struct {
	kubevirtclient.Client
	vmiOwners   []metav1.OwnerReference
	vmiRemovals int
	lookups     int
}

func (c *teardownClient) vmiGarbageCollected() bool {
	owner := metav1.GetControllerOf(&metav1.ObjectMeta{OwnerReferences: c.vmiOwners})
	return owner != nil && owner.Kind == "VirtualMachine" && owner.APIVersion == kubevirtv1.SchemeGroupVersion.String()
}

func (c *teardownClient) GetWorkloadManagingVirtualMachine(context.Context, string, string) (*kubevirtv1.VirtualMachine, error) {
	c.lookups++
	return nil, k8serrors.NewNotFound(schema.GroupResource{Group: "kubevirt.io", Resource: "virtualmachines"}, testVMName)
}

func (c *teardownClient) GetVirtualMachine(context.Context, string, string) (*kubevirtv1.VirtualMachineInstance, error) {
	c.lookups++
	vmi := &kubevirtv1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{Name: testVMName, Namespace: testInfraNamespace, OwnerReferences: c.vmiOwners},
	}
	if c.vmiRemovals == 0 {
		vmi.Status.VolumeStatus = []kubevirtv1.VolumeStatus{{Name: testVolumeName, HotplugVolume: &kubevirtv1.HotplugVolumeStatus{}}}
	}
	return vmi, nil
}

func (c *teardownClient) EnsureVolumeRemovedVMI(context.Context, string, string, string) (bool, error) {
	return c.vmiRemovals > 0, nil
}

func (c *teardownClient) RemoveVolumeFromVMI(context.Context, string, string, *kubevirtv1.RemoveVolumeOptions) error {
	c.vmiRemovals++
	return nil
}

func (c *teardownClient) EnsureVolumeRemoved(context.Context, string, string, string, time.Duration) error {
	if c.vmiRemovals > 0 || c.vmiGarbageCollected() {
		return nil
	}
	return errors.New("timed out waiting for the VMI to drop the volume")
}

func vmOwner() []metav1.OwnerReference {
	return []metav1.OwnerReference{
		*metav1.NewControllerRef(&kubevirtv1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Name: testVMName}}, kubevirtv1.VirtualMachineGroupVersionKind),
	}
}

// The flag has to reach the upstream controller service, which alone decides
// whether to call the VMI-level removevolume. A VMI no VM controls gets the
// call either way, since virt-api accepts it there.
func TestControllerUnpublishVolumeHonoursVMIHotplugFallback(t *testing.T) {
	const name = "enable-vmi-hotplug-fallback"
	tests := []struct {
		name         string
		args         []string
		owners       []metav1.OwnerReference
		wantRemovals int
	}{
		{name: "default, VM-owned VMI", args: nil, owners: vmOwner(), wantRemovals: 1},
		{name: "disabled, VM-owned VMI", args: []string{"--" + name + "=false"}, owners: vmOwner(), wantRemovals: 0},
		{name: "disabled, orphaned VMI", args: []string{"--" + name + "=false"}, owners: nil, wantRemovals: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Cleanup(func() {
				_ = flag.Set(name, flag.Lookup(name).DefValue)
				_ = flag.Set("infra-cluster-namespace", flag.Lookup("infra-cluster-namespace").DefValue)
			})
			args := append([]string{"--infra-cluster-namespace=" + testInfraNamespace}, tt.args...)
			if err := flag.CommandLine.Parse(args); err != nil {
				t.Fatalf("parse flags: %v", err)
			}

			client := &teardownClient{vmiOwners: tt.owners}
			infra := fake.NewClientset(&corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{Name: testVolumeName, Namespace: testInfraNamespace},
				Spec:       corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}},
			})
			enforcement := util.StorageClassEnforcement{AllowAll: true, AllowDefault: true}
			w := newWrappedControllerService(service.NewKubevirtCSIDriver(), client, infra, nil, nil, enforcement)

			req := &csi.ControllerUnpublishVolumeRequest{VolumeId: testVolumeName, NodeId: testInfraNamespace + "/" + testVMName}
			if _, err := w.ControllerUnpublishVolume(context.Background(), req); err != nil {
				t.Fatalf("ControllerUnpublishVolume() error = %v", err)
			}
			if client.vmiRemovals != tt.wantRemovals {
				t.Errorf("RemoveVolumeFromVMI calls = %d, want %d", client.vmiRemovals, tt.wantRemovals)
			}
		})
	}
}

func nfsCNP(owners ...string) *unstructured.Unstructured {
	refs := make([]interface{}, 0, len(owners))
	for _, owner := range owners {
		refs = append(refs, map[string]interface{}{
			"apiVersion": kubevirtv1.SchemeGroupVersion.String(), "kind": "VirtualMachineInstance", "name": owner, "uid": owner,
		})
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "cilium.io/v2",
		"kind":       "CiliumNetworkPolicy",
		"metadata":   map[string]interface{}{"name": "csi-nfs-" + testVolumeName, "namespace": testInfraNamespace, "ownerReferences": refs},
	}}
}

// An RWX filesystem volume is served over NFS, not hot-plugged, so unpublish
// only releases this node from the volume's CiliumNetworkPolicy and never
// reaches the upstream hot-unplug.
func TestControllerUnpublishVolumeNFSReleasesNetworkPolicyOnly(t *testing.T) {
	tests := []struct {
		name       string
		owners     []string
		wantOwners []string
	}{
		{name: "other nodes still mount it", owners: []string{testVMName, "worker-1"}, wantOwners: []string{"worker-1"}},
		{name: "last node", owners: []string{testVMName}, wantOwners: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &teardownClient{vmiOwners: vmOwner()}
			filesystem := corev1.PersistentVolumeFilesystem
			infra := fake.NewClientset(&corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{Name: testVolumeName, Namespace: testInfraNamespace},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
					VolumeMode:  &filesystem,
				},
			})
			dynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
				map[schema.GroupVersionResource]string{ciliumNetworkPolicyGVR: "CiliumNetworkPolicyList"}, nfsCNP(tt.owners...))
			w := &WrappedControllerService{
				ControllerService: service.NewControllerService(client, testInfraNamespace, nil, util.StorageClassEnforcement{AllowAll: true}, true),
				infraClient:       infra,
				dynamicClient:     dynamic,
				virtClient:        client,
				infraNamespace:    testInfraNamespace,
			}

			req := &csi.ControllerUnpublishVolumeRequest{VolumeId: testVolumeName, NodeId: testInfraNamespace + "/" + testVMName}
			if _, err := w.ControllerUnpublishVolume(context.Background(), req); err != nil {
				t.Fatalf("ControllerUnpublishVolume() error = %v", err)
			}
			if client.lookups != 0 || client.vmiRemovals != 0 {
				t.Errorf("VM/VMI lookups = %d, RemoveVolumeFromVMI calls = %d, want none", client.lookups, client.vmiRemovals)
			}

			cnp, err := dynamic.Resource(ciliumNetworkPolicyGVR).Namespace(testInfraNamespace).Get(context.Background(), "csi-nfs-"+testVolumeName, metav1.GetOptions{})
			if tt.wantOwners == nil {
				if !k8serrors.IsNotFound(err) {
					t.Fatalf("CiliumNetworkPolicy still present after its last owner left: err = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("get CiliumNetworkPolicy: %v", err)
			}
			var got []string
			for _, ref := range cnp.GetOwnerReferences() {
				got = append(got, ref.Name)
			}
			if len(got) != len(tt.wantOwners) || got[0] != tt.wantOwners[0] {
				t.Errorf("CiliumNetworkPolicy owners = %v, want %v", got, tt.wantOwners)
			}
		})
	}
}
