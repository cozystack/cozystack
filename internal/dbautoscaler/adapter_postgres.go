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
	"fmt"

	"k8s.io/apimachinery/pkg/types"

	autoscalingv1alpha1 "github.com/cozystack/cozystack/api/autoscaling/v1alpha1"
)

// PostgresAdapter is the CloudNativePG (CNPG) topology adapter. A cozystack
// postgres Application renders a single CNPG Cluster with instances =
// .Values.replicas (1 primary + replicas-1 standbys); read traffic is served by
// the standbys through the <release>-ro endpoint.
//
// The PromQL expressions below are the MVP defaults: they are constrained to the
// target namespace (required for tenant isolation) and select the CNPG replica
// pods. Exact metric names are calibrated against real workloads (see the
// proposal's Open questions); the namespace constraint is not negotiable.
type PostgresAdapter struct{}

func (PostgresAdapter) Kind() string         { return "Postgres" }
func (PostgresAdapter) ReplicasPath() string { return "replicas" }
func (PostgresAdapter) PrimaryCount() int32  { return 1 }

// ReleaseName is the CNPG Flux release / WorkloadMonitor name for an app.
func (PostgresAdapter) ReleaseName(appName string) string { return "postgres-" + appName }

// QuorumFloor is maxSyncReplicas + 1: the chart documents maxSyncReplicas as
// "must be less than total replicas", so dropping to/below it makes CNPG
// cap/reject the change and can starve synchronous commits.
func (PostgresAdapter) QuorumFloor(appValues map[string]any) int32 {
	maxSync := nestedInt(appValues, 0, "quorum", "maxSyncReplicas")
	return maxSync + 1
}

// Scalable: a cozystack postgres is always a single primary-replica CNPG cluster
// (no sharded mode), so it is always horizontally scalable.
func (PostgresAdapter) Scalable(appValues map[string]any) (bool, string) {
	return true, ""
}

func (PostgresAdapter) DriverQuery(app types.NamespacedName, metric autoscalingv1alpha1.MetricType) string {
	// Read-serving pods are the CNPG standbys: cnpg.io/instanceRole="replica".
	replicaSelector := fmt.Sprintf(`namespace=%q,cnpg_io_cluster=%q,cnpg_io_instanceRole="replica"`, app.Namespace, app.Name)
	switch metric {
	case autoscalingv1alpha1.MetricReadConnections:
		return fmt.Sprintf(`sum(cnpg_backends_total{%s})`, replicaSelector)
	case autoscalingv1alpha1.MetricReadCPUUtilization:
		// CPU utilisation as a percentage of the pod's CPU request, summed over
		// the read-serving pods.
		podSelector := fmt.Sprintf(`namespace=%q,pod=~"%s-[0-9]+"`, app.Namespace, app.Name)
		return fmt.Sprintf(`sum(rate(container_cpu_usage_seconds_total{%s,container="postgres"}[5m])) * 100`, podSelector)
	default:
		return ""
	}
}

func (PostgresAdapter) ReplicationLagQuery(app types.NamespacedName) string {
	// cnpg_pg_replication_lag is exported in seconds and already scraped into
	// VictoriaMetrics for cozystack's CNPG dashboards/alerts.
	return fmt.Sprintf(`max(cnpg_pg_replication_lag{namespace=%q,cnpg_io_cluster=%q})`, app.Namespace, app.Name)
}

func (PostgresAdapter) WriteActivityQuery(app types.NamespacedName) string {
	// Non-zero while the primary's WAL is advancing (write-activity gating for the
	// lag brake): rate of the current WAL LSN on the primary.
	return fmt.Sprintf(
		`sum(rate(cnpg_pg_replication_in_recovery{namespace=%q,cnpg_io_cluster=%q}[5m]))`,
		app.Namespace, app.Name)
}

var _ TopologyAdapter = PostgresAdapter{}
