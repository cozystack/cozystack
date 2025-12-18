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
// +kubebuilder:resource:scope=Cluster,shortName=ps
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Variant",type="string",JSONPath=".status.variant",description="Selected variant"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status",description="Ready status"
// +kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].message",description="Ready message"

// PackageSet is the Schema for the packagesets API
type PackageSet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PackageSetSpec   `json:"spec,omitempty"`
	Status PackageSetStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PackageSetList contains a list of PackageSets
type PackageSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PackageSet `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PackageSet{}, &PackageSetList{})
}

// PackageSetSpec defines the desired state of PackageSet
type PackageSetSpec struct {
	// IgnoreDependencies is a list of package set dependencies to ignore
	// Dependencies listed here will not be installed even if they are specified in the PackageSetDefinition
	// +optional
	IgnoreDependencies []string `json:"ignoreDependencies,omitempty"`

	// Packages is a map of package name to package overrides
	// Allows overriding values and enabling/disabling specific packages from the PackageSetDefinition
	// +optional
	Packages map[string]PackageSetPackageOverride `json:"packages,omitempty"`
}

// PackageSetPackageOverride defines overrides for a specific package
type PackageSetPackageOverride struct {
	// Enabled indicates whether this package should be installed
	// If false, the package will be disabled even if it's defined in the PackageSetDefinition
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// Values contains Helm chart values as a JSON object
	// These values will be merged with the default values from the PackageSetDefinition
	// +optional
	Values *apiextensionsv1.JSON `json:"values,omitempty"`
}

// PackageSetStatus defines the observed state of PackageSet
type PackageSetStatus struct {
	// Variant is the selected variant name, or "default" if not specified
	// This field is populated by the controller based on spec.packageSetDefinitionRef.variant
	// +optional
	Variant string `json:"variant,omitempty"`

	// Conditions represents the latest available observations of a PackageSet's state
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
