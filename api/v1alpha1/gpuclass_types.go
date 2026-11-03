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

// GPUClass condition types.
const (
	// GPUClassResourceNameConflict is set on a class whose resource name
	// an older class already has. Such a class is not offered.
	GPUClassResourceNameConflict = "ResourceNameConflict"
	// GPUClassResourceNameNotPermitted is set on a class whose resource
	// name the KubeVirt permittedHostDevices table leaves out.
	GPUClassResourceNameNotPermitted = "ResourceNameNotPermitted"
	// GPUClassLicenseSecretMissing is set on a class whose license Secret
	// is missing or has no client configuration token.
	GPUClassLicenseSecretMissing = "LicenseSecretMissing"
)

// DevicePluginSource serves a class through a Kubernetes device plugin.
type DevicePluginSource struct {
	// ResourceName is the extended resource the device plugin advertises for
	// this class's GPUs. A class whose resource name an older class already
	// has is not offered.
	// +required
	// +kubebuilder:validation:MinLength=3
	// +kubebuilder:validation:MaxLength=308
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/[A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?$`
	// +kubebuilder:validation:XValidation:rule="!self.startsWith('gpuclass.cozystack.io/') && !self.contains('.gpuclass.cozystack.io/')",message="resourceName must not be in gpuclass.cozystack.io, whose keys are GPU budgets"
	// +kubebuilder:validation:XValidation:rule="!self.startsWith('requests.') && !self.contains('kubernetes.io/') && self.indexOf('/') <= 244",message="resourceName must be an extended resource name: outside kubernetes.io, without the requests. prefix, and with a domain of at most 244 characters"
	ResourceName string `json:"resourceName"`
}

// GPUClassSource is the backend that serves a class. Exactly one member is
// set. The name "dra" is reserved for a DRA-backed member.
// +kubebuilder:validation:XValidation:rule="[has(self.devicePlugin)].filter(x, x).size() == 1",message="exactly one source must be set; devicePlugin is the only source supported, dra is reserved"
// +kubebuilder:validation:XValidation:rule="has(self.devicePlugin) == has(oldSelf.devicePlugin)",message="source kind is immutable; create a new GPUClass instead"
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
	// +kubebuilder:validation:Pattern=`^(([a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?[A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?)?$`
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

// GPUClassLicense is how a VM of a vGPU class gets licensed: the platform
// copies the Secret into every namespace entitled to vGPU, and each VM of
// the class mounts the copy.
type GPUClassLicense struct {
	// SecretName names a Secret in cozy-system whose key
	// client_configuration_token.tok holds the client configuration token
	// of the hoster's NVIDIA license server.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	SecretName string `json:"secretName"`

	// FeatureType is the FeatureType the guest's license client reads from
	// gridd.conf: 1 lets the driver pick the license from the vGPU type,
	// 4 is NVIDIA vGPU for Compute.
	// +optional
	// +kubebuilder:default=1
	// +kubebuilder:validation:Enum=1;4
	FeatureType int32 `json:"featureType,omitempty"`
}

// GPUClassSpec defines one GPU offer of the hoster's catalog. What makes
// the offer never changes, so a class name stands for one offer for its
// lifetime.
// +kubebuilder:validation:XValidation:rule="self.mode == 'vgpu' ? has(self.profile) : !has(self.profile)",message="profile is required for mode vgpu and not allowed for mode passthrough"
// +kubebuilder:validation:XValidation:rule="!has(self.license) || self.mode == 'vgpu'",message="license is allowed only for mode vgpu"
// +kubebuilder:validation:XValidation:rule="has(self.memory) == has(oldSelf.memory) && (!has(self.memory) || quantity(string(self.memory)).compareTo(quantity(string(oldSelf.memory))) == 0)",message="memory is immutable; create a new GPUClass instead"
type GPUClassSpec struct {
	// DisplayName is the human-readable name of the offer.
	// +optional
	// +kubebuilder:validation:MaxLength=256
	DisplayName string `json:"displayName,omitempty"`

	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="vendor is immutable; create a new GPUClass instead"
	Vendor string `json:"vendor"`

	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="model is immutable; create a new GPUClass instead"
	Model string `json:"model"`

	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="mode is immutable; create a new GPUClass instead"
	Mode GPUMode `json:"mode"`

	// Profile is the vGPU profile, such as L40S-24C. Set only for mode vgpu.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="profile is immutable; create a new GPUClass instead"
	Profile string `json:"profile,omitempty"`

	// License is set only for mode vgpu. A change reaches each VM at its
	// next start.
	// +optional
	License *GPUClassLicense `json:"license,omitempty"`

	// Memory is the GPU memory of one unit of the class.
	// +optional
	// +kubebuilder:validation:XValidation:rule="quantity(string(self)).isGreaterThan(quantity('0'))",message="memory must be positive"
	Memory *resource.Quantity `json:"memory,omitempty"`

	// MaxCount is how many GPUs of this class one VM may take. Unset means 1
	// for mode vgpu and, for mode passthrough, no limit beyond the GPUs of
	// one node.
	// +optional
	// +kubebuilder:validation:Minimum=1
	MaxCount *int32 `json:"maxCount,omitempty"`

	// Tolerations are the taints of the class's nodes that its VMs
	// tolerate, in the shape of a pod's, operators Exists and Equal only.
	// The virt-launcher pod of a VM that names the class gets them, and a
	// node with a NoSchedule or NoExecute taint they do not tolerate adds
	// nothing to the class's free units.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=32
	Tolerations []GPUClassToleration `json:"tolerations,omitempty"`

	// +required
	Source GPUClassSource `json:"source"`
}

// GPUClassStatus is readable by every authenticated user, so it carries no
// node names and no counts beyond free units.
type GPUClassStatus struct {
	// Free is the number of free units of the class over its nodes that
	// are Ready, not cordoned and have no NoSchedule or NoExecute taint
	// the class does not tolerate. Absent until the controller has
	// computed it.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Free *int32 `json:"free,omitempty"`

	// MaxFreePerNode is the largest number of free units on one node.
	// +optional
	// +kubebuilder:validation:Minimum=0
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
