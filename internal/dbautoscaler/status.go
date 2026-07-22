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
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	autoscalingv1alpha1 "github.com/cozystack/cozystack/api/autoscaling/v1alpha1"
)

type rulesResolved struct {
	window time.Duration
	step   int32
}

// resolveBehavior fills in scale-up/scale-down windows and steps from the DHA,
// applying the proposal's defaults for omitted fields.
func resolveBehavior(dha *autoscalingv1alpha1.DatabaseHorizontalAutoscaler) (up, down rulesResolved) {
	up = rulesResolved{window: defaultScaleUpWindow, step: defaultStep}
	down = rulesResolved{window: defaultScaleDownWindow, step: defaultStep}
	if dha.Spec.Behavior == nil {
		return up, down
	}
	if b := dha.Spec.Behavior.ScaleUp; b != nil {
		if b.StabilizationWindowSeconds != nil {
			up.window = time.Duration(*b.StabilizationWindowSeconds) * time.Second
		}
		if b.Step != nil {
			up.step = *b.Step
		}
	}
	if b := dha.Spec.Behavior.ScaleDown; b != nil {
		if b.StabilizationWindowSeconds != nil {
			down.window = time.Duration(*b.StabilizationWindowSeconds) * time.Second
		}
		if b.Step != nil {
			down.step = *b.Step
		}
	}
	return up, down
}

// backoffDuration is the wait before re-attempting a scale-up size that failed
// to converge: the convergence deadline, doubled per consecutive failure, capped
// at 8x.
func backoffDuration(base time.Duration, count int) time.Duration {
	if count < 1 {
		count = 1
	}
	d := base
	for i := 1; i < count && i < 4; i++ {
		d *= 2
	}
	return d
}

func resolveConvergenceDeadline(dha *autoscalingv1alpha1.DatabaseHorizontalAutoscaler) time.Duration {
	if dha.Spec.Behavior != nil && dha.Spec.Behavior.ConvergenceDeadlineSeconds != nil {
		return time.Duration(*dha.Spec.Behavior.ConvergenceDeadlineSeconds) * time.Second
	}
	return defaultConvergenceDeadline
}

// appendHistory adds a recommendation and prunes entries older than the widest
// stabilization window so the slice stays bounded.
func appendHistory(history []Recommendation, now time.Time, desired int32, keep time.Duration) []Recommendation {
	history = append(history, Recommendation{Time: now, Desired: desired})
	cutoff := now.Add(-keep)
	pruned := history[:0]
	for _, r := range history {
		if !r.Time.Before(cutoff) {
			pruned = append(pruned, r)
		}
	}
	return pruned
}

func max(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

// setScalingActive sets the ScalingActive condition.
func (r *Reconciler) setScalingActive(dha *autoscalingv1alpha1.DatabaseHorizontalAutoscaler, active bool, reason, msg string) {
	status := metav1.ConditionTrue
	if !active {
		status = metav1.ConditionFalse
	}
	apimeta.SetStatusCondition(&dha.Status.Conditions, metav1.Condition{
		Type:               autoscalingv1alpha1.ConditionScalingActive,
		Status:             status,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: dha.Generation,
	})
}

// applyDecisionToStatus mirrors a decision into the DHA status conditions and
// the current/desired metrics.
func (r *Reconciler) applyDecisionToStatus(
	dha *autoscalingv1alpha1.DatabaseHorizontalAutoscaler,
	in ScaleInput,
	d Decision,
	obs []MetricObservation,
	ownershipConflict bool,
) {
	dha.Status.CurrentReplicas = in.Current
	dha.Status.DesiredReplicas = d.Desired
	dha.Status.LastConvergedReplicas = in.LastConverged

	dha.Status.CurrentMetrics = make([]autoscalingv1alpha1.MetricStatus, 0, len(obs))
	for _, o := range obs {
		// Render each observed value in the same unit as its target. CPU averages
		// are already millicores (the driver query yields millicores), so wrap them
		// directly as a milli-quantity ("250" millicores => "250m"). Plain-count
		// metrics (ReadConnections) are scaled by 1000 so the milli-quantity renders
		// the whole number ("150" => "150").
		var q resource.Quantity
		if o.Type == string(autoscalingv1alpha1.MetricReadCPUUtilization) {
			q = *resource.NewMilliQuantity(int64(o.AveragePerReplica), resource.DecimalSI)
		} else {
			q = *resource.NewMilliQuantity(int64(o.AveragePerReplica*1000), resource.DecimalSI)
		}
		dha.Status.CurrentMetrics = append(dha.Status.CurrentMetrics, autoscalingv1alpha1.MetricStatus{
			Type:         autoscalingv1alpha1.MetricType(o.Type),
			AverageValue: q,
		})
	}

	// AbleToScale.
	if d.Able {
		apimeta.SetStatusCondition(&dha.Status.Conditions, metav1.Condition{
			Type:               autoscalingv1alpha1.ConditionAbleToScale,
			Status:             metav1.ConditionTrue,
			Reason:             autoscalingv1alpha1.ReasonReady,
			Message:            "ready to scale",
			ObservedGeneration: dha.Generation,
		})
	} else {
		apimeta.SetStatusCondition(&dha.Status.Conditions, metav1.Condition{
			Type:               autoscalingv1alpha1.ConditionAbleToScale,
			Status:             metav1.ConditionFalse,
			Reason:             d.UnableReason,
			Message:            d.Message,
			ObservedGeneration: dha.Generation,
		})
	}

	// ScalingLimited (ownership conflict takes precedence in the message).
	switch {
	case ownershipConflict:
		apimeta.SetStatusCondition(&dha.Status.Conditions, metav1.Condition{
			Type:               autoscalingv1alpha1.ConditionScalingLimited,
			Status:             metav1.ConditionTrue,
			Reason:             autoscalingv1alpha1.ReasonOwnershipConflict,
			Message:            "replicas changed by a competing writer",
			ObservedGeneration: dha.Generation,
		})
	case d.Limited:
		apimeta.SetStatusCondition(&dha.Status.Conditions, metav1.Condition{
			Type:               autoscalingv1alpha1.ConditionScalingLimited,
			Status:             metav1.ConditionTrue,
			Reason:             d.LimitedReason,
			Message:            d.Message,
			ObservedGeneration: dha.Generation,
		})
	default:
		apimeta.SetStatusCondition(&dha.Status.Conditions, metav1.Condition{
			Type:               autoscalingv1alpha1.ConditionScalingLimited,
			Status:             metav1.ConditionFalse,
			Reason:             autoscalingv1alpha1.ReasonReady,
			Message:            "within limits",
			ObservedGeneration: dha.Generation,
		})
	}
}
