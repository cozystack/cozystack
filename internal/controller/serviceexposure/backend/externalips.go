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

// externalIPsBackend pins the Service to operator-supplied addresses via
// Service.spec.externalIPs. The node already owns the address, so there
// is no pool to allocate and no announcer to render — this backend
// produces zero cluster resources and exists to preserve the historical
// default behaviour while keeping the API uniform across backends. The
// Service's own spec.externalIPs is set by the chart that renders it, not
// by this controller.
type externalIPsBackend struct{}

func (externalIPsBackend) Name() networkv1alpha1.ExposureBackend {
	return networkv1alpha1.BackendExternalIPs
}

func (externalIPsBackend) Desired(
	_ *networkv1alpha1.ExposureClass,
	_ []string,
) ([]*unstructured.Unstructured, error) {
	return nil, nil
}

func (externalIPsBackend) Observe(svc *corev1.Service) (assignedIPs []string, ready bool, reason string) {
	if len(svc.Spec.ExternalIPs) > 0 {
		return append([]string(nil), svc.Spec.ExternalIPs...), true, ""
	}
	return nil, false, "NoExternalIPs"
}

func init() { register(externalIPsBackend{}) }
