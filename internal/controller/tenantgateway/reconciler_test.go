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
	"testing"

	cmv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	gatewayv1alpha1 "github.com/cozystack/cozystack/api/gateway/v1alpha1"
)

// newScheme builds a scheme registering everything the controller is
// expected to read or write: TenantGateway (own group), Gateway API
// HTTPRoute / TLSRoute / Gateway, cert-manager Certificate, plus the
// k8s built-ins (corev1 Namespace, etc.) via the client-go scheme.
func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("client-go scheme: %v", err)
	}
	if err := gatewayv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("tenantgateway scheme: %v", err)
	}
	if err := gatewayv1.Install(s); err != nil {
		t.Fatalf("gateway v1 scheme: %v", err)
	}
	if err := gatewayv1alpha2.Install(s); err != nil {
		t.Fatalf("gateway v1alpha2 scheme: %v", err)
	}
	if err := cmv1.AddToScheme(s); err != nil {
		t.Fatalf("cert-manager scheme: %v", err)
	}
	return s
}

// TestReconcile_NotFoundIsNoop pins the early-exit path: a deleted
// TenantGateway should result in no error and no requeue. This is a
// canary for the bare reconciler skeleton — the surface that exists
// before any Gateway/Certificate logic lands.
func TestReconcile_NotFoundIsNoop(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()

	r := &Reconciler{Client: c, Scheme: s}
	res, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "tenant-foo", Name: "missing"},
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Fatalf("expected empty Result, got %+v", res)
	}
}

// TestReconcile_TenantGatewayProducesGateway pins the basic Gateway
// materialisation: when a TenantGateway exists in a tenant namespace,
// the reconciler creates a gateway.networking.k8s.io Gateway with the
// same name in the same namespace, GatewayClassName matching spec, and
// at minimum the static `http` listener that ACME HTTP-01 challenges
// route through.
func TestReconcile_TenantGatewayProducesGateway(t *testing.T) {
	s := newScheme(t)
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
		Spec: gatewayv1alpha1.TenantGatewaySpec{
			Apex:             "foo.example.com",
			CertMode:         gatewayv1alpha1.CertModeHTTP01,
			GatewayClassName: "cilium",
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(tgw).
		WithStatusSubresource(tgw).
		Build()

	r := &Reconciler{Client: c, Scheme: s}
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &gatewayv1.Gateway{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"}, got); err != nil {
		t.Fatalf("get Gateway: %v", err)
	}
	if got.Spec.GatewayClassName != "cilium" {
		t.Errorf("Gateway.Spec.GatewayClassName=%q, want cilium", got.Spec.GatewayClassName)
	}
	// The http listener must always be present — ACME HTTP-01 challenges
	// route through it regardless of certMode.
	var sawHTTP bool
	for _, l := range got.Spec.Listeners {
		if l.Name == "http" && l.Port == 80 && l.Protocol == gatewayv1.HTTPProtocolType {
			sawHTTP = true
			break
		}
	}
	if !sawHTTP {
		t.Errorf("expected http listener (port 80, HTTP) for ACME, got %+v", got.Spec.Listeners)
	}
}

// TestReconcile_OwnerReferenceOnGateway pins the lifecycle contract:
// the rendered Gateway must carry the TenantGateway as its
// controller-owner so cascade-delete works (deleting the TenantGateway
// cleans up the Gateway).
func TestReconcile_OwnerReferenceOnGateway(t *testing.T) {
	s := newScheme(t)
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cozystack",
			Namespace: "tenant-foo",
			UID:       "tgw-uid",
		},
		Spec: gatewayv1alpha1.TenantGatewaySpec{
			Apex:             "foo.example.com",
			CertMode:         gatewayv1alpha1.CertModeHTTP01,
			GatewayClassName: "cilium",
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(tgw).WithStatusSubresource(tgw).Build()

	r := &Reconciler{Client: c, Scheme: s}
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &gatewayv1.Gateway{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"}, got); err != nil {
		t.Fatalf("get Gateway: %v", err)
	}
	var owned bool
	for _, ref := range got.OwnerReferences {
		if ref.UID == "tgw-uid" && ref.Controller != nil && *ref.Controller {
			owned = true
			break
		}
	}
	if !owned {
		t.Errorf("expected controller OwnerReference to TenantGateway uid=tgw-uid, got %+v", got.OwnerReferences)
	}
}

// TestReconcile_DNS01ModeRendersWildcardListener pins the opt-in DNS-01
// branch: when CertMode=dns01 the rendered Gateway carries the
// wildcard `https` listener for `*.<apex>` plus the `https-apex`
// listener for the bare apex domain.
func TestReconcile_DNS01ModeRendersWildcardListener(t *testing.T) {
	s := newScheme(t)
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
		Spec: gatewayv1alpha1.TenantGatewaySpec{
			Apex:             "foo.example.com",
			CertMode:         gatewayv1alpha1.CertModeDNS01,
			GatewayClassName: "cilium",
			DNS01: &gatewayv1alpha1.DNS01Config{
				Provider: "cloudflare",
				Cloudflare: &gatewayv1alpha1.CloudflareDNS01{
					APITokenSecretRef: corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "cf-token"},
						Key:                  "api-token",
					},
				},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(tgw).WithStatusSubresource(tgw).Build()

	r := &Reconciler{Client: c, Scheme: s}
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &gatewayv1.Gateway{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"}, got); err != nil {
		t.Fatalf("get Gateway: %v", err)
	}
	var sawWildcard, sawApex bool
	for _, l := range got.Spec.Listeners {
		if l.Hostname != nil && string(*l.Hostname) == "*.foo.example.com" && l.Protocol == gatewayv1.HTTPSProtocolType {
			sawWildcard = true
		}
		if l.Hostname != nil && string(*l.Hostname) == "foo.example.com" && l.Protocol == gatewayv1.HTTPSProtocolType {
			sawApex = true
		}
	}
	if !sawWildcard {
		t.Errorf("expected wildcard *.foo.example.com HTTPS listener in DNS-01 mode, got %+v", got.Spec.Listeners)
	}
	if !sawApex {
		t.Errorf("expected apex foo.example.com HTTPS listener in DNS-01 mode, got %+v", got.Spec.Listeners)
	}
}

// TestReconcile_HTTP01ModeNoWildcardListener pins the default branch:
// in HTTP-01 mode the Gateway must NOT have a wildcard `*.<apex>`
// listener (because HTTP-01 cannot issue wildcard certs). Per-app
// listeners are added later by route-driven reconciliation.
func TestReconcile_HTTP01ModeNoWildcardListener(t *testing.T) {
	s := newScheme(t)
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
		Spec: gatewayv1alpha1.TenantGatewaySpec{
			Apex:             "foo.example.com",
			CertMode:         gatewayv1alpha1.CertModeHTTP01,
			GatewayClassName: "cilium",
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(tgw).WithStatusSubresource(tgw).Build()

	r := &Reconciler{Client: c, Scheme: s}
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &gatewayv1.Gateway{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"}, got); err != nil {
		t.Fatalf("get Gateway: %v", err)
	}
	for _, l := range got.Spec.Listeners {
		if l.Hostname != nil && string(*l.Hostname) == "*.foo.example.com" {
			t.Errorf("HTTP-01 mode must not render wildcard listener, found %+v", l)
		}
	}
}

// TestReconcile_AlwaysCreatesIssuer pins the cert-manager
// infrastructure: every TenantGateway materialises a per-tenant
// ACME Issuer in its namespace, regardless of certMode. The Issuer
// is named "<tgw-name>-gateway".
func TestReconcile_AlwaysCreatesIssuer(t *testing.T) {
	for _, mode := range []gatewayv1alpha1.CertMode{
		gatewayv1alpha1.CertModeHTTP01,
		gatewayv1alpha1.CertModeDNS01,
	} {
		t.Run(string(mode), func(t *testing.T) {
			s := newScheme(t)
			tgw := &gatewayv1alpha1.TenantGateway{
				ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
				Spec: gatewayv1alpha1.TenantGatewaySpec{
					Apex:             "foo.example.com",
					CertMode:         mode,
					GatewayClassName: "cilium",
				},
			}
			if mode == gatewayv1alpha1.CertModeDNS01 {
				tgw.Spec.DNS01 = &gatewayv1alpha1.DNS01Config{
					Provider: "cloudflare",
					Cloudflare: &gatewayv1alpha1.CloudflareDNS01{
						APITokenSecretRef: corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "cf-token"},
							Key:                  "api-token",
						},
					},
				}
			}
			c := fake.NewClientBuilder().WithScheme(s).WithObjects(tgw).WithStatusSubresource(tgw).Build()

			r := &Reconciler{Client: c, Scheme: s}
			if _, err := r.Reconcile(context.TODO(), ctrl.Request{
				NamespacedName: types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"},
			}); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			got := &cmv1.Issuer{}
			if err := c.Get(context.TODO(), types.NamespacedName{Name: "cozystack-gateway", Namespace: "tenant-foo"}, got); err != nil {
				t.Fatalf("expected Issuer cozystack-gateway in tenant-foo: %v", err)
			}
			if got.Spec.ACME == nil {
				t.Fatalf("expected ACME issuer, got %+v", got.Spec)
			}
		})
	}
}

// TestReconcile_HTTP01IssuerHasGatewayHTTPRouteSolver pins the HTTP-01
// path: the per-tenant Issuer's ACME solver block references the
// tenant's own Gateway via gatewayHTTPRoute, sectionName=http. This is
// what allows cert-manager to publish HTTP-01 challenge HTTPRoutes
// onto the right Gateway.
func TestReconcile_HTTP01IssuerHasGatewayHTTPRouteSolver(t *testing.T) {
	s := newScheme(t)
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
		Spec: gatewayv1alpha1.TenantGatewaySpec{
			Apex:             "foo.example.com",
			CertMode:         gatewayv1alpha1.CertModeHTTP01,
			GatewayClassName: "cilium",
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(tgw).WithStatusSubresource(tgw).Build()

	r := &Reconciler{Client: c, Scheme: s}
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	iss := &cmv1.Issuer{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "cozystack-gateway", Namespace: "tenant-foo"}, iss); err != nil {
		t.Fatalf("get Issuer: %v", err)
	}
	if iss.Spec.ACME == nil || len(iss.Spec.ACME.Solvers) != 1 {
		t.Fatalf("expected exactly one ACME solver, got %+v", iss.Spec.ACME)
	}
	solver := iss.Spec.ACME.Solvers[0]
	if solver.HTTP01 == nil {
		t.Fatalf("expected HTTP-01 solver, got %+v", solver)
	}
	if solver.HTTP01.GatewayHTTPRoute == nil {
		t.Fatalf("expected gatewayHTTPRoute solver, got %+v", solver.HTTP01)
	}
	if len(solver.HTTP01.GatewayHTTPRoute.ParentRefs) != 1 {
		t.Fatalf("expected exactly one parentRef, got %+v", solver.HTTP01.GatewayHTTPRoute.ParentRefs)
	}
	pr := solver.HTTP01.GatewayHTTPRoute.ParentRefs[0]
	if pr.Name != "cozystack" {
		t.Errorf("parentRef.Name=%q, want cozystack", pr.Name)
	}
	if pr.SectionName == nil || string(*pr.SectionName) != "http" {
		t.Errorf("parentRef.SectionName=%v, want http", pr.SectionName)
	}
}

// TestReconcile_DNS01IssuerCloudflareSolver pins the DNS-01 + cloudflare
// path: the Issuer carries a dns01.cloudflare solver block that
// references the operator-supplied API token Secret.
func TestReconcile_DNS01IssuerCloudflareSolver(t *testing.T) {
	s := newScheme(t)
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
		Spec: gatewayv1alpha1.TenantGatewaySpec{
			Apex:             "foo.example.com",
			CertMode:         gatewayv1alpha1.CertModeDNS01,
			GatewayClassName: "cilium",
			DNS01: &gatewayv1alpha1.DNS01Config{
				Provider: "cloudflare",
				Cloudflare: &gatewayv1alpha1.CloudflareDNS01{
					APITokenSecretRef: corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "cloudflare-api-token-secret"},
						Key:                  "api-token",
					},
				},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(tgw).WithStatusSubresource(tgw).Build()

	r := &Reconciler{Client: c, Scheme: s}
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	iss := &cmv1.Issuer{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "cozystack-gateway", Namespace: "tenant-foo"}, iss); err != nil {
		t.Fatalf("get Issuer: %v", err)
	}
	if iss.Spec.ACME == nil || len(iss.Spec.ACME.Solvers) != 1 {
		t.Fatalf("expected exactly one ACME solver, got %+v", iss.Spec.ACME)
	}
	solver := iss.Spec.ACME.Solvers[0]
	if solver.DNS01 == nil || solver.DNS01.Cloudflare == nil {
		t.Fatalf("expected dns01.cloudflare solver, got %+v", solver)
	}
	if solver.DNS01.Cloudflare.APIToken == nil || solver.DNS01.Cloudflare.APIToken.Name != "cloudflare-api-token-secret" {
		t.Errorf("Cloudflare token secret=%+v, want name=cloudflare-api-token-secret", solver.DNS01.Cloudflare.APIToken)
	}
}

// TestReconcile_DNS01CreatesWildcardCertificate pins the wildcard Cert
// rendered in DNS-01 mode: dnsNames cover both <apex> and *.<apex>,
// the cert references the per-tenant Issuer, and the secretName
// matches what the Gateway listeners expect.
func TestReconcile_DNS01CreatesWildcardCertificate(t *testing.T) {
	s := newScheme(t)
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
		Spec: gatewayv1alpha1.TenantGatewaySpec{
			Apex:             "foo.example.com",
			CertMode:         gatewayv1alpha1.CertModeDNS01,
			GatewayClassName: "cilium",
			DNS01: &gatewayv1alpha1.DNS01Config{
				Provider: "cloudflare",
				Cloudflare: &gatewayv1alpha1.CloudflareDNS01{
					APITokenSecretRef: corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "cf-token"},
						Key:                  "api-token",
					},
				},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(tgw).WithStatusSubresource(tgw).Build()

	r := &Reconciler{Client: c, Scheme: s}
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cert := &cmv1.Certificate{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "cozystack-gateway-tls", Namespace: "tenant-foo"}, cert); err != nil {
		t.Fatalf("get Certificate: %v", err)
	}
	if cert.Spec.SecretName != "cozystack-gateway-tls" {
		t.Errorf("SecretName=%q, want cozystack-gateway-tls", cert.Spec.SecretName)
	}
	if cert.Spec.IssuerRef.Kind != "Issuer" || cert.Spec.IssuerRef.Name != "cozystack-gateway" {
		t.Errorf("IssuerRef=%+v, want {Kind: Issuer, Name: cozystack-gateway}", cert.Spec.IssuerRef)
	}
	wantDNS := map[string]bool{"foo.example.com": false, "*.foo.example.com": false}
	for _, n := range cert.Spec.DNSNames {
		if _, ok := wantDNS[n]; ok {
			wantDNS[n] = true
		}
	}
	for n, seen := range wantDNS {
		if !seen {
			t.Errorf("missing DNS name %q in cert.spec.dnsNames=%v", n, cert.Spec.DNSNames)
		}
	}
}

// httpRouteAttached builds an HTTPRoute in the given namespace with a
// parentRef pointing at the tenant-foo/cozystack Gateway and a single
// hostname.
func httpRouteAttached(name, ns, hostname string) *gatewayv1.HTTPRoute {
	gwGroup := gatewayv1.Group(gatewayv1.GroupName)
	gwKind := gatewayv1.Kind("Gateway")
	gwNs := gatewayv1.Namespace("tenant-foo")
	return &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{
					{
						Group:     &gwGroup,
						Kind:      &gwKind,
						Namespace: &gwNs,
						Name:      gatewayv1.ObjectName("cozystack"),
					},
				},
			},
			Hostnames: []gatewayv1.Hostname{gatewayv1.Hostname(hostname)},
		},
	}
}

// TestReconcile_HTTP01ProducesListenerForHTTPRoute pins the route-driven
// listener flow: an HTTPRoute attached to the tenant Gateway with
// hostname `harbor.<apex>` causes Reconcile to append a per-app HTTPS
// listener to the Gateway, with the matching Certificate name and
// hostname.
func TestReconcile_HTTP01ProducesListenerForHTTPRoute(t *testing.T) {
	s := newScheme(t)
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
		Spec: gatewayv1alpha1.TenantGatewaySpec{
			Apex:               "foo.example.com",
			CertMode:           gatewayv1alpha1.CertModeHTTP01,
			GatewayClassName:   "cilium",
			AttachedNamespaces: []string{"cozy-harbor"},
		},
	}
	route := httpRouteAttached("harbor", "cozy-harbor", "harbor.foo.example.com")

	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(tgw, route).
		WithStatusSubresource(tgw).
		Build()

	r := &Reconciler{Client: c, Scheme: s}
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	gw := &gatewayv1.Gateway{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"}, gw); err != nil {
		t.Fatalf("get Gateway: %v", err)
	}
	var sawHarbor bool
	for _, l := range gw.Spec.Listeners {
		if l.Hostname != nil && string(*l.Hostname) == "harbor.foo.example.com" && l.Protocol == gatewayv1.HTTPSProtocolType {
			sawHarbor = true
			if l.TLS == nil || len(l.TLS.CertificateRefs) == 0 {
				t.Errorf("expected TLS config with certificateRefs, got %+v", l.TLS)
			}
			break
		}
	}
	if !sawHarbor {
		t.Errorf("expected per-app listener for harbor.foo.example.com, got %+v", gw.Spec.Listeners)
	}
}

// TestReconcile_HTTP01ProducesCertificateForHTTPRoute pins the
// per-listener Certificate flow: each unique HTTPRoute hostname gets a
// Certificate named after the hostname's first label, with dnsNames
// containing exactly that hostname (not wildcard).
func TestReconcile_HTTP01ProducesCertificateForHTTPRoute(t *testing.T) {
	s := newScheme(t)
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
		Spec: gatewayv1alpha1.TenantGatewaySpec{
			Apex:               "foo.example.com",
			CertMode:           gatewayv1alpha1.CertModeHTTP01,
			GatewayClassName:   "cilium",
			AttachedNamespaces: []string{"cozy-harbor"},
		},
	}
	route := httpRouteAttached("harbor", "cozy-harbor", "harbor.foo.example.com")

	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(tgw, route).
		WithStatusSubresource(tgw).
		Build()

	r := &Reconciler{Client: c, Scheme: s}
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cert := &cmv1.Certificate{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "cozystack-harbor-tls", Namespace: "tenant-foo"}, cert); err != nil {
		t.Fatalf("expected Certificate cozystack-harbor-tls: %v", err)
	}
	if got := cert.Spec.DNSNames; len(got) != 1 || got[0] != "harbor.foo.example.com" {
		t.Errorf("DNSNames=%v, want [harbor.foo.example.com]", got)
	}
	if cert.Spec.IssuerRef.Name != "cozystack-gateway" {
		t.Errorf("IssuerRef.Name=%q, want cozystack-gateway", cert.Spec.IssuerRef.Name)
	}
}

// TestReconcile_MultipleHTTPRoutesSameHostnameDeduplicates pins
// dedup: two HTTPRoutes with the same hostname (e.g. main + canary)
// produce exactly one listener and one Certificate, not two.
func TestReconcile_MultipleHTTPRoutesSameHostnameDeduplicates(t *testing.T) {
	s := newScheme(t)
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
		Spec: gatewayv1alpha1.TenantGatewaySpec{
			Apex:               "foo.example.com",
			CertMode:           gatewayv1alpha1.CertModeHTTP01,
			GatewayClassName:   "cilium",
			AttachedNamespaces: []string{"cozy-harbor"},
		},
	}
	r1 := httpRouteAttached("harbor-main", "cozy-harbor", "harbor.foo.example.com")
	r2 := httpRouteAttached("harbor-canary", "cozy-harbor", "harbor.foo.example.com")

	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(tgw, r1, r2).
		WithStatusSubresource(tgw).
		Build()

	r := &Reconciler{Client: c, Scheme: s}
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	gw := &gatewayv1.Gateway{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"}, gw); err != nil {
		t.Fatalf("get Gateway: %v", err)
	}
	var harborCount int
	for _, l := range gw.Spec.Listeners {
		if l.Hostname != nil && string(*l.Hostname) == "harbor.foo.example.com" {
			harborCount++
		}
	}
	if harborCount != 1 {
		t.Errorf("expected exactly one harbor listener, got %d", harborCount)
	}

	certs := &cmv1.CertificateList{}
	if err := c.List(context.TODO(), certs); err != nil {
		t.Fatalf("list certs: %v", err)
	}
	var harborCertCount int
	for _, ct := range certs.Items {
		if len(ct.Spec.DNSNames) == 1 && ct.Spec.DNSNames[0] == "harbor.foo.example.com" {
			harborCertCount++
		}
	}
	if harborCertCount != 1 {
		t.Errorf("expected exactly one harbor cert, got %d", harborCertCount)
	}
}

// TestReconcile_DNS01ModeIgnoresHTTPRoutesForListeners pins the inverse:
// in DNS-01 mode the wildcard listener handles everything, so the
// reconciler must NOT add per-app listeners or certs in response to
// HTTPRoutes. The static https / https-apex pair stays the only
// HTTPS listeners.
func TestReconcile_DNS01ModeIgnoresHTTPRoutesForListeners(t *testing.T) {
	s := newScheme(t)
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
		Spec: gatewayv1alpha1.TenantGatewaySpec{
			Apex:               "foo.example.com",
			CertMode:           gatewayv1alpha1.CertModeDNS01,
			GatewayClassName:   "cilium",
			AttachedNamespaces: []string{"cozy-harbor"},
			DNS01: &gatewayv1alpha1.DNS01Config{
				Provider: "cloudflare",
				Cloudflare: &gatewayv1alpha1.CloudflareDNS01{
					APITokenSecretRef: corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "cf-token"},
						Key:                  "api-token",
					},
				},
			},
		},
	}
	route := httpRouteAttached("harbor", "cozy-harbor", "harbor.foo.example.com")

	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(tgw, route).
		WithStatusSubresource(tgw).
		Build()

	r := &Reconciler{Client: c, Scheme: s}
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	gw := &gatewayv1.Gateway{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"}, gw); err != nil {
		t.Fatalf("get Gateway: %v", err)
	}
	for _, l := range gw.Spec.Listeners {
		if l.Hostname != nil && string(*l.Hostname) == "harbor.foo.example.com" {
			t.Errorf("DNS-01 mode must not render per-app listener; found %+v", l)
		}
	}
	cert := &cmv1.Certificate{}
	err := c.Get(context.TODO(), types.NamespacedName{Name: "cozystack-harbor-tls", Namespace: "tenant-foo"}, cert)
	if err == nil {
		t.Errorf("DNS-01 mode must not render per-app cert")
	}
}

// TestReconcile_HTTP01DoesNotCreateWildcardCertificate pins the
// inverse: HTTP-01 mode must NOT create the wildcard Certificate (the
// underlying ACME challenge type can't issue wildcards).
func TestReconcile_HTTP01DoesNotCreateWildcardCertificate(t *testing.T) {
	s := newScheme(t)
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
		Spec: gatewayv1alpha1.TenantGatewaySpec{
			Apex:             "foo.example.com",
			CertMode:         gatewayv1alpha1.CertModeHTTP01,
			GatewayClassName: "cilium",
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(tgw).WithStatusSubresource(tgw).Build()

	r := &Reconciler{Client: c, Scheme: s}
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cert := &cmv1.Certificate{}
	err := c.Get(context.TODO(), types.NamespacedName{Name: "cozystack-gateway-tls", Namespace: "tenant-foo"}, cert)
	if err == nil {
		t.Errorf("HTTP-01 mode rendered wildcard Certificate; should be absent")
	}
}
