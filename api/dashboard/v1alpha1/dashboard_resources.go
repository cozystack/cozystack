// SPDX-License-Identifier: Apache-2.0
// Package v1alpha1 defines front.in-cloud.io API types.
//
// Group: dashboard.cozystack.io
// Version: v1alpha1
package v1alpha1

import (
	v1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// -----------------------------------------------------------------------------
// Shared shapes
// -----------------------------------------------------------------------------

// CommonStatus is a generic Status block with Kubernetes conditions.
type CommonStatus struct {
	// ObservedGeneration reflects the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of an object's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ArbitrarySpec holds schemaless user data and preserves unknown fields.
// We map the entire .spec to a single JSON payload to mirror the CRDs you provided.
// NOTE: Using apiextensionsv1.JSON avoids losing arbitrary structure during round-trips.
type ArbitrarySpec struct {
	// +kubebuilder:validation:XPreserveUnknownFields
	// +kubebuilder:pruning:PreserveUnknownFields
	v1.JSON `json:",inline"`
}

// -----------------------------------------------------------------------------
// Sidebar
// -----------------------------------------------------------------------------

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=sidebars,scope=Cluster
// +kubebuilder:subresource:status
type Sidebar struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ArbitrarySpec `json:"spec"`
	Status CommonStatus  `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type SidebarList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Sidebar `json:"items"`
}

// -----------------------------------------------------------------------------
// CustomFormsPrefill (shortName: cfp)
// -----------------------------------------------------------------------------

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=customformsprefills,scope=Cluster,shortName=cfp
// +kubebuilder:subresource:status
type CustomFormsPrefill struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ArbitrarySpec `json:"spec"`
	Status CommonStatus  `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type CustomFormsPrefillList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CustomFormsPrefill `json:"items"`
}

// -----------------------------------------------------------------------------
// BreadcrumbInside
// -----------------------------------------------------------------------------

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=breadcrumbsinside,scope=Cluster
// +kubebuilder:subresource:status
type BreadcrumbInside struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ArbitrarySpec `json:"spec"`
	Status CommonStatus  `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type BreadcrumbInsideList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BreadcrumbInside `json:"items"`
}

// -----------------------------------------------------------------------------
// CustomFormsOverride (shortName: cfo)
// -----------------------------------------------------------------------------

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=customformsoverrides,scope=Cluster,shortName=cfo
// +kubebuilder:subresource:status
type CustomFormsOverride struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ArbitrarySpec `json:"spec"`
	Status CommonStatus  `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type CustomFormsOverrideList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CustomFormsOverride `json:"items"`
}

// -----------------------------------------------------------------------------
// TableUriMapping
// -----------------------------------------------------------------------------

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=tableurimappings,scope=Cluster
// +kubebuilder:subresource:status
type TableUriMapping struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ArbitrarySpec `json:"spec"`
	Status CommonStatus  `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type TableUriMappingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TableUriMapping `json:"items"`
}

// -----------------------------------------------------------------------------
// Breadcrumb
// -----------------------------------------------------------------------------

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=breadcrumbs,scope=Cluster
// +kubebuilder:subresource:status
type Breadcrumb struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ArbitrarySpec `json:"spec"`
	Status CommonStatus  `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type BreadcrumbList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Breadcrumb `json:"items"`
}

// -----------------------------------------------------------------------------
// MarketplacePanel
// -----------------------------------------------------------------------------

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=marketplacepanels,scope=Cluster
// +kubebuilder:subresource:status
type MarketplacePanel struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ArbitrarySpec `json:"spec"`
	Status CommonStatus  `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type MarketplacePanelList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MarketplacePanel `json:"items"`
}

// -----------------------------------------------------------------------------
// Navigation
// -----------------------------------------------------------------------------

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=navigations,scope=Cluster
// +kubebuilder:subresource:status
type Navigation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ArbitrarySpec `json:"spec"`
	Status CommonStatus  `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type NavigationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Navigation `json:"items"`
}

// -----------------------------------------------------------------------------
// CustomColumnsOverride
// -----------------------------------------------------------------------------

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=customcolumnsoverrides,scope=Cluster
// +kubebuilder:subresource:status
type CustomColumnsOverride struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ArbitrarySpec `json:"spec"`
	Status CommonStatus  `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type CustomColumnsOverrideList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CustomColumnsOverride `json:"items"`
}

// -----------------------------------------------------------------------------
// Factory
// -----------------------------------------------------------------------------

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=factories,scope=Cluster
// +kubebuilder:subresource:status
type Factory struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ArbitrarySpec `json:"spec"`
	Status CommonStatus  `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type FactoryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Factory `json:"items"`
}
