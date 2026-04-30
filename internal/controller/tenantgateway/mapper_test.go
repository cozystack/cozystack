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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

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
