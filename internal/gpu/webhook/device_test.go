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

package webhook

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu"
)

func order(className string, count int32) *gpuv1alpha1.GPUDevice {
	return &gpuv1alpha1.GPUDevice{
		TypeMeta:   metav1.TypeMeta{APIVersion: "gpu.cozystack.io/v1alpha1", Kind: "GPUDevice"},
		ObjectMeta: metav1.ObjectMeta{Name: "order", Namespace: tenantNS},
		Spec:       gpuv1alpha1.GPUDeviceSpec{ClassName: className, Count: count},
	}
}

func TestValidateDevice(t *testing.T) {
	deletedClass := class("gone", gpuv1alpha1.GPUModePassthrough, h100RN)
	deleting(deletedClass)
	pinned := order("h100", 1)
	pinned.Labels = map[string]string{gpu.LabelTemplateDevice: gpu.LabelValueTrue}

	tests := []struct {
		name   string
		objs   []client.Object
		order  *gpuv1alpha1.GPUDevice
		denied []string
	}{
		{
			name:  "an order within budget and capacity is admitted",
			objs:  []client.Object{class("h100", gpuv1alpha1.GPUModePassthrough, h100RN, freeUnits(4, 2))},
			order: order("h100", 2),
		},
		{
			name:   "a missing class is refused",
			order:  order("h100", 1),
			denied: []string{"GPUClass h100", "does not exist"},
		},
		{
			name:   "a deleted class takes no new orders",
			objs:   []client.Object{deletedClass},
			order:  order("gone", 1),
			denied: []string{"GPUClass gone", "being deleted"},
		},
		{
			name:   "an order above maxCount is refused",
			objs:   []client.Object{class("h100", gpuv1alpha1.GPUModePassthrough, h100RN, maxCount(2))},
			order:  order("h100", 3),
			denied: []string{"3 of GPUClass h100", "maxCount 2"},
		},
		{
			name:   "a vGPU class allows one per VM when maxCount is unset",
			objs:   []client.Object{class("l40s", gpuv1alpha1.GPUModeVGPU, l40sRN), namespace(tenantNS, true)},
			order:  order("l40s", 2),
			denied: []string{"2 of GPUClass l40s", "maxCount 1"},
		},
		{
			name:   "an order above the class's free units is refused",
			objs:   []client.Object{class("h100", gpuv1alpha1.GPUModePassthrough, h100RN, freeUnits(3, 3))},
			order:  order("h100", 4),
			denied: []string{"4 of GPUClass h100", "the class has 3 free units"},
		},
		{
			name:   "an order above the free units of one node is refused",
			objs:   []client.Object{class("h100", gpuv1alpha1.GPUModePassthrough, h100RN, freeUnits(4, 2))},
			order:  order("h100", 3),
			denied: []string{"3 of GPUClass h100", "no node has more than 2 free units"},
		},
		{
			name: "an order above a tenant budget is refused",
			objs: []client.Object{
				class("h100", gpuv1alpha1.GPUModePassthrough, h100RN),
				tenantRelease(t, tenantNS, map[string]string{"gpu.cozystack.io/h100": "1"}),
				device("held", "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeA),
			},
			order:  order("h100", 1),
			denied: []string{"1 of GPUClass h100", "budget gpu.cozystack.io/h100 of tenant tenant-acme is 1"},
		},
		{
			name: "an ancestor's total caps the subtree",
			objs: []client.Object{
				class("h100", gpuv1alpha1.GPUModePassthrough, h100RN),
				tenantRelease(t, "tenant-root", map[string]string{"gpu.cozystack.io/total": "2"}),
				&gpuv1alpha1.GPUDevice{
					ObjectMeta: metav1.ObjectMeta{Name: "sibling", Namespace: "tenant-other"},
					Spec:       gpuv1alpha1.GPUDeviceSpec{ClassName: "h100", Count: 2},
					Status:     gpuv1alpha1.GPUDeviceStatus{Phase: gpuv1alpha1.GPUDeviceAttached, NodeName: nodeA},
				},
			},
			order:  order("h100", 1),
			denied: []string{"budget gpu.cozystack.io/total of tenant tenant-root is 2"},
		},
		{
			name: "a raw key counts the raw-name pods it covers",
			objs: []client.Object{
				class("h100", gpuv1alpha1.GPUModePassthrough, h100RN),
				tenantRelease(t, tenantNS, map[string]string{h100RN: "2", "gpu.cozystack.io/total": "4"}),
				namespace(tenantNS, false),
				rawPod("raw-vm", tenantNS, h100RN, 2),
			},
			order:  order("h100", 1),
			denied: []string{"budget " + h100RN + " of tenant tenant-acme is 2 and 2 is in use"},
		},
		{
			name: "a raw key does not count GPUs a device already covers",
			objs: []client.Object{
				class("h100", gpuv1alpha1.GPUModePassthrough, h100RN),
				tenantRelease(t, tenantNS, map[string]string{h100RN: "2", "gpu.cozystack.io/total": "4"}),
				namespace(tenantNS, false),
				attached(device("held", "h100", 1, gpuv1alpha1.GPUDeviceAttached, nodeA), "vm-a"),
				launcherOf(rawPod("virt-launcher-vm-a", tenantNS, h100RN, 1), "vm-a"),
			},
			order: order("h100", 1),
		},
		{
			name:   "a vGPU class needs the entitlement",
			objs:   []client.Object{class("l40s", gpuv1alpha1.GPUModeVGPU, l40sRN), namespace(tenantNS, false)},
			order:  order("l40s", 1),
			denied: []string{"GPUClass l40s", "not entitled to vGPU"},
		},
		{
			name:  "an entitled tenant orders vGPU",
			objs:  []client.Object{class("l40s", gpuv1alpha1.GPUModeVGPU, l40sRN), namespace(tenantNS, true)},
			order: order("l40s", 1),
		},
		{
			name:  "a template device was judged by the VMI webhook",
			objs:  []client.Object{class("h100", gpuv1alpha1.GPUModePassthrough, h100RN, freeUnits(0, 0))},
			order: pinned,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHandler(newClient(t, tt.objs...))
			resp := h.ValidateDevice(context.Background(), createRequest(t, tt.order, false))
			if tt.denied == nil {
				mustAllow(t, resp)
				return
			}
			mustDeny(t, resp, tt.denied...)
		})
	}
}

func rawPod(name, ns, rn string, units int64) *corev1.Pod {
	q := *resourceQuantity(units)
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: corev1.PodSpec{NodeName: nodeA, Containers: []corev1.Container{{
			Name:      "compute",
			Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceName(rn): q}},
		}}},
	}
}

func launcherOf(p *corev1.Pod, vmi string) *corev1.Pod {
	p.OwnerReferences = []metav1.OwnerReference{{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachineInstance", Name: vmi, UID: "uid"}}
	return p
}

func TestTenantReleaseKey(t *testing.T) {
	for ns, want := range map[string][2]string{
		"tenant-root":    {"tenant-root", "tenant-root"},
		"tenant-acme":    {"tenant-root", "tenant-acme"},
		"tenant-acme-ml": {"tenant-acme", "tenant-ml"},
		"cozy-system":    {"", ""},
	} {
		if gotNS, gotName := tenantReleaseKey(ns); gotNS != want[0] || gotName != want[1] {
			t.Errorf("tenantReleaseKey(%s) = %s/%s, want %s/%s", ns, gotNS, gotName, want[0], want[1])
		}
	}
}
