/*
Copyright 2025 The Cozystack Authors.

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
	"k8s.io/apimachinery/pkg/runtime"
)

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(GroupVersion,
			&ProxmoxNetworkZone{},
			&ProxmoxNetworkZoneList{},
		)
		return nil
	})
}

// ProxmoxNetworkZoneSpec describes one Proxmox transport domain: the bridge VMs
// attach to, the VLANs the allocator may hand out on it, and the Kube-OVN
// provider network that carries those VLANs to the cluster nodes.
type ProxmoxNetworkZoneSpec struct {
	// Bridge is the VLAN-aware Proxmox bridge every VM NIC of this zone attaches
	// to. It must exist with the same name on every Proxmox node VMs may be
	// placed on, and must pass every VLAN in VLANRanges.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=15
	// +kubebuilder:validation:Pattern=`^[a-zA-Z][a-zA-Z0-9_.-]*$`
	Bridge string `json:"bridge"`

	// ProviderNetwork is the Kube-OVN ProviderNetwork whose interface is the
	// trunk carrying this zone's VLANs to the cluster nodes.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=12
	ProviderNetwork string `json:"providerNetwork"`

	// VLANRanges lists the VLAN IDs the allocator may assign, as single IDs
	// ("120") or inclusive ranges ("101-199"). Shrinking a range never takes a
	// VLAN away from a network that already holds it; the zone reports the
	// stranded allocation instead.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:items:Pattern=`^[0-9]{1,4}(-[0-9]{1,4})?$`
	VLANRanges []string `json:"vlanRanges"`

	// ReservedVLANs are never assigned, even inside VLANRanges.
	// +optional
	// +kubebuilder:validation:items:Minimum=1
	// +kubebuilder:validation:items:Maximum=4094
	ReservedVLANs []int32 `json:"reservedVLANs,omitempty"`

	// MTU written into every VM NIC of the zone. Unset keeps the Proxmox
	// default.
	// +optional
	// +kubebuilder:validation:Minimum=576
	// +kubebuilder:validation:Maximum=65520
	MTU *int32 `json:"mtu,omitempty"`

	// NamespaceSelector limits which namespaces may bind networks to this zone.
	// Unset admits every namespace.
	// +optional
	NamespaceSelector *metav1.LabelSelector `json:"namespaceSelector,omitempty"`

	// StaticAssignments pins a VLAN to one ProxmoxNetwork. This is the only way
	// a specific VLAN ID reaches a network, and only an infrastructure admin can
	// write it. The VLAN does not have to be inside VLANRanges, but it may not be
	// reserved or assigned to another network.
	// +optional
	// +listType=map
	// +listMapKey=namespace
	// +listMapKey=name
	StaticAssignments []StaticVLANAssignment `json:"staticAssignments,omitempty"`

	// Egress names the platform transit network VPC egress gateways of this
	// zone attach to.
	// +optional
	Egress *ZoneEgress `json:"egress,omitempty"`
}

// StaticVLANAssignment pins a VLAN to a ProxmoxNetwork.
type StaticVLANAssignment struct {
	// Namespace of the ProxmoxNetwork.
	Namespace string `json:"namespace"`
	// Name of the ProxmoxNetwork.
	Name string `json:"name"`
	// VLAN to assign.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4094
	VLAN int32 `json:"vlan"`
}

// ZoneEgress describes the transit network of a zone.
type ZoneEgress struct {
	// ExternalSubnet is the Kube-OVN subnet VpcEgressGateways attach their
	// external interface to.
	// +kubebuilder:validation:MinLength=1
	ExternalSubnet string `json:"externalSubnet"`

	// GatewayNamespace is the platform namespace the VPC egress gateways of
	// this zone run in. Gateway pods need NET_ADMIN and a privileged init
	// container, which a tenant namespace's Pod Security level refuses, so
	// they never run next to the tenant's workloads.
	// +kubebuilder:validation:MinLength=1
	GatewayNamespace string `json:"gatewayNamespace"`
}

// ProxmoxNetworkZoneStatus reports allocation state.
type ProxmoxNetworkZoneStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions: Ready.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// TotalVLANs is the size of VLANRanges minus ReservedVLANs.
	// +optional
	TotalVLANs int32 `json:"totalVLANs"`
	// AllocatedVLANs counts VLANs held by networks of this zone.
	// +optional
	AllocatedVLANs int32 `json:"allocatedVLANs"`
	// FreeVLANs counts VLANs still available for allocation.
	// +optional
	FreeVLANs int32 `json:"freeVLANs"`
	// Networks counts ProxmoxNetworks bound to this zone.
	// +optional
	Networks int32 `json:"networks"`
}

// ProxmoxNetworkZone is an infrastructure-owned Proxmox transport domain.
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=pxzone
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Bridge",type=string,JSONPath=`.spec.bridge`
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=`.spec.providerNetwork`
// +kubebuilder:printcolumn:name="Allocated",type=integer,JSONPath=`.status.allocatedVLANs`
// +kubebuilder:printcolumn:name="Free",type=integer,JSONPath=`.status.freeVLANs`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type ProxmoxNetworkZone struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ProxmoxNetworkZoneSpec   `json:"spec,omitempty"`
	Status ProxmoxNetworkZoneStatus `json:"status,omitempty"`
}

// ProxmoxNetworkZoneList contains a list of ProxmoxNetworkZone.
// +kubebuilder:object:root=true
type ProxmoxNetworkZoneList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ProxmoxNetworkZone `json:"items"`
}
