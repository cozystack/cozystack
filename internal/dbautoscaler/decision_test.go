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
	"testing"
	"time"

	autoscalingv1alpha1 "github.com/cozystack/cozystack/api/autoscaling/v1alpha1"
)

func ptr[T any](v T) *T { return &v }

func TestDesiredForMetric(t *testing.T) {
	// Example from the proposal §1: current 3 (1 primary + 2 read), avg 210,
	// target 150 => desiredRead = ceil(2*210/150) = ceil(2.8) = 3 => desired 4.
	got := desiredForMetric(3, 1, MetricObservation{AveragePerReplica: 210, Target: 150})
	if got != 4 {
		t.Fatalf("desiredForMetric = %d, want 4", got)
	}

	// At/under target => no growth (desiredRead stays at Rcur).
	if got := desiredForMetric(3, 1, MetricObservation{AveragePerReplica: 100, Target: 150}); got != 3 {
		t.Fatalf("under-target desiredForMetric = %d, want 3", got)
	}

	// Non-positive target is rejected (defence in depth): returns current.
	if got := desiredForMetric(3, 1, MetricObservation{AveragePerReplica: 100, Target: 0}); got != 3 {
		t.Fatalf("zero-target desiredForMetric = %d, want current 3", got)
	}
}

func TestDesiredFromMetricsMax(t *testing.T) {
	// Two metrics, the higher one wins (HPA semantics).
	metrics := []MetricObservation{
		{Type: "ReadConnections", AveragePerReplica: 160, Target: 150},    // -> ceil(2*160/150)=3 -> 4
		{Type: "ReadCPUUtilization", AveragePerReplica: 500, Target: 100}, // -> ceil(2*500/100)=10 -> 11
	}
	if got := desiredFromMetrics(3, 1, metrics); got != 11 {
		t.Fatalf("desiredFromMetrics = %d, want 11", got)
	}
}

func TestApplyStep(t *testing.T) {
	tests := []struct {
		name                                    string
		current, desired, up, down, floor, want int32
	}{
		{"up within step", 3, 4, 1, 1, 2, 4},
		{"up capped by step", 3, 6, 1, 1, 2, 4},
		{"down within step", 6, 5, 1, 1, 2, 5},
		{"down capped by step", 6, 3, 1, 1, 0, 5},
		{"quorum jump overrides step", 6, 2, 1, 1, 4, 4},
		{"down to exactly floor jumps", 6, 4, 1, 1, 4, 4},
		{"no change", 4, 4, 1, 1, 2, 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := applyStep(tt.current, tt.desired, tt.up, tt.down, tt.floor); got != tt.want {
				t.Errorf("applyStep(%d,%d,up=%d,down=%d,floor=%d) = %d, want %d",
					tt.current, tt.desired, tt.up, tt.down, tt.floor, got, tt.want)
			}
		})
	}
}

func TestStabilizeScaleDownHoldsWindow(t *testing.T) {
	now := time.Unix(10000, 0)
	// A higher recommendation 100s ago is still inside a 300s down-window, so a
	// scale-down to 3 is held back to 5.
	hist := []Recommendation{{Time: now.Add(-100 * time.Second), Desired: 5}}
	got := stabilize(now, 6, 3, 300*time.Second, 300*time.Second, hist)
	if got != 5 {
		t.Fatalf("stabilize scale-down = %d, want 5 (held by window)", got)
	}

	// Outside the window it no longer holds: full scale-down to 3.
	hist = []Recommendation{{Time: now.Add(-400 * time.Second), Desired: 5}}
	if got := stabilize(now, 6, 3, 300*time.Second, 300*time.Second, hist); got != 3 {
		t.Fatalf("stabilize scale-down (expired) = %d, want 3", got)
	}
}

func TestStabilizeScaleUpMinOverWindow(t *testing.T) {
	now := time.Unix(10000, 0)
	// A lower recommendation inside the up-window damps a spike up to 8 down to 5.
	hist := []Recommendation{{Time: now.Add(-100 * time.Second), Desired: 5}}
	got := stabilize(now, 4, 8, 300*time.Second, 300*time.Second, hist)
	if got != 5 {
		t.Fatalf("stabilize scale-up = %d, want 5 (min over window)", got)
	}
}

// baseInput is a healthy, ready-to-scale input; tests override fields.
func baseInput() ScaleInput {
	return ScaleInput{
		Now:             time.Unix(10000, 0),
		Current:         3,
		PrimaryCount:    1,
		Min:             2,
		Max:             6,
		QuorumFloor:     2,
		MetricAvailable: true,
		Operational:     true,
		ScaleInFlight:   false,
		ScaleUpStep:     1,
		ScaleDownStep:   1,
		ScaleUpWindow:   0,
		ScaleDownWindow: 0,
		Metrics:         []MetricObservation{{Type: "ReadConnections", AveragePerReplica: 210, Target: 150}},
	}
}

func TestDecideFailSafe(t *testing.T) {
	in := baseInput()
	in.MetricAvailable = false
	d := Decide(in)
	if d.Kind != DecisionFreeze || d.Able {
		t.Fatalf("fail-safe: got kind=%v able=%v", d.Kind, d.Able)
	}
	if d.UnableReason != autoscalingv1alpha1.ReasonMetricUnavailable {
		t.Fatalf("fail-safe reason = %s", d.UnableReason)
	}
}

func TestDecideScaleUp(t *testing.T) {
	d := Decide(baseInput())
	// raw desired 4, step 1 up from 3 => 4.
	if d.Kind != DecisionScale || d.Desired != 4 {
		t.Fatalf("scale-up: kind=%v desired=%d, want Scale/4", d.Kind, d.Desired)
	}
	if !d.Able {
		t.Fatalf("scale-up should be Able")
	}
}

func TestDecideNoChange(t *testing.T) {
	in := baseInput()
	in.Metrics = []MetricObservation{{Type: "ReadConnections", AveragePerReplica: 150, Target: 150}}
	// Rcur=2, ceil(2*150/150)=2 => desired 3 == current.
	d := Decide(in)
	if d.Kind != DecisionNoChange || d.Desired != 3 {
		t.Fatalf("no-change: kind=%v desired=%d", d.Kind, d.Desired)
	}
}

func TestDecideClampMax(t *testing.T) {
	in := baseInput()
	in.Current = 6
	in.ScaleUpStep = 5
	in.Metrics = []MetricObservation{{Type: "ReadConnections", AveragePerReplica: 900, Target: 150}}
	d := Decide(in)
	if d.Desired != 6 || !d.Limited || d.LimitedReason != autoscalingv1alpha1.ReasonAtLimit {
		t.Fatalf("clamp max: desired=%d limited=%v reason=%s", d.Desired, d.Limited, d.LimitedReason)
	}
}

func TestDecideQuorumFloorClampUp(t *testing.T) {
	in := baseInput()
	in.Current = 5
	in.QuorumFloor = 4
	in.Min = 2
	in.ScaleDownStep = 5
	// metric wants to shrink hard; quorum floor holds it at 4.
	in.Metrics = []MetricObservation{{Type: "ReadConnections", AveragePerReplica: 10, Target: 150}}
	d := Decide(in)
	if d.Desired != 4 || !d.Limited || d.LimitedReason != autoscalingv1alpha1.ReasonQuorumFloor {
		t.Fatalf("quorum floor: desired=%d limited=%v reason=%s", d.Desired, d.Limited, d.LimitedReason)
	}
}

func TestDecideQuorumFloorJumpsUpBypassingStep(t *testing.T) {
	// maxSyncReplicas was raised so the floor (5) is above current (2); the climb
	// to a safe quorum must not be throttled by the step limit of 1.
	in := baseInput()
	in.Current = 2
	in.QuorumFloor = 5
	in.Max = 6
	in.ScaleUpStep = 1
	// metrics want to stay low; the quorum floor forces the jump.
	in.Metrics = []MetricObservation{{Type: "ReadConnections", AveragePerReplica: 1, Target: 150}}
	d := Decide(in)
	if d.Desired != 5 || !d.Limited || d.LimitedReason != autoscalingv1alpha1.ReasonQuorumFloor {
		t.Fatalf("quorum up-jump: desired=%d limited=%v reason=%s, want 5/true/QuorumFloor", d.Desired, d.Limited, d.LimitedReason)
	}
}

func TestDecideQuorumExceedsQuotaFreezes(t *testing.T) {
	in := baseInput()
	in.QuorumFloor = 4
	in.QuotaMaxReplicas = ptr(int32(3))
	d := Decide(in)
	if d.Kind != DecisionFreeze || d.UnableReason != autoscalingv1alpha1.ReasonQuorumExceedsQuota {
		t.Fatalf("quorum>quota: kind=%v reason=%s", d.Kind, d.UnableReason)
	}
}

func TestDecideQuotaCeiling(t *testing.T) {
	in := baseInput()
	in.Current = 4
	in.QuorumFloor = 2
	in.Max = 6
	in.QuotaMaxReplicas = ptr(int32(4))
	in.ScaleUpStep = 5
	in.Metrics = []MetricObservation{{Type: "ReadConnections", AveragePerReplica: 900, Target: 150}}
	d := Decide(in)
	if d.Desired != 4 || !d.Limited || d.LimitedReason != autoscalingv1alpha1.ReasonQuotaExceeded {
		t.Fatalf("quota ceiling: desired=%d limited=%v reason=%s", d.Desired, d.Limited, d.LimitedReason)
	}
}

func TestDecideLagBrake(t *testing.T) {
	in := baseInput()
	in.LagBraked = true
	d := Decide(in)
	if d.Kind != DecisionFreeze || d.UnableReason != autoscalingv1alpha1.ReasonReplicationLag {
		t.Fatalf("lag brake: kind=%v reason=%s", d.Kind, d.UnableReason)
	}
}

func TestDecideSingleFlightFreeze(t *testing.T) {
	in := baseInput()
	in.ScaleInFlight = true
	d := Decide(in)
	if d.Kind != DecisionFreeze || d.UnableReason != autoscalingv1alpha1.ReasonScaleInFlight {
		t.Fatalf("single-flight: kind=%v reason=%s", d.Kind, d.UnableReason)
	}
}

func TestDecideNotOperationalFreeze(t *testing.T) {
	in := baseInput()
	in.Operational = false
	d := Decide(in)
	if d.Kind != DecisionFreeze || d.UnableReason != autoscalingv1alpha1.ReasonNotOperational {
		t.Fatalf("not operational: kind=%v reason=%s", d.Kind, d.UnableReason)
	}
}

func TestDecideStuckScalingRollback(t *testing.T) {
	in := baseInput()
	in.Current = 5
	in.ScaleInFlight = true
	in.ConvergenceDeadline = 900 * time.Second
	inFlight := in.Now.Add(-1000 * time.Second)
	in.InFlightSince = &inFlight
	in.LastConverged = ptr(int32(3))
	d := Decide(in)
	if d.Kind != DecisionRollback || d.Desired != 3 || d.UnableReason != autoscalingv1alpha1.ReasonStuckScaling {
		t.Fatalf("stuck rollback: kind=%v desired=%d reason=%s", d.Kind, d.Desired, d.UnableReason)
	}
}

func TestDecideStuckScalingNoSafeTargetFreezes(t *testing.T) {
	in := baseInput()
	in.Current = 5
	in.QuorumFloor = 4
	in.ScaleInFlight = true
	in.ConvergenceDeadline = 900 * time.Second
	inFlight := in.Now.Add(-1000 * time.Second)
	in.InFlightSince = &inFlight
	in.LastConverged = ptr(int32(3)) // below quorum floor => not safe
	d := Decide(in)
	if d.Kind != DecisionFreeze || d.UnableReason != autoscalingv1alpha1.ReasonStuckScaling {
		t.Fatalf("stuck no-safe-target: kind=%v reason=%s", d.Kind, d.UnableReason)
	}
}
