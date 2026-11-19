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
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName={pkg,pkgs}
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Variant",type="string",JSONPath=".spec.variant",description="Selected variant"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status",description="Ready status"
// +kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].message",description="Ready message"

// Package is the Schema for the packages API
type Package struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PackageSpec   `json:"spec,omitempty"`
	Status PackageStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PackageList contains a list of Packages
type PackageList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Package `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Package{}, &PackageList{})
}

// PackageSpec defines the desired state of Package
type PackageSpec struct {
	// Variant is the name of the variant to use from the PackageSource
	// If not specified, defaults to "default"
	// +optional
	Variant string `json:"variant,omitempty"`

	// IgnoreDependencies is a list of package source dependencies to ignore
	// Dependencies listed here will not be installed even if they are specified in the PackageSource
	// +optional
	IgnoreDependencies []string `json:"ignoreDependencies,omitempty"`

	// Components is a map of release name to component overrides
	// Allows overriding values and enabling/disabling specific components from the PackageSource
	// +optional
	Components map[string]PackageComponent `json:"components,omitempty"`
}

// PackageComponent defines overrides for a specific component
type PackageComponent struct {
	// Enabled indicates whether this component should be installed
	// If false, the component will be disabled even if it's defined in the PackageSource
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// Values contains Helm chart values as a JSON object
	// These values will be merged with the default values from the PackageSource
	// +optional
	Values *apiextensionsv1.JSON `json:"values,omitempty"`
}

// PackageStatus defines the observed state of Package
type PackageStatus struct {
	// Conditions represents the latest available observations of a Package's state
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Dependencies tracks the readiness status of each dependency
	// Key is the dependency package name, value indicates if the dependency is ready
	// +optional
	Dependencies map[string]DependencyStatus `json:"dependencies,omitempty"`
}

// DependencyStatus represents the readiness status of a dependency
type DependencyStatus struct {
	// Ready indicates whether the dependency is ready
	Ready bool `json:"ready"`
}
