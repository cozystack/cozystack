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
// Metrics come from a single mysqld_exporter Deployment (<release>-metrics) that
// scrapes every instance, distinguishing them by the per-instance `target` label
// (mariadb-<name>-0, -1, ...) rather than the pod label. Read-serving standbys
// are the targets that expose the slave-only series mysql_slave_status_*; the
// primary is the target without it. Calibrated against real metrics on a live
// cozystack cluster. Namespace matcher is mandatory (tenant isolation).
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

// metricsJob is the mysqld_exporter scrape job for this app.
func (a MariaDBAdapter) metricsJob(app types.NamespacedName) string {
	return a.ReleaseName(app.Name) + "-metrics"
}

// replicaFilter keeps only the standby targets (those exporting slave status).
func (a MariaDBAdapter) replicaFilter(app types.NamespacedName) string {
	return fmt.Sprintf(` and on(target) mysql_slave_status_slave_io_running{namespace=%q,job=%q}`,
		app.Namespace, a.metricsJob(app))
}

func (a MariaDBAdapter) DriverQuery(app types.NamespacedName, metric autoscalingv1alpha1.MetricType) string {
	job := a.metricsJob(app)
	switch metric {
	case autoscalingv1alpha1.MetricReadConnections:
		return fmt.Sprintf(`sum(mysql_global_status_threads_connected{namespace=%q,job=%q}%s)`,
			app.Namespace, job, a.replicaFilter(app))
	case autoscalingv1alpha1.MetricReadCPUUtilization:
		// container_cpu is per instance pod; select replica pods by the
		// mariadb-operator role label.
		return fmt.Sprintf(
			`sum(rate(container_cpu_usage_seconds_total{namespace=%q,container="mariadb"}[5m])`+
				` * on(namespace,pod) group_left() kube_pod_labels{namespace=%q,label_app_kubernetes_io_instance=%q,label_k8s_mariadb_com_role="replica"}) * 1000`,
			app.Namespace, app.Namespace, a.ReleaseName(app.Name))
	default:
		return ""
	}
}

func (a MariaDBAdapter) ReplicationLagQuery(app types.NamespacedName) string {
	// Seconds_Behind_Master is exported only on standbys.
	return fmt.Sprintf(`max(mysql_slave_status_seconds_behind_master{namespace=%q,job=%q})`,
		app.Namespace, a.metricsJob(app))
}

func (a MariaDBAdapter) WriteActivityQuery(app types.NamespacedName) string {
	// Query rate on the primary (the target without slave status), gating the
	// lag brake.
	return fmt.Sprintf(
		`sum(rate(mysql_global_status_queries{namespace=%q,job=%q}[5m])`+
			` unless on(target) mysql_slave_status_slave_io_running{namespace=%q,job=%q})`,
		app.Namespace, a.metricsJob(app), app.Namespace, a.metricsJob(app))
}

var _ TopologyAdapter = MariaDBAdapter{}
