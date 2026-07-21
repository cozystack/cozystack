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

// MongoDBAdapter handles the Percona Server for MongoDB app. In replica-set mode
// (sharding: false) it is 1 dynamically-elected primary + replicas-1 secondaries
// that serve reads (readPreference); replicas maps to replset rs0 size. When
// sharding: true the app is NOT horizontally scalable (adding shards requires
// data rebalancing), so Scalable returns false.
//
// Two caveats, both surfaced honestly rather than hidden:
//   - MongoDB elects its primary dynamically and Percona puts no primary/replica
//     role label on pods, so read-serving members are selected by the exporter's
//     member-state series (mongodb_mongod_replset_my_state == 2 => SECONDARY),
//     not by a kube_pod_labels role join.
//   - cozystack ships the Percona PMM/mongodb_exporter DISABLED by default, so
//     these series are not scraped until an operator enables monitoring. Until
//     then the reconcile loop reads no metric and fail-safe freezes (never scales
//     blind) — correct, but it means MongoDB autoscaling needs the exporter on.
//
// The namespace matcher is mandatory (tenant isolation); expressions are
// calibrated on a live cluster in a follow-up.
type MongoDBAdapter struct{}

func (MongoDBAdapter) Kind() string                      { return "MongoDB" }
func (MongoDBAdapter) ReplicasPath() string              { return "replicas" }
func (MongoDBAdapter) PrimaryCount() int32               { return 1 }
func (MongoDBAdapter) ReleaseName(appName string) string { return "mongodb-" + appName }

// QuorumFloor: no sync-replica key exists; the floor is the single primary. The
// election voting majority is not force-derived here — the DHA minReplicas (>=2)
// keeps the set serving reads, and operators keep an odd member count.
func (MongoDBAdapter) QuorumFloor(appValues map[string]any) int32 { return 1 }

// Scalable: only replica-set mode is scalable; a sharded cluster is not (adding
// shards is an orchestrated data-rebalancing procedure, out of scope by design).
func (MongoDBAdapter) Scalable(appValues map[string]any) (bool, string) {
	if nestedBool(appValues, false, "sharding") {
		return false, "sharded MongoDB is not horizontally scalable (shard addition requires data rebalancing)"
	}
	return true, ""
}

// secondaryScope restricts a per-pod mongodb_exporter metric to this app's
// SECONDARY members (my_state == 2), scoped to the app instance.
func (a MongoDBAdapter) secondaryScope(app types.NamespacedName) string {
	return fmt.Sprintf(
		` and on(namespace,pod) (mongodb_mongod_replset_my_state{namespace=%q} == 2)`+
			` * on(namespace,pod) group_left() kube_pod_labels{namespace=%q,label_app_kubernetes_io_instance=%q}`,
		app.Namespace, app.Namespace, a.ReleaseName(app.Name))
}

func (a MongoDBAdapter) DriverQuery(app types.NamespacedName, metric autoscalingv1alpha1.MetricType) string {
	switch metric {
	case autoscalingv1alpha1.MetricReadConnections:
		return fmt.Sprintf(`sum(mongodb_ss_connections{namespace=%q,conn_type="current"}%s)`,
			app.Namespace, a.secondaryScope(app))
	case autoscalingv1alpha1.MetricReadCPUUtilization:
		return fmt.Sprintf(
			`sum(rate(container_cpu_usage_seconds_total{namespace=%q,container="mongod"}[5m])%s) * 1000`,
			app.Namespace, a.secondaryScope(app))
	default:
		return ""
	}
}

func (a MongoDBAdapter) ReplicationLagQuery(app types.NamespacedName) string {
	// Secondary replication lag in seconds.
	return fmt.Sprintf(
		`max(mongodb_mongod_replset_member_replication_lag{namespace=%q}`+
			` * on(namespace,pod) group_left() kube_pod_labels{namespace=%q,label_app_kubernetes_io_instance=%q})`,
		app.Namespace, app.Namespace, a.ReleaseName(app.Name))
}

func (a MongoDBAdapter) WriteActivityQuery(app types.NamespacedName) string {
	// Write ops on the primary (my_state == 1) gate the lag brake.
	return fmt.Sprintf(
		`sum(rate(mongodb_ss_opcounters{namespace=%q,legacy_op_type=~"insert|update|delete"}[5m])`+
			` and on(namespace,pod) (mongodb_mongod_replset_my_state{namespace=%q} == 1)`+
			` * on(namespace,pod) group_left() kube_pod_labels{namespace=%q,label_app_kubernetes_io_instance=%q})`,
		app.Namespace, app.Namespace, app.Namespace, a.ReleaseName(app.Name))
}

var _ TopologyAdapter = MongoDBAdapter{}
