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

const testNamespace = "tenant-demo"

type kubernetesAppMapper struct{}

func (kubernetesAppMapper) Map(*helmv2.HelmRelease) (string, string, string, error) {
	return "apps.cozystack.io/v1alpha1", "Kubernetes", "kubernetes-", nil
}

func testObject(apiVersion, kind, name string, labels map[string]string, owner *unstructured.Unstructured) *unstructured.Unstructured {
	o := &unstructured.Unstructured{}
	o.SetAPIVersion(apiVersion)
	o.SetKind(kind)
	o.SetNamespace(testNamespace)
	o.SetName(name)
	o.SetLabels(labels)
	if owner != nil {
		o.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: owner.GetAPIVersion(), Kind: owner.GetKind(), Name: owner.GetName()}})
	}
	return o
}

// walkPodOfOrphan walks a pod owned through a VMI by a VM that has no
// ownerReference of its own, the shape a Kubernetes worker node has.
func walkPodOfOrphan(t *testing.T, vmLabels map[string]string) []ObjectID {
	t.Helper()
	app := testObject("apps.cozystack.io/v1alpha1", "Kubernetes", "cluster-a", nil, nil)
	hr := testObject(HRAPIVersion, HRKind, "kubernetes-cluster-a", nil, nil)
	vm := testObject("kubevirt.io/v1", "VirtualMachine", "worker", vmLabels, nil)
	vmi := testObject("kubevirt.io/v1", "VirtualMachineInstance", "worker", nil, vm)
	pod := testObject("v1", "Pod", "virt-launcher-worker-abcde", nil, vmi)

	objs := []runtime.Object{app, hr, vm, vmi}
	mapper := meta.NewDefaultRESTMapper(nil)
	for _, o := range objs {
		mapper.Add(o.GetObjectKind().GroupVersionKind(), meta.RESTScopeNamespace)
	}
	mapper.Add(schema.GroupVersionKind{Version: "v1", Kind: "Pod"}, meta.RESTScopeNamespace)
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), objs...)
	return WalkOwnershipGraph(context.Background(), dyn, mapper, kubernetesAppMapper{}, pod)
}

func TestWalkResolvesOrphanThroughHelmReleaseLabel(t *testing.T) {
	got := walkPodOfOrphan(t, map[string]string{HRLabel: "kubernetes-cluster-a"})
	want := ObjectID{APIVersion: "apps.cozystack.io/v1alpha1", Kind: "Kubernetes", Namespace: testNamespace, Name: "cluster-a"}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("WalkOwnershipGraph(pod) = %+v, want [%+v]", got, want)
	}
}

func TestWalkStopsAtUnlabelledOrphan(t *testing.T) {
	if got := walkPodOfOrphan(t, nil); len(got) != 0 {
		t.Fatalf("WalkOwnershipGraph(pod) = %+v, want no ancestors", got)
	}
}
