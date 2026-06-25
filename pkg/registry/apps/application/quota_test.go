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

package application

import (
	"context"
	"encoding/json"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
)

const testTenantPrefix = "tenant-"

// tenantValuesJSON renders a tenant values blob carrying the given declared
// resourceQuotas. A nil/empty map yields a blob with no quota (unbounded).
func tenantValuesJSON(t *testing.T, quotas map[string]string) *apiextv1.JSON {
	t.Helper()
	values := map[string]any{}
	if len(quotas) > 0 {
		values["resourceQuotas"] = quotas
	}
	raw, err := json.Marshal(values)
	if err != nil {
		t.Fatalf("marshal tenant values: %v", err)
	}
	return &apiextv1.JSON{Raw: raw}
}

// tenantHelmRelease builds a tenant HelmRelease (as stored by the aggregated
// apiserver) named "<prefix><name>" in namespace, with the given declared
// quotas.
func tenantHelmRelease(t *testing.T, name, namespace string, quotas map[string]string) *helmv2.HelmRelease {
	t.Helper()
	return &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testTenantPrefix + name,
			Namespace: namespace,
			Labels: map[string]string{
				ApplicationKindLabel:  "Tenant",
				ApplicationGroupLabel: appsv1alpha1.GroupName,
				ApplicationNameLabel:  name,
			},
		},
		Spec: helmv2.HelmReleaseSpec{
			Values: tenantValuesJSON(t, quotas),
		},
	}
}

// newTenantREST wires a tenant REST handler over a fake client seeded with the
// given HelmReleases.
func newTenantREST(t *testing.T, objects ...client.Object) *REST {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := helmv2.AddToScheme(scheme); err != nil {
		t.Fatalf("register helmv2 scheme: %v", err)
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	return &REST{
		c: fakeClient,
		gvk: schema.GroupVersionKind{
			Group:   appsv1alpha1.GroupName,
			Version: "v1alpha1",
			Kind:    "Tenant",
		},
		gvr: schema.GroupVersionResource{
			Group:    appsv1alpha1.GroupName,
			Version:  "v1alpha1",
			Resource: "tenants",
		},
		kindName: "Tenant",
		releaseConfig: config.ReleaseConfig{
			Prefix: testTenantPrefix,
		},
	}
}

// childApplication builds a Tenant Application named name in namespace with the
// given declared quotas.
func childApplication(t *testing.T, name, namespace string, quotas map[string]string) *appsv1alpha1.Application {
	t.Helper()
	return &appsv1alpha1.Application{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps.cozystack.io/v1alpha1",
			Kind:       "Tenant",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: tenantValuesJSON(t, quotas),
	}
}

func TestValidateTenantResourceQuotas(t *testing.T) {
	tests := []struct {
		name string
		// parent owns the namespace the child is created in; its HelmRelease
		// lives one level up.
		parent *helmv2.HelmRelease
		// siblings already exist in the child's namespace.
		siblings []*helmv2.HelmRelease
		// child being created/updated.
		childName   string
		childNS     string
		childQuotas map[string]string
		wantDenied  bool
	}{
		{
			name:        "child within parent budget is allowed",
			parent:      tenantHelmRelease(t, "foo", "tenant-root", map[string]string{"cpu": "10"}),
			childName:   "bar",
			childNS:     "tenant-foo",
			childQuotas: map[string]string{"cpu": "4"},
			wantDenied:  false,
		},
		{
			name:        "child exactly at remaining is allowed",
			parent:      tenantHelmRelease(t, "foo", "tenant-root", map[string]string{"cpu": "10"}),
			siblings:    []*helmv2.HelmRelease{tenantHelmRelease(t, "bar", "tenant-foo", map[string]string{"cpu": "4"})},
			childName:   "baz",
			childNS:     "tenant-foo",
			childQuotas: map[string]string{"cpu": "6"},
			wantDenied:  false,
		},
		{
			name:        "child exceeding remaining after sibling carve-out is denied",
			parent:      tenantHelmRelease(t, "foo", "tenant-root", map[string]string{"cpu": "10"}),
			siblings:    []*helmv2.HelmRelease{tenantHelmRelease(t, "bar", "tenant-foo", map[string]string{"cpu": "4"})},
			childName:   "baz",
			childNS:     "tenant-foo",
			childQuotas: map[string]string{"cpu": "7"},
			wantDenied:  true,
		},
		{
			name:        "child exceeding parent total is denied",
			parent:      tenantHelmRelease(t, "foo", "tenant-root", map[string]string{"cpu": "10"}),
			childName:   "bar",
			childNS:     "tenant-foo",
			childQuotas: map[string]string{"cpu": "11"},
			wantDenied:  true,
		},
		{
			name:        "child without quota shares parent pool (allowed)",
			parent:      tenantHelmRelease(t, "foo", "tenant-root", map[string]string{"cpu": "10"}),
			childName:   "bar",
			childNS:     "tenant-foo",
			childQuotas: nil,
			wantDenied:  false,
		},
		{
			name:        "unbounded parent does not constrain child",
			parent:      tenantHelmRelease(t, "foo", "tenant-root", nil),
			childName:   "bar",
			childNS:     "tenant-foo",
			childQuotas: map[string]string{"cpu": "1000"},
			wantDenied:  false,
		},
		{
			name:        "missing parent HelmRelease does not constrain child",
			parent:      nil,
			childName:   "bar",
			childNS:     "tenant-foo",
			childQuotas: map[string]string{"cpu": "1000"},
			wantDenied:  false,
		},
		{
			name:        "per-resource enforcement denies only the over-budget resource",
			parent:      tenantHelmRelease(t, "foo", "tenant-root", map[string]string{"cpu": "10", "memory": "20Gi"}),
			childName:   "bar",
			childNS:     "tenant-foo",
			childQuotas: map[string]string{"cpu": "5", "memory": "25Gi"},
			wantDenied:  true,
		},
		{
			name:        "child bounding a resource the parent does not bound is allowed",
			parent:      tenantHelmRelease(t, "foo", "tenant-root", map[string]string{"cpu": "10"}),
			childName:   "bar",
			childNS:     "tenant-foo",
			childQuotas: map[string]string{"memory": "999Gi"},
			wantDenied:  false,
		},
		{
			name:        "deep nesting: grandchild within child budget allowed",
			parent:      tenantHelmRelease(t, "bar", "tenant-foo", map[string]string{"cpu": "6"}),
			childName:   "baz",
			childNS:     "tenant-foo-bar",
			childQuotas: map[string]string{"cpu": "5"},
			wantDenied:  false,
		},
		{
			name:        "deep nesting: grandchild exceeding child budget denied",
			parent:      tenantHelmRelease(t, "bar", "tenant-foo", map[string]string{"cpu": "6"}),
			childName:   "baz",
			childNS:     "tenant-foo-bar",
			childQuotas: map[string]string{"cpu": "7"},
			wantDenied:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var objects []client.Object
			if tc.parent != nil {
				objects = append(objects, tc.parent)
			}
			for _, s := range tc.siblings {
				objects = append(objects, s)
			}
			r := newTenantREST(t, objects...)

			app := childApplication(t, tc.childName, tc.childNS, tc.childQuotas)
			errs := r.validateTenantResourceQuotas(context.Background(), app)
			gotDenied := len(errs) > 0
			if gotDenied != tc.wantDenied {
				t.Fatalf("validateTenantResourceQuotas denied=%v, want %v (errors: %v)", gotDenied, tc.wantDenied, errs)
			}
		})
	}
}

// TestValidateTenantResourceQuotas_UpdateExcludesSelf verifies that raising a
// tenant's own quota does not double-count its existing carve-out as a sibling.
func TestValidateTenantResourceQuotas_UpdateExcludesSelf(t *testing.T) {
	parent := tenantHelmRelease(t, "foo", "tenant-root", map[string]string{"cpu": "10"})
	// "bar" already exists in tenant-foo with cpu=4. Raising it to cpu=9 must
	// be allowed (only sibling carve-outs other than bar count, and there are
	// none), not rejected as 4+9 > 10.
	self := tenantHelmRelease(t, "bar", "tenant-foo", map[string]string{"cpu": "4"})
	r := newTenantREST(t, parent, self)

	app := childApplication(t, "bar", "tenant-foo", map[string]string{"cpu": "9"})
	if errs := r.validateTenantResourceQuotas(context.Background(), app); len(errs) > 0 {
		t.Fatalf("update of own quota within parent budget should be allowed, got: %v", errs)
	}

	// Raising bar above the full parent budget is still rejected.
	appOver := childApplication(t, "bar", "tenant-foo", map[string]string{"cpu": "11"})
	if errs := r.validateTenantResourceQuotas(context.Background(), appOver); len(errs) == 0 {
		t.Fatalf("update of own quota above parent budget should be denied")
	}
}

func TestParentTenantHelmReleaseRef(t *testing.T) {
	tests := []struct {
		owned    string
		wantName string
		wantNS   string
		wantOK   bool
	}{
		{"tenant-root", "tenant-root", "tenant-root", true},
		{"tenant-foo", "tenant-foo", "tenant-root", true},
		{"tenant-foo-bar", "tenant-bar", "tenant-foo", true},
		{"tenant-foo-bar-baz", "tenant-baz", "tenant-foo-bar", true},
		{"not-a-tenant", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.owned, func(t *testing.T) {
			name, ns, ok := parentTenantHelmReleaseRef(tc.owned, testTenantPrefix)
			if name != tc.wantName || ns != tc.wantNS || ok != tc.wantOK {
				t.Fatalf("parentTenantHelmReleaseRef(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tc.owned, name, ns, ok, tc.wantName, tc.wantNS, tc.wantOK)
			}
		})
	}
}
