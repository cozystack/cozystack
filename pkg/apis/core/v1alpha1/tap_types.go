// SPDX-License-Identifier: Apache-2.0
// Copyright 2025 The Cozystack Authors.

// Tap is a read-only, virtual resource that powers the dashboard marketplace
// view. Each Tap is computed on read from a PackageSource on the cluster plus
// the ApplicationDefinitions attributable to it, so the dashboard can list
// connected repositories and the packages they expose without walking those
// resources from the browser. Taps are cluster-scoped, mirroring PackageSource.
//
// A Tap carries catalog metadata only. It deliberately never exposes a pull
// Secret name or credentials, so browsing the catalog cannot leak a private
// tap's credentials across the tenant boundary.

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Tap represents one connected External-Apps repository (a PackageSource) and
// the packages it exposes.
type Tap struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec TapSpec `json:"spec,omitempty"`
}

// TapSpec is the computed state of a tap.
type TapSpec struct {
	// Source is where the tap's artifact comes from (no credentials).
	Source TapSource `json:"source,omitempty"`
	// Community is true for community-tapped sources (community.* names), false
	// for official platform sources.
	Community bool `json:"community,omitempty"`
	// Ready reflects the PackageSource's Ready condition.
	Ready bool `json:"ready,omitempty"`
	// Message is the PackageSource's Ready condition message, if any.
	Message string `json:"message,omitempty"`
	// Packages are the packages this tap exposes, derived from the
	// ApplicationDefinitions attributable to the PackageSource.
	Packages []TapPackage `json:"packages,omitempty"`

	// URL is a connect-time input only: the oci:// reference to tap. It is
	// consumed when a Tap is created and is never populated on read.
	URL string `json:"url,omitempty"`
	// Tag is a connect-time input only: the OCI tag to tap (defaults to latest).
	Tag string `json:"tag,omitempty"`
	// SecretRef is a connect-time input only: the name of a pull-credential
	// Secret in cozy-system for a private repository. It is consumed when a Tap
	// is created and, being a credential reference, is never returned on read.
	SecretRef string `json:"secretRef,omitempty"`
}

// TapSource identifies the Flux source backing a tap. It carries no credential.
type TapSource struct {
	// Kind is the Flux source kind (OCIRepository or GitRepository).
	Kind string `json:"kind,omitempty"`
	// Name is the Flux source name in cozy-system.
	Name string `json:"name,omitempty"`
}

// TapPackage is one package exposed by a tap.
type TapPackage struct {
	// Name is the ApplicationDefinition name.
	Name string `json:"name"`
	// Kind is the application kind users create (spec.application.kind).
	Kind string `json:"kind,omitempty"`
	// Component is the PackageSource component this package maps to.
	Component string `json:"component,omitempty"`
	// Description, Category, Tags, Icon are dashboard catalog metadata.
	Description string   `json:"description,omitempty"`
	Category    string   `json:"category,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Icon        string   `json:"icon,omitempty"`
	// Privileged is true when the backing component declares install.privileged.
	Privileged bool `json:"privileged,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// TapList is a list of Taps.
type TapList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Tap `json:"items"`
}
