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
// cap/reject the change and can starve synchronous commits. A negative value is
// clamped to 0 (floor 1) defensively; Scalable rejects it outright before this
// is reached, but the floor must never come out below 1.
func (PostgresAdapter) QuorumFloor(appValues map[string]any) int32 {
	maxSync := nestedInt(appValues, 0, "quorum", "maxSyncReplicas")
	if maxSync < 0 {
		maxSync = 0
	}
	return maxSync + 1
}

// Scalable: a cozystack postgres is always a single primary-replica CNPG cluster
// (no sharded mode). It is scalable unless the synchronous-replica configuration
// is invalid: the postgres values schema does not lower-bound maxSyncReplicas, so
// a negative value would yield a quorum floor below 1 and silently disable the
// floor guard. Reject it as non-scalable rather than scaling on a bad floor.
func (PostgresAdapter) Scalable(appValues map[string]any) (bool, string) {
	if maxSync := nestedInt(appValues, 0, "quorum", "maxSyncReplicas"); maxSync < 0 {
		return false, fmt.Sprintf("invalid quorum.maxSyncReplicas %d (must be >= 0)", maxSync)
	}
	return true, ""
}

// roleJoin returns a `* on(namespace,pod) group_left() kube_pod_labels{...}`
// suffix that restricts a per-pod CNPG metric to this application's pods with
// the given CNPG instance role. CNPG's own metrics carry no role or application
// label, so the pod role and lineage come from kube-state-metrics' kube_pod_labels
// (verified against a live cozystack cluster). The namespace matcher is mandatory
// for tenant isolation.
func roleJoin(app types.NamespacedName, role string) string {
	return fmt.Sprintf(
		` * on(namespace,pod) group_left() kube_pod_labels{namespace=%q,`+
			`label_apps_cozystack_io_application_name=%q,`+
			`label_apps_cozystack_io_application_kind="Postgres",`+
			`label_cnpg_io_instance_role=%q}`,
		app.Namespace, app.Name, role)
}

func (PostgresAdapter) DriverQuery(app types.NamespacedName, metric autoscalingv1alpha1.MetricType) string {
	switch metric {
	case autoscalingv1alpha1.MetricReadConnections:
		// Active client backends on the read-serving (replica) pods.
		return fmt.Sprintf(
			`sum(cnpg_backends_total{namespace=%q,state="active"}%s)`,
			app.Namespace, roleJoin(app, "replica"))
	case autoscalingv1alpha1.MetricReadCPUUtilization:
		// CPU usage of the read-serving pods, in millicores summed over replicas.
		return fmt.Sprintf(
			`sum(rate(container_cpu_usage_seconds_total{namespace=%q,container="postgres"}[5m])%s) * 1000`,
			app.Namespace, roleJoin(app, "replica"))
	default:
		return ""
	}
}

func (PostgresAdapter) ReplicationLagQuery(app types.NamespacedName) string {
	// cnpg_pg_replication_lag (seconds), restricted to the replica pods.
	return fmt.Sprintf(
		`max(cnpg_pg_replication_lag{namespace=%q}%s)`,
		app.Namespace, roleJoin(app, "replica"))
}

func (PostgresAdapter) WriteActivityQuery(app types.NamespacedName) string {
	// Non-zero while the primary is shipping WAL to standbys (write-activity gate
	// for the lag brake): rate of bytes sent from the primary.
	return fmt.Sprintf(
		`sum(rate(cnpg_pg_stat_replication_sent_diff_bytes{namespace=%q}[5m])%s)`,
		app.Namespace, roleJoin(app, "primary"))
}

var _ TopologyAdapter = PostgresAdapter{}
