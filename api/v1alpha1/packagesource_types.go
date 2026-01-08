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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName={pks}
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Variants",type="string",JSONPath=".status.variants",description="Package variants (comma-separated)"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status",description="Ready status"
// +kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].message",description="Ready message"

// PackageSource is the Schema for the packagesources API
type PackageSource struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PackageSourceSpec   `json:"spec,omitempty"`
	Status PackageSourceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PackageSourceList contains a list of PackageSources
type PackageSourceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PackageSource `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PackageSource{}, &PackageSourceList{})
}

// PackageSourceSpec defines the desired state of PackageSource
type PackageSourceSpec struct {
	// SourceRef is the source reference for the package source charts
	// +optional
	SourceRef *PackageSourceRef `json:"sourceRef,omitempty"`

	// Variants is a list of package source variants
	// Each variant defines components, applications, dependencies, and libraries for a specific configuration
	// +optional
	Variants []Variant `json:"variants,omitempty"`
}

// Variant defines a single variant configuration
type Variant struct {
	// Name is the unique identifier for this variant
	// +required
	Name string `json:"name"`

	// DependsOn is a list of package source dependencies
	// For example: "cozystack.networking"
	// +optional
	DependsOn []string `json:"dependsOn,omitempty"`

	// Libraries is a list of Helm library charts used by components in this variant
	// +optional
	Libraries []Library `json:"libraries,omitempty"`

	// Components is a list of Helm releases to be installed as part of this variant
	// +optional
	Components []Component `json:"components,omitempty"`
}

// Library defines a Helm library chart
type Library struct {
	// Name is the optional name for library placed in charts
	// +optional
	Name string `json:"name,omitempty"`

	// Path is the path to the library chart directory
	// +required
	Path string `json:"path"`
}

// PackageSourceRef defines the source reference for package source charts
type PackageSourceRef struct {
	// Kind of the source reference
	// +kubebuilder:validation:Enum=GitRepository;OCIRepository
	// +required
	Kind string `json:"kind"`

	// Name of the source reference
	// +required
	Name string `json:"name"`

	// Namespace of the source reference
	// +required
	Namespace string `json:"namespace"`

	// Path is the base path where packages are located in the source.
	// For GitRepository, defaults to "packages" if not specified.
	// For OCIRepository, defaults to empty string (root) if not specified.
	// +optional
	Path string `json:"path,omitempty"`
}

// ComponentInstall defines installation parameters for a component
type ComponentInstall struct {
	// ReleaseName is the name of the HelmRelease resource that will be created
	// If not specified, defaults to the component Name field
	// +optional
	ReleaseName string `json:"releaseName,omitempty"`

	// Namespace is the Kubernetes namespace where the release will be installed
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Privileged indicates whether this release requires privileged access
	// +optional
	Privileged bool `json:"privileged,omitempty"`

	// DependsOn is a list of component names that must be installed before this component
	// +optional
	DependsOn []string `json:"dependsOn,omitempty"`
}

// Component defines a single Helm release component within a package source
type Component struct {
	// Name is the unique identifier for this component within the package source
	// +required
	Name string `json:"name"`

	// Path is the path to the Helm chart directory
	// +required
	Path string `json:"path"`

	// Install defines installation parameters for this component
	// +optional
	Install *ComponentInstall `json:"install,omitempty"`

	// Libraries is a list of library names that this component depends on
	// These libraries must be defined at the variant level
	// +optional
	Libraries []string `json:"libraries,omitempty"`

	// ValuesFiles is a list of values file names to use
	// +optional
	ValuesFiles []string `json:"valuesFiles,omitempty"`
}

// PackageSourceStatus defines the observed state of PackageSource
type PackageSourceStatus struct {
	// Variants is a comma-separated list of package variant names
	// This field is populated by the controller based on spec.variants keys
	// +optional
	Variants string `json:"variants,omitempty"`

	// Conditions represents the latest available observations of a PackageSource's state
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
