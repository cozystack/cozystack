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
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu"
)

func order(className string, count int32) *gpuv1alpha1.GPUClaim {
	return &gpuv1alpha1.GPUClaim{
		TypeMeta:   metav1.TypeMeta{APIVersion: "gpu.cozystack.io/v1alpha1", Kind: "GPUClaim"},
		ObjectMeta: metav1.ObjectMeta{Name: "order", Namespace: tenantNS},
		Spec:       gpuv1alpha1.GPUClaimSpec{ClassName: className, Count: count},
	}
}

func TestValidateClaim(t *testing.T) {
	deletedClass := class("gone", gpuv1alpha1.GPUModePassthrough, h100RN)
	deleting(deletedClass)
	launcherVMA := launcherOf(rawPod("virt-launcher-vm-a", tenantNS, h100RN, 1), "vm-a")
	// The tamper policy in cozystack-basics refuses the mark, whatever its
	// value, from anyone it does not trust, so a tenant cannot use it to
	// skip these checks.
	template := order("h100", 1)
	template.Labels = map[string]string{gpu.LabelTemplateClaim: gpu.LabelValueTrue}
	emptyMark := order("h100", 1)
	emptyMark.Labels = map[string]string{gpu.LabelTemplateClaim: ""}

	tests := []struct {
		name   string
		objs   []client.Object
		order  *gpuv1alpha1.GPUClaim
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
				claim("held", "h100", 1, gpuv1alpha1.GPUClaimBound, nodeA),
			},
			order:  order("h100", 1),
			denied: []string{"1 of GPUClass h100", "budget gpu.cozystack.io/h100 of tenant tenant-acme is 1"},
		},
		{
			name: "an ancestor's total caps the subtree",
			objs: []client.Object{
				class("h100", gpuv1alpha1.GPUModePassthrough, h100RN),
				tenantRelease(t, "tenant-root", map[string]string{"gpu.cozystack.io/total": "2"}),
				&gpuv1alpha1.GPUClaim{
					ObjectMeta: metav1.ObjectMeta{Name: "sibling", Namespace: "tenant-other"},
					Spec:       gpuv1alpha1.GPUClaimSpec{ClassName: "h100", Count: 2},
					Status:     gpuv1alpha1.GPUClaimStatus{Phase: gpuv1alpha1.GPUClaimAttached, NodeName: nodeA},
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
			name: "a raw key does not count GPUs a claim already covers",
			objs: []client.Object{
				class("h100", gpuv1alpha1.GPUModePassthrough, h100RN),
				tenantRelease(t, tenantNS, map[string]string{h100RN: "2", "gpu.cozystack.io/total": "4"}),
				namespace(tenantNS, false),
				attached(claim("held", "h100", 1, gpuv1alpha1.GPUClaimAttached, nodeA), "vm-a"),
				launcherVMA,
				activeVMI("vm-a", launcherVMA),
			},
			order: order("h100", 1),
		},
		{
			// A Lost claim still counts against the budget itself, so the
			// pod of its VMI must not count its units a second time.
			name: "a raw key does not count GPUs a Lost claim covers",
			objs: []client.Object{
				class("h100", gpuv1alpha1.GPUModePassthrough, h100RN),
				tenantRelease(t, tenantNS, map[string]string{h100RN: "2", "gpu.cozystack.io/total": "4"}),
				namespace(tenantNS, false),
				attached(claim("lost", "h100", 1, gpuv1alpha1.GPUClaimLost, nodeA), "vm-a"),
				launcherVMA,
				activeVMI("vm-a", launcherVMA),
			},
			order: order("h100", 1),
		},
		{
			// The pod names vm-a, but vm-a does not list it as active, so
			// its units are raw-name usage the claim does not cover.
			name: "a raw key counts a pod its named VMI does not list as active",
			objs: []client.Object{
				class("h100", gpuv1alpha1.GPUModePassthrough, h100RN),
				tenantRelease(t, tenantNS, map[string]string{h100RN: "2", "gpu.cozystack.io/total": "4"}),
				namespace(tenantNS, false),
				attached(claim("held", "h100", 1, gpuv1alpha1.GPUClaimAttached, nodeA), "vm-a"),
				launcherVMA,
				activeVMI("vm-a"),
			},
			order:  order("h100", 1),
			denied: []string{"budget " + h100RN + " of tenant tenant-acme is 2 and 2 is in use"},
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
			name: "a template claim skips the budget and capacity checks",
			objs: []client.Object{class("h100", gpuv1alpha1.GPUModePassthrough, h100RN, freeUnits(0, 0)),
				tenantRelease(t, tenantNS, map[string]string{"gpu.cozystack.io/total": "0"})},
			order: template,
		},
		{
			name: "the mark counts whatever its value",
			objs: []client.Object{class("h100", gpuv1alpha1.GPUModePassthrough, h100RN, freeUnits(0, 0)),
				tenantRelease(t, tenantNS, map[string]string{"gpu.cozystack.io/total": "0"})},
			order: emptyMark,
		},
		{
			name: "the same order without the mark is judged",
			objs: []client.Object{class("h100", gpuv1alpha1.GPUModePassthrough, h100RN, freeUnits(0, 0)),
				tenantRelease(t, tenantNS, map[string]string{"gpu.cozystack.io/total": "0"})},
			order:  order("h100", 1),
			denied: []string{"budget gpu.cozystack.io/total of tenant tenant-acme is 0"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHandler(newClient(t, tt.objs...))
			resp := h.ValidateClaim(context.Background(), createRequest(t, tt.order, false))
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
	p.UID = types.UID("uid-" + p.Name)
	p.OwnerReferences = []metav1.OwnerReference{{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachineInstance", Name: vmi, UID: "uid"}}
	return p
}

func resourceQuantity(n int64) *resource.Quantity { return resource.NewQuantity(n, resource.DecimalSI) }

// Every replica serves the webhook from its own informers while only the
// leader binds, so a lagging cache must not decide a refusal: here the
// cache has not seen the claim the leader bound, nor the cleared
// entitlement, and the class it caches still shows free units.
func TestValidateClaimJudgesPastTheCache(t *testing.T) {
	budget := tenantRelease(t, tenantNS, map[string]string{"gpu.cozystack.io/total": "1"})
	h100 := class("h100", gpuv1alpha1.GPUModePassthrough, h100RN)
	rawKey := tenantRelease(t, tenantNS, map[string]string{h100RN: "2", "gpu.cozystack.io/total": "4"})
	heldA := attached(claim("held", "h100", 1, gpuv1alpha1.GPUClaimAttached, nodeA), "vm-a")
	launcherVMA := launcherOf(rawPod("virt-launcher-vm-a", tenantNS, h100RN, 1), "vm-a")
	for name, tt := range map[string]struct {
		cache, live []client.Object
		order       *gpuv1alpha1.GPUClaim
		denied      string
	}{
		"a claim the cache has not seen bound": {
			cache:  []client.Object{class("h100", gpuv1alpha1.GPUModePassthrough, h100RN), budget, claim("held", "h100", 1, gpuv1alpha1.GPUClaimPending, "")},
			live:   []client.Object{class("h100", gpuv1alpha1.GPUModePassthrough, h100RN), budget, claim("held", "h100", 1, gpuv1alpha1.GPUClaimBound, nodeA)},
			order:  order("h100", 1),
			denied: "budget gpu.cozystack.io/total of tenant tenant-acme is 1 and 1 is in use",
		},
		"free units the cache still shows": {
			cache:  []client.Object{class("h100", gpuv1alpha1.GPUModePassthrough, h100RN, freeUnits(2, 2))},
			live:   []client.Object{class("h100", gpuv1alpha1.GPUModePassthrough, h100RN, freeUnits(0, 0))},
			order:  order("h100", 1),
			denied: "the class has 0 free units",
		},
		"a namespace of a raw key's subtree the cache has not seen": {
			cache:  []client.Object{h100, rawKey},
			live:   []client.Object{h100, rawKey, namespace(tenantNS, false), rawPod("raw-vm", tenantNS, h100RN, 2)},
			order:  order("h100", 1),
			denied: "budget " + h100RN + " of tenant tenant-acme is 2 and 2 is in use",
		},
		"a launcher the cache still lists as its VMI's": {
			cache:  []client.Object{h100, rawKey, namespace(tenantNS, false), heldA, launcherVMA, activeVMI("vm-a", launcherVMA)},
			live:   []client.Object{h100, rawKey, namespace(tenantNS, false), heldA, launcherVMA, activeVMI("vm-a")},
			order:  order("h100", 1),
			denied: "budget " + h100RN + " of tenant tenant-acme is 2 and 2 is in use",
		},
		"an entitlement the cache still shows": {
			cache:  []client.Object{class("l40s", gpuv1alpha1.GPUModeVGPU, l40sRN), namespace(tenantNS, true)},
			live:   []client.Object{class("l40s", gpuv1alpha1.GPUModeVGPU, l40sRN), namespace(tenantNS, false)},
			order:  order("l40s", 1),
			denied: "not entitled to vGPU",
		},
	} {
		t.Run(name, func(t *testing.T) {
			cached := newClient(t, tt.cache...)
			h := &Handler{Client: cached, Reader: newClient(t, tt.live...)}
			mustDeny(t, h.ValidateClaim(context.Background(), createRequest(t, tt.order, false)), tt.denied)
		})
	}
}

// A pod that requests no GPU cannot change a raw budget, so its VMI is not
// read: a VMI read that fails for such a pod does not fail the order.
func TestValidateClaimReadsNoVMIForPodsWithoutGPUs(t *testing.T) {
	plain := launcherOf(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "virt-launcher-plain", Namespace: tenantNS},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "compute"}}}}, "plain")
	live := newClient(t, class("h100", gpuv1alpha1.GPUModePassthrough, h100RN), namespace(tenantNS, false), plain,
		tenantRelease(t, tenantNS, map[string]string{h100RN: "2", "gpu.cozystack.io/total": "4"}))
	reader := interceptor.NewClient(live, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if u, ok := obj.(*unstructured.Unstructured); ok && u.GetKind() == vmiGVK.Kind {
				return errors.New("VMI read")
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})
	h := &Handler{Client: live, Reader: reader}
	mustAllow(t, h.ValidateClaim(context.Background(), createRequest(t, order("h100", 1), false)))
}
