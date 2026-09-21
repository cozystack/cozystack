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
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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

// String names the kind the way the API does, for an error that has to
// say which object a write failed on.
func (k routeKind) String() string {
	switch k {
	case routeKindHTTP:
		return "HTTPRoute"
	case routeKindTLS:
		return "TLSRoute"
	default:
		return fmt.Sprintf("route kind %d", int(k))
	}
}

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
	// forwards records whether the route resolves at least one backend,
	// for routeKindTLS only — an HTTPRoute's is neither set nor read,
	// because no decision here consults an HTTPRoute's backends.
	//
	// It sits in a type used as a map key, which is safe because it is
	// derived once per route per pass: every ref built from one route
	// carries the same value, so the field cannot split that route's
	// entries across two keys.
	forwards bool
}

// servableOn reports whether ref could be served on hostname h by any
// passthrough listener this TenantGateway renders.
//
// The question is per route, not per hostname, and that is the whole
// point: a claim can overlap several reserved hostnames, so "some
// listener here admits every namespace" is true of the claim while
// being false of the listener this particular route asked for. A
// sectionName pins the route to one listener, and then only that
// listener's own hostname and attach set decide.
//
// byHostname maps each rendered passthrough hostname to the listener
// answering it, which carries the port it is published on and whether
// it admits the publishing tenant alone, as the native-port ones do.
// The caller is expected to have found at least one entry overlapping
// h before asking; a hostname nothing answers is a different shape,
// built by the caller with its own cause.
//
// sections maps a rendered passthrough listener name to the hostname it
// answers, and holds nothing else, so a sectionName absent from it
// names no passthrough listener. That covers two shapes and they do not
// get the same answer. A name carrying the passthrough prefix is one
// this controller renders the whole namespace of, so its absence is a
// fact: the route attaches to nothing, and it is told so. A name
// outside the prefix belongs to another listener family, the
// HTTPS-terminate ones among them, whose set is what the caller's loop
// is still computing; there the route is unserved for a reason this
// function does not model, so it comes back with withdrawnNone and the
// caller records no refusal: the route is then written Accepted like
// any attached route, and the class controller's own entry answers for
// the listener it named. Either way it is not
// servable, which matters on its own, because this verdict is also
// membership in the ownership race and a route that can never attach
// must not take a hostname away from one that can.
//
// On refusal the second return says which leg failed, because the two
// send an operator to different places and one of them can fire on a
// route inside the tenant, where a namespace refusal would contradict
// the object it is written on.
func servableOn(ref routeRef, h, tenantNamespace string, byHostname map[string]passthroughListener, sections map[string]string) (bool, withdrawalCause, string) {
	pinned := ""
	if ref.parentRef.SectionName != nil {
		name := string(*ref.parentRef.SectionName)
		named, exists := sections[name]
		if !exists {
			// Inside the passthrough listener namespace the answer is
			// knowable, because every listener there is rendered from
			// this spec: a name absent from sections names none, so the
			// route attaches to nothing whatever answers its hostname.
			// Outside it the name belongs to a listener family this
			// pass cannot enumerate — the terminate listeners are the
			// output of the loop this feeds, so their names are not
			// settled while it runs — and a refusal would be a guess.
			if strings.HasPrefix(name, passthroughListenerPrefix) {
				return false, withdrawnNoSuchSection, ""
			}
			return false, withdrawnNone, ""
		}
		pinned = named
	}
	// refusedBy is the name of a listener that matched and then turned
	// this route away, and it is the third return because the message
	// has to name that one. The caller's own pick is the smallest
	// overlapping name, chosen for stable text, and on a claim covering
	// several reserved names that is regularly a listener which would
	// have admitted the route. Smallest is also what makes it stable
	// here: the map iterates in no order, so without it two reconciles
	// could name two different refusers and rewrite the condition.
	refusedBy := ""
	sectionAnswers := false
	portAnswers := ref.parentRef.Port == nil
	for rh, l := range byHostname {
		if !hostnamesOverlap(rh, h) {
			continue
		}
		if pinned != "" && rh != pinned {
			continue
		}
		sectionAnswers = true
		// A parentRef may pin the listener by port as well as by name,
		// and Gateway API requires both to match when both are given.
		// Without this a route naming port 443 attaches to nothing on a
		// native-port listener and is still told it is fine.
		if ref.parentRef.Port != nil && int32(*ref.parentRef.Port) != l.port {
			continue
		}
		portAnswers = true
		if l.tenantOnly && ref.namespace != tenantNamespace {
			if refusedBy == "" || rh < refusedBy {
				refusedBy = rh
			}
			continue
		}
		return true, withdrawnNone, ""
	}
	// Reaching here with a pinned listener that never matched the name
	// means the sectionName is the reason, whatever the namespace is.
	if pinned != "" && !sectionAnswers {
		return false, withdrawnSectionMismatch, ""
	}
	// The name matched and the port did not. Reported apart from the
	// sectionName case because the field the route's owner edits is a
	// different one, and apart from the namespace case because the
	// route may well be inside the tenant.
	if !portAnswers {
		return false, withdrawnPortMismatch, ""
	}
	return false, withdrawnForeignNamespace, refusedBy
}

// httpClaimVerdict is what an HTTPRoute's claim on one hostname earns
// before the hostname race and the passthrough overlap are consulted.
type httpClaimVerdict int

const (
	// httpClaimTerminates: the claim stands, and earns the terminate
	// listener unless a passthrough listener takes the name.
	httpClaimTerminates httpClaimVerdict = iota
	// httpClaimServedPlain: the route selected the port-80 listener and
	// is admitted by it, so it is served there as plain HTTP. It earns
	// no terminate listener, which it would never attach to, and is
	// refused nothing, because it is served exactly as it asked.
	httpClaimServedPlain
	// httpClaimRefused: the route attaches to no listener that could
	// serve this hostname, for the cause carried in the returned
	// withdrawnHostname.
	httpClaimRefused
)

// judgeHTTPRouteClaim is servableOn's counterpart for an HTTPRoute:
// whether this route could be served on hostname h by a listener this
// TenantGateway renders for it. The listener in question is the HTTP-01
// terminate listener, published on httpsListenerPort, and the refusals
// are the ways the route can be unable to reach one.
//
// A sectionName naming a rendered passthrough listener pins the route
// to a listener that forwards a stream it never terminates and takes
// TLSRoute alone by protocol; one naming a passthrough listener this
// Gateway does not render pins it to nothing. A wildcard hostname is one HTTP-01 can
// terminate for nobody: the terminate listener carries a certificate
// this controller orders through the HTTP-01 challenge, and the issuers
// the platform accepts do not issue a wildcard through that challenge
// (Let's Encrypt, Challenge Types: "This challenge cannot be used to
// issue wildcard certificates"), so the order would never complete and
// the listener would reference a Secret that never arrives; the
// wildcard modes serve such a route from the apex-wide listener
// instead. That refusal is a statement about the terminate listener
// rather than about the route, so it is judged where that listener is
// decided rather than at the top: the port-80 listener carries no
// hostname and serves a wildcard claim like any other, and a route
// selecting it never needed the listener HTTP-01 cannot supply. No
// overlap is read either way, because no listener setting changes it.
// A parentRef port
// attaches the route to the listeners on that port alone, so a port the
// terminate listener is not published on is judged the way servableOn
// judges it for a TLSRoute, with the one port the Gateway answers HTTP
// on read for what it is: the port-80 listener carries no hostname and
// admits httpListenerNamespaces alone. A route selects it by pinning
// its port, by naming it, or both; from one of those namespaces the
// route is served plain, from anywhere else the listener turns it away
// and it is told so, and a name and a port that select different
// listeners, http with port 443 for one, select none, which Gateway API
// requires when both are given. The solver route cert-manager writes
// for the ACME challenge names the listener without a port, from the
// namespace the Certificate lives in, so it is served plain.
//
// A sectionName outside the passthrough prefix and not http names a
// listener family this pass is still deciding, and such a route is left
// to terminate as any unpinned one; servableOn says why.
//
// Which verdict comes first matters only for a route hit by more than
// one, and the order is the order in which the owner would have to act:
// a listener the sectionName names is refused whatever port it sits on,
// and a wildcard is refused only once the route is judged to want the
// terminate listener at all.
func judgeHTTPRouteClaim(ref routeRef, h string, tgw *gatewayv1alpha1.TenantGateway, sections map[string]string) (withdrawnHostname, httpClaimVerdict) {
	w := withdrawnHostname{hostname: h}
	hasSection := ref.parentRef.SectionName != nil
	section := ""
	if hasSection {
		section = string(*ref.parentRef.SectionName)
		if _, rendersIt := sections[section]; rendersIt {
			w.section = section
			w.cause = withdrawnKindNotAdmitted
			return w, httpClaimRefused
		}
		// Inside the passthrough namespace every listener is rendered
		// from this spec, so a name absent from sections names none,
		// and the route attaches to nothing whatever answers its
		// hostname; the same reading servableOn gives a TLSRoute.
		if strings.HasPrefix(section, passthroughListenerPrefix) {
			w.section = section
			w.cause = withdrawnNoSuchSection
			return w, httpClaimRefused
		}
	}
	hasPort := ref.parentRef.Port != nil
	var port int32
	if hasPort {
		port = int32(*ref.parentRef.Port)
	}
	namesHTTP := hasSection && section == httpListenerName
	pinsHTTPPort := hasPort && port == httpListenerPort
	switch {
	case (namesHTTP && hasPort && !pinsHTTPPort) || (pinsHTTPPort && hasSection && !namesHTTP):
		// The http listener is the only one on its port, so a name and
		// a port that disagree about it select nothing.
		w.section = section
		w.port = port
		w.cause = withdrawnSectionPortMismatch
		return w, httpClaimRefused
	case namesHTTP || pinsHTTPPort:
		if httpListenerAdmits(tgw, ref.namespace) {
			return w, httpClaimServedPlain
		}
		w.cause = withdrawnHTTPListenerNamespace
		return w, httpClaimRefused
	case !hasPort || port == httpsListenerPort:
		// Reached only once the route is judged to want the terminate
		// listener, which is the one HTTP-01 cannot carry a wildcard
		// certificate for. The branches above take the route that
		// asked for the port-80 listener instead, and that listener
		// serves a claim whatever shape its hostname has.
		if isWildcardHostname(h) {
			w.cause = withdrawnWildcardHostname
			return w, httpClaimRefused
		}
		return w, httpClaimTerminates
	}
	w.port = port
	w.cause = withdrawnPortMismatch
	return w, httpClaimRefused
}

// httpListenerAdmits reports whether the port-80 listener's namespace
// selector admits a route from namespace.
func httpListenerAdmits(tgw *gatewayv1alpha1.TenantGateway, namespace string) bool {
	return slices.Contains(httpListenerNamespaces(tgw), namespace)
}

// keepsLoss reports whether a refusal for cause leaves a loss the
// hostname race recorded on the route. A cause that describes a
// listener answering the name makes the race beside the point, so the
// loss goes with the hostname. A cause that says only what the route
// asked for, a section that names no listener or a listener that
// cannot take its kind, says nothing about who holds the name: the
// route took nothing from anyone, and the loss to whoever did claim it
// is as true as before.
func keepsLoss(cause withdrawalCause) bool {
	return cause == withdrawnNoSuchSection || cause == withdrawnKindNotAdmitted || cause == withdrawnSectionPortMismatch
}

// rankRouteRefs orders claimants the way ownership decides them:
// cozy-* namespaces first, then by namespace and name. Shared rather
// than inlined because a hostname a passthrough listener answers has
// its race recounted over the TLSRoutes alone, and a second copy of
// the order would let the two counts disagree about who won.
func rankRouteRefs(refs []routeRef) {
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
}

// dropLostHostname removes one hostname from a route's loser record,
// deleting the entry when nothing is left, so an empty slice never
// reads as "lost something".
func dropLostHostname(losers map[routeRef][]string, ref routeRef, hostname string) {
	if rest := slices.DeleteFunc(losers[ref], func(lost string) bool { return lost == hostname }); len(rest) > 0 {
		losers[ref] = rest
	} else {
		delete(losers, ref)
	}
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
		rankRouteRefs(refs)
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

// withdrawalCause names why the controller declined to serve a
// hostname. Named rather than inferred from an empty field, because
// the cases read differently to an operator and the difference decides
// where they go looking: a namespace refusal sends them to the attach
// list, a section mismatch to the route's own parentRef.
type withdrawalCause int

const (
	// withdrawnNone: no cause, returned alongside a servable verdict.
	// Explicit rather than the zero value of a real cause, so that
	// "nothing was withdrawn" cannot be read as a decision.
	withdrawnNone withdrawalCause = iota
	// withdrawnAnswered: a passthrough listener answers the name, so no
	// terminate listener is rendered for it.
	withdrawnAnswered
	// withdrawnUnanswered: nothing answers the name at all.
	withdrawnUnanswered
	// withdrawnForeignNamespace: a native-port listener answers the
	// name but admits only the tenant's own namespace, so this route
	// cannot attach to it however the hostname resolves.
	withdrawnForeignNamespace
	// withdrawnSectionMismatch: the route pinned itself to a listener
	// by sectionName, that listener exists, and it answers a different
	// hostname. Distinct from the namespace case because it happens to
	// routes inside the tenant too, where blaming the namespace states
	// something the object itself contradicts.
	withdrawnSectionMismatch
	// withdrawnPortMismatch: the route pinned itself to a listener by
	// parentRef.port, a listener answers its hostname, and it is
	// published on another port. Gateway API requires a pinned port to
	// match the selected listener, so the route attaches to nothing.
	withdrawnPortMismatch
	// withdrawnNoSuchSection: the route pinned itself by sectionName to
	// a passthrough listener this Gateway does not render. Separate
	// from withdrawnSectionMismatch, where the named listener exists
	// and answers another hostname: there the operator moves the route
	// or the hostname, here the name itself is wrong.
	withdrawnNoSuchSection
	// withdrawnKindNotAdmitted: an HTTPRoute pinned itself by
	// sectionName to a rendered passthrough listener, which forwards a
	// stream it never terminates and takes TLSRoute alone by protocol.
	// The only cause that judges a route's kind, and the only one an
	// HTTPRoute reaches through its sectionName; the section causes
	// above are reached by TLSRoutes, whose kind a passthrough listener
	// does take.
	withdrawnKindNotAdmitted
	// withdrawnWildcardHostname: an HTTPRoute claimed a wildcard under
	// certMode http01, whose terminate listener would carry a
	// certificate the HTTP-01 challenge cannot issue for a wildcard
	// name. Refused before any overlap is read, because no listener
	// setting changes it; the wildcard modes serve such a route from
	// the apex-wide listener.
	withdrawnWildcardHostname
	// withdrawnHTTPListenerNamespace: an HTTPRoute selected the port-80
	// listener, by port or by name, from a namespace that listener does
	// not admit. The listener answered and turned the route away, the
	// shape withdrawnForeignNamespace has on a native-port listener, kept
	// apart because the message names a different listener and a
	// different attach set.
	withdrawnHTTPListenerNamespace
	// withdrawnSectionPortMismatch: an HTTPRoute pinned a sectionName and
	// a parentRef port that select different listeners. Gateway API
	// requires both to match one listener when both are given, so the
	// route attaches to nothing, whatever answers its hostname.
	withdrawnSectionPortMismatch
)

// withdrawnHostname pairs a hostname the controller declined to serve
// with the passthrough hostname involved, empty when the cause is that
// nothing answers it. The pair is carried rather than the claimed name
// alone because a wildcard entry is not derivable from the route: the
// route claims pg.db.<apex> and the spec that took it away says
// *.db.<apex>.
type withdrawnHostname struct {
	hostname   string
	answeredBy string
	cause      withdrawalCause
	// section carries the sectionName that missed, for
	// withdrawnSectionMismatch and withdrawnNoSuchSection. The message
	// has to name it, because it is the field the route's owner edits.
	// answeredBy then carries what the named listener does answer, and
	// is empty for withdrawnNoSuchSection, where it answers nothing.
	section string
	// port carries the parentRef port that missed, for
	// withdrawnPortMismatch only, with answeredBy naming the listener
	// that does answer the hostname on a port of its own.
	port int32
}

// describeWithdrawn renders the pairs in a stable order, dropping
// repeats: one route may list a hostname twice, since Gateway API
// declares spec.hostnames a plain array. The message is rebuilt on
// every reconcile, so an unstable order would rewrite the condition
// each pass and churn the route's status forever.
//
// Each cause is rendered separately, because a route hit by more than
// one of them has to be told about each.
func describeWithdrawn(hostnames []withdrawnHostname) withdrawnByCause {
	var withListener, without, elsewhere, wrongSection, onAnotherPort, noSuchSection, refusedKind, wildcard, httpRefused, sectionPort []string
	for _, h := range hostnames {
		switch h.cause {
		case withdrawnHTTPListenerNamespace:
			httpRefused = append(httpRefused, h.hostname)
		case withdrawnSectionPortMismatch:
			sectionPort = append(sectionPort, fmt.Sprintf("%s (sectionName %s, port %d)", h.hostname, h.section, h.port))
		case withdrawnWildcardHostname:
			wildcard = append(wildcard, h.hostname)
		case withdrawnKindNotAdmitted:
			refusedKind = append(refusedKind, fmt.Sprintf("%s (sectionName %s)", h.hostname, h.section))
		case withdrawnNoSuchSection:
			noSuchSection = append(noSuchSection, fmt.Sprintf("%s (sectionName %s)", h.hostname, h.section))
		case withdrawnSectionMismatch:
			wrongSection = append(wrongSection, fmt.Sprintf("%s (sectionName %s answers %s)", h.hostname, h.section, h.answeredBy))
		case withdrawnPortMismatch:
			onAnotherPort = append(onAnotherPort, fmt.Sprintf("%s (port %d, answered on another port)", h.hostname, h.port))
		case withdrawnUnanswered:
			without = append(without, h.hostname)
		case withdrawnForeignNamespace:
			// The pair is only worth printing when it says something the
			// hostname does not: for a wildcard claim the matched entry
			// is a different string, for a concrete one it is the same.
			if h.answeredBy == h.hostname {
				elsewhere = append(elsewhere, h.hostname)
			} else {
				elsewhere = append(elsewhere, fmt.Sprintf("%s (matched by %s)", h.hostname, h.answeredBy))
			}
		case withdrawnAnswered:
			withListener = append(withListener, fmt.Sprintf("%s (answered by %s)", h.hostname, h.answeredBy))
		}
	}
	sort.Strings(withListener)
	sort.Strings(without)
	sort.Strings(elsewhere)
	sort.Strings(wrongSection)
	sort.Strings(onAnotherPort)
	sort.Strings(noSuchSection)
	sort.Strings(refusedKind)
	sort.Strings(wildcard)
	sort.Strings(httpRefused)
	sort.Strings(sectionPort)
	return withdrawnByCause{
		answered:      strings.Join(slices.Compact(withListener), ", "),
		unserved:      strings.Join(slices.Compact(without), ", "),
		foreign:       strings.Join(slices.Compact(elsewhere), ", "),
		mismatched:    strings.Join(slices.Compact(wrongSection), ", "),
		wrongPort:     strings.Join(slices.Compact(onAnotherPort), ", "),
		absentSection: strings.Join(slices.Compact(noSuchSection), ", "),
		refusedKind:   strings.Join(slices.Compact(refusedKind), ", "),
		wildcard:      strings.Join(slices.Compact(wildcard), ", "),
		httpRefused:   strings.Join(slices.Compact(httpRefused), ", "),
		sectionPort:   strings.Join(slices.Compact(sectionPort), ", "),
	}
}

// withdrawnByCause holds one rendered hostname list per refusal cause,
// each empty when no hostname on the route hit that cause. Named fields
// rather than a return list: every one is a string, so a positional
// form binds one interchangeable value per cause at the call site and a
// swapped pair compiles.
type withdrawnByCause struct {
	answered      string
	unserved      string
	foreign       string
	mismatched    string
	wrongPort     string
	absentSection string
	refusedKind   string
	wildcard      string
	httpRefused   string
	sectionPort   string
}

// updateRouteStatuses writes RouteParentStatus entries under our
// ControllerName, one per (route, parentRef) tuple that attached to
// this TenantGateway. Accepted=True for tuples not in losers,
// Accepted=False with Reason=HostnameConflict for tuples that lost
// at least one hostname race. Other controllers' entries (Cilium
// etc.) are untouched.
//
// attached is every (route, parentRef) tuple collectHostnameClaims
// found on this Gateway, and claimed the subset that carried a
// hostname. Both are needed and neither is derivable from the map of
// claims: claimed keyed by tuple rather than by hostname is what gives
// a multi-parentRef route an entry per ref instead of one for whichever
// ref won the per-hostname race, and the difference between the two
// sets is the tuples this controller has to stop having an opinion on.
//
// withdrawn carries the tuples the controller declined to serve. None
// of them lost a race, so HostnameConflict would misname the cause,
// and what separates the rest is how precisely Gateway API can name
// the refusal. Two of Gateway API's own reasons are reached:
// NotAllowedByListeners, where a listener matches the route and turns
// it away, refusing its namespace, on a native-port listener or on the
// port-80 one, or, for an HTTPRoute pinning a passthrough listener,
// being unable to serve its kind; and NoMatchingParent, for a
// sectionName naming a passthrough listener this Gateway does not
// render, or a sectionName and a parentRef port that select different
// listeners, which is what Gateway API calls a parentRef whose port or
// section matches no listener. The rest land on
// NoMatchingListenerHostname, which is where the residue goes rather
// than a shape in its own right; the shapes are enumerated on the
// withdrawalCause constants, so a cause added later cannot slip past a
// list here that nobody updated. Reading that reason as the absence of
// a listener is wrong for most of what reaches it: a passthrough
// listener answers the SNI and, in the port-443 form, admits routes
// from every namespace attached to this Gateway, and a listener a route
// pins by the wrong sectionName or the wrong parentRef port is rendered
// and serving, just not to this route. Either way the route condition is the only
// object left that can say why, the listener that used to carry a
// condition of its own having gone with it.
//
// A write that fails is returned, after every other route has had its
// write, rather than logged. Returned, because the condition is the
// only object saying why a hostname stopped being served, and a pass
// that reports success over a lost write leaves the route reading
// Accepted=True until the resync; failing the reconcile puts the write
// under its own requeue. After the others, because one route's
// conflict is no reason to leave the rest a pass behind.
func (r *Reconciler) updateRouteStatuses(
	ctx context.Context,
	tgw *gatewayv1alpha1.TenantGateway,
	attached map[routeRef]struct{},
	claimed map[routeRef]struct{},
	losers map[routeRef][]string,
	withdrawn map[routeRef][]withdrawnHostname,
) error {
	var errs []error

	// LastTransitionTime is set by apimeta.SetStatusCondition inside
	// mergeRouteParentStatus only when the condition actually
	// transitions; building Conditions here without it keeps the
	// no-op reconcile no-op.
	//
	// Walked over what attached rather than over what claimed, so the
	// two outcomes are branches of one decision and a ref cannot take
	// both. Claiming nothing is not a stable property of a route: a
	// TLSRoute with no spec.hostnames borrows the hostname of each
	// passthrough listener its parentRef selects, so removing the entry
	// behind it empties the claim set without the route changing at all,
	// and an HTTPRoute reaches the same place by dropping its own
	// hostnames. Judging only what claims would leave the previous
	// pass's verdict standing over a listener set that no longer exists,
	// which is what the whole-apex pass drops entries to avoid; the
	// difference is that here the mode never changed. Retracting says no
	// more than the controller can support, and it is what a route in
	// this shape gets on a first pass, so nothing depends on which pass
	// it arrived by.
	for ref := range attached {
		if _, judged := claimed[ref]; !judged {
			if err := r.dropRouteParentStatus(ctx, ref); err != nil {
				errs = append(errs, fmt.Errorf("drop status of unclaimed %s %s/%s: %w", ref.kind, ref.namespace, ref.name, err))
			}
			continue
		}
		lost, isLoser := losers[ref]
		gone, isWithdrawn := withdrawn[ref]
		if isLoser || isWithdrawn {
			// One route can be hit by several causes on different
			// hostnames, and Gateway API gives it a single Accepted
			// condition, so the message carries all of them rather
			// than whichever branch runs first. Only the reason has
			// to choose.
			var causes []string
			// The reason names the shape of the refusal, and the causes
			// below do not share one. Nothing rendering a listener for
			// the name is NoMatchingListenerHostname, the default here
			// and the reason a mixed set of causes settles on, because
			// a reason true of one claim misleads about the rest. A
			// race lost to another route reports HostnameConflict even
			// mixed with others, because it is the half the owner can
			// act on. A listener matching the name while refusing the
			// route's namespace is NotAllowedByListeners, which is
			// what Gateway API calls that case, and only when it is
			// the whole story.
			reason := string(gatewayv1.RouteReasonNoMatchingListenerHostname)
			if isLoser {
				// Sorted for the same reason describeWithdrawn sorts:
				// the hostnames come out of a map, and a message that
				// reorders between passes rewrites the condition, which
				// writes status, which requeues this object through the
				// route watch.
				sort.Strings(lost)
				// Compacted for the same reason describeWithdrawn
				// compacts: spec.hostnames is a plain array, so one
				// route may list a name twice, and the message would
				// then say it twice.
				causes = append(causes, fmt.Sprintf("hostname(s) %s already claimed by another route", strings.Join(slices.Compact(lost), ", ")))
				reason = "HostnameConflict"
			}
			if isWithdrawn {
				desc := describeWithdrawn(gone)
				if desc.absentSection != "" {
					causes = append(causes, fmt.Sprintf("hostname(s) %s pinned by sectionName to a TLS-passthrough listener this Gateway does not render", desc.absentSection))
				}
				if desc.refusedKind != "" {
					causes = append(causes, fmt.Sprintf("hostname(s) %s pinned by sectionName to a TLS-passthrough listener, which terminates nothing, so no HTTPRoute is served through it", desc.refusedKind))
				}
				if desc.mismatched != "" {
					causes = append(causes, fmt.Sprintf("hostname(s) %s not served through the sectionName this route names, which answers a different hostname", desc.mismatched))
				}
				if desc.wrongPort != "" {
					causes = append(causes, fmt.Sprintf("hostname(s) %s not served through the parentRef port this route names, which no listener answering them is published on", desc.wrongPort))
				}
				if desc.answered != "" {
					causes = append(causes, fmt.Sprintf("hostname(s) %s answered by a TLS-passthrough listener, so no HTTPS listener is rendered for them", desc.answered))
				}
				if desc.unserved != "" {
					causes = append(causes, fmt.Sprintf("hostname(s) %s claimed only by a TLSRoute, which needs a passthrough listener this Gateway does not declare", desc.unserved))
				}
				if desc.wildcard != "" {
					causes = append(causes, fmt.Sprintf("hostname(s) %s are wildcards, which certMode http01 cannot terminate: the HTTP-01 challenge issues no wildcard certificate, so no HTTPS listener is rendered for them", desc.wildcard))
				}
				if desc.sectionPort != "" {
					causes = append(causes, fmt.Sprintf("hostname(s) %s pinned by sectionName and by parentRef port to listeners that are not one, so they select none", desc.sectionPort))
				}
				if desc.httpRefused != "" {
					causes = append(causes, fmt.Sprintf("hostname(s) %s pinned to the %s listener, which admits routes from namespace(s) %s only", desc.httpRefused, httpListenerName, strings.Join(httpListenerNamespaces(tgw), ", ")))
				}
				if desc.foreign != "" {
					causes = append(causes, fmt.Sprintf("hostname(s) %s served by a native-port listener that admits routes from namespace %s only", desc.foreign, tgw.Namespace))
				}
				// Each reason below is checked after every append rather
				// than beside its own, and counted rather than
				// enumerated: a reason more precise than the default is
				// true only when its cause is the whole story, and one
				// entry says so whatever order the appends ran in, so a
				// cause added later cannot slip past a list nobody
				// updated. A listener that answers the route and turns it
				// away, refusing its namespace or unable to serve its
				// kind, is NotAllowedByListeners; a parentRef selecting
				// no listener, a section this Gateway does not render or
				// a name and a port that disagree, is NoMatchingParent.
				// One route cannot carry two of these on one hostname,
				// so at most one of them fires.
				if len(causes) == 1 {
					switch {
					case desc.foreign != "", desc.refusedKind != "", desc.httpRefused != "":
						reason = string(gatewayv1.RouteReasonNotAllowedByListeners)
					case desc.absentSection != "", desc.sectionPort != "":
						reason = string(gatewayv1.RouteReasonNoMatchingParent)
					}
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
				errs = append(errs, fmt.Errorf("update status of unserved %s %s/%s: %w", ref.kind, ref.namespace, ref.name, err))
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
			errs = append(errs, fmt.Errorf("update status of %s %s/%s: %w", ref.kind, ref.namespace, ref.name, err))
		}
	}
	return errors.Join(errs...)
}

// reconcileWholeApexRouteStatuses keeps this controller's Accepted
// condition on attached routes honest in the modes where
// updateRouteStatuses above says nothing. collectHostnameClaims returns
// no claims for a whole-apex mode, so that pass runs over an empty ref
// set and neither writes nor retracts anything.
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
// The refusal skips a route that declares no hostnames. Under HTTP-01
// such a route is judged on the hostname the listener it selects lends
// it, but that substitution needs a rendered passthrough listener, and
// these modes are exactly the ones where the route's sectionName may
// name none. A refusal written here would then have no path back: the
// HTTP-01 pass judges only routes that produced a claim, so a route
// pinned to a section it renders no listener for would carry an edge
// refusal after the tenant moved off edge. The drop does not skip it:
// the HTTP-01 pass did judge it, on the lent name, and that verdict
// has no other way out once the mode moves, so what edge writes on such
// a route is no opinion rather than a refusal.
//
// HTTPRoutes are dropped in every whole-apex mode. Every condition this
// controller writes on one is written under HTTP-01, where the claims
// pass runs, and the whole-apex modes serve those routes from the
// apex-wide listener, so a verdict left standing describes a listener
// set that no longer exists: a wildcard refused under http01 kept
// saying http01 could not terminate it while dns01 was serving it. The
// redirect route this controller renders never carries an entry of its
// own, so it is skipped rather than read.
func (r *Reconciler) reconcileWholeApexRouteStatuses(ctx context.Context, tgw *gatewayv1alpha1.TenantGateway) error {
	if !servesWholeApex(tgw) {
		return nil
	}
	withdraw := !rendersTLSPassthrough(tgw)
	// Collected and returned after the loop, for the reason
	// updateRouteStatuses gives: the refusal written here is the only
	// object saying the route attaches to nothing, so a lost write has
	// to fail the reconcile, and one route's conflict must not leave
	// the rest unwritten.
	var errs []error

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
		for _, parentRef := range allAttachingParentRefs(route.Spec.ParentRefs, route.Namespace, tgw) {
			ref := routeRef{
				kind:      routeKindTLS,
				namespace: route.Namespace,
				name:      route.Name,
				parentRef: parentRef,
			}
			var err error
			if withdraw && len(route.Spec.Hostnames) > 0 {
				err = r.updateRouteParentStatus(ctx, ref, []metav1.Condition{
					{
						Type:    "Accepted",
						Status:  metav1.ConditionFalse,
						Reason:  string(gatewayv1.RouteReasonNoMatchingParent),
						Message: fmt.Sprintf("TenantGateway %s/%s terminates TLS at its class provider and renders no TLS-passthrough listener", tgw.Namespace, tgw.Name),
					},
				})
			} else {
				err = r.dropRouteParentStatus(ctx, ref)
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("update status of %s %s/%s: %w", ref.kind, ref.namespace, ref.name, err))
			}
		}
	}

	httpRoutes := &gatewayv1.HTTPRouteList{}
	if err := r.List(ctx, httpRoutes); err != nil {
		return fmt.Errorf("list HTTPRoutes: %w", err)
	}
	for i := range httpRoutes.Items {
		route := &httpRoutes.Items[i]
		if _, ok := allowed[route.Namespace]; !ok {
			continue
		}
		if isHTTPRedirectRoute(route, tgw) {
			continue
		}
		for _, parentRef := range allAttachingParentRefs(route.Spec.ParentRefs, route.Namespace, tgw) {
			ref := routeRef{
				kind:      routeKindHTTP,
				namespace: route.Namespace,
				name:      route.Name,
				parentRef: parentRef,
			}
			if err := r.dropRouteParentStatus(ctx, ref); err != nil {
				errs = append(errs, fmt.Errorf("update status of %s %s/%s: %w", ref.kind, ref.namespace, ref.name, err))
			}
		}
	}
	return errors.Join(errs...)
}

// dropRouteParentStatus removes this controller's RouteParentStatus
// entry for ref, leaving every other controller's entry in place. Same
// idempotency contract as updateRouteParentStatus: the Status().Update
// is issued only when the entry was actually there.
func (r *Reconciler) dropRouteParentStatus(ctx context.Context, ref routeRef) error {
	switch ref.kind {
	case routeKindHTTP:
		route := &gatewayv1.HTTPRoute{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: ref.namespace, Name: ref.name}, route); err != nil {
			return fmt.Errorf("get HTTPRoute: %w", err)
		}
		before := route.DeepCopy()
		removeRouteParentStatus(&route.Status.Parents, ref.parentRef)
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
		removeRouteParentStatus(&route.Status.Parents, ref.parentRef)
		if routeParentStatusEqual(before.Status.Parents, route.Status.Parents) {
			return nil
		}
		return r.Status().Update(ctx, route)
	default:
		return fmt.Errorf("unknown route kind %d for %s/%s", ref.kind, ref.namespace, ref.name)
	}
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

// routeStatusWriteError wraps the failures of the route-status steps so
// the Reconcile wrapper can tell a write that failed there apart from a
// desired-state step failing, without matching on message text.
type routeStatusWriteError struct{ err error }

func (e routeStatusWriteError) Error() string { return e.err.Error() }
func (e routeStatusWriteError) Unwrap() error { return e.err }

// retryableRouteWrite reports whether every failure in err is one the
// next pass can outlive on its own: a conflict, meaning the class
// controller wrote the same route between this pass's read and its
// write, or a route that vanished in the same gap. Both are byproducts
// of two writers sharing one status list, and neither says anything
// about the tenant, so the reconcile requeues instead of pinning
// Ready=False on the TenantGateway. Anything else — an Invalid the
// apiserver will reject on every pass, a Forbidden — stays fatal, and
// one fatal leaf fails the whole join: a mixed batch must not be
// retried into silence.
func retryableRouteWrite(err error) bool {
	if err == nil {
		return false
	}
	if u, ok := err.(interface{ Unwrap() []error }); ok {
		for _, leaf := range u.Unwrap() {
			if !retryableRouteWrite(leaf) {
				return false
			}
		}
		return true
	}
	return apierrors.IsConflict(err) || apierrors.IsNotFound(err)
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
