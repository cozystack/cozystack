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

// IsDefaultExposureClassAnnotation marks an ExposureClass as the
// cluster default, mirroring the storageclass.kubernetes.io/is-default-class
// convention. A ServiceExposure with an empty exposureClassName resolves
// to the ExposureClass carrying this annotation set to "true".
const IsDefaultExposureClassAnnotation = "exposureclass.network.cozystack.io/is-default-class"

// ExposureBackend names the cluster-side mechanism that allocates a
// LoadBalancer IP and announces it on the network. A backend covers BOTH
// allocation and announcement: for bare-metal the VIP pool and the L2
// announcer are inseparable.
// +kubebuilder:validation:Enum=externalIPs;metallb;cilium;robotlb
type ExposureBackend string

const (
	// BackendExternalIPs pins the Service to operator-supplied addresses
	// via Service.spec.externalIPs (typically node public IPs). No pool
	// and no announcer are rendered — the node already owns the address.
	// This is the default and reproduces the historical behaviour.
	BackendExternalIPs ExposureBackend = "externalIPs"

	// BackendMetalLB allocates from a metallb.io IPAddressPool and
	// announces via an L2Advertisement, both rendered by the controller
	// in the cozy-metallb namespace.
	BackendMetalLB ExposureBackend = "metallb"

	// BackendCilium allocates from a CiliumLoadBalancerIPPool and
	// announces via a CiliumL2AnnouncementPolicy, both rendered by the
	// controller (cluster-scoped).
	BackendCilium ExposureBackend = "cilium"

	// BackendRobotLB delegates allocation and announcement to a cloud
	// load balancer (e.g. Hetzner robotlb). The controller renders no
	// in-cluster pool or announcer; it only reports the assigned IP.
	BackendRobotLB ExposureBackend = "robotlb"
)

// ExposureClassSpec is the cluster-admin backend configuration. It maps a
// logical exposure class to a concrete backend and its address scope,
// following the StorageClass / IngressClass / GatewayClass pattern: the
// admin owns the backend choice, tenants reference the class by name.
type ExposureClassSpec struct {
	// Backend selects the concrete allocation+announcement mechanism.
	// +required
	Backend ExposureBackend `json:"backend"`

	// Addresses is the VIP address scope for pool-based backends (metallb,
	// cilium), as CIDRs or start-end ranges. These are virtual VIPs, NOT
	// node public IPs. Ignored by the externalIPs and robotlb backends.
	// +optional
	Addresses []string `json:"addresses,omitempty"`

	// L2 enables L2 (ARP/NDP) announcement for bare-metal backends
	// (metallb, cilium). Defaults to true. Set false when announcement is
	// handled out of band (e.g. BGP added later, or a routed network).
	// +kubebuilder:default=true
	// +optional
	L2 *bool `json:"l2,omitempty"`

	// Interfaces optionally scopes L2 announcement to the named host
	// network interfaces (cilium CiliumL2AnnouncementPolicy.spec.interfaces;
	// metallb L2Advertisement.spec.interfaces). Empty announces on all.
	// +optional
	Interfaces []string `json:"interfaces,omitempty"`
}

// ExposureClassStatus reports the observed state of the ExposureClass.
type ExposureClassStatus struct {
	// ObservedGeneration mirrors the .metadata.generation reflected in the
	// latest reconciled state.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions describes the current state of the ExposureClass.
	// Standard condition type: Ready.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=expcls
// +kubebuilder:printcolumn:name="Backend",type="string",JSONPath=".spec.backend"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// ExposureClass is the Schema for the exposureclasses API. It is the
// cluster-admin-facing configuration that binds a logical class name to a
// LoadBalancer backend and its address scope. Tenants reference it by name
// from a ServiceExposure; the cozystack-controller renders the backend's
// pool and announcer resources from this CR.
type ExposureClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ExposureClassSpec   `json:"spec,omitempty"`
	Status ExposureClassStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ExposureClassList contains a list of ExposureClass.
type ExposureClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ExposureClass `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ExposureClass{}, &ExposureClassList{})
}
