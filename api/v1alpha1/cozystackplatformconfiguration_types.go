/*
Copyright 2025 The Cozystack Authors.

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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster

// CozystackPlatformConfiguration is the Schema for the cozystackplatformconfigurations API
type CozystackPlatformConfiguration struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec CozystackPlatformConfigurationSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// CozystackPlatformConfigurationList contains a list of CozystackPlatformConfigurations
type CozystackPlatformConfigurationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CozystackPlatformConfiguration `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CozystackPlatformConfiguration{}, &CozystackPlatformConfigurationList{})
}

// CozystackPlatformConfigurationSpec defines the desired state of CozystackPlatformConfiguration
type CozystackPlatformConfigurationSpec struct {
	// Source configuration for GitRepository
	// +required
	Source CozystackPlatformConfigurationSource `json:"source"`

	// Chart configuration for HelmRelease
	// +required
	Chart CozystackPlatformConfigurationChart `json:"chart"`

	// Values to pass to HelmRelease
	// +optional
	Values *apiextensionsv1.JSON `json:"values,omitempty"`
}

// CozystackPlatformConfigurationSource defines the source configuration for GitRepository
// Reuses Flux GitRepositorySpec fields
type CozystackPlatformConfigurationSource struct {
	// URL of the Git repository
	// +required
	URL string `json:"url"`

	// Git repository reference (branch, tag, semver, commit)
	// +optional
	Ref *sourcev1.GitRepositoryRef `json:"ref,omitempty"`

	// Interval at which to check for updates
	// +kubebuilder:default:="1m0s"
	Interval metav1.Duration `json:"interval,omitempty"`

	// Timeout for Git operations
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`

	// Secret reference containing credentials
	// +optional
	SecretRef *corev1.LocalObjectReference `json:"secretRef,omitempty"`

	// Ignore overrides the set of patterns for ignoring files and folders
	// +optional
	Ignore *string `json:"ignore,omitempty"`

	// Include specifies a list of Git sub-paths to include
	// +optional
	Include []sourcev1.GitRepositoryInclude `json:"include,omitempty"`

	// RecurseSubmodules enables the initialization of all submodules
	// +optional
	RecurseSubmodules bool `json:"recurseSubmodules,omitempty"`

	// Verification specifies the Git commit signature verification configuration
	// +optional
	Verification *sourcev1.GitRepositoryVerification `json:"verification,omitempty"`
}

// CozystackPlatformConfigurationChart defines the chart configuration for HelmRelease
type CozystackPlatformConfigurationChart struct {
	// Path to the Helm chart directory in the Git repository
	// +required
	Path string `json:"path"`

	// Interval at which to reconcile the HelmRelease
	// +kubebuilder:default:="5m"
	Interval metav1.Duration `json:"interval,omitempty"`
}

