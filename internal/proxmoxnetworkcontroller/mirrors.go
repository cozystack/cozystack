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

// Minimal mirrors of the Kube-OVN and in-cluster IPAM objects this controller
// reads. Importing github.com/kubeovn/kube-ovn or the IPAM provider module would
// tie this module's k8s.io libraries to theirs (see the same reasoning in
// pkg/apis/sdn/DESIGN.md, section 3.4). Field names and JSON tags match the
// CRDs, and only the fields read here are mirrored.
//
// The controller never Updates a mirrored object, because an Update through a
// partial mirror would drop every field the mirror lacks. It only Creates the
// Vlans it owns in full, and changes anything else with merge patches that
// carry metadata alone.
//
// +kubebuilder:object:generate=true
package proxmoxnetworkcontroller

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	kubeovnGroupVersion = schema.GroupVersion{Group: "kubeovn.io", Version: "v1"}
	ipamGroupVersion    = schema.GroupVersion{Group: "ipam.cluster.x-k8s.io", Version: "v1alpha2"}
)

// AddMirrorsToScheme registers the mirrors.
func AddMirrorsToScheme(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(kubeovnGroupVersion,
		&Vlan{}, &VlanList{},
		&Subnet{}, &SubnetList{},
		&ProviderNetwork{}, &ProviderNetworkList{},
		&VpcEgressGateway{}, &VpcEgressGatewayList{},
	)
	metav1.AddToGroupVersion(scheme, kubeovnGroupVersion)
	scheme.AddKnownTypes(ipamGroupVersion,
		&InClusterIPPool{}, &InClusterIPPoolList{},
	)
	metav1.AddToGroupVersion(scheme, ipamGroupVersion)
	return nil
}

// KubeOVNCondition mirrors kubeovn.io/v1 Condition.
type KubeOVNCondition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

// VlanSpec mirrors kubeovn.io/v1 VlanSpec.
type VlanSpec struct {
	ID       int    `json:"id"`
	Provider string `json:"provider,omitempty"`
}

// VlanStatus mirrors kubeovn.io/v1 VlanStatus.
type VlanStatus struct {
	Subnets  []string `json:"subnets,omitempty"`
	Conflict bool     `json:"conflict,omitempty"`
}

// Vlan mirrors kubeovn.io/v1 Vlan (cluster-scoped).
// +kubebuilder:object:root=true
type Vlan struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              VlanSpec   `json:"spec"`
	Status            VlanStatus `json:"status,omitempty"`
}

// VlanList mirrors kubeovn.io/v1 VlanList.
// +kubebuilder:object:root=true
type VlanList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Vlan `json:"items"`
}

// SubnetSpec mirrors the kubeovn.io/v1 SubnetSpec fields read here.
type SubnetSpec struct {
	Vpc            string   `json:"vpc,omitempty"`
	CIDRBlock      string   `json:"cidrBlock,omitempty"`
	Gateway        string   `json:"gateway,omitempty"`
	ExcludeIps     []string `json:"excludeIps,omitempty"`
	Provider       string   `json:"provider,omitempty"`
	Vlan           string   `json:"vlan,omitempty"`
	LogicalGateway bool     `json:"logicalGateway,omitempty"`
}

// SubnetStatus mirrors the kubeovn.io/v1 SubnetStatus fields read here.
type SubnetStatus struct {
	Conditions []KubeOVNCondition `json:"conditions,omitempty"`
}

// Subnet mirrors kubeovn.io/v1 Subnet (cluster-scoped).
// +kubebuilder:object:root=true
type Subnet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              SubnetSpec   `json:"spec"`
	Status            SubnetStatus `json:"status,omitempty"`
}

// SubnetList mirrors kubeovn.io/v1 SubnetList.
// +kubebuilder:object:root=true
type SubnetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Subnet `json:"items"`
}

// ProviderNetworkStatus mirrors the kubeovn.io/v1 ProviderNetworkStatus fields read here.
type ProviderNetworkStatus struct {
	Ready      bool     `json:"ready,omitempty"`
	ReadyNodes []string `json:"readyNodes,omitempty"`
}

// ProviderNetwork mirrors kubeovn.io/v1 ProviderNetwork (cluster-scoped).
// +kubebuilder:object:root=true
type ProviderNetwork struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Status            ProviderNetworkStatus `json:"status,omitempty"`
}

// ProviderNetworkList mirrors kubeovn.io/v1 ProviderNetworkList.
// +kubebuilder:object:root=true
type ProviderNetworkList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ProviderNetwork `json:"items"`
}

// VpcEgressGatewaySpec mirrors the kubeovn.io/v1 VpcEgressGatewaySpec fields read here.
type VpcEgressGatewaySpec struct {
	VPC string `json:"vpc,omitempty"`
}

// VpcEgressGateway mirrors kubeovn.io/v1 VpcEgressGateway (namespaced).
// +kubebuilder:object:root=true
type VpcEgressGateway struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              VpcEgressGatewaySpec `json:"spec"`
}

// VpcEgressGatewayList mirrors kubeovn.io/v1 VpcEgressGatewayList.
// +kubebuilder:object:root=true
type VpcEgressGatewayList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VpcEgressGateway `json:"items"`
}

// InClusterIPPoolSpec mirrors ipam.cluster.x-k8s.io/v1alpha2 InClusterIPPoolSpec.
type InClusterIPPoolSpec struct {
	Addresses         []string `json:"addresses"`
	Prefix            int      `json:"prefix"`
	Gateway           string   `json:"gateway,omitempty"`
	ExcludedAddresses []string `json:"excludedAddresses,omitempty"`
}

// InClusterIPPoolAddresses mirrors the pool's address counts.
type InClusterIPPoolAddresses struct {
	Total      int `json:"total"`
	Used       int `json:"used"`
	Free       int `json:"free"`
	OutOfRange int `json:"outOfRange"`
}

// InClusterIPPoolStatus mirrors ipam.cluster.x-k8s.io/v1alpha2 InClusterIPPoolStatus.
type InClusterIPPoolStatus struct {
	Addresses *InClusterIPPoolAddresses `json:"ipAddresses,omitempty"`
}

// InClusterIPPool mirrors ipam.cluster.x-k8s.io/v1alpha2 InClusterIPPool.
// +kubebuilder:object:root=true
type InClusterIPPool struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              InClusterIPPoolSpec   `json:"spec"`
	Status            InClusterIPPoolStatus `json:"status,omitempty"`
}

// InClusterIPPoolList mirrors ipam.cluster.x-k8s.io/v1alpha2 InClusterIPPoolList.
// +kubebuilder:object:root=true
type InClusterIPPoolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InClusterIPPool `json:"items"`
}
