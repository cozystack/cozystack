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

// buildAllowedRoutes computes the AllowedRoutes block applied to
// HTTPS / TLS-passthrough listeners: a Selector that matches the
// built-in `kubernetes.io/metadata.name` label (kube-apiserver-
// written, unspoofable). Accepted set is the publishing tenant
// namespace plus tgw.Spec.AttachedNamespaces. This is Layer 1 of
// the security model documented in the gateway chart README.
func buildAllowedRoutes(tgw *gatewayv1alpha1.TenantGateway) *gatewayv1.AllowedRoutes {
	values := []string{tgw.Namespace}
	seen := map[string]struct{}{tgw.Namespace: {}}
	for _, ns := range tgw.Spec.AttachedNamespaces {
		if ns == "" {
			continue
		}
		if _, dup := seen[ns]; dup {
			continue
		}
		seen[ns] = struct{}{}
		values = append(values, ns)
	}
	return allowedRoutesFromValues(values)
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

// perListenerName produces the Gateway listener name for a per-app
// HTTPS listener: "https-<first-label>-<8-hex>". The hex suffix is a
// 32-bit prefix of sha256(hostname), which makes collision between
// two distinct hostnames a 1-in-2^32 event — well below any
// realistic chart load. The first-label prefix is kept for
// human readability when reading Gateway.spec.listeners.
func perListenerName(hostname string) string {
	return "https-" + hostnameFirstLabel(hostname) + "-" + hostnameSuffix(hostname)
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

	default:
		return nil, fmt.Errorf("unknown certMode=%q", tgw.Spec.CertMode)
	}
}

// renderWildcardCertificate builds the cert-manager Certificate that
// covers <apex> and *.<apex>. Only used in DNS-01 mode; the listeners
// rendered by renderGateway reference its secretName.
func (r *Reconciler) renderWildcardCertificate(tgw *gatewayv1alpha1.TenantGateway) (*cmv1.Certificate, error) {
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
			DNSNames: []string{
				tgw.Spec.Apex,
				"*." + tgw.Spec.Apex,
			},
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
