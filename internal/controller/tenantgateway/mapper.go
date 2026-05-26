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

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

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
