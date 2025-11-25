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
// +kubebuilder:resource:scope=Cluster,shortName=platform

// Platform is the Schema for the platforms API
type Platform struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec PlatformSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// PlatformList contains a list of Platform
type PlatformList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Platform `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Platform{}, &PlatformList{})
}

// PlatformSpec defines the desired state of Platform
type PlatformSpec struct {
	// SourceRef is the source reference for the platform chart
	// This is used to generate the ArtifactGenerator
	// +required
	SourceRef SourceRef `json:"sourceRef"`

	// Values contains Helm chart values as a JSON object
	// These values are passed directly to HelmRelease.values
	// +optional
	Values *apiextensionsv1.JSON `json:"values,omitempty"`

	// Interval is the interval at which to reconcile the HelmRelease
	// +kubebuilder:default="5m"
	// +optional
	Interval *metav1.Duration `json:"interval,omitempty"`

	// BasePath is the base path where the platform chart is located in the source.
	// For GitRepository, defaults to "packages/core/platform" if not specified.
	// For OCIRepository, defaults to "core/platform" if not specified.
	// +optional
	BasePath string `json:"basePath,omitempty"`
}

