// SPDX-License-Identifier: Apache-2.0
// Copyright 2025 The Cozystack Authors.

package operator

import (
	"context"
	"testing"

	sourcewatcherv1beta1 "github.com/fluxcd/source-watcher/api/v2/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/marketplace/naming"
	"github.com/cozystack/cozystack/internal/marketplace/tapconst"
)

// tapPrivilegedPS builds a PackageSource whose default variant has one
// privileged component ("op") and one benign component ("web"). When tap is
// true the source carries the marketplace-tap label, marking it a tap-managed
// source whose privileged content is withheld until the registration is
// confirmed.
func tapPrivilegedPS(tap bool) *cozyv1alpha1.PackageSource {
	ps := &cozyv1alpha1.PackageSource{
		ObjectMeta: metav1.ObjectMeta{Name: "demo.app"},
		Spec: cozyv1alpha1.PackageSourceSpec{
			SourceRef: &cozyv1alpha1.PackageSourceRef{Kind: "OCIRepository", Name: "tap-demo-app", Namespace: "cozy-system"},
			Variants: []cozyv1alpha1.Variant{{
				Name: "default",
				Components: []cozyv1alpha1.Component{
					{Name: "op", Path: "sys/op", Install: &cozyv1alpha1.ComponentInstall{Namespace: "cozy-op", Privileged: true}},
					{Name: "web", Path: "apps/web", Install: &cozyv1alpha1.ComponentInstall{Namespace: "cozy-web"}},
				},
			}},
		},
	}
	if tap {
		ps.Labels = map[string]string{tapconst.Label: "true"}
	}
	return ps
}

func agArtifactNames(t *testing.T, cl client.Client) map[string]bool {
	t.Helper()
	var ag sourcewatcherv1beta1.ArtifactGenerator
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "demo.app", Namespace: "cozy-system"}, &ag); err != nil {
		t.Fatalf("get ArtifactGenerator: %v", err)
	}
	names := map[string]bool{}
	for _, oa := range ag.Spec.OutputArtifacts {
		names[oa.Name] = true
	}
	return names
}

// The ExternalArtifact is what the helm-controller upgrades a HelmRelease from,
// independently of the Package reconciler. Withholding the privileged
// component's artifact until the registration is confirmed narrows the window in
// which unconfirmed privileged content could reach the cluster. The benign
// component's artifact is always produced (it carries the catalog registration),
// and a non-tap (platform) source is never gated.
func TestReconcileArtifactGeneratorsWithholdsUnconfirmedPrivileged(t *testing.T) {
	opName := naming.ArtifactName("demo.app", "default", "op")
	webName := naming.ArtifactName("demo.app", "default", "web")

	cases := []struct {
		name      string
		tapSource bool
		pkg       *cozyv1alpha1.Package
		wantOp    bool
	}{
		{"tap source, no registration Package", true, nil, false},
		{"tap source, unconfirmed registration", true, &cozyv1alpha1.Package{ObjectMeta: metav1.ObjectMeta{Name: "demo.app", Labels: map[string]string{tapconst.Label: "true"}}}, false},
		{"tap source, confirmed registration", true, &cozyv1alpha1.Package{ObjectMeta: metav1.ObjectMeta{Name: "demo.app"}}, true},
		{"non-tap platform source", false, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			objs := []client.Object{tapPrivilegedPS(tc.tapSource)}
			if tc.pkg != nil {
				objs = append(objs, tc.pkg)
			}
			cl := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).Build()
			r := &PackageSourceReconciler{Client: cl, Scheme: testScheme(t)}
			if err := r.reconcileArtifactGenerators(context.Background(), tapPrivilegedPS(tc.tapSource)); err != nil {
				t.Fatalf("reconcileArtifactGenerators: %v", err)
			}
			names := agArtifactNames(t, cl)
			if !names[webName] {
				t.Errorf("benign component artifact %q must always be produced", webName)
			}
			if names[opName] != tc.wantOp {
				t.Errorf("privileged component artifact %q: produced=%v, want %v", opName, names[opName], tc.wantOp)
			}
		})
	}
}
