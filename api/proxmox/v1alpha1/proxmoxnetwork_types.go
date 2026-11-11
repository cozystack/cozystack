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
			&ProxmoxNetwork{},
			&ProxmoxNetworkList{},
		)
		return nil
	})
}

const (
	// Finalizer is set on the ProxmoxNetwork, its Kube-OVN Subnet and its
	// Kube-OVN Vlan. It is removed only once the network's IP pool holds no
	// allocated address, so a VLAN is never handed to another network while a
	// VM still sits on it.
	Finalizer = "proxmox.cozystack.io/network"

	// LabelZone marks Kube-OVN Vlans allocated from a zone.
	LabelZone = "proxmox.cozystack.io/zone"
	// LabelNetworkNamespace and LabelNetworkName point a Kube-OVN Vlan back at
	// the ProxmoxNetwork that holds it.
	LabelNetworkNamespace = "proxmox.cozystack.io/network-namespace"
	LabelNetworkName      = "proxmox.cozystack.io/network-name"

	// AnnotationNamespaceNetworks is maintained on every namespace that owns a
	// ready ProxmoxNetwork. Its value is a comma-separated, sorted list of
	// "<bridge>/<vlan>/<pool>" triples. Admission policy reads it to check that
	// a ProxmoxMachine only uses networks of its own namespace.
	AnnotationNamespaceNetworks = "proxmox.cozystack.io/networks"

	// AnnotationDeletedSubnets is kept by the controller on a zone's Kube-OVN
	// Vlan whose network is being deleted or already gone, while Kube-OVN
	// still lists deleted subnets in the Vlan's status.subnets. Its value is a
	// JSON object that maps each such subnet to when the controller first saw
	// it gone (RFC 3339, UTC). Each one holds the VLAN until Kube-OVN prunes
	// it, or for at most the prune timeout from that time, so a subnet deleted
	// later gets its own full wait. A network recreated under the same name
	// drops the annotation when it adopts the Vlan, and a deleting network
	// restarts any time in it from before its own deletion.
	AnnotationDeletedSubnets = "proxmox.cozystack.io/deleted-subnets"

	// Condition types.
	ConditionVLANAllocated = "VLANAllocated"
	ConditionSubnetReady   = "SubnetReady"
	ConditionIPPoolReady   = "IPPoolReady"
	ConditionReady         = "Ready"
	// ConditionSwitchDeleted appears only on a network being deleted. It is
	// False while Kube-OVN still lists a deleted subnet on the network's Vlan,
	// which it stops doing only once it has deleted that subnet's logical
	// switch. Its lastTransitionTime marks when the network began to wait.
	// The timeout after which the VLAN is released anyway runs per subnet,
	// from the times in AnnotationDeletedSubnets on the Vlan.
	ConditionSwitchDeleted = "SwitchDeleted"
	// ConditionGatewayReady is True once the subnet's VPC router port has
	// an OVN HA chassis group of the zone's trunk nodes, the only way a VM on
	// the VLAN gets ARP answers for its gateway: Kube-OVN's OVN drops ARP
	// requests for a router port's addresses that arrive through a localnet
	// port unless the router port is a distributed gateway port. While the
	// controller manages these groups, Ready requires it; with management
	// off it is Unknown with reason GatewayReasonDisabled and Ready ignores
	// it. While the OVN NB cannot be reached, a network whose gateway was up,
	// or that was Ready when management was switched on, keeps Ready for ten
	// minutes: GatewayReady stays True, or is Unknown with reason
	// GatewayReasonNBUnavailable, which Ready does not wait for.
	ConditionGatewayReady = "GatewayReady"

	// Reasons of ConditionGatewayReady.
	GatewayReasonChassisAssigned   = "ChassisAssigned"
	GatewayReasonNoTrunkNodes      = "NoTrunkNodes"
	GatewayReasonRouterPortMissing = "RouterPortMissing"
	GatewayReasonNBUnavailable     = "NBUnavailable"
	// GatewayReasonForeignGroup: the group of that name, or the group or
	// gateway_chassis the router port already uses, is someone else's.
	GatewayReasonForeignGroup = "ForeignGroup"
	GatewayReasonDisabled     = "Disabled"

	// GatewayGroupPrefix starts the name of the OVN NB HA_Chassis_Group the
	// controller keeps for a network's router port: "px-<vpc>-<subnet>".
	GatewayGroupPrefix = "px-"
	// GatewayOwner is the value of external_ids:owner on those groups. The
	// controller changes and deletes only groups that carry it, and adopts a
	// group of the right name that has no owner at all.
	GatewayOwner = "proxmox-network"
	// GatewayExternalIDOwner and GatewayExternalIDNetwork are the
	// external_ids keys on those groups; the network is "<namespace>/<name>".
	GatewayExternalIDOwner   = "owner"
	GatewayExternalIDNetwork = "network"
)

// ProxmoxNetworkSpec binds one VPC subnet to a zone. The vpc chart writes it;
// tenants only read it.
// +kubebuilder:validation:XValidation:rule="self.zone == oldSelf.zone",message="zone is immutable"
// +kubebuilder:validation:XValidation:rule="self.subnet == oldSelf.subnet",message="subnet is immutable"
type ProxmoxNetworkSpec struct {
	// Zone is the ProxmoxNetworkZone the VLAN is allocated from.
	// +kubebuilder:validation:MinLength=1
	Zone string `json:"zone"`

	// Subnet is the Kube-OVN Subnet this network extends onto Proxmox. Its
	// spec.vlan must name a Vlan with the same name as the Subnet, which the
	// controller creates.
	// +kubebuilder:validation:MinLength=1
	Subnet string `json:"subnet"`

	// IPPool is the InClusterIPPool in this namespace that Proxmox VMs on the
	// subnet draw addresses from. Its addresses must lie inside the subnet CIDR
	// and inside the subnet's excludeIps, so Kube-OVN never hands them to pods.
	// +kubebuilder:validation:MinLength=1
	IPPool string `json:"ipPool"`
}

// IPPoolUsage mirrors the address counts of the InClusterIPPool.
type IPPoolUsage struct {
	Total int32 `json:"total"`
	Used  int32 `json:"used"`
	Free  int32 `json:"free"`
}

// ProxmoxNetworkStatus is what a consumer needs to attach a VM.
type ProxmoxNetworkStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// VLAN is the allocated 802.1Q tag.
	// +optional
	VLAN int32 `json:"vlan,omitempty"`
	// VLANName is the Kube-OVN Vlan object that carries the tag.
	// +optional
	VLANName string `json:"vlanName,omitempty"`
	// Bridge is the Proxmox bridge VM NICs attach to.
	// +optional
	Bridge string `json:"bridge,omitempty"`
	// MTU for VM NICs, when the zone sets one.
	// +optional
	MTU *int32 `json:"mtu,omitempty"`
	// CIDR, Gateway and Prefix of the subnet.
	// +optional
	CIDR string `json:"cidr,omitempty"`
	// +optional
	Gateway string `json:"gateway,omitempty"`
	// +optional
	Prefix int32 `json:"prefix,omitempty"`
	// IPPool usage.
	// +optional
	IPPool *IPPoolUsage `json:"ipPool,omitempty"`
	// Conditions: VLANAllocated, SubnetReady, IPPoolReady, GatewayReady,
	// Ready, and SwitchDeleted while the network is being deleted.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ProxmoxNetwork maps one VPC subnet onto a Proxmox VLAN.
// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=pxnet
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Zone",type=string,JSONPath=`.spec.zone`
// +kubebuilder:printcolumn:name="VLAN",type=integer,JSONPath=`.status.vlan`
// +kubebuilder:printcolumn:name="Bridge",type=string,JSONPath=`.status.bridge`
// +kubebuilder:printcolumn:name="CIDR",type=string,JSONPath=`.status.cidr`
// +kubebuilder:printcolumn:name="Used",type=integer,JSONPath=`.status.ipPool.used`
// +kubebuilder:printcolumn:name="Free",type=integer,JSONPath=`.status.ipPool.free`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`,priority=1
type ProxmoxNetwork struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ProxmoxNetworkSpec   `json:"spec,omitempty"`
	Status ProxmoxNetworkStatus `json:"status,omitempty"`
}

// ProxmoxNetworkList contains a list of ProxmoxNetwork.
// +kubebuilder:object:root=true
type ProxmoxNetworkList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ProxmoxNetwork `json:"items"`
}
