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
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"gomodules.xyz/jsonpatch/v2"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu/model"
)

// PinPod pins a virt-launcher pod to the node its VMI's devices are bound
// to. The pin goes on the pod, not the VMI, because LiveUpdate rewrites a
// VMI's node affinity from the VM template. It is a required node affinity
// rather than nodeName, which would let kubelet start the pod on a
// cordoned node.
func (h *Handler) PinPod(ctx context.Context, req admission.Request) admission.Response {
	if req.Operation != admissionv1.Create {
		return admission.Allowed("")
	}
	pod := &corev1.Pod{}
	if err := json.Unmarshal(req.Object.Raw, pod); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	var vmiName string
	var vmiUID types.UID
	for _, ref := range pod.OwnerReferences {
		if ref.Kind == vmiGVK.Kind && ref.APIVersion == vmiGVK.GroupVersion().String() {
			vmiName, vmiUID = ref.Name, ref.UID
		}
	}
	if vmiName == "" {
		return admission.Denied(fmt.Sprintf("pod %s carries a device reference but no VMI owner", pod.Name))
	}

	node, err := h.deviceNode(ctx, req.Namespace, vmiName, vmiUID)
	if err != nil {
		return admission.Denied(err.Error())
	}
	if !pinTo(pod, node) {
		return admission.Allowed("")
	}
	// One op on the affinity alone: a diff against the re-marshalled pod
	// would also write every zero value the typed struct adds.
	return admission.Patched("", jsonpatch.NewOperation("add", "/spec/affinity", pod.Spec.Affinity))
}

// deviceNode returns the one node the devices attached to the VMI hold
// their units on. It reads past the cache: the pod can reach a replica
// whose cache has the attachment but not yet the binder's answer, which
// may have moved the device.
func (h *Handler) deviceNode(ctx context.Context, ns, vmiName string, vmiUID types.UID) (string, error) {
	l := &gpuv1alpha1.GPUDeviceList{}
	if err := h.Reader.List(ctx, l, client.InNamespace(ns)); err != nil {
		return "", fmt.Errorf("list GPUDevices: %w", err)
	}
	nodes := map[string][]string{}
	for i := range l.Items {
		d := &l.Items[i]
		if d.Status.AttachedTo == nil || d.Status.AttachedTo.VMI != vmiName {
			continue
		}
		// A template device still owned by an earlier VMI of this name is
		// not this pod's.
		if owner := vmiOwner(d); owner != "" && owner != vmiUID {
			continue
		}
		if !model.HoldsGPUs(d.Status.Phase) || d.Status.NodeName == "" {
			return "", fmt.Errorf("GPUDevice %s of VMI %s is %s and holds no node", d.Name, vmiName, phaseOf(d))
		}
		nodes[d.Status.NodeName] = append(nodes[d.Status.NodeName], d.Name)
	}
	switch len(nodes) {
	case 0:
		return "", fmt.Errorf("VMI %s has no GPUDevice attached", vmiName)
	case 1:
		for n := range nodes {
			return n, nil
		}
	}
	var parts []string
	for n, devices := range nodes {
		parts = append(parts, fmt.Sprintf("%s on %s", strings.Join(devices, ", "), n))
	}
	sort.Strings(parts)
	return "", fmt.Errorf("the GPUDevices of VMI %s are on different nodes: %s", vmiName, strings.Join(parts, "; "))
}

// pinTo adds the node's name to every required node selector term, or
// sets it as the only term, and reports whether the pod changed. A term
// that already carries it is left alone, so a reinvocation is a no-op.
func pinTo(pod *corev1.Pod, node string) bool {
	req := corev1.NodeSelectorRequirement{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{node}}
	if pod.Spec.Affinity == nil {
		pod.Spec.Affinity = &corev1.Affinity{}
	}
	if pod.Spec.Affinity.NodeAffinity == nil {
		pod.Spec.Affinity.NodeAffinity = &corev1.NodeAffinity{}
	}
	na := pod.Spec.Affinity.NodeAffinity
	if na.RequiredDuringSchedulingIgnoredDuringExecution == nil || len(na.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms) == 0 {
		na.RequiredDuringSchedulingIgnoredDuringExecution = &corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchFields: []corev1.NodeSelectorRequirement{req}}},
		}
		return true
	}
	changed := false
	terms := na.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	for i := range terms {
		if slices.ContainsFunc(terms[i].MatchFields, func(r corev1.NodeSelectorRequirement) bool {
			return r.Key == req.Key && r.Operator == req.Operator && slices.Equal(r.Values, req.Values)
		}) {
			continue
		}
		terms[i].MatchFields = append(terms[i].MatchFields, req)
		changed = true
	}
	return changed
}

func vmiOwner(d *gpuv1alpha1.GPUDevice) types.UID {
	for _, ref := range d.OwnerReferences {
		if ref.Kind == vmiGVK.Kind && ref.APIVersion == vmiGVK.GroupVersion().String() {
			return ref.UID
		}
	}
	return ""
}

func phaseOf(d *gpuv1alpha1.GPUDevice) gpuv1alpha1.GPUDevicePhase {
	if d.Status.Phase == "" {
		return gpuv1alpha1.GPUDevicePending
	}
	return d.Status.Phase
}
