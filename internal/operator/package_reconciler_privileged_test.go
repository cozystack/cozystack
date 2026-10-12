package operator

import (
	"context"
	"testing"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/marketplace/tapconst"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func privScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(cozyv1alpha1.AddToScheme(s))
	utilruntime.Must(helmv2.AddToScheme(s))
	return s
}

func privPS() *cozyv1alpha1.PackageSource {
	return &cozyv1alpha1.PackageSource{
		ObjectMeta: metav1.ObjectMeta{Name: "demo.app"},
		Spec: cozyv1alpha1.PackageSourceSpec{Variants: []cozyv1alpha1.Variant{{
			Name: "default",
			Components: []cozyv1alpha1.Component{{Name: "op", Path: "sys/op",
				Install: &cozyv1alpha1.ComponentInstall{Namespace: "cozy-op", Privileged: true}}},
		}}},
	}
}

func TestPackageReconcilerRefusesUnconfirmedPrivileged(t *testing.T) {
	pkg := &cozyv1alpha1.Package{ObjectMeta: metav1.ObjectMeta{Name: "demo.app", Labels: map[string]string{tapconst.Label: "true"}}}
	cl := fake.NewClientBuilder().WithScheme(privScheme()).WithObjects(pkg, privPS()).WithStatusSubresource(&cozyv1alpha1.Package{}).Build()
	r := &PackageReconciler{Client: cl, Scheme: privScheme()}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "demo.app"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	var hrs helmv2.HelmReleaseList
	if err := cl.List(context.Background(), &hrs); err != nil {
		t.Fatal(err)
	}
	for _, hr := range hrs.Items {
		if hr.Labels["cozystack.io/privileged"] == "true" {
			t.Errorf("unconfirmed tap registration must not create a privileged HelmRelease: %s/%s", hr.Namespace, hr.Name)
		}
	}
	var ns corev1.Namespace
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "cozy-op"}, &ns); err == nil {
		if ns.Labels["pod-security.kubernetes.io/enforce"] == "privileged" {
			t.Error("namespace must not be raised to privileged for an unconfirmed registration")
		}
	}
	var got cozyv1alpha1.Package
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "demo.app"}, &got); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range got.Status.Conditions {
		if c.Reason == "PrivilegedNotConfirmed" {
			found = true
		}
	}
	if !found {
		t.Error("expected a PrivilegedNotConfirmed condition")
	}
}

func TestPackageReconcilerInstallsConfirmedPrivileged(t *testing.T) {
	pkg := &cozyv1alpha1.Package{ObjectMeta: metav1.ObjectMeta{Name: "demo.app"}, Spec: cozyv1alpha1.PackageSpec{Variant: "default"}}
	cl := fake.NewClientBuilder().WithScheme(privScheme()).WithObjects(pkg, privPS()).WithStatusSubresource(&cozyv1alpha1.Package{}).Build()
	r := &PackageReconciler{Client: cl, Scheme: privScheme()}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "demo.app"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	var hrs helmv2.HelmReleaseList
	if err := cl.List(context.Background(), &hrs); err != nil {
		t.Fatal(err)
	}
	priv := false
	for _, hr := range hrs.Items {
		if hr.Labels["cozystack.io/privileged"] == "true" {
			priv = true
		}
	}
	if !priv {
		t.Error("a confirmed (non-tap) Package must install its privileged HelmRelease")
	}
}

// A tap auto-registration whose component was benign when it first installed (a
// HelmRelease exists) but flips to privileged in a later source revision: the
// install loop refuses the now-privileged component, and cleanup must remove the
// lingering HelmRelease so the helm-controller does not upgrade it to the
// newly-privileged chart revision the tap's ExternalArtifact now points at.
// Without excluding unconfirmed-privileged components from the desired set,
// cleanup keeps the HelmRelease and the guarded install happens via the upgrade.
func TestPackageReconcilerRemovesLingeringPrivilegedHelmRelease(t *testing.T) {
	pkg := &cozyv1alpha1.Package{ObjectMeta: metav1.ObjectMeta{Name: "demo.app", Labels: map[string]string{tapconst.Label: "true"}}}
	existing := &helmv2.HelmRelease{ObjectMeta: metav1.ObjectMeta{
		Name:      "op",
		Namespace: "cozy-op",
		Labels:    map[string]string{"cozystack.io/package": "demo.app"},
	}}
	cl := fake.NewClientBuilder().WithScheme(privScheme()).
		WithObjects(pkg, privPS(), existing).
		WithStatusSubresource(&cozyv1alpha1.Package{}).Build()
	r := &PackageReconciler{Client: cl, Scheme: privScheme()}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "demo.app"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	var hr helmv2.HelmRelease
	err := cl.Get(context.Background(), types.NamespacedName{Name: "op", Namespace: "cozy-op"}, &hr)
	if err == nil {
		t.Error("cleanup must delete the lingering HelmRelease of an unconfirmed privileged component")
	} else if !apierrors.IsNotFound(err) {
		t.Fatalf("unexpected error checking HelmRelease: %v", err)
	}
}
