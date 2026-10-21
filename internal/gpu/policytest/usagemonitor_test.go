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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/authentication/user"

	"github.com/cozystack/cozystack/internal/gpu"
)

const (
	labelPolicyFile = "gpu-usage-monitor-label-policy.yaml"
	usageMonitor    = "cozystack-gpu-usage-monitor-label"
)

func TestUsageMonitorLabelMatch(t *testing.T) {
	p := get(t, labelPolicyFile, usageMonitor).policy
	rules := p.Spec.MatchConstraints.ResourceRules
	if len(rules) != 1 {
		t.Fatalf("want one resource rule, got %d", len(rules))
	}
	r := rules[0]
	all := []string{"*"}
	if !slices.Equal(r.APIGroups, all) || !slices.Equal(r.APIVersions, all) {
		t.Errorf("rule must cover every group and version, got %+v", r)
	}
	// Subresources too: a pod's labels can be written through pods/status.
	// The API server refuses "*/*" next to any other entry.
	if !slices.Equal(r.Resources, []string{"*/*"}) {
		t.Errorf("resources = %v, want every resource and subresource", r.Resources)
	}
	ops := []admissionregistrationv1.OperationType{admissionregistrationv1.Create, admissionregistrationv1.Update}
	if !slices.Equal(r.Operations, ops) {
		t.Errorf("operations = %v, want %v", r.Operations, ops)
	}
	if r.Scope == nil || *r.Scope != admissionregistrationv1.NamespacedScope {
		t.Errorf("scope must be Namespaced, got %v", r.Scope)
	}
	requireTenantNamespaceBindings(t, get(t, labelPolicyFile, usageMonitor))
}

func TestUsageMonitorLabel(t *testing.T) {
	v := compile(t, get(t, labelPolicyFile, usageMonitor).policy)
	reserved := map[string]string{gpu.LabelUsageMonitor: "gpu-devices"}

	labelled := func(o *unstructured.Unstructured, labels map[string]string) *unstructured.Unstructured {
		o = o.DeepCopy()
		if labels != nil {
			o.SetLabels(labels)
		}
		return o
	}
	pod := obj("v1", "Pod", "tenant-acme", "virt-launcher-vm-instance-trainer-x7k2p", map[string]any{
		"metadata": map[string]any{"ownerReferences": []any{map[string]any{
			"apiVersion": "kubevirt.io/v1", "kind": "VirtualMachineInstance", "name": "vm-instance-trainer", "uid": "1", "controller": true,
		}}},
		"spec": map[string]any{"containers": []any{map[string]any{"name": "compute"}}},
	})
	dataVolume := obj("cdi.kubevirt.io/v1beta1", "DataVolume", "tenant-acme", "vm-disk-trainer", map[string]any{"spec": map[string]any{}})
	configMap := obj("v1", "ConfigMap", "tenant-acme", "settings", map[string]any{"data": map[string]any{"a": "b"}})
	gpuDevice := device(nil, nil)
	vmInstance := obj("apps.cozystack.io/v1alpha1", "VMInstance", "tenant-acme", "trainer", map[string]any{"spec": map[string]any{}})
	podGVK := schema.GroupVersionKind{Version: "v1", Kind: "Pod"}
	dvGVK := schema.GroupVersionKind{Group: "cdi.kubevirt.io", Version: "v1beta1", Kind: "DataVolume"}
	cmGVK := schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}
	appGVK := schema.GroupVersionKind{Group: "apps.cozystack.io", Version: "v1alpha1", Kind: "VMInstance"}

	withFinalizers := func(o *unstructured.Unstructured, f ...string) *unstructured.Unstructured {
		o = o.DeepCopy()
		o.SetFinalizers(f)
		return o
	}
	kcmUser := &user.DefaultInfo{Name: "system:kube-controller-manager"}

	cases := []struct {
		name      string
		gvk       schema.GroupVersionKind
		resource  string
		sub       string
		namespace string
		old, new  *unstructured.Unstructured
		user      user.Info
		denied    bool
	}{
		// A VM template's labels reach the VMI and its virt-launcher pod,
		// and the DataVolumes created from the VM's dataVolumeTemplates carry
		// those templates' labels; none of them may carry the key.
		{name: "virt-launcher pod inheriting the label", gvk: podGVK, resource: "pods", new: labelled(pod, reserved), user: kubevirtSA, denied: true},
		{name: "DataVolume created with the label", gvk: dvGVK, resource: "datavolumes", new: labelled(dataVolume, reserved), user: kubevirtSA, denied: true},
		{name: "tenant ConfigMap with the label", gvk: cmGVK, resource: "configmaps", new: labelled(configMap, reserved), user: tenantUser, denied: true},
		{name: "tenant labels an existing ConfigMap", gvk: cmGVK, resource: "configmaps", old: configMap, new: labelled(configMap, reserved), user: tenantUser, denied: true},
		{name: "GPUDevice with the label", gvk: deviceGVK, resource: "gpudevices", new: labelled(gpuDevice, reserved), user: tenantUser, denied: true},
		{name: "application with the label", gvk: appGVK, resource: "vminstances", new: labelled(vmInstance, reserved), user: tenantUser, denied: true},
		{name: "label with an empty value", gvk: cmGVK, resource: "configmaps", new: labelled(configMap, map[string]string{gpu.LabelUsageMonitor: ""}), user: tenantUser, denied: true},
		{name: "cluster admin sets the label", gvk: cmGVK, resource: "configmaps", new: labelled(configMap, reserved), user: clusterAdmin, denied: true},
		{name: "controller sets the label", gvk: cmGVK, resource: "configmaps", new: labelled(configMap, reserved), user: controllerSA, denied: true},
		{name: "pod status write adding the label", gvk: podGVK, resource: "pods", sub: "status", old: pod, new: labelled(pod, reserved), user: kubevirtOtherSA, denied: true},
		// The policy ratchets, so an object labelled before it existed is never
		// stuck: kubelet status writes, finalizer removal and dropping the key
		// all pass.
		{name: "status write on a pod that already carries the label", gvk: podGVK, resource: "pods", sub: "status", old: labelled(pod, reserved), new: labelled(pod, reserved), user: kubevirtOtherSA},
		{name: "finalizer removal on an object that already carries the label", gvk: cmGVK, resource: "configmaps", old: withFinalizers(labelled(configMap, reserved), "kubernetes.io/pvc-protection"), new: labelled(configMap, reserved), user: kcmUser},
		{name: "update dropping the label", gvk: cmGVK, resource: "configmaps", old: labelled(configMap, reserved), new: configMap, user: tenantUser},
		// Changing the value could make an object labelled before the policy
		// start matching the monitor.
		{name: "update changing the label's value", gvk: cmGVK, resource: "configmaps", old: labelled(configMap, reserved), new: labelled(configMap, map[string]string{gpu.LabelUsageMonitor: "other"}), user: tenantUser, denied: true},
		{name: "pod without labels", gvk: podGVK, resource: "pods", new: pod, user: kubevirtSA},
		{name: "pod with other labels", gvk: podGVK, resource: "pods", new: labelled(pod, map[string]string{"kubevirt.io": "virt-launcher", gpu.LabelDeviceReference: "true"}), user: kubevirtSA},
		{name: "update dropping other labels", gvk: cmGVK, resource: "configmaps", old: labelled(configMap, map[string]string{"a": "b"}), new: configMap, user: tenantUser},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ns := tc.namespace
			if ns == "" {
				ns = "tenant-acme"
			}
			newObj := tc.new.DeepCopy()
			newObj.SetNamespace(ns)
			var oldObj *unstructured.Unstructured
			if tc.old != nil {
				oldObj = tc.old.DeepCopy()
				oldObj.SetNamespace(ns)
			}
			denials := evaluate(t, v, request{gvk: tc.gvk, resource: tc.resource, sub: tc.sub, namespace: ns, obj: newObj, old: oldObj, user: tc.user})
			if got := len(denials) > 0; got != tc.denied {
				t.Fatalf("denied = %v, want %v (%v)", got, tc.denied, denials)
			}
			for _, m := range denials {
				if !strings.Contains(m, gpu.LabelUsageMonitor) {
					t.Errorf("message must name the label, got %q", m)
				}
			}
		})
	}
}
