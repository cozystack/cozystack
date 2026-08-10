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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	cmacmev1 "github.com/cert-manager/cert-manager/pkg/apis/acme/v1"
	cmv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmetav1 "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	gatewayv1alpha1 "github.com/cozystack/cozystack/api/gateway/v1alpha1"
)

// Label keys / values written by this controller. Hoisted to consts
// to keep occurrences in sync (CI's goconst flags ≥2 duplicates).
const (
	cozystackManagedByLabel   = "cozystack.io/managed-by"
	cozystackManagedByValue   = "cozystack-controller"
	cozystackTenantGatewayKey = "cozystack.io/tenantgateway"
	cozystackPerListenerCert  = "cozystack.io/per-listener-cert"

	// namespaceGatewayLabel marks a Namespace as attaching to the
	// Gateway owned by the tenant named in its value. Apps/tenant
	// chart writes it via namespace.yaml (own name when owning a
	// Gateway, inherited ancestor name otherwise); cozystack-
	// controller patches it onto every namespace in
	// TenantGateway.Spec.AttachedNamespaces so cozy-* system
	// namespaces (cert-manager, monitoring, harbor, …) reach the
	// publishing Gateway alongside the tenant tree.
	namespaceGatewayLabel = "namespace.cozystack.io/gateway"

	// namespaceGatewayManagedByAnnotation tags namespaces the
	// controller wrote namespaceGatewayLabel onto. Labels without
	// this annotation are Helm-owned (apps/tenant chart) and the
	// controller MUST NOT strip them — stripping a chart-written
	// label would break inheritance for every child tenant under
	// this Gateway every reconcile cycle. The annotation also
	// scopes GC to "labels this specific TenantGateway wrote": if
	// two TGWs ever shared an attached namespace name (they
	// can't, but defensively), each only manages its own writes.
	namespaceGatewayManagedByAnnotation = "cozystack.io/gateway-attached-by"
)

// acmeServerForIssuer maps the operator-facing issuerName field to
// the concrete ACME server URL. Empty → default to letsencrypt-prod
// to match the CRD default and the historical chart behaviour.
func acmeServerForIssuer(name gatewayv1alpha1.IssuerName) (string, error) {
	switch name {
	case "", gatewayv1alpha1.IssuerNameLetsEncryptProd:
		return letsencryptProdServer, nil
	case gatewayv1alpha1.IssuerNameLetsEncryptStage:
		return letsencryptStageServer, nil
	default:
		return "", fmt.Errorf("unsupported issuerName %q (supported: letsencrypt-prod, letsencrypt-stage)", name)
	}
}

// acmeChallengeNamespace is the namespace cert-manager publishes
// HTTP-01 challenge HTTPRoutes from. Hardcoded to the cozystack
// platform default; if you ever move cert-manager out of cozy-cert-
// manager, add a TenantGateway spec field to override this.
const acmeChallengeNamespace = "cozy-cert-manager"

// buildAllowedRoutes computes the AllowedRoutes block applied to the
// HTTPS-terminate and port-443 passthrough listeners, and to nothing
// else: the native-port listeners from tlsPassthroughListeners take a
// narrower one from allowedRoutesFromValues. It is a label selector
// matching
// namespace.cozystack.io/gateway = <tgw.Namespace>. Every namespace
// carrying that label attaches to this Gateway. The label has two
// writers:
//
//   - apps/tenant chart namespace.yaml — every tenant namespace
//     gets the label pointing at the nearest ancestor that owns a
//     Gateway (self if owning, inherited otherwise). This is how
//     child tenants attach without their own LB IP / Certificate.
//   - cozystack-controller (see ensureNamespaceLabels in
//     reconciler.go) — patches the label onto every namespace
//     in tgw.Spec.AttachedNamespaces so cozy-* system namespaces
//     reach the Gateway alongside the tenant tree.
//
// The previous shape pinned a static `kubernetes.io/metadata.name
// In [list]` whitelist. That foreclosed inheritance because a child
// tenant's namespace was not literally on the list. The label-
// based selector restores inheritance parity with the legacy
// ingress flow and matches the upstream Gateway API multi-tenancy
// pattern (Kamaji, GKE, Istio Ambient).
func buildAllowedRoutes(tgw *gatewayv1alpha1.TenantGateway) *gatewayv1.AllowedRoutes {
	from := gatewayv1.NamespacesFromSelector
	return &gatewayv1.AllowedRoutes{
		Namespaces: &gatewayv1.RouteNamespaces{
			From: &from,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					namespaceGatewayLabel: tgw.Namespace,
				},
			},
		},
	}
}

// buildHTTPListenerAllowedRoutes returns a strictly narrower
// allowedRoutes for the port-80 listener: only the tenant namespace
// (the controller-owned http→https redirect HTTPRoute lives there)
// and the cert-manager challenge namespace (HTTP-01 ACME challenges
// publish a transient HTTPRoute under /.well-known/acme-challenge/).
//
// Why: app HTTPRoutes (harbor, keycloak, dashboard, bucket) attach
// by hostname with no sectionName, so without this narrower filter
// they would also bind to the HTTP listener — Gateway API
// tie-breaks merged routes by creationTimestamp, so an app route
// created before the controller's redirect would silently serve
// plaintext on port 80 and leak credentials. Restricting the HTTP
// listener's allowedRoutes namespaces excludes the cozy-* / tenant-*
// namespaces apps live in, while keeping cert-manager's challenge
// namespace open so ACME still completes.
func buildHTTPListenerAllowedRoutes(tgw *gatewayv1alpha1.TenantGateway) *gatewayv1.AllowedRoutes {
	values := []string{tgw.Namespace}
	if acmeChallengeNamespace != tgw.Namespace {
		values = append(values, acmeChallengeNamespace)
	}
	return allowedRoutesFromValues(values)
}

func allowedRoutesFromValues(values []string) *gatewayv1.AllowedRoutes {
	from := gatewayv1.NamespacesFromSelector
	return &gatewayv1.AllowedRoutes{
		Namespaces: &gatewayv1.RouteNamespaces{
			From: &from,
			Selector: &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{
						Key:      "kubernetes.io/metadata.name",
						Operator: metav1.LabelSelectorOpIn,
						Values:   values,
					},
				},
			},
		},
	}
}

// hostnameFirstLabel returns the first DNS label of a hostname (the
// part before the first '.'), normalised to lowercase. The lowercase
// pass is defensive: Gateway listener names and Certificate object
// names both must satisfy RFC 1123 (lowercase), so an upper-case
// input hostname like `HARBOR.foo.example.com` would otherwise
// produce an invalid listener name `https-HARBOR-...`. The upstream
// Gateway API admission webhook normalises hostnames already, but
// running ToLower here matches what hostnameSuffix does and keeps
// the contract local.
func hostnameFirstLabel(hostname string) string {
	hostname = strings.ToLower(hostname)
	if i := strings.Index(hostname, "."); i >= 0 {
		return hostname[:i]
	}
	return hostname
}

// hostnameSuffix returns a short stable suffix derived from the full
// hostname so that two routes whose first label collides
// ("harbor.foo.example.com" vs "harbor.alice.example.com") produce
// distinct listener / cert names. Without this suffix, listener
// admission rejects the second listener with a duplicate-name error
// and the entire Gateway becomes Programmed=False.
func hostnameSuffix(hostname string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(hostname)))
	return hex.EncodeToString(sum[:4])
}

// passthroughListenerPrefix is the "tls-" prefix both passthrough
// render loops (TLSPassthroughServices and TLSPassthroughListeners) put
// in front of their identifier to form the Gateway listener name.
// Hoisted to a const so the collision check below and the two render
// sites can never drift apart.
const passthroughListenerPrefix = "tls-"

// reservedGatewayPorts are the ports renderGateway always occupies with
// its own listeners: 80 (the http listener carrying the ACME challenge
// and the http->https redirect) and 443 (the HTTPS-terminate listeners
// and the port-443 TLSPassthroughServices listeners). A native-port
// passthrough listener must avoid both: a TLS listener on port 80
// alongside the HTTP listener, or on 443 alongside a terminate listener
// for the same hostname, is a protocol conflict. Gateway API admits
// either — its listener uniqueness rule keys on (port, protocol,
// hostname), so differing protocols are distinct — and the conflict
// surfaces as Conflicted on the listeners, which then serve nothing.
// Rejecting the entry up front turns that into a per-field error on
// TenantGateway status instead.
var reservedGatewayPorts = map[int32]struct{}{80: {}, 443: {}}

// maxGatewayListeners mirrors the MaxItems Gateway API declares on
// Gateway.spec.listeners. Exceeding it fails admission on the whole
// Gateway, so renderGateway checks the assembled total against it.
const maxGatewayListeners = 64

// validatePassthroughListenerCertMode refuses passthrough listeners in
// the two wildcard certificate modes.
//
// It judges the field against a sibling field rather than against
// itself, which is why it is separate from
// validateTLSPassthroughListeners. dns01 and existingSecret serve the
// tenant from one terminate listener for "*.<apex>" on port 443 instead
// of one per published hostname, and a passthrough hostname has to sit
// inside the apex, so that wildcard SNI-intersects every entry this
// field can hold. Suppression cannot answer it either: those modes
// render no per-hostname listener to withdraw, and the wildcard covers
// the whole apex.
//
// The intersection matters because the Cilium this repo pins does not
// keep the two apart. v1.19.5 collapses the Gateway into a single Envoy
// listener, and toFilterChainMatch in
// operator/pkg/model/translation/envoy_listener.go matches on
// transport_protocol and server_names only, so the terminate chain for
// "*.<apex>" and the passthrough chain for a name under it end up in one
// listener with an exact match winning over the wildcard. Upstream
// states the consequence above NeedsCrossProtocolSplit in
// operator/pkg/model/model.go as of v1.19.6: a combined Envoy listener
// "would otherwise erase the original Gateway listener port boundary and
// route traffic for one listener to another". A TLS connection arriving
// on 443 for that name reaches the database backend.
//
// v1.19.6 splits the Envoy listeners per port and this stops being
// true, though not unconditionally: NeedsPerPortListeners requires
// more than one HTTPS port, or more than one routed passthrough port,
// or a cross-port SNI overlap. A tenant keeping any shipped
// tlsPassthroughServices entry has a routed passthrough listener on
// 443 already, so a native-port entry satisfies the second of those.
// Lifting the refusal is then deleting a check, with no API or schema
// change, and the pin is the thing to watch:
// packages/system/cilium/images/cilium/Dockerfile.
func validatePassthroughListenerCertMode(listeners []gatewayv1alpha1.TLSPassthroughListener, mode gatewayv1alpha1.CertMode) error {
	if len(listeners) == 0 {
		return nil
	}
	if mode != gatewayv1alpha1.CertModeDNS01 && mode != gatewayv1alpha1.CertModeExistingSecret {
		return nil
	}
	return fmt.Errorf("tlsPassthroughListeners: unsupported with certMode %q; that mode serves the tenant from one wildcard terminate listener covering every hostname under the apex, and the pinned Cilium routes both by SNI alone in one Envoy listener, so a connection on 443 would reach the passthrough backend", mode)
}

// passthroughHostnames returns every hostname a passthrough listener on
// this Gateway claims: "<svc>.<apex>" for each TLSPassthroughServices
// entry and the declared Hostname of each TLSPassthroughListeners entry.
// Both render from the spec alone, with no route involved, so the set is
// known before any claim is collected.
//
// None of them may also get an HTTPS-terminate listener, and the reason
// differs by field only in which layer refuses.
//
// A TLSPassthroughServices entry shares port 443 with the terminate
// listeners, so a hostname claimed by both produces two listeners on one
// port under one name. Gateway API admits the pair and then requires both
// to report Conflicted, and neither serves.
//
// A TLSPassthroughListeners entry sits on its own port, and that is not
// the protection it looks like on the Cilium this repo pins. v1.19.5
// (packages/system/cilium/images/cilium/Dockerfile) translates the whole
// Gateway into a single Envoy listener and hangs the ports off
// AdditionalAddresses; toFilterChainMatch in
// operator/pkg/model/translation/envoy_listener.go matches on
// transport_protocol and server_names and nothing else, so the Gateway
// listener's port never reaches the match. Two chains carrying one SNI
// from two Gateway ports become two chains with identical criteria in one
// Envoy listener. Upstream says what that costs, in
// operator/pkg/model/model.go on v1.19.6, above NeedsCrossProtocolSplit:
// a combined Envoy listener "would otherwise erase the original Gateway
// listener port boundary and route traffic for one listener to another".
// v1.19.6 answers it by splitting the Envoy listeners per port when
// NeedsPerPortListeners holds, which a Gateway carrying both a
// port-443 passthrough listener and a native-port one does; v1.19.5
// has neither the split nor the diagnostic, so the answer here is to
// keep the pair from being rendered. Revisit when the pin moves.
//
// The cost is that an HTTPRoute claiming a hostname declared here gets
// no listener. Nothing hostile is needed to reach it:
// tlsPassthroughServices is a chart value shipped defaulted to api,
// vm-exportproxy and cdi-uploadproxy, so a tenant app named after one of
// them collides with a platform default. Suppression is not what breaks
// that hostname — before this filter the same collision rendered the
// Conflicted pair, equally unserved — but it does take away the
// Conflicted condition, which was the one place the collision was
// visible on the Gateway. updateRouteStatuses puts it back on the route
// instead, as Accepted=False with NoMatchingListenerHostname naming the
// passthrough hostname that answers the claim.
//
// The caller matches a claimed hostname against this set by SNI overlap
// rather than by equality, because a "*.db.<apex>" entry answers
// "pg.db.<apex>" on the pinned Cilium exactly as an explicit entry would:
// the filter chain match carries ServerNames and no port. Comparing by
// equality leaves that pair rendered and exposed to the translation this
// filter exists to avoid. Withdrawing the whole subtree is the intended
// reading of a wildcard entry, which declares that everything under the
// name bypasses termination.
func passthroughHostnames(tgw *gatewayv1alpha1.TenantGateway) map[string]struct{} {
	out := make(map[string]struct{}, len(tgw.Spec.TLSPassthroughServices)+len(tgw.Spec.TLSPassthroughListeners))
	for _, svc := range tgw.Spec.TLSPassthroughServices {
		out[svc+"."+tgw.Spec.Apex] = struct{}{}
	}
	for _, pl := range tgw.Spec.TLSPassthroughListeners {
		out[pl.Hostname] = struct{}{}
	}
	return out
}

// validateTLSPassthroughListeners enforces the cross-field invariants on
// spec.tlsPassthroughListeners that the CRD schema cannot express on its
// own: DNS-1123 label names unique across the list AND not colliding
// with a name that spec.tlsPassthroughServices already renders as a
// tls-<svc> listener; ports in 1..65535, unique across the list, and
// never one of the reserved Gateway ports (80/443); and hostnames that
// are a syntactically valid exact RFC 1123 domain or left-most-label
// wildcard AND fall within the tenant apex. It returns a descriptive
// error on the first violation so the reconcile fails loudly — markFailed
// surfaces it on the TenantGateway status — rather than emitting a
// Gateway with a duplicate, clashing, or out-of-apex listener that the
// Gateway API (or the cozystack-gateway-hostname-policy VAP) would then
// reject wholesale, taking every other listener (including every app's
// HTTP/HTTPS listener) down with it.
//
// passthroughServices is tgw.Spec.TLSPassthroughServices: the port-443
// passthrough list whose rendered tls-<svc> names share the listener
// namespace with this list. Both loops use passthroughListenerPrefix, so
// a raw name == svc comparison is exactly a rendered-name collision.
// apex is tgw.Spec.Apex, the hostname suffix every listener on the tenant
// Gateway must fall under.
func validateTLSPassthroughListeners(listeners []gatewayv1alpha1.TLSPassthroughListener, passthroughServices []string, apex string) error {
	serviceNames := make(map[string]struct{}, len(passthroughServices))
	for _, svc := range passthroughServices {
		// A repeated entry renders the same tls-<svc> listener name
		// twice, and Gateway API rejects the object for duplicate
		// listener names — the same wholesale failure this function
		// exists to convert into a status error. The schema does not
		// catch it: the field is a plain array, not a set.
		if _, dup := serviceNames[svc]; dup {
			return fmt.Errorf("tlsPassthroughServices: duplicate entry %q; it would render the %s%s Gateway listener twice", svc, passthroughListenerPrefix, svc)
		}
		serviceNames[svc] = struct{}{}
	}
	seenNames := make(map[string]struct{}, len(listeners))
	seenPorts := make(map[int32]struct{}, len(listeners))
	// Seeded with the hostnames the port-443 service listeners already
	// occupy (<svc>.<apex>, matching renderGateway) so the check below
	// spans both lists: a listener entry can collide with a service
	// hostname while their names differ, which the name checks miss.
	// source names the claimant the way the user wrote it, not the way
	// it renders: an error that says tls-api sends the reader looking
	// through the Gateway for a name they never typed, while the entry
	// they have to edit sits in the TenantGateway spec.
	type claimedHostname struct{ hostname, source string }
	seenHostnames := make([]claimedHostname, 0, len(listeners)+len(passthroughServices))
	for _, svc := range passthroughServices {
		seenHostnames = append(seenHostnames, claimedHostname{svc + "." + apex, fmt.Sprintf("tlsPassthroughServices entry %q", svc)})
	}
	for _, l := range listeners {
		if errs := validation.IsDNS1123Label(l.Name); len(errs) > 0 {
			return fmt.Errorf("tlsPassthroughListeners: invalid name %q: %s", l.Name, strings.Join(errs, "; "))
		}
		if _, dup := seenNames[l.Name]; dup {
			return fmt.Errorf("tlsPassthroughListeners: duplicate name %q", l.Name)
		}
		if _, clash := serviceNames[l.Name]; clash {
			return fmt.Errorf("tlsPassthroughListeners: name %q collides with tlsPassthroughServices entry %q; both render a %s%s Gateway listener", l.Name, l.Name, passthroughListenerPrefix, l.Name)
		}
		seenNames[l.Name] = struct{}{}

		if l.Port < 1 || l.Port > 65535 {
			return fmt.Errorf("tlsPassthroughListeners: listener %q port %d out of range 1..65535", l.Name, l.Port)
		}
		if _, reserved := reservedGatewayPorts[l.Port]; reserved {
			return fmt.Errorf("tlsPassthroughListeners: listener %q port %d is reserved for the Gateway's http (80) and terminate (443) listeners; use the engine's native port", l.Name, l.Port)
		}
		// One listener per port is a phase-1 narrowing, not a Gateway
		// API requirement: TLS listeners are distinct by the (port,
		// protocol, hostname) triple, so several passthrough listeners
		// could share a port and be selected by SNI — the port-443
		// tls-<svc> listeners above already do exactly that. It is
		// narrowed here because the field's purpose is the engine's
		// native port, where a second listener means two engines
		// answering on one port and the SNI deciding which, and
		// nothing downstream (routing, certificates) exists yet to
		// make that configuration testable. Lifting the restriction is
		// removing this check — no API or schema change — so it stays
		// available once the later phases land.
		if _, dup := seenPorts[l.Port]; dup {
			return fmt.Errorf("tlsPassthroughListeners: duplicate port %d (listener %q)", l.Port, l.Name)
		}
		seenPorts[l.Port] = struct{}{}

		if errs := validatePassthroughHostname(l.Hostname); len(errs) > 0 {
			return fmt.Errorf("tlsPassthroughListeners: listener %q invalid hostname %q: %s", l.Name, l.Hostname, strings.Join(errs, "; "))
		}
		if !hostnameWithinApex(l.Hostname, apex) {
			return fmt.Errorf("tlsPassthroughListeners: listener %q hostname %q is outside the tenant apex %q; it must equal the apex or be a subdomain of it (the cozystack-gateway-hostname-policy VAP rejects out-of-apex listener hostnames, failing the whole Gateway)", l.Name, l.Hostname, apex)
		}

		// Two listeners sharing a hostname on different ports are
		// distinct to Gateway API — listeners are keyed by (port,
		// protocol, hostname) — and the object is accepted. Cilium
		// routes passthrough by SNI without distinguishing the port
		// (cilium#42898, fixed upstream by cilium#44889 and
		// backported via cilium#46826 into 1.19.6 — cozystack pins
		// 1.19.5 in packages/system/cilium/images/cilium/Dockerfile,
		// so revisit this restriction when that pin moves), so only
		// one of them works and which one depends on route ordering.
		// On a native database port that is a raw stream forwarded to
		// the wrong backend, with Accepted and Programmed both true
		// and nothing on the status to show for it. Reject the shape
		// instead.
		//
		// Checked after the apex test on purpose: an out-of-apex
		// hostname that also happens to overlap should report the apex
		// violation, which names the actual mistake.
		for _, claimed := range seenHostnames {
			if !hostnamesOverlap(l.Hostname, claimed.hostname) {
				continue
			}
			return fmt.Errorf("tlsPassthroughListeners: listener %q hostname %q overlaps %s hostname %q; Cilium routes TLS passthrough by SNI alone and cannot distinguish two listeners whose hostnames match the same ClientHello, even on different ports", l.Name, l.Hostname, claimed.source, claimed.hostname)
		}
		seenHostnames = append(seenHostnames, claimedHostname{l.Hostname, fmt.Sprintf("listener %q", l.Name)})
	}
	return nil
}

// hostnamesOverlap reports whether two listener hostnames can match the
// same ClientHello SNI. Exact-string equality is not enough: a wildcard
// matches any number of labels to its left, per Gateway API's Hostname
// contract, so "*.foo.example.com" covers both "api.foo.example.com"
// and "a.b.foo.example.com" — and covers "*.db.foo.example.com" too.
// A wildcard does NOT match the bare suffix itself ("*.foo.example.com"
// does not match "foo.example.com"), which is why the exact leg tests
// for the leading dot.
//
// This matters because the caller rejects overlapping hostnames on the
// premise that Cilium routes passthrough by SNI alone. An exact-match
// check would let a single "*.<apex>" entry silently shadow the
// tls-<svc> listeners the chart ships by default (api, vm-exportproxy,
// cdi-uploadproxy all render <svc>.<apex>), which is the exact failure
// the check exists to prevent, reachable from stock values.
func hostnamesOverlap(a, b string) bool {
	if a == b {
		return true
	}
	return hostnameCovers(a, b) || hostnameCovers(b, a)
}

// hostnameCovers reports whether wildcard hostname w matches hostname x.
// Returns false when w is not a wildcard; the equality case is handled
// by the caller.
func hostnameCovers(w, x string) bool {
	suffix, ok := strings.CutPrefix(w, "*.")
	if !ok {
		return false
	}
	// A wildcard covers another wildcard when it covers everything that
	// one could match, i.e. when the other's suffix sits under ours.
	// The equality leg is unreachable through hostnamesOverlap, which
	// answers identical hostnames before it calls here; it stays so the
	// predicate is right read on its own, since a wildcard does cover
	// itself.
	if inner, isWildcard := strings.CutPrefix(x, "*."); isWildcard {
		return inner == suffix || strings.HasSuffix(inner, "."+suffix)
	}
	return strings.HasSuffix(x, "."+suffix)
}

// validatePassthroughHostname accepts an exact RFC 1123 hostname or a
// left-most-label wildcard ("*.example.com"), matching the shape the
// Gateway API allows for a listener hostname.
func validatePassthroughHostname(hostname string) []string {
	if strings.HasPrefix(hostname, "*.") {
		return validation.IsWildcardDNS1123Subdomain(hostname)
	}
	return validation.IsDNS1123Subdomain(hostname)
}

// hostnameWithinApex reports whether an exact or wildcard listener
// hostname falls within the tenant apex: it must equal the apex or be a
// subdomain of it. This mirrors the cozystack-gateway-hostname-policy
// ValidatingAdmissionPolicy (packages/system/cozystack-basics), whose
// CEL allows a listener hostname iff it equals the namespace host label
// or ends with "." + that label. A wildcard such as "*.db.<apex>"
// satisfies the suffix test and is accepted, exactly as the VAP accepts
// it. Rejecting an out-of-apex hostname here converts a wholesale Gateway
// rejection (the VAP denies the whole object on the first reconcile,
// taking every listener down) into a clear per-field error on the
// TenantGateway status. The leading "." in the suffix prevents a
// sibling-domain false match ("evilfoo.example.com" is not under
// "foo.example.com").
//
// It mirrors the VAP's shape, not its input: the VAP reads the
// namespace's namespace.cozystack.io/host label, this reads
// tgw.Spec.Apex. They are expected to be the same value — the tenant
// chart writes both from the same computed host, and layers 4 and 5 of
// the security model (packages/extra/gateway/README.md) restrict who
// may change either — but nothing in this function enforces it. If they
// ever diverge, this pre-check passes and the VAP still denies the
// whole Gateway, which is the outcome the pre-check exists to convert
// into a clean per-field status error.
func hostnameWithinApex(hostname, apex string) bool {
	return hostname == apex || strings.HasSuffix(hostname, "."+apex)
}

// perListenerName produces the Gateway listener name for a per-app
// HTTPS listener: "https-<first-label>-<8-hex>". The hex suffix is a
// 32-bit prefix of sha256(hostname), which makes collision between
// two distinct hostnames a 1-in-2^32 event — well below any
// realistic chart load. The first-label prefix is kept for
// human readability when reading Gateway.spec.listeners.
func perListenerName(hostname string) string {
	return "https-" + hostnameFirstLabel(hostname) + "-" + hostnameSuffix(hostname)
}

// childListenerName produces the Gateway listener name for the
// per-child-apex wildcard listener rendered in DNS-01 mode. Same
// shape as perListenerName but with a "child-" infix so the
// listener role is readable at a glance in Gateway.spec.listeners
// and so a child apex can never collide with a per-app HTTPS
// listener whose first-label happens to be "alice".
func childListenerName(childApex string) gatewayv1.SectionName {
	return gatewayv1.SectionName("https-child-" + hostnameFirstLabel(childApex) + "-" + hostnameSuffix(childApex))
}

// perListenerCertName produces the cert-manager Certificate name for
// a per-listener cert: "<tgw>-<first-label>-<8-hex>-tls".
func perListenerCertName(tgw *gatewayv1alpha1.TenantGateway, hostname string) string {
	return tgw.Name + "-" + hostnameFirstLabel(hostname) + "-" + hostnameSuffix(hostname) + "-tls"
}

// renderIssuer builds the per-tenant ACME Issuer. The solver block
// is selected by certMode: HTTP-01 with a gatewayHTTPRoute solver
// pointing back at the tenant's own Gateway/http listener, or DNS-01
// with the operator-supplied provider config. The ACME server URL is
// selected by spec.issuerName.
func (r *Reconciler) renderIssuer(tgw *gatewayv1alpha1.TenantGateway) (*cmv1.Issuer, error) {
	server, err := acmeServerForIssuer(tgw.Spec.IssuerName)
	if err != nil {
		return nil, err
	}

	solver, err := buildSolver(tgw)
	if err != nil {
		return nil, err
	}

	issuer := &cmv1.Issuer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      gatewayIssuerName(tgw),
			Namespace: tgw.Namespace,
			Labels: map[string]string{
				cozystackManagedByLabel: cozystackManagedByValue,
			},
		},
		Spec: cmv1.IssuerSpec{
			IssuerConfig: cmv1.IssuerConfig{
				ACME: &cmacmev1.ACMEIssuer{
					Server: server,
					PrivateKey: cmmetav1.SecretKeySelector{
						LocalObjectReference: cmmetav1.LocalObjectReference{
							Name: tgw.Name + "-acme-account",
						},
					},
					Solvers: []cmacmev1.ACMEChallengeSolver{*solver},
				},
			},
		},
	}
	if err := controllerutil.SetControllerReference(tgw, issuer, r.Scheme); err != nil {
		return nil, err
	}
	return issuer, nil
}

func buildSolver(tgw *gatewayv1alpha1.TenantGateway) (*cmacmev1.ACMEChallengeSolver, error) {
	switch tgw.Spec.CertMode {
	case gatewayv1alpha1.CertModeHTTP01, "":
		// HTTP-01 with gatewayHTTPRoute solver pointing at the tenant's
		// own Gateway. cert-manager publishes a transient HTTPRoute
		// attached to sectionName=http on this Gateway; the local Cilium
		// data plane forwards the ACME challenge HTTP request.
		section := gatewayv1.SectionName("http")
		ns := gatewayv1.Namespace(tgw.Namespace)
		return &cmacmev1.ACMEChallengeSolver{
			HTTP01: &cmacmev1.ACMEChallengeSolverHTTP01{
				GatewayHTTPRoute: &cmacmev1.ACMEChallengeSolverHTTP01GatewayHTTPRoute{
					ParentRefs: []gatewayv1.ParentReference{
						{
							Group:       ptrGroup(gatewayv1.GroupName),
							Kind:        ptrKind("Gateway"),
							Name:        gatewayv1.ObjectName(tgw.Name),
							Namespace:   &ns,
							SectionName: &section,
						},
					},
				},
			},
		}, nil

	case gatewayv1alpha1.CertModeDNS01:
		if tgw.Spec.DNS01 == nil {
			return nil, fmt.Errorf("certMode=dns01 requires spec.dns01 to be set")
		}
		switch tgw.Spec.DNS01.Provider {
		case "cloudflare":
			if tgw.Spec.DNS01.Cloudflare == nil {
				return nil, fmt.Errorf("dns01.provider=cloudflare requires dns01.cloudflare to be set")
			}
			return &cmacmev1.ACMEChallengeSolver{
				DNS01: &cmacmev1.ACMEChallengeSolverDNS01{
					Cloudflare: &cmacmev1.ACMEIssuerDNS01ProviderCloudflare{
						APIToken: &cmmetav1.SecretKeySelector{
							LocalObjectReference: cmmetav1.LocalObjectReference{
								Name: tgw.Spec.DNS01.Cloudflare.APITokenSecretRef.Name,
							},
							Key: tgw.Spec.DNS01.Cloudflare.APITokenSecretRef.Key,
						},
					},
				},
			}, nil
		case "route53":
			if tgw.Spec.DNS01.Route53 == nil {
				return nil, fmt.Errorf("dns01.provider=route53 requires dns01.route53 to be set")
			}
			cfg := tgw.Spec.DNS01.Route53
			r53 := &cmacmev1.ACMEIssuerDNS01ProviderRoute53{
				Region:      cfg.Region,
				AccessKeyID: cfg.AccessKeyID,
			}
			if cfg.SecretAccessKeySecretRef != nil {
				r53.SecretAccessKey = cmmetav1.SecretKeySelector{
					LocalObjectReference: cmmetav1.LocalObjectReference{Name: cfg.SecretAccessKeySecretRef.Name},
					Key:                  cfg.SecretAccessKeySecretRef.Key,
				}
			}
			return &cmacmev1.ACMEChallengeSolver{
				DNS01: &cmacmev1.ACMEChallengeSolverDNS01{Route53: r53},
			}, nil
		case "digitalocean":
			if tgw.Spec.DNS01.DigitalOcean == nil {
				return nil, fmt.Errorf("dns01.provider=digitalocean requires dns01.digitalocean to be set")
			}
			return &cmacmev1.ACMEChallengeSolver{
				DNS01: &cmacmev1.ACMEChallengeSolverDNS01{
					DigitalOcean: &cmacmev1.ACMEIssuerDNS01ProviderDigitalOcean{
						Token: cmmetav1.SecretKeySelector{
							LocalObjectReference: cmmetav1.LocalObjectReference{Name: tgw.Spec.DNS01.DigitalOcean.TokenSecretRef.Name},
							Key:                  tgw.Spec.DNS01.DigitalOcean.TokenSecretRef.Key,
						},
					},
				},
			}, nil
		case "rfc2136":
			if tgw.Spec.DNS01.RFC2136 == nil {
				return nil, fmt.Errorf("dns01.provider=rfc2136 requires dns01.rfc2136 to be set")
			}
			cfg := tgw.Spec.DNS01.RFC2136
			alg := cfg.TSIGAlgorithm
			if alg == "" {
				alg = "HMACSHA256"
			}
			return &cmacmev1.ACMEChallengeSolver{
				DNS01: &cmacmev1.ACMEChallengeSolverDNS01{
					RFC2136: &cmacmev1.ACMEIssuerDNS01ProviderRFC2136{
						Nameserver:    cfg.Nameserver,
						TSIGKeyName:   cfg.TSIGKeyName,
						TSIGAlgorithm: alg,
						TSIGSecret: cmmetav1.SecretKeySelector{
							LocalObjectReference: cmmetav1.LocalObjectReference{Name: cfg.TSIGSecretSecretRef.Name},
							Key:                  cfg.TSIGSecretSecretRef.Key,
						},
					},
				},
			}, nil
		default:
			return nil, fmt.Errorf("unsupported dns01.provider=%q (supported: cloudflare, route53, digitalocean, rfc2136)", tgw.Spec.DNS01.Provider)
		}

	case gatewayv1alpha1.CertModeExistingSecret:
		// existingSecret mode mints no Issuer, so reconcileIssuer never
		// calls buildSolver in this mode. Guard defensively so a future
		// caller gets a clear contract error instead of silently
		// falling through to the unknown-certMode default below.
		return nil, fmt.Errorf("certMode=existingSecret does not use an ACME solver")

	default:
		return nil, fmt.Errorf("unknown certMode=%q", tgw.Spec.CertMode)
	}
}

// renderWildcardCertificate builds the cert-manager Certificate that
// covers <apex> and *.<apex>, plus per-child-apex SANs for every
// tenant inheriting through this Gateway. Only used in DNS-01 mode;
// the listeners rendered by renderGateway reference its secretName.
//
// childApexes is the deduplicated, sorted list of apex hostnames
// inherited by child tenants whose namespace carries
// namespace.cozystack.io/gateway = tgw.Namespace. Caller collects
// them via collectInheritingChildApexes. Without these SANs the
// parent's single-level wildcard (*.<apex>) cannot match a child
// route's hostname (harbor.alice.example.com is two labels deeper
// than the wildcard accepts).
func (r *Reconciler) renderWildcardCertificate(tgw *gatewayv1alpha1.TenantGateway, childApexes []string) (*cmv1.Certificate, error) {
	dnsNames := []string{tgw.Spec.Apex, "*." + tgw.Spec.Apex}
	seen := map[string]struct{}{
		tgw.Spec.Apex:        {},
		"*." + tgw.Spec.Apex: {},
	}
	for _, apex := range childApexes {
		if apex == "" {
			continue
		}
		// Skip a child whose host label collides with the parent
		// apex (mis-labelled namespace, or two tenants briefly
		// sharing an apex during a rename). cert-manager rejects
		// duplicate dnsNames; the inheriting tenant still attaches
		// via the parent SANs already present.
		if _, dup := seen[apex]; !dup {
			dnsNames = append(dnsNames, apex)
			seen[apex] = struct{}{}
		}
		wildcard := "*." + apex
		if _, dup := seen[wildcard]; !dup {
			dnsNames = append(dnsNames, wildcard)
			seen[wildcard] = struct{}{}
		}
	}
	cert := &cmv1.Certificate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      gatewayCertificateName(tgw),
			Namespace: tgw.Namespace,
			Labels: map[string]string{
				cozystackManagedByLabel: cozystackManagedByValue,
			},
		},
		Spec: cmv1.CertificateSpec{
			SecretName: gatewayCertificateName(tgw),
			IssuerRef: cmmetav1.ObjectReference{
				Kind: "Issuer",
				Name: gatewayIssuerName(tgw),
			},
			DNSNames: dnsNames,
		},
	}
	if err := controllerutil.SetControllerReference(tgw, cert, r.Scheme); err != nil {
		return nil, err
	}
	return cert, nil
}

func ptrGroup(g string) *gatewayv1.Group {
	gg := gatewayv1.Group(g)
	return &gg
}

func ptrKind(k string) *gatewayv1.Kind {
	kk := gatewayv1.Kind(k)
	return &kk
}

// renderHTTPRedirect builds the HTTPRoute that catches every hostname
// on the Gateway's HTTP listener and 301-redirects to HTTPS. Without
// this, app-owned HTTPRoutes that attach to the Gateway by hostname
// (no sectionName) silently serve plaintext on port 80 — the legacy
// nginx Ingress flow had ssl-redirect: "true" enabled by default; the
// new TenantGateway path replicates that contract here.
func (r *Reconciler) renderHTTPRedirect(tgw *gatewayv1alpha1.TenantGateway) (*gatewayv1.HTTPRoute, error) {
	section := gatewayv1.SectionName("http")
	scheme := "https"
	statusCode := 301
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tgw.Name + "-http-redirect",
			Namespace: tgw.Namespace,
			Labels: map[string]string{
				cozystackManagedByLabel:   cozystackManagedByValue,
				cozystackTenantGatewayKey: tgw.Name,
			},
		},
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{
					{
						Group:       ptrGroup(gatewayv1.GroupName),
						Kind:        ptrKind("Gateway"),
						Name:        gatewayv1.ObjectName(tgw.Name),
						SectionName: &section,
					},
				},
			},
			Rules: []gatewayv1.HTTPRouteRule{
				{
					Filters: []gatewayv1.HTTPRouteFilter{
						{
							Type: gatewayv1.HTTPRouteFilterRequestRedirect,
							RequestRedirect: &gatewayv1.HTTPRequestRedirectFilter{
								Scheme:     &scheme,
								StatusCode: &statusCode,
							},
						},
					},
				},
			},
		},
	}
	if err := controllerutil.SetControllerReference(tgw, route, r.Scheme); err != nil {
		return nil, fmt.Errorf("set controller reference on redirect HTTPRoute: %w", err)
	}
	return route, nil
}

// renderPerListenerCertificate builds a cert-manager Certificate for a
// single hostname (HTTP-01 mode). Each per-app listener references
// this cert via its TLS configuration. Returns an error if the
// scheme can't establish the controllerRef back to the
// TenantGateway — without it, deleting the TenantGateway leaves
// orphan Certificates behind.
func (r *Reconciler) renderPerListenerCertificate(tgw *gatewayv1alpha1.TenantGateway, hostname string) (*cmv1.Certificate, error) {
	name := perListenerCertName(tgw, hostname)
	cert := &cmv1.Certificate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: tgw.Namespace,
			Labels: map[string]string{
				cozystackManagedByLabel:   cozystackManagedByValue,
				cozystackTenantGatewayKey: tgw.Name,
				cozystackPerListenerCert:  "true",
			},
		},
		Spec: cmv1.CertificateSpec{
			SecretName: name,
			IssuerRef: cmmetav1.ObjectReference{
				Kind: "Issuer",
				Name: gatewayIssuerName(tgw),
			},
			DNSNames: []string{hostname},
		},
	}
	if err := controllerutil.SetControllerReference(tgw, cert, r.Scheme); err != nil {
		return nil, fmt.Errorf("set controller reference on Certificate %s: %w", name, err)
	}
	return cert, nil
}
