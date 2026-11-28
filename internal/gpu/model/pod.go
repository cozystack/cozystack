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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/sets"
)

// Pod is the GPU units a pod requests.
type Pod struct {
	Namespace string
	// NodeName is empty for a pod not yet scheduled, which takes no unit
	// of any node.
	NodeName string
	Units    map[corev1.ResourceName]int64
}

// PodFrom returns the units p requests of each name in gpuNames, and
// false when it requests none or is finished.
func PodFrom(p *corev1.Pod, gpuNames sets.Set[corev1.ResourceName]) (Pod, bool) {
	if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
		return Pod{}, false
	}
	units := map[corev1.ResourceName]int64{}
	for name := range gpuNames {
		if u := podUnits(p, name); u > 0 {
			units[name] = u
		}
	}
	if len(units) == 0 {
		return Pod{}, false
	}
	return Pod{Namespace: p.Namespace, NodeName: p.Spec.NodeName, Units: units}, true
}

// podUnits is the pod's effective request of name, as the scheduler
// computes it: sidecars (init containers that always restart) run beside
// the later init containers and the main ones, and the pod overhead comes
// on top.
func podUnits(p *corev1.Pod, name corev1.ResourceName) int64 {
	var sum, sidecars, initPeak int64
	for i := range p.Spec.Containers {
		sum += containerUnits(&p.Spec.Containers[i], name)
	}
	for i := range p.Spec.InitContainers {
		c := &p.Spec.InitContainers[i]
		u := containerUnits(c, name)
		if c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			sidecars += u
			initPeak = max(initPeak, sidecars)
			continue
		}
		initPeak = max(initPeak, sidecars+u)
	}
	return max(sum+sidecars, initPeak) + p.Spec.Overhead.Name(name, resource.DecimalSI).Value()
}

// containerUnits reads the limit first: an extended resource may be given
// as a limit alone, and when both are set they are equal.
func containerUnits(c *corev1.Container, name corev1.ResourceName) int64 {
	if q, ok := c.Resources.Limits[name]; ok {
		return q.Value()
	}
	if q, ok := c.Resources.Requests[name]; ok {
		return q.Value()
	}
	return 0
}
