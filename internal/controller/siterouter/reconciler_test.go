// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Cozystack Authors.

package siterouter

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/cozystack/cozystack/internal/siterouter/denyset"
)

func TestValidateManagementCIDR(t *testing.T) {
	tests := []struct {
		name           string
		managementCIDR string
		allowOpen      bool
		wantErr        bool
	}{
		{
			name:           "empty without allow-open fails closed",
			managementCIDR: "",
			allowOpen:      false,
			wantErr:        true,
		},
		{
			name:           "empty with allow-open is permitted",
			managementCIDR: "",
			allowOpen:      true,
			wantErr:        false,
		},
		{
			name:           "valid CIDR is accepted",
			managementCIDR: "10.244.0.0/16",
			allowOpen:      false,
			wantErr:        false,
		},
		{
			name:           "valid CIDR is accepted regardless of allow-open",
			managementCIDR: "10.244.0.0/16",
			allowOpen:      true,
			wantErr:        false,
		},
		{
			name:           "malformed CIDR is rejected",
			managementCIDR: "10.244.0.0/33",
			allowOpen:      true,
			wantErr:        true,
		},
		{
			name:           "bare IP without mask is rejected",
			managementCIDR: "10.244.0.1",
			allowOpen:      false,
			wantErr:        true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateManagementCIDR(tt.managementCIDR, tt.allowOpen)
			if tt.wantErr && err == nil {
				t.Fatalf("ValidateManagementCIDR(%q, %v) = nil, want error", tt.managementCIDR, tt.allowOpen)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("ValidateManagementCIDR(%q, %v) = %v, want nil", tt.managementCIDR, tt.allowOpen, err)
			}
		})
	}
}

func newTestReconciler(t *testing.T, objs ...client.Object) *SiteRouterReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add client-go scheme: %v", err)
	}
	if err := helmv2.AddToScheme(scheme); err != nil {
		t.Fatalf("add helm-controller scheme: %v", err)
	}
	fc := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		Build()
	return &SiteRouterReconciler{Client: fc, Scheme: scheme, ManagementCIDR: "10.244.0.0/16"}
}

func siteRouterHR(name string) *helmv2.HelmRelease {
	return &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      releasePrefix + name,
			Namespace: "tenant-test",
			Labels: map[string]string{
				appKindLabelKey:  siteRouterKind,
				appGroupLabelKey: appGroup,
				appNameLabelKey:  name,
			},
		},
	}
}

// TestReconcileNoInstance verifies a missing instance is a clean no-op.
func TestReconcileNoInstance(t *testing.T) {
	r := newTestReconciler(t)
	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "tenant-test", Name: "site-router-absent"},
	})
	if err != nil {
		t.Fatalf("Reconcile absent instance: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Fatalf("expected no requeue for absent instance, got %+v", res)
	}
}

// TestReconcileInstanceAddsFinalizer verifies the scaffold reconcile discovers
// the instance and establishes the cleanup finalizer without performing any
// mediation.
func TestReconcileInstanceAddsFinalizer(t *testing.T) {
	hr := siteRouterHR("demo")
	r := newTestReconciler(t, hr)

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: hr.Namespace, Name: hr.Name},
	}); err != nil {
		t.Fatalf("Reconcile instance: %v", err)
	}

	got := &helmv2.HelmRelease{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: hr.Namespace, Name: hr.Name}, got); err != nil {
		t.Fatalf("get instance after reconcile: %v", err)
	}
	found := false
	for _, f := range got.Finalizers {
		if f == finalizer {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected finalizer %q on instance, got %v", finalizer, got.Finalizers)
	}
}

// TestInstanceName covers deriving the bare instance name from the HelmRelease.
func TestInstanceName(t *testing.T) {
	if got := instanceName(siteRouterHR("demo")); got != "demo" {
		t.Fatalf("instanceName from labeled HR = %q, want demo", got)
	}
	unlabeled := &helmv2.HelmRelease{ObjectMeta: metav1.ObjectMeta{Name: "site-router-demo"}}
	if got := instanceName(unlabeled); got != "demo" {
		t.Fatalf("instanceName from prefix strip = %q, want demo", got)
	}
}

// siteRouterHRWithValues builds a SiteRouter HelmRelease whose spec.values decode
// to the given map — the authoritative tenant inputs the controller reads (D7).
func siteRouterHRWithValues(t *testing.T, name string, values map[string]interface{}) *helmv2.HelmRelease {
	t.Helper()
	hr := siteRouterHR(name)
	raw, err := json.Marshal(values)
	if err != nil {
		t.Fatalf("marshal values: %v", err)
	}
	hr.Spec.Values = &apiextensionsv1.JSON{Raw: raw}
	return hr
}

// cozystackConfigMap is the cozy-system/cozystack ConfigMap the controller reads
// the cluster pod/service/join CIDRs from for deny-set validation.
func cozystackConfigMap() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "cozy-system"},
		Data: map[string]string{
			"ipv4-pod-cidr":  "10.244.0.0/16",
			"ipv4-svc-cidr":  "10.96.0.0/16",
			"ipv4-join-cidr": "100.64.0.0/16",
		},
	}
}

// TestReconcile_DenySetRejection encodes the T07 Acceptance "an overlapping
// remoteCIDR is rejected with InvalidRemoteCIDR, route not programmed": a
// remoteCIDR overlapping the cluster pod CIDR must fail validation with a
// machine-readable reason naming the offending CIDR, and the tenant namespace
// must NOT gain a routes annotation.
func TestReconcile_DenySetRejection(t *testing.T) {
	hr := siteRouterHRWithValues(t, "demo", map[string]interface{}{
		"remoteCIDRs": []interface{}{"10.244.7.0/24"}, // overlaps pod 10.244.0.0/16
	})
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-test"}}
	r := newTestReconciler(t, hr, ns, cozystackConfigMap())

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: hr.Namespace, Name: hr.Name},
	})
	if err == nil {
		t.Fatalf("expected reconcile to fail deny-set validation, got nil error")
	}
	if !strings.Contains(err.Error(), denyset.ReasonInvalidRemoteCIDR) {
		t.Errorf("error %q should carry reason %q", err.Error(), denyset.ReasonInvalidRemoteCIDR)
	}
	if !strings.Contains(err.Error(), "10.244.7.0/24") {
		t.Errorf("error %q should name the offending CIDR 10.244.7.0/24", err.Error())
	}

	got := &corev1.Namespace{}
	if err := r.Get(context.Background(), types.NamespacedName{Name: "tenant-test"}, got); err != nil {
		t.Fatalf("get namespace: %v", err)
	}
	if v, programmed := got.Annotations[routesAnnotation]; programmed {
		t.Errorf("a rejected remoteCIDR must not program routes, namespace has %s=%q", routesAnnotation, v)
	}
}

// TestReconcile_FinalizerRestoresStateOnDelete encodes the T07 Acceptance
// "deleting the instance removes the routes annotation + restores port_security":
// on delete the controller must withdraw its own route entry from the namespace
// and restore the gateway pod's port_security before releasing the finalizer.
func TestReconcile_FinalizerRestoresStateOnDelete(t *testing.T) {
	hr := siteRouterHRWithValues(t, "demo", map[string]interface{}{
		"remoteCIDRs": []interface{}{"172.31.0.0/16"},
	})
	hr.Finalizers = []string{finalizer}

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "tenant-test",
			Annotations: map[string]string{routesAnnotation: `[{"dst":"172.31.0.0/16","gw":"10.244.0.5"}]`},
		},
	}
	gateway := gwPod("virt-launcher-site-router-demo-abcde", "demo", "10.244.0.5")
	gateway.Annotations = map[string]string{portSecurityAnnotation: portSecurityRelaxed}

	r := newTestReconciler(t, hr, ns, gateway)

	// Enter the deleting state: the finalizer keeps the HR around for cleanup.
	if err := r.Delete(context.Background(), hr); err != nil {
		t.Fatalf("delete HR: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: hr.Namespace, Name: hr.Name},
	}); err != nil {
		t.Fatalf("reconcile delete: %v", err)
	}

	// port_security restored on the gateway pod (annotation cleared or flipped
	// back to enforcing — anything but the relaxed value).
	gotPod := &corev1.Pod{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "tenant-test", Name: gateway.Name}, gotPod); err != nil {
		t.Fatalf("get gateway pod: %v", err)
	}
	if v, set := gotPod.Annotations[portSecurityAnnotation]; set && v == portSecurityRelaxed {
		t.Errorf("gateway pod port_security must be restored on delete, still %s=%q", portSecurityAnnotation, v)
	}

	// This instance's route entry withdrawn from the namespace annotation.
	gotNS := &corev1.Namespace{}
	if err := r.Get(context.Background(), types.NamespacedName{Name: "tenant-test"}, gotNS); err != nil {
		t.Fatalf("get namespace: %v", err)
	}
	if ann := gotNS.Annotations[routesAnnotation]; strings.Contains(ann, "172.31.0.0/16") {
		t.Errorf("instance route entry must be removed on delete, namespace still has %s=%q", routesAnnotation, ann)
	}
}
