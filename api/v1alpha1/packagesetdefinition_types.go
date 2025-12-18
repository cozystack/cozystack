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
// +kubebuilder:resource:scope=Cluster,shortName=psd
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Variants",type="string",JSONPath=".status.variants",description="Package variants (comma-separated)"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status",description="Ready status"
// +kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].message",description="Ready message"

// PackageSetDefinition is the Schema for the packagesetdefinitions API
type PackageSetDefinition struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PackageSetDefinitionSpec   `json:"spec,omitempty"`
	Status PackageSetDefinitionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PackageSetDefinitionList contains a list of PackageSetDefinitions
type PackageSetDefinitionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PackageSetDefinition `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PackageSetDefinition{}, &PackageSetDefinitionList{})
}

// PackageSetDefinitionSpec defines the desired state of PackageSetDefinition
type PackageSetDefinitionSpec struct {
	// SourceRef is the source reference for the package set charts
	// +optional
	SourceRef *PackageSetDefinitionSourceRef `json:"sourceRef,omitempty"`

	// Variants is a list of package set variants
	// Each variant defines packages, sources, dependencies, and libraries for a specific configuration
	// +optional
	Variants []PackageSetDefinitionVariant `json:"variants,omitempty"`
}

// PackageSetDefinitionVariant defines a single variant configuration
type PackageSetDefinitionVariant struct {
	// Name is the unique identifier for this variant
	// +required
	Name string `json:"name"`

	// DependsOn is a list of package set dependencies in the format "packageSetName/target"
	// For example: "cozystack-system/network"
	// If specified, the dependencies listed in the target's packages will be taken
	// from the specified package set and added to all packages in this variant
	// +optional
	DependsOn []string `json:"dependsOn,omitempty"`

	// Libraries is a list of Helm library charts used by packages in this variant
	// +optional
	Libraries []PackageSetDefinitionLibrary `json:"libraries,omitempty"`

	// Packages is a list of Helm releases to be installed as part of this variant
	// +optional
	Packages []PackageSetDefinitionPackage `json:"packages,omitempty"`

	// Sources is a list of application sources for this variant
	// +optional
	Sources []PackageSetDefinitionApplication `json:"sources,omitempty"`
}

// PackageSetDefinitionDependencyTarget defines a named group of packages that can be referenced
// by other package sets via dependsOn
type PackageSetDefinitionDependencyTarget struct {
	// Name is the unique identifier for this dependency target
	// +required
	Name string `json:"name"`

	// Packages is a list of package names that belong to this target
	// These packages will be added as dependencies when this target is referenced
	// +required
	Packages []string `json:"packages"`
}

// PackageSetDefinitionLibrary defines a Helm library chart
type PackageSetDefinitionLibrary struct {
	// Name is the optional name for library placed in charts
	// +optional
	Name string `json:"name,omitempty"`

	// Path is the path to the library chart directory
	// +required
	Path string `json:"path"`
}

// PackageSetDefinitionSourceRef defines the source reference for package set charts
type PackageSetDefinitionSourceRef struct {
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

	// BasePath is the base path where packages are located in the source.
	// For GitRepository, defaults to "packages" if not specified.
	// For OCIRepository, defaults to empty string (root) if not specified.
	// +optional
	Path string `json:"path,omitempty"`
}

// PackageSetDefinitionPackage defines a single Helm release within a package set
type PackageSetDefinitionPackage struct {
	// Name is the unique identifier for this package within the package set
	// +required
	Name string `json:"name"`

	// ReleaseName is the name of the HelmRelease resource that will be created
	// If not specified, defaults to the Name field
	// +optional
	ReleaseName string `json:"releaseName,omitempty"`

	// Path is the path to the Helm chart directory
	// +required
	Path string `json:"path"`

	// Namespace is the Kubernetes namespace where the release will be installed
	// +required
	Namespace string `json:"namespace"`

	// Privileged indicates whether this release requires privileged access
	// +optional
	Privileged bool `json:"privileged,omitempty"`

	// Disabled indicates whether this release is disabled (should not be installed)
	// +optional
	Disabled bool `json:"disabled,omitempty"`

	// DependsOn is a list of release names that must be installed before this release
	// +optional
	DependsOn []string `json:"dependsOn,omitempty"`

	// Libraries is a list of library charts that this package depends on
	// +optional
	Libraries []PackageSetDefinitionLibrary `json:"libraries,omitempty"`

	// ValuesFiles is a list of values file names to use
	// +optional
	ValuesFiles []string `json:"valuesFiles,omitempty"`
}

// PackageSetDefinitionApplication defines a single application release within a package set
type PackageSetDefinitionApplication struct {
	// Name is the unique identifier for this application within the package set
	// +required
	Name string `json:"name"`

	// Path is the path to the Helm chart directory
	// +required
	Path string `json:"path"`

	// Libraries is a list of library names that this application depends on
	// These libraries must be defined at the variant level
	// +optional
	Libraries []string `json:"libraries,omitempty"`
}

// PackageSetDefinitionStatus defines the observed state of PackageSetDefinition
type PackageSetDefinitionStatus struct {
	// Variants is a comma-separated list of package variant names
	// This field is populated by the controller based on spec.packages keys
	// +optional
	Variants string `json:"variants,omitempty"`

	// Conditions represents the latest available observations of a PackageSetDefinition's state
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
