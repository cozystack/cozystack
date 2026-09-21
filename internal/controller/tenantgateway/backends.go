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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"
)

// tlsRouteForwards reports whether route names at least one backend the
// pinned Cilium would forward to, which is what decides whether the
// route puts a filter chain on its hostname.
//
// The question is answered exactly as v1.19.5 answers it and no more
// precisely. toTLSRoutes (operator/pkg/model/ingestion/gateway.go:587-616)
// keeps a backendRef when IsBackendReferenceAllowed passes and
// getServiceSpec finds the Service, then tlsPassthroughFilterChains
// (operator/pkg/model/translation/envoy_listener.go:408-411) skips a
// route whose surviving list is empty. Neither step reads the Service's
// ports or its endpoints, so neither does this: a backendRef naming a
// port the Service does not publish still yields a chain that carries
// the SNI and forwards to a closed port, which is a broken backend
// rather than an unclaimed hostname.
//
// Any rule with a surviving backend is enough. Cilium appends one model
// route per rule, so a route whose second rule resolves carries the SNI
// however little the first one does.
//
// A ServiceImport backend counts as unresolvable. Cilium resolves those
// through mcs-api, whose CRDs this platform does not install, so the
// conservative answer is also the accurate one here — and it errs
// towards keeping a terminate listener rather than shedding one.
func (r *Reconciler) tlsRouteForwards(ctx context.Context, route *gatewayv1alpha2.TLSRoute, grants []gatewayv1beta1.ReferenceGrant) (bool, error) {
	for _, rule := range route.Spec.Rules {
		for _, be := range rule.BackendRefs {
			if !backendRefIsService(be.BackendObjectReference) {
				continue
			}
			ns := backendRefNamespace(be.BackendObjectReference, route.Namespace)
			if ns != route.Namespace && !serviceReferenceGranted(grants, route.Namespace, ns, string(be.Name)) {
				continue
			}
			err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: string(be.Name)}, &corev1.Service{})
			if apierrors.IsNotFound(err) {
				continue
			}
			// A read that failed for any other reason says nothing
			// about whether the Service is there, and a withdrawal
			// decided on it would shed a listener on the strength of an
			// unavailable apiserver. Fail the reconcile instead and let
			// the requeue decide once the answer is knowable.
			if err != nil {
				return false, fmt.Errorf("get backend Service %s/%s: %w", ns, be.Name, err)
			}
			return true, nil
		}
	}
	return false, nil
}

// backendRefIsService mirrors Cilium's helpers.IsService: an absent
// kind or group means Service in the core group, so the common
// backendRef that names neither is one.
func backendRefIsService(ref gatewayv1.BackendObjectReference) bool {
	return (ref.Kind == nil || *ref.Kind == "Service") &&
		(ref.Group == nil || *ref.Group == corev1.GroupName)
}

// backendRefNamespace mirrors Cilium's helpers.NamespaceDerefOr, which
// treats an empty namespace the same as an absent one. Getting that
// wrong would read a ref written with `namespace: ""` as
// cross-namespace and demand a grant for the route's own namespace.
func backendRefNamespace(ref gatewayv1.BackendObjectReference, routeNamespace string) string {
	if ref.Namespace != nil && *ref.Namespace != "" {
		return string(*ref.Namespace)
	}
	return routeNamespace
}

// serviceReferenceGranted answers the cross-namespace half of
// Cilium's helpers.isReferenceAllowed for the one direction that
// matters here: a TLSRoute reaching a Service in another namespace.
//
// The grant lives in the namespace being referenced, names the
// referring kind and namespace in from, and the referenced kind in to.
// An unnamed to entry admits every Service in the grant's namespace,
// a named one only that Service. Same-namespace references never reach
// this function: they need no grant and the caller decides them first.
func serviceReferenceGranted(grants []gatewayv1beta1.ReferenceGrant, routeNamespace, backendNamespace, backendName string) bool {
	for _, grant := range grants {
		if grant.Namespace != backendNamespace {
			continue
		}
		for _, from := range grant.Spec.From {
			if string(from.Group) != gatewayv1.GroupName || from.Kind != "TLSRoute" || string(from.Namespace) != routeNamespace {
				continue
			}
			for _, to := range grant.Spec.To {
				if string(to.Group) != corev1.GroupName || to.Kind != "Service" {
					continue
				}
				if to.Name == nil || string(*to.Name) == backendName {
					return true
				}
			}
		}
	}
	return false
}
