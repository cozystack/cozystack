// SPDX-License-Identifier: Apache-2.0

package application

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/registry/sorting"
)

func TestApplicationListSortsAlphabetically(t *testing.T) {
	items := []appsv1alpha1.Application{
		{ObjectMeta: metav1.ObjectMeta{Namespace: "ns-b", Name: "zebra"}},
		{ObjectMeta: metav1.ObjectMeta{Namespace: "ns-a", Name: "alpha"}},
		{ObjectMeta: metav1.ObjectMeta{Namespace: "ns-b", Name: "alpha"}},
		{ObjectMeta: metav1.ObjectMeta{Namespace: "ns-a", Name: "bravo"}},
	}

	sorting.ByNamespacedName[appsv1alpha1.Application, *appsv1alpha1.Application](items)

	expected := []struct{ ns, name string }{
		{"ns-a", "alpha"},
		{"ns-a", "bravo"},
		{"ns-b", "alpha"},
		{"ns-b", "zebra"},
	}

	for i, exp := range expected {
		if items[i].Namespace != exp.ns || items[i].Name != exp.name {
			t.Errorf("item %d: expected %s/%s, got %s/%s", i, exp.ns, exp.name, items[i].Namespace, items[i].Name)
		}
	}
}
