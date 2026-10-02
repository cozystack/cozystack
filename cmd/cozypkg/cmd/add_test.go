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

package cmd

import (
	"context"
	"testing"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/marketplace/collision"
	"github.com/cozystack/cozystack/internal/marketplace/tapconst"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestPrivilegedComponents(t *testing.T) {
	ps := &cozyv1alpha1.PackageSource{
		Spec: cozyv1alpha1.PackageSourceSpec{
			Variants: []cozyv1alpha1.Variant{
				{
					Name: "default",
					Components: []cozyv1alpha1.Component{
						{Name: "app", Path: "apps/app"},
						{Name: "risky", Path: "apps/risky", Install: &cozyv1alpha1.ComponentInstall{Privileged: true}},
					},
				},
				{
					Name:       "safe",
					Components: []cozyv1alpha1.Component{{Name: "app", Path: "apps/app"}},
				},
			},
		},
	}

	if got := collision.PrivilegedInstallComponents(ps, "default"); len(got) != 1 || got[0] != "risky" {
		t.Errorf("default variant privileged = %v, want [risky]", got)
	}
	if got := collision.PrivilegedInstallComponents(ps, "safe"); len(got) != 0 {
		t.Errorf("safe variant should have no privileged components, got %v", got)
	}
	if got := collision.PrivilegedInstallComponents(ps, "nonexistent"); len(got) != 0 {
		t.Errorf("unknown variant should return none, got %v", got)
	}
}

func TestPinRegistrationToUser(t *testing.T) {
	pkg := &cozyv1alpha1.Package{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      map[string]string{tapconst.Label: "true", "keep": "me"},
			Annotations: map[string]string{tapconst.SourceAnnotation: "tap-a", "keep": "me"},
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "PackageSource", Name: "acme.app"},
				{Kind: "SomethingElse", Name: "other"},
			},
		},
	}
	pinRegistrationToUser(pkg, "full")
	if pkg.Spec.Variant != "full" {
		t.Errorf("variant not pinned, got %q", pkg.Spec.Variant)
	}
	if pkg.GetLabels()[tapconst.Label] == "true" {
		t.Error("a pinned Package must no longer carry the tap label")
	}
	if _, ok := pkg.Labels[tapconst.Label]; ok {
		t.Error("tap label must be shed")
	}
	if _, ok := pkg.Annotations[tapconst.SourceAnnotation]; ok {
		t.Error("source annotation must be shed")
	}
	if pkg.Labels["keep"] != "me" || pkg.Annotations["keep"] != "me" {
		t.Error("unrelated labels/annotations must be preserved")
	}
	if len(pkg.OwnerReferences) != 1 || pkg.OwnerReferences[0].Kind != "SomethingElse" {
		t.Errorf("only the PackageSource ownerRef must be dropped, got %+v", pkg.OwnerReferences)
	}
}

func TestHandoverManagedRegistration(t *testing.T) {
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(cozyv1alpha1.AddToScheme(s))
	pkg := &cozyv1alpha1.Package{ObjectMeta: metav1.ObjectMeta{
		Name:            "acme.app",
		Labels:          map[string]string{tapconst.Label: "true"},
		Annotations:     map[string]string{tapconst.SourceAnnotation: "tap-acme-app"},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "cozystack.io/v1alpha1", Kind: "PackageSource", Name: "acme.app"}},
	}}
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(pkg).Build()
	if err := handoverManagedRegistration(context.Background(), cl, pkg, "full"); err != nil {
		t.Fatalf("handover: %v", err)
	}
	var got cozyv1alpha1.Package
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "acme.app"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Variant != "full" {
		t.Errorf("variant not pinned, got %q", got.Spec.Variant)
	}
	if got.Labels[tapconst.Label] == "true" {
		t.Error("tap label must be shed on handover")
	}
	if _, ok := got.Annotations[tapconst.SourceAnnotation]; ok {
		t.Error("source annotation must be shed on handover")
	}
	for _, ref := range got.OwnerReferences {
		if ref.Kind == "PackageSource" {
			t.Error("PackageSource ownerRef must be shed on handover")
		}
	}
}
