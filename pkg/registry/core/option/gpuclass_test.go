// SPDX-License-Identifier: Apache-2.0

package option

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stypes "k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/utils/ptr"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu"
	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
)

func toUnstructured(t *testing.T, gvk schema.GroupVersionKind, obj any) *unstructured.Unstructured {
	t.Helper()
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		t.Fatalf("convert %T: %v", obj, err)
	}
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(gvk)
	return u
}

func gpuClass(t *testing.T, name string, mode gpuv1alpha1.GPUMode, resourceName string, mutate func(*gpuv1alpha1.GPUClass)) *unstructured.Unstructured {
	t.Helper()
	c := &gpuv1alpha1.GPUClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: gpuv1alpha1.GPUClassSpec{
			Vendor: "nvidia",
			Model:  "L40S",
			Mode:   mode,
			Source: gpuv1alpha1.GPUClassSource{DevicePlugin: &gpuv1alpha1.DevicePluginSource{ResourceName: resourceName}},
		},
	}
	if mutate != nil {
		mutate(c)
	}
	return toUnstructured(t, gvrGPUClasses.GroupVersion().WithKind("GPUClass"), c)
}

func gpuDevice(t *testing.T, namespace, name, class string, count int32, phase gpuv1alpha1.GPUDevicePhase, vmi string) *unstructured.Unstructured {
	t.Helper()
	d := &gpuv1alpha1.GPUDevice{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       gpuv1alpha1.GPUDeviceSpec{ClassName: class, Count: count},
		Status:     gpuv1alpha1.GPUDeviceStatus{Phase: phase},
	}
	if vmi != "" {
		d.Status.AttachedTo = &gpuv1alpha1.GPUDeviceAttachment{VMI: vmi}
	}
	return toUnstructured(t, gvrGPUDevices.GroupVersion().WithKind("GPUDevice"), d)
}

func tenantRelease(t *testing.T, namespace, name string, quotas map[string]any) *unstructured.Unstructured {
	t.Helper()
	hr := newObj(gvrHelmReleases.GroupVersion().WithKind("HelmRelease"), namespace, name, nil)
	_ = unstructured.SetNestedMap(hr.Object, map[string]any{"resourceQuotas": quotas}, "spec", "values")
	return hr
}

func tenantNamespace(t *testing.T, name string, entitled bool) *unstructured.Unstructured {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if entitled {
		ns.Labels = map[string]string{gpu.LabelNamespaceVGPU: gpu.LabelValueTrue}
	}
	return toUnstructured(t, gvrNamespaces.GroupVersion().WithKind("Namespace"), ns)
}

func gpuPod(t *testing.T, namespace, name, resourceName string, units int64, vmi string) *unstructured.Unstructured {
	t.Helper()
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, UID: k8stypes.UID("pod-uid-" + name)},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "compute",
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceName(resourceName): *resource.NewQuantity(units, resource.DecimalSI)},
				Limits:   corev1.ResourceList{corev1.ResourceName(resourceName): *resource.NewQuantity(units, resource.DecimalSI)},
			},
		}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	if vmi != "" {
		p.OwnerReferences = []metav1.OwnerReference{{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachineInstance", Name: vmi, UID: "uid-" + k8stypes.UID(vmi)}}
	}
	return toUnstructured(t, gvrPods.GroupVersion().WithKind("Pod"), p)
}

// gpuVMI is a VMI that lists the gpuPod pods named launchers as active.
func gpuVMI(namespace, name string, launchers ...string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{}}
	u.SetGroupVersionKind(gvrVMIs.GroupVersion().WithKind("VirtualMachineInstance"))
	u.SetNamespace(namespace)
	u.SetName(name)
	active := map[string]any{}
	for _, l := range launchers {
		active["pod-uid-"+l] = "node-a"
	}
	_ = unstructured.SetNestedMap(u.Object, active, "status", "activePods")
	return u
}

func newGPUDyn(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds(), objs...)
}

func gpuClassItems(t *testing.T, dyn *dynamicfake.FakeDynamicClient, namespace string) map[string]corev1alpha1.OptionItem {
	t.Helper()
	items, err := DefaultProviders(dyn)["gpuclass"](context.Background(), namespace)
	if err != nil {
		t.Fatalf("gpuclass provider: %v", err)
	}
	out := map[string]corev1alpha1.OptionItem{}
	for _, it := range items {
		out[it.Value] = it
	}
	return out
}

func wantInt(t *testing.T, field string, got *int64, want *int64) {
	t.Helper()
	switch {
	case got == nil && want == nil:
	case got == nil || want == nil:
		t.Errorf("%s = %v, want %v", field, ptrString(got), ptrString(want))
	case *got != *want:
		t.Errorf("%s = %d, want %d", field, *got, *want)
	}
}

func ptrString(p *int64) string {
	if p == nil {
		return "absent"
	}
	return resource.NewQuantity(*p, resource.DecimalSI).String()
}

func TestGPUClassProviderCatalog(t *testing.T) {
	dyn := newGPUDyn(
		tenantNamespace(t, "tenant-foo", false),
		gpuClass(t, "a100", gpuv1alpha1.GPUModePassthrough, "nvidia.com/GA100", func(c *gpuv1alpha1.GPUClass) {
			c.Spec.DisplayName = "NVIDIA A100, passthrough"
			c.Status.Free = ptr.To[int32](3)
			c.Status.MaxFreePerNode = ptr.To[int32](2)
		}),
		gpuClass(t, "l40s-24c", gpuv1alpha1.GPUModeVGPU, "nvidia.com/NVIDIA_L40S-24C", func(c *gpuv1alpha1.GPUClass) {
			c.Spec.MaxCount = ptr.To[int32](2)
			c.Spec.Profile = "L40S-24C"
		}),
		gpuClass(t, "l40s-48c", gpuv1alpha1.GPUModeVGPU, "nvidia.com/NVIDIA_L40S-48C", nil),
		gpuClass(t, "retired", gpuv1alpha1.GPUModePassthrough, "nvidia.com/GA100", func(c *gpuv1alpha1.GPUClass) {
			c.DeletionTimestamp = &metav1.Time{Time: time.Unix(1700000000, 0)}
			c.Finalizers = []string{gpu.FinalizerClass}
		}),
	)
	items := gpuClassItems(t, dyn, "tenant-foo")

	if _, ok := items["retired"]; ok {
		t.Errorf("a deleted class takes no new orders and must not be offered")
	}
	a100 := items["a100"]
	if a100.Label != "NVIDIA A100, passthrough" {
		t.Errorf("a100 label = %q, want the displayName", a100.Label)
	}
	wantInt(t, "a100 free", a100.Free, ptr.To[int64](3))
	wantInt(t, "a100 maxFreePerNode", a100.MaxFreePerNode, ptr.To[int64](2))
	wantInt(t, "a100 maxCount", a100.MaxCount, nil)
	wantInt(t, "a100 remainingBudget", a100.RemainingBudget, nil)

	// vGPU without the entitlement: nothing can be ordered.
	l40s := items["l40s-24c"]
	wantInt(t, "l40s-24c maxCount", l40s.MaxCount, ptr.To[int64](2))
	wantInt(t, "l40s-24c remainingBudget", l40s.RemainingBudget, ptr.To[int64](0))
	wantInt(t, "l40s-24c free", l40s.Free, nil)
	wantInt(t, "l40s-48c maxCount", items["l40s-48c"].MaxCount, ptr.To[int64](1))
}

func TestGPUClassProviderRemainingBudgetOverAncestors(t *testing.T) {
	dyn := newGPUDyn(
		tenantNamespace(t, "tenant-foo-baz", true),
		gpuClass(t, "a100", gpuv1alpha1.GPUModePassthrough, "nvidia.com/GA100", nil),
		gpuClass(t, "l40s-24c", gpuv1alpha1.GPUModeVGPU, "nvidia.com/NVIDIA_L40S-24C", nil),
		// foo caps its subtree at 4; baz (the caller) adds a class key of its own.
		tenantRelease(t, "tenant-root", "tenant-foo", map[string]any{"gpu.cozystack.io/total": int64(4)}),
		tenantRelease(t, "tenant-foo", "tenant-baz", map[string]any{"gpu.cozystack.io/a100": int64(5)}),
		gpuDevice(t, "tenant-foo-bar", "sibling", "a100", 1, gpuv1alpha1.GPUDeviceBound, ""),
		gpuDevice(t, "tenant-foo", "pending", "a100", 2, gpuv1alpha1.GPUDevicePending, ""),
		gpuDevice(t, "tenant-other", "elsewhere", "a100", 3, gpuv1alpha1.GPUDeviceAttached, "vm"),
	)
	items := gpuClassItems(t, dyn, "tenant-foo-baz")

	wantInt(t, "a100 remainingBudget", items["a100"].RemainingBudget, ptr.To[int64](3))
	wantInt(t, "l40s-24c remainingBudget", items["l40s-24c"].RemainingBudget, ptr.To[int64](3))
}

func TestGPUClassProviderCountsRawNameUsage(t *testing.T) {
	dyn := newGPUDyn(
		tenantNamespace(t, "tenant-foo", false),
		tenantNamespace(t, "tenant-foo-bar", false),
		tenantNamespace(t, "tenant-foo-baz", false),
		tenantNamespace(t, "tenant-other", false),
		gpuClass(t, "a100", gpuv1alpha1.GPUModePassthrough, "nvidia.com/GA100", nil),
		tenantRelease(t, "tenant-root", "tenant-foo", map[string]any{"nvidia.com/GA100": int64(10), "gpu.cozystack.io/total": int64(20)}),
		// A raw key nearer the caller does not narrow the pods read to its
		// own subtree: foo's key still needs its siblings' pods.
		tenantRelease(t, "tenant-foo", "tenant-baz", map[string]any{"nvidia.com/GA100": int64(100), "gpu.cozystack.io/total": int64(100)}),
		// The caller's VM: its device covers both units its pod requests.
		gpuDevice(t, "tenant-foo-baz", "train", "a100", 2, gpuv1alpha1.GPUDeviceAttached, "vm1"),
		gpuPod(t, "tenant-foo-baz", "virt-launcher-vm1", "nvidia.com/GA100", 2, "vm1"),
		gpuVMI("tenant-foo-baz", "vm1", "virt-launcher-vm1"),
		// A raw-name VM of a sibling.
		gpuPod(t, "tenant-foo-bar", "virt-launcher-raw", "nvidia.com/GA100", 1, "raw"),
		// A Lost device still counts against budgets, so it covers its VMI's
		// pod like any attached device.
		gpuDevice(t, "tenant-foo-bar", "lost", "a100", 1, gpuv1alpha1.GPUDeviceLost, "vm3"),
		gpuPod(t, "tenant-foo-bar", "virt-launcher-vm3", "nvidia.com/GA100", 1, "vm3"),
		gpuVMI("tenant-foo-bar", "vm3", "virt-launcher-vm3"),
		// A pod naming vm3 that vm3 does not list as active is raw-name
		// usage, whatever its owner reference says.
		gpuPod(t, "tenant-foo-bar", "forged", "nvidia.com/GA100", 1, "vm3"),
		// Outside foo's subtree.
		gpuPod(t, "tenant-other", "virt-launcher-x", "nvidia.com/GA100", 3, "x"),
	)
	items := gpuClassItems(t, dyn, "tenant-foo-baz")

	// 10 - 3 (the devices) - 1 (the sibling's raw-name pod) - 1 (the pod
	// vm3 does not list); the covered virt-launcher pods add nothing.
	wantInt(t, "a100 remainingBudget", items["a100"].RemainingBudget, ptr.To[int64](5))

	var listed []string
	for _, a := range dyn.Actions() {
		if a.GetVerb() == "list" && a.GetResource() == gvrPods {
			listed = append(listed, a.GetNamespace())
		}
	}
	slices.Sort(listed)
	if want := []string{"tenant-foo", "tenant-foo-bar", "tenant-foo-baz"}; !slices.Equal(listed, want) {
		t.Errorf("pods listed in %q, want only the raw key's subtree %q", listed, want)
	}
}

func TestGPUClassProviderReadsNoPodsWithoutRawKeys(t *testing.T) {
	dyn := newGPUDyn(
		tenantNamespace(t, "tenant-foo", false),
		gpuClass(t, "a100", gpuv1alpha1.GPUModePassthrough, "nvidia.com/GA100", nil),
		tenantRelease(t, "tenant-root", "tenant-foo", map[string]any{"gpu.cozystack.io/total": int64(2), "nvidia.com/OTHER": int64(1)}),
	)
	for _, resource := range []string{"pods", "namespaces"} {
		dyn.PrependReactor("list", resource, func(clienttesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New(resource + " must not be listed")
		})
	}
	items := gpuClassItems(t, dyn, "tenant-foo")
	wantInt(t, "a100 remainingBudget", items["a100"].RemainingBudget, ptr.To[int64](2))
}

func TestGPUDeviceProviderListsOwnDevices(t *testing.T) {
	dyn := newGPUDyn(
		gpuDevice(t, "tenant-foo", "train-1", "a100", 1, gpuv1alpha1.GPUDeviceBound, ""),
		gpuDevice(t, "tenant-foo", "train-0", "a100", 1, gpuv1alpha1.GPUDevicePending, ""),
		gpuDevice(t, "tenant-bar", "theirs", "a100", 1, gpuv1alpha1.GPUDeviceBound, ""),
	)
	items, err := DefaultProviders(dyn)["gpudevice"](context.Background(), "tenant-foo")
	if err != nil {
		t.Fatalf("gpudevice provider: %v", err)
	}
	if got := values(items); !slices.Equal(got, []string{"train-0", "train-1"}) {
		t.Fatalf("gpudevice: got %v", got)
	}
}

// An absent remainingBudget means nothing limits the order, so a budget the
// source cannot read fails the source instead of reading as unlimited.
func TestGPUClassProviderFailsWhenBudgetsAreUnreadable(t *testing.T) {
	for _, tc := range []struct{ verb, resource string }{
		{"list", "gpuclasses"},
		{"get", "helmreleases"},
		{"list", "gpudevices"},
		{"get", "namespaces"},
		{"list", "namespaces"},
		{"list", "pods"},
		{"get", "virtualmachineinstances"},
	} {
		t.Run(tc.verb+" "+tc.resource, func(t *testing.T) {
			dyn := newGPUDyn(
				tenantNamespace(t, "tenant-foo", false),
				gpuClass(t, "a100", gpuv1alpha1.GPUModePassthrough, "nvidia.com/GA100", nil),
				// The raw key makes the source read pods too.
				tenantRelease(t, "tenant-root", "tenant-foo", map[string]any{"gpu.cozystack.io/total": int64(2), "nvidia.com/GA100": int64(2)}),
				gpuPod(t, "tenant-foo", "virt-launcher-raw", "nvidia.com/GA100", 1, "raw"),
			)
			dyn.PrependReactor(tc.verb, tc.resource, func(clienttesting.Action) (bool, runtime.Object, error) {
				return true, nil, errors.New("simulated API outage")
			})
			if items, err := DefaultProviders(dyn)["gpuclass"](context.Background(), "tenant-foo"); err == nil {
				t.Fatalf("want an error, got items %v", items)
			}
		})
	}
}

// A pod that requests no raw GPU name cannot change a raw-key count, so its
// VMI is never read: with a raw key on the root that would be one read per
// virt-launcher pod of the cluster.
func TestGPUClassProviderReadsNoVMIOfAPodWithoutRawNames(t *testing.T) {
	dyn := newGPUDyn(
		tenantNamespace(t, "tenant-foo", false),
		gpuClass(t, "a100", gpuv1alpha1.GPUModePassthrough, "nvidia.com/GA100", nil),
		tenantRelease(t, "tenant-root", "tenant-foo", map[string]any{"gpu.cozystack.io/total": int64(2), "nvidia.com/GA100": int64(2)}),
		gpuPod(t, "tenant-foo", "virt-launcher-cpu-only", "cpu", 1, "cpu-only"),
	)
	dyn.PrependReactor("get", "virtualmachineinstances", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("virtualmachineinstances must not be read")
	})
	items := gpuClassItems(t, dyn, "tenant-foo")
	wantInt(t, "a100 remainingBudget", items["a100"].RemainingBudget, ptr.To[int64](2))
}
