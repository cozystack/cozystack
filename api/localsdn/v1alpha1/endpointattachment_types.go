/*
Copyright 2026 The Cozystack Authors.

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

// EndpointApplicationReference names the managed application whose endpoint
// is attached. All three fields are compared against the lineage labels the
// platform stamps on the application's objects.
type EndpointApplicationReference struct {
	// Group of the application's API.
	// +kubebuilder:default=apps.cozystack.io
	// +optional
	Group string `json:"group,omitempty"`

	// Kind of the application, e.g. Postgres.
	// +kubebuilder:validation:MinLength=1
	Kind string `json:"kind"`

	// Name of the application instance in this namespace.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// EndpointReference selects one endpoint of the application. It is a struct
// so that a named-endpoint vocabulary can be added beside serviceName later.
type EndpointReference struct {
	// ServiceName is a tenant-facing Service of the application, in the
	// attachment's namespace.
	// +kubebuilder:validation:MinLength=1
	ServiceName string `json:"serviceName"`
}

// AddressFamily is the IP family an address claim requests.
// +kubebuilder:validation:Enum=IPv4;IPv6;Dual
type AddressFamily string

const (
	AddressFamilyIPv4 AddressFamily = "IPv4"
	AddressFamilyIPv6 AddressFamily = "IPv6"
	AddressFamilyDual AddressFamily = "Dual"
)

// ExposureMethod selects a VM datapath mode rendered through the cozy-proxy
// Service contract. The values are the vm-instance chart's externalMethod
// vocabulary.
// +kubebuilder:validation:Enum=WholeIP;PortList
type ExposureMethod string

const (
	ExposureMethodWholeIP  ExposureMethod = "WholeIP"
	ExposureMethodPortList ExposureMethod = "PortList"
)

// LoadBalancerAttachment attaches a dedicated address through one additive
// type: LoadBalancer Service that mirrors the endpoint Service.
//
// className, claimName and family select the address and are immutable:
// changing any of them could re-mint or release an address a client has
// allow-listed. ports, method and allowICMP only shape the rendered Service.
// +kubebuilder:validation:XValidation:rule="!(has(self.className) && has(self.claimName))",message="className and claimName are mutually exclusive"
// +kubebuilder:validation:XValidation:rule="(has(self.className) ? self.className : ”) == (has(oldSelf.className) ? oldSelf.className : ”)",message="className is immutable"
// +kubebuilder:validation:XValidation:rule="(has(self.claimName) ? self.claimName : ”) == (has(oldSelf.claimName) ? oldSelf.claimName : ”)",message="claimName is immutable"
// +kubebuilder:validation:XValidation:rule="(has(self.family) ? self.family : ”) == (has(oldSelf.family) ? oldSelf.family : ”)",message="family is immutable"
// +kubebuilder:validation:XValidation:rule="!has(self.allowICMP) || (has(self.method) && self.method == 'PortList')",message="allowICMP applies to method PortList only"
// +kubebuilder:validation:XValidation:rule="!(has(self.method) && self.method == 'PortList') || (has(self.ports) && size(self.ports) > 0)",message="method PortList requires ports"
type LoadBalancerAttachment struct {
	// ClassName mints a fresh IPAddressClaim from this IPAddressClass. Empty
	// with no claimName mints from the default class.
	// +optional
	ClassName string `json:"className,omitempty"`

	// ClaimName binds a pre-reserved IPAddressClaim in this namespace instead
	// of minting one. The claim outlives the attachment.
	// +optional
	ClaimName string `json:"claimName,omitempty"`

	// Family is passed to a minted claim and validated against a referenced
	// one.
	// +optional
	Family AddressFamily `json:"family,omitempty"`

	// Ports is a subset of the endpoint's ports to publish. It is the only
	// port source when the endpoint has nothing meaningful to mirror (a VM's
	// sentinel-port Service).
	// +kubebuilder:validation:MaxItems=256
	// +kubebuilder:validation:items:Minimum=1
	// +kubebuilder:validation:items:Maximum=65535
	// +listType=set
	// +optional
	Ports []int32 `json:"ports,omitempty"`

	// Method selects a VM datapath mode. WholeIP renders 1:1 NAT of every
	// port to a single backend; PortList renders the listed ports.
	// +optional
	Method ExposureMethod `json:"method,omitempty"`

	// AllowICMP lets ICMP through in PortList mode.
	// +optional
	AllowICMP *bool `json:"allowICMP,omitempty"`
}

// EndpointAttachmentSpec defines the desired state of EndpointAttachment.
//
// The mechanism is a union with loadBalancer as its only member today. A
// future member is added by extending the list in the exactly-one rule.
// +kubebuilder:validation:XValidation:rule="[has(self.loadBalancer)].filter(m, m).size() == 1",message="exactly one mechanism member must be set"
// +kubebuilder:validation:XValidation:rule="has(self.loadBalancer) == has(oldSelf.loadBalancer)",message="the mechanism member is immutable"
type EndpointAttachmentSpec struct {
	// ApplicationRef names the application in this namespace. Immutable.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="applicationRef is immutable"
	ApplicationRef EndpointApplicationReference `json:"applicationRef"`

	// Endpoint selects which tenant-facing Service of the application to
	// attach the address to.
	Endpoint EndpointReference `json:"endpoint"`

	// LoadBalancer attaches a dedicated address through an additive
	// LoadBalancer Service.
	// +optional
	LoadBalancer *LoadBalancerAttachment `json:"loadBalancer,omitempty"`
}

// EndpointAttachmentPhase is a printed summary of the conditions.
// +kubebuilder:validation:Enum=Pending;Attached;Detached
type EndpointAttachmentPhase string

const (
	// EndpointAttachmentPending: not yet both resolved and provisioned.
	EndpointAttachmentPending EndpointAttachmentPhase = "Pending"
	// EndpointAttachmentAttached: Resolved and Provisioned are both true.
	EndpointAttachmentAttached EndpointAttachmentPhase = "Attached"
	// EndpointAttachmentDetached: the endpoint Service went away after the
	// attachment had rendered; the address is kept, nothing is published.
	EndpointAttachmentDetached EndpointAttachmentPhase = "Detached"
)

// Condition types and reasons reported on an EndpointAttachment.
const (
	// ConditionResolved is true when the endpoint Service exists and passes
	// the tenant-facing authorization check.
	ConditionResolved = "Resolved"
	// ConditionProvisioned is true when the claim is bound and the rendered
	// Service carries its address. It asserts address and Service wiring
	// only; end-to-end reachability additionally depends on the CNI's own
	// policy objects.
	ConditionProvisioned = "Provisioned"

	ReasonResolved                     = "Resolved"
	ReasonApplicationNotFound          = "ApplicationNotFound"
	ReasonApplicationIncarnationChange = "ApplicationIncarnationChanged"
	ReasonEndpointNotFound             = "EndpointNotFound"
	ReasonEndpointLineageMismatch      = "EndpointLineageMismatch"
	ReasonEndpointNotTenantFacing      = "EndpointNotTenantFacing"

	ReasonProvisioned             = "Provisioned"
	ReasonEndpointDetached        = "EndpointDetached"
	ReasonNothingToPublish        = "NothingToPublish"
	ReasonDatapathUnavailable     = "DatapathContractUnavailable"
	ReasonMultipleBackends        = "MultipleBackends"
	ReasonBackendAlreadyDelegated = "BackendAlreadyDelegated"
	ReasonClaimNotFound           = "ClaimNotFound"
	ReasonClaimPending            = "ClaimPending"
	ReasonClaimInUse              = "ClaimInUse"
	ReasonFamilyConflict          = "FamilyConflict"
	ReasonWaitingForAddress       = "WaitingForAddress"
	ReasonRenderFailed            = "RenderFailed"
)

// EndpointAttachmentStatus defines the observed state of EndpointAttachment.
type EndpointAttachmentStatus struct {
	// ObservedGeneration is the spec generation the status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase summarises the conditions.
	// +optional
	Phase EndpointAttachmentPhase `json:"phase,omitempty"`

	// ServiceName is the rendered LoadBalancer Service.
	// +optional
	ServiceName string `json:"serviceName,omitempty"`

	// ClaimName is the IPAddressClaim the rendered Service consumes, minted
	// or referenced.
	// +optional
	ClaimName string `json:"claimName,omitempty"`

	// Addresses mirrors the claim's bound addresses.
	// +optional
	Addresses []string `json:"addresses,omitempty"`

	// Conditions follow metav1.Condition semantics.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Application",type="string",JSONPath=".spec.applicationRef.name"
// +kubebuilder:printcolumn:name="Endpoint",type="string",JSONPath=".spec.endpoint.serviceName"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Addresses",type="string",JSONPath=".status.addresses"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// EndpointAttachment attaches an external address to one tenant-facing
// endpoint of one managed application.
type EndpointAttachment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   EndpointAttachmentSpec   `json:"spec,omitempty"`
	Status EndpointAttachmentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// EndpointAttachmentList contains a list of EndpointAttachment.
type EndpointAttachmentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []EndpointAttachment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&EndpointAttachment{}, &EndpointAttachmentList{})
}
