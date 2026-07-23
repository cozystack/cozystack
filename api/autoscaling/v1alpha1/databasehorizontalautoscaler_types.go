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

package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// ManagedByAnnotation marks an Application whose replicas value is owned by a
	// DatabaseHorizontalAutoscaler. Its value is the name of the owning DHA.
	ManagedByAnnotation = "autoscaling.cozystack.io/managed-by"

	// FieldManager is the field manager the operator uses when it writes the
	// Application's replicas value.
	FieldManager = "db-autoscaler"

	// Finalizer ensures the operator clears the managed-by marker from the target
	// Application before the DHA is removed, so ownership enforcement does not
	// outlive the DHA.
	Finalizer = "autoscaling.cozystack.io/finalizer"

	// DefaultAPIGroup is the API group of the scale target when TargetRef.APIGroup
	// is left empty.
	DefaultAPIGroup = "apps.cozystack.io"
)

// Condition types reported on the DHA status.
const (
	// ConditionScalingActive is True while the target is scalable and the loop is
	// making decisions, False (with a reason) when scaling is disabled for the target.
	ConditionScalingActive = "ScalingActive"
	// ConditionAbleToScale is True when the operator is free to apply a decision,
	// False while frozen (metric unavailable, lag brake, single-flight, stuck scaling).
	ConditionAbleToScale = "AbleToScale"
	// ConditionScalingLimited is True when a guardrail clamped the desired count
	// (min/max, quorum floor, quota) or a competing writer holds replicas.
	ConditionScalingLimited = "ScalingLimited"
)

// Condition reasons.
const (
	ReasonSharded            = "Sharded"
	ReasonNotScalable        = "NotScalable"
	ReasonReady              = "Ready"
	ReasonMetricUnavailable  = "MetricUnavailable"
	ReasonReplicationLag     = "ReplicationLag"
	ReasonScaleInFlight      = "ScaleInFlight"
	ReasonNotOperational     = "NotOperational"
	ReasonStuckScaling       = "StuckScaling"
	ReasonQuorumFloor        = "QuorumFloor"
	ReasonAtLimit            = "AtLimit"
	ReasonQuotaExceeded      = "QuotaExceeded"
	ReasonQuorumExceedsQuota = "QuorumExceedsQuota"
	ReasonOwnershipConflict  = "OwnershipConflict"
	ReasonInvalidTarget      = "InvalidMetricTarget"
)

// MetricType is the fixed, safe set of driver metrics the autoscaler understands.
// +kubebuilder:validation:Enum=ReadConnections;ReadCPUUtilization
type MetricType string

const (
	// MetricReadConnections drives on the number of client connections served by
	// the read-serving replicas.
	MetricReadConnections MetricType = "ReadConnections"
	// MetricReadCPUUtilization drives on CPU usage of the read-serving replicas,
	// in millicores. The target is read as a Kubernetes CPU quantity, so both
	// idioms work as expected: "250m" = 250 millicores, "1" = 1000 millicores.
	MetricReadCPUUtilization MetricType = "ReadCPUUtilization"
)

// TargetRef points at the managed database Application to scale.
type TargetRef struct {
	// Kind of the target Application (e.g. Postgres).
	// +required
	Kind string `json:"kind"`

	// Name of the target Application in the same namespace as the DHA.
	// +required
	Name string `json:"name"`

	// APIGroup of the target. Defaults to apps.cozystack.io.
	// +kubebuilder:default="apps.cozystack.io"
	// +optional
	APIGroup string `json:"apiGroup,omitempty"`
}

// MetricTarget is the per-read-serving-replica goal for a metric.
type MetricTarget struct {
	// AverageValue is the target value per read-serving replica. It must be
	// strictly positive; a zero or negative target makes the autoscaler freeze
	// the DHA with condition AbleToScale=False, reason InvalidMetricTarget
	// (the metric cannot be divided by a non-positive target).
	// +required
	AverageValue resource.Quantity `json:"averageValue"`
}

// MetricSpec selects one driver metric and its target.
type MetricSpec struct {
	// Type of the driver metric.
	// +required
	Type MetricType `json:"type"`

	// Target goal for this metric.
	// +required
	Target MetricTarget `json:"target"`
}

// ScalingRules bounds how fast the autoscaler moves in one direction.
type ScalingRules struct {
	// StabilizationWindowSeconds is how long the signal must hold before acting.
	// +kubebuilder:validation:Minimum=0
	// +optional
	StabilizationWindowSeconds *int32 `json:"stabilizationWindowSeconds,omitempty"`

	// Step is the maximum number of replicas changed in a single decision.
	// The quorum floor overrides this limit (a safe quorum is never rate-limited).
	// +kubebuilder:validation:Minimum=1
	// +optional
	Step *int32 `json:"step,omitempty"`
}

// Behavior tunes the scaling dynamics.
type Behavior struct {
	// ScaleUp rules.
	// +optional
	ScaleUp *ScalingRules `json:"scaleUp,omitempty"`

	// ScaleDown rules (typically a longer stabilization window than ScaleUp).
	// +optional
	ScaleDown *ScalingRules `json:"scaleDown,omitempty"`

	// ConvergenceDeadlineSeconds bounds how long a patched scale may take to
	// converge (availableReplicas == replicas) before the operator surfaces
	// StuckScaling and rolls back to lastConvergedReplicas. Capped at 24h so the
	// exponential re-attempt backoff (up to 8x this value) cannot overflow a
	// time.Duration.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=86400
	// +optional
	ConvergenceDeadlineSeconds *int32 `json:"convergenceDeadlineSeconds,omitempty"`
}

// Constraints are the safety knobs for stateful workloads.
type Constraints struct {
	// RespectQuorum keeps the desired count at or above the engine's quorum floor.
	// +kubebuilder:default=true
	// +optional
	RespectQuorum bool `json:"respectQuorum,omitempty"`

	// MaxReplicationLagSeconds is the lag threshold above which scaling is braked
	// (only while the primary is actively writing).
	// +kubebuilder:validation:Minimum=0
	// +optional
	MaxReplicationLagSeconds *int32 `json:"maxReplicationLagSeconds,omitempty"`
}

// DatabaseHorizontalAutoscalerSpec defines the desired state.
// +kubebuilder:validation:XValidation:rule="self.maxReplicas >= self.minReplicas",message="maxReplicas must be greater than or equal to minReplicas"
type DatabaseHorizontalAutoscalerSpec struct {
	// TargetRef is the managed database Application to scale.
	// +required
	TargetRef TargetRef `json:"targetRef"`

	// MinReplicas is the minimum TOTAL instance count (primary + standbys). Must be
	// >= 2 so at least one read-serving replica exists.
	// +kubebuilder:validation:Minimum=2
	// +required
	MinReplicas *int32 `json:"minReplicas"`

	// MaxReplicas is the maximum TOTAL instance count.
	// +kubebuilder:validation:Minimum=2
	// +required
	MaxReplicas *int32 `json:"maxReplicas"`

	// Metrics drive the scaling decision. When several are set the desired count is
	// the maximum of the per-metric desired counts (HPA semantics).
	// +kubebuilder:validation:MinItems=1
	// +required
	Metrics []MetricSpec `json:"metrics"`

	// Behavior tunes scaling dynamics.
	// +optional
	Behavior *Behavior `json:"behavior,omitempty"`

	// Constraints are the safety knobs.
	// +optional
	Constraints *Constraints `json:"constraints,omitempty"`

	// DryRun writes decisions to status/events without applying any patch.
	// +kubebuilder:default=false
	// +optional
	DryRun bool `json:"dryRun,omitempty"`
}

// MetricStatus is the last observed value of a driver metric.
type MetricStatus struct {
	// Type of the driver metric.
	Type MetricType `json:"type"`

	// AverageValue observed per read-serving replica.
	AverageValue resource.Quantity `json:"averageValue"`
}

// DatabaseHorizontalAutoscalerStatus defines the observed state.
type DatabaseHorizontalAutoscalerStatus struct {
	// CurrentReplicas is the total instance count last observed on the target.
	// +optional
	CurrentReplicas int32 `json:"currentReplicas"`

	// DesiredReplicas is the total instance count the autoscaler computed.
	// +optional
	DesiredReplicas int32 `json:"desiredReplicas"`

	// LastConvergedReplicas is the last count that reached availableReplicas == replicas.
	// +optional
	LastConvergedReplicas *int32 `json:"lastConvergedReplicas,omitempty"`

	// LastAppliedReplicas is the last replica count the autoscaler itself wrote.
	// Persisted so the ownership back-off (detecting a competing writer) survives
	// a controller restart or leader failover.
	// +optional
	LastAppliedReplicas *int32 `json:"lastAppliedReplicas,omitempty"`

	// LastScaleTime is when the autoscaler last patched the target.
	// +optional
	LastScaleTime *metav1.Time `json:"lastScaleTime,omitempty"`

	// CurrentMetrics is the last observed value of each driver metric.
	// +optional
	CurrentMetrics []MetricStatus `json:"currentMetrics,omitempty"`

	// ObservedGeneration is the .metadata.generation the status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=dha
// +kubebuilder:printcolumn:name="Target",type="string",JSONPath=".spec.targetRef.name"
// +kubebuilder:printcolumn:name="Min",type="integer",JSONPath=".spec.minReplicas"
// +kubebuilder:printcolumn:name="Max",type="integer",JSONPath=".spec.maxReplicas"
// +kubebuilder:printcolumn:name="Current",type="integer",JSONPath=".status.currentReplicas"
// +kubebuilder:printcolumn:name="Desired",type="integer",JSONPath=".status.desiredReplicas"
// +kubebuilder:printcolumn:name="DryRun",type="boolean",JSONPath=".spec.dryRun"

// DatabaseHorizontalAutoscaler is the Schema for the databasehorizontalautoscalers API.
type DatabaseHorizontalAutoscaler struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DatabaseHorizontalAutoscalerSpec   `json:"spec,omitempty"`
	Status DatabaseHorizontalAutoscalerStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// DatabaseHorizontalAutoscalerList contains a list of DatabaseHorizontalAutoscaler.
type DatabaseHorizontalAutoscalerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DatabaseHorizontalAutoscaler `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DatabaseHorizontalAutoscaler{}, &DatabaseHorizontalAutoscalerList{})
}
