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

package tenantgateway

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	gatewayv1alpha1 "github.com/cozystack/cozystack/api/gateway/v1alpha1"
)

// reconcileStatus refreshes status.observedGeneration, status.listeners,
// and the Ready condition on the TenantGateway. It runs at the end of
// Reconcile so the snapshot reflects the new Gateway/Certificate state.
func (r *Reconciler) reconcileStatus(
	ctx context.Context,
	tgw *gatewayv1alpha1.TenantGateway,
	dynHostnames []string,
) error {
	gw := &gatewayv1.Gateway{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: tgw.Namespace, Name: tgw.Name}, gw); err != nil {
		return fmt.Errorf("get Gateway for status: %w", err)
	}

	listeners := make([]gatewayv1alpha1.TenantGatewayListenerStatus, 0, len(gw.Spec.Listeners))
	for _, l := range gw.Spec.Listeners {
		s := gatewayv1alpha1.TenantGatewayListenerStatus{
			Name:  string(l.Name),
			Ready: true,
		}
		if l.Hostname != nil {
			s.Hostname = string(*l.Hostname)
		}
		if l.TLS != nil && len(l.TLS.CertificateRefs) > 0 {
			s.CertificateName = string(l.TLS.CertificateRefs[0].Name)
		}
		listeners = append(listeners, s)
	}

	desiredConds := []metav1.Condition{
		{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			ObservedGeneration: tgw.Generation,
			Reason:             "Reconciled",
			Message:            fmt.Sprintf("Gateway %s/%s reconciled with %d listeners", tgw.Namespace, tgw.Name, len(listeners)),
		},
	}

	stale := tgw.DeepCopy()
	stale.Status.ObservedGeneration = tgw.Generation
	stale.Status.Listeners = listeners
	for _, c := range desiredConds {
		meta.SetStatusCondition(&stale.Status.Conditions, c)
	}

	if statusEqual(tgw.Status, stale.Status) {
		return nil
	}
	tgw.Status = stale.Status
	return r.Status().Update(ctx, tgw)
}

func statusEqual(a, b gatewayv1alpha1.TenantGatewayStatus) bool {
	if a.ObservedGeneration != b.ObservedGeneration {
		return false
	}
	if len(a.Listeners) != len(b.Listeners) {
		return false
	}
	for i := range a.Listeners {
		if a.Listeners[i] != b.Listeners[i] {
			return false
		}
	}
	if len(a.Conditions) != len(b.Conditions) {
		return false
	}
	for i := range a.Conditions {
		ac, bc := a.Conditions[i], b.Conditions[i]
		if ac.Type != bc.Type ||
			ac.Status != bc.Status ||
			ac.Reason != bc.Reason ||
			ac.Message != bc.Message ||
			ac.ObservedGeneration != bc.ObservedGeneration {
			return false
		}
	}
	return true
}
