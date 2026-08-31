// SPDX-License-Identifier: Apache-2.0
// Package v1alpha1 defines forklift.cozystack.io API types.
//
// Group: forklift.cozystack.io
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
	// ReasonForkliftNotInstalled means the migration engine's CRDs are absent.
	// The controller ships independently of Forklift, so this is a normal state
	// on a cluster that has enabled one package and not the other.
	ReasonForkliftNotInstalled = "ForkliftNotInstalled"
)

// InventoryTruncatedAnnotation marks a published VM list that was cut short
// because the source holds more machines than one object should carry. A picker
// missing an entry then has a visible reason rather than looking broken.
const InventoryTruncatedAnnotation = thisGroup + "/inventory-truncated"

// The contract between the controller that publishes a source's machine list
// and the aggregated API that serves it to the console's picker. It lives here
// because both sides must agree on it and neither owns the other.
const (
	// inventoryConfigMapPrefix makes the object's name derivable from the
	// source alone, so a reader addresses it without a lookup.
	inventoryConfigMapPrefix = "vmimport-inventory-"

	// InventoryVMsKey holds the JSON array of PublishedVM.
	InventoryVMsKey = "vms"
)

// InventoryConfigMapName is where a source's published machine list lives.
func InventoryConfigMapName(source string) string {
	return inventoryConfigMapPrefix + source
}

// PublishedVM is one entry of that list: deliberately the smallest thing a
// picker needs, a value to write and a name to show.
type PublishedVM struct {
	// ID is the managed-object reference, which is what a task records.
	ID string `json:"id"`
	// Name is the machine's name as the source knows it.
	Name string `json:"name"`
	// Path distinguishes two machines sharing a name in different folders.
	Path string `json:"path,omitempty"`
}

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

	// CACert is the PEM-encoded certificate authority that signed the
	// endpoint's TLS certificate. Required unless insecureSkipVerify is set:
	// the migration engine refuses a vSphere connection that can neither
	// verify the endpoint nor was told not to.
	//
	// This is a CA certificate, not a fingerprint. A SHA-1 thumbprint is what
	// the engine wants for a direct ESXi host connection, which v1 does not
	// expose, and supplying one here does not satisfy the check.
	// +optional
	CACert string `json:"caCert,omitempty"`

	// InsecureSkipVerify disables verification of the endpoint's TLS
	// certificate. Convenient in a lab and a bad idea anywhere else: the
	// credentials above travel over that connection. Prefer caCert.
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
	// +kubebuilder:validation:XValidation:rule="has(self.caCert) || (has(self.insecureSkipVerify) && self.insecureSkipVerify)",message="either caCert must be set or insecureSkipVerify must be true: the migration engine will not open a vSphere connection it can neither verify nor was told to trust"
	Credentials ProviderCredentials `json:"credentials"`

	// Hosts redirects the disk transfer for individual ESXi hosts.
	//
	// Disk data does not travel through vCenter. VDDK opens its connection
	// straight to the ESXi host holding the VM, at whatever address vCenter
	// advertises for it — and that address is frequently one the cluster
	// cannot reach: a management network the workers are not on, or, worse,
	// one that collides with the cluster's own Service CIDR, where the packet
	// is swallowed by service routing and never leaves. The transfer then
	// fails late, after validation has passed, with an NBD error naming
	// neither the address nor the reason.
	//
	// An entry here names the host and the address to use instead. Each
	// carries its own credentials because the ESXi host authenticates the
	// connection itself rather than honouring the vCenter session, and the
	// engine connection-tests them before the transfer starts.
	//
	// +optional
	// +listType=map
	// +listMapKey=id
	Hosts []HostOverride `json:"hosts,omitempty"`
}

// HostOverride redirects the disk transfer for one ESXi host.
type HostOverride struct {
	// ID is the host's managed-object reference in the source inventory
	// (e.g. `host-10`). It is what the VM's inventory record names as its
	// host, not the hostname.
	// +kubebuilder:validation:MinLength=1
	ID string `json:"id"`

	// Address is the address the cluster should use to reach this host for the
	// disk transfer, replacing the one vCenter advertises.
	// +kubebuilder:validation:MinLength=1
	Address string `json:"address"`

	// Credentials is the account on the ESXi host itself. The host
	// authenticates the transfer connection directly, so the provider's
	// vCenter account does not carry over.
	// +kubebuilder:validation:XValidation:rule="has(self.caCert) || (has(self.insecureSkipVerify) && self.insecureSkipVerify)",message="either caCert must be set or insecureSkipVerify must be true: the migration engine will not open a connection to the host it can neither verify nor was told to trust"
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
