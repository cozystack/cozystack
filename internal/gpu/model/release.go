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
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
)

// The KubeVirt CR the platform renders permittedHostDevices into.
const (
	KubeVirtNamespace = "cozy-kubevirt"
	KubeVirtName      = "kubevirt"
)

// TenantReleaseRef maps the namespace a tenant owns to the name and
// namespace of that tenant's HelmRelease, the inverse of the tenant
// namespace naming in cozystack-api (REST.computeTenantNamespace):
//
//	"tenant-root"        -> ("tenant-root", "tenant-root")  // root is its own parent
//	"tenant-foo"         -> ("tenant-foo",  "tenant-root")  // foo lives in root
//	"tenant-foo-bar"     -> ("tenant-bar",  "tenant-foo")   // bar lives in foo
//	"tenant-foo-bar-baz" -> ("tenant-baz",  "tenant-foo-bar")
func TenantReleaseRef(ownedNamespace, prefix string) (name, namespace string, ok bool) {
	if ownedNamespace == rootTenantNamespace {
		return prefix + "root", rootTenantNamespace, true
	}
	if !strings.HasPrefix(ownedNamespace, prefix) {
		return "", "", false
	}
	segments := strings.Split(ownedNamespace, "-")
	last := segments[len(segments)-1]
	if len(segments) == 2 {
		return prefix + last, rootTenantNamespace, true
	}
	return prefix + last, strings.Join(segments[:len(segments)-1], "-"), true
}

// TenantNamespaceOf is the namespace the tenant of a HelmRelease owns.
func TenantNamespaceOf(releaseNamespace, releaseName, prefix string) string {
	app := strings.TrimPrefix(releaseName, prefix)
	if releaseNamespace == rootTenantNamespace {
		return prefix + app
	}
	return releaseNamespace + "-" + app
}

// DeclaredQuotas reads resourceQuotas from a tenant HelmRelease's values,
// every key as written: GPU keys are never rendered into a namespace
// ResourceQuota, so the release is where budgets are declared. A value that
// is not a quantity fails the read rather than reading as no budget.
func DeclaredQuotas(values []byte) (corev1.ResourceList, error) {
	out := corev1.ResourceList{}
	if len(values) == 0 {
		return out, nil
	}
	var v struct {
		ResourceQuotas map[string]resource.Quantity `json:"resourceQuotas"`
	}
	if err := json.Unmarshal(values, &v); err != nil {
		return nil, fmt.Errorf("decode tenant resourceQuotas: %w", err)
	}
	for k, q := range v.ResourceQuotas {
		out[corev1.ResourceName(k)] = q
	}
	return out, nil
}

// PermittedResourceNames returns the resource names of the KubeVirt CR's
// permittedHostDevices table, or nil when the table is unset and KubeVirt
// checks nothing (renderresources.go validatePermittedHostDevices in
// v1.9.0). It reads the PCI and mediated lists, where GPUs are declared.
func PermittedResourceNames(kv *unstructured.Unstructured) sets.Set[string] {
	table, ok, _ := unstructured.NestedMap(kv.Object, "spec", "configuration", "permittedHostDevices")
	if !ok || table == nil {
		return nil
	}
	out := sets.New[string]()
	for _, list := range []string{"pciHostDevices", "mediatedDevices"} {
		entries, _, _ := unstructured.NestedSlice(table, list)
		for _, e := range entries {
			if m, ok := e.(map[string]any); ok {
				if name, ok := m["resourceName"].(string); ok {
					out.Insert(name)
				}
			}
		}
	}
	return out
}

// Coverage maps a VMI to the units, per resource name, that its attached
// devices account for, so its virt-launcher pod does not count them twice.
type Coverage map[types.NamespacedName]map[corev1.ResourceName]int64

// CoveredForBudget counts every attached device, in any phase: a Lost
// device still counts against budgets itself. Do not use it for node
// capacity, where that device holds nothing and its pod holds the units.
func CoveredForBudget(devices []*gpuv1alpha1.GPUDevice, classes map[string]Class) Coverage {
	return covered(devices, classes, func(*gpuv1alpha1.GPUDevice) bool { return true })
}

// CoveredForCapacity counts only attached devices that hold units on a
// node. Do not use it for budgets, where it would count a Lost device's
// units twice.
func CoveredForCapacity(devices []*gpuv1alpha1.GPUDevice, classes map[string]Class) Coverage {
	return covered(devices, classes, func(d *gpuv1alpha1.GPUDevice) bool { return HoldsGPUs(d.Status.Phase) })
}

func covered(devices []*gpuv1alpha1.GPUDevice, classes map[string]Class, keep func(*gpuv1alpha1.GPUDevice) bool) Coverage {
	out := Coverage{}
	for _, d := range devices {
		rn := classes[d.Spec.ClassName].ResourceName
		if d.Status.AttachedTo == nil || rn == "" || !keep(d) {
			continue
		}
		k := types.NamespacedName{Namespace: d.Namespace, Name: d.Status.AttachedTo.VMI}
		if out[k] == nil {
			out[k] = map[corev1.ResourceName]int64{}
		}
		out[k][rn] += int64(d.Spec.Count)
	}
	return out
}

// ActivePods is a VMI's status.activePods: the UIDs of the pods KubeVirt
// runs it in.
func ActivePods(vmi *unstructured.Unstructured) sets.Set[types.UID] {
	out := sets.New[types.UID]()
	active, _, _ := unstructured.NestedMap(vmi.Object, "status", "activePods")
	for uid := range active {
		out.Insert(types.UID(uid))
	}
	return out
}

// LauncherVMI returns the VMI a pod is the launcher of: the KubeVirt VMI
// its owner reference names, and only when that VMI lists the pod in
// status.activePods. Whoever creates a pod sets its owner references and
// labels, while no tenant role can write a VMI's status. activePods returns
// the named VMI's active pods, nil when there is no such VMI.
func LauncherVMI(p *corev1.Pod, activePods func(types.NamespacedName) (sets.Set[types.UID], error)) (types.NamespacedName, bool, error) {
	for _, ref := range p.OwnerReferences {
		if ref.Kind != "VirtualMachineInstance" || ref.APIVersion != "kubevirt.io/v1" {
			continue
		}
		k := types.NamespacedName{Namespace: p.Namespace, Name: ref.Name}
		active, err := activePods(k)
		if err != nil {
			return types.NamespacedName{}, false, err
		}
		if !active.Has(p.UID) {
			return types.NamespacedName{}, false, nil
		}
		return k, true, nil
	}
	return types.NamespacedName{}, false, nil
}
