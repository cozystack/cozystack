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

package backend

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	networkv1alpha1 "github.com/cozystack/cozystack/api/network/v1alpha1"
)

// ciliumBackend allocates VIPs from a CiliumLoadBalancerIPPool (LB-IPAM)
// and announces them on L2 via a CiliumL2AnnouncementPolicy. Both CRs are
// cluster-scoped, class-level (one per ExposureClass), and scoped via a
// serviceSelector to LoadBalancer Services in the class's namespaces.
// Note the two CRs use different apiVersions in Cilium 1.19 (pool: v2,
// L2 policy: v2alpha1).
type ciliumBackend struct{}

func (ciliumBackend) Name() networkv1alpha1.ExposureBackend {
	return networkv1alpha1.BackendCilium
}

func (ciliumBackend) Desired(
	class *networkv1alpha1.ExposureClass,
	namespaces []string,
) ([]*unstructured.Unstructured, error) {
	// No live exposures ⇒ nothing to render (and the controller GCs the
	// class's CRs). Check this BEFORE validating addresses so that deleting
	// the last exposure of a misconfigured (address-less) class still
	// converges to an empty desired set instead of erroring.
	if len(namespaces) == 0 {
		return nil, nil
	}
	if len(class.Spec.Addresses) == 0 {
		return nil, fmt.Errorf("cilium backend requires exposureClass %q to set spec.addresses", class.Name)
	}
	blocks := ciliumBlocks(class.Spec.Addresses)
	if len(blocks) == 0 {
		return nil, fmt.Errorf("cilium backend: exposureClass %q has no usable addresses", class.Name)
	}
	name := PoolName(class.Name)
	selector := namespaceServiceSelector(namespaces)

	pool := newUnstructured(ciliumPoolAPIVersion, "CiliumLoadBalancerIPPool", "", name, map[string]interface{}{
		"blocks":          blocks,
		"serviceSelector": selector,
	})

	objs := []*unstructured.Unstructured{pool}

	if l2Enabled(class) {
		l2Spec := map[string]interface{}{
			"loadBalancerIPs": true,
			"serviceSelector": selector,
		}
		if len(class.Spec.Interfaces) > 0 {
			l2Spec["interfaces"] = toIfaceSlice(class.Spec.Interfaces)
		}
		l2 := newUnstructured(ciliumL2APIVersion, "CiliumL2AnnouncementPolicy", "", name, l2Spec)
		objs = append(objs, l2)
	}

	return objs, nil
}

func (ciliumBackend) Observe(svc *corev1.Service) (assignedIPs []string, ready bool, reason string) {
	ips := loadBalancerStatusIPs(svc)
	if len(ips) > 0 {
		return ips, true, ""
	}
	return nil, false, "AwaitingAllocation"
}

func init() { register(ciliumBackend{}) }
