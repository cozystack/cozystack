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

// NodeGroupOS is the operating system of a worker pool.
// +kubebuilder:validation:Enum=talos;kairos
type NodeGroupOS string

const (
	NodeGroupOSTalos  NodeGroupOS = "talos"
	NodeGroupOSKairos NodeGroupOS = "kairos"
)

// The image rule is written as CEL rather than as a required field so that a
// second source (a worker image catalog entry) can join it as an exactly-one
// rule without loosening a required field.

// NodeGroupClassImage is the worker image of a Kairos class.
// +kubebuilder:validation:XValidation:rule="has(self.reference)",message="image.reference must be set"
type NodeGroupClassImage struct {
	// KubernetesVersion is the exact Kubernetes release of kubelet and kubeadm
	// in the image. A pool on this class runs its Machines at this version, so
	// its minor must equal the pool's version.
	// +required
	// +kubebuilder:validation:Pattern=`^v[0-9]+\.[0-9]+\.[0-9]+$`
	KubernetesVersion string `json:"kubernetesVersion"`

	// Reference is the container disk holding the worker OS, as a registry
	// reference without a scheme, preferably pinned by digest. CDI imports it
	// into each worker's system disk without credentials, so it must be
	// publicly pullable.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	// +kubebuilder:validation:Pattern=`^[^\s:/]+(:[0-9]+)?(/[^\s/]+)+$`
	Reference string `json:"reference,omitempty"`
}

// NodeGroupClassSpec is immutable as a whole: a new image or version is a new
// class, so no edit can roll every pool that uses one.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable; create a new NodeGroupClass instead"
// +kubebuilder:validation:XValidation:rule="self.os == 'kairos' || !has(self.image)",message="image is allowed only with os kairos"
type NodeGroupClassSpec struct {
	// OS is the operating system of the workers of every pool on this class.
	// +required
	OS NodeGroupOS `json:"os"`

	// Image is the worker image of a Kairos class. Omitted, a pool boots the
	// platform image for its Kubernetes version.
	// +optional
	Image *NodeGroupClassImage `json:"image,omitempty"`
}

// The chart resolves the classes named talos and kairos without reading them,
// so an object under either name with other content would be silently
// ignored. The rule sits on the root because CEL sees metadata.name only there.

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:validation:XValidation:rule="!(self.metadata.name in ['talos', 'kairos']) || (self.spec.os == self.metadata.name && !has(self.spec.image))",message="the built-in classes talos and kairos carry only os, equal to their name"
// +kubebuilder:printcolumn:name="OS",type="string",JSONPath=".spec.os"
// +kubebuilder:printcolumn:name="Kubernetes",type="string",JSONPath=".spec.image.kubernetesVersion"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// NodeGroupClass selects the worker OS, and for Kairos the worker image, of
// the KubernetesNodes pools that name it in nodeGroupClassName. Classes are
// created by the platform operator; the platform ships talos and kairos.
type NodeGroupClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec NodeGroupClassSpec `json:"spec"`
}

// +kubebuilder:object:root=true

// NodeGroupClassList contains a list of NodeGroupClass.
type NodeGroupClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NodeGroupClass `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NodeGroupClass{}, &NodeGroupClassList{})
}
