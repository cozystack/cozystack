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

package policytest

import (
	"slices"
	"strings"
	"testing"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/authentication/user"

	"github.com/cozystack/cozystack/internal/gpu"
)

const (
	classPolicyFile    = "gpu-class-policy.yaml"
	classImmutability  = "cozystack-gpu-class-immutability"
	classDeletedOrders = "cozystack-gpu-class-deleted-orders"
)

var (
	classGVK = schema.GroupVersionKind{Group: gpu.Group, Version: "v1alpha1", Kind: "GPUClass"}
	claimGVK = schema.GroupVersionKind{Group: gpu.Group, Version: "v1alpha1", Kind: "GPUClaim"}
)

func classSpec() map[string]any {
	return map[string]any{
		"displayName": "NVIDIA L40S, 24 GB vGPU (compute)",
		"vendor":      "nvidia",
		"model":       "L40S",
		"mode":        "vgpu",
		"profile":     "L40S-24C",
		"memory":      "24Gi",
		"maxCount":    int64(2),
		"onCordon":    "Move",
		"nodeSelector": map[string]any{
			"gpu.cozystack.io/10de-26b9": "L40S-24C",
		},
		"source": map[string]any{
			"devicePlugin": map[string]any{"resourceName": "nvidia.com/NVIDIA_L40S-24C"},
		},
	}
}

func class(spec map[string]any, finalizers ...string) *unstructured.Unstructured {
	u := obj(classGVK.GroupVersion().String(), classGVK.Kind, "", "l40s-24c", map[string]any{"spec": spec})
	if len(finalizers) > 0 {
		u.SetFinalizers(finalizers)
	}
	return u
}

func TestClassImmutabilityMatch(t *testing.T) {
	p := get(t, classPolicyFile, classImmutability).policy
	rules := p.Spec.MatchConstraints.ResourceRules
	if len(rules) != 1 {
		t.Fatalf("want one resource rule, got %d", len(rules))
	}
	r := rules[0]
	if !slices.Equal(r.APIGroups, []string{gpu.Group}) || !slices.Equal(r.Resources, []string{"gpuclasses"}) {
		t.Errorf("rule = %+v, want gpuclasses in %s", r, gpu.Group)
	}
	if !slices.Equal(r.Operations, []admissionregistrationv1.OperationType{admissionregistrationv1.Update}) {
		t.Errorf("operations = %v, want [UPDATE]", r.Operations)
	}
}

func TestClassImmutability(t *testing.T) {
	v := compile(t, get(t, classPolicyFile, classImmutability).policy)
	held := gpu.FinalizerClass

	with := func(mutate func(map[string]any)) map[string]any {
		s := classSpec()
		mutate(s)
		return s
	}

	cases := []struct {
		name       string
		old, new   map[string]any
		finalizers []string
		user       user.Info
		denied     bool
	}{
		{name: "unchanged spec", old: classSpec(), new: classSpec(), finalizers: []string{held}},
		{name: "resourceName change", old: classSpec(), new: with(func(s map[string]any) {
			s["source"] = map[string]any{"devicePlugin": map[string]any{"resourceName": "nvidia.com/L40S-24C"}}
		}), finalizers: []string{held}},
		{name: "nodeSelector change", old: classSpec(), new: with(func(s map[string]any) {
			s["nodeSelector"] = map[string]any{"pool": "b"}
		}), finalizers: []string{held}},
		{name: "tolerations change", old: classSpec(), new: with(func(s map[string]any) {
			s["tolerations"] = []any{map[string]any{"key": "nvidia.com/gpu", "operator": "Exists", "effect": "NoSchedule"}}
		}), finalizers: []string{held}},
		{name: "onCordon change", old: classSpec(), new: with(func(s map[string]any) { s["onCordon"] = "Stay" }), finalizers: []string{held}},
		{name: "displayName change", old: classSpec(), new: with(func(s map[string]any) { s["displayName"] = "L40S" }), finalizers: []string{held}},
		{name: "vendor change", old: classSpec(), new: with(func(s map[string]any) { s["vendor"] = "amd" }), finalizers: []string{held}, denied: true},
		{name: "model change", old: classSpec(), new: with(func(s map[string]any) { s["model"] = "L40" }), finalizers: []string{held}, denied: true},
		{name: "profile change", old: classSpec(), new: with(func(s map[string]any) { s["profile"] = "L40S-48C" }), finalizers: []string{held}, denied: true},
		{name: "profile removed", old: classSpec(), new: with(func(s map[string]any) { delete(s, "profile") }), finalizers: []string{held}, denied: true},
		{name: "memory change", old: classSpec(), new: with(func(s map[string]any) { s["memory"] = "48Gi" }), finalizers: []string{held}, denied: true},
		{name: "maxCount change", old: classSpec(), new: with(func(s map[string]any) { s["maxCount"] = int64(4) }), finalizers: []string{held}, denied: true},
		{name: "maxCount set where unset", old: with(func(s map[string]any) { delete(s, "maxCount") }), new: classSpec(), finalizers: []string{held}, denied: true},
		{name: "maxCount cleared", old: classSpec(), new: with(func(s map[string]any) { delete(s, "maxCount") }), finalizers: []string{held}, denied: true},
		{name: "source kind change", old: classSpec(), new: with(func(s map[string]any) {
			s["source"] = map[string]any{"dra": map[string]any{"deviceClassName": "gpu.nvidia.com"}}
		}), finalizers: []string{held}, denied: true},
		{name: "second source member", old: classSpec(), new: with(func(s map[string]any) {
			s["source"] = map[string]any{
				"devicePlugin": map[string]any{"resourceName": "nvidia.com/NVIDIA_L40S-24C"},
				"dra":          map[string]any{"deviceClassName": "gpu.nvidia.com"},
			}
		}), finalizers: []string{held}, denied: true},
		{name: "cluster admin cannot change the meaning either", old: classSpec(), new: with(func(s map[string]any) { s["model"] = "L40" }), finalizers: []string{held}, user: clusterAdmin, denied: true},
		{name: "model change without the finalizer", old: classSpec(), new: with(func(s map[string]any) { s["model"] = "L40" })},
		{name: "model change with only a foreign finalizer", old: classSpec(), new: with(func(s map[string]any) { s["model"] = "L40" }), finalizers: []string{"example.com/other"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var u user.Info = clusterAdmin
			if tc.user != nil {
				u = tc.user
			}
			oldObj := class(tc.old, tc.finalizers...)
			newObj := class(tc.new, tc.finalizers...)
			denials := evaluate(t, v, request{gvk: classGVK, resource: "gpuclasses", obj: newObj, old: oldObj, user: u})
			if got := len(denials) > 0; got != tc.denied {
				t.Fatalf("denied = %v, want %v (%v)", got, tc.denied, denials)
			}
			for _, m := range denials {
				if !strings.Contains(m, "GPUClass l40s-24c") {
					t.Errorf("message must name the class, got %q", m)
				}
			}
		})
	}
}

// The finalizer is checked on the stored object, so removing it and
// changing the spec in one write is still refused.
func TestClassImmutabilityHoldsWhileTheFinalizerIsRemoved(t *testing.T) {
	v := compile(t, get(t, classPolicyFile, classImmutability).policy)
	newSpec := classSpec()
	newSpec["model"] = "L40"
	denials := evaluate(t, v, request{gvk: classGVK, resource: "gpuclasses", obj: class(newSpec), old: class(classSpec(), gpu.FinalizerClass), user: controllerSA})
	if len(denials) == 0 {
		t.Fatal("a spec change in the write that removes the finalizer must be refused")
	}
}

func TestClassDeletedOrdersBinding(t *testing.T) {
	s := get(t, classPolicyFile, classDeletedOrders)
	pk := s.policy.Spec.ParamKind
	if pk == nil || pk.APIVersion != classGVK.GroupVersion().String() || pk.Kind != classGVK.Kind {
		t.Fatalf("paramKind = %+v, want GPUClass", pk)
	}
	rules := s.policy.Spec.MatchConstraints.ResourceRules
	if len(rules) != 1 || !slices.Equal(rules[0].Resources, []string{"gpuclaims"}) ||
		!slices.Equal(rules[0].Operations, []admissionregistrationv1.OperationType{admissionregistrationv1.Create}) {
		t.Fatalf("rules = %+v, want CREATE of gpuclaims", rules)
	}
	ref := s.binding(t).Spec.ParamRef
	if ref == nil || ref.Name != "" || ref.Selector == nil || len(ref.Selector.MatchLabels) != 0 || len(ref.Selector.MatchExpressions) != 0 {
		t.Fatalf("paramRef = %+v, want an empty selector that matches every class", ref)
	}
	// No class at all is not a reason to refuse: an unknown class is the
	// order webhook's to refuse, with a message that names it.
	if ref.ParameterNotFoundAction == nil || *ref.ParameterNotFoundAction != admissionregistrationv1.AllowAction {
		t.Fatalf("parameterNotFoundAction = %v, want Allow", ref.ParameterNotFoundAction)
	}
}

func TestClassDeletedOrders(t *testing.T) {
	v := compile(t, get(t, classPolicyFile, classDeletedOrders).policy)

	order := obj(claimGVK.GroupVersion().String(), claimGVK.Kind, "tenant-acme", "train-0", map[string]any{
		"spec": map[string]any{"className": "l40s-24c", "count": int64(1)},
	})
	deleting := class(classSpec(), gpu.FinalizerClass)
	now := metav1.Now()
	deleting.SetDeletionTimestamp(&now)
	other := class(classSpec(), gpu.FinalizerClass)
	other.SetName("h100")
	otherDeleting := other.DeepCopy()
	otherDeleting.SetDeletionTimestamp(&now)

	cases := []struct {
		name   string
		param  *unstructured.Unstructured
		user   user.Info
		denied bool
	}{
		{name: "order against a live class", param: class(classSpec(), gpu.FinalizerClass)},
		{name: "order against a class being deleted", param: deleting, denied: true},
		{name: "template claim the controller creates against a class being deleted", param: deleting, user: controllerSA, denied: true},
		{name: "another class being deleted", param: otherDeleting},
		{name: "another live class", param: other},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var u user.Info = tenantUser
			if tc.user != nil {
				u = tc.user
			}
			denials := evaluate(t, v, request{gvk: claimGVK, resource: "gpuclaims", namespace: "tenant-acme", obj: order, user: u, params: tc.param})
			if got := len(denials) > 0; got != tc.denied {
				t.Fatalf("denied = %v, want %v (%v)", got, tc.denied, denials)
			}
			for _, m := range denials {
				if !strings.Contains(m, "GPUClass l40s-24c") {
					t.Errorf("message must name the class, got %q", m)
				}
			}
		})
	}
}
