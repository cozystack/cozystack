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

package controller

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/cozystack/cozystack/internal/gpu"
	"github.com/cozystack/cozystack/internal/gpu/model"
)

var (
	vmiGVK      = schema.GroupVersionKind{Group: "kubevirt.io", Version: "v1", Kind: "VirtualMachineInstance"}
	vmGVK       = schema.GroupVersionKind{Group: "kubevirt.io", Version: "v1", Kind: "VirtualMachine"}
	kubeVirtGVK = schema.GroupVersionKind{Group: "kubevirt.io", Version: "v1", Kind: "KubeVirt"}
)

// vmi is the part of a VirtualMachineInstance the controller reads.
type vmi struct {
	Name, Namespace string
	UID             types.UID
	ResourceVersion string
	Phase           string
	NodeName        string
	// VM is the controlling VirtualMachine, "" for a VMI created without one.
	VM         string
	Deleting   bool
	ActivePods sets.Set[types.UID]
	// Refs are the VMI's claim references; RefsErr is set when its
	// annotation does not parse.
	Refs    []model.ClaimRef
	RefsErr error
	// PVCs are the claims of the VMI's persistentVolumeClaim and
	// dataVolume volumes; a DataVolume's PVC has the DataVolume's name.
	PVCs []string
}

// final reports a VMI that no longer runs its domain. A deletion timestamp
// alone does not count: on an isolated node the domain can still hold the GPU.
func (v *vmi) final() bool {
	return v.Phase == "Succeeded" || v.Phase == "Failed"
}

// unscheduled reports a VMI whose virt-launcher pod has not been placed yet.
func (v *vmi) unscheduled() bool {
	return v.NodeName == "" && (v.Phase == "" || v.Phase == "Pending" || v.Phase == "Scheduling")
}

func vmiFrom(u *unstructured.Unstructured) *vmi {
	out := &vmi{Name: u.GetName(), Namespace: u.GetNamespace(), UID: u.GetUID(), ResourceVersion: u.GetResourceVersion(),
		Deleting: u.GetDeletionTimestamp() != nil, ActivePods: model.ActivePods(u)}
	if a, ok := u.GetAnnotations()[gpu.AnnotationClaims]; ok {
		out.Refs, out.RefsErr = model.ParseClaimRefs(a)
	}
	volumes, _, _ := unstructured.NestedSlice(u.Object, "spec", "volumes")
	for _, vol := range volumes {
		m, _ := vol.(map[string]any)
		if name, _, _ := unstructured.NestedString(m, "persistentVolumeClaim", "claimName"); name != "" {
			out.PVCs = append(out.PVCs, name)
		}
		if name, _, _ := unstructured.NestedString(m, "dataVolume", "name"); name != "" {
			out.PVCs = append(out.PVCs, name)
		}
	}
	out.Phase, _, _ = unstructured.NestedString(u.Object, "status", "phase")
	out.NodeName, _, _ = unstructured.NestedString(u.Object, "status", "nodeName")
	for _, ref := range u.GetOwnerReferences() {
		if ref.Kind == vmGVK.Kind && ref.Controller != nil && *ref.Controller {
			out.VM = ref.Name
		}
	}
	return out
}

// vm is the part of a VirtualMachine the controller reads.
type vm struct {
	Name, Namespace string
	RunStrategy     string
	// Claims are the held claims the VM's template names.
	Claims []string
}

// restartsDeletedVMI reports whether KubeVirt creates a new VMI after the
// controller deletes the current one (vm.go syncRunStrategy in v1.9.0).
func (v *vm) restartsDeletedVMI() bool {
	return v.RunStrategy == "Always" || v.RunStrategy == "Once"
}

func vmFrom(u *unstructured.Unstructured) *vm {
	out := &vm{Name: u.GetName(), Namespace: u.GetNamespace()}
	out.RunStrategy, _, _ = unstructured.NestedString(u.Object, "spec", "runStrategy")
	if out.RunStrategy == "" {
		// spec.running is the deprecated spelling of Always/Halted.
		if running, ok, _ := unstructured.NestedBool(u.Object, "spec", "running"); ok && running {
			out.RunStrategy = "Always"
		} else {
			out.RunStrategy = "Halted"
		}
	}
	annotation, _, _ := unstructured.NestedString(u.Object, "spec", "template", "metadata", "annotations", gpu.AnnotationClaims)
	if annotation == "" {
		return out
	}
	refs, err := model.ParseClaimRefs(annotation)
	if err != nil {
		return out
	}
	for _, r := range refs {
		if r.Claim != "" {
			out.Claims = append(out.Claims, r.Claim)
		}
	}
	return out
}
