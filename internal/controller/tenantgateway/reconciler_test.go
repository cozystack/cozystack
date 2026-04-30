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
