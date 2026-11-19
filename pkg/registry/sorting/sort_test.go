// SPDX-License-Identifier: Apache-2.0

package sorting

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type testClusterScoped struct {
	metav1.ObjectMeta
}

type testNamespaceScoped struct {
	metav1.ObjectMeta
}

func TestByName(t *testing.T) {
	items := []testClusterScoped{
		{ObjectMeta: metav1.ObjectMeta{Name: "zebra"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "alpha"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "mike"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "bravo"}},
	}

	ByName[testClusterScoped, *testClusterScoped](items)

	expected := []string{"alpha", "bravo", "mike", "zebra"}

	for i, name := range expected {
		if items[i].Name != name {
			t.Errorf("item %d: expected %q, got %q", i, name, items[i].Name)
		}
	}
}

func TestByNamespacedName(t *testing.T) {
	items := []testNamespaceScoped{
		{ObjectMeta: metav1.ObjectMeta{Namespace: "ns-b", Name: "zebra"}},
		{ObjectMeta: metav1.ObjectMeta{Namespace: "ns-a", Name: "alpha"}},
		{ObjectMeta: metav1.ObjectMeta{Namespace: "ns-b", Name: "alpha"}},
		{ObjectMeta: metav1.ObjectMeta{Namespace: "ns-a", Name: "bravo"}},
	}

	ByNamespacedName[testNamespaceScoped, *testNamespaceScoped](items)

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
