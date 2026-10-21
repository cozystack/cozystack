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
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
)

// The class's capacity and load show the hoster's inventory, which the
// GPUClass status, readable by every authenticated user, must not.
var (
	classCapacity = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "cozystack_gpu_class_capacity_units",
		Help: "Allocatable GPU units of the class on nodes that serve it.",
	}, []string{"class"})
	classBound = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "cozystack_gpu_class_bound_units",
		Help: "GPU units of the class held by Bound or Attached devices.",
	}, []string{"class"})
	classLost = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "cozystack_gpu_class_lost_units",
		Help: "GPU units of the class whose devices are Lost.",
	}, []string{"class"})
	classWaiting = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "cozystack_gpu_class_waiting_units",
		Help: "GPU units of the class that wait for a node: Pending orders and Lost devices.",
	}, []string{"class"})
)

func init() {
	metrics.Registry.MustRegister(classCapacity, classBound, classLost, classWaiting)
}

func (r *Reconciler) exportMetrics(w *world) {
	for _, g := range []*prometheus.GaugeVec{classCapacity, classBound, classLost, classWaiting} {
		g.Reset()
	}
	for name := range w.classes {
		rn := w.resourceName(name)
		var capacity float64
		for _, n := range w.snap.Nodes {
			if w.snap.Serves(name, n.Name) {
				capacity += float64(n.Allocatable[rn])
			}
		}
		classCapacity.WithLabelValues(name).Set(capacity)
		classBound.WithLabelValues(name).Set(0)
		classLost.WithLabelValues(name).Set(0)
		classWaiting.WithLabelValues(name).Set(0)
	}
	for _, d := range w.devices {
		if w.classes[d.Spec.ClassName] == nil || released(d) {
			continue
		}
		units := float64(d.Spec.Count)
		switch d.Status.Phase {
		case gpuv1alpha1.GPUDeviceBound, gpuv1alpha1.GPUDeviceAttached:
			classBound.WithLabelValues(d.Spec.ClassName).Add(units)
		case gpuv1alpha1.GPUDeviceLost:
			classLost.WithLabelValues(d.Spec.ClassName).Add(units)
			classWaiting.WithLabelValues(d.Spec.ClassName).Add(units)
		default:
			classWaiting.WithLabelValues(d.Spec.ClassName).Add(units)
		}
	}
}
