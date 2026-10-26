package lineage

import (
	"context"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

const capkNamespace = "tenant-demo"

// kubernetesAppMapper maps the cluster's HelmRelease to its Kubernetes
// application, as the webhook's ApplicationDefinition mapper does.
type kubernetesAppMapper struct{}

func (kubernetesAppMapper) Map(*helmv2.HelmRelease) (string, string, string, error) {
	return "apps.cozystack.io/v1alpha1", "Kubernetes", "kubernetes-", nil
}

func capkObject(apiVersion, kind, name string, labels map[string]string, owner *unstructured.Unstructured) *unstructured.Unstructured {
	o := &unstructured.Unstructured{}
	o.SetAPIVersion(apiVersion)
	o.SetKind(kind)
	o.SetNamespace(capkNamespace)
	o.SetName(name)
	o.SetLabels(labels)
	if owner != nil {
		o.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: owner.GetAPIVersion(), Kind: owner.GetKind(), Name: owner.GetName()}})
	}
	return o
}

// capkNodeChain builds the objects behind the virt-launcher pod of a
// Kubernetes worker node, as observed on a live cluster: the pod is owned by
// its VMI and the VMI by its VM, but CAPK leaves the VM without an
// ownerReference and points at its KubevirtMachine through labels only. From
// the KubevirtMachine up, ownerReferences run to the CAPI Cluster, which the
// cluster's HelmRelease labels.
func capkNodeChain(vmLabels map[string]string) (*unstructured.Unstructured, []runtime.Object, meta.RESTMapper) {
	app := capkObject("apps.cozystack.io/v1alpha1", "Kubernetes", "cluster-a", nil, nil)
	hr := capkObject(HRAPIVersion, HRKind, "kubernetes-cluster-a", nil, nil)
	cluster := capkObject("cluster.x-k8s.io/v1beta1", "Cluster", "kubernetes-cluster-a", map[string]string{HRLabel: "kubernetes-cluster-a"}, nil)
	md := capkObject("cluster.x-k8s.io/v1beta1", "MachineDeployment", "kubernetes-cluster-a-md0", nil, cluster)
	ms := capkObject("cluster.x-k8s.io/v1beta1", "MachineSet", "kubernetes-cluster-a-md0-x", nil, md)
	machine := capkObject("cluster.x-k8s.io/v1beta1", "Machine", "kubernetes-cluster-a-md0-x-y", nil, ms)
	kvm := capkObject(KubevirtMachineAPIVersion, KubevirtMachineKind, "kubernetes-cluster-a-md0-x-y", nil, machine)
	vm := capkObject(VMAPIVersion, VMKind, "kubernetes-cluster-a-md0-x-y", vmLabels, nil)
	vmi := capkObject("kubevirt.io/v1", "VirtualMachineInstance", "kubernetes-cluster-a-md0-x-y", nil, vm)
	pod := capkObject("v1", "Pod", "virt-launcher-kubernetes-cluster-a-md0-x-y-abcde", nil, vmi)

	objs := []runtime.Object{app, hr, cluster, md, ms, machine, kvm, vm, vmi}
	mapper := meta.NewDefaultRESTMapper(nil)
	for _, o := range objs {
		mapper.Add(o.GetObjectKind().GroupVersionKind(), meta.RESTScopeNamespace)
	}
	mapper.Add(schema.GroupVersionKind{Version: "v1", Kind: "Pod"}, meta.RESTScopeNamespace)
	return pod, objs, mapper
}

func walkNodePod(t *testing.T, vmLabels map[string]string) []ObjectID {
	t.Helper()
	pod, objs, mapper := capkNodeChain(vmLabels)
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), objs...)
	return WalkOwnershipGraph(context.Background(), dyn, mapper, kubernetesAppMapper{}, pod)
}

func TestWalkResolvesCAPKNodeVMToKubernetesApp(t *testing.T) {
	got := walkNodePod(t, map[string]string{
		KubevirtMachineNameLabel:      "kubernetes-cluster-a-md0-x-y",
		KubevirtMachineNamespaceLabel: capkNamespace,
	})
	want := ObjectID{APIVersion: "apps.cozystack.io/v1alpha1", Kind: "Kubernetes", Namespace: capkNamespace, Name: "cluster-a"}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("WalkOwnershipGraph(node pod) = %+v, want [%+v]", got, want)
	}
}

func TestWalkIgnoresKubevirtMachineOfAnotherNamespace(t *testing.T) {
	// A VM pointing at a KubevirtMachine outside its own namespace (an external
	// infrastructure cluster, or a forged label) must not be resolved through it.
	got := walkNodePod(t, map[string]string{
		KubevirtMachineNameLabel:      "kubernetes-cluster-a-md0-x-y",
		KubevirtMachineNamespaceLabel: "tenant-other",
	})
	if len(got) != 0 {
		t.Fatalf("WalkOwnershipGraph(node pod) = %+v, want no ancestors", got)
	}
}

func TestWalkStopsAtUnlabelledVM(t *testing.T) {
	// A standalone VM with no CAPK labels and no ownerReference has no ancestors.
	if got := walkNodePod(t, nil); len(got) != 0 {
		t.Fatalf("WalkOwnershipGraph(pod of standalone VM) = %+v, want no ancestors", got)
	}
}
