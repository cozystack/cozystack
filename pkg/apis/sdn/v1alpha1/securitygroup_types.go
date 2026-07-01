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

	// MembershipLabelPrefix is the prefix of a SecurityGroup's membership label
	// key. The full key is MembershipLabelPrefix + <securitygroup name>; the
	// value is always the empty string. The securitygroup-controller stamps this
	// label onto the pods of the applications a SecurityGroup is attached to, and
	// the backing CiliumNetworkPolicy's endpointSelector matches it. fromSG/toSG
	// peers resolve to this same key on the referenced group, which lets the
	// Cilium dataplane resolve group-to-group references live.
	MembershipLabelPrefix = "securitygroup.sdn.cozystack.io/"

	// MembershipFinalizer guards a SecurityGroup's backing CiliumNetworkPolicy so
	// the securitygroup-controller can strip the membership labels off member
	// pods before the policy is removed. The REST storage re-asserts it on every
	// write (a full-replace PUT would otherwise strip it and orphan the labels),
	// and the controller adds and removes it — both must use this one definition.
	MembershipFinalizer = "sdn.cozystack.io/securitygroup-membership"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// SecurityGroup is a tenant-facing, namespace-scoped firewall object. It is a
// projection of a single CiliumNetworkPolicy served by the Cozystack
// aggregated API: tenants manage SecurityGroups while the platform translates
// each one 1:1 into a CiliumNetworkPolicy in the same namespace, without
// granting tenants direct access to the cilium.io API group.
//
// A SecurityGroup is a membership group: it owns a membership label
// (MembershipLabelPrefix + its name) that the securitygroup-controller stamps
// onto the pods of the applications listed in spec.attachments. The backing
// CiliumNetworkPolicy selects that membership label, so one SecurityGroup can
// apply to several applications at once.
type SecurityGroup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec describes the applications this SecurityGroup attaches to and the
	// traffic it allows.
	Spec SecurityGroupSpec `json:"spec,omitempty"`
}

// SecurityGroupSpec describes the managed applications a SecurityGroup attaches
// to and the traffic it allows. The backing CiliumNetworkPolicy's
// endpointSelector is the SecurityGroup's own membership label, which the
// securitygroup-controller maintains on the attached applications' pods — so a
// SecurityGroup can only ever apply to those applications' own pods in the same
// namespace.
type SecurityGroupSpec struct {
	// Attachments lists the managed applications whose pods join this group. The
	// securitygroup-controller stamps the SecurityGroup's membership label
	// (MembershipLabelPrefix + name) onto the pods of each referenced
	// application in the same namespace, and removes it when the attachment is
	// dropped. An empty list means the group selects no pods.
	Attachments []ApplicationReference `json:"attachments,omitempty"`

	// Ingress is the list of rules describing allowed inbound traffic. Each rule
	// only ADDS allowed sources. An empty list adds no allow rules and does NOT
	// isolate the member pods: effective connectivity is the union of every
	// policy selecting a pod, including the platform's blanket-allow baseline, so
	// an empty list leaves ingress open rather than denying it. Actual deny /
	// default-deny enforcement depends on the default-deny baseline, tracked
	// separately as future work.
	Ingress []IngressRule `json:"ingress,omitempty"`

	// Egress is the list of rules describing allowed outbound traffic. Each rule
	// only ADDS allowed destinations. An empty list adds no allow rules and does
	// NOT isolate the member pods: effective connectivity is the union of every
	// policy selecting a pod, including the platform's blanket-allow baseline, so
	// an empty list leaves egress open rather than denying it. Actual deny /
	// default-deny enforcement depends on the default-deny baseline, tracked
	// separately as future work.
	Egress []EgressRule `json:"egress,omitempty"`
}

// ApplicationReference identifies a managed Cozystack application by its
// group, kind and name. It is used both for SecurityGroup attachments and for
// fromApp/toApp peers, and resolves to the application's lineage labels
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
	// FromApp selects source pods belonging to the referenced managed
	// applications, by their lineage labels.
	FromApp []ApplicationReference `json:"fromApp,omitempty"`

	// FromSG selects source pods that are members of the named SecurityGroups in
	// the same namespace, by their membership label. The reference is live: it
	// follows the other group's membership as attachments change.
	FromSG []string `json:"fromSG,omitempty"`

	// FromCIDR is a list of CIDR ranges allowed as traffic sources.
	FromCIDR []string `json:"fromCIDR,omitempty"`

	// ToPorts restricts the rule to the listed destination ports. An empty list
	// allows traffic on all ports.
	ToPorts []PortRule `json:"toPorts,omitempty"`
}

// EgressRule describes one set of allowed outbound destinations and ports.
type EgressRule struct {
	// ToApp selects destination pods belonging to the referenced managed
	// applications, by their lineage labels.
	ToApp []ApplicationReference `json:"toApp,omitempty"`

	// ToSG selects destination pods that are members of the named SecurityGroups
	// in the same namespace, by their membership label. The reference is live: it
	// follows the other group's membership as attachments change.
	ToSG []string `json:"toSG,omitempty"`

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
