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
)

// The class's inventory and load, which the GPUClass status, readable by
// every authenticated user, must not show.
var (
	classCapacity = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "cozystack_gpu_class_capacity_units",
		Help: "Allocatable units of the GPU class's resource name summed over every node, free or not.",
	}, []string{"class"})
	classUsed = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "cozystack_gpu_class_used_units",
		Help: "Units of the GPU class's resource name that scheduled, unfinished pods request, summed over every node.",
	}, []string{"class"})
)

func init() {
	metrics.Registry.MustRegister(classCapacity, classUsed)
}
