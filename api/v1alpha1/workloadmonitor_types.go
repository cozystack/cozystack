package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WorkloadMonitorSpec defines the desired state of WorkloadMonitor
type WorkloadMonitorSpec struct {
	// Selector is a label selector to find workloads to monitor
	// +required
	Selector map[string]string `json:"selector"`

	// Kind specifies the kind of the workload
	// +optional
	Kind string `json:"kind,omitempty"`

	// Type specifies the type of the workload
	// +optional
	Type string `json:"type,omitempty"`

	// Version specifies the version of the workload
	// +optional
	Version string `json:"version,omitempty"`

	// MinReplicas specifies the minimum number of replicas that should be available
	// +kubebuilder:validation:Minimum=0
	// +optional
	MinReplicas *int32 `json:"minReplicas,omitempty"`

	// Replicas is the desired number of replicas
	// If not specified, will use observedReplicas as the target
	// +kubebuilder:validation:Minimum=0
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`
}

// WorkloadMonitorReasonDataVolumeNotReady is the status Reason of a monitor
// that is not operational because a DataVolume it selects is not ready.
const WorkloadMonitorReasonDataVolumeNotReady = "DataVolumeNotReady"

// WorkloadMonitorStatus defines the observed state of WorkloadMonitor
type WorkloadMonitorStatus struct {
	// Operational indicates if the workload meets all operational requirements
	// +optional
	Operational *bool `json:"operational,omitempty"`

	// AvailableReplicas is the number of ready replicas
	// +optional
	AvailableReplicas int32 `json:"availableReplicas"`

	// ObservedReplicas is the total number of pods observed
	// +optional
	ObservedReplicas int32 `json:"observedReplicas"`

	// Message names what keeps the workload from being operational, when the
	// controller can tell.
	// +optional
	Message string `json:"message,omitempty"`

	// Reason is a machine-readable cause for Message, such as
	// DataVolumeNotReady.
	// +optional
	Reason string `json:"reason,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Kind",type="string",JSONPath=".spec.kind"
// +kubebuilder:printcolumn:name="Type",type="string",JSONPath=".spec.type"
// +kubebuilder:printcolumn:name="Version",type="string",JSONPath=".spec.version"
// +kubebuilder:printcolumn:name="Replicas",type="integer",JSONPath=".spec.replicas"
// +kubebuilder:printcolumn:name="MinReplicas",type="integer",JSONPath=".spec.minReplicas"
// +kubebuilder:printcolumn:name="Available",type="integer",JSONPath=".status.availableReplicas"
// +kubebuilder:printcolumn:name="Observed",type="integer",JSONPath=".status.observedReplicas"
// +kubebuilder:printcolumn:name="Operational",type="boolean",JSONPath=".status.operational"

// WorkloadMonitor is the Schema for the workloadmonitors API
type WorkloadMonitor struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WorkloadMonitorSpec   `json:"spec,omitempty"`
	Status WorkloadMonitorStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// WorkloadMonitorList contains a list of WorkloadMonitor
type WorkloadMonitorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WorkloadMonitor `json:"items"`
}

func init() {
	SchemeBuilder.Register(&WorkloadMonitor{}, &WorkloadMonitorList{})
}

// GetSelector returns the label selector from metadata
func (w *WorkloadMonitor) GetSelector() map[string]string {
	return w.Spec.Selector
}

// Selector specifies the label selector for workloads
type Selector map[string]string
