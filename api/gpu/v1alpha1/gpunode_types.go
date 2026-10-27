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

// PCIFunction is the role of a PCI function in GPU virtualization.
// +kubebuilder:validation:Enum=pf;vf;whole
type PCIFunction string

const (
	// PCIFunctionPF is an SR-IOV physical function that carries VFs.
	PCIFunctionPF PCIFunction = "pf"
	// PCIFunctionVF is an SR-IOV virtual function, one vGPU.
	PCIFunctionVF PCIFunction = "vf"
	// PCIFunctionWhole is a GPU served whole, without VFs.
	PCIFunctionWhole PCIFunction = "whole"
)

// GPUPodReference names the pod that uses a device.
type GPUPodReference struct {
	// +required
	Namespace string `json:"namespace"`
	// +required
	Pod string `json:"pod"`
}

// GPUNodeDevice is one PCI function of a GPU.
type GPUNodeDevice struct {
	// +required
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-7]$`
	PCIAddress string `json:"pciAddress"`

	// +optional
	Model string `json:"model,omitempty"`

	// +required
	Function PCIFunction `json:"function"`

	// VFs is the number of VFs a physical function carries.
	// +optional
	VFs int32 `json:"vfs,omitempty"`

	// Profile is the vGPU profile written to a VF.
	// +optional
	Profile string `json:"profile,omitempty"`

	// ResourceName is the device-plugin resource that advertises this
	// function, as kubelet's pod-resources API reports it.
	// +optional
	ResourceName string `json:"resourceName,omitempty"`

	// Healthy is true when kubelet lists every device-plugin device of the
	// function as allocatable, and false when an unfinished pod holds one
	// that it does not. Absent for a function no device plugin advertises.
	// +optional
	Healthy *bool `json:"healthy,omitempty"`

	// Allocatable is the number of this function's device-plugin devices
	// that kubelet reports as allocatable for ResourceName: 1 for a VF or a
	// function passed through whole, N for an mdev parent. A device that
	// names a whole IOMMU group counts once, on the group's GPU function
	// with the lowest PCI address, and 0 on its other GPU functions.
	// Absent when the agent mapped no device-plugin device to this
	// function.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Allocatable *int32 `json:"allocatable,omitempty"`

	// +optional
	UsedBy *GPUPodReference `json:"usedBy,omitempty"`
}

// GPUNodeStatus is the GPU inventory of one node, written by the node agent.
type GPUNodeStatus struct {
	// +optional
	// +listType=map
	// +listMapKey=pciAddress
	Devices []GPUNodeDevice `json:"devices,omitempty"`

	// ResourceNames are the device-plugin resource names on this node whose
	// devices are GPU PCI functions. They join the cluster's GPU
	// resource-name set.
	// +optional
	// +listType=set
	ResourceNames []string `json:"resourceNames,omitempty"`

	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:metadata:annotations="helm.sh/resource-policy=keep"
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// GPUNode is the GPU inventory of the node of the same name. It is owned by
// that Node and goes away with it.
type GPUNode struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Status GPUNodeStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// GPUNodeList contains a list of GPUNode.
type GPUNodeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GPUNode `json:"items"`
}

func init() {
	SchemeBuilder.Register(&GPUNode{}, &GPUNodeList{})
}
