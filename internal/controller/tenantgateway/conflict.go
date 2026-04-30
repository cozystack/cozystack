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
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	gatewayv1alpha1 "github.com/cozystack/cozystack/api/gateway/v1alpha1"
)

// routeKind discriminates HTTPRoute vs TLSRoute when stamping
// RouteParentStatus back. Without this, status writes would target
// the wrong resource type entirely.
type routeKind int

const (
	routeKindHTTP routeKind = iota
	routeKindTLS
)

// ControllerName is the value used in HTTPRoute.Status.Parents[].ControllerName
// for entries written by this reconciler. Distinct from any GatewayClass
// controllerName (Cilium etc.) so multiple controllers can coexist.
const ControllerName gatewayv1.GatewayController = "gateway.cozystack.io/tenantgateway-controller"

// routeRef is a lightweight identifier of an HTTPRoute or TLSRoute
// as far as hostname-conflict resolution is concerned. The kind
// field is required so status updates write to the right resource
// type — TLSRoute and HTTPRoute may share namespace/name but live
// at different GVKs.
type routeRef struct {
	kind      routeKind
	namespace string
	name      string
	parentRef gatewayv1.ParentReference // exact ref the route used to attach
}

// resolveHostnameOwners groups hostnames by owner-route and decides
// who wins when more than one route claims the same hostname.
// Returns:
//   - winners: hostname -> the routeRef that should produce a listener.
//   - losers: routeRef -> []hostname for which this route is NOT the winner.
//
// Rule: cozy-* namespace beats anything else; within the same priority
// tier the route with the lexicographically smallest namespace/name
// pair wins (deterministic).
func resolveHostnameOwners(claims map[string][]routeRef) (map[string]routeRef, map[routeRef][]string) {
	winners := make(map[string]routeRef, len(claims))
	losers := make(map[routeRef][]string)

	for hostname, refs := range claims {
		if len(refs) == 0 {
			continue
		}
		sort.Slice(refs, func(i, j int) bool {
			ic := strings.HasPrefix(refs[i].namespace, "cozy-")
			jc := strings.HasPrefix(refs[j].namespace, "cozy-")
			if ic != jc {
				return ic // cozy-* sorts first
			}
			if refs[i].namespace != refs[j].namespace {
				return refs[i].namespace < refs[j].namespace
			}
			return refs[i].name < refs[j].name
		})
		winner := refs[0]
		winners[hostname] = winner
		for _, lr := range refs[1:] {
			// Same-namespace routes claiming the same hostname are
			// not a conflict — Gateway API merges them by path /
			// headers / etc. Only cross-namespace claims are a
			// hijack signal.
			if lr.namespace == winner.namespace {
				continue
			}
			losers[lr] = append(losers[lr], hostname)
		}
	}
	return winners, losers
}

// updateRouteStatuses writes HTTPRoute.Status.Parents entries under
// our ControllerName: Accepted=True for winners, Accepted=False with
// Reason=HostnameConflict for losers. Status.Parents entries written by
// other controllers (Cilium etc.) are left untouched.
func (r *Reconciler) updateRouteStatuses(
	ctx context.Context,
	tgw *gatewayv1alpha1.TenantGateway,
	winners map[string]routeRef,
	losers map[routeRef][]string,
) error {
	logger := log.FromContext(ctx)

	winnerRefs := map[routeRef]struct{}{}
	for _, ref := range winners {
		winnerRefs[ref] = struct{}{}
	}
	now := metav1.Now()

	for ref := range winnerRefs {
		if _, isLoser := losers[ref]; isLoser {
			// A route can be a winner for one hostname and a loser
			// for another; conflict status takes priority over the
			// happy path.
			continue
		}
		if err := r.updateRouteParentStatus(ctx, ref, []metav1.Condition{
			{
				Type:               "Accepted",
				Status:             metav1.ConditionTrue,
				Reason:             "Accepted",
				Message:            fmt.Sprintf("Route attached to TenantGateway %s/%s", tgw.Namespace, tgw.Name),
				LastTransitionTime: now,
			},
		}); err != nil {
			logger.Error(err, "update winner route status", "route", ref.namespace+"/"+ref.name)
		}
	}

	for ref, hostnames := range losers {
		if err := r.updateRouteParentStatus(ctx, ref, []metav1.Condition{
			{
				Type:               "Accepted",
				Status:             metav1.ConditionFalse,
				Reason:             "HostnameConflict",
				Message:            fmt.Sprintf("Hostname(s) %s already claimed by another route on TenantGateway %s/%s", strings.Join(hostnames, ", "), tgw.Namespace, tgw.Name),
				LastTransitionTime: now,
			},
		}); err != nil {
			logger.Error(err, "update loser route status", "route", ref.namespace+"/"+ref.name)
		}
	}
	return nil
}

// updateRouteParentStatus locates or creates the RouteParentStatus
// entry for our ControllerName on the given route (HTTPRoute or
// TLSRoute, by ref.kind) and overwrites its Conditions with the
// supplied set.
func (r *Reconciler) updateRouteParentStatus(ctx context.Context, ref routeRef, conds []metav1.Condition) error {
	switch ref.kind {
	case routeKindHTTP:
		route := &gatewayv1.HTTPRoute{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: ref.namespace, Name: ref.name}, route); err != nil {
			return fmt.Errorf("get HTTPRoute: %w", err)
		}
		mergeRouteParentStatus(&route.Status.Parents, ref.parentRef, conds)
		return r.Status().Update(ctx, route)
	case routeKindTLS:
		route := &gatewayv1alpha2.TLSRoute{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: ref.namespace, Name: ref.name}, route); err != nil {
			return fmt.Errorf("get TLSRoute: %w", err)
		}
		mergeRouteParentStatus(&route.Status.Parents, ref.parentRef, conds)
		return r.Status().Update(ctx, route)
	default:
		return fmt.Errorf("unknown route kind %d for %s/%s", ref.kind, ref.namespace, ref.name)
	}
}

// mergeRouteParentStatus updates or appends the RouteParentStatus
// entry tagged with our ControllerName. Other entries (Cilium,
// other controllers) are left alone.
func mergeRouteParentStatus(parents *[]gatewayv1.RouteParentStatus, ref gatewayv1.ParentReference, conds []metav1.Condition) {
	for i := range *parents {
		ps := &(*parents)[i]
		if ps.ControllerName == ControllerName {
			ps.ParentRef = ref
			ps.Conditions = conds
			return
		}
	}
	*parents = append(*parents, gatewayv1.RouteParentStatus{
		ControllerName: ControllerName,
		ParentRef:      ref,
		Conditions:     conds,
	})
}

