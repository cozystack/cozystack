// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Cozystack Authors.

package siterouter

import (
	"context"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
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
	if res.Requeue || res.RequeueAfter != 0 {
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
