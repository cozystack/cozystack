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

// GPUDevicePhase decides what a device holds and costs; the shared model
// package carries the table.
// +kubebuilder:validation:Enum=Pending;Bound;Attached;Lost
type GPUDevicePhase string

const (
	GPUDevicePending  GPUDevicePhase = "Pending"
	GPUDeviceBound    GPUDevicePhase = "Bound"
	GPUDeviceAttached GPUDevicePhase = "Attached"
	GPUDeviceLost     GPUDevicePhase = "Lost"
)

// BindResult is the binder's answer to a BindRequest.
// +kubebuilder:validation:Enum=Succeeded;Refused
type BindResult string

const (
	BindSucceeded BindResult = "Succeeded"
	BindRefused   BindResult = "Refused"
)

// GPUDeviceSpec is immutable: a change is a new order.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable; order a new GPUDevice instead"
type GPUDeviceSpec struct {
	// ClassName is the GPUClass the device is ordered against.
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

// GPUDeviceAttachment names the VMI a device is attached to.
type GPUDeviceAttachment struct {
	// VMI is the name of the VirtualMachineInstance in the device's namespace.
	// It is recorded at admission, before the VMI exists.
	// +required
	// +kubebuilder:validation:MinLength=1
	VMI string `json:"vmi"`
}

// BindRequest asks the binder to place a set of devices onto one node. The
// admission webhook writes ID, VMI and Devices; the binder writes Result and
// Message. internal/gpu/doc.go carries the full contract.
type BindRequest struct {
	// ID is shared by every device of one request and is unique per request.
	// +required
	// +kubebuilder:validation:MinLength=1
	ID string `json:"id"`

	// VMI is the VirtualMachineInstance the set is gathered for.
	// +required
	// +kubebuilder:validation:MinLength=1
	VMI string `json:"vmi"`

	// Devices names every device of the set, in the device's namespace,
	// this one included.
	// +required
	// +kubebuilder:validation:MinItems=1
	// +listType=set
	Devices []string `json:"devices"`

	// RequestedAt lets the controller expire a request whose VMI never appears.
	// +required
	RequestedAt metav1.Time `json:"requestedAt"`

	// +optional
	Result BindResult `json:"result,omitempty"`

	// Message explains a refusal and names each device and its node.
	// +optional
	Message string `json:"message,omitempty"`
}

// GPUDeviceStatus is written by the controller and the admission webhook;
// tenants cannot write it.
type GPUDeviceStatus struct {
	// +optional
	Phase GPUDevicePhase `json:"phase,omitempty"`

	// NodeName is the node the device holds its units on.
	// +optional
	NodeName string `json:"nodeName,omitempty"`

	// +optional
	AttachedTo *GPUDeviceAttachment `json:"attachedTo,omitempty"`

	// BoundAt is when the device was last bound to a node.
	// +optional
	BoundAt *metav1.Time `json:"boundAt,omitempty"`

	// +optional
	BindRequest *BindRequest `json:"bindRequest,omitempty"`

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

// GPUDevice is a tenant's GPU, or a set of GPUs of one class used together
// on one node.
type GPUDevice struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GPUDeviceSpec   `json:"spec"`
	Status GPUDeviceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// GPUDeviceList contains a list of GPUDevice.
type GPUDeviceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GPUDevice `json:"items"`
}

func init() {
	SchemeBuilder.Register(&GPUDevice{}, &GPUDeviceList{})
}
