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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ProjectionType selects what a TenantProjection entry publishes from its
// source Secret into a tenant-visible, key-free Secret.
// +kubebuilder:validation:Enum=CACert
type ProjectionType string

const (
	// ProjectionTypeCACert publishes a key-free copy of a CA certificate
	// (only ca.crt) so the tenant can trust the application's TLS
	// endpoints. It is the only supported projection type.
	ProjectionTypeCACert ProjectionType = "CACert"
)

// TenantProjectionEntry declares one artifact to project from a source
// Secret into a tenant-visible, key-free Secret.
type TenantProjectionEntry struct {
	// Type selects what to project. CACert is the only supported type.
	// +required
	Type ProjectionType `json:"type"`

	// SourceSecretName names the Secret in the TenantProjection's own
	// namespace holding the material to project. Only the extracted,
	// key-free certificate is ever published, so naming a Secret that
	// also carries private key material does not widen tenant visibility.
	// +required
	// +kubebuilder:validation:MinLength=1
	SourceSecretName string `json:"sourceSecretName"`

	// SourceKey is the key inside the source Secret holding the CA
	// certificate in PEM form. The certificate is always republished
	// under "ca.crt", whatever it is called at the source.
	// +kubebuilder:default=ca.crt
	// +optional
	SourceKey string `json:"sourceKey,omitempty"`
}

// TenantProjectionSpec declares the projections to publish for a tenant.
type TenantProjectionSpec struct {
	// Projections is the list of artifacts to publish from source Secrets
	// in this namespace.
	// +required
	// +kubebuilder:validation:MinItems=1
	Projections []TenantProjectionEntry `json:"projections"`
}

// TenantProjectionStatus reports the observed state of the projections.
type TenantProjectionStatus struct {
	// ObservedGeneration mirrors the .metadata.generation reflected in the
	// latest reconciled state.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions describes the current state of the TenantProjection.
	// Standard condition types: Ready. Ready is False with reason
	// SourceNotFound while a referenced source Secret does not exist.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=tproj
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// TenantProjection is the Schema for the tenantprojections API.
// It is a namespaced, platform-internal sentinel a chart renders to declare
// which of an application's Secrets should be published to the tenant as a
// key-free copy. The cozystack-controller reconciles the projected Secret
// from this object and owns it via an OwnerReference back to this sentinel.
type TenantProjection struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TenantProjectionSpec   `json:"spec,omitempty"`
	Status TenantProjectionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TenantProjectionList contains a list of TenantProjection.
type TenantProjectionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TenantProjection `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TenantProjection{}, &TenantProjectionList{})
}
