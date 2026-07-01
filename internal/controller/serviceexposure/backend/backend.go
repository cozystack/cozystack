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

// Package backend implements the pluggable LoadBalancer backends the
// serviceexposure controller dispatches to. Each backend translates an
// ExposureClass (the admin's backend + VIP address scope) into the
// concrete cluster resources that allocate a VIP and announce it.
//
// The pool and announcer are CLASS-level, not per-Service: a VIP address
// range is a property of the ExposureClass, and overlapping pools are
// rejected by Cilium LB-IPAM and double-count IPs in MetalLB. So a backend
// renders exactly ONE pool and ONE announcer per class, scoped via the
// pool/policy's own selector to the set of namespaces that have at least
// one ServiceExposure resolving to the class — never per Service, and
// never by mutating the Service.
//
// The pool/announcer CRs belong to foreign API groups (metallb.io,
// cilium.io) not in the controller's Go scheme, so backends emit them as
// *unstructured.Unstructured. Desired is a PURE function of its inputs (no
// client calls) so it is exhaustively unit-testable.
package backend

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	networkv1alpha1 "github.com/cozystack/cozystack/api/network/v1alpha1"
)

const (
	// MetalLBNamespace is where the cozystack metallb chart installs, and
	// therefore where its IPAddressPool / L2Advertisement CRs must live.
	MetalLBNamespace = "cozy-metallb"

	metallbAPIVersion = "metallb.io/v1beta1"
	// CiliumLoadBalancerIPPool graduated to cilium.io/v2 in Cilium 1.16+,
	// while CiliumL2AnnouncementPolicy is still served at v2alpha1 — the
	// two LB-IPAM CRs use DIFFERENT apiVersions in the same release.
	ciliumPoolAPIVersion = "cilium.io/v2"
	ciliumL2APIVersion   = "cilium.io/v2alpha1"

	// serviceNamespaceSelectorKey is the synthetic label key Cilium LB-IPAM
	// and L2 announcement understand in a serviceSelector to scope a
	// pool/policy to specific namespaces, even though it is not a real
	// label on the Service object.
	serviceNamespaceSelectorKey = "io.kubernetes.service.namespace"
)

// Backend is one LoadBalancer allocation+announcement mechanism. The
// reconciler resolves an ExposureClass to a Backend and delegates.
// Adding a new backend (BGP, another cloud LB) means implementing this
// interface and registering it — no reconciler or API change.
type Backend interface {
	// Name returns the ExposureBackend enum value this backend implements.
	Name() networkv1alpha1.ExposureBackend

	// Desired returns the CLASS-level cluster CRs (pool + announcer) for
	// the class, scoped to the given namespaces — the sorted, unique set
	// of namespaces that have at least one ServiceExposure resolving to
	// the class. An empty namespace set (no live exposures) returns no
	// objects, so the controller garbage-collects the class's CRs. Backends
	// without in-cluster resources (externalIPs, robotlb) always return
	// nil. Returned objects carry GVK, name and spec but NOT management
	// labels/ownership — the reconciler stamps those.
	Desired(class *networkv1alpha1.ExposureClass, namespaces []string) ([]*unstructured.Unstructured, error)

	// Observe reads the externally-assigned addresses and readiness back
	// from a target Service for status reporting.
	Observe(svc *corev1.Service) (assignedIPs []string, ready bool, reason string)
}

// ManagedGVKs are every GroupVersionKind any backend may render. The
// reconciler scans these during garbage collection to reclaim CRs whose
// owning ExposureClass changed backend or lost all its exposures.
func ManagedGVKs() []schema.GroupVersionKind {
	return []schema.GroupVersionKind{
		{Group: "metallb.io", Version: "v1beta1", Kind: "IPAddressPool"},
		{Group: "metallb.io", Version: "v1beta1", Kind: "L2Advertisement"},
		{Group: "cilium.io", Version: "v2", Kind: "CiliumLoadBalancerIPPool"},
		{Group: "cilium.io", Version: "v2alpha1", Kind: "CiliumL2AnnouncementPolicy"},
	}
}

// ManagedSpecKeys returns the top-level spec keys this controller owns for
// a given kind. The reconciler add/updates/deletes ONLY these keys when
// converging an existing object, leaving everything else (notably the
// apiserver's CRD-defaulted keys: metallb autoAssign/avoidBuggyIPs, cilium
// disabled/allowFirstLastIPs) untouched. A managed key absent from a fresh
// render is removed, so clearing e.g. class.spec.interfaces propagates.
func ManagedSpecKeys(kind string) []string {
	switch kind {
	case "IPAddressPool":
		return []string{"addresses", "serviceAllocation"}
	case "L2Advertisement":
		return []string{"ipAddressPools", "interfaces"}
	case "CiliumLoadBalancerIPPool":
		return []string{"blocks", "serviceSelector"}
	case "CiliumL2AnnouncementPolicy":
		return []string{"loadBalancerIPs", "serviceSelector", "interfaces"}
	}
	return nil
}

// PoolName is the deterministic name for the class-level pool/announcer
// CRs. ExposureClass names are cluster-unique DNS-1123 labels, so this is
// globally unique for both the cluster-scoped Cilium CRs and the
// cozy-metallb CRs — no cross-namespace collision is possible.
func PoolName(className string) string {
	return "cozystack-" + className
}

// l2Enabled reports whether L2 announcement should be rendered. The API
// default is true; a nil pointer (e.g. an object built in tests without
// admission defaulting) is treated as true.
func l2Enabled(class *networkv1alpha1.ExposureClass) bool {
	return class.Spec.L2 == nil || *class.Spec.L2
}

// namespaceServiceSelector builds a label selector that scopes a Cilium
// pool/policy to LoadBalancer Services in the given namespaces, via the
// synthetic io.kubernetes.service.namespace key Cilium understands.
func namespaceServiceSelector(namespaces []string) map[string]interface{} {
	return map[string]interface{}{
		"matchExpressions": []interface{}{
			map[string]interface{}{
				"key":      serviceNamespaceSelectorKey,
				"operator": "In",
				"values":   toIfaceSlice(namespaces),
			},
		},
	}
}

// toIfaceSlice converts a string slice to []interface{} for unstructured.
func toIfaceSlice(in []string) []interface{} {
	out := make([]interface{}, 0, len(in))
	for _, s := range in {
		out = append(out, s)
	}
	return out
}

// ciliumBlocks converts class addresses (CIDR or "start-stop" range form)
// into CiliumLoadBalancerIPPool.spec.blocks entries.
func ciliumBlocks(addresses []string) []interface{} {
	blocks := make([]interface{}, 0, len(addresses))
	for _, a := range addresses {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		// A range "start-stop" has a dash and no slash; a CIDR has a slash.
		if strings.Contains(a, "-") && !strings.Contains(a, "/") {
			parts := strings.SplitN(a, "-", 2)
			blocks = append(blocks, map[string]interface{}{
				"start": strings.TrimSpace(parts[0]),
				"stop":  strings.TrimSpace(parts[1]),
			})
			continue
		}
		blocks = append(blocks, map[string]interface{}{"cidr": a})
	}
	return blocks
}

// newUnstructured constructs a typed-but-unstructured object with its GVK
// and metadata set. Namespace is left empty for cluster-scoped kinds.
func newUnstructured(apiVersion, kind, namespace, name string, spec map[string]interface{}) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata":   map[string]interface{}{"name": name},
		"spec":       spec,
	}}
	if namespace != "" {
		obj.SetNamespace(namespace)
	}
	return obj
}

// loadBalancerStatusIPs extracts assigned IPs from a Service's
// status.loadBalancer.ingress (cloud / pool backends report here).
func loadBalancerStatusIPs(svc *corev1.Service) []string {
	var ips []string
	for _, ing := range svc.Status.LoadBalancer.Ingress {
		switch {
		case ing.IP != "":
			ips = append(ips, ing.IP)
		case ing.Hostname != "":
			ips = append(ips, ing.Hostname)
		}
	}
	return ips
}
