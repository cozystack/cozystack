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

// Package dbautoscaler implements the Database Horizontal Autoscaler operator:
// it computes the desired read-replica count of a managed database from
// VictoriaMetrics telemetry and applies the decision by patching the
// Application's replicas value, subject to the safety guardrails in the design
// proposal (community/design-proposals/database-horizontal-autoscaling).
package dbautoscaler

import (
	"math"
	"time"
)

// MetricObservation is one driver metric already averaged over the read-serving
// replicas (raw sum divided by replicas-PrimaryCount), together with its target.
type MetricObservation struct {
	Type              string
	AveragePerReplica float64
	Target            float64
}

// Recommendation is a raw desired count computed at a point in time, retained so
// the stabilization windows can look back over recent history (HPA semantics).
type Recommendation struct {
	Time    time.Time
	Desired int32
}

// ScaleInput is the complete, deterministic input to a scaling decision. The
// reconciler resolves every field (from the Application, WorkloadMonitor,
// VictoriaMetrics and the DHA spec) before calling Decide, so the decision logic
// itself is pure and unit-testable without a cluster.
type ScaleInput struct {
	Now          time.Time
	Current      int32 // total instance count observed on the target
	PrimaryCount int32 // non-read-serving instances (CNPG: 1)
	Min          int32
	Max          int32
	QuorumFloor  int32 // e.g. CNPG maxSyncReplicas+1

	// RespectQuorum enforces the quorum floor. When false, the tenant has opted
	// out and the count may fall to Min even below the engine's sync floor.
	RespectQuorum bool

	// QuotaMaxReplicas is the largest total instance count that fits the tenant
	// quota. nil means unknown/unbounded (advisory pre-check could not run).
	QuotaMaxReplicas *int32

	// Metrics is empty when no metric could be read; that trips the fail-safe.
	Metrics []MetricObservation

	// MetricAvailable is false when vmselect was unreachable or a metric is
	// missing: never scale blind.
	MetricAvailable bool

	// LagBraked is true when replication lag exceeds the threshold AND the
	// primary is actively writing (write-activity gated).
	LagBraked bool

	// Operational mirrors WorkloadMonitor.status.operational.
	Operational bool
	// ScaleInFlight is true while availableReplicas != replicas.
	ScaleInFlight bool

	ScaleUpStep      int32
	ScaleDownStep    int32
	ScaleUpWindow    time.Duration
	ScaleDownWindow  time.Duration
	RecommendHistory []Recommendation

	// Convergence tracking for the single-flight deadline.
	ConvergenceDeadline time.Duration
	InFlightSince       *time.Time // when the autoscaler's last patch was applied
	LastConverged       *int32

	DryRun bool
}

// DecisionKind classifies the outcome of a scaling decision.
type DecisionKind int

const (
	// DecisionNoChange means the desired count already matches current.
	DecisionNoChange DecisionKind = iota
	// DecisionScale means a patch to Desired should be applied.
	DecisionScale
	// DecisionFreeze means no patch may be applied this cycle (unsafe/blind/in-flight).
	DecisionFreeze
	// DecisionRollback means a stuck scale-up must be rolled back to LastConverged.
	DecisionRollback
	// DecisionInactive means the target is not scalable at all.
	DecisionInactive
)

// Decision is the result of Decide.
type Decision struct {
	Kind    DecisionKind
	Desired int32 // the replica count to write (valid for Scale/Rollback)

	// Limited records that a guardrail clamped the desired count.
	Limited       bool
	LimitedReason string

	// Able is false when the operator is frozen (metric/lag/in-flight/stuck).
	Able         bool
	UnableReason string

	// RawDesired is the pre-guardrail recommendation, added to history.
	RawDesired int32

	Message string
}

// ceilDiv returns ceil(a/b) for non-negative a and positive b.
func ceilDiv(a, b float64) int32 {
	if b <= 0 {
		return 0
	}
	return int32(math.Ceil(a / b))
}

// desiredForMetric applies the replica model from §1 of the proposal:
//
//	Rcur         = current - primaryCount   (read-serving replicas now)
//	desiredRead  = ceil(Rcur * avgPerReplica / target)
//	desired      = desiredRead + primaryCount
//
// target must be strictly positive; a non-positive target yields current
// (the reconciler rejects such targets before calling, this is defence in depth).
func desiredForMetric(current, primaryCount int32, m MetricObservation) int32 {
	if m.Target <= 0 {
		return current
	}
	rcur := current - primaryCount
	if rcur < 1 {
		rcur = 1
	}
	desiredRead := ceilDiv(float64(rcur)*m.AveragePerReplica, m.Target)
	if desiredRead < 1 {
		desiredRead = 1
	}
	return desiredRead + primaryCount
}

// desiredFromMetrics returns the maximum desired count across all metrics
// (HPA multi-metric semantics).
func desiredFromMetrics(current, primaryCount int32, metrics []MetricObservation) int32 {
	desired := int32(0)
	for _, m := range metrics {
		d := desiredForMetric(current, primaryCount, m)
		if d > desired {
			desired = d
		}
	}
	if desired == 0 {
		return current
	}
	return desired
}

// stabilize applies the stabilization window. For scale-up the recommendation is
// the minimum over the up-window (do not chase a spike); for scale-down it is the
// maximum over the down-window (only shrink once the signal held for the whole
// window). The current raw recommendation participates in both.
func stabilize(now time.Time, current, raw int32, upWindow, downWindow time.Duration, history []Recommendation) int32 {
	if raw > current {
		// scaling up: min over up-window
		min := raw
		for _, r := range history {
			if now.Sub(r.Time) <= upWindow && r.Desired < min {
				min = r.Desired
			}
		}
		if min < current {
			min = current
		}
		return min
	}
	if raw < current {
		// scaling down: max over down-window
		max := raw
		for _, r := range history {
			if now.Sub(r.Time) <= downWindow && r.Desired > max {
				max = r.Desired
			}
		}
		if max > current {
			max = current
		}
		return max
	}
	return current
}

// applyStep limits the change to at most step replicas per decision, EXCEPT that
// reaching the quorum floor overrides the step limit (a safe quorum is never
// rate-limited): a target at or below the floor may jump straight to the floor
// in a single decision. Returns the step-limited desired count.
func applyStep(current, desired, upStep, downStep, quorumFloor int32) int32 {
	switch {
	case desired > current:
		capped := current + upStep
		if desired > capped {
			return capped
		}
		return desired
	case desired < current:
		// Quorum-jump exception: a recommendation at/below the floor is not
		// rate-limited — go straight to the floor.
		if desired <= quorumFloor {
			return quorumFloor
		}
		floored := current - downStep
		if desired < floored {
			return floored
		}
		return desired
	default:
		return current
	}
}
