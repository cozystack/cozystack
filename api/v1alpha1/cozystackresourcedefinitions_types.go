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

// CozystackResourceDefinition is the Schema for the cozystackresourcedefinitions API
type CozystackResourceDefinition struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec CozystackResourceDefinitionSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// CozystackResourceDefinitionList contains a list of CozystackResourceDefinitions
type CozystackResourceDefinitionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CozystackResourceDefinition `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CozystackResourceDefinition{}, &CozystackResourceDefinitionList{})
}

type CozystackResourceDefinitionSpec struct {
	// Application configuration
	Application CozystackResourceDefinitionApplication `json:"application"`
	// Release configuration
	Release CozystackResourceDefinitionRelease `json:"release"`
	// Secret selectors
	Secrets CozystackResourceDefinitionSecrets `json:"secrets,omitempty"`
}

type CozystackResourceDefinitionChart struct {
	// Name of the Helm chart
	Name string `json:"name"`
	// Source reference for the Helm chart
	SourceRef SourceRef `json:"sourceRef"`
}

type SourceRef struct {
	// Kind of the source reference
	// +kubebuilder:default:="HelmRepository"
	Kind string `json:"kind"`
	// Name of the source reference
	Name string `json:"name"`
	// Namespace of the source reference
	// +kubebuilder:default:="cozy-public"
	Namespace string `json:"namespace"`
}

type CozystackResourceDefinitionApplication struct {
	// Kind of the application, used for UI and API
	Kind string `json:"kind"`
	// OpenAPI schema for the application, used for API validation
	OpenAPISchema string `json:"openAPISchema"`
	// Plural name of the application, used for UI and API
	Plural string `json:"plural"`
	// Singular name of the application, used for UI and API
	Singular string `json:"singular"`
}

type CozystackResourceDefinitionRelease struct {
	// Helm chart configuration
	Chart CozystackResourceDefinitionChart `json:"chart"`
	// Labels for the release
	Labels map[string]string `json:"labels,omitempty"`
	// Prefix for the release name
	Prefix string `json:"prefix"`
}

type CozystackResourceDefinitionSecrets struct {
	// Exclude contains an array of label selectors that target secrets.
	// If a secret matches the selector in any of the elements in the array, it is
	// hidden from the user, regardless of the matches in the include array.
	Exclude []*metav1.LabelSelector `json:"exclude,omitempty"`
	// Include contains an array of label selectors that target secrets.
	// If a secret matches the selector in any of the elements in the array, and
	// matches none of the selectors in the exclude array that secret is marked
	// as a tenant secret and is visible to users.
	Include []*metav1.LabelSelector `json:"include,omitempty"`
}
