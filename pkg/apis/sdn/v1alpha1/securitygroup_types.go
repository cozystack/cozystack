// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Cozystack Authors.

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

const (
	// SecurityGroupKind is the kind of the SecurityGroup resource.
	SecurityGroupKind = "SecurityGroup"
	// SecurityGroupListKind is the kind of the SecurityGroupList resource.
	SecurityGroupListKind = "SecurityGroupList"
	// SecurityGroupSingularName is the singular resource name.
	SecurityGroupSingularName = "securitygroup"
	// SecurityGroupPluralName is the plural resource name.
	SecurityGroupPluralName = "securitygroups"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// SecurityGroup is a tenant-facing, namespace-scoped firewall object. It is a
// projection of a single CiliumNetworkPolicy served by the Cozystack
// aggregated API: tenants manage SecurityGroups while the platform translates
// each one 1:1 into a CiliumNetworkPolicy in the same namespace, without
// granting tenants direct access to the cilium.io API group.
type SecurityGroup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec describes the traffic the SecurityGroup allows.
	Spec SecurityGroupSpec `json:"spec,omitempty"`
}

// SecurityGroupSpec describes the traffic a SecurityGroup allows and the
// managed application it attaches to. The platform derives the backing
// CiliumNetworkPolicy's endpointSelector from TargetRef rather than copying a
// tenant-authored selector, so a SecurityGroup can only ever apply to the
// referenced application's own pods in the same namespace.
type SecurityGroupSpec struct {
	// TargetRef references the managed application whose pods this SecurityGroup
	// applies to. The backing CiliumNetworkPolicy's endpointSelector is derived
	// from this reference via the application's lineage labels, so the selector
	// is machine-generated and cannot be pointed at arbitrary or platform-owned
	// pods.
	TargetRef ApplicationReference `json:"targetRef"`

	// Ingress is the list of rules describing allowed inbound traffic. An empty
	// list denies all ingress to the targeted application's pods.
	Ingress []IngressRule `json:"ingress,omitempty"`

	// Egress is the list of rules describing allowed outbound traffic. An empty
	// list denies all egress from the targeted application's pods.
	Egress []EgressRule `json:"egress,omitempty"`
}

// ApplicationReference identifies a managed Cozystack application by its
// group, kind and name. The SecurityGroup projects this reference into an
// endpointSelector matching the application's lineage labels
// (apps.cozystack.io/application.{group,kind,name}).
type ApplicationReference struct {
	// APIGroup of the referenced application. Defaults to apps.cozystack.io when
	// empty, the group under which Cozystack serves its managed applications.
	APIGroup string `json:"apiGroup,omitempty"`

	// Kind of the referenced application, e.g. "Postgres".
	Kind string `json:"kind"`

	// Name of the referenced application.
	Name string `json:"name"`
}

// IngressRule describes one set of allowed inbound sources and ports.
type IngressRule struct {
	// FromEndpoints selects source pods by label. An empty selector matches all
	// pods in the same namespace.
	FromEndpoints []metav1.LabelSelector `json:"fromEndpoints,omitempty"`

	// FromCIDR is a list of CIDR ranges allowed as traffic sources.
	FromCIDR []string `json:"fromCIDR,omitempty"`

	// ToPorts restricts the rule to the listed destination ports. An empty list
	// allows traffic on all ports.
	ToPorts []PortRule `json:"toPorts,omitempty"`
}

// EgressRule describes one set of allowed outbound destinations and ports.
type EgressRule struct {
	// ToEndpoints selects destination pods by label. An empty selector matches
	// all pods in the same namespace.
	ToEndpoints []metav1.LabelSelector `json:"toEndpoints,omitempty"`

	// ToCIDR is a list of CIDR ranges allowed as traffic destinations.
	ToCIDR []string `json:"toCIDR,omitempty"`

	// ToFQDNs is a list of fully qualified domain name matchers allowed as
	// traffic destinations.
	ToFQDNs []FQDNSelector `json:"toFQDNs,omitempty"`

	// ToPorts restricts the rule to the listed destination ports. An empty list
	// allows traffic on all ports.
	ToPorts []PortRule `json:"toPorts,omitempty"`
}

// PortRule is a set of ports a traffic rule applies to.
type PortRule struct {
	// Ports is the list of port/protocol pairs the rule applies to.
	Ports []PortProtocol `json:"ports,omitempty"`
}

// PortProtocol is a single port and protocol pair.
type PortProtocol struct {
	// Port is the L4 port number as a string, or a named port. An empty value
	// matches all ports.
	Port string `json:"port,omitempty"`

	// Protocol is the L4 protocol. One of TCP, UDP, SCTP or ANY. Defaults to ANY
	// when empty.
	Protocol string `json:"protocol,omitempty"`
}

// FQDNSelector matches destination traffic by domain name.
type FQDNSelector struct {
	// MatchName matches a fully qualified domain name exactly.
	MatchName string `json:"matchName,omitempty"`

	// MatchPattern matches fully qualified domain names against a pattern, where
	// "*" matches a single domain label.
	MatchPattern string `json:"matchPattern,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// SecurityGroupList is a list of SecurityGroup objects.
type SecurityGroupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SecurityGroup `json:"items"`
}
