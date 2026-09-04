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

	apimeta "k8s.io/apimachinery/pkg/api/meta"
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

// updateRouteStatuses writes RouteParentStatus entries under our
// ControllerName, one per (route, parentRef) tuple that attached to
// this TenantGateway. Accepted=True for tuples not in losers,
// Accepted=False with Reason=HostnameConflict for tuples that lost
// at least one hostname race. Other controllers' entries (Cilium
// etc.) are untouched.
//
// allRefs is the full set of (route, parentRef) tuples observed by
// collectHostnameClaims — without it, multi-parentRef routes would
// only get a status entry for whichever ref happened to win the
// per-hostname race, dropping per-section visibility for the others.
func (r *Reconciler) updateRouteStatuses(
	ctx context.Context,
	tgw *gatewayv1alpha1.TenantGateway,
	allRefs map[routeRef]struct{},
	losers map[routeRef][]string,
) error {
	logger := log.FromContext(ctx)

	// LastTransitionTime is set by apimeta.SetStatusCondition inside
	// mergeRouteParentStatus only when the condition actually
	// transitions; building Conditions here without it keeps the
	// no-op reconcile no-op.
	for ref := range allRefs {
		if hostnames, isLoser := losers[ref]; isLoser {
			// A route can claim multiple hostnames; conflict status
			// takes priority over the happy path.
			if err := r.updateRouteParentStatus(ctx, ref, []metav1.Condition{
				{
					Type:    "Accepted",
					Status:  metav1.ConditionFalse,
					Reason:  "HostnameConflict",
					Message: fmt.Sprintf("Hostname(s) %s already claimed by another route on TenantGateway %s/%s", strings.Join(hostnames, ", "), tgw.Namespace, tgw.Name),
				},
			}); err != nil {
				logger.Error(err, "update loser route status", "route", ref.namespace+"/"+ref.name)
			}
			continue
		}
		if err := r.updateRouteParentStatus(ctx, ref, []metav1.Condition{
			{
				Type:    "Accepted",
				Status:  metav1.ConditionTrue,
				Reason:  "Accepted",
				Message: fmt.Sprintf("Route attached to TenantGateway %s/%s", tgw.Namespace, tgw.Name),
			},
		}); err != nil {
			logger.Error(err, "update winner route status", "route", ref.namespace+"/"+ref.name)
		}
	}
	return nil
}

// reconcileTLSRouteWithdrawal keeps this controller's Accepted condition
// on attached TLSRoutes honest in the modes where updateRouteStatuses
// above says nothing. collectHostnameClaims returns no claims for a
// whole-apex mode, so that pass runs over an empty ref set and neither
// writes nor retracts anything.
//
// Edge renders no TLS-passthrough listener, so every attached TLSRoute
// pins a section that is gone and is told so. NoMatchingParent is
// Gateway API's reason for a parentRef whose port or sectionName
// matches no listener on the Gateway, which is what the platform's own
// api, vm-exportproxy and cdi-uploadproxy routes now have: all three
// pin sectionName tls-<svc>.
//
// The other whole-apex modes render those listeners and serve them, but
// this controller collects no claims there and so has no basis to judge
// any TLSRoute. It therefore owns no condition on them, and drops the
// one it wrote while the tenant ran edge. Leaving that behind inverts
// the problem the withdrawal exists for: a served endpoint reported as
// matching no parent, with a message naming a class provider that no
// longer terminates anything.
//
// HTTP-01 is not handled here at all: it collects claims, so
// updateRouteStatuses has already judged every route this pass would
// have anything to say about.
//
// Both directions therefore skip a route that declares no hostnames.
// collectHostnameClaims gathers refs inside a loop over spec.hostnames
// and TLSRoute v1alpha2 sets no minItems on it, so such a route never
// reaches updateRouteStatuses in any mode. Withdrawing it would write a
// condition with no path back: HTTP-01 renders its listener again and
// judges only the routes that produced claims. This controller does not
// judge a hostname-less route, so it holds no opinion on one either.
func (r *Reconciler) reconcileTLSRouteWithdrawal(ctx context.Context, tgw *gatewayv1alpha1.TenantGateway) error {
	if !servesWholeApex(tgw) {
		return nil
	}
	withdraw := !rendersTLSPassthrough(tgw)
	logger := log.FromContext(ctx)

	allowed, err := r.allowedAttachNamespaces(ctx, tgw)
	if err != nil {
		return err
	}
	routes := &gatewayv1alpha2.TLSRouteList{}
	if err := r.List(ctx, routes); err != nil {
		return fmt.Errorf("list TLSRoutes: %w", err)
	}
	for i := range routes.Items {
		route := &routes.Items[i]
		if _, ok := allowed[route.Namespace]; !ok {
			continue
		}
		if len(route.Spec.Hostnames) == 0 {
			continue
		}
		for _, parentRef := range allAttachingParentRefs(route.Spec.ParentRefs, route.Namespace, tgw) {
			ref := routeRef{
				kind:      routeKindTLS,
				namespace: route.Namespace,
				name:      route.Name,
				parentRef: parentRef,
			}
			var err error
			if withdraw {
				err = r.updateRouteParentStatus(ctx, ref, []metav1.Condition{
					{
						Type:    "Accepted",
						Status:  metav1.ConditionFalse,
						Reason:  "NoMatchingParent",
						Message: fmt.Sprintf("TenantGateway %s/%s terminates TLS at its class provider and renders no TLS-passthrough listener", tgw.Namespace, tgw.Name),
					},
				})
			} else {
				err = r.dropTLSRouteParentStatus(ctx, ref)
			}
			if err != nil {
				logger.Error(err, "reconcile TLSRoute status", "route", ref.namespace+"/"+ref.name)
			}
		}
	}
	return nil
}

// dropTLSRouteParentStatus removes this controller's RouteParentStatus
// entry for ref from a TLSRoute, leaving every other controller's entry
// in place. Same idempotency contract as updateRouteParentStatus: the
// Status().Update is issued only when the entry was actually there.
func (r *Reconciler) dropTLSRouteParentStatus(ctx context.Context, ref routeRef) error {
	route := &gatewayv1alpha2.TLSRoute{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: ref.namespace, Name: ref.name}, route); err != nil {
		return fmt.Errorf("get TLSRoute: %w", err)
	}
	before := route.DeepCopy()
	removeRouteParentStatus(&route.Status.Parents, ref.parentRef)
	if routeParentStatusEqual(before.Status.Parents, route.Status.Parents) {
		return nil
	}
	return r.Status().Update(ctx, route)
}

// removeRouteParentStatus deletes the entry tagged with (ControllerName,
// ref), the exact key mergeRouteParentStatus writes under.
func removeRouteParentStatus(parents *[]gatewayv1.RouteParentStatus, ref gatewayv1.ParentReference) {
	kept := (*parents)[:0]
	for _, ps := range *parents {
		if ps.ControllerName == ControllerName && parentRefEqual(ps.ParentRef, ref) {
			continue
		}
		kept = append(kept, ps)
	}
	*parents = kept
}

// updateRouteParentStatus locates or creates the RouteParentStatus
// entry for our ControllerName on the given route (HTTPRoute or
// TLSRoute, by ref.kind) and merges Conditions in.
//
// Idempotency contract: Status().Update() is only issued when the
// merge actually changes something. apimeta.SetStatusCondition
// preserves LastTransitionTime when Type/Status/Reason/Message all
// match the existing entry, so a quiescent reconcile produces no
// resource-version bump and the Owns/Watches re-trigger storm
// short-circuits at the controller-runtime workqueue level.
func (r *Reconciler) updateRouteParentStatus(ctx context.Context, ref routeRef, conds []metav1.Condition) error {
	switch ref.kind {
	case routeKindHTTP:
		route := &gatewayv1.HTTPRoute{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: ref.namespace, Name: ref.name}, route); err != nil {
			return fmt.Errorf("get HTTPRoute: %w", err)
		}
		before := route.DeepCopy()
		mergeRouteParentStatus(&route.Status.Parents, ref.parentRef, conds)
		if routeParentStatusEqual(before.Status.Parents, route.Status.Parents) {
			return nil
		}
		return r.Status().Update(ctx, route)
	case routeKindTLS:
		route := &gatewayv1alpha2.TLSRoute{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: ref.namespace, Name: ref.name}, route); err != nil {
			return fmt.Errorf("get TLSRoute: %w", err)
		}
		before := route.DeepCopy()
		mergeRouteParentStatus(&route.Status.Parents, ref.parentRef, conds)
		if routeParentStatusEqual(before.Status.Parents, route.Status.Parents) {
			return nil
		}
		return r.Status().Update(ctx, route)
	default:
		return fmt.Errorf("unknown route kind %d for %s/%s", ref.kind, ref.namespace, ref.name)
	}
}

// mergeRouteParentStatus updates or appends the RouteParentStatus
// entry tagged with (ControllerName, ParentRef), using
// apimeta.SetStatusCondition to preserve LastTransitionTime across
// no-op reconciles. Other controllers' entries (Cilium, etc.) are
// left alone.
//
// Per Gateway API, RouteParentStatus is keyed by (ParentRef,
// ControllerName) — a single route may attach via multiple parentRefs
// (e.g. one per sectionName) and each attachment owns its own status
// entry. Keying only on ControllerName would let a multi-ref route's
// later reconcile overwrite the earlier ref's status, hiding
// per-section conflicts.
func mergeRouteParentStatus(parents *[]gatewayv1.RouteParentStatus, ref gatewayv1.ParentReference, conds []metav1.Condition) {
	for i := range *parents {
		ps := &(*parents)[i]
		if ps.ControllerName != ControllerName {
			continue
		}
		if !parentRefEqual(ps.ParentRef, ref) {
			continue
		}
		for _, c := range conds {
			apimeta.SetStatusCondition(&ps.Conditions, c)
		}
		return
	}
	// First-time stamp for this (ControllerName, ParentRef) pair:
	// build the slice via SetStatusCondition so transition timestamps
	// are populated by the helper rather than hand-stamped time.Now()
	// at construction.
	newPS := gatewayv1.RouteParentStatus{
		ControllerName: ControllerName,
		ParentRef:      ref,
	}
	for _, c := range conds {
		apimeta.SetStatusCondition(&newPS.Conditions, c)
	}
	*parents = append(*parents, newPS)
}

// routeParentStatusEqual compares two RouteParentStatus slices
// ignoring observation-only fields that legitimately differ across
// reconciles (LastTransitionTime is preserved by SetStatusCondition
// when nothing else changed, but explicit comparison guards against
// drift).
func routeParentStatusEqual(a, b []gatewayv1.RouteParentStatus) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		ap, bp := a[i], b[i]
		if ap.ControllerName != bp.ControllerName {
			return false
		}
		if !parentRefEqual(ap.ParentRef, bp.ParentRef) {
			return false
		}
		if !routeConditionsEqual(ap.Conditions, bp.Conditions) {
			return false
		}
	}
	return true
}

func parentRefEqual(a, b gatewayv1.ParentReference) bool {
	return strDerefEqual(a.Group, b.Group) &&
		strDerefEqual(a.Kind, b.Kind) &&
		strDerefEqual(a.Namespace, b.Namespace) &&
		a.Name == b.Name &&
		strDerefEqual(a.SectionName, b.SectionName) &&
		port32DerefEqual(a.Port, b.Port)
}

func strDerefEqual[T ~string](a, b *T) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func port32DerefEqual(a, b *gatewayv1.PortNumber) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func routeConditionsEqual(a, b []metav1.Condition) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		ac, bc := a[i], b[i]
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
