/*
Copyright 2025 The Cozystack Authors.

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
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

// routeToTenantGateway returns an EventHandler that maps an HTTPRoute
// or TLSRoute change back to the TenantGateway resources whose Gateway
// the route attaches to. controller-runtime requeues the parent so
// listener / cert lifecycle stays in sync with route additions and
// removals.
//
// Empty in this commit — the next commits flesh out the parentRef
// resolution; the wiring needs to exist now so SetupWithManager
// compiles.
func routeToTenantGateway() handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		_ = ctx
		switch r := obj.(type) {
		case *gatewayv1.HTTPRoute:
			_ = r
		case *gatewayv1alpha2.TLSRoute:
			_ = r
		}
		return []reconcile.Request{{NamespacedName: types.NamespacedName{}}}
	})
}
