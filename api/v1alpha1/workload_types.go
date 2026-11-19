/*
Copyright 2025.

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

// WorkloadStatus defines the observed state of Workload
type WorkloadStatus struct {
	// Kind represents the type of workload (redis, postgres, etc.)
	// +required
	Kind string `json:"kind"`

	// Type represents the specific role of the workload (redis, sentinel, etc.)
	// If not specified, defaults to Kind
	// +optional
	Type string `json:"type,omitempty"`

	// Resources specifies the compute resources allocated to this workload
	// +required
	Resources map[string]resource.Quantity `json:"resources"`

	// Operational indicates if all pods of the workload are ready
	// +optional
	Operational bool `json:"operational"`
}

// +kubebuilder:object:root=true
// +kubebuilder:printcolumn:name="Kind",type="string",JSONPath=".status.kind"
// +kubebuilder:printcolumn:name="Type",type="string",JSONPath=".status.type"
// +kubebuilder:printcolumn:name="CPU",type="string",JSONPath=".status.resources.cpu"
// +kubebuilder:printcolumn:name="Memory",type="string",JSONPath=".status.resources.memory"
// +kubebuilder:printcolumn:name="Operational",type="boolean",JSONPath=`.status.operational`

// Workload is the Schema for the workloads API
type Workload struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Status WorkloadStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// WorkloadList contains a list of Workload
type WorkloadList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Workload `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Workload{}, &WorkloadList{})
}
