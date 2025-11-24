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

	// Artifacts is a list of Helm charts that will be built as ExternalArtifacts
	// These artifacts can be referenced by CozystackResourceDefinitions
	// +optional
	Artifacts []BundleArtifact `json:"artifacts,omitempty"`

	// Packages is a list of Helm releases to be installed as part of this bundle
	// +required
	Packages []BundleRelease `json:"packages"`

	// DeletionPolicy defines how child resources should be handled when the bundle is deleted.
	// - "Delete" (default): Child resources will be deleted when the bundle is deleted (via ownerReference).
	// - "Orphan": Child resources will be orphaned (ownerReferences will be removed).
	// +kubebuilder:validation:Enum=Delete;Orphan
	// +kubebuilder:default=Delete
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`

	// Labels are labels that will be applied to all resources created by this bundle
	// (ArtifactGenerators and HelmReleases). These labels are merged with the default
	// cozystack.io/bundle label.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// BasePath is the base path where packages are located in the source.
	// For GitRepository, defaults to "packages" if not specified.
	// For OCIRepository, defaults to empty string (root) if not specified.
	// +optional
	BasePath string `json:"basePath,omitempty"`
}

// DeletionPolicy defines how child resources should be handled when the parent is deleted.
// +kubebuilder:validation:Enum=Delete;Orphan
type DeletionPolicy string

const (
	// DeletionPolicyDelete means child resources will be deleted when the parent is deleted.
	DeletionPolicyDelete DeletionPolicy = "Delete"
	// DeletionPolicyOrphan means child resources will be orphaned (ownerReferences removed).
	DeletionPolicyOrphan DeletionPolicy = "Orphan"
)

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

// BundleArtifact defines a Helm chart artifact that will be built as ExternalArtifact
type BundleArtifact struct {
	// Name is the unique identifier for this artifact (used as ExternalArtifact name)
	// +required
	Name string `json:"name"`

	// Path is the path to the Helm chart directory
	// +required
	Path string `json:"path"`

	// Libraries is a list of library names that this artifact depends on
	// +optional
	Libraries []string `json:"libraries,omitempty"`
}

// BundleSourceRef defines the source reference for bundle charts
type BundleSourceRef struct {
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
}

// +kubebuilder:validation:XValidation:rule="(has(self.path) && !has(self.artifact)) || (!has(self.path) && has(self.artifact))",message="either path or artifact must be set, but not both"
// BundleRelease defines a single Helm release within a bundle
type BundleRelease struct {
	// Name is the unique identifier for this release within the bundle
	// +required
	Name string `json:"name"`

	// ReleaseName is the name of the HelmRelease resource that will be created
	// +required
	ReleaseName string `json:"releaseName"`

	// Path is the path to the Helm chart directory
	// Either Path or Artifact must be specified, but not both
	// +optional
	Path string `json:"path,omitempty"`

	// Artifact is the name of an artifact from the bundle's artifacts list
	// The artifact must exist in the bundle's artifacts section
	// Either Path or Artifact must be specified, but not both
	// +optional
	Artifact string `json:"artifact,omitempty"`

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

	// Labels are labels that will be applied to the HelmRelease created for this package
	// These labels are merged with bundle-level labels and the default cozystack.io/bundle label
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// NamespaceLabels are labels that will be applied to the namespace for this package
	// These labels are merged with labels from other packages in the same namespace
	// +optional
	NamespaceLabels map[string]string `json:"namespaceLabels,omitempty"`
}
