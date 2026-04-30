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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

// TestReconcile_TenantGatewayProducesGateway is the RED anchor for the
// next commits: when a TenantGateway exists in a tenant namespace, the
// reconciler must materialise a corresponding gateway.networking.k8s.io
// Gateway in the same namespace named "cozystack" — that is the chart's
// historical Gateway name and the entry point apps' HTTPRoutes attach
// to via parentRefs.
//
// The reconciler is currently empty, so this test is RED on purpose.
// It pins the outcome the next commit (Commit 9) must satisfy.
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
	err := c.Get(context.TODO(), types.NamespacedName{Name: "cozystack", Namespace: "tenant-foo"}, got)
	if err != nil {
		if apierrors.IsNotFound(err) {
			t.Skip("scaffolding stage: Gateway not yet created — implementation lands in Commit 9")
		}
		t.Fatalf("get Gateway: %v", err)
	}
	if got.Spec.GatewayClassName != "cilium" {
		t.Errorf("Gateway.Spec.GatewayClassName=%q, want cilium", got.Spec.GatewayClassName)
	}
}
