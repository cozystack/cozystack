// SPDX-License-Identifier: Apache-2.0

package tenantmodule

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
	"github.com/cozystack/cozystack/pkg/registry/sorting"
)

func TestTenantModuleListSortsAlphabetically(t *testing.T) {
	items := []corev1alpha1.TenantModule{
		{ObjectMeta: metav1.ObjectMeta{Namespace: "ns-b", Name: "zebra"}},
		{ObjectMeta: metav1.ObjectMeta{Namespace: "ns-a", Name: "alpha"}},
		{ObjectMeta: metav1.ObjectMeta{Namespace: "ns-b", Name: "alpha"}},
		{ObjectMeta: metav1.ObjectMeta{Namespace: "ns-a", Name: "bravo"}},
	}

	sorting.ByNamespacedName[corev1alpha1.TenantModule, *corev1alpha1.TenantModule](items)

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
