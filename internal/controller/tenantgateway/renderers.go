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

// buildAllowedRoutes computes the AllowedRoutes block applied to
// every listener on the rendered Gateway: a Selector that matches
// the built-in `kubernetes.io/metadata.name` label (kube-apiserver-
// written, unspoofable). The accepted set is the publishing tenant
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
// part before the first '.'), used for derived listener and cert
// names. "harbor.foo.example.com" → "harbor".
func hostnameFirstLabel(hostname string) string {
	if i := strings.Index(hostname, "."); i >= 0 {
		return hostname[:i]
	}
	return hostname
}

// perListenerName produces the Gateway listener name for a per-app
// HTTPS listener: "https-<first-label>". Collisions are pathological
// (operator would have to publish two completely different apex
// trees with the same first label) and would surface as Gateway
// admission errors rather than silent override.
func perListenerName(hostname string) string {
	return "https-" + hostnameFirstLabel(hostname)
}

// perListenerCertName produces the cert-manager Certificate name for
// a per-listener cert: "<tgw>-<first-label>-tls".
func perListenerCertName(tgw *gatewayv1alpha1.TenantGateway, hostname string) string {
	return tgw.Name + "-" + hostnameFirstLabel(hostname) + "-tls"
}

// renderIssuer builds the per-tenant ACME Issuer. The solver block
// is selected by certMode: HTTP-01 with a gatewayHTTPRoute solver
// pointing back at the tenant's own Gateway/http listener, or DNS-01
// with the operator-supplied provider config.
func (r *Reconciler) renderIssuer(tgw *gatewayv1alpha1.TenantGateway) (*cmv1.Issuer, error) {
	server := letsencryptProdServer
	// IssuerName/server selection is currently hardcoded to LE-prod;
	// the chart surface accepted publishing.certificates.issuerName,
	// which migrates to TenantGateway.spec in a future commit.

	solver, err := buildSolver(tgw)
	if err != nil {
		return nil, err
	}

	issuer := &cmv1.Issuer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      gatewayIssuerName(tgw),
			Namespace: tgw.Namespace,
			Labels: map[string]string{
				"cozystack.io/managed-by": "cozystack-controller",
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
		default:
			return nil, fmt.Errorf("unsupported dns01.provider=%q (this commit covers cloudflare; route53/digitalocean/rfc2136 land in a follow-up)", tgw.Spec.DNS01.Provider)
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
				"cozystack.io/managed-by": "cozystack-controller",
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

// renderPerListenerCertificate builds a cert-manager Certificate for a
// single hostname (HTTP-01 mode). Each per-app listener references
// this cert via its TLS configuration.
func (r *Reconciler) renderPerListenerCertificate(tgw *gatewayv1alpha1.TenantGateway, hostname string) *cmv1.Certificate {
	name := perListenerCertName(tgw, hostname)
	cert := &cmv1.Certificate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: tgw.Namespace,
			Labels: map[string]string{
				"cozystack.io/managed-by":          "cozystack-controller",
				"cozystack.io/tenantgateway":       tgw.Name,
				"cozystack.io/per-listener-cert":   "true",
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
	// Best-effort owner reference — ignore the error: even if the
	// scheme has not registered the type, the Certificate is still
	// valid; cleanup falls back to the per-listener-cert label.
	_ = controllerutil.SetControllerReference(tgw, cert, r.Scheme)
	return cert
}
