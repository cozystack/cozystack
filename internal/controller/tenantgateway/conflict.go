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
	"slices"
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

// resolveHostnameOwners decides who wins when more than one route
// claims the same hostname, and returns the routes that lost:
// routeRef -> []hostname for which this route is NOT the winner.
//
// The winners are resolved but not returned, because ownership decides
// route status and not what gets rendered. A hostname carries at most
// one listener however many routes claim it, and runReconcileSteps
// derives that from the claims themselves, so a winner of the wrong
// kind cannot take a terminate listener away from an HTTPRoute that
// also claimed the name.
//
// Rule: cozy-* namespace beats anything else; within the same priority
// tier the route with the lexicographically smallest namespace/name
// pair wins (deterministic).
func resolveHostnameOwners(claims map[string][]routeRef) map[routeRef][]string {
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
	return losers
}

// withdrawnHostname pairs a hostname the controller declined to serve
// with the TLS-passthrough hostname whose SNI answers it, or with the
// empty string when nothing answers it at all. The pair is carried
// rather than the claimed name alone because a wildcard entry is not
// derivable from the route: the route claims pg.db.<apex> and the spec
// that took it away says *.db.<apex>.
type withdrawnHostname struct {
	hostname   string
	answeredBy string
}

// describeWithdrawn renders the pairs in a stable order, dropping
// repeats: one route may list a hostname twice, since Gateway API
// declares spec.hostnames a plain array. The message is rebuilt on
// every reconcile, so an unstable order would rewrite the condition
// each pass and churn the route's status forever.
//
// An empty answeredBy means no listener answers the name at all, which
// reads differently and is rendered differently.
func describeWithdrawn(hostnames []withdrawnHostname) (answered, unserved string) {
	var withListener, without []string
	for _, h := range hostnames {
		if h.answeredBy == "" {
			without = append(without, h.hostname)
			continue
		}
		withListener = append(withListener, fmt.Sprintf("%s (answered by %s)", h.hostname, h.answeredBy))
	}
	sort.Strings(withListener)
	sort.Strings(without)
	return strings.Join(slices.Compact(withListener), ", "), strings.Join(slices.Compact(without), ", ")
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
//
// withdrawn carries the tuples the controller declined to serve: a
// hostname a TLS-passthrough listener answers, or one claimed only by
// a TLSRoute with no passthrough listener behind it. Neither lost a
// race, so HostnameConflict would misname the cause; they get
// NoMatchingListenerHostname, which is literally true — the controller
// rendered no listener for that hostname, and the route condition is
// the only object left that can say why, the listener that used to
// carry a condition of its own having gone with it.
func (r *Reconciler) updateRouteStatuses(
	ctx context.Context,
	tgw *gatewayv1alpha1.TenantGateway,
	allRefs map[routeRef]struct{},
	losers map[routeRef][]string,
	withdrawn map[routeRef][]withdrawnHostname,
) error {
	logger := log.FromContext(ctx)

	// LastTransitionTime is set by apimeta.SetStatusCondition inside
	// mergeRouteParentStatus only when the condition actually
	// transitions; building Conditions here without it keeps the
	// no-op reconcile no-op.
	for ref := range allRefs {
		lost, isLoser := losers[ref]
		gone, isWithdrawn := withdrawn[ref]
		if isLoser || isWithdrawn {
			// One route can be hit by both causes on different
			// hostnames, and Gateway API gives it a single Accepted
			// condition, so the message carries both rather than
			// whichever branch runs first. Only the reason has to
			// choose, and it names the race: that is the half the
			// operator can act on, by moving or removing the other
			// route, while a withdrawn hostname is a property of the
			// TenantGateway spec.
			var causes []string
			reason := string(gatewayv1.RouteReasonNoMatchingListenerHostname)
			if isLoser {
				// Sorted for the same reason describeWithdrawn sorts:
				// the hostnames come out of a map, and a message that
				// reorders between passes rewrites the condition, which
				// writes status, which requeues this object through the
				// route watch.
				sort.Strings(lost)
				causes = append(causes, fmt.Sprintf("hostname(s) %s already claimed by another route", strings.Join(lost, ", ")))
				reason = "HostnameConflict"
			}
			if isWithdrawn {
				answered, unserved := describeWithdrawn(gone)
				if answered != "" {
					causes = append(causes, fmt.Sprintf("hostname(s) %s answered by a TLS-passthrough listener, so no HTTPS listener is rendered for them", answered))
				}
				if unserved != "" {
					causes = append(causes, fmt.Sprintf("hostname(s) %s claimed only by a TLSRoute, which needs a passthrough listener this Gateway does not declare", unserved))
				}
			}
			if err := r.updateRouteParentStatus(ctx, ref, []metav1.Condition{
				{
					Type:    "Accepted",
					Status:  metav1.ConditionFalse,
					Reason:  reason,
					Message: fmt.Sprintf("On TenantGateway %s/%s: %s", tgw.Namespace, tgw.Name, strings.Join(causes, "; ")),
				},
			}); err != nil {
				logger.Error(err, "update unserved route status", "route", ref.namespace+"/"+ref.name)
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
