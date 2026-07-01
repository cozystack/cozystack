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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ServiceExposureSpec is the mechanism-agnostic tenant intent: "expose
// this Service externally via the named ExposureClass". It never names a
// backend; the controller translates it into whichever backend the cluster
// admin wired behind the referenced class.
type ServiceExposureSpec struct {
	// ServiceRef names the Service in this namespace to expose. The
	// controller scopes the backend pool/announcer to that Service and
	// reads its assigned IP back into status. The Service's own type is
	// owned by the chart that renders it, not by this controller.
	// +required
	ServiceRef corev1.LocalObjectReference `json:"serviceRef"`

	// ExposureClassName references a cluster-scoped ExposureClass the
	// admin configured. Empty selects the cluster default ExposureClass
	// (the one annotated exposureclass.network.cozystack.io/is-default-class=true).
	// +optional
	ExposureClassName string `json:"exposureClassName,omitempty"`
}

// ServiceExposureStatus reports the observed state of the exposure.
type ServiceExposureStatus struct {
	// ObservedGeneration mirrors the .metadata.generation reflected in the
	// latest reconciled state.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// ResolvedBackend records which backend the referenced class resolved
	// to, so operators can see where an exposure was routed.
	// +optional
	ResolvedBackend string `json:"resolvedBackend,omitempty"`

	// AssignedIPs reports the external addresses the backend assigned,
	// read back from the Service (status.loadBalancer.ingress or the
	// externalIPs pin).
	// +optional
	AssignedIPs []string `json:"assignedIPs,omitempty"`

	// Conditions describes the current state of the ServiceExposure.
	// Standard condition type: Ready.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=svcexp
// +kubebuilder:printcolumn:name="Service",type="string",JSONPath=".spec.serviceRef.name"
// +kubebuilder:printcolumn:name="Class",type="string",JSONPath=".spec.exposureClassName"
// +kubebuilder:printcolumn:name="Backend",type="string",JSONPath=".status.resolvedBackend"
// +kubebuilder:printcolumn:name="IPs",type="string",JSONPath=".status.assignedIPs"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// ServiceExposure is the Schema for the serviceexposures API. It expresses
// the mechanism-agnostic tenant intent to expose a Service externally; the
// cozystack-controller reconciles the backend-specific pool and announcer
// resources from this CR and reports the assigned IPs in status.
type ServiceExposure struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ServiceExposureSpec   `json:"spec,omitempty"`
	Status ServiceExposureStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ServiceExposureList contains a list of ServiceExposure.
type ServiceExposureList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ServiceExposure `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ServiceExposure{}, &ServiceExposureList{})
}
