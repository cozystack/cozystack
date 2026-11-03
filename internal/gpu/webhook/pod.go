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

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu"
	"github.com/cozystack/cozystack/internal/gpu/model"
)

// KubeVirt's launcher pod label (staging/src/kubevirt.io/api/core/v1/types.go
// in v1.9.0); the chart's objectSelector matches it too.
const (
	labelKubeVirt     = "kubevirt.io"
	valueVirtLauncher = "virt-launcher"
)

// MutateLauncher adds the tolerations of a VMI's classes to its
// virt-launcher pods, migration targets and the temporary pod that binds
// WaitForFirstConsumer volumes included. They go on the pod rather than
// on the VMI: under vmRolloutStrategy LiveUpdate KubeVirt v1.9.0 resets a
// VMI's tolerations to the VM template's on every sync
// (pkg/virt-controller/watch/vm/vm.go, handleTolerationsChangeRequest).
func (h *Handler) MutateLauncher(ctx context.Context, req admission.Request) admission.Response {
	if req.Operation != admissionv1.Create {
		return admission.Allowed("")
	}
	pod := &corev1.Pod{}
	if err := json.Unmarshal(req.Object.Raw, pod); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if pod.Labels[labelKubeVirt] != valueVirtLauncher || pod.Labels[gpu.LabelGPUReference] != gpu.LabelValueTrue {
		return admission.Allowed("")
	}
	owner := slices.IndexFunc(pod.OwnerReferences, func(r metav1.OwnerReference) bool {
		return r.Kind == vmiGVK.Kind && r.APIVersion == vmiGVK.GroupVersion().String()
	})
	if owner < 0 {
		return admission.Denied(fmt.Sprintf("pod %s carries %s but no VirtualMachineInstance owner", pod.GenerateName+pod.Name, gpu.LabelGPUReference))
	}
	ref := pod.OwnerReferences[owner]
	vmi := &unstructured.Unstructured{}
	vmi.SetGroupVersionKind(vmiGVK)
	err := h.Reader.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: ref.Name}, vmi)
	switch {
	case apierrors.IsNotFound(err):
		return admission.Denied(fmt.Sprintf("VirtualMachineInstance %s of the pod does not exist", ref.Name))
	case err != nil:
		return admission.Errored(http.StatusInternalServerError, err)
	case vmi.GetUID() != ref.UID:
		return admission.Denied(fmt.Sprintf("VirtualMachineInstance %s is not the owner the pod names", ref.Name))
	}
	entries, err := model.ParseClassEntries(vmi.GetAnnotations()[gpu.AnnotationGPUs])
	if err != nil {
		return admission.Denied(fmt.Sprintf("VirtualMachineInstance %s: annotation %s: %v", ref.Name, gpu.AnnotationGPUs, err))
	}

	have := slices.Clone(pod.Spec.Tolerations)
	var ops []jsonpatch.Operation
	seen := map[string]bool{}
	for _, e := range entries {
		if seen[e.ClassName] {
			continue
		}
		seen[e.ClassName] = true
		c := &cozyv1alpha1.GPUClass{}
		err := h.Reader.Get(ctx, client.ObjectKey{Name: e.ClassName}, c)
		if apierrors.IsNotFound(err) {
			return admission.Denied(fmt.Sprintf("GPUClass %s no longer exists; the launcher's class tolerations cannot be resolved", e.ClassName))
		}
		if err != nil {
			return admission.Errored(http.StatusInternalServerError, err)
		}
		for _, t := range model.ClassFrom(c).Tolerations {
			if slices.ContainsFunc(have, func(o corev1.Toleration) bool { return apiequality.Semantic.DeepEqual(o, t) }) {
				continue
			}
			if len(have) == 0 {
				ops = append(ops, jsonpatch.NewOperation("add", "/spec/tolerations", []any{t}))
			} else {
				ops = append(ops, jsonpatch.NewOperation("add", "/spec/tolerations/-", t))
			}
			have = append(have, t)
		}
	}
	if len(ops) == 0 {
		return admission.Allowed("")
	}
	return admission.Patched("", ops...)
}
