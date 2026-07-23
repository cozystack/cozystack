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

	autoscalingv1alpha1 "github.com/cozystack/cozystack/api/autoscaling/v1alpha1"
)

// safeRollbackTarget returns the replica count to roll a stuck scale-up back to,
// and whether such a target is safe. The target is LastConverged re-validated
// against the current quorum floor and tenant quota (§5, stuck scaling).
func safeRollbackTarget(in ScaleInput, qfloor int32) (int32, bool) {
	if in.LastConverged == nil {
		return 0, false
	}
	target := *in.LastConverged
	if target < qfloor {
		return 0, false
	}
	if in.QuotaMaxReplicas != nil && target > *in.QuotaMaxReplicas {
		return 0, false
	}
	return target, true
}

// Decide is the pure scaling decision. It assumes the target has already been
// found scalable by the adapter; a non-scalable target is reported by the
// reconciler as ScalingActive=False without calling Decide.
//
// Guardrail precedence (§5): quota > quorum floor > min/max.
func Decide(in ScaleInput) Decision {
	// Fail-safe: never scale blind. vmselect unreachable or a metric missing.
	if !in.MetricAvailable || len(in.Metrics) == 0 {
		return Decision{
			Kind:         DecisionFreeze,
			Desired:      in.Current,
			Able:         false,
			UnableReason: autoscalingv1alpha1.ReasonMetricUnavailable,
			Message:      "metric unavailable; refusing to scale blind",
		}
	}

	raw := desiredFromMetrics(in.Current, in.PrimaryCount, in.Metrics)

	// Effective quorum floor: enforced only when the tenant opts in via
	// RespectQuorum (default true). Opted out => no floor beyond the min bound.
	qfloor := in.QuorumFloor
	if !in.RespectQuorum {
		qfloor = 1
	}

	// Quota is a hard ceiling. If even the quorum floor does not fit the quota,
	// we must neither exceed quota nor violate quorum: freeze.
	if in.QuotaMaxReplicas != nil && qfloor > *in.QuotaMaxReplicas {
		return Decision{
			Kind:          DecisionFreeze,
			Desired:       in.Current,
			RawDesired:    raw,
			Able:          false,
			UnableReason:  autoscalingv1alpha1.ReasonQuorumExceedsQuota,
			Limited:       true,
			LimitedReason: autoscalingv1alpha1.ReasonQuorumExceedsQuota,
			Message: fmt.Sprintf("quorum floor %d exceeds tenant quota ceiling %d",
				qfloor, *in.QuotaMaxReplicas),
		}
	}

	// Single-flight with convergence deadline.
	if in.ScaleInFlight {
		if in.InFlightSince != nil && in.ConvergenceDeadline > 0 &&
			in.Now.Sub(*in.InFlightSince) > in.ConvergenceDeadline {
			// A patched scale never converged. Roll back if we have a safe target,
			// releasing single-flight so a relieving scale-down can proceed.
			if target, ok := safeRollbackTarget(in, qfloor); ok && target != in.Current {
				return Decision{
					Kind:         DecisionRollback,
					Desired:      target,
					RawDesired:   raw,
					Able:         false,
					UnableReason: autoscalingv1alpha1.ReasonStuckScaling,
					Message:      fmt.Sprintf("scale did not converge; rolling back to %d", target),
				}
			}
			return Decision{
				Kind:         DecisionFreeze,
				Desired:      in.Current,
				RawDesired:   raw,
				Able:         false,
				UnableReason: autoscalingv1alpha1.ReasonStuckScaling,
				Message:      "scale did not converge and no safe rollback target",
			}
		}
		return Decision{
			Kind:         DecisionFreeze,
			Desired:      in.Current,
			RawDesired:   raw,
			Able:         false,
			UnableReason: autoscalingv1alpha1.ReasonScaleInFlight,
			Message:      "scale in flight; waiting for convergence",
		}
	}

	if !in.Operational {
		return Decision{
			Kind:         DecisionFreeze,
			Desired:      in.Current,
			RawDesired:   raw,
			Able:         false,
			UnableReason: autoscalingv1alpha1.ReasonNotOperational,
			Message:      "target not operational; freezing",
		}
	}

	// Lag brake forbids both directions.
	if in.LagBraked {
		return Decision{
			Kind:         DecisionFreeze,
			Desired:      in.Current,
			RawDesired:   raw,
			Able:         false,
			UnableReason: autoscalingv1alpha1.ReasonReplicationLag,
			Message:      "replication lag above threshold with active writes",
		}
	}

	d := Decision{RawDesired: raw, Able: true}

	// Natural target: stabilized recommendation clamped by the boundary guardrails
	// (min/max, then quorum floor, then quota). The step limit is applied afterwards
	// to the current->natural transition, so reaching the quorum floor is not
	// rate-limited (§5).
	natural := stabilize(in.Now, in.Current, raw, in.ScaleUpWindow, in.ScaleDownWindow, in.RecommendHistory)

	if natural < in.Min {
		natural = in.Min
	}
	if natural > in.Max {
		natural = in.Max
		d.Limited = true
		d.LimitedReason = autoscalingv1alpha1.ReasonAtLimit
		d.Message = fmt.Sprintf("held at maxReplicas %d", in.Max)
	}

	// Quorum floor overrides min/max: clamp UP to the floor even above max
	// (skipped when the tenant opted out via RespectQuorum: qfloor is then 1).
	if natural < qfloor {
		natural = qfloor
		d.Limited = true
		d.LimitedReason = autoscalingv1alpha1.ReasonQuorumFloor
		d.Message = fmt.Sprintf("held at quorum floor %d", qfloor)
	}

	// Quota is the hard ceiling and wins over everything (we already proved the
	// quorum floor fits the quota, so this never drops below a safe quorum).
	if in.QuotaMaxReplicas != nil && natural > *in.QuotaMaxReplicas {
		natural = *in.QuotaMaxReplicas
		d.Limited = true
		d.LimitedReason = autoscalingv1alpha1.ReasonQuotaExceeded
		d.Message = fmt.Sprintf("clamped to tenant quota ceiling %d", *in.QuotaMaxReplicas)
	}

	// Rate-limit the current->natural transition. Reaching the quorum floor is
	// exempt from the step limit on the way UP: urgently climbing to a safe quorum
	// must not be rate-limited, and the step must never leave the count below the
	// floor while climbing (whether the target lands on the floor or above it).
	// Scaling DOWN always respects scaleDownStep (shedding many standbys at once is
	// not a safety need), gated additionally by the scale-down stabilization window.
	desired := applyStep(in.Current, natural, in.ScaleUpStep, in.ScaleDownStep)
	if natural > in.Current && desired < qfloor {
		// climbing but the step would leave us below a safe quorum — jump to the floor.
		desired = qfloor
	}

	d.Desired = desired
	if desired == in.Current {
		d.Kind = DecisionNoChange
	} else {
		d.Kind = DecisionScale
	}
	return d
}
