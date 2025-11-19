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
// +kubebuilder:resource:scope=Cluster

// CozystackBundle is the Schema for the cozystackbundles API
type CozystackBundle struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec CozystackBundleSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// CozystackBundleList contains a list of CozystackBundles
type CozystackBundleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CozystackBundle `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CozystackBundle{}, &CozystackBundleList{})
}

// CozystackBundleSpec defines the desired state of CozystackBundle
type CozystackBundleSpec struct {
	// SourceRef is the source reference for the bundle charts
	// +required
	SourceRef BundleSourceRef `json:"sourceRef"`

	// DependsOn is a list of bundle dependencies in the format "bundleName/target"
	// For example: "cozystack-system/network"
	// If specified, the dependencies listed in the target's packages will be taken
	// from the specified bundle and added to all packages in this bundle
	// +optional
	DependsOn []string `json:"dependsOn,omitempty"`

	// DependencyTargets defines named groups of packages that can be referenced
	// by other bundles via dependsOn. Each target has a name and a list of packages.
	// +optional
	DependencyTargets []BundleDependencyTarget `json:"dependencyTargets,omitempty"`

	// Libraries is a list of Helm library charts used by packages
	// +optional
	Libraries []BundleLibrary `json:"libraries,omitempty"`

	// Packages is a list of Helm releases to be installed as part of this bundle
	// +required
	Packages []BundleRelease `json:"packages"`
}

// BundleDependencyTarget defines a named group of packages that can be referenced
// by other bundles via dependsOn
type BundleDependencyTarget struct {
	// Name is the unique identifier for this dependency target
	// +required
	Name string `json:"name"`

	// Packages is a list of package names that belong to this target
	// These packages will be added as dependencies when this target is referenced
	// +required
	Packages []string `json:"packages"`
}

// BundleLibrary defines a Helm library chart
type BundleLibrary struct {
	// Name is the unique identifier for this library
	// +required
	Name string `json:"name"`

	// Path is the path to the library chart directory
	// +required
	Path string `json:"path"`
}

// BundleSourceRef defines the source reference for bundle charts
type BundleSourceRef struct {
	// Kind of the source reference
	// +kubebuilder:validation:Enum=GitRepository
	// +required
	Kind string `json:"kind"`

	// Name of the source reference
	// +required
	Name string `json:"name"`

	// Namespace of the source reference
	// +required
	Namespace string `json:"namespace"`
}

// BundleRelease defines a single Helm release within a bundle
type BundleRelease struct {
	// Name is the unique identifier for this release within the bundle
	// +required
	Name string `json:"name"`

	// ReleaseName is the name of the HelmRelease resource that will be created
	// +required
	ReleaseName string `json:"releaseName"`

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

	// Libraries is a list of library names that this package depends on
	// +optional
	Libraries []string `json:"libraries,omitempty"`

	// Values contains Helm chart values as a JSON object
	// +optional
	Values *apiextensionsv1.JSON `json:"values,omitempty"`

	// ValuesFiles is a list of values file names to use
	// +optional
	ValuesFiles []string `json:"valuesFiles,omitempty"`
}
