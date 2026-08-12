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

// httpRedirectRouteName returns the name of the controller-owned
// http→https redirect HTTPRoute for this TenantGateway. Shared by the
// renderer and by collectHostnameClaims, which excludes exactly this
// route from the hostname-claim set, so the two must agree on the name.
func httpRedirectRouteName(tgw *gatewayv1alpha1.TenantGateway) string {
	return tgw.Name + "-http-redirect"
}

// acmeChallengeNamespace is where cert-manager itself runs. Hardcoded to
// the cozystack platform default; if you ever move cert-manager out of
// cozy-cert-manager, add a TenantGateway spec field to override this.
//
// It is not where the HTTP-01 solver HTTPRoute appears: cert-manager
// creates the Challenge, and with it the solver route, in the
// Certificate's own namespace, which for these per-listener certs is the
// tenant namespace. That namespace is already on the port-80 allow list
// on its own account, so this entry is belt and braces.
const acmeChallengeNamespace = "cozy-cert-manager"

// buildAllowedRoutes computes the AllowedRoutes block applied to
// HTTPS / TLS-passthrough listeners: a label selector matching
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
// allowedRoutes for the port-80 listener: only the tenant namespace,
// where both the controller-owned http→https redirect HTTPRoute and
// cert-manager's transient HTTP-01 solver route under
// /.well-known/acme-challenge/ live, plus the namespace cert-manager
// itself runs in (see acmeChallengeNamespace).
//
// Why: app HTTPRoutes (harbor, keycloak, dashboard, bucket) attach
// by hostname with no sectionName, so any of them the filter admits
// reaches the HTTP listener and can serve plaintext there. The filter
// keeps out the ones published from elsewhere; an app in the Gateway's
// own namespace is admitted and is not covered by this. Restricting the HTTP
// listener's allowedRoutes namespaces keeps out routes from the other
// cozy-* namespaces and from inheriting child tenants. The Gateway's
// own namespace stays open, because the redirect and the ACME solver
// route both live there, and so does acmeChallengeNamespace.
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
		tgw.Spec.Apex:           {},
		"*." + tgw.Spec.Apex:    {},
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

// renderHTTPRedirect builds the HTTPRoute that catches the tenant apex
// and its subdomains on the Gateway's HTTP listener and 301-redirects
// them to HTTPS. Without this, app-owned HTTPRoutes that attach to the
// Gateway by hostname (no sectionName) silently serve plaintext on port
// 80 — the legacy nginx Ingress flow had ssl-redirect: "true" enabled by
// default; the new TenantGateway path replicates that contract here.
func (r *Reconciler) renderHTTPRedirect(tgw *gatewayv1alpha1.TenantGateway) (*gatewayv1.HTTPRoute, error) {
	// Spec.Apex carries MinLength=1, so admission normally rejects an
	// empty value before it reaches here. Check anyway: the CRD and this
	// binary are rolled out separately, and against an older CRD an empty
	// apex would render hostnames "" and "*.", both of which fail the
	// HTTPRoute schema. That surfaces as an apiserver validation error
	// naming the route rather than the field, which is a poor thing to
	// hand an operator.
	//
	// This wins the race in HTTP-01 without tlsPassthroughServices: the
	// listener set is built from route hostnames alone there, so however
	// many routes are attached, reconcileGateway never reads the apex
	// ahead of this point. DNS-01 and existingSecret render an apex
	// listener, and any tlsPassthroughServices entry builds
	// "<service>.<apex>", so in those the operator meets the equivalent
	// schema error against the Gateway first.
	if tgw.Spec.Apex == "" {
		return nil, fmt.Errorf("spec.apex is empty on TenantGateway %s/%s: the http-to-https redirect route derives its hostnames from the apex and cannot be rendered without one", tgw.Namespace, tgw.Name)
	}
	section := gatewayv1.SectionName("http")
	scheme := "https"
	statusCode := 301
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      httpRedirectRouteName(tgw),
			Namespace: tgw.Namespace,
			Labels: map[string]string{
				cozystackManagedByLabel:   cozystackManagedByValue,
				cozystackTenantGatewayKey: tgw.Name,
			},
		},
		Spec: gatewayv1.HTTPRouteSpec{
			// The apex and its wildcard, rather than no hostnames at
			// all. This route lives in a tenant-* namespace, where
			// cozystack-route-hostname-policy requires spec.hostnames
			// to be present, non-empty and inside the namespace apex;
			// a hostname-less route is denied at admission and takes
			// this whole reconcile down with it.
			//
			// Both entries are load-bearing. A wildcard hostname is a
			// suffix match spanning any number of labels, so
			// "*.<apex>" covers every subdomain at every depth, but it
			// does not cover the bare "<apex>", hence the plain apex
			// first.
			//
			// Naming them costs no coverage, and the other half of
			// this change is why: the port-80 listener admits routes
			// only from this namespace and cert-manager's, and the
			// tightened cozystack-route-hostname-policy requires every
			// route here to declare hostnames inside the apex. So any
			// host published from this namespace is already one of
			// these two.
			//
			// An inheriting child cannot widen that set either.
			// cozystack-gateway-hostname-policy compares every listener
			// against the host label of the Gateway's own namespace, so
			// a child apex outside it never gets its listener: the
			// Gateway write carrying it is refused at admission whole,
			// in reconcileGateway and before this route is rendered. A
			// child apex under this one passes that check, and the
			// wildcard above already covers it.
			Hostnames: []gatewayv1.Hostname{
				gatewayv1.Hostname(tgw.Spec.Apex),
				gatewayv1.Hostname("*." + tgw.Spec.Apex),
			},
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
