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
// +kubebuilder:resource:scope=Cluster

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
	Secrets CozystackResourceDefinitionResources `json:"secrets,omitempty"`
	// Service selectors
	Services CozystackResourceDefinitionResources `json:"services,omitempty"`
	// Ingress selectors
	Ingresses CozystackResourceDefinitionResources `json:"ingresses,omitempty"`

	// Dashboard configuration for this resource
	Dashboard *CozystackResourceDefinitionDashboard `json:"dashboard,omitempty"`
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

// CozystackResourceDefinitionResourceSelector extends metav1.LabelSelector with resourceNames support.
// A resource matches this selector only if it satisfies ALL criteria:
// - Label selector conditions (matchExpressions and matchLabels)
// - AND has a name that matches one of the names in resourceNames (if specified)
//
// The resourceNames field supports Go templates with the following variables available:
// - {{ .name }}: The name of the managing application (from apps.cozystack.io/application.name)
// - {{ .kind }}: The lowercased kind of the managing application (from apps.cozystack.io/application.kind)
// - {{ .namespace }}: The namespace of the resource being processed
//
// Example YAML:
//   secrets:
//     include:
//     - matchExpressions:
//       - key: badlabel
//         operator: DoesNotExist
//       matchLabels:
//         goodlabel: goodvalue
//       resourceNames:
//       - "{{ .name }}-secret"
//       - "{{ .kind }}-{{ .name }}-tls"
//       - "specificname"
type CozystackResourceDefinitionResourceSelector struct {
	metav1.LabelSelector `json:",inline"`
	// ResourceNames is a list of resource names to match
	// If specified, the resource must have one of these exact names to match the selector
	// +optional
	ResourceNames []string `json:"resourceNames,omitempty"`
}

type CozystackResourceDefinitionResources struct {
	// Exclude contains an array of resource selectors that target resources.
	// If a resource matches the selector in any of the elements in the array, it is
	// hidden from the user, regardless of the matches in the include array.
	Exclude []*CozystackResourceDefinitionResourceSelector `json:"exclude,omitempty"`
	// Include contains an array of resource selectors that target resources.
	// If a resource matches the selector in any of the elements in the array, and
	// matches none of the selectors in the exclude array that resource is marked
	// as a tenant resource and is visible to users.
	Include []*CozystackResourceDefinitionResourceSelector `json:"include,omitempty"`
}

// ---- Dashboard types ----

// DashboardTab enumerates allowed UI tabs.
// +kubebuilder:validation:Enum=workloads;ingresses;services;secrets;yaml
type DashboardTab string

const (
	DashboardTabWorkloads DashboardTab = "workloads"
	DashboardTabIngresses DashboardTab = "ingresses"
	DashboardTabServices  DashboardTab = "services"
	DashboardTabSecrets   DashboardTab = "secrets"
	DashboardTabYAML      DashboardTab = "yaml"
)

// CozystackResourceDefinitionDashboard describes how this resource appears in the UI.
type CozystackResourceDefinitionDashboard struct {
	// Human-readable name shown in the UI (e.g., "Bucket")
	Singular string `json:"singular"`
	// Plural human-readable name (e.g., "Buckets")
	Plural string `json:"plural"`
	// Hard-coded name used in the UI (e.g., "bucket")
	// +optional
	Name string `json:"name,omitempty"`
	// Whether this resource is singular (not a collection) in the UI
	// +optional
	SingularResource bool `json:"singularResource,omitempty"`
	// Order weight for sorting resources in the UI (lower first)
	// +optional
	Weight int `json:"weight,omitempty"`
	// Short description shown in catalogs or headers (e.g., "S3 compatible storage")
	// +optional
	Description string `json:"description,omitempty"`
	// Icon encoded as a string (e.g., inline SVG, base64, or data URI)
	// +optional
	Icon string `json:"icon,omitempty"`
	// Category used to group resources in the UI (e.g., "Storage", "Networking")
	Category string `json:"category"`
	// Free-form tags for search and filtering
	// +optional
	Tags []string `json:"tags,omitempty"`
	// Which tabs to show for this resource
	// +optional
	Tabs []DashboardTab `json:"tabs,omitempty"`
	// Order of keys in the YAML view
	// +optional
	KeysOrder [][]string `json:"keysOrder,omitempty"`
	// Whether this resource is a module (tenant module)
	// +optional
	Module bool `json:"module,omitempty"`
}
