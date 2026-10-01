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
	"strings"
	"testing"

	cmv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	gatewayv1alpha1 "github.com/cozystack/cozystack/api/gateway/v1alpha1"
)

var shippedPassthroughServices = []string{"api", "vm-exportproxy", "cdi-uploadproxy"}

// renderedGateways returns every Gateway the spec renders, the
// passthrough one only when the spec asks for it.
func renderedGateways(t *testing.T, tgw *gatewayv1alpha1.TenantGateway, dynHostnames []string) []*gatewayv1.Gateway {
	t.Helper()
	r := &Reconciler{Scheme: newScheme(t)}
	main, err := r.renderGateway(tgw, dynHostnames, nil)
	if err != nil {
		t.Fatalf("renderGateway: %v", err)
	}
	out := []*gatewayv1.Gateway{main}
	pt, err := r.renderPassthroughGateway(tgw)
	if err != nil {
		t.Fatalf("renderPassthroughGateway: %v", err)
	}
	if pt != nil {
		out = append(out, pt)
	}
	return out
}

// TestNoRenderedGatewayMixesTerminateAndPassthroughOnOnePort pins the
// layout rule the whole passthrough split exists for. Gateway API lets
// an implementation refuse HTTPS and TLS listeners on one port of one
// address even when their hostnames differ ("Compatible Listeners"),
// and Cilium from v1.19.6 marks such a pair ProtocolConflict, dropping
// both from Envoy from v1.20. So no Gateway may carry the two on one
// port, whatever the cert mode, whatever the hostnames.
func TestNoRenderedGatewayMixesTerminateAndPassthroughOnOnePort(t *testing.T) {
	const apex = "foo.example.com"
	for _, mode := range []gatewayv1alpha1.CertMode{
		gatewayv1alpha1.CertModeHTTP01,
		gatewayv1alpha1.CertModeDNS01,
		gatewayv1alpha1.CertModeExistingSecret,
	} {
		t.Run(string(mode), func(t *testing.T) {
			tgw := &gatewayv1alpha1.TenantGateway{
				ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
				Spec: gatewayv1alpha1.TenantGatewaySpec{
					Apex:                   apex,
					CertMode:               mode,
					GatewayClassName:       "cilium",
					WildcardSecretRef:      &corev1.LocalObjectReference{Name: "wildcard-tls"},
					TLSPassthroughServices: shippedPassthroughServices,
					TLSPassthroughListeners: []gatewayv1alpha1.TLSPassthroughListener{
						{Name: "postgres", Port: 5432, Hostname: "postgres." + apex},
					},
				},
			}
			gateways := renderedGateways(t, tgw, []string{"app." + apex, "dashboard." + apex})
			if len(gateways) != 2 {
				t.Fatalf("rendered %d Gateways, want 2: the port-443 passthrough services need their own", len(gateways))
			}
			for _, gw := range gateways {
				terminate := map[gatewayv1.PortNumber]string{}
				passthrough := map[gatewayv1.PortNumber]string{}
				for _, l := range gw.Spec.Listeners {
					if l.TLS == nil || l.TLS.Mode == nil {
						continue
					}
					switch *l.TLS.Mode {
					case gatewayv1.TLSModeTerminate:
						terminate[l.Port] = string(l.Name)
					case gatewayv1.TLSModePassthrough:
						passthrough[l.Port] = string(l.Name)
					}
				}
				for port, name := range terminate {
					if other, ok := passthrough[port]; ok {
						t.Errorf("Gateway %s carries terminate listener %s and passthrough listener %s on port %d", gw.Name, name, other, port)
					}
				}
			}
		})
	}
}

// TestPassthroughServicesRenderOnTheirOwnGateway pins the shape of the
// second Gateway: the class and namespace of the main one, one TLS
// passthrough listener per service on 443, TLSRoute alone, and an
// owner reference so it goes with the TenantGateway.
func TestPassthroughServicesRenderOnTheirOwnGateway(t *testing.T) {
	const apex = "foo.example.com"
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo", UID: "tgw-uid"},
		Spec: gatewayv1alpha1.TenantGatewaySpec{
			Apex:                   apex,
			CertMode:               gatewayv1alpha1.CertModeHTTP01,
			GatewayClassName:       "some-class",
			TLSPassthroughServices: shippedPassthroughServices,
		},
	}
	gateways := renderedGateways(t, tgw, nil)
	if len(gateways) != 2 {
		t.Fatalf("rendered %d Gateways, want 2", len(gateways))
	}
	main, pt := gateways[0], gateways[1]

	for _, l := range main.Spec.Listeners {
		if l.Protocol == gatewayv1.TLSProtocolType && l.Port == httpsListenerPort {
			t.Errorf("main Gateway still carries port-443 passthrough listener %s", l.Name)
		}
	}

	if pt.Name != "cozystack-passthrough" || pt.Namespace != "tenant-foo" {
		t.Errorf("passthrough Gateway is %s/%s, want tenant-foo/cozystack-passthrough", pt.Namespace, pt.Name)
	}
	if pt.Spec.GatewayClassName != "some-class" {
		t.Errorf("passthrough Gateway class %q, want the tenant's class some-class", pt.Spec.GatewayClassName)
	}
	if !ownedByTenantGateway(pt.OwnerReferences, tgw) {
		t.Errorf("passthrough Gateway is not owned by the TenantGateway: %+v", pt.OwnerReferences)
	}
	if got, want := len(pt.Spec.Listeners), len(shippedPassthroughServices); got != want {
		t.Fatalf("passthrough Gateway has %d listeners, want %d", got, want)
	}
	for i, svc := range shippedPassthroughServices {
		l := pt.Spec.Listeners[i]
		if string(l.Name) != passthroughListenerPrefix+svc {
			t.Errorf("listener %d named %s, want %s", i, l.Name, passthroughListenerPrefix+svc)
		}
		if l.Hostname == nil || string(*l.Hostname) != svc+"."+apex {
			t.Errorf("listener %s hostname %v, want %s", l.Name, l.Hostname, svc+"."+apex)
		}
		if l.Port != httpsListenerPort || l.Protocol != gatewayv1.TLSProtocolType {
			t.Errorf("listener %s is %s/%d, want TLS/443", l.Name, l.Protocol, l.Port)
		}
		if l.TLS == nil || l.TLS.Mode == nil || *l.TLS.Mode != gatewayv1.TLSModePassthrough {
			t.Errorf("listener %s is not in Passthrough mode: %+v", l.Name, l.TLS)
		}
		if l.AllowedRoutes == nil || len(l.AllowedRoutes.Kinds) != 1 || l.AllowedRoutes.Kinds[0].Kind != "TLSRoute" {
			t.Errorf("listener %s admits %+v, want TLSRoute alone", l.Name, l.AllowedRoutes)
		}
	}
}

// TestNoPassthroughGatewayWithoutServices pins that the second Gateway
// exists only when something is published through it: a Gateway costs
// the tenant an address, and edge renders no TLS listener at all.
func TestNoPassthroughGatewayWithoutServices(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mode     gatewayv1alpha1.CertMode
		services []string
	}{
		{"no services", gatewayv1alpha1.CertModeHTTP01, nil},
		{"edge", gatewayv1alpha1.CertModeEdge, shippedPassthroughServices},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tgw := &gatewayv1alpha1.TenantGateway{
				ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
				Spec: gatewayv1alpha1.TenantGatewaySpec{
					Apex:                   "foo.example.com",
					CertMode:               tc.mode,
					GatewayClassName:       "cilium",
					TLSPassthroughServices: tc.services,
				},
			}
			if gateways := renderedGateways(t, tgw, nil); len(gateways) != 1 {
				t.Errorf("rendered %d Gateways, want the main one alone", len(gateways))
			}
		})
	}
}

// TestReconcile_PassthroughGatewayFollowsTheServiceList pins the
// lifecycle: declaring services creates the second Gateway, dropping
// them deletes it, so a tenant that stops publishing through it gets
// its address back.
func TestReconcile_PassthroughGatewayFollowsTheServiceList(t *testing.T) {
	s := newScheme(t)
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
		Spec: gatewayv1alpha1.TenantGatewaySpec{
			Apex:                   "foo.example.com",
			CertMode:               gatewayv1alpha1.CertModeHTTP01,
			GatewayClassName:       "cilium",
			TLSPassthroughServices: shippedPassthroughServices,
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(tgw).WithStatusSubresource(tgw).Build()
	r := &Reconciler{Client: c, Scheme: s}
	key := types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"}
	ptKey := types.NamespacedName{Name: "cozystack-passthrough", Namespace: "tenant-foo"}

	if _, err := r.Reconcile(context.TODO(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if err := c.Get(context.TODO(), ptKey, &gatewayv1.Gateway{}); err != nil {
		t.Fatalf("passthrough Gateway not created: %v", err)
	}

	updated := &gatewayv1alpha1.TenantGateway{}
	if err := c.Get(context.TODO(), key, updated); err != nil {
		t.Fatalf("get TenantGateway: %v", err)
	}
	updated.Spec.TLSPassthroughServices = nil
	if err := c.Update(context.TODO(), updated); err != nil {
		t.Fatalf("drop services: %v", err)
	}
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if err := c.Get(context.TODO(), ptKey, &gatewayv1.Gateway{}); !apierrors.IsNotFound(err) {
		t.Errorf("passthrough Gateway survived the empty service list: err=%v", err)
	}
	if err := c.Get(context.TODO(), key, &gatewayv1.Gateway{}); err != nil {
		t.Errorf("main Gateway went with it: %v", err)
	}
}

// TestReconcile_PassthroughGatewayRefusesAForeignGateway pins the same
// takeover guard the main Gateway has: a Gateway of that name the
// TenantGateway does not own is neither overwritten nor deleted.
func TestReconcile_PassthroughGatewayRefusesAForeignGateway(t *testing.T) {
	for _, services := range [][]string{shippedPassthroughServices, nil} {
		t.Run(fmt.Sprintf("services=%d", len(services)), func(t *testing.T) {
			s := newScheme(t)
			tgw := &gatewayv1alpha1.TenantGateway{
				ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
				Spec: gatewayv1alpha1.TenantGatewaySpec{
					Apex:                   "foo.example.com",
					CertMode:               gatewayv1alpha1.CertModeHTTP01,
					GatewayClassName:       "cilium",
					TLSPassthroughServices: services,
				},
			}
			foreign := &gatewayv1.Gateway{
				ObjectMeta: metav1.ObjectMeta{Name: "cozystack-passthrough", Namespace: "tenant-foo"},
				Spec: gatewayv1.GatewaySpec{
					GatewayClassName: "other",
					Listeners:        []gatewayv1.Listener{{Name: "x", Port: 8443, Protocol: gatewayv1.TLSProtocolType}},
				},
			}
			c := fake.NewClientBuilder().WithScheme(s).WithObjects(tgw, foreign).WithStatusSubresource(tgw).Build()
			r := &Reconciler{Client: c, Scheme: s}
			_, err := r.Reconcile(context.TODO(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"}})
			if services != nil && err == nil {
				t.Error("expected the reconcile to refuse the foreign Gateway, got nil")
			}
			got := &gatewayv1.Gateway{}
			if err := c.Get(context.TODO(), client.ObjectKeyFromObject(foreign), got); err != nil {
				t.Fatalf("foreign Gateway was deleted: %v", err)
			}
			if got.Spec.GatewayClassName != "other" {
				t.Errorf("foreign Gateway was overwritten: class %q", got.Spec.GatewayClassName)
			}
		})
	}
}

// programGateway writes the status a Gateway controller writes once it
// has accepted and programmed the Gateway and every listener.
func programGateway(t *testing.T, c client.Client, key types.NamespacedName) {
	t.Helper()
	gw := &gatewayv1.Gateway{}
	if err := c.Get(context.TODO(), key, gw); err != nil {
		t.Fatalf("get Gateway %s: %v", key, err)
	}
	ok := func(typ string) metav1.Condition {
		return metav1.Condition{Type: typ, Status: metav1.ConditionTrue, Reason: typ, LastTransitionTime: metav1.Now()}
	}
	gw.Status.Conditions = []metav1.Condition{ok("Accepted"), ok("Programmed")}
	gw.Status.Listeners = nil
	for _, l := range gw.Spec.Listeners {
		gw.Status.Listeners = append(gw.Status.Listeners, gatewayv1.ListenerStatus{
			Name:           l.Name,
			Conditions:     []metav1.Condition{ok("Accepted"), ok("Programmed")},
			SupportedKinds: []gatewayv1.RouteGroupKind{},
		})
	}
	if err := c.Status().Update(context.TODO(), gw); err != nil {
		t.Fatalf("program Gateway %s: %v", key, err)
	}
}

func readyCondition(t *testing.T, c client.Client, key types.NamespacedName) *metav1.Condition {
	t.Helper()
	got := &gatewayv1alpha1.TenantGateway{}
	if err := c.Get(context.TODO(), key, got); err != nil {
		t.Fatalf("get TenantGateway: %v", err)
	}
	for i := range got.Status.Conditions {
		if got.Status.Conditions[i].Type == "Ready" {
			return &got.Status.Conditions[i]
		}
	}
	t.Fatalf("no Ready condition on %+v", got.Status)
	return nil
}

// TestReconcile_ReadyWaitsForThePassthroughGateway pins that Ready
// covers every Gateway the TenantGateway renders: the tenant chart
// waits on Ready, and a passthrough Gateway with no address is a
// published service nobody can reach.
func TestReconcile_ReadyWaitsForThePassthroughGateway(t *testing.T) {
	s := newScheme(t)
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
		Spec: gatewayv1alpha1.TenantGatewaySpec{
			Apex:                   "foo.example.com",
			CertMode:               gatewayv1alpha1.CertModeHTTP01,
			GatewayClassName:       "cilium",
			TLSPassthroughServices: []string{"api"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(tgw).WithStatusSubresource(tgw, &gatewayv1.Gateway{}).Build()
	r := &Reconciler{Client: c, Scheme: s}
	key := types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"}
	ptKey := types.NamespacedName{Name: "cozystack-passthrough", Namespace: "tenant-foo"}

	if _, err := r.Reconcile(context.TODO(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	programGateway(t, c, key)
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if ready := readyCondition(t, c, key); ready.Status != metav1.ConditionFalse {
		t.Errorf("Ready=%s with the passthrough Gateway unprogrammed, want False", ready.Status)
	}

	programGateway(t, c, ptKey)
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("third reconcile: %v", err)
	}
	if ready := readyCondition(t, c, key); ready.Status != metav1.ConditionTrue {
		t.Errorf("Ready=%s (%s) with both Gateways programmed, want True", ready.Status, ready.Message)
	}
	got := &gatewayv1alpha1.TenantGateway{}
	if err := c.Get(context.TODO(), key, got); err != nil {
		t.Fatalf("get TenantGateway: %v", err)
	}
	found := false
	for _, l := range got.Status.Listeners {
		if l.Name == passthroughListenerPrefix+"api" {
			found = true
		}
	}
	if !found {
		t.Errorf("status listeners %+v leave out the passthrough Gateway's tls-api", got.Status.Listeners)
	}
}

// onServiceGateway points every TLSRoute parentRef that names one of
// tgw's tlsPassthroughServices sections at the passthrough Gateway,
// where those sections render, and returns tgw and objs for the fake
// client.
func onServiceGateway(tgw *gatewayv1alpha1.TenantGateway, objs ...client.Object) []client.Object {
	services := map[string]struct{}{}
	for _, svc := range tgw.Spec.TLSPassthroughServices {
		services[passthroughListenerPrefix+svc] = struct{}{}
	}
	for _, o := range objs {
		route, ok := o.(*gatewayv1alpha2.TLSRoute)
		if !ok {
			continue
		}
		for i, ref := range route.Spec.ParentRefs {
			if ref.SectionName == nil || string(ref.Name) != tgw.Name {
				continue
			}
			if _, ok := services[string(*ref.SectionName)]; ok {
				route.Spec.ParentRefs[i].Name = gatewayv1.ObjectName(passthroughGatewayName(tgw))
			}
		}
	}
	return append([]client.Object{tgw}, objs...)
}

// tenantListeners returns the listeners of every Gateway key's
// TenantGateway renders, as one Gateway for the assertion helpers.
func tenantListeners(t *testing.T, c client.Client, key types.NamespacedName) *gatewayv1.Gateway {
	t.Helper()
	gw := &gatewayv1.Gateway{}
	if err := c.Get(context.TODO(), key, gw); err != nil {
		t.Fatalf("get Gateway %s: %v", key, err)
	}
	pt := &gatewayv1.Gateway{}
	err := c.Get(context.TODO(), types.NamespacedName{Namespace: key.Namespace, Name: key.Name + "-passthrough"}, pt)
	switch {
	case err == nil:
		gw.Spec.Listeners = append(gw.Spec.Listeners, pt.Spec.Listeners...)
	case !apierrors.IsNotFound(err):
		t.Fatalf("get passthrough Gateway: %v", err)
	}
	return gw
}

// passthroughGatewayTLSRoute is a TLSRoute on a port-443 passthrough
// service, which lives on the passthrough Gateway.
func passthroughGatewayTLSRoute(name, ns, hostname, service string) *gatewayv1alpha2.TLSRoute {
	route := tlsRouteAttached(name, ns, hostname, passthroughListenerPrefix+service, "tenant-foo")
	route.Spec.ParentRefs[0].Name = "cozystack-passthrough"
	return route
}

// TestReconcile_TLSRouteOnThePassthroughGatewayHoldsItsHostname pins
// that a route on the second Gateway still takes its name off the main
// one: DNS sends that name to the passthrough address, so a terminate
// listener and an HTTP-01 order for it on the main Gateway would never
// be reached.
func TestReconcile_TLSRouteOnThePassthroughGatewayHoldsItsHostname(t *testing.T) {
	const hostname = "api.foo.example.com"
	s := newScheme(t)
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
		Spec: gatewayv1alpha1.TenantGatewaySpec{
			Apex:                   "foo.example.com",
			CertMode:               gatewayv1alpha1.CertModeHTTP01,
			GatewayClassName:       "cilium",
			TLSPassthroughServices: []string{"api"},
		},
	}
	route := httpRouteAttached("api", "tenant-foo", hostname)
	claimant := passthroughGatewayTLSRoute("api-tls", "tenant-foo", hostname, "api")
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(tgw, route, claimant).
		WithObjects(tlsRouteBackends(claimant)...).
		WithStatusSubresource(tgw, route, claimant).
		Build()
	r := &Reconciler{Client: c, Scheme: s}
	key := types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"}
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	gw := &gatewayv1.Gateway{}
	if err := c.Get(context.TODO(), key, gw); err != nil {
		t.Fatalf("get Gateway: %v", err)
	}
	if terminate, _ := listenerNamesByProtocol(gw, hostname); len(terminate) != 0 {
		t.Errorf("main Gateway terminates %s on %v although the passthrough Gateway carries it", hostname, terminate)
	}
}

// reconcileServices reconciles a TenantGateway publishing services on
// the passthrough Gateway, with objs alongside.
func reconcileServices(t *testing.T, services []string, objs ...client.Object) client.Client {
	t.Helper()
	s := newScheme(t)
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
		Spec: gatewayv1alpha1.TenantGatewaySpec{
			Apex:                   "foo.example.com",
			CertMode:               gatewayv1alpha1.CertModeHTTP01,
			GatewayClassName:       "cilium",
			TLSPassthroughServices: services,
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(append([]client.Object{tgw}, objs...)...).
		WithStatusSubresource(append([]client.Object{tgw}, objs...)...).
		Build()
	r := &Reconciler{Client: c, Scheme: s}
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(tgw)}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	return c
}

func mainGateway(t *testing.T, c client.Client) *gatewayv1.Gateway {
	t.Helper()
	gw := &gatewayv1.Gateway{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"}, gw); err != nil {
		t.Fatalf("get main Gateway: %v", err)
	}
	return gw
}

// TestReconcile_HTTPRouteOnThePassthroughGatewayClaimsNothing pins that
// an HTTPRoute naming the passthrough Gateway earns no terminate
// listener and no Certificate on the main one: that Gateway carries TLS
// listeners alone, so the route attaches to nothing there and a
// listener for it on the main Gateway would serve a route that never
// named it.
func TestReconcile_HTTPRouteOnThePassthroughGatewayClaimsNothing(t *testing.T) {
	const hostname = "shop.foo.example.com"
	route := httpRouteAttached("shop", "tenant-foo", hostname)
	route.Spec.ParentRefs[0].Name = "cozystack-passthrough"
	c := reconcileServices(t, []string{"api"}, route)

	if terminate, _ := listenerNamesByProtocol(mainGateway(t, c), hostname); len(terminate) != 0 {
		t.Errorf("main Gateway renders %v for a route that names the passthrough Gateway", terminate)
	}
	certs := &cmv1.CertificateList{}
	if err := c.List(context.TODO(), certs); err != nil {
		t.Fatal(err)
	}
	if names := certNamesOrdering(certs, hostname); len(names) != 0 {
		t.Errorf("Certificate %v issued for a route that names the passthrough Gateway", names)
	}
}

// TestReconcile_TLSRouteOnTheMainGatewayIsNotServedByThePassthroughOne
// pins that a TLSRoute naming the main Gateway with no sectionName is
// judged against the main Gateway's listeners alone. The tls-api
// listener on the passthrough Gateway answers the same SNI, but it is
// not a listener of the Gateway the route named, so the route serves
// nothing and the terminate listener for the name stays.
func TestReconcile_TLSRouteOnTheMainGatewayIsNotServedByThePassthroughOne(t *testing.T) {
	const hostname = "api.foo.example.com"
	route := httpRouteAttached("api", "tenant-foo", hostname)
	unpinned := tlsRouteAttached("api-tls", "tenant-foo", hostname, "", "tenant-foo")
	unpinned.Spec.ParentRefs[0].SectionName = nil
	c := reconcileServices(t, []string{"api"}, append([]client.Object{route, unpinned}, tlsRouteBackends(unpinned)...)...)

	if terminate, _ := listenerNamesByProtocol(mainGateway(t, c), hostname); len(terminate) != 1 {
		t.Errorf("main Gateway terminates %s on %v, want one listener: no listener of the Gateway the TLSRoute named answers it", hostname, terminate)
	}
}

// TestReconcile_HostnamelessTLSRouteBorrowsOnlyFromTheGatewayItNames
// pins that a TLSRoute declaring no hostnames borrows the hostname of
// a passthrough listener only on the Gateway it names. On the main
// Gateway there is none, so it claims nothing and this controller
// writes no condition on it.
func TestReconcile_HostnamelessTLSRouteBorrowsOnlyFromTheGatewayItNames(t *testing.T) {
	route := tlsRouteAttached("borrower", "tenant-foo", "", "", "tenant-foo")
	route.Spec.ParentRefs[0].SectionName = nil
	route.Spec.Hostnames = nil
	c := reconcileServices(t, []string{"vm-exportproxy"}, append([]client.Object{route}, tlsRouteBackends(route)...)...)

	got := &gatewayv1alpha2.TLSRoute{}
	if err := c.Get(context.TODO(), client.ObjectKeyFromObject(route), got); err != nil {
		t.Fatal(err)
	}
	for _, p := range got.Status.Parents {
		if string(p.ControllerName) == testControllerName {
			t.Errorf("route naming the main Gateway was judged on a name borrowed from the passthrough Gateway: %+v", p.Conditions)
		}
	}
}

// TestReconcile_UnansweredTLSRouteIsNotPointedAtTheOtherGateway pins
// what a TLSRoute hears when it names tls-<svc> on the main Gateway for
// a hostname nothing answers. The section exists only on the passthrough
// Gateway, so telling the route that its section answers a different
// hostname would send its owner to a listener the Gateway it named does
// not have.
func TestReconcile_UnansweredTLSRouteIsNotPointedAtTheOtherGateway(t *testing.T) {
	route := tlsRouteAttached("other-tls", "tenant-foo", "other.foo.example.com", passthroughListenerPrefix+"api", "tenant-foo")
	c := reconcileServices(t, []string{"api"}, append([]client.Object{route}, tlsRouteBackends(route)...)...)

	got := &gatewayv1alpha2.TLSRoute{}
	if err := c.Get(context.TODO(), client.ObjectKeyFromObject(route), got); err != nil {
		t.Fatal(err)
	}
	var cond *metav1.Condition
	for _, p := range got.Status.Parents {
		if string(p.ControllerName) != testControllerName {
			continue
		}
		for i := range p.Conditions {
			if p.Conditions[i].Type == "Accepted" {
				cond = &p.Conditions[i]
			}
		}
	}
	if cond == nil || cond.Status != metav1.ConditionFalse {
		t.Fatalf("want Accepted=False on an unanswered hostname, got %+v", cond)
	}
	want := "On TenantGateway tenant-foo/cozystack: hostname(s) other.foo.example.com claimed only by a TLSRoute, which needs a passthrough listener this Gateway does not declare"
	if cond.Message != want {
		t.Errorf("message %q, want %q: %s is not a listener of the Gateway the route named, so it cannot be the section that answers another hostname", cond.Message, want, passthroughListenerPrefix+"api")
	}
}

// TestReconcile_TLSRouteOnAServiceSectionOfTheMainGatewayIsRefused pins
// what a route written for the old layout hears: tls-<svc> is no longer
// a listener of the main Gateway, so a route naming it there attaches
// to nothing and is told so rather than silently kept.
func TestReconcile_TLSRouteOnAServiceSectionOfTheMainGatewayIsRefused(t *testing.T) {
	const hostname = "api.foo.example.com"
	s := newScheme(t)
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
		Spec: gatewayv1alpha1.TenantGatewaySpec{
			Apex:                   "foo.example.com",
			CertMode:               gatewayv1alpha1.CertModeHTTP01,
			GatewayClassName:       "cilium",
			TLSPassthroughServices: []string{"api"},
		},
	}
	stale := tlsRouteAttached("api-tls", "tenant-foo", hostname, passthroughListenerPrefix+"api", "tenant-foo")
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(tgw, stale).
		WithObjects(tlsRouteBackends(stale)...).
		WithStatusSubresource(tgw, stale).
		Build()
	r := &Reconciler{Client: c, Scheme: s}
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got := &gatewayv1alpha2.TLSRoute{}
	if err := c.Get(context.TODO(), client.ObjectKeyFromObject(stale), got); err != nil {
		t.Fatalf("get TLSRoute: %v", err)
	}
	if len(got.Status.Parents) == 0 {
		t.Fatal("route on a section the main Gateway no longer has got no status at all")
	}
	for _, p := range got.Status.Parents {
		for _, cond := range p.Conditions {
			if cond.Type == string(gatewayv1.RouteConditionAccepted) && cond.Status == metav1.ConditionTrue {
				t.Errorf("route on a section the main Gateway no longer has reads Accepted=True: %+v", cond)
			}
		}
	}
}

// TestReconcile_ReadyNamesAClassThatCannotServePassthrough pins the
// portability contract: a GatewayClass declares what it supports in
// status.supportedFeatures, and a TenantGateway rendering passthrough
// listeners on one that lacks TLSRoute reports it rather than reading
// Ready on listeners nothing will serve. A class that declares nothing
// is not judged, since older implementations do not fill the field in.
func TestReconcile_ReadyNamesAClassThatCannotServePassthrough(t *testing.T) {
	apiListener := []gatewayv1alpha1.TLSPassthroughListener{{Name: "api", Port: 6443, Hostname: "api.foo.example.com"}}
	for _, tc := range []struct {
		name      string
		features  []gatewayv1.FeatureName
		listeners []gatewayv1alpha1.TLSPassthroughListener
		wantClass bool
	}{
		{"class without TLSRoute, passthrough listener", []gatewayv1.FeatureName{"Gateway", "HTTPRoute"}, apiListener, true},
		{"class with TLSRoute", []gatewayv1.FeatureName{"Gateway", "HTTPRoute", "TLSRoute"}, apiListener, false},
		{"class declaring nothing", nil, apiListener, false},
		{"class without TLSRoute, no passthrough", []gatewayv1.FeatureName{"Gateway", "HTTPRoute"}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newScheme(t)
			tgw := &gatewayv1alpha1.TenantGateway{
				ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
				Spec: gatewayv1alpha1.TenantGatewaySpec{
					Apex:                    "foo.example.com",
					CertMode:                gatewayv1alpha1.CertModeHTTP01,
					GatewayClassName:        "other",
					TLSPassthroughListeners: tc.listeners,
				},
			}
			class := &gatewayv1.GatewayClass{
				ObjectMeta: metav1.ObjectMeta{Name: "other"},
				Spec:       gatewayv1.GatewayClassSpec{ControllerName: "example.com/other"},
			}
			for _, f := range tc.features {
				class.Status.SupportedFeatures = append(class.Status.SupportedFeatures, gatewayv1.SupportedFeature{Name: f})
			}
			c := fake.NewClientBuilder().WithScheme(s).WithObjects(tgw, class).WithStatusSubresource(tgw, &gatewayv1.Gateway{}).Build()
			r := &Reconciler{Client: c, Scheme: s}
			key := types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"}
			if _, err := r.Reconcile(context.TODO(), ctrl.Request{NamespacedName: key}); err != nil {
				t.Fatalf("first reconcile: %v", err)
			}
			programGateway(t, c, key)
			if _, err := r.Reconcile(context.TODO(), ctrl.Request{NamespacedName: key}); err != nil {
				t.Fatalf("second reconcile: %v", err)
			}
			ready := readyCondition(t, c, key)
			if got := ready.Reason == "GatewayClassUnsupported"; got != tc.wantClass {
				t.Errorf("Ready=%s reason=%s (%s), want the class refusal: %v", ready.Status, ready.Reason, ready.Message, tc.wantClass)
			}
			if tc.wantClass && (ready.Status != metav1.ConditionFalse || !strings.Contains(ready.Message, "TLSRoute") || !strings.Contains(ready.Message, "other")) {
				t.Errorf("refusal must be Ready=False naming the class and the feature: %+v", ready)
			}
		})
	}
}
