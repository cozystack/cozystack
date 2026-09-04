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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	gatewayv1alpha1 "github.com/cozystack/cozystack/api/gateway/v1alpha1"
)

// routeToTenantGateway returns an EventHandler that maps an HTTPRoute
// or TLSRoute change back to the TenantGateway resources whose Gateway
// the route attaches to. controller-runtime requeues the parent so
// listener / cert lifecycle stays in sync with route additions and
// removals.
func (r *Reconciler) routeToTenantGateway() handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(r.mapRouteToTenantGateways)
}

// mapRouteToTenantGateways is the underlying MapFunc, exposed as a
// method so tests can drive it directly without going through
// controller-runtime's EventHandler.Generic().
func (r *Reconciler) mapRouteToTenantGateways(ctx context.Context, obj client.Object) []reconcile.Request {
	var (
		parentRefs []gatewayv1.ParentReference
		routeNs    string
	)
	switch route := obj.(type) {
	case *gatewayv1.HTTPRoute:
		parentRefs = route.Spec.ParentRefs
		routeNs = route.Namespace
	case *gatewayv1alpha2.TLSRoute:
		parentRefs = route.Spec.ParentRefs
		routeNs = route.Namespace
	default:
		return nil
	}
	if len(parentRefs) == 0 {
		return nil
	}

	list := &gatewayv1alpha1.TenantGatewayList{}
	if err := r.List(ctx, list); err != nil {
		log.FromContext(ctx).Error(err, "list TenantGateways for route mapper")
		return nil
	}

	var out []reconcile.Request
	for i := range list.Items {
		tgw := &list.Items[i]
		if _, ok := pickAttachingParentRef(parentRefs, routeNs, tgw); !ok {
			continue
		}
		out = append(out, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Namespace: tgw.Namespace,
				Name:      tgw.Name,
			},
		})
	}
	return out
}

// backendToTenantGateways returns an EventHandler that maps a Service
// or ReferenceGrant change back to the TenantGateways whose withdrawal
// decision it can flip.
//
// A TLSRoute takes a hostname over from the terminate listener only
// once it forwards somewhere, so the withdrawal waits on an object the
// route does not own: the Service arriving, or the grant that admits a
// cross-namespace reference. Neither is watched by the route mapper,
// and without this the terminate listener stands until something else
// happens to requeue the Gateway.
func (r *Reconciler) backendToTenantGateways() handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(r.mapBackendToTenantGateways)
}

// mapBackendToTenantGateways is the underlying MapFunc, exposed as a
// method for the same reason mapRouteToTenantGateways is.
//
// Only TLSRoutes are walked, because no decision here reads an
// HTTPRoute's backends. The Service leg matches on name and namespace;
// the grant leg on the referenced namespace alone, since deciding
// whether this particular grant admits this particular reference would
// duplicate the resolution rules to save a reconcile that resolves
// them anyway.
func (r *Reconciler) mapBackendToTenantGateways(ctx context.Context, obj client.Object) []reconcile.Request {
	var affects func(routeNamespace string, ref gatewayv1.BackendObjectReference) bool
	switch o := obj.(type) {
	case *corev1.Service:
		affects = func(routeNamespace string, ref gatewayv1.BackendObjectReference) bool {
			return backendRefIsService(ref) &&
				string(ref.Name) == o.Name &&
				backendRefNamespace(ref, routeNamespace) == o.Namespace
		}
	case *gatewayv1beta1.ReferenceGrant:
		affects = func(routeNamespace string, ref gatewayv1.BackendObjectReference) bool {
			ns := backendRefNamespace(ref, routeNamespace)
			return ns != routeNamespace && ns == o.Namespace
		}
	default:
		return nil
	}

	routes := &gatewayv1alpha2.TLSRouteList{}
	if err := r.List(ctx, routes); err != nil {
		log.FromContext(ctx).Error(err, "list TLSRoutes for backend mapper")
		return nil
	}
	gateways := &gatewayv1alpha1.TenantGatewayList{}
	if err := r.List(ctx, gateways); err != nil {
		log.FromContext(ctx).Error(err, "list TenantGateways for backend mapper")
		return nil
	}

	// Deduplicated across routes: several TLSRoutes on one Gateway can
	// name the same Service, and one requeue reconciles all of them.
	seen := map[types.NamespacedName]struct{}{}
	var out []reconcile.Request
	for i := range routes.Items {
		route := &routes.Items[i]
		if !tlsRouteNamesBackend(route, affects) {
			continue
		}
		for j := range gateways.Items {
			tgw := &gateways.Items[j]
			if _, ok := pickAttachingParentRef(route.Spec.ParentRefs, route.Namespace, tgw); !ok {
				continue
			}
			key := types.NamespacedName{Namespace: tgw.Namespace, Name: tgw.Name}
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, reconcile.Request{NamespacedName: key})
		}
	}
	return out
}

// tlsRouteNamesBackend reports whether any backendRef on route matches,
// over every rule: one rule resolving is enough to put the route's
// hostname on the passthrough listener, so one rule matching is enough
// to make the event worth a reconcile.
func tlsRouteNamesBackend(route *gatewayv1alpha2.TLSRoute, affects func(string, gatewayv1.BackendObjectReference) bool) bool {
	for _, rule := range route.Spec.Rules {
		for _, be := range rule.BackendRefs {
			if affects(route.Namespace, be.BackendObjectReference) {
				return true
			}
		}
	}
	return false
}
