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

package agent

import (
	"cmp"
	"slices"

	corev1 "k8s.io/api/core/v1"
	podresourcesv1 "k8s.io/kubelet/pkg/apis/podresources/v1"
	"k8s.io/utils/ptr"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
)

// Inventory builds the agent's part of a GPUNode status: the GPU functions
// under sysfs, with the resource name, allocatable count, health and user
// that kubelet's pod-resources API reports for each, and the resource names
// of GPUs, placed or not. live tells whether a listed pod still runs.
//
// The pod-resources API carries no device health. kubelet lists as
// allocatable only the devices their plugin reports healthy, so a function
// is healthy when every plugin device on it is allocatable, and unhealthy
// when a live pod holds one that is not. A function no plugin device maps to
// has no health; an unhealthy device that no pod holds looks the same.
func Inventory(sysfs string, alloc *podresourcesv1.AllocatableResourcesResponse, pods []*podresourcesv1.PodResources, live func(namespace, name string) bool) (gpuv1alpha1.GPUNodeStatus, error) {
	gpus, err := scan(sysfs)
	if err != nil {
		return gpuv1alpha1.GPUNodeStatus{}, err
	}
	r := resolver{sysfs: sysfs, gpus: gpus}
	names := map[string]bool{}
	allocatable := map[[2]string]bool{}
	// Allocatable devices per function and resource name.
	counts := map[[2]string]int32{}

	claim := func(resourceName, id string, healthy bool) []string {
		// A GPU the agent cannot place still makes its name a GPU name.
		if nvmlUUID.MatchString(id) {
			names[resourceName] = true
		}
		addrs := r.resolve(id)
		for _, a := range addrs {
			d := gpus[a]
			// Two plugins on one function is a host-layer fault; the
			// smallest name keeps the report stable.
			if d.ResourceName == "" || resourceName < d.ResourceName {
				d.ResourceName = resourceName
			}
			d.Healthy = ptr.To(healthy && ptr.Deref(d.Healthy, true))
			gpus[a] = d
			names[resourceName] = true
		}
		return addrs
	}

	for _, cd := range alloc.GetDevices() {
		for _, id := range cd.GetDeviceIds() {
			allocatable[[2]string{cd.GetResourceName(), id}] = true
			// An IOMMU group ID is one plugin device however many GPUs the
			// group holds, so it counts once, on its first function.
			if addrs := claim(cd.GetResourceName(), id, true); len(addrs) > 0 {
				counts[[2]string{addrs[0], cd.GetResourceName()}]++
			}
		}
	}
	for _, p := range pods {
		if !live(p.GetNamespace(), p.GetName()) {
			continue
		}
		user := &gpuv1alpha1.GPUPodReference{Namespace: p.GetNamespace(), Pod: p.GetName()}
		for _, c := range p.GetContainers() {
			for _, cd := range c.GetDevices() {
				for _, id := range cd.GetDeviceIds() {
					for _, a := range claim(cd.GetResourceName(), id, allocatable[[2]string{cd.GetResourceName(), id}]) {
						// The schema holds one user per function, and an mdev
						// parent can serve several pods: report the smallest by
						// namespace and name, so the report is stable.
						d := gpus[a]
						if d.UsedBy == nil || refLess(user, d.UsedBy) {
							d.UsedBy = user
						}
						gpus[a] = d
					}
				}
			}
		}
	}

	var st gpuv1alpha1.GPUNodeStatus
	for a, d := range gpus {
		if d.ResourceName != "" {
			d.Allocatable = ptr.To(counts[[2]string{a, d.ResourceName}])
		}
		st.Devices = append(st.Devices, d)
	}
	slices.SortFunc(st.Devices, func(a, b gpuv1alpha1.GPUNodeDevice) int { return cmp.Compare(a.PCIAddress, b.PCIAddress) })
	for n := range names {
		st.ResourceNames = append(st.ResourceNames, n)
	}
	slices.Sort(st.ResourceNames)
	return st, nil
}

func refLess(a, b *gpuv1alpha1.GPUPodReference) bool {
	return cmp.Or(cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Pod, b.Pod)) < 0
}

// podLive is false in a final phase. kubelet 1.32 and 1.33 still list such
// pods with the devices they held, which a new pod may already use.
func podLive(phase corev1.PodPhase) bool {
	return phase != corev1.PodSucceeded && phase != corev1.PodFailed
}
