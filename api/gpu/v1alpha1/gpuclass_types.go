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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// GPUMode is how a class's GPUs reach a VM.
// +kubebuilder:validation:Enum=passthrough;vgpu
type GPUMode string

const (
	GPUModePassthrough GPUMode = "passthrough"
	GPUModeVGPU        GPUMode = "vgpu"
)

// OnCordonPolicy decides what happens to an unattached device on a cordoned node.
// +kubebuilder:validation:Enum=Move;Stay
type OnCordonPolicy string

const (
	// OnCordonMove moves an unattached device to another node of its class
	// when one has room, keeping its hold.
	OnCordonMove OnCordonPolicy = "Move"
	// OnCordonStay keeps the device on its node until the cordon is lifted.
	OnCordonStay OnCordonPolicy = "Stay"
)

// DevicePluginSource serves a class through a Kubernetes device plugin.
type DevicePluginSource struct {
	// ResourceName is the extended resource the device plugin advertises for
	// this class's GPUs. Two classes may share it only when their node
	// selectors do not overlap.
	// +required
	// +kubebuilder:validation:MinLength=3
	// +kubebuilder:validation:MaxLength=317
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`
	ResourceName string `json:"resourceName"`
}

// GPUClassSource is the backend that serves a class. Exactly one member is
// set. The name "dra" is reserved for a DRA-backed member.
// +kubebuilder:validation:XValidation:rule="[has(self.devicePlugin)].filter(x, x).size() == 1",message="exactly one source must be set; devicePlugin is the only source supported, dra is reserved"
type GPUClassSource struct {
	// +optional
	DevicePlugin *DevicePluginSource `json:"devicePlugin,omitempty"`
}

// GPUClassSpec defines one GPU offer of the hoster's catalog.
// +kubebuilder:validation:XValidation:rule="self.mode == 'vgpu' ? has(self.profile) : !has(self.profile)",message="profile is required for mode vgpu and not allowed for mode passthrough"
type GPUClassSpec struct {
	// DisplayName is the human-readable name of the offer.
	// +optional
	// +kubebuilder:validation:MaxLength=256
	DisplayName string `json:"displayName,omitempty"`

	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Vendor string `json:"vendor"`

	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Model string `json:"model"`

	// +required
	Mode GPUMode `json:"mode"`

	// Profile is the vGPU profile, such as L40S-24C. Set only for mode vgpu.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Profile string `json:"profile,omitempty"`

	// Memory is the GPU memory of one unit of the class.
	// +optional
	Memory *resource.Quantity `json:"memory,omitempty"`

	// MaxCount is how many GPUs of this class one VM may take. Unset means 1
	// for mode vgpu and, for mode passthrough, no limit beyond the GPUs of
	// one node; read it through the shared model rather than the raw field.
	// +optional
	// +kubebuilder:validation:Minimum=1
	MaxCount *int32 `json:"maxCount,omitempty"`

	// OnCordon decides whether an unattached device leaves a cordoned node.
	// +optional
	// +kubebuilder:default=Move
	OnCordon OnCordonPolicy `json:"onCordon,omitempty"`

	// NodeSelector selects the nodes that serve this class.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// +required
	Source GPUClassSource `json:"source"`
}

// GPUClassStatus is readable by every authenticated user, so it carries no
// node names and no counts beyond free capacity.
type GPUClassStatus struct {
	// Free is the number of free units of the class on eligible nodes.
	// Absent until the controller has computed it.
	// +optional
	Free *int32 `json:"free,omitempty"`

	// MaxFreePerNode is the largest number of free units on one node.
	// +optional
	MaxFreePerNode *int32 `json:"maxFreePerNode,omitempty"`

	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:metadata:annotations="helm.sh/resource-policy=keep"
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Mode",type="string",JSONPath=".spec.mode"
// +kubebuilder:printcolumn:name="Model",type="string",JSONPath=".spec.model"
// +kubebuilder:printcolumn:name="Profile",type="string",JSONPath=".spec.profile"
// +kubebuilder:printcolumn:name="Free",type="integer",JSONPath=".status.free"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:validation:XValidation:rule="size(self.metadata.name) <= 63 && self.metadata.name.matches('^[a-z0-9]([-a-z0-9]*[a-z0-9])?$')",message="GPUClass name must be a DNS label, since it becomes part of a quota key"
// +kubebuilder:validation:XValidation:rule="self.metadata.name != 'total'",message="GPUClass name total is reserved for the gpu.cozystack.io/total quota key"

// GPUClass is one GPU offer of the hoster's catalog.
type GPUClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GPUClassSpec   `json:"spec"`
	Status GPUClassStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// GPUClassList contains a list of GPUClass.
type GPUClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GPUClass `json:"items"`
}

func init() {
	SchemeBuilder.Register(&GPUClass{}, &GPUClassList{})
}
