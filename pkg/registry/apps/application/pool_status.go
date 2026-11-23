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

package application

import (
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/reservation"
)

// projectPoolStatus copies the pool figures the tenant-quota controller
// published on a Tenant's release into the Tenant's status, with the pool's
// conditions next to the release's own. A release without them, because the
// Tenant roots no pool or the reservation is off, projects nothing.
func projectPoolStatus(app *appsv1alpha1.Application, hr *helmv2.HelmRelease) {
	s, err := reservation.ReadPoolStatus(hr.Annotations)
	if err != nil {
		klog.Warningf("ignoring an unreadable pool status on HelmRelease %s/%s: %v", hr.Namespace, hr.Name, err)
		return
	}
	if s == nil {
		return
	}
	app.Status.Reserved = toResourceList(s.Reserved)
	app.Status.CarvedOut = toResourceList(s.CarvedOut)
	app.Status.Budget = toResourceList(s.Budget)
	for _, c := range s.Conditions {
		if c.Type != reservation.ConditionOvercommitted && c.Type != reservation.ConditionReservationIncomplete {
			continue
		}
		app.Status.Conditions = append(app.Status.Conditions, c)
	}
}

func toResourceList(rl reservation.ResourceList) corev1.ResourceList {
	if len(rl) == 0 {
		return nil
	}
	out := make(corev1.ResourceList, len(rl))
	for k, v := range rl {
		out[corev1.ResourceName(k)] = v.DeepCopy()
	}
	return out
}
