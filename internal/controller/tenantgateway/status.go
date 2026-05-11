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
// and the Ready condition on the TenantGateway based on the actual
// state of the rendered Gateway (Gateway.Status.Listeners +
// Gateway.Status.Conditions). Operators reading `kubectl get tgw`
// see real readiness, not a fictional always-True flag.
func (r *Reconciler) reconcileStatus(
	ctx context.Context,
	tgw *gatewayv1alpha1.TenantGateway,
	dynHostnames []string,
) error {
	gw := &gatewayv1.Gateway{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: tgw.Namespace, Name: tgw.Name}, gw); err != nil {
		return fmt.Errorf("get Gateway for status: %w", err)
	}

	gwListenerStatus := indexListenerStatus(gw.Status.Listeners)

	listeners := make([]gatewayv1alpha1.TenantGatewayListenerStatus, 0, len(gw.Spec.Listeners))
	allReady := true
	for _, l := range gw.Spec.Listeners {
		ready, reason := listenerReadinessFromGatewayStatus(string(l.Name), gwListenerStatus)
		s := gatewayv1alpha1.TenantGatewayListenerStatus{
			Name:   string(l.Name),
			Ready:  ready,
			Reason: reason,
		}
		if l.Hostname != nil {
			s.Hostname = string(*l.Hostname)
		}
		if l.TLS != nil && len(l.TLS.CertificateRefs) > 0 {
			s.CertificateName = string(l.TLS.CertificateRefs[0].Name)
		}
		listeners = append(listeners, s)
		if !ready {
			allReady = false
		}
	}

	gwAccepted, gwProgrammed := gatewayConditionStatus(gw.Status.Conditions)

	var ready metav1.Condition
	switch {
	case !gwAccepted:
		ready = metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			ObservedGeneration: tgw.Generation,
			Reason:             "GatewayNotAccepted",
			Message:            fmt.Sprintf("Underlying Gateway %s/%s has not been accepted by its controller yet", tgw.Namespace, tgw.Name),
		}
	case !gwProgrammed:
		ready = metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			ObservedGeneration: tgw.Generation,
			Reason:             "GatewayNotProgrammed",
			Message:            fmt.Sprintf("Underlying Gateway %s/%s has not been programmed by its controller yet", tgw.Namespace, tgw.Name),
		}
	case !allReady:
		ready = metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			ObservedGeneration: tgw.Generation,
			Reason:             "ListenersNotReady",
			Message:            fmt.Sprintf("One or more listeners on Gateway %s/%s are not ready", tgw.Namespace, tgw.Name),
		}
	default:
		ready = metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			ObservedGeneration: tgw.Generation,
			Reason:             "Reconciled",
			Message:            fmt.Sprintf("Gateway %s/%s programmed with %d listeners", tgw.Namespace, tgw.Name, len(listeners)),
		}
	}

	stale := tgw.DeepCopy()
	stale.Status.ObservedGeneration = tgw.Generation
	stale.Status.Listeners = listeners
	meta.SetStatusCondition(&stale.Status.Conditions, ready)

	if statusEqual(tgw.Status, stale.Status) {
		return nil
	}
	tgw.Status = stale.Status
	return r.Status().Update(ctx, tgw)
}

// indexListenerStatus turns Gateway.Status.Listeners into a name→status
// map for O(1) lookup per spec listener.
func indexListenerStatus(in []gatewayv1.ListenerStatus) map[string]gatewayv1.ListenerStatus {
	out := make(map[string]gatewayv1.ListenerStatus, len(in))
	for _, ls := range in {
		out[string(ls.Name)] = ls
	}
	return out
}

// listenerReadinessFromGatewayStatus maps the Gateway controller's
// per-listener conditions onto our TenantGatewayListenerStatus.Ready
// boolean + a human-readable reason. A listener is ready iff both
// Accepted=True and Programmed=True. If the Gateway controller has
// not yet reported any condition for the listener, Ready=false with
// reason "Pending" — operators see "in flight" rather than a green
// light that does not exist.
func listenerReadinessFromGatewayStatus(name string, idx map[string]gatewayv1.ListenerStatus) (bool, string) {
	ls, ok := idx[name]
	if !ok {
		return false, "Pending"
	}
	var accepted, programmed bool
	for _, c := range ls.Conditions {
		if c.Type == "Accepted" && c.Status == metav1.ConditionTrue {
			accepted = true
		}
		if c.Type == "Programmed" && c.Status == metav1.ConditionTrue {
			programmed = true
		}
	}
	switch {
	case accepted && programmed:
		return true, ""
	case !accepted:
		return false, "NotAccepted"
	default:
		return false, "NotProgrammed"
	}
}

// gatewayConditionStatus reports (Accepted, Programmed) booleans
// from the Gateway-level Status.Conditions. Either is false when the
// condition is missing or its Status is not True.
func gatewayConditionStatus(conds []metav1.Condition) (accepted, programmed bool) {
	for _, c := range conds {
		if c.Type == "Accepted" && c.Status == metav1.ConditionTrue {
			accepted = true
		}
		if c.Type == "Programmed" && c.Status == metav1.ConditionTrue {
			programmed = true
		}
	}
	return
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
