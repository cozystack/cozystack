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
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	gatewayv1alpha1 "github.com/cozystack/cozystack/api/gateway/v1alpha1"
)

// TestMapRouteToTenantGateways_HTTPRouteEnqueuesMatchingTGW pins the
// happy path: an HTTPRoute parentRef'ing tenant-foo/cozystack returns
// a single reconcile.Request for that TenantGateway.
func TestMapRouteToTenantGateways_HTTPRouteEnqueuesMatchingTGW(t *testing.T) {
	s := newScheme(t)
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
		Spec: gatewayv1alpha1.TenantGatewaySpec{
			Apex:             "foo.example.com",
			CertMode:         gatewayv1alpha1.CertModeHTTP01,
			GatewayClassName: "cilium",
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(tgw).Build()
	r := &Reconciler{Client: c, Scheme: s}

	route := httpRouteAttached("harbor", "cozy-harbor", "harbor.foo.example.com")
	reqs := r.mapRouteToTenantGateways(context.TODO(), route)
	if len(reqs) != 1 {
		t.Fatalf("expected 1 reconcile.Request, got %d (%+v)", len(reqs), reqs)
	}
	if reqs[0].Namespace != "tenant-foo" || reqs[0].Name != "cozystack" {
		t.Errorf("expected request for tenant-foo/cozystack, got %+v", reqs[0])
	}
}

// TestMapRouteToTenantGateways_NoMatchingTGWReturnsNil pins the
// not-our-Gateway path: an HTTPRoute parentRef'ing some other
// Gateway returns no requests.
func TestMapRouteToTenantGateways_NoMatchingTGWReturnsNil(t *testing.T) {
	s := newScheme(t)
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
		Spec:       gatewayv1alpha1.TenantGatewaySpec{Apex: "foo.example.com"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(tgw).Build()
	r := &Reconciler{Client: c, Scheme: s}

	otherGroup := gatewayv1.Group(gatewayv1.GroupName)
	otherKind := gatewayv1.Kind("Gateway")
	otherNs := gatewayv1.Namespace("tenant-bar")
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "stray", Namespace: "tenant-bar"},
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{
					{Group: &otherGroup, Kind: &otherKind, Namespace: &otherNs, Name: "other-gateway"},
				},
			},
			Hostnames: []gatewayv1.Hostname{"x.bar.example.com"},
		},
	}

	if reqs := r.mapRouteToTenantGateways(context.TODO(), route); len(reqs) != 0 {
		t.Errorf("expected 0 requests, got %+v", reqs)
	}
}

// TestMapRouteToTenantGateways_EmptyParentRefsReturnsNil pins the
// guard: an HTTPRoute with no parentRefs should not enqueue any
// reconciliation.
func TestMapRouteToTenantGateways_EmptyParentRefsReturnsNil(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()
	r := &Reconciler{Client: c, Scheme: s}

	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "noref", Namespace: "tenant-foo"},
	}
	if reqs := r.mapRouteToTenantGateways(context.TODO(), route); len(reqs) != 0 {
		t.Errorf("expected 0 requests, got %+v", reqs)
	}
}

// TestMapBackendToTenantGateways covers the requeue the deferred
// withdrawal depends on. A TLSRoute takes a hostname over only once it
// forwards somewhere, so the Service arriving and the ReferenceGrant
// admitting a cross-namespace reference each have to reach the
// TenantGateway that would then shed its terminate listener.
//
// The negatives matter as much as the positives here: this maps over
// every TLSRoute in the cluster on every Service event, so a match
// that is too loose turns ordinary Service churn into reconciles of
// every tenant Gateway.
func TestMapBackendToTenantGateways(t *testing.T) {
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
		Spec: gatewayv1alpha1.TenantGatewaySpec{
			Apex:             "foo.example.com",
			CertMode:         gatewayv1alpha1.CertModeHTTP01,
			GatewayClassName: "cilium",
		},
	}
	local := tlsRouteAttached("api-tls", "tenant-foo", "api.foo.example.com", "tls-api", "tenant-foo")
	remote := tlsRouteAttached("db-tls", "tenant-foo", "db.foo.example.com", "tls-db", "tenant-foo")
	remote.Spec.Rules[0].BackendRefs = []gatewayv1alpha2.BackendRef{tlsBackendRef("db-backend", "cozy-public")}

	cases := []struct {
		name string
		obj  client.Object
		want int
	}{
		{
			name: "the Service a route names",
			obj:  &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: tlsRouteBackendName, Namespace: "tenant-foo"}},
			want: 1,
		},
		{
			name: "the same name in another namespace",
			obj:  &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: tlsRouteBackendName, Namespace: "tenant-bar"}},
			want: 0,
		},
		{
			name: "a Service no route names",
			obj:  &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: "tenant-foo"}},
			want: 0,
		},
		{
			name: "the Service a cross-namespace ref names",
			obj:  &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "db-backend", Namespace: "cozy-public"}},
			want: 1,
		},
		{
			name: "a grant in the referenced namespace",
			obj:  &gatewayv1beta1.ReferenceGrant{ObjectMeta: metav1.ObjectMeta{Name: "grant", Namespace: "cozy-public"}},
			want: 1,
		},
		{
			name: "a grant in a namespace nothing references",
			obj:  &gatewayv1beta1.ReferenceGrant{ObjectMeta: metav1.ObjectMeta{Name: "grant", Namespace: "cozy-elsewhere"}},
			want: 0,
		},
		{
			// The route's own namespace never needs a grant, so a grant
			// there says nothing about whether the route forwards.
			name: "a grant in the route's own namespace",
			obj:  &gatewayv1beta1.ReferenceGrant{ObjectMeta: metav1.ObjectMeta{Name: "grant", Namespace: "tenant-foo"}},
			want: 0,
		},
		{
			name: "an object of a kind this mapper does not handle",
			obj:  &gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"}},
			want: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newScheme(t)
			c := fake.NewClientBuilder().WithScheme(s).WithObjects(tgw, local, remote).Build()
			r := &Reconciler{Client: c, Scheme: s}
			if reqs := r.mapBackendToTenantGateways(context.TODO(), tc.obj); len(reqs) != tc.want {
				t.Errorf("requests = %+v, want %d", reqs, tc.want)
			}
		})
	}
}

// TestMapBackendToTenantGateways_OneRequestPerGateway pins the
// deduplication: two routes on one Gateway naming the same Service
// would otherwise enqueue it twice for a single event.
func TestMapBackendToTenantGateways_OneRequestPerGateway(t *testing.T) {
	s := newScheme(t)
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
		Spec: gatewayv1alpha1.TenantGatewaySpec{
			Apex:             "foo.example.com",
			CertMode:         gatewayv1alpha1.CertModeHTTP01,
			GatewayClassName: "cilium",
		},
	}
	first := tlsRouteAttached("api-tls", "tenant-foo", "api.foo.example.com", "tls-api", "tenant-foo")
	second := tlsRouteAttached("db-tls", "tenant-foo", "db.foo.example.com", "tls-db", "tenant-foo")

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(tgw, first, second).Build()
	r := &Reconciler{Client: c, Scheme: s}
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: tlsRouteBackendName, Namespace: "tenant-foo"}}
	if reqs := r.mapBackendToTenantGateways(context.TODO(), svc); len(reqs) != 1 {
		t.Errorf("requests = %+v, want exactly 1", reqs)
	}
}
