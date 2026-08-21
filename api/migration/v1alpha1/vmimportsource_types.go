// SPDX-License-Identifier: Apache-2.0
// Package v1alpha1 defines migration.cozystack.io API types.
//
// Group: migration.cozystack.io
// Version: v1alpha1
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(GroupVersion,
			&VMImportSource{},
			&VMImportSourceList{},
		)
		return nil
	})
}

const (
	// ManagedByLabel marks the objects the migration controller owns, so a
	// projected Secret can never overwrite one the controller did not create.
	ManagedByLabel = "apps.cozystack.io/managed-by"
	// ManagedByValue is the value ManagedByLabel carries on our objects.
	ManagedByValue = "cozystack-migration"

	// SourceReadyCondition reports whether the provider endpoint answered and
	// everything the platform must supply for this provider type is in place.
	SourceReadyCondition = "Ready"
)

// Condition reasons carried on a VMImportSource.
const (
	// ReasonConnected means the provider endpoint answered an authenticated call.
	ReasonConnected = "Connected"
	// ReasonConnectionFailed means the endpoint was unreachable or rejected the credentials.
	ReasonConnectionFailed = "ConnectionFailed"
	// ReasonCredentialsMissing means the credentials Secret could not be projected.
	ReasonCredentialsMissing = "CredentialsMissing"
	// ReasonVDDKNotConfigured means a vSphere source was created on a cluster
	// whose administrator has not supplied a VDDK image. The VMware path is
	// unavailable rather than broken: see the platform's migration settings.
	ReasonVDDKNotConfigured = "VDDKNotConfigured"
)

// ProviderType names a source virtualization platform.
// +kubebuilder:validation:Enum=vsphere
type ProviderType string

const (
	// ProviderVSphere is VMware vSphere, reached through its vCenter SDK endpoint.
	ProviderVSphere ProviderType = "vsphere"
)

// ProviderCredentials holds the account the platform uses to read the source
// inventory and the disk data. The controller materializes these into a Secret;
// a tenant never creates a Kubernetes Secret itself.
type ProviderCredentials struct {
	// Username is the account used to authenticate against the provider
	// (e.g. `migration@vsphere.local`).
	// +kubebuilder:validation:MinLength=1
	Username string `json:"username"`

	// Password is the account's password.
	// +kubebuilder:validation:MinLength=1
	Password string `json:"password"`

	// Thumbprint is the SHA-1 thumbprint of the endpoint's TLS certificate.
	// Required for vSphere unless insecureSkipVerify is set.
	// +optional
	Thumbprint string `json:"thumbprint,omitempty"`

	// InsecureSkipVerify disables verification of the endpoint's TLS
	// certificate. Intended for lab environments; prefer a thumbprint.
	// +optional
	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty"`
}

// VMImportSourceSpec describes a connection to a source virtualization platform.
type VMImportSourceSpec struct {
	// Type is the source platform this connection speaks to.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="type is immutable"
	Type ProviderType `json:"type"`

	// URL is the provider's API endpoint (e.g. `https://vcenter.example.com/sdk`).
	// +kubebuilder:validation:MinLength=1
	URL string `json:"url"`

	// Credentials is the account used to reach the provider.
	Credentials ProviderCredentials `json:"credentials"`
}

// VMImportSourceStatus reports whether the connection is usable.
type VMImportSourceStatus struct {
	// ObservedGeneration is the .metadata.generation the conditions were computed from.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represents the latest available observations of the connection's state.
	// The Ready condition is the connection's readiness: it is true only once the
	// endpoint has answered and every platform prerequisite for this provider
	// type is satisfied.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=vmis;vmimportsrc
// +kubebuilder:printcolumn:name="Type",type="string",JSONPath=".spec.type",priority=0
// +kubebuilder:printcolumn:name="URL",type="string",JSONPath=".spec.url",priority=0
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status",priority=0
// +kubebuilder:printcolumn:name="Reason",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].reason",priority=1
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",priority=0
// +kubebuilder:selectablefield:JSONPath=`.spec.type`

// VMImportSource registers a reusable connection to a source virtualization
// platform. It is long-lived: VMImportTasks reference it, and deleting it
// deregisters the connection without touching anything already imported.
type VMImportSource struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   VMImportSourceSpec   `json:"spec,omitempty"`
	Status VMImportSourceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// VMImportSourceList contains a list of VMImportSources.
type VMImportSourceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VMImportSource `json:"items"`
}
