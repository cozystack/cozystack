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

// RedisAdapter handles the spotahome redis-operator RedisFailover app: one
// master + replicas-1 read-serving slaves, with 3 Sentinels (fixed, not part of
// the read-replica axis). replicas maps to spec.redis.replicas. There is no
// synchronous-replica quorum, so the floor is the single master.
//
// Pods carry the label redisfailovers-role=master|slave, surfaced by
// kube-state-metrics as label_redisfailovers_role, which the PromQL joins on to
// select read-serving slaves. Metrics come from the redis_exporter sidecar.
// The namespace matcher is mandatory (tenant isolation); the exact expressions
// are calibrated on a live cluster in a follow-up.
type RedisAdapter struct{}

func (RedisAdapter) Kind() string                      { return "Redis" }
func (RedisAdapter) ReplicasPath() string              { return "replicas" }
func (RedisAdapter) PrimaryCount() int32               { return 1 }
func (RedisAdapter) ReleaseName(appName string) string { return "redis-" + appName }

// QuorumFloor: async replication, no sync-replica quorum; floor is the master.
func (RedisAdapter) QuorumFloor(appValues map[string]any) int32 { return 1 }

// Scalable: cozystack redis is always a single-shard Sentinel failover (no Redis
// Cluster / sharding), so it is always horizontally scalable.
func (RedisAdapter) Scalable(appValues map[string]any) (bool, string) { return true, "" }

// slaveJoin restricts a per-pod redis_exporter metric to this app's slave pods.
func (a RedisAdapter) slaveJoin(app types.NamespacedName) string {
	return fmt.Sprintf(
		` * on(namespace,pod) group_left() kube_pod_labels{namespace=%q,`+
			`label_app_kubernetes_io_instance=%q,label_redisfailovers_role="slave"}`,
		app.Namespace, a.ReleaseName(app.Name))
}

func (a RedisAdapter) DriverQuery(app types.NamespacedName, metric autoscalingv1alpha1.MetricType) string {
	switch metric {
	case autoscalingv1alpha1.MetricReadConnections:
		return fmt.Sprintf(`sum(redis_connected_clients{namespace=%q}%s)`,
			app.Namespace, a.slaveJoin(app))
	case autoscalingv1alpha1.MetricReadCPUUtilization:
		return fmt.Sprintf(
			`sum(rate(container_cpu_usage_seconds_total{namespace=%q,container="redis"}[5m])%s) * 1000`,
			app.Namespace, a.slaveJoin(app))
	default:
		return ""
	}
}

func (a RedisAdapter) ReplicationLagQuery(app types.NamespacedName) string {
	// Replica lag in bytes: master offset minus each slave's replica offset,
	// restricted to this app's slave pods.
	return fmt.Sprintf(
		`max(redis_master_repl_offset{namespace=%q} - on() group_right()`+
			` (redis_slave_repl_offset{namespace=%q}%s))`,
		app.Namespace, app.Namespace, a.slaveJoin(app))
}

func (a RedisAdapter) WriteActivityQuery(app types.NamespacedName) string {
	// Master repl offset advancing => the primary is taking writes.
	return fmt.Sprintf(
		`sum(rate(redis_master_repl_offset{namespace=%q}[5m])`+
			` * on(namespace,pod) group_left() kube_pod_labels{namespace=%q,`+
			`label_app_kubernetes_io_instance=%q,label_redisfailovers_role="master"})`,
		app.Namespace, app.Namespace, a.ReleaseName(app.Name))
}

var _ TopologyAdapter = RedisAdapter{}
