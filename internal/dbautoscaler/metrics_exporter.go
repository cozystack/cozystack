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

package dbautoscaler

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

// Prometheus gauges exported per DHA so the shipped alerts can fire on freeze /
// limit-reached conditions. Registered against the controller-runtime metrics
// registry, served on the operator's metrics endpoint.
var (
	metricCurrentReplicas = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "db_autoscaler_current_replicas",
		Help: "Current total instance count observed on the target.",
	}, []string{"namespace", "name"})
	metricDesiredReplicas = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "db_autoscaler_desired_replicas",
		Help: "Desired total instance count computed by the autoscaler.",
	}, []string{"namespace", "name"})
	metricScalingLimited = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "db_autoscaler_scaling_limited",
		Help: "1 when a guardrail is clamping the desired count (min/max, quorum, quota) or a competing writer holds replicas.",
	}, []string{"namespace", "name"})
	metricFrozen = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "db_autoscaler_frozen",
		Help: "1 when the autoscaler is frozen and unable to scale (metric unavailable, lag brake, stuck scaling, quorum exceeds quota).",
	}, []string{"namespace", "name"})
)

func init() {
	metrics.Registry.MustRegister(metricCurrentReplicas, metricDesiredReplicas, metricScalingLimited, metricFrozen)
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// exportMetrics publishes the decision as gauges for the given DHA.
func exportMetrics(namespace, name string, in ScaleInput, d Decision, ownershipConflict bool) {
	labels := prometheus.Labels{"namespace": namespace, "name": name}
	metricCurrentReplicas.With(labels).Set(float64(in.Current))
	metricDesiredReplicas.With(labels).Set(float64(d.Desired))
	metricScalingLimited.With(labels).Set(b2f(d.Limited || ownershipConflict))
	metricFrozen.With(labels).Set(b2f(!d.Able))
}

// clearMetrics drops the gauges for a deleted DHA.
func clearMetrics(namespace, name string) {
	labels := prometheus.Labels{"namespace": namespace, "name": name}
	metricCurrentReplicas.Delete(labels)
	metricDesiredReplicas.Delete(labels)
	metricScalingLimited.Delete(labels)
	metricFrozen.Delete(labels)
}
