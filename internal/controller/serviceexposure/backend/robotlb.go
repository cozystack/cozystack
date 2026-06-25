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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	networkv1alpha1 "github.com/cozystack/cozystack/api/network/v1alpha1"
)

// robotlbBackend delegates allocation and announcement to a cloud load
// balancer (Hetzner robotlb watches LoadBalancer Services and provisions
// a Hetzner Cloud LB). The controller renders no in-cluster pool or
// announcer; it only reports the cloud-assigned IP back into status.
type robotlbBackend struct{}

func (robotlbBackend) Name() networkv1alpha1.ExposureBackend {
	return networkv1alpha1.BackendRobotLB
}

func (robotlbBackend) Desired(
	_ *networkv1alpha1.ExposureClass,
	_ []string,
) ([]*unstructured.Unstructured, error) {
	return nil, nil
}

func (robotlbBackend) Observe(svc *corev1.Service) (assignedIPs []string, ready bool, reason string) {
	ips := loadBalancerStatusIPs(svc)
	if len(ips) > 0 {
		return ips, true, ""
	}
	return nil, false, "AwaitingCloudLB"
}

func init() { register(robotlbBackend{}) }
