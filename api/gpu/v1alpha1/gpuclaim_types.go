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

// GPUClaimPhase decides what a claim holds and costs: a Bound or Attached
// claim holds units on its node, and a Lost one still counts against
// budgets.
// +kubebuilder:validation:Enum=Pending;Bound;Attached;Lost
type GPUClaimPhase string

const (
	GPUClaimPending  GPUClaimPhase = "Pending"
	GPUClaimBound    GPUClaimPhase = "Bound"
	GPUClaimAttached GPUClaimPhase = "Attached"
	GPUClaimLost     GPUClaimPhase = "Lost"
)

// GPUClaimSpec is immutable: a change is a new order.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable; order a new GPUClaim instead"
type GPUClaimSpec struct {
	// ClassName is the GPUClass the claim is ordered against.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	ClassName string `json:"className"`

	// Count is the number of GPUs of the class, always on one node.
	// +required
	// +kubebuilder:validation:Minimum=1
	Count int32 `json:"count"`
}

// GPUClaimAttachment names the VMI a claim is attached to.
type GPUClaimAttachment struct {
	// VMI is the name of the VirtualMachineInstance in the claim's namespace.
	// For a held claim it is recorded at admission, before the VMI exists.
	// +required
	// +kubebuilder:validation:MinLength=1
	VMI string `json:"vmi"`
}

// RejectedNode is a node that refused the launcher pod of the claim's set
// after the controller pinned it there.
type RejectedNode struct {
	// +required
	// +kubebuilder:validation:MinLength=1
	NodeName string `json:"nodeName"`

	// Reason is the scheduler's message for the refusal.
	// +optional
	Reason string `json:"reason,omitempty"`

	// Until is when the node may be picked for the set again, unless the
	// node changes first.
	// +required
	Until metav1.Time `json:"until"`
}

// GPUClaimStatus is written by the controller and the admission webhook;
// tenants cannot write it.
type GPUClaimStatus struct {
	// +optional
	Phase GPUClaimPhase `json:"phase,omitempty"`

	// NodeName is the node the claim holds its units on.
	// +optional
	NodeName string `json:"nodeName,omitempty"`

	// +optional
	AttachedTo *GPUClaimAttachment `json:"attachedTo,omitempty"`

	// BoundAt is when the claim was last bound to a node.
	// +optional
	BoundAt *metav1.Time `json:"boundAt,omitempty"`

	// RejectedNodes are not picked for the claim's set until their Until.
	// +optional
	// +listType=map
	// +listMapKey=nodeName
	RejectedNodes []RejectedNode `json:"rejectedNodes,omitempty"`

	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// keep: an uninstall or a failed reinstall of the controller chart must not
// delete every hold and the usage history along with the CRD.
// +kubebuilder:metadata:annotations="helm.sh/resource-policy=keep"
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:selectablefield:JSONPath=`.status.nodeName`
// +kubebuilder:printcolumn:name="Class",type="string",JSONPath=".spec.className"
// +kubebuilder:printcolumn:name="Count",type="integer",JSONPath=".spec.count"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Node",type="string",JSONPath=".status.nodeName"
// +kubebuilder:printcolumn:name="VMI",type="string",JSONPath=".status.attachedTo.vmi"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// GPUClaim is a tenant's GPU, or a set of GPUs of one class used together
// on one node.
type GPUClaim struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GPUClaimSpec   `json:"spec"`
	Status GPUClaimStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// GPUClaimList contains a list of GPUClaim.
type GPUClaimList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GPUClaim `json:"items"`
}

func init() {
	SchemeBuilder.Register(&GPUClaim{}, &GPUClaimList{})
}
