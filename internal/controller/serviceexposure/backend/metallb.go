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

// metallbBackend allocates VIPs from a metallb.io IPAddressPool and
// announces them on L2 via an L2Advertisement. Both CRs are class-level
// (one per ExposureClass) and live in the cozy-metallb namespace (where
// the metallb chart installs). The pool is scoped via serviceAllocation
// to the class's namespaces, so it is not cluster-wide.
type metallbBackend struct{}

func (metallbBackend) Name() networkv1alpha1.ExposureBackend {
	return networkv1alpha1.BackendMetalLB
}

func (metallbBackend) Desired(
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
		return nil, fmt.Errorf("metallb backend requires exposureClass %q to set spec.addresses", class.Name)
	}
	name := PoolName(class.Name)

	pool := newUnstructured(metallbAPIVersion, "IPAddressPool", MetalLBNamespace, name, map[string]interface{}{
		"addresses": toIfaceSlice(class.Spec.Addresses),
		// Scope allocation to the class's namespaces so this pool only
		// serves LoadBalancer Services in namespaces that opted in via a
		// ServiceExposure — symmetric with the Cilium backend.
		"serviceAllocation": map[string]interface{}{
			"namespaces": toIfaceSlice(namespaces),
		},
	})

	objs := []*unstructured.Unstructured{pool}

	if l2Enabled(class) {
		l2Spec := map[string]interface{}{
			"ipAddressPools": []interface{}{name},
		}
		if len(class.Spec.Interfaces) > 0 {
			l2Spec["interfaces"] = toIfaceSlice(class.Spec.Interfaces)
		}
		l2 := newUnstructured(metallbAPIVersion, "L2Advertisement", MetalLBNamespace, name, l2Spec)
		objs = append(objs, l2)
	}

	return objs, nil
}

func (metallbBackend) Observe(svc *corev1.Service) (assignedIPs []string, ready bool, reason string) {
	ips := loadBalancerStatusIPs(svc)
	if len(ips) > 0 {
		return ips, true, ""
	}
	return nil, false, "AwaitingAllocation"
}

func init() { register(metallbBackend{}) }
