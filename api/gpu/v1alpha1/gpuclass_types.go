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
	corev1 "k8s.io/api/core/v1"
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

// OnCordonPolicy decides what happens to an unattached claim on a cordoned node.
// +kubebuilder:validation:Enum=Move;Stay
type OnCordonPolicy string

const (
	// OnCordonMove moves an unattached claim to another node of its class
	// when one has room, keeping its hold.
	OnCordonMove OnCordonPolicy = "Move"
	// OnCordonStay keeps the claim on its node until the cordon is lifted.
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
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/[A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?$`
	// +kubebuilder:validation:XValidation:rule="!self.startsWith('gpu.cozystack.io/')",message="resourceName must not be in gpu.cozystack.io, whose keys are GPU budgets"
	ResourceName string `json:"resourceName"`
}

// GPUClassSource is the backend that serves a class. Exactly one member is
// set. The name "dra" is reserved for a DRA-backed member.
// +kubebuilder:validation:XValidation:rule="[has(self.devicePlugin)].filter(x, x).size() == 1",message="exactly one source must be set; devicePlugin is the only source supported, dra is reserved"
type GPUClassSource struct {
	// +optional
	DevicePlugin *DevicePluginSource `json:"devicePlugin,omitempty"`
}

// GPUClassToleration is a pod toleration that pod validation accepts on
// every cluster: the class's tolerations are copied onto launcher pods, so
// one the API server refuses there would break every start of the class.
// Gt and Lt are left out because pod validation refuses them where their
// feature gate is off.
// +kubebuilder:validation:XValidation:rule="(has(self.key) && size(self.key) > 0) || (has(self.operator) && self.operator == 'Exists')",message="a toleration with an empty key needs operator Exists"
// +kubebuilder:validation:XValidation:rule="!has(self.operator) || self.operator != 'Exists' || !has(self.value) || size(self.value) == 0",message="a toleration with operator Exists takes no value"
// +kubebuilder:validation:XValidation:rule="!has(self.tolerationSeconds) || (has(self.effect) && self.effect == 'NoExecute')",message="tolerationSeconds needs effect NoExecute"
// +kubebuilder:validation:XValidation:rule="!has(self.key) || self.key.indexOf('/') <= 253",message="toleration key prefix must be at most 253 characters"
type GPUClassToleration struct {
	// +optional
	// +kubebuilder:validation:MaxLength=317
	// +kubebuilder:validation:Pattern=`^([a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?[A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?$`
	Key string `json:"key,omitempty"`

	// Operator is Equal when unset.
	// +optional
	// +kubebuilder:validation:Enum=Exists;Equal
	Operator corev1.TolerationOperator `json:"operator,omitempty"`

	// +optional
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^(([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9])?$`
	Value string `json:"value,omitempty"`

	// Effect matches every effect when unset.
	// +optional
	// +kubebuilder:validation:Enum=NoSchedule;PreferNoSchedule;NoExecute
	Effect corev1.TaintEffect `json:"effect,omitempty"`

	// +optional
	TolerationSeconds *int64 `json:"tolerationSeconds,omitempty"`
}

// Toleration returns t as a pod toleration.
func (t GPUClassToleration) Toleration() corev1.Toleration {
	return corev1.Toleration{Key: t.Key, Operator: t.Operator, Value: t.Value, Effect: t.Effect, TolerationSeconds: t.TolerationSeconds}
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

	// Mode cannot change: a pool that names the class takes its worker
	// template from it.
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="mode is immutable; create a new GPUClass instead"
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
	// one node.
	// +optional
	// +kubebuilder:validation:Minimum=1
	MaxCount *int32 `json:"maxCount,omitempty"`

	// OnCordon decides whether an unattached claim leaves a cordoned node.
	// +optional
	// +kubebuilder:default=Move
	OnCordon OnCordonPolicy `json:"onCordon,omitempty"`

	// NodeSelector selects the nodes that serve this class.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Tolerations are the taints of the class's nodes that its VMs
	// tolerate, in the shape of a pod's, operators Exists and Equal only. A
	// node with a NoSchedule or NoExecute taint they do not tolerate serves
	// no claim of the class, and the virt-launcher pod of a VMI gets the
	// tolerations of its claims' classes.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=32
	Tolerations []GPUClassToleration `json:"tolerations,omitempty"`

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
