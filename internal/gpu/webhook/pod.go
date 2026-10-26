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

	"gomodules.xyz/jsonpatch/v2"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu"
	"github.com/cozystack/cozystack/internal/gpu/model"
)

// KubeVirt's pod labels (staging/src/kubevirt.io/api/core/v1/types.go in
// v1.9.0). The chart's objectSelector matches on the same keys.
const (
	labelKubeVirt     = "kubevirt.io"
	valueVirtLauncher = "virt-launcher"
	labelMigrationJob = "kubevirt.io/migrationJobUID"
)

// GatePod holds a virt-launcher pod of a VMI with claim references at
// gpu.SchedulingGateClaims until the controller has placed the VMI's
// claims and pinned the pod, and adds the tolerations of the claims'
// classes, which binding already counted on. It gates the temporary pod
// KubeVirt creates to bind WaitForFirstConsumer volumes too, so a local
// volume binds on the claims' node, and leaves migration target pods
// alone: KubeVirt deletes a target pod that stays pending.
func (h *Handler) GatePod(ctx context.Context, req admission.Request) admission.Response {
	if req.Operation != admissionv1.Create {
		return admission.Allowed("")
	}
	pod := &corev1.Pod{}
	if err := json.Unmarshal(req.Object.Raw, pod); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if pod.Labels[labelKubeVirt] != valueVirtLauncher || pod.Labels[gpu.LabelClaimReference] != gpu.LabelValueTrue {
		return admission.Allowed("")
	}
	if _, ok := pod.Labels[labelMigrationJob]; ok {
		return admission.Allowed("")
	}

	owner := slices.IndexFunc(pod.OwnerReferences, func(r metav1.OwnerReference) bool {
		return r.Kind == vmiGVK.Kind && r.APIVersion == vmiGVK.GroupVersion().String()
	})
	if owner < 0 {
		return admission.Denied(fmt.Sprintf("pod %s carries a claim reference but no VirtualMachineInstance owner", pod.GenerateName+pod.Name))
	}
	ref := pod.OwnerReferences[owner]
	vmi := &unstructured.Unstructured{}
	vmi.SetGroupVersionKind(vmiGVK)
	// Past the cache: virt-controller creates the pod right after the VMI.
	err := h.Reader.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: ref.Name}, vmi)
	switch {
	case apierrors.IsNotFound(err):
		return admission.Denied(fmt.Sprintf("VirtualMachineInstance %s of the pod does not exist", ref.Name))
	case err != nil:
		return admission.Errored(http.StatusInternalServerError, err)
	case vmi.GetUID() != ref.UID:
		return admission.Denied(fmt.Sprintf("VirtualMachineInstance %s is not the owner the pod names", ref.Name))
	}
	refs, err := model.ParseClaimRefs(vmi.GetAnnotations()[gpu.AnnotationClaims])
	if err != nil {
		return admission.Denied(fmt.Sprintf("VirtualMachineInstance %s: annotation %s: %v", ref.Name, gpu.AnnotationClaims, err))
	}
	tolerations, err := h.classTolerations(ctx, req.Namespace, refs)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}

	var ops []jsonpatch.Operation
	if !slices.ContainsFunc(pod.Spec.SchedulingGates, func(g corev1.PodSchedulingGate) bool { return g.Name == gpu.SchedulingGateClaims }) {
		ops = appendOp(ops, "/spec/schedulingGates", len(pod.Spec.SchedulingGates) == 0, corev1.PodSchedulingGate{Name: gpu.SchedulingGateClaims})
	}
	have := slices.Clone(pod.Spec.Tolerations)
	for _, t := range tolerations {
		if slices.ContainsFunc(have, func(o corev1.Toleration) bool { return apiequality.Semantic.DeepEqual(o, t) }) {
			continue
		}
		ops = appendOp(ops, "/spec/tolerations", len(have) == 0, t)
		have = append(have, t)
	}
	if len(ops) == 0 {
		return admission.Allowed("")
	}
	return admission.Patched("", ops...)
}

// appendOp appends value to the list at path, creating the list when the
// pod has none: a JSON patch cannot append to a field that is absent.
func appendOp(ops []jsonpatch.Operation, path string, empty bool, value any) []jsonpatch.Operation {
	if empty {
		return append(ops, jsonpatch.NewOperation("add", path, []any{value}))
	}
	return append(ops, jsonpatch.NewOperation("add", path+"/-", value))
}

// classTolerations returns the tolerations of the classes of refs, in
// reference order. A held claim or a class gone since the
// VMI was admitted adds nothing: the controller does not release a set it
// cannot complete.
func (h *Handler) classTolerations(ctx context.Context, ns string, refs []model.ClaimRef) ([]corev1.Toleration, error) {
	var out []corev1.Toleration
	seen := map[string]bool{}
	for _, r := range refs {
		className := ""
		if r.ClaimTemplate != nil {
			className = r.ClaimTemplate.ClassName
		} else {
			d := &gpuv1alpha1.GPUClaim{}
			err := h.Reader.Get(ctx, client.ObjectKey{Namespace: ns, Name: r.Claim}, d)
			if apierrors.IsNotFound(err) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("get GPUClaim %s: %w", r.Claim, err)
			}
			className = d.Spec.ClassName
		}
		if seen[className] {
			continue
		}
		seen[className] = true
		c := &gpuv1alpha1.GPUClass{}
		err := h.Reader.Get(ctx, client.ObjectKey{Name: className}, c)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("get GPUClass %s: %w", className, err)
		}
		out = append(out, model.ClassFrom(c).Tolerations...)
	}
	return out, nil
}
