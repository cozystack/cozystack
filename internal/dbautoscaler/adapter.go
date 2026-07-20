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
	"k8s.io/apimachinery/pkg/types"

	autoscalingv1alpha1 "github.com/cozystack/cozystack/api/autoscaling/v1alpha1"
)

// TopologyAdapter isolates per-engine topology so the reconcile loop stays
// engine-agnostic. Only primary-replica engines are scalable; sharded modes
// report Scalable=false with a reason.
type TopologyAdapter interface {
	// Kind is the apps.cozystack.io Kind this adapter handles (e.g. "Postgres").
	Kind() string

	// ReplicasPath is the values key that holds the total instance count.
	ReplicasPath() string

	// PrimaryCount is the number of non-read-serving instances (CNPG: 1).
	PrimaryCount() int32

	// QuorumFloor is the minimum safe total instance count derived from the
	// engine's synchronous-replica configuration (CNPG: maxSyncReplicas + 1).
	QuorumFloor(appValues map[string]any) int32

	// DriverQuery returns the PromQL for a read-load metric summed over the
	// read-serving replicas. It MUST constrain the query to the target's
	// namespace so a tenant can never read another tenant's series.
	DriverQuery(app types.NamespacedName, metric autoscalingv1alpha1.MetricType) string

	// ReplicationLagQuery returns the PromQL for the replication lag gauge (seconds).
	ReplicationLagQuery(app types.NamespacedName) string

	// WriteActivityQuery returns the PromQL whose value is >0 while the primary's
	// WAL position is advancing (used to gate the lag brake, §5).
	WriteActivityQuery(app types.NamespacedName) string

	// Scalable reports whether the target can be horizontally scaled, and if not, why.
	Scalable(appValues map[string]any) (bool, string)

	// ReleaseName maps an Application name to its Flux release name, which is also
	// the name of its WorkloadMonitor (CNPG: "postgres-"+name).
	ReleaseName(appName string) string
}

// AdapterFor returns the adapter for a kind, or nil if the engine is not
// supported for horizontal autoscaling.
func AdapterFor(kind string) TopologyAdapter {
	switch kind {
	case "Postgres":
		return PostgresAdapter{}
	default:
		return nil
	}
}

// nestedInt reads an integer at path keys from an untyped values map, returning
// def when the path is absent or not a number. JSON round-trips integers as
// float64, so both int and float64 are accepted.
func nestedInt(values map[string]any, def int32, keys ...string) int32 {
	cur := any(values)
	for _, k := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			return def
		}
		cur, ok = m[k]
		if !ok {
			return def
		}
	}
	switch v := cur.(type) {
	case float64:
		return int32(v)
	case int64:
		return int32(v)
	case int:
		return int32(v)
	case int32:
		return v
	default:
		return def
	}
}

// nestedBool reads a bool at path keys, returning def when absent.
func nestedBool(values map[string]any, def bool, keys ...string) bool {
	cur := any(values)
	for _, k := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			return def
		}
		cur, ok = m[k]
		if !ok {
			return def
		}
	}
	if b, ok := cur.(bool); ok {
		return b
	}
	return def
}
