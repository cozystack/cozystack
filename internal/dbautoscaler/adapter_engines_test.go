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
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/types"

	autoscalingv1alpha1 "github.com/cozystack/cozystack/api/autoscaling/v1alpha1"
)

func TestAdapterForAllEngines(t *testing.T) {
	scalable := map[string]string{
		"Postgres": "postgres-",
		"MariaDB":  "mariadb-",
		"Redis":    "redis-",
		"MongoDB":  "mongodb-",
	}
	for kind, prefix := range scalable {
		a := AdapterFor(kind)
		if a == nil {
			t.Fatalf("AdapterFor(%q) = nil, want an adapter", kind)
		}
		if a.Kind() != kind {
			t.Errorf("%s: Kind() = %q", kind, a.Kind())
		}
		if a.ReplicasPath() != "replicas" {
			t.Errorf("%s: ReplicasPath() = %q", kind, a.ReplicasPath())
		}
		if a.PrimaryCount() != 1 {
			t.Errorf("%s: PrimaryCount() = %d, want 1", kind, a.PrimaryCount())
		}
		if got := a.ReleaseName("db"); got != prefix+"db" {
			t.Errorf("%s: ReleaseName(db) = %q, want %q", kind, got, prefix+"db")
		}
	}
	// Sharded / data-rebalancing engines have no adapter.
	for _, kind := range []string{"ClickHouse", "Kafka", "OpenSearch", "RabbitMQ"} {
		if AdapterFor(kind) != nil {
			t.Errorf("AdapterFor(%q) should be nil (not horizontally scalable)", kind)
		}
	}
}

func TestMongoDBNotScalable(t *testing.T) {
	a := MongoDBAdapter{}
	// Sharded mode: not scalable (data rebalancing).
	ok, reason := a.Scalable(map[string]any{"sharding": true})
	if ok || reason == "" {
		t.Fatalf("sharded MongoDB must be not-scalable with a reason, got ok=%v reason=%q", ok, reason)
	}
	// Replica-set mode: also not-scalable yet (exporter disabled / uncalibrated).
	ok, reason = a.Scalable(map[string]any{})
	if ok || reason == "" {
		t.Fatalf("replica-set MongoDB must be not-scalable-yet with a reason, got ok=%v reason=%q", ok, reason)
	}
}

func TestRedisScalableMariaDBNotYet(t *testing.T) {
	if ok, _ := (RedisAdapter{}).Scalable(map[string]any{}); !ok {
		t.Errorf("redis should be scalable")
	}
	// MariaDB is not scalable until the chart supports on-the-fly scale-out
	// (bootstrapFrom); otherwise it thrashes patch->stuck->rollback.
	ok, reason := (MariaDBAdapter{}).Scalable(map[string]any{})
	if ok {
		t.Errorf("mariadb must report not-scalable until the chart supports scale-out")
	}
	if reason == "" {
		t.Errorf("mariadb not-scalable must carry a reason")
	}
}

func TestNewAdaptersQuorumFloorIsOne(t *testing.T) {
	// None of these engines expose a synchronous-replica quorum key.
	for _, a := range []TopologyAdapter{MariaDBAdapter{}, RedisAdapter{}, MongoDBAdapter{}} {
		if got := a.QuorumFloor(map[string]any{"quorum": map[string]any{"maxSyncReplicas": float64(5)}}); got != 1 {
			t.Errorf("%s: QuorumFloor = %d, want 1 (no sync-quorum concept)", a.Kind(), got)
		}
	}
}

// TestEngineQueriesNamespaceScoped is the security-critical guard: every driver,
// lag and write-activity query MUST constrain to the target namespace so a
// tenant can never read another tenant's series.
func TestEngineQueriesNamespaceScoped(t *testing.T) {
	app := types.NamespacedName{Namespace: "tenant-acme", Name: "db"}
	adapters := []TopologyAdapter{MariaDBAdapter{}, RedisAdapter{}, MongoDBAdapter{}}
	for _, a := range adapters {
		queries := []string{
			a.DriverQuery(app, autoscalingv1alpha1.MetricReadConnections),
			a.DriverQuery(app, autoscalingv1alpha1.MetricReadCPUUtilization),
			a.ReplicationLagQuery(app),
			a.WriteActivityQuery(app),
		}
		for i, q := range queries {
			if q == "" {
				// An empty query is a deliberate "no signal" (e.g. Redis has no
				// seconds-based replication-lag gauge); skip scope checks for it.
				continue
			}
			if !strings.Contains(q, `namespace="tenant-acme"`) {
				t.Errorf("%s: query %d not namespace-scoped: %s", a.Kind(), i, q)
			}
			// must reference the app's release name for scoping
			if !strings.Contains(q, a.ReleaseName("db")) {
				t.Errorf("%s: query %d does not reference release %q: %s", a.Kind(), i, a.ReleaseName("db"), q)
			}
		}
	}
}

// TestRedisLagBrakeDisabled: Redis exposes replication progress only in bytes,
// which cannot be compared to a seconds threshold, so the lag query is empty and
// the brake is off. The other engines keep a real seconds-based lag query.
func TestRedisLagBrakeDisabled(t *testing.T) {
	app := types.NamespacedName{Namespace: "tenant-acme", Name: "db"}
	if q := (RedisAdapter{}).ReplicationLagQuery(app); q != "" {
		t.Fatalf("Redis ReplicationLagQuery must be empty (byte-unit lag cannot gate a seconds threshold), got %q", q)
	}
	for _, a := range []TopologyAdapter{PostgresAdapter{}, MariaDBAdapter{}, MongoDBAdapter{}} {
		if q := a.ReplicationLagQuery(app); q == "" {
			t.Errorf("%s must keep a seconds-based replication-lag query", a.Kind())
		}
	}
}

func TestEngineDriverQueryUnknownMetric(t *testing.T) {
	app := types.NamespacedName{Namespace: "ns", Name: "db"}
	for _, a := range []TopologyAdapter{MariaDBAdapter{}, RedisAdapter{}, MongoDBAdapter{}} {
		if q := a.DriverQuery(app, autoscalingv1alpha1.MetricType("Bogus")); q != "" {
			t.Errorf("%s: unknown metric should yield empty query, got %s", a.Kind(), q)
		}
	}
}
