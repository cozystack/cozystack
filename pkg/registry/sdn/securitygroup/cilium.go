// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Cozystack Authors.

// Package securitygroup implements the REST storage for the SecurityGroup
// resource. SecurityGroup is a namespace-scoped projection of a single
// CiliumNetworkPolicy: the storage translates each SecurityGroup into a
// CiliumNetworkPolicy in the same namespace and back.
package securitygroup

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	sdnv1alpha1 "github.com/cozystack/cozystack/pkg/apis/sdn/v1alpha1"
)

// CiliumNetworkPolicy is a minimal, in-tree mirror of the cilium.io/v2
// CiliumNetworkPolicy resource. It carries only the metadata and the subset of
// the policy spec that SecurityGroup exposes, so the Cozystack API server can
// read and write CiliumNetworkPolicy objects through a controller-runtime
// client without importing the full Cilium module (which pins a Kubernetes
// version incompatible with this project's apimachinery fork).
type CiliumNetworkPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec is the policy enforced by the CiliumNetworkPolicy.
	Spec *CiliumNetworkPolicySpec `json:"spec,omitempty"`
}

// CiliumNetworkPolicySpec is the subset of the cilium.io/v2 CiliumNetworkPolicy
// spec that the SecurityGroup projection writes. Unlike SecurityGroupSpec it
// carries a concrete endpointSelector (set by the storage to the SecurityGroup's
// own membership label, never copied from tenant input) and Cilium-shaped
// ingress/egress rules with endpoint selectors. The JSON tags match the
// CiliumNetworkPolicy CRD exactly, so marshalling produces a wire-compatible
// object.
type CiliumNetworkPolicySpec struct {
	// EndpointSelector selects the pods the policy applies to. The storage sets
	// it to the SecurityGroup's own membership label.
	EndpointSelector metav1.LabelSelector `json:"endpointSelector,omitempty"`

	// Ingress is the list of allowed inbound traffic rules.
	Ingress []CiliumIngressRule `json:"ingress,omitempty"`

	// Egress is the list of allowed outbound traffic rules.
	Egress []CiliumEgressRule `json:"egress,omitempty"`
}

// CiliumIngressRule mirrors a single cilium.io/v2 ingress rule. fromApp/fromSG
// peers project into fromEndpoints label selectors; fromCIDR and toPorts carry
// over 1:1. PortRule/FQDNSelector are reused from the SecurityGroup types
// because their wire shape already matches Cilium.
type CiliumIngressRule struct {
	// FromEndpoints selects allowed source pods by label.
	FromEndpoints []metav1.LabelSelector `json:"fromEndpoints,omitempty"`

	// FromCIDR is a list of CIDR ranges allowed as traffic sources.
	FromCIDR []string `json:"fromCIDR,omitempty"`

	// ToPorts restricts the rule to the listed destination ports.
	ToPorts []sdnv1alpha1.PortRule `json:"toPorts,omitempty"`
}

// CiliumEgressRule mirrors a single cilium.io/v2 egress rule. toApp/toSG peers
// project into toEndpoints label selectors; toCIDR, toFQDNs and toPorts carry
// over 1:1.
type CiliumEgressRule struct {
	// ToEndpoints selects allowed destination pods by label.
	ToEndpoints []metav1.LabelSelector `json:"toEndpoints,omitempty"`

	// ToCIDR is a list of CIDR ranges allowed as traffic destinations.
	ToCIDR []string `json:"toCIDR,omitempty"`

	// ToFQDNs is a list of FQDN matchers allowed as traffic destinations.
	ToFQDNs []sdnv1alpha1.FQDNSelector `json:"toFQDNs,omitempty"`

	// ToPorts restricts the rule to the listed destination ports.
	ToPorts []sdnv1alpha1.PortRule `json:"toPorts,omitempty"`
}

// CiliumNetworkPolicyList is a list of CiliumNetworkPolicy objects.
type CiliumNetworkPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CiliumNetworkPolicy `json:"items"`
}

const (
	ciliumGroup       = "cilium.io"
	ciliumVersion     = "v2"
	cnpKind           = "CiliumNetworkPolicy"
	cnpAPIVersionFull = ciliumGroup + "/" + ciliumVersion
)

// ciliumGroupVersion is the {group,version} of the backing CiliumNetworkPolicy.
var ciliumGroupVersion = schema.GroupVersion{Group: ciliumGroup, Version: ciliumVersion}

// AddToScheme registers the in-tree CiliumNetworkPolicy mirror with the given
// scheme so the controller-runtime client can serialize the backing objects.
func AddToScheme(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(ciliumGroupVersion,
		&CiliumNetworkPolicy{},
		&CiliumNetworkPolicyList{},
	)
	metav1.AddToGroupVersion(scheme, ciliumGroupVersion)
	return nil
}

// DeepCopyInto copies the receiver into out.
func (in *CiliumNetworkPolicy) DeepCopyInto(out *CiliumNetworkPolicy) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	if in.Spec != nil {
		out.Spec = in.Spec.DeepCopy()
	}
}

// DeepCopy returns a deep copy of the receiver.
func (in *CiliumNetworkPolicy) DeepCopy() *CiliumNetworkPolicy {
	if in == nil {
		return nil
	}
	out := new(CiliumNetworkPolicy)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject returns a deep copy of the receiver as a runtime.Object.
func (in *CiliumNetworkPolicy) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto copies the receiver into out. Hand-written because cilium.go is
// not run through deepcopy-gen.
func (in *CiliumNetworkPolicySpec) DeepCopyInto(out *CiliumNetworkPolicySpec) {
	*out = *in
	in.EndpointSelector.DeepCopyInto(&out.EndpointSelector)
	if in.Ingress != nil {
		out.Ingress = make([]CiliumIngressRule, len(in.Ingress))
		for i := range in.Ingress {
			in.Ingress[i].DeepCopyInto(&out.Ingress[i])
		}
	}
	if in.Egress != nil {
		out.Egress = make([]CiliumEgressRule, len(in.Egress))
		for i := range in.Egress {
			in.Egress[i].DeepCopyInto(&out.Egress[i])
		}
	}
}

// DeepCopy returns a deep copy of the receiver.
func (in *CiliumNetworkPolicySpec) DeepCopy() *CiliumNetworkPolicySpec {
	if in == nil {
		return nil
	}
	out := new(CiliumNetworkPolicySpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto copies the receiver into out.
func (in *CiliumIngressRule) DeepCopyInto(out *CiliumIngressRule) {
	*out = *in
	if in.FromEndpoints != nil {
		out.FromEndpoints = make([]metav1.LabelSelector, len(in.FromEndpoints))
		for i := range in.FromEndpoints {
			in.FromEndpoints[i].DeepCopyInto(&out.FromEndpoints[i])
		}
	}
	if in.FromCIDR != nil {
		out.FromCIDR = make([]string, len(in.FromCIDR))
		copy(out.FromCIDR, in.FromCIDR)
	}
	if in.ToPorts != nil {
		out.ToPorts = make([]sdnv1alpha1.PortRule, len(in.ToPorts))
		for i := range in.ToPorts {
			in.ToPorts[i].DeepCopyInto(&out.ToPorts[i])
		}
	}
}

// DeepCopyInto copies the receiver into out.
func (in *CiliumEgressRule) DeepCopyInto(out *CiliumEgressRule) {
	*out = *in
	if in.ToEndpoints != nil {
		out.ToEndpoints = make([]metav1.LabelSelector, len(in.ToEndpoints))
		for i := range in.ToEndpoints {
			in.ToEndpoints[i].DeepCopyInto(&out.ToEndpoints[i])
		}
	}
	if in.ToCIDR != nil {
		out.ToCIDR = make([]string, len(in.ToCIDR))
		copy(out.ToCIDR, in.ToCIDR)
	}
	if in.ToFQDNs != nil {
		out.ToFQDNs = make([]sdnv1alpha1.FQDNSelector, len(in.ToFQDNs))
		copy(out.ToFQDNs, in.ToFQDNs)
	}
	if in.ToPorts != nil {
		out.ToPorts = make([]sdnv1alpha1.PortRule, len(in.ToPorts))
		for i := range in.ToPorts {
			in.ToPorts[i].DeepCopyInto(&out.ToPorts[i])
		}
	}
}

// DeepCopyInto copies the receiver into out.
func (in *CiliumNetworkPolicyList) DeepCopyInto(out *CiliumNetworkPolicyList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]CiliumNetworkPolicy, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

// DeepCopy returns a deep copy of the receiver.
func (in *CiliumNetworkPolicyList) DeepCopy() *CiliumNetworkPolicyList {
	if in == nil {
		return nil
	}
	out := new(CiliumNetworkPolicyList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject returns a deep copy of the receiver as a runtime.Object.
func (in *CiliumNetworkPolicyList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}
