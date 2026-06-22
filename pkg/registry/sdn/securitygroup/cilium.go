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
// carries a concrete endpointSelector (derived by the storage from the
// SecurityGroup's TargetRef, never copied from tenant input). The JSON tags
// match the CiliumNetworkPolicy CRD exactly, so marshalling produces a
// wire-compatible object. The rule slice types are shared with SecurityGroupSpec
// since the ingress/egress projection is a 1:1 copy.
type CiliumNetworkPolicySpec struct {
	// EndpointSelector selects the pods the policy applies to. The storage
	// derives it from the SecurityGroup's TargetRef.
	EndpointSelector metav1.LabelSelector `json:"endpointSelector,omitempty"`

	// Ingress is the list of allowed inbound traffic rules.
	Ingress []sdnv1alpha1.IngressRule `json:"ingress,omitempty"`

	// Egress is the list of allowed outbound traffic rules.
	Egress []sdnv1alpha1.EgressRule `json:"egress,omitempty"`
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
// not run through deepcopy-gen; the field handling mirrors the generated
// SecurityGroupSpec deepcopy.
func (in *CiliumNetworkPolicySpec) DeepCopyInto(out *CiliumNetworkPolicySpec) {
	*out = *in
	in.EndpointSelector.DeepCopyInto(&out.EndpointSelector)
	if in.Ingress != nil {
		out.Ingress = make([]sdnv1alpha1.IngressRule, len(in.Ingress))
		for i := range in.Ingress {
			in.Ingress[i].DeepCopyInto(&out.Ingress[i])
		}
	}
	if in.Egress != nil {
		out.Egress = make([]sdnv1alpha1.EgressRule, len(in.Egress))
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
