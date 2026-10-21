/*
Copyright 2026 The Cozystack Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package model

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
)

func TestTenantReleaseRef(t *testing.T) {
	tests := []struct {
		owned, wantName, wantNS string
		wantOK                  bool
	}{
		{"tenant-root", "tenant-root", "tenant-root", true},
		{"tenant-foo", "tenant-foo", "tenant-root", true},
		{"tenant-foo-bar", "tenant-bar", "tenant-foo", true},
		{"tenant-foo-bar-baz", "tenant-baz", "tenant-foo-bar", true},
		{"not-a-tenant", "", "", false},
	}
	for _, tc := range tests {
		name, ns, ok := TenantReleaseRef(tc.owned, TenantPrefix)
		if name != tc.wantName || ns != tc.wantNS || ok != tc.wantOK {
			t.Errorf("TenantReleaseRef(%q) = (%q, %q, %v), want (%q, %q, %v)", tc.owned, name, ns, ok, tc.wantName, tc.wantNS, tc.wantOK)
		}
		if ok {
			if back := TenantNamespaceOf(ns, name, TenantPrefix); back != tc.owned {
				t.Errorf("TenantNamespaceOf(%q, %q) = %q, want %q", ns, name, back, tc.owned)
			}
		}
	}
}

func TestDeclaredQuotas(t *testing.T) {
	q, err := DeclaredQuotas([]byte(`{"resourceQuotas":{"cpu":"4","gpu.cozystack.io/total":"2","nvidia.com/GH100_H100":"1"},"other":1}`))
	if err != nil {
		t.Fatal(err)
	}
	total, cpu := q["gpu.cozystack.io/total"], q["cpu"]
	if len(q) != 3 || total.Cmp(resource.MustParse("2")) != 0 || cpu.Cmp(resource.MustParse("4")) != 0 {
		t.Fatalf("quotas = %v, want every key as written", q)
	}
	if q, err := DeclaredQuotas(nil); err != nil || len(q) != 0 {
		t.Fatalf("no values = %v, %v", q, err)
	}
	if _, err := DeclaredQuotas([]byte(`{"resourceQuotas":{"cpu":"lots"}}`)); err == nil {
		t.Fatal("an unreadable budget must fail, not read as no budget")
	}
}

func TestPermittedResourceNames(t *testing.T) {
	kv := &unstructured.Unstructured{Object: map[string]any{}}
	if PermittedResourceNames(kv) != nil {
		t.Fatal("an unset table checks nothing")
	}
	_ = unstructured.SetNestedField(kv.Object, map[string]any{
		"pciHostDevices":  []any{map[string]any{"resourceName": "a"}},
		"mediatedDevices": []any{map[string]any{"resourceName": "b"}},
		"usb":             []any{map[string]any{"resourceName": "c"}},
	}, "spec", "configuration", "permittedHostDevices")
	if got := PermittedResourceNames(kv); !got.Equal(sets.New("a", "b")) {
		t.Fatalf("permitted = %v, want the PCI and mediated names only", sets.List(got))
	}
}

func TestCoverage(t *testing.T) {
	classes := map[string]Class{"h100": {Name: "h100", ResourceName: h100Name}}
	dev := func(name string, phase gpuv1alpha1.GPUDevicePhase, vmi string) *gpuv1alpha1.GPUDevice {
		d := &gpuv1alpha1.GPUDevice{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "tenant-a"},
			Spec:       gpuv1alpha1.GPUDeviceSpec{ClassName: "h100", Count: 2},
			Status:     gpuv1alpha1.GPUDeviceStatus{Phase: phase},
		}
		if vmi != "" {
			d.Status.AttachedTo = &gpuv1alpha1.GPUDeviceAttachment{VMI: vmi}
		}
		return d
	}
	devices := []*gpuv1alpha1.GPUDevice{
		dev("held", gpuv1alpha1.GPUDeviceAttached, "vm"),
		dev("lost", gpuv1alpha1.GPUDeviceLost, "vm"),
		dev("free", gpuv1alpha1.GPUDeviceBound, ""),
	}
	k := types.NamespacedName{Namespace: "tenant-a", Name: "vm"}
	if got := CoveredForBudget(devices, classes)[k][h100Name]; got != 4 {
		t.Errorf("budget coverage = %d, want 4: a Lost device still counts against budgets", got)
	}
	if got := CoveredForCapacity(devices, classes)[k][h100Name]; got != 2 {
		t.Errorf("capacity coverage = %d, want 2: a Lost device holds no units", got)
	}
}

func TestLauncherVMI(t *testing.T) {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-a", OwnerReferences: []metav1.OwnerReference{
		{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "rs"},
		{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachineInstance", Name: "vm"},
	}}}
	if k, ok := LauncherVMI(p); !ok || k != (types.NamespacedName{Namespace: "tenant-a", Name: "vm"}) {
		t.Fatalf("LauncherVMI = %v, %v", k, ok)
	}
	p.OwnerReferences[1].APIVersion = "example.com/v1"
	if _, ok := LauncherVMI(p); ok {
		t.Fatal("a VirtualMachineInstance kind of another group is not KubeVirt's")
	}
}
