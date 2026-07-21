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

// MariaDBAdapter handles the mariadb-operator MariaDB app. A cozystack mariadb
// with replicas>1 runs asynchronous primary-replica replication (one writable
// primary + replicas-1 read-only standbys, reached via the <release>-secondary
// service); replicas=1 is a standalone instance with no read replicas. There is
// no synchronous-replica quorum, so the floor is just the single primary.
//
// The mariadb-operator exposes no primary/replica role label on pods that is
// vendored in this repo, so read-serving replicas are identified by the
// slave-only exporter series mysql_slave_status_slave_io_running. The PromQL is
// namespace-scoped (tenant isolation, mandatory) and calibrated against real
// mysqld_exporter metrics in a follow-up, like the postgres adapter was.
type MariaDBAdapter struct{}

func (MariaDBAdapter) Kind() string                      { return "MariaDB" }
func (MariaDBAdapter) ReplicasPath() string              { return "replicas" }
func (MariaDBAdapter) PrimaryCount() int32               { return 1 }
func (MariaDBAdapter) ReleaseName(appName string) string { return "mariadb-" + appName }

// QuorumFloor: async replication has no sync-replica quorum key; the floor is the
// single primary.
func (MariaDBAdapter) QuorumFloor(appValues map[string]any) int32 { return 1 }

// Scalable: cozystack mariadb is always single-primary async replication (no
// sharding), so it is always horizontally scalable.
func (MariaDBAdapter) Scalable(appValues map[string]any) (bool, string) { return true, "" }

// replicaScope restricts a per-pod mysqld_exporter metric to this app's replica
// pods: scoped to the app instance via kube_pod_labels and to standbys via the
// slave-only series mysql_slave_status_slave_io_running.
func (a MariaDBAdapter) replicaScope(app types.NamespacedName) string {
	return fmt.Sprintf(
		` and on(namespace,pod) mysql_slave_status_slave_io_running{namespace=%q}`+
			` * on(namespace,pod) group_left() kube_pod_labels{namespace=%q,label_app_kubernetes_io_instance=%q}`,
		app.Namespace, app.Namespace, a.ReleaseName(app.Name))
}

func (a MariaDBAdapter) DriverQuery(app types.NamespacedName, metric autoscalingv1alpha1.MetricType) string {
	switch metric {
	case autoscalingv1alpha1.MetricReadConnections:
		return fmt.Sprintf(`sum(mysql_global_status_threads_connected{namespace=%q}%s)`,
			app.Namespace, a.replicaScope(app))
	case autoscalingv1alpha1.MetricReadCPUUtilization:
		return fmt.Sprintf(
			`sum(rate(container_cpu_usage_seconds_total{namespace=%q,container="mariadb"}[5m])`+
				` * on(namespace,pod) group_left() kube_pod_labels{namespace=%q,label_app_kubernetes_io_instance=%q}`+
				` and on(namespace,pod) mysql_slave_status_slave_io_running{namespace=%q}) * 1000`,
			app.Namespace, app.Namespace, a.ReleaseName(app.Name), app.Namespace)
	default:
		return ""
	}
}

func (a MariaDBAdapter) ReplicationLagQuery(app types.NamespacedName) string {
	// Seconds_Behind_Master is exported only on standbys.
	return fmt.Sprintf(
		`max(mysql_slave_status_seconds_behind_master{namespace=%q}`+
			` * on(namespace,pod) group_left() kube_pod_labels{namespace=%q,label_app_kubernetes_io_instance=%q})`,
		app.Namespace, app.Namespace, a.ReleaseName(app.Name))
}

func (a MariaDBAdapter) WriteActivityQuery(app types.NamespacedName) string {
	// Write activity on the primary (rows written), gating the lag brake.
	return fmt.Sprintf(
		`sum(rate(mysql_global_status_commands_total{namespace=%q,command=~"insert|update|delete|replace"}[5m])`+
			` * on(namespace,pod) group_left() kube_pod_labels{namespace=%q,label_app_kubernetes_io_instance=%q}`+
			` unless on(namespace,pod) mysql_slave_status_slave_io_running{namespace=%q})`,
		app.Namespace, app.Namespace, a.ReleaseName(app.Name), app.Namespace)
}

var _ TopologyAdapter = MariaDBAdapter{}
