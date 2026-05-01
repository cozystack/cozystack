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
	"strings"
	"testing"

	cmv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
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

// TestReconcile_IsIdempotent pins the no-op reconcile contract: a
// second Reconcile pass over the same TenantGateway with no spec
// change must not bump ResourceVersion on any owned resource. Without
// this guarantee, every reconcile triggers the Owns/Watches and the
// controller hot-loops indefinitely (continuous cluster writes,
// rate-limited only by the workqueue). Confirmed manually that the
// pre-fix code bumped Gateway / Issuer RV on every pass.
func TestReconcile_IsIdempotent(t *testing.T) {
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
		WithStatusSubresource(tgw, &gatewayv1.Gateway{}, &gatewayv1.HTTPRoute{}).
		Build()

	r := &Reconciler{Client: c, Scheme: s}
	for i := 0; i < 2; i++ {
		if _, err := r.Reconcile(context.TODO(), ctrl.Request{
			NamespacedName: types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"},
		}); err != nil {
			t.Fatalf("reconcile pass %d: %v", i+1, err)
		}
	}

	gw := &gatewayv1.Gateway{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"}, gw); err != nil {
		t.Fatalf("get Gateway: %v", err)
	}
	rvAfterFirst := gw.ResourceVersion

	// Third pass: still no diff, RV must not move.
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"},
	}); err != nil {
		t.Fatalf("third reconcile: %v", err)
	}
	gw2 := &gatewayv1.Gateway{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"}, gw2); err != nil {
		t.Fatalf("get Gateway after pass 3: %v", err)
	}
	if gw2.ResourceVersion != rvAfterFirst {
		t.Errorf("Gateway ResourceVersion bumped on no-op reconcile: %s → %s", rvAfterFirst, gw2.ResourceVersion)
	}

	iss := &cmv1.Issuer{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "cozystack-gateway", Namespace: "tenant-foo"}, iss); err != nil {
		t.Fatalf("get Issuer: %v", err)
	}
	rvIssuer := iss.ResourceVersion
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"},
	}); err != nil {
		t.Fatalf("fourth reconcile: %v", err)
	}
	iss2 := &cmv1.Issuer{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "cozystack-gateway", Namespace: "tenant-foo"}, iss2); err != nil {
		t.Fatalf("get Issuer after pass 4: %v", err)
	}
	if iss2.ResourceVersion != rvIssuer {
		t.Errorf("Issuer ResourceVersion bumped on no-op reconcile: %s → %s", rvIssuer, iss2.ResourceVersion)
	}
}

// TestReconcile_HTTPListenerExcludesAppNamespaces pins the
// security contract: the HTTP listener (port 80) accepts routes
// only from the tenant namespace (controller's redirect HTTPRoute)
// and the cert-manager challenge namespace. App namespaces
// (cozy-harbor, cozy-keycloak, etc.) are explicitly excluded so
// app HTTPRoutes that attach by hostname (no sectionName) cannot
// bind to port 80 and silently serve plaintext.
func TestReconcile_HTTPListenerExcludesAppNamespaces(t *testing.T) {
	s := newScheme(t)
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
		Spec: gatewayv1alpha1.TenantGatewaySpec{
			Apex:               "foo.example.com",
			CertMode:           gatewayv1alpha1.CertModeHTTP01,
			GatewayClassName:   "cilium",
			AttachedNamespaces: []string{"cozy-harbor", "cozy-keycloak", "cozy-cert-manager"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(tgw).WithStatusSubresource(tgw, &gatewayv1.Gateway{}).Build()

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

	var httpListener *gatewayv1.Listener
	var httpsListener *gatewayv1.Listener
	for i := range gw.Spec.Listeners {
		switch gw.Spec.Listeners[i].Name {
		case "http":
			httpListener = &gw.Spec.Listeners[i]
		}
		if gw.Spec.Listeners[i].Hostname != nil {
			httpsListener = &gw.Spec.Listeners[i]
		}
	}
	if httpListener == nil {
		t.Fatalf("http listener not found")
	}

	httpValues := httpListener.AllowedRoutes.Namespaces.Selector.MatchExpressions[0].Values
	if !containsString(httpValues, "tenant-foo") {
		t.Errorf("http listener missing tenant-foo: %v", httpValues)
	}
	if !containsString(httpValues, "cozy-cert-manager") {
		t.Errorf("http listener missing cozy-cert-manager (HTTP-01 ACME would break): %v", httpValues)
	}
	for _, app := range []string{"cozy-harbor", "cozy-keycloak"} {
		if containsString(httpValues, app) {
			t.Errorf("http listener accepts %s — apps from this namespace can serve plaintext on port 80: %v", app, httpValues)
		}
	}

	if httpsListener != nil {
		httpsValues := httpsListener.AllowedRoutes.Namespaces.Selector.MatchExpressions[0].Values
		// HTTPS listeners keep the broader app-namespaces list.
		if !containsString(httpsValues, "cozy-harbor") {
			t.Errorf("https listener should still accept cozy-harbor: %v", httpsValues)
		}
	}
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// TestReconcile_CertModeTransitionHTTP01ToDNS01CleansPerListenerCerts
// pins the GC contract: switching certMode from http01 to dns01
// reclaims per-listener Certificates created during the http01
// phase. Without it, those Certificates outlive the mode change,
// keep their backing Secrets around, and count against LE rate
// limits indefinitely.
func TestReconcile_CertModeTransitionHTTP01ToDNS01CleansPerListenerCerts(t *testing.T) {
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
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(tgw, route).WithStatusSubresource(tgw, &gatewayv1.Gateway{}).Build()
	r := &Reconciler{Client: c, Scheme: s}

	// Phase 1: HTTP-01 reconcile creates a per-listener cert.
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"},
	}); err != nil {
		t.Fatalf("phase 1 reconcile: %v", err)
	}
	preCerts := &cmv1.CertificateList{}
	if err := c.List(context.TODO(), preCerts); err != nil {
		t.Fatalf("phase 1 list certs: %v", err)
	}
	var sawHarborCert bool
	for _, ct := range preCerts.Items {
		if len(ct.Spec.DNSNames) == 1 && ct.Spec.DNSNames[0] == "harbor.foo.example.com" {
			sawHarborCert = true
		}
	}
	if !sawHarborCert {
		t.Fatalf("expected per-listener harbor cert after HTTP-01 phase, got %d certs", len(preCerts.Items))
	}

	// Phase 2: flip certMode to DNS-01 and reconcile again. The
	// per-listener cert from phase 1 must be gone.
	updated := &gatewayv1alpha1.TenantGateway{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"}, updated); err != nil {
		t.Fatalf("get tgw: %v", err)
	}
	updated.Spec.CertMode = gatewayv1alpha1.CertModeDNS01
	updated.Spec.DNS01 = &gatewayv1alpha1.DNS01Config{
		Provider: "cloudflare",
		Cloudflare: &gatewayv1alpha1.CloudflareDNS01{
			APITokenSecretRef: corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "cf-token"},
				Key:                  "api-token",
			},
		},
	}
	if err := c.Update(context.TODO(), updated); err != nil {
		t.Fatalf("flip certMode: %v", err)
	}
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"},
	}); err != nil {
		t.Fatalf("phase 2 reconcile: %v", err)
	}

	postCerts := &cmv1.CertificateList{}
	if err := c.List(context.TODO(), postCerts); err != nil {
		t.Fatalf("phase 2 list certs: %v", err)
	}
	for _, ct := range postCerts.Items {
		if len(ct.Spec.DNSNames) == 1 && ct.Spec.DNSNames[0] == "harbor.foo.example.com" {
			t.Errorf("per-listener harbor cert leaked into DNS-01 phase: %+v", ct.Name)
		}
	}
}

// TestReconcile_CertModeTransitionDNS01ToHTTP01CleansWildcardCert
// pins the symmetric path: switching from dns01 to http01 deletes
// the wildcard Certificate left behind by the previous DNS-01
// phase.
func TestReconcile_CertModeTransitionDNS01ToHTTP01CleansWildcardCert(t *testing.T) {
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
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(tgw).WithStatusSubresource(tgw, &gatewayv1.Gateway{}).Build()
	r := &Reconciler{Client: c, Scheme: s}

	if _, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"},
	}); err != nil {
		t.Fatalf("phase 1 reconcile: %v", err)
	}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "cozystack-gateway-tls", Namespace: "tenant-foo"}, &cmv1.Certificate{}); err != nil {
		t.Fatalf("expected wildcard cert in DNS-01 phase: %v", err)
	}

	updated := &gatewayv1alpha1.TenantGateway{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"}, updated); err != nil {
		t.Fatalf("get tgw: %v", err)
	}
	updated.Spec.CertMode = gatewayv1alpha1.CertModeHTTP01
	updated.Spec.DNS01 = nil
	if err := c.Update(context.TODO(), updated); err != nil {
		t.Fatalf("flip certMode: %v", err)
	}
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"},
	}); err != nil {
		t.Fatalf("phase 2 reconcile: %v", err)
	}

	if err := c.Get(context.TODO(), types.NamespacedName{Name: "cozystack-gateway-tls", Namespace: "tenant-foo"}, &cmv1.Certificate{}); err == nil {
		t.Errorf("wildcard cert leaked after switch to HTTP-01")
	}
}

// TestReconcile_RouteFromUnwhitelistedNamespaceIgnored pins the
// safety filter: HTTPRoutes whose namespace is not the tenant
// namespace and not in Spec.AttachedNamespaces are ignored by the
// reconciler (no per-listener cert, no listener). The Gateway's
// own allowedRoutes selector rejects the actual attach at runtime,
// but provisioning a cert for that hostname would still eat LE rate
// limits and leak the operator's reachable hostnames.
func TestReconcile_RouteFromUnwhitelistedNamespaceIgnored(t *testing.T) {
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
	// Route in cozy-harbor — allowed.
	allowed := httpRouteAttached("harbor", "cozy-harbor", "harbor.foo.example.com")
	// Route in tenant-attacker — NOT in AttachedNamespaces.
	stray := httpRouteAttached("phish", "tenant-attacker", "phish.foo.example.com")

	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(tgw, allowed, stray).
		WithStatusSubresource(tgw, &gatewayv1.Gateway{}).
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
		if l.Hostname != nil && string(*l.Hostname) == "phish.foo.example.com" {
			t.Errorf("listener for unwhitelisted-namespace hostname rendered: %+v", l)
		}
	}

	// The harbor cert exists; no phish cert is provisioned.
	certs := &cmv1.CertificateList{}
	if err := c.List(context.TODO(), certs); err != nil {
		t.Fatalf("list certs: %v", err)
	}
	var sawHarbor, sawPhish bool
	for _, ct := range certs.Items {
		if len(ct.Spec.DNSNames) == 1 {
			switch ct.Spec.DNSNames[0] {
			case "harbor.foo.example.com":
				sawHarbor = true
			case "phish.foo.example.com":
				sawPhish = true
			}
		}
	}
	if !sawHarbor {
		t.Errorf("expected harbor cert (allowed namespace) — none of %d certs match", len(certs.Items))
	}
	if sawPhish {
		t.Errorf("phish cert was provisioned despite tenant-attacker not being in AttachedNamespaces")
	}
}

// TestReconcile_RendersHTTPToHTTPSRedirectRoute pins the security
// contract: every TenantGateway materialises a controller-owned
// HTTPRoute attached to sectionName=http carrying a 301 redirect to
// HTTPS. Without this, app HTTPRoutes that attach to the Gateway by
// hostname (no sectionName) silently serve plaintext on port 80,
// downgrading the legacy nginx Ingress ssl-redirect contract.
func TestReconcile_RendersHTTPToHTTPSRedirectRoute(t *testing.T) {
	s := newScheme(t)
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
		Spec: gatewayv1alpha1.TenantGatewaySpec{
			Apex:             "foo.example.com",
			CertMode:         gatewayv1alpha1.CertModeHTTP01,
			GatewayClassName: "cilium",
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(tgw).WithStatusSubresource(tgw, &gatewayv1.Gateway{}).Build()

	r := &Reconciler{Client: c, Scheme: s}
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &gatewayv1.HTTPRoute{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "cozystack-http-redirect", Namespace: "tenant-foo"}, got); err != nil {
		t.Fatalf("expected redirect HTTPRoute cozystack-http-redirect: %v", err)
	}
	if len(got.Spec.ParentRefs) != 1 {
		t.Fatalf("expected one parentRef, got %+v", got.Spec.ParentRefs)
	}
	pr := got.Spec.ParentRefs[0]
	if pr.SectionName == nil || string(*pr.SectionName) != "http" {
		t.Errorf("parentRef.SectionName=%v, want http", pr.SectionName)
	}
	if len(got.Spec.Rules) != 1 || len(got.Spec.Rules[0].Filters) != 1 {
		t.Fatalf("expected exactly one rule with one filter, got %+v", got.Spec.Rules)
	}
	f := got.Spec.Rules[0].Filters[0]
	if f.Type != gatewayv1.HTTPRouteFilterRequestRedirect {
		t.Errorf("filter type=%s, want RequestRedirect", f.Type)
	}
	if f.RequestRedirect == nil || f.RequestRedirect.Scheme == nil || *f.RequestRedirect.Scheme != "https" {
		t.Errorf("filter scheme=%v, want https", f.RequestRedirect)
	}
	if f.RequestRedirect.StatusCode == nil || *f.RequestRedirect.StatusCode != 301 {
		t.Errorf("filter status=%v, want 301", f.RequestRedirect.StatusCode)
	}
}

// TestReconcile_GatewayUpdatePreservesForeignLabels pins the
// label-merge contract: a Gateway carrying labels written by other
// actors (Cilium operator, kubectl label, future controllers) keeps
// those labels across reconciliation. Wholesale replacement would
// drop them — Gateway is shared infra, not an operator-only field.
func TestReconcile_GatewayUpdatePreservesForeignLabels(t *testing.T) {
	s := newScheme(t)
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
		Spec: gatewayv1alpha1.TenantGatewaySpec{
			Apex:             "foo.example.com",
			CertMode:         gatewayv1alpha1.CertModeHTTP01,
			GatewayClassName: "cilium",
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(tgw).WithStatusSubresource(tgw, &gatewayv1.Gateway{}).Build()
	r := &Reconciler{Client: c, Scheme: s}

	// First reconcile creates the Gateway.
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"},
	}); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}

	// Simulate another actor stamping a foreign label.
	gw := &gatewayv1.Gateway{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"}, gw); err != nil {
		t.Fatalf("get Gateway: %v", err)
	}
	if gw.Labels == nil {
		gw.Labels = map[string]string{}
	}
	gw.Labels["example.com/owner"] = "someone-else"
	if err := c.Update(context.TODO(), gw); err != nil {
		t.Fatalf("foreign label update: %v", err)
	}

	// Second reconcile must merge, not clobber.
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"},
	}); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	got := &gatewayv1.Gateway{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"}, got); err != nil {
		t.Fatalf("get Gateway: %v", err)
	}
	if got.Labels["example.com/owner"] != "someone-else" {
		t.Errorf("foreign label dropped on update; labels=%v", got.Labels)
	}
	if got.Labels["cozystack.io/managed-by"] != "cozystack-controller" {
		t.Errorf("controller label missing; labels=%v", got.Labels)
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

// TestReconcile_IssuerNameStagingHitsStagingACME pins the LE-stage
// path: spec.issuerName=letsencrypt-stage produces an Issuer pointing
// at the LE staging ACME server, NOT the production one. Without this
// wiring an operator who set issuerName=letsencrypt-stage on a dev
// cluster would silently get prod-issued certs and burn through real
// LE rate limits.
func TestReconcile_IssuerNameStagingHitsStagingACME(t *testing.T) {
	s := newScheme(t)
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
		Spec: gatewayv1alpha1.TenantGatewaySpec{
			Apex:             "foo.example.com",
			CertMode:         gatewayv1alpha1.CertModeHTTP01,
			IssuerName:       gatewayv1alpha1.IssuerNameLetsEncryptStage,
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
	if iss.Spec.ACME == nil {
		t.Fatalf("expected ACME issuer, got %+v", iss.Spec)
	}
	if iss.Spec.ACME.Server != "https://acme-staging-v02.api.letsencrypt.org/directory" {
		t.Errorf("ACME.Server=%q, want LE staging URL", iss.Spec.ACME.Server)
	}
}

// TestReconcile_IssuerNameProdHitsProdACME pins the default path:
// no issuerName set (or letsencrypt-prod) → prod ACME server.
func TestReconcile_IssuerNameProdHitsProdACME(t *testing.T) {
	s := newScheme(t)
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
		Spec: gatewayv1alpha1.TenantGatewaySpec{
			Apex:             "foo.example.com",
			CertMode:         gatewayv1alpha1.CertModeHTTP01,
			GatewayClassName: "cilium",
			// IssuerName intentionally unset.
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
	if iss.Spec.ACME.Server != "https://acme-v02.api.letsencrypt.org/directory" {
		t.Errorf("ACME.Server=%q, want LE prod URL", iss.Spec.ACME.Server)
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

	// Listener+cert names embed a content-addressed hostname suffix
	// to avoid collisions; look up the cert by DNSNames instead.
	certs := &cmv1.CertificateList{}
	if err := c.List(context.TODO(), certs); err != nil {
		t.Fatalf("list certs: %v", err)
	}
	var cert *cmv1.Certificate
	for i := range certs.Items {
		if len(certs.Items[i].Spec.DNSNames) == 1 && certs.Items[i].Spec.DNSNames[0] == "harbor.foo.example.com" {
			cert = &certs.Items[i]
			break
		}
	}
	if cert == nil {
		t.Fatalf("expected Certificate with dnsNames=[harbor.foo.example.com], got %d certs", len(certs.Items))
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

// ControllerName is the controllerName used by this controller in
// RouteParentStatus entries. Mirrors the constant in conflict.go.
const testControllerName = "gateway.cozystack.io/tenantgateway-controller"

// TestReconcile_ListenersHaveAllowedRoutesSelector pins Layer 1 of
// the security model: every listener carries an AllowedRoutes
// selector keyed on kubernetes.io/metadata.name (kube-apiserver-
// written, unspoofable). Without this, routes from outside the
// tenant namespace silently fail to attach (default From: Same).
func TestReconcile_ListenersHaveAllowedRoutesSelector(t *testing.T) {
	s := newScheme(t)
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
		Spec: gatewayv1alpha1.TenantGatewaySpec{
			Apex:               "foo.example.com",
			CertMode:           gatewayv1alpha1.CertModeHTTP01,
			GatewayClassName:   "cilium",
			AttachedNamespaces: []string{"cozy-harbor", "cozy-dashboard"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(tgw).WithStatusSubresource(tgw).Build()

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
		if l.AllowedRoutes == nil || l.AllowedRoutes.Namespaces == nil ||
			l.AllowedRoutes.Namespaces.From == nil ||
			*l.AllowedRoutes.Namespaces.From != gatewayv1.NamespacesFromSelector {
			t.Fatalf("listener %s missing Selector AllowedRoutes: %+v", l.Name, l.AllowedRoutes)
		}
		sel := l.AllowedRoutes.Namespaces.Selector
		if sel == nil || len(sel.MatchExpressions) != 1 {
			t.Fatalf("listener %s expected one MatchExpression, got %+v", l.Name, sel)
		}
		expr := sel.MatchExpressions[0]
		if expr.Key != "kubernetes.io/metadata.name" {
			t.Errorf("listener %s selector key=%q, want kubernetes.io/metadata.name", l.Name, expr.Key)
		}
		// http listener carries a narrower allowedRoutes (tenant ns
		// + cert-manager challenge ns) — see TestReconcile_HTTPListenerExcludesAppNamespaces.
		// Other listeners get the broad attached-namespaces list.
		var want []string
		if string(l.Name) == "http" {
			want = []string{"tenant-foo", "cozy-cert-manager"}
		} else {
			want = []string{"tenant-foo", "cozy-harbor", "cozy-dashboard"}
		}
		got := expr.Values
		if len(got) != len(want) {
			t.Errorf("listener %s selector values=%v, want %v", l.Name, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("listener %s selector values[%d]=%q, want %q", l.Name, i, got[i], want[i])
			}
		}
	}
}

// TestReconcile_TLSPassthroughListenersRendered pins the Passthrough
// listener flow: each entry in TLSPassthroughServices materialises a
// dedicated tls-<svc> listener (port 443, protocol TLS, mode
// Passthrough) with hostname <svc>.<apex> and AllowedRoutes.Kinds
// restricted to TLSRoute. The TLSRoute templates for cozystack-api,
// vm-exportproxy and cdi-uploadproxy attach to these by sectionName.
func TestReconcile_TLSPassthroughListenersRendered(t *testing.T) {
	s := newScheme(t)
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
		Spec: gatewayv1alpha1.TenantGatewaySpec{
			Apex:                   "foo.example.com",
			CertMode:               gatewayv1alpha1.CertModeHTTP01,
			GatewayClassName:       "cilium",
			TLSPassthroughServices: []string{"api", "vm-exportproxy", "cdi-uploadproxy"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(tgw).WithStatusSubresource(tgw).Build()

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
	wanted := map[string]string{
		"tls-api":             "api.foo.example.com",
		"tls-vm-exportproxy":  "vm-exportproxy.foo.example.com",
		"tls-cdi-uploadproxy": "cdi-uploadproxy.foo.example.com",
	}
	for _, l := range gw.Spec.Listeners {
		host, want := wanted[string(l.Name)]
		if !want {
			continue
		}
		delete(wanted, string(l.Name))

		if l.Protocol != gatewayv1.TLSProtocolType {
			t.Errorf("%s protocol=%s, want TLS", l.Name, l.Protocol)
		}
		if l.Port != 443 {
			t.Errorf("%s port=%d, want 443", l.Name, l.Port)
		}
		if l.Hostname == nil || string(*l.Hostname) != host {
			t.Errorf("%s hostname=%v, want %s", l.Name, l.Hostname, host)
		}
		if l.TLS == nil || l.TLS.Mode == nil || *l.TLS.Mode != gatewayv1.TLSModePassthrough {
			t.Errorf("%s TLS mode is not Passthrough: %+v", l.Name, l.TLS)
		}
		if l.AllowedRoutes == nil || len(l.AllowedRoutes.Kinds) != 1 ||
			l.AllowedRoutes.Kinds[0].Kind != "TLSRoute" {
			t.Errorf("%s AllowedRoutes.Kinds restriction missing: %+v", l.Name, l.AllowedRoutes)
		}
	}
	if len(wanted) > 0 {
		t.Errorf("expected listeners not rendered: %+v", wanted)
	}
}

// TestReconcile_StatusObservedGeneration pins observedGeneration: the
// status field tracks .metadata.generation so operators can tell
// whether the controller has caught up with the latest spec.
func TestReconcile_StatusObservedGeneration(t *testing.T) {
	s := newScheme(t)
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "cozystack",
			Namespace:  "tenant-foo",
			Generation: 7,
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

	got := &gatewayv1alpha1.TenantGateway{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"}, got); err != nil {
		t.Fatalf("get tgw: %v", err)
	}
	if got.Status.ObservedGeneration != 7 {
		t.Errorf("Status.ObservedGeneration=%d, want 7", got.Status.ObservedGeneration)
	}
}

// TestReconcile_StatusListenersMirrorGateway pins
// status.listeners — one TenantGatewayListenerStatus entry per
// Listener on the rendered Gateway. The static `http` listener is
// always present in HTTP-01 mode; the test asserts at least that one
// shows up with its hostname carried through.
func TestReconcile_StatusListenersMirrorGateway(t *testing.T) {
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

	got := &gatewayv1alpha1.TenantGateway{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"}, got); err != nil {
		t.Fatalf("get tgw: %v", err)
	}
	var sawHTTP, sawHarbor bool
	for _, l := range got.Status.Listeners {
		if l.Name == "http" {
			sawHTTP = true
		}
		if l.Hostname == "harbor.foo.example.com" {
			sawHarbor = true
			if l.CertificateName == "" {
				t.Errorf("expected CertificateName populated for harbor listener, got %+v", l)
			}
		}
	}
	if !sawHTTP {
		t.Errorf("expected http listener in Status.Listeners, got %+v", got.Status.Listeners)
	}
	if !sawHarbor {
		t.Errorf("expected harbor listener in Status.Listeners, got %+v", got.Status.Listeners)
	}
}

// TestReconcile_StatusReadyFalseUntilGatewayProgrammed pins the
// readiness contract: until the Gateway controller marks the
// underlying Gateway Programmed=True, the TenantGateway carries
// Ready=False with a non-empty Reason. Operators waiting on
// `kubectl wait --for=condition=Ready` see real progress, not a
// fictional green flag the moment the CR is created.
func TestReconcile_StatusReadyFalseUntilGatewayProgrammed(t *testing.T) {
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

	got := &gatewayv1alpha1.TenantGateway{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"}, got); err != nil {
		t.Fatalf("get tgw: %v", err)
	}
	var ready *metav1.Condition
	for i := range got.Status.Conditions {
		if got.Status.Conditions[i].Type == "Ready" {
			ready = &got.Status.Conditions[i]
			break
		}
	}
	if ready == nil {
		t.Fatalf("expected Ready condition, got %+v", got.Status.Conditions)
	}
	if ready.Status != metav1.ConditionFalse {
		t.Errorf("Ready.Status=%s, want False (Gateway not yet Programmed)", ready.Status)
	}
	if ready.Reason == "" {
		t.Errorf("expected non-empty Reason on Ready=False, got %+v", ready)
	}
}

// TestReconcile_StatusReadyTrueWhenGatewayProgrammed pins the green
// path: once the Gateway controller writes Accepted=True +
// Programmed=True on the Gateway and per-listener Accepted=True +
// Programmed=True on each ListenerStatus, the TenantGateway flips
// Ready=True.
func TestReconcile_StatusReadyTrueWhenGatewayProgrammed(t *testing.T) {
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
		WithStatusSubresource(tgw, &gatewayv1.Gateway{}).
		Build()

	r := &Reconciler{Client: c, Scheme: s}
	// First reconcile creates the Gateway; we then patch its status to
	// simulate Cilium's controller having reconciled it, and run a
	// second reconcile so the TenantGateway picks up the new status.
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"},
	}); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	gw := &gatewayv1.Gateway{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"}, gw); err != nil {
		t.Fatalf("get Gateway: %v", err)
	}
	gw.Status.Conditions = []metav1.Condition{
		{Type: "Accepted", Status: metav1.ConditionTrue, Reason: "Accepted", LastTransitionTime: metav1.Now()},
		{Type: "Programmed", Status: metav1.ConditionTrue, Reason: "Programmed", LastTransitionTime: metav1.Now()},
	}
	gw.Status.Listeners = make([]gatewayv1.ListenerStatus, 0, len(gw.Spec.Listeners))
	for _, l := range gw.Spec.Listeners {
		gw.Status.Listeners = append(gw.Status.Listeners, gatewayv1.ListenerStatus{
			Name: l.Name,
			Conditions: []metav1.Condition{
				{Type: "Accepted", Status: metav1.ConditionTrue, Reason: "Accepted", LastTransitionTime: metav1.Now()},
				{Type: "Programmed", Status: metav1.ConditionTrue, Reason: "Programmed", LastTransitionTime: metav1.Now()},
			},
			SupportedKinds: []gatewayv1.RouteGroupKind{},
		})
	}
	if err := c.Status().Update(context.TODO(), gw); err != nil {
		t.Fatalf("patch Gateway status: %v", err)
	}

	if _, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"},
	}); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	got := &gatewayv1alpha1.TenantGateway{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"}, got); err != nil {
		t.Fatalf("get tgw: %v", err)
	}
	var ready *metav1.Condition
	for i := range got.Status.Conditions {
		if got.Status.Conditions[i].Type == "Ready" {
			ready = &got.Status.Conditions[i]
			break
		}
	}
	if ready == nil || ready.Status != metav1.ConditionTrue {
		t.Errorf("expected Ready=True after Gateway Programmed, got %+v", ready)
	}
	for _, l := range got.Status.Listeners {
		if !l.Ready {
			t.Errorf("expected listener %s ready=true, got %+v", l.Name, l)
		}
	}
}

// TestReconcile_TwoRoutesSameHostnameCozyWins pins the conflict
// resolution rule: when two HTTPRoutes attached to the same Gateway
// claim the same hostname but live in different namespaces, the
// cozy-* namespace wins and the other route gets a
// HostnameConflict condition under our controllerName in its
// Status.Parents.
func TestReconcile_TwoRoutesSameHostnameCozyWins(t *testing.T) {
	s := newScheme(t)
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
		Spec: gatewayv1alpha1.TenantGatewaySpec{
			Apex:               "foo.example.com",
			CertMode:           gatewayv1alpha1.CertModeHTTP01,
			GatewayClassName:   "cilium",
			AttachedNamespaces: []string{"cozy-harbor", "tenant-foo"},
		},
	}
	cozyRoute := httpRouteAttached("harbor", "cozy-harbor", "harbor.foo.example.com")
	tenantRoute := httpRouteAttached("harbor-shadow", "tenant-foo", "harbor.foo.example.com")

	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(tgw, cozyRoute, tenantRoute).
		WithStatusSubresource(tgw, &gatewayv1.HTTPRoute{}).
		Build()

	r := &Reconciler{Client: c, Scheme: s}
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Listener / cert exist (winner served).
	gw := &gatewayv1.Gateway{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"}, gw); err != nil {
		t.Fatalf("get Gateway: %v", err)
	}
	var sawHarbor bool
	for _, l := range gw.Spec.Listeners {
		if l.Hostname != nil && string(*l.Hostname) == "harbor.foo.example.com" {
			sawHarbor = true
			break
		}
	}
	if !sawHarbor {
		t.Errorf("expected harbor listener present (winner served), got %+v", gw.Spec.Listeners)
	}

	// Loser HTTPRoute carries HostnameConflict condition under our
	// controllerName in Status.Parents.
	got := &gatewayv1.HTTPRoute{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "harbor-shadow", Namespace: "tenant-foo"}, got); err != nil {
		t.Fatalf("get loser route: %v", err)
	}
	var sawConflict bool
	for _, ps := range got.Status.Parents {
		if string(ps.ControllerName) != testControllerName {
			continue
		}
		for _, cond := range ps.Conditions {
			if cond.Type == "Accepted" && cond.Status == metav1.ConditionFalse && cond.Reason == "HostnameConflict" {
				sawConflict = true
				break
			}
		}
	}
	if !sawConflict {
		t.Errorf("expected HostnameConflict condition on loser route, got Status.Parents=%+v", got.Status.Parents)
	}

	// Winner HTTPRoute carries Accepted=True (no conflict) under our
	// controllerName.
	winner := &gatewayv1.HTTPRoute{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "harbor", Namespace: "cozy-harbor"}, winner); err != nil {
		t.Fatalf("get winner route: %v", err)
	}
	var sawAccepted bool
	for _, ps := range winner.Status.Parents {
		if string(ps.ControllerName) != testControllerName {
			continue
		}
		for _, cond := range ps.Conditions {
			if cond.Type == "Accepted" && cond.Status == metav1.ConditionTrue {
				sawAccepted = true
			}
		}
	}
	if !sawAccepted {
		t.Errorf("expected Accepted=True on winner route, got Status.Parents=%+v", winner.Status.Parents)
	}
}

// TestReconcile_SameNamespaceSameHostnameNoConflict pins the dedup
// path: two HTTPRoutes in the same namespace claiming the same
// hostname is normal (canary, version split) — no conflict
// condition should be raised.
func TestReconcile_SameNamespaceSameHostnameNoConflict(t *testing.T) {
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
		WithStatusSubresource(tgw, &gatewayv1.HTTPRoute{}).
		Build()

	r := &Reconciler{Client: c, Scheme: s}
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, name := range []string{"harbor-main", "harbor-canary"} {
		got := &gatewayv1.HTTPRoute{}
		if err := c.Get(context.TODO(), types.NamespacedName{Name: name, Namespace: "cozy-harbor"}, got); err != nil {
			t.Fatalf("get %s: %v", name, err)
		}
		for _, ps := range got.Status.Parents {
			if string(ps.ControllerName) != testControllerName {
				continue
			}
			for _, cond := range ps.Conditions {
				if cond.Reason == "HostnameConflict" {
					t.Errorf("unexpected HostnameConflict on %s (same-namespace dedup is not a conflict)", name)
				}
			}
		}
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

// TestReconcile_HTTPSListenersRestrictRouteKindsToHTTPRoute pins the
// round-7 hardening: every HTTPS (TLS-terminate) listener must declare
// AllowedRoutes.Kinds=[HTTPRoute]. Without that explicit restriction
// Gateway API's default behaviour permits any route kind whose hostname
// matches a listener — so a tenant carrying RBAC for GRPCRoute /
// TCPRoute / UDPRoute could attach by hostname to a TLS-terminate
// listener, bypassing the route-hostname VAP (which only binds to
// HTTPRoute and TLSRoute) and serving traffic under the apex cert
// without admission validation.
//
// Both certMode branches are exercised: HTTP-01 (per-app https-<label>
// listeners) and DNS-01 (the wildcard `https` + apex `https-apex`
// pair). The TLS-passthrough listener test elsewhere already pins
// Kinds=[TLSRoute] for that branch; this one covers TLS-terminate.
func TestReconcile_HTTPSListenersRestrictRouteKindsToHTTPRoute(t *testing.T) {
	cases := []struct {
		name string
		tgw  *gatewayv1alpha1.TenantGateway
		// extra objects to seed (e.g. HTTPRoute so HTTP-01 mode renders a per-app listener)
		extra []client.Object
	}{
		{
			name: "DNS-01 mode (wildcard + apex listeners)",
			tgw: &gatewayv1alpha1.TenantGateway{
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
			},
		},
		{
			name: "HTTP-01 mode (per-app listener from attached HTTPRoute)",
			tgw: &gatewayv1alpha1.TenantGateway{
				ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
				Spec: gatewayv1alpha1.TenantGatewaySpec{
					Apex:             "foo.example.com",
					CertMode:         gatewayv1alpha1.CertModeHTTP01,
					GatewayClassName: "cilium",
				},
			},
			extra: []client.Object{
				&gatewayv1.HTTPRoute{
					ObjectMeta: metav1.ObjectMeta{Name: "harbor", Namespace: "tenant-foo"},
					Spec: gatewayv1.HTTPRouteSpec{
						Hostnames: []gatewayv1.Hostname{"harbor.foo.example.com"},
						CommonRouteSpec: gatewayv1.CommonRouteSpec{
							ParentRefs: []gatewayv1.ParentReference{
								{
									Group:     ptrGroup(gatewayv1.GroupName),
									Kind:      ptrKind("Gateway"),
									Name:      "cozystack",
									Namespace: ptrNamespace("tenant-foo"),
								},
							},
						},
					},
				},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newScheme(t)
			builder := fake.NewClientBuilder().WithScheme(s).WithObjects(tc.tgw).WithStatusSubresource(tc.tgw)
			if len(tc.extra) > 0 {
				builder = builder.WithObjects(tc.extra...)
			}
			c := builder.Build()
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
			httpsCount := 0
			for _, l := range gw.Spec.Listeners {
				if l.Protocol != gatewayv1.HTTPSProtocolType {
					continue
				}
				httpsCount++
				if l.AllowedRoutes == nil || len(l.AllowedRoutes.Kinds) != 1 {
					t.Fatalf("listener %s: expected exactly one allowed Kind, got %+v", l.Name, l.AllowedRoutes)
				}
				gk := l.AllowedRoutes.Kinds[0]
				if gk.Kind != "HTTPRoute" {
					t.Errorf("listener %s: AllowedRoutes.Kinds[0]=%q, want HTTPRoute", l.Name, gk.Kind)
				}
				if gk.Group == nil || *gk.Group != gatewayv1.Group(gatewayv1.GroupName) {
					t.Errorf("listener %s: AllowedRoutes.Kinds[0].Group=%v, want %q", l.Name, gk.Group, gatewayv1.GroupName)
				}
			}
			if httpsCount == 0 {
				t.Fatalf("expected at least one HTTPS listener, listeners=%+v", gw.Spec.Listeners)
			}
		})
	}
}

// TestReconcile_DNS01IssuerRoute53Solver pins the DNS-01 + route53 path
// (added in branch-review round 6 alongside cloudflare). The Issuer
// must carry a dns01.route53 solver block referencing the operator-
// supplied IAM credentials. Without coverage, a future renderer
// refactor could regress to the cloudflare-only path the round-1 draft
// shipped with.
func TestReconcile_DNS01IssuerRoute53Solver(t *testing.T) {
	s := newScheme(t)
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
		Spec: gatewayv1alpha1.TenantGatewaySpec{
			Apex:             "foo.example.com",
			CertMode:         gatewayv1alpha1.CertModeDNS01,
			GatewayClassName: "cilium",
			DNS01: &gatewayv1alpha1.DNS01Config{
				Provider: "route53",
				Route53: &gatewayv1alpha1.Route53DNS01{
					Region:      "us-east-1",
					AccessKeyID: "AKIAIOSFODNN7EXAMPLE",
					SecretAccessKeySecretRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "aws-iam-secret"},
						Key:                  "secret-access-key",
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
	if solver.DNS01 == nil || solver.DNS01.Route53 == nil {
		t.Fatalf("expected dns01.route53 solver, got %+v", solver)
	}
	r53 := solver.DNS01.Route53
	if r53.Region != "us-east-1" {
		t.Errorf("Route53 Region=%q, want us-east-1", r53.Region)
	}
	if r53.AccessKeyID != "AKIAIOSFODNN7EXAMPLE" {
		t.Errorf("Route53 AccessKeyID=%q, want AKIAIOSFODNN7EXAMPLE", r53.AccessKeyID)
	}
	if r53.SecretAccessKey.Name != "aws-iam-secret" || r53.SecretAccessKey.Key != "secret-access-key" {
		t.Errorf("Route53 SecretAccessKey ref=%+v, want name=aws-iam-secret key=secret-access-key", r53.SecretAccessKey)
	}
}

// TestReconcile_DNS01IssuerDigitalOceanSolver pins the DNS-01 +
// digitalocean path. Mirrors the cloudflare/route53 solver tests so
// every advertised provider has a Go-level pin.
func TestReconcile_DNS01IssuerDigitalOceanSolver(t *testing.T) {
	s := newScheme(t)
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
		Spec: gatewayv1alpha1.TenantGatewaySpec{
			Apex:             "foo.example.com",
			CertMode:         gatewayv1alpha1.CertModeDNS01,
			GatewayClassName: "cilium",
			DNS01: &gatewayv1alpha1.DNS01Config{
				Provider: "digitalocean",
				DigitalOcean: &gatewayv1alpha1.DigitalOceanDNS01{
					TokenSecretRef: corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "do-api-token"},
						Key:                  "access-token",
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
	if solver.DNS01 == nil || solver.DNS01.DigitalOcean == nil {
		t.Fatalf("expected dns01.digitalocean solver, got %+v", solver)
	}
	tok := solver.DNS01.DigitalOcean.Token
	if tok.Name != "do-api-token" || tok.Key != "access-token" {
		t.Errorf("DigitalOcean Token ref=%+v, want name=do-api-token key=access-token", tok)
	}
}

// TestReconcile_DNS01IssuerRFC2136Solver pins the DNS-01 + rfc2136
// path (BIND-style dynamic update). The TSIG algorithm default is
// also exercised — leaving it empty must produce HMACSHA256 in the
// rendered solver, matching cert-manager's documented default.
func TestReconcile_DNS01IssuerRFC2136Solver(t *testing.T) {
	s := newScheme(t)
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
		Spec: gatewayv1alpha1.TenantGatewaySpec{
			Apex:             "foo.example.com",
			CertMode:         gatewayv1alpha1.CertModeDNS01,
			GatewayClassName: "cilium",
			DNS01: &gatewayv1alpha1.DNS01Config{
				Provider: "rfc2136",
				RFC2136: &gatewayv1alpha1.RFC2136DNS01{
					Nameserver:  "ns1.example.test:53",
					TSIGKeyName: "letsencrypt.example.test.",
					// TSIGAlgorithm intentionally empty to pin the default.
					TSIGSecretSecretRef: corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "tsig-secret"},
						Key:                  "tsig-secret-key",
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
	if solver.DNS01 == nil || solver.DNS01.RFC2136 == nil {
		t.Fatalf("expected dns01.rfc2136 solver, got %+v", solver)
	}
	r2136 := solver.DNS01.RFC2136
	if r2136.Nameserver != "ns1.example.test:53" {
		t.Errorf("RFC2136 Nameserver=%q, want ns1.example.test:53", r2136.Nameserver)
	}
	if r2136.TSIGKeyName != "letsencrypt.example.test." {
		t.Errorf("RFC2136 TSIGKeyName=%q, want letsencrypt.example.test.", r2136.TSIGKeyName)
	}
	if r2136.TSIGAlgorithm != "HMACSHA256" {
		t.Errorf("RFC2136 TSIGAlgorithm=%q, want HMACSHA256 (default)", r2136.TSIGAlgorithm)
	}
	if r2136.TSIGSecret.Name != "tsig-secret" || r2136.TSIGSecret.Key != "tsig-secret-key" {
		t.Errorf("RFC2136 TSIGSecret ref=%+v, want name=tsig-secret key=tsig-secret-key", r2136.TSIGSecret)
	}
}

// TestReconcile_DNS01ProviderMissingConfigErrors pins the input-
// validation surface added in round 6: each non-cloudflare provider
// returns a deterministic error if the operator omits the matching
// config block. Without these guards the controller would crash when
// dereferencing the nil pointer (panic on a single misconfigured
// tenant takes the controller down for the whole cluster).
func TestReconcile_DNS01ProviderMissingConfigErrors(t *testing.T) {
	cases := []struct {
		name     string
		dns01    *gatewayv1alpha1.DNS01Config
		wantSubs string
	}{
		{
			name: "route53 without route53 block",
			dns01: &gatewayv1alpha1.DNS01Config{
				Provider: "route53",
			},
			wantSubs: "dns01.route53",
		},
		{
			name: "digitalocean without digitalocean block",
			dns01: &gatewayv1alpha1.DNS01Config{
				Provider: "digitalocean",
			},
			wantSubs: "dns01.digitalocean",
		},
		{
			name: "rfc2136 without rfc2136 block",
			dns01: &gatewayv1alpha1.DNS01Config{
				Provider: "rfc2136",
			},
			wantSubs: "dns01.rfc2136",
		},
		{
			name: "unknown provider",
			dns01: &gatewayv1alpha1.DNS01Config{
				Provider: "linode",
			},
			wantSubs: "unsupported dns01.provider",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tgw := &gatewayv1alpha1.TenantGateway{
				ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
				Spec: gatewayv1alpha1.TenantGatewaySpec{
					Apex:             "foo.example.com",
					CertMode:         gatewayv1alpha1.CertModeDNS01,
					GatewayClassName: "cilium",
					DNS01:            tc.dns01,
				},
			}
			_, err := buildSolver(tgw)
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSubs) {
				t.Errorf("error=%q, want to contain %q", err.Error(), tc.wantSubs)
			}
		})
	}
}

func ptrNamespace(ns string) *gatewayv1.Namespace {
	v := gatewayv1.Namespace(ns)
	return &v
}

func ptrSectionName(s string) *gatewayv1.SectionName {
	v := gatewayv1.SectionName(s)
	return &v
}

// TestReconcile_RefusesToTakeOverForeignGateway pins the safety
// guard against silently rewriting a pre-existing Gateway that
// happens to share the TenantGateway-derived name. Without the
// ownerRef check, an operator who hand-crafted a Gateway named
// `cozystack` in the tenant namespace would lose its config (spec
// rewritten) AND have no cascade-delete chain back to the
// TenantGateway (no OwnerReference established), leaving an orphan
// after the TenantGateway is deleted.
func TestReconcile_RefusesToTakeOverForeignGateway(t *testing.T) {
	s := newScheme(t)
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
		Spec: gatewayv1alpha1.TenantGatewaySpec{
			Apex:             "foo.example.com",
			CertMode:         gatewayv1alpha1.CertModeHTTP01,
			GatewayClassName: "cilium",
		},
	}
	// Foreign Gateway with the same NamespacedName but no OwnerReference.
	foreign := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cozystack",
			Namespace: "tenant-foo",
			Labels: map[string]string{
				"author":              "operator-by-hand",
				"some.other/operator": "controlled",
			},
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: gatewayv1.ObjectName("not-cilium"),
			Listeners: []gatewayv1.Listener{
				{Name: "operator-port", Port: 9999, Protocol: gatewayv1.HTTPProtocolType},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(tgw, foreign).WithStatusSubresource(tgw).Build()

	r := &Reconciler{Client: c, Scheme: s}
	_, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"},
	})
	if err == nil {
		t.Fatalf("expected Reconcile to surface a takeover-refusal error, got nil")
	}
	if !strings.Contains(err.Error(), "not owned by TenantGateway") {
		t.Errorf("expected error mentioning ownership refusal, got: %v", err)
	}

	// The foreign Gateway must NOT be modified.
	got := &gatewayv1.Gateway{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"}, got); err != nil {
		t.Fatalf("get Gateway: %v", err)
	}
	if string(got.Spec.GatewayClassName) != "not-cilium" {
		t.Errorf("foreign Gateway.Spec was overwritten: gatewayClassName=%q, want not-cilium", got.Spec.GatewayClassName)
	}
	if len(got.Spec.Listeners) != 1 || got.Spec.Listeners[0].Port != 9999 {
		t.Errorf("foreign Gateway listeners were overwritten: %+v", got.Spec.Listeners)
	}
	if got.Labels["author"] != "operator-by-hand" {
		t.Errorf("foreign label scrubbed: labels=%+v", got.Labels)
	}

	// Status condition should reflect the failure (Ready=False with
	// the takeover error captured).
	updated := &gatewayv1alpha1.TenantGateway{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"}, updated); err != nil {
		t.Fatalf("get TenantGateway: %v", err)
	}
	hasReadyFalse := false
	for _, cond := range updated.Status.Conditions {
		if cond.Type == "Ready" && cond.Status == metav1.ConditionFalse && cond.Reason == "ReconcileError" {
			hasReadyFalse = true
			break
		}
	}
	if !hasReadyFalse {
		t.Errorf("expected Ready=False ReconcileError on TenantGateway status, got %+v", updated.Status.Conditions)
	}
}

// TestReconcile_RefusesToTakeOverForeignRedirectRoute pins the same
// guard for the controller-owned http→https redirect HTTPRoute. A
// pre-existing HTTPRoute named `<tgw>-http-redirect` could otherwise
// be silently rewritten and orphaned.
func TestReconcile_RefusesToTakeOverForeignRedirectRoute(t *testing.T) {
	s := newScheme(t)
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
		Spec: gatewayv1alpha1.TenantGatewaySpec{
			Apex:             "foo.example.com",
			CertMode:         gatewayv1alpha1.CertModeHTTP01,
			GatewayClassName: "cilium",
		},
	}
	foreign := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cozystack-http-redirect",
			Namespace: "tenant-foo",
		},
		Spec: gatewayv1.HTTPRouteSpec{
			Hostnames: []gatewayv1.Hostname{"operator.foo.example.com"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(tgw, foreign).WithStatusSubresource(tgw, &gatewayv1.HTTPRoute{}).Build()

	r := &Reconciler{Client: c, Scheme: s}
	_, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"},
	})
	if err == nil {
		t.Fatalf("expected Reconcile to surface a takeover-refusal error, got nil")
	}
	if !strings.Contains(err.Error(), "not owned by TenantGateway") {
		t.Errorf("expected error mentioning ownership refusal, got: %v", err)
	}

	// The foreign HTTPRoute hostnames must be preserved.
	got := &gatewayv1.HTTPRoute{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "cozystack-http-redirect", Namespace: "tenant-foo"}, got); err != nil {
		t.Fatalf("get HTTPRoute: %v", err)
	}
	if len(got.Spec.Hostnames) != 1 || got.Spec.Hostnames[0] != "operator.foo.example.com" {
		t.Errorf("foreign HTTPRoute spec overwritten: %+v", got.Spec)
	}
}

// TestReconcile_OwnerReferencesOnDownstream pins the cascade-delete
// contract for every controller-owned downstream resource: Issuer,
// wildcard Certificate (DNS-01 mode), per-listener Certificate
// (HTTP-01 mode), and the http→https redirect HTTPRoute. Without an
// OwnerReference back to the TenantGateway, kubectl delete on the CR
// leaves orphans behind that keep eating cert-manager rate limits and
// stale Gateway listener references.
func TestReconcile_OwnerReferencesOnDownstream(t *testing.T) {
	s := newScheme(t)
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
		Spec: gatewayv1alpha1.TenantGatewaySpec{
			Apex:             "foo.example.com",
			CertMode:         gatewayv1alpha1.CertModeHTTP01,
			GatewayClassName: "cilium",
		},
	}
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "harbor", Namespace: "tenant-foo"},
		Spec: gatewayv1.HTTPRouteSpec{
			Hostnames: []gatewayv1.Hostname{"harbor.foo.example.com"},
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{
					{
						Group:     ptrGroup(gatewayv1.GroupName),
						Kind:      ptrKind("Gateway"),
						Name:      "cozystack",
						Namespace: ptrNamespace("tenant-foo"),
					},
				},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(tgw, route).WithStatusSubresource(tgw, &gatewayv1.HTTPRoute{}).Build()

	r := &Reconciler{Client: c, Scheme: s}
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hasOwnerRef := func(refs []metav1.OwnerReference, ownerName string) bool {
		for _, ref := range refs {
			if ref.Kind == "TenantGateway" && ref.Name == ownerName && ref.Controller != nil && *ref.Controller {
				return true
			}
		}
		return false
	}

	// Issuer
	iss := &cmv1.Issuer{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "cozystack-gateway", Namespace: "tenant-foo"}, iss); err != nil {
		t.Fatalf("get Issuer: %v", err)
	}
	if !hasOwnerRef(iss.OwnerReferences, "cozystack") {
		t.Errorf("Issuer missing controller OwnerReference back to TenantGateway, got %+v", iss.OwnerReferences)
	}

	// Per-listener Certificate (HTTP-01 mode renders one per hostname).
	certList := &cmv1.CertificateList{}
	if err := c.List(context.TODO(), certList); err != nil {
		t.Fatalf("list Certificates: %v", err)
	}
	if len(certList.Items) == 0 {
		t.Fatalf("expected at least one per-listener Certificate, got 0")
	}
	for _, cert := range certList.Items {
		if !hasOwnerRef(cert.OwnerReferences, "cozystack") {
			t.Errorf("Certificate %s missing controller OwnerReference, got %+v", cert.Name, cert.OwnerReferences)
		}
	}

	// HTTP→HTTPS redirect HTTPRoute (controller-owned).
	redirect := &gatewayv1.HTTPRoute{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "cozystack-http-redirect", Namespace: "tenant-foo"}, redirect); err != nil {
		t.Fatalf("get redirect HTTPRoute: %v", err)
	}
	if !hasOwnerRef(redirect.OwnerReferences, "cozystack") {
		t.Errorf("redirect HTTPRoute missing controller OwnerReference, got %+v", redirect.OwnerReferences)
	}
}

// TestReconcile_DNS01WildcardCertOwnerReference pins the wildcard
// Certificate's OwnerReference contract, since it's only rendered in
// DNS-01 mode and the previous test exercises HTTP-01.
func TestReconcile_DNS01WildcardCertOwnerReference(t *testing.T) {
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
		t.Fatalf("get wildcard Certificate: %v", err)
	}
	hasOwner := false
	for _, ref := range cert.OwnerReferences {
		if ref.Kind == "TenantGateway" && ref.Name == "cozystack" && ref.Controller != nil && *ref.Controller {
			hasOwner = true
			break
		}
	}
	if !hasOwner {
		t.Errorf("wildcard Certificate missing controller OwnerReference, got %+v", cert.OwnerReferences)
	}
}

// TestReconcile_GatewayUpdateRestoresControllerLabel pins the inverse
// of TestReconcile_GatewayUpdatePreservesForeignLabels: a foreign
// actor that scrubs a controller-owned label must see it restored on
// the next reconcile. Without this, an out-of-band tool (or a buggy
// admission policy) could permanently strip cozystack.io/managed-by
// and break label-based selectors that depend on it.
func TestReconcile_GatewayUpdateRestoresControllerLabel(t *testing.T) {
	s := newScheme(t)
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
		Spec: gatewayv1alpha1.TenantGatewaySpec{
			Apex:             "foo.example.com",
			CertMode:         gatewayv1alpha1.CertModeHTTP01,
			GatewayClassName: "cilium",
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(tgw).WithStatusSubresource(tgw, &gatewayv1.Gateway{}).Build()
	r := &Reconciler{Client: c, Scheme: s}
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"},
	}); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}

	// Foreign actor strips the controller-owned managed-by label.
	gw := &gatewayv1.Gateway{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"}, gw); err != nil {
		t.Fatalf("get Gateway: %v", err)
	}
	delete(gw.Labels, "cozystack.io/managed-by")
	if err := c.Update(context.TODO(), gw); err != nil {
		t.Fatalf("update Gateway: %v", err)
	}

	if _, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"},
	}); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	got := &gatewayv1.Gateway{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"}, got); err != nil {
		t.Fatalf("get Gateway after second reconcile: %v", err)
	}
	if got.Labels["cozystack.io/managed-by"] != "cozystack-controller" {
		t.Errorf("controller label not restored after foreign delete: labels=%+v", got.Labels)
	}
}

// TestRender_HTTPListener_PinsACMEChallengeNamespace pins the literal
// const value `acmeChallengeNamespace = "cozy-cert-manager"`. If the
// platform ever moves cert-manager to a different namespace, this
// test fails loudly — and it's expected to be updated together with
// the namespace change so HTTP-01 challenge HTTPRoutes still bind.
// Without this pin, a refactor could change the string in one place
// (the cert-manager helm release) without updating the tenant
// Gateway's http-listener allowedRoutes.
func TestRender_HTTPListener_PinsACMEChallengeNamespace(t *testing.T) {
	if acmeChallengeNamespace != "cozy-cert-manager" {
		t.Errorf("acmeChallengeNamespace=%q, want cozy-cert-manager — if cert-manager moves, update the cozy-cert-manager helm release namespace AND this constant in lockstep, then update this test", acmeChallengeNamespace)
	}
}

// TestReconcile_MultiParentRefRouteWritesPerRefStatus pins the
// per-(ParentRef, ControllerName) status contract: when a single
// HTTPRoute carries two parentRefs to the same TenantGateway Gateway
// (different sectionNames), the controller writes one
// RouteParentStatus entry per parentRef under its ControllerName
// instead of overwriting one entry on each iteration. Prior behavior
// kept only whichever parentRef came first in pickAttachingParentRef,
// silently dropping per-section conflict signals — a regression that
// would only surface for tenants stitching multiple sectionNames into
// one HTTPRoute.
func TestReconcile_MultiParentRefRouteWritesPerRefStatus(t *testing.T) {
	s := newScheme(t)
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
		Spec: gatewayv1alpha1.TenantGatewaySpec{
			Apex:             "foo.example.com",
			CertMode:         gatewayv1alpha1.CertModeHTTP01,
			GatewayClassName: "cilium",
		},
	}
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "harbor", Namespace: "tenant-foo"},
		Spec: gatewayv1.HTTPRouteSpec{
			Hostnames: []gatewayv1.Hostname{"harbor.foo.example.com"},
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{
					{
						Group:       ptrGroup(gatewayv1.GroupName),
						Kind:        ptrKind("Gateway"),
						Name:        "cozystack",
						Namespace:   ptrNamespace("tenant-foo"),
						SectionName: ptrSectionName("https-harbor-deadbeef"),
					},
					{
						Group:       ptrGroup(gatewayv1.GroupName),
						Kind:        ptrKind("Gateway"),
						Name:        "cozystack",
						Namespace:   ptrNamespace("tenant-foo"),
						SectionName: ptrSectionName("http"),
					},
				},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(tgw, route).
		WithStatusSubresource(tgw, &gatewayv1.HTTPRoute{}).
		Build()

	r := &Reconciler{Client: c, Scheme: s}
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &gatewayv1.HTTPRoute{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "harbor", Namespace: "tenant-foo"}, got); err != nil {
		t.Fatalf("get HTTPRoute: %v", err)
	}
	ours := 0
	sections := map[string]bool{}
	for _, ps := range got.Status.Parents {
		if ps.ControllerName != "gateway.cozystack.io/tenantgateway-controller" {
			continue
		}
		ours++
		if ps.ParentRef.SectionName != nil {
			sections[string(*ps.ParentRef.SectionName)] = true
		}
	}
	if ours != 2 {
		t.Errorf("expected 2 RouteParentStatus entries under our ControllerName, got %d (full status=%+v)", ours, got.Status.Parents)
	}
	if !sections["https-harbor-deadbeef"] || !sections["http"] {
		t.Errorf("expected status entries for both sectionNames, got %+v", sections)
	}
}
