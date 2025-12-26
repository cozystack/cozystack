// SPDX-License-Identifier: Apache-2.0

package tenantnamespace

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMakeListSortsAlphabetically(t *testing.T) {
	r := &REST{}

	// Create namespaces in non-alphabetical order
	src := &corev1.NamespaceList{
		Items: []corev1.Namespace{
			{ObjectMeta: metav1.ObjectMeta{Name: "tenant-zebra"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "tenant-alpha"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "tenant-mike"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "tenant-bravo"}},
		},
	}

	allowed := []string{"tenant-zebra", "tenant-alpha", "tenant-mike", "tenant-bravo"}

	result := r.makeList(src, allowed)

	expected := []string{"tenant-alpha", "tenant-bravo", "tenant-mike", "tenant-zebra"}

	if len(result.Items) != len(expected) {
		t.Fatalf("expected %d items, got %d", len(expected), len(result.Items))
	}

	for i, name := range expected {
		if result.Items[i].Name != name {
			t.Errorf("item %d: expected %q, got %q", i, name, result.Items[i].Name)
		}
	}
}
