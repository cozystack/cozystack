// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Cozystack Authors.

package securitygroupcontroller

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// CiliumNetworkPolicy is a metadata-only, in-tree mirror of the cilium.io/v2
// CiliumNetworkPolicy resource. The securitygroup-controller only reads a
// policy's marker label, its attachments annotation and its finalizers — never
// the spec — so this mirror deliberately omits the spec entirely, keeping the
// controller binary free of the full Cilium module (whose Kubernetes pin is
// incompatible with this project's apimachinery fork).
//
// NEVER Update an object of this type: a PUT would serialize it without a spec
// and wipe the real policy's rules. Finalizer changes go through MergeFrom
// patches, which carry only the changed fields.
type CiliumNetworkPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
}

// CiliumNetworkPolicyList is a list of CiliumNetworkPolicy objects.
type CiliumNetworkPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CiliumNetworkPolicy `json:"items"`
}

var ciliumGroupVersion = schema.GroupVersion{Group: "cilium.io", Version: "v2"}

// AddToScheme registers the in-tree CiliumNetworkPolicy mirror so the
// controller-runtime client can list and patch the backing policies.
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
