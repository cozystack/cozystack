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
	"reflect"
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
	rawNamesPolicyFile = "gpu-raw-names-policy.yaml"
	rawNames           = "cozystack-gpu-raw-names-forbidden"
)

var (
	podGVK = schema.GroupVersionKind{Version: "v1", Kind: "Pod"}
	vmiGVK = schema.GroupVersionKind{Group: "kubevirt.io", Version: "v1", Kind: "VirtualMachineInstance"}

	kubevirtSA      = &user.DefaultInfo{Name: "system:serviceaccount:cozy-kubevirt:kubevirt-controller", Groups: []string{"system:serviceaccounts", "system:serviceaccounts:cozy-kubevirt", "system:authenticated"}}
	kubevirtOtherSA = &user.DefaultInfo{Name: "system:serviceaccount:cozy-kubevirt:kubevirt-handler", Groups: []string{"system:serviceaccounts", "system:serviceaccounts:cozy-kubevirt", "system:authenticated"}}
)

// tenantNamespaceSelectors select every tenant namespace: tenant-root by
// name, every other tenant by the ancestor label each carries for
// tenant-root.
var tenantNamespaceSelectors = []metav1.LabelSelector{
	{MatchExpressions: []metav1.LabelSelectorRequirement{{
		Key: "kubernetes.io/metadata.name", Operator: metav1.LabelSelectorOpIn, Values: []string{"tenant-root"},
	}}},
	{MatchExpressions: []metav1.LabelSelectorRequirement{{
		Key: "tenant.cozystack.io/tenant-root", Operator: metav1.LabelSelectorOpExists,
	}}},
}

// requireTenantNamespaceBindings fails unless the policy is bound once per
// tenant namespace selector, and nothing in the policy narrows or widens that.
func requireTenantNamespaceBindings(t *testing.T, s *shipped) {
	t.Helper()
	if len(s.bindings) != len(tenantNamespaceSelectors) {
		t.Fatalf("%s: want %d bindings, got %d", s.policy.Name, len(tenantNamespaceSelectors), len(s.bindings))
	}
	for i, want := range tenantNamespaceSelectors {
		if !slices.ContainsFunc(s.bindings, func(b *admissionregistrationv1.ValidatingAdmissionPolicyBinding) bool {
			mr := b.Spec.MatchResources
			return mr != nil && mr.NamespaceSelector != nil && reflect.DeepEqual(*mr.NamespaceSelector, want)
		}) {
			t.Fatalf("%s: no binding selects tenant namespaces by %+v (selector %d)", s.policy.Name, want, i)
		}
	}
	if ns := s.policy.Spec.MatchConstraints.NamespaceSelector; ns != nil && (len(ns.MatchLabels) > 0 || len(ns.MatchExpressions) > 0) {
		t.Fatalf("%s: policy namespaceSelector %+v would narrow the bindings", s.policy.Name, ns)
	}
	if len(s.policy.Spec.MatchConditions) > 0 {
		t.Fatalf("%s: tenant namespaces are selected by the bindings, not by match conditions", s.policy.Name)
	}
}

func settings(names string) *unstructured.Unstructured {
	return obj("v1", "ConfigMap", gpu.SettingsNamespace, gpu.SettingsConfigMap, map[string]any{
		"data": map[string]any{
			gpu.SettingsKeyResourceNames:    names,
			gpu.SettingsKeyRawResourceNames: string(gpu.RawNamesForbidden),
		},
	})
}

const gpuNames = "nvidia.com/GH100_H100,nvidia.com/NVIDIA_L40S-24C,nvidia.com/gpu"

func TestRawNamesBinding(t *testing.T) {
	s := get(t, rawNamesPolicyFile, rawNames)
	pk := s.policy.Spec.ParamKind
	if pk == nil || pk.APIVersion != "v1" || pk.Kind != "ConfigMap" {
		t.Fatalf("paramKind = %+v, want ConfigMap", pk)
	}
	// The parameter is resolved before any expression runs, so a missing
	// ConfigMap denies everything the bindings match. Matching more than
	// tenant namespaces would block every pod in the cluster, the controller
	// that publishes the ConfigMap among them, until the ConfigMap exists.
	requireTenantNamespaceBindings(t, s)
	for _, b := range s.bindings {
		ref := b.Spec.ParamRef
		if ref == nil || ref.Name != gpu.SettingsConfigMap || ref.Namespace != gpu.SettingsNamespace || ref.Selector != nil {
			t.Fatalf("%s: paramRef = %+v, want %s/%s", b.Name, ref, gpu.SettingsNamespace, gpu.SettingsConfigMap)
		}
		// The hoster asked for raw names to be forbidden; until the
		// controller publishes the GPU resource names nothing can tell a GPU
		// from another resource, so the policy fails closed.
		if ref.ParameterNotFoundAction == nil || *ref.ParameterNotFoundAction != admissionregistrationv1.DenyAction {
			t.Fatalf("%s: parameterNotFoundAction = %v, want Deny", b.Name, ref.ParameterNotFoundAction)
		}
	}

	rules := s.policy.Spec.MatchConstraints.ResourceRules
	want := map[string][]string{"": {"pods"}, "kubevirt.io": {"virtualmachineinstances"}}
	if len(rules) != len(want) {
		t.Fatalf("want %d rules, got %+v", len(want), rules)
	}
	for _, r := range rules {
		if len(r.APIGroups) != 1 || !slices.Equal(r.Resources, want[r.APIGroups[0]]) {
			t.Errorf("unexpected rule %+v", r)
		}
		if !slices.Equal(r.Operations, []admissionregistrationv1.OperationType{admissionregistrationv1.Create}) {
			t.Errorf("rule %+v must match CREATE", r)
		}
	}
}

func pod(namespace string, owner bool, containers, initContainers []any) *unstructured.Unstructured {
	fields := map[string]any{"spec": map[string]any{"containers": containers}}
	if initContainers != nil {
		fields["spec"].(map[string]any)["initContainers"] = initContainers
	}
	u := obj("v1", "Pod", namespace, "virt-launcher-vm-instance-trainer-x7k2p", fields)
	if owner {
		u.Object["metadata"].(map[string]any)["ownerReferences"] = []any{map[string]any{
			"apiVersion": "kubevirt.io/v1", "kind": "VirtualMachineInstance", "name": "vm-instance-trainer", "uid": "1", "controller": true,
		}}
	}
	return u
}

func container(section string, resources map[string]any) []any {
	c := map[string]any{"name": "compute"}
	if resources != nil {
		c["resources"] = map[string]any{section: resources}
	}
	return []any{c}
}

func TestRawNamesPods(t *testing.T) {
	v := compile(t, get(t, rawNamesPolicyFile, rawNames).policy)
	gpuReq := map[string]any{"nvidia.com/GH100_H100": "1"}
	plain := map[string]any{"cpu": "1", "memory": "1Gi"}
	nic := map[string]any{"nvidia.com/hostdev": "1"}
	tenantSuperAdmin := &user.DefaultInfo{Name: "tenant-super-admin", Groups: []string{"tenant-acme-super-admin", "system:authenticated"}}

	cases := []struct {
		name   string
		pod    *unstructured.Unstructured
		user   user.Info
		denied bool
	}{
		{name: "virt-launcher pod of a VMI", pod: pod("tenant-acme", true, container("limits", gpuReq), nil), user: kubevirtSA},
		// A migration target pod is created by the same service account and
		// owned by the VMI, so it is allowed the same way.
		{name: "migration target pod of a VMI", pod: pod("tenant-acme", true, container("requests", gpuReq), nil), user: kubevirtSA},
		{name: "pod with a GPU from another KubeVirt component", pod: pod("tenant-acme", true, container("limits", gpuReq), nil), user: kubevirtOtherSA, denied: true},
		{name: "pod with a GPU from kubevirt-controller without a VMI owner", pod: pod("tenant-acme", false, container("limits", gpuReq), nil), user: kubevirtSA, denied: true},
		{name: "pod with a GPU from a tenant service account", pod: pod("tenant-acme", true, container("limits", gpuReq), nil), user: tenantNamespaced, denied: true},
		{name: "pod with a GPU from a tenant super-admin", pod: pod("tenant-acme", true, container("limits", gpuReq), nil), user: tenantSuperAdmin, denied: true},
		{name: "pod with a GPU from a cluster admin", pod: pod("tenant-acme", false, container("limits", gpuReq), nil), user: clusterAdmin, denied: true},
		{name: "pod with a GPU request only", pod: pod("tenant-acme", false, container("requests", gpuReq), nil), user: tenantNamespaced, denied: true},
		{name: "pod with a GPU in an init container", pod: pod("tenant-acme", false, container("limits", plain), container("limits", gpuReq)), user: tenantNamespaced, denied: true},
		{name: "pod requesting the container variant's GPU name", pod: pod("tenant-acme", false, container("limits", map[string]any{"nvidia.com/gpu": "1"}), nil), user: tenantNamespaced, denied: true},
		{name: "pod without GPUs", pod: pod("tenant-acme", false, container("limits", plain), nil), user: tenantNamespaced},
		{name: "pod without resources", pod: pod("tenant-acme", false, container("limits", nil), nil), user: tenantNamespaced},
		{name: "pod with a NIC VF", pod: pod("tenant-acme", false, container("limits", nic), nil), user: tenantNamespaced},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			denials := evaluate(t, v, request{gvk: podGVK, resource: "pods", namespace: tc.pod.GetNamespace(), obj: tc.pod, user: tc.user, params: settings(gpuNames)})
			if got := len(denials) > 0; got != tc.denied {
				t.Fatalf("denied = %v, want %v (%v)", got, tc.denied, denials)
			}
			for _, m := range denials {
				if !strings.Contains(m, "gpu.rawResourceNames is forbidden") {
					t.Errorf("message must name the setting, got %q", m)
				}
			}
		})
	}
}

func vmi(labels map[string]string, domain map[string]any) *unstructured.Unstructured {
	u := obj("kubevirt.io/v1", "VirtualMachineInstance", "tenant-acme", "vm-instance-trainer", map[string]any{
		"spec": map[string]any{"domain": domain},
	})
	if labels != nil {
		u.SetLabels(labels)
	}
	return u
}

func TestRawNamesVMIs(t *testing.T) {
	v := compile(t, get(t, rawNamesPolicyFile, rawNames).policy)
	ref := map[string]string{gpu.LabelClaimReference: gpu.LabelValueTrue}
	devices := func(d map[string]any) map[string]any { return map[string]any{"devices": d} }
	rawGPU := devices(map[string]any{"gpus": []any{map[string]any{"name": "gpu0", "deviceName": "nvidia.com/GH100_H100"}}})
	// Any gpus entry is a GPU entry, whatever its device name.
	unknownGPU := devices(map[string]any{"gpus": []any{map[string]any{"name": "gpu1", "deviceName": "nvidia.com/A400"}}})
	hostGPU := devices(map[string]any{"hostDevices": []any{map[string]any{"name": "h0", "deviceName": "nvidia.com/NVIDIA_L40S-24C"}}})
	hostNIC := devices(map[string]any{"hostDevices": []any{map[string]any{"name": "h0", "deviceName": "nvidia.com/hostdev"}}})
	resourceGPU := map[string]any{"devices": map[string]any{}, "resources": map[string]any{"limits": map[string]any{"nvidia.com/gpu": "1"}}}
	resourceRequestGPU := map[string]any{"devices": map[string]any{}, "resources": map[string]any{"requests": map[string]any{"nvidia.com/gpu": "1"}}}
	plain := map[string]any{"devices": map[string]any{"disks": []any{}}, "resources": map[string]any{"requests": map[string]any{"memory": "4Gi"}}}
	webhookGPU := devices(map[string]any{"gpus": []any{map[string]any{"name": gpu.VMIGPUNamePrefix + "train-0-0", "deviceName": "nvidia.com/GH100_H100"}}})

	cases := []struct {
		name   string
		vmi    *unstructured.Unstructured
		denied bool
	}{
		// A VM written by a tenant super-admin, a VMIReplicaSet and a worker
		// pool all reach the API as a VMI created by kubevirt-controller.
		{name: "VMI with a raw gpus entry", vmi: vmi(nil, rawGPU), denied: true},
		{name: "VMI with a gpus entry of an unknown name", vmi: vmi(nil, unknownGPU), denied: true},
		{name: "VMI with a GPU host device", vmi: vmi(nil, hostGPU), denied: true},
		{name: "VMI with a GPU resource limit", vmi: vmi(nil, resourceGPU), denied: true},
		{name: "VMI with a GPU resource request", vmi: vmi(nil, resourceRequestGPU), denied: true},
		{name: "VMI with a reference label of another value", vmi: vmi(map[string]string{gpu.LabelClaimReference: "yes"}, rawGPU), denied: true},
		{name: "referencing VMI with the webhook's entries", vmi: vmi(ref, webhookGPU)},
		{name: "VMI with a NIC host device", vmi: vmi(nil, hostNIC)},
		{name: "VMI without GPUs", vmi: vmi(nil, plain)},
		{name: "VMI without devices", vmi: vmi(nil, map[string]any{})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			denials := evaluate(t, v, request{gvk: vmiGVK, resource: "virtualmachineinstances", namespace: "tenant-acme", obj: tc.vmi, user: kubevirtSA, params: settings(gpuNames)})
			if got := len(denials) > 0; got != tc.denied {
				t.Fatalf("denied = %v, want %v (%v)", got, tc.denied, denials)
			}
			for _, m := range denials {
				if !strings.Contains(m, "VirtualMachineInstance vm-instance-trainer") || !strings.Contains(m, "gpu.rawResourceNames is forbidden") {
					t.Errorf("message must name the VMI and the setting, got %q", m)
				}
			}
		})
	}
}

// Every gpus entry counts even when the published set is empty, and a set
// that lists nothing else refuses no host device or resource.
func TestRawNamesEmptySet(t *testing.T) {
	v := compile(t, get(t, rawNamesPolicyFile, rawNames).policy)
	rawGPU := map[string]any{"devices": map[string]any{"gpus": []any{map[string]any{"name": "gpu0", "deviceName": "nvidia.com/GH100_H100"}}}}
	host := map[string]any{"devices": map[string]any{"hostDevices": []any{map[string]any{"name": "h0", "deviceName": "nvidia.com/GH100_H100"}}}}
	for _, params := range []*unstructured.Unstructured{
		settings(""),
		obj("v1", "ConfigMap", gpu.SettingsNamespace, gpu.SettingsConfigMap, map[string]any{}),
	} {
		if d := evaluate(t, v, request{gvk: vmiGVK, resource: "virtualmachineinstances", namespace: "tenant-acme", obj: vmi(nil, rawGPU), user: kubevirtSA, params: params}); len(d) == 0 {
			t.Errorf("a gpus entry must be refused with an empty set (%v)", params.Object)
		}
		if d := evaluate(t, v, request{gvk: vmiGVK, resource: "virtualmachineinstances", namespace: "tenant-acme", obj: vmi(nil, host), user: kubevirtSA, params: params}); len(d) != 0 {
			t.Errorf("a host device outside an empty set must pass, got %v", d)
		}
	}
}
