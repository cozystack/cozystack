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

package serviceexposure

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	networkv1alpha1 "github.com/cozystack/cozystack/api/network/v1alpha1"
)

func TestMapServiceToExposures(t *testing.T) {
	s := newScheme(t)
	match := exposure("")                                                    // serviceRef root-ingress in tenant-root
	other := exposure("")                                                    // same, but we mutate name/ref below
	other.Name = "other"
	other.Spec.ServiceRef.Name = "different-svc"
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(match, other).Build()
	r := &Reconciler{Client: c, Scheme: s}

	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "root-ingress", Namespace: "tenant-root"}}
	reqs := r.mapServiceToExposures(context.Background(), svc)
	if len(reqs) != 1 || reqs[0].Name != "root-ingress" {
		t.Fatalf("want 1 request for root-ingress, got %v", reqs)
	}
}

func TestMapClassToExposures_NamedAndDefault(t *testing.T) {
	s := newScheme(t)
	named := exposure("gold")
	named.Name = "named"
	usesDefault := exposure("") // empty ⇒ resolves to default
	usesDefault.Name = "uses-default"
	usesOther := exposure("silver")
	usesOther.Name = "uses-other"
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(named, usesDefault, usesOther).Build()
	r := &Reconciler{Client: c, Scheme: s}

	// A change to the "gold" class (not default) hits only the named one.
	gold := &networkv1alpha1.ExposureClass{ObjectMeta: metav1.ObjectMeta{Name: "gold"}}
	reqs := r.mapClassToExposures(context.Background(), gold)
	if len(reqs) != 1 || reqs[0].Name != "named" {
		t.Fatalf("gold class should map to [named], got %v", reqs)
	}

	// A change to the default class hits every empty-class exposure.
	def := &networkv1alpha1.ExposureClass{ObjectMeta: metav1.ObjectMeta{
		Name:        "default",
		Annotations: map[string]string{networkv1alpha1.IsDefaultExposureClassAnnotation: "true"},
	}}
	reqs = r.mapClassToExposures(context.Background(), def)
	if len(reqs) != 1 || reqs[0].Name != "uses-default" {
		t.Fatalf("default class should map to [uses-default], got %v", reqs)
	}
}
