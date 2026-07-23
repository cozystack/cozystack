package main

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

var (
	plansGVR      = schema.GroupVersionResource{Group: "forklift.konveyor.io", Version: "v1beta1", Resource: "plans"}
	migrationsGVR = schema.GroupVersionResource{Group: "forklift.konveyor.io", Version: "v1beta1", Resource: "migrations"}
)

func newForkliftObj(kind, namespace, name, uid string, ann map[string]string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]interface{}{}}
	u.SetAPIVersion("forklift.konveyor.io/v1beta1")
	u.SetKind(kind)
	u.SetNamespace(namespace)
	u.SetName(name)
	if uid != "" {
		u.SetUID(types.UID(uid))
	}
	if ann != nil {
		u.SetAnnotations(ann)
	}
	return u
}

func newMigration(namespace, name string, succeeded bool) *unstructured.Unstructured {
	u := newForkliftObj("Migration", namespace, name, "", nil)
	status := "False"
	if succeeded {
		status = "True"
	}
	_ = unstructured.SetNestedSlice(u.Object, []interface{}{
		map[string]interface{}{"type": "Succeeded", "status": status},
	}, "status", "conditions")
	return u
}

func fakeClient(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			plansGVR:      "PlanList",
			migrationsGVR: "MigrationList",
		},
		objs...,
	)
}

// getTargetNamespace must apply the same cross-tenant guard end-to-end via the
// Plan annotation as the pure resolveTargetNamespace: a tenant-scoped Plan is
// confined to its own namespace regardless of the requested target.
func TestGetTargetNamespace(t *testing.T) {
	const annKey = "vm-import.cozystack.io/target-namespace"
	cases := []struct {
		name   string
		planNs string
		ann    map[string]string
		want   string
	}{
		{"tenant plan confined despite foreign target", "tenant-a", map[string]string{annKey: "tenant-b"}, "tenant-a"},
		{"tenant plan without annotation stays local", "tenant-a", nil, "tenant-a"},
		{"admin plan honors requested tenant", "cozy-forklift", map[string]string{annKey: "tenant-b"}, "tenant-b"},
		{"admin plan without annotation stays local", "cozy-forklift", nil, "cozy-forklift"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &AdoptionController{dynamicClient: fakeClient(newForkliftObj("Plan", tc.planNs, "p", "uid-1", tc.ann))}
			if got := c.getTargetNamespace(context.Background(), tc.planNs, "p"); got != tc.want {
				t.Errorf("getTargetNamespace(%q) = %q, want %q", tc.planNs, got, tc.want)
			}
		})
	}
}

func TestGetTargetNamespaceMissingPlanDefaultsLocal(t *testing.T) {
	c := &AdoptionController{dynamicClient: fakeClient()}
	if got := c.getTargetNamespace(context.Background(), "tenant-a", "missing"); got != "tenant-a" {
		t.Errorf("missing plan: got %q, want tenant-a", got)
	}
}

func TestResolvePlan(t *testing.T) {
	c := &AdoptionController{dynamicClient: fakeClient(
		newForkliftObj("Plan", "tenant-a", "import-1", "uid-aaa", nil),
		newForkliftObj("Plan", "cozy-forklift", "import-2", "uid-bbb", nil),
	)}
	if name, ns, ok := c.resolvePlan(context.Background(), "uid-bbb"); !ok || name != "import-2" || ns != "cozy-forklift" {
		t.Errorf("resolvePlan(uid-bbb) = %q/%q ok=%v, want cozy-forklift/import-2 ok=true", ns, name, ok)
	}
	if _, _, ok := c.resolvePlan(context.Background(), "uid-unknown"); ok {
		t.Errorf("resolvePlan(unknown) ok=true, want false")
	}
}

func TestIsMigrationComplete(t *testing.T) {
	cases := []struct {
		name string
		objs []runtime.Object
		want bool
	}{
		{"succeeded", []runtime.Object{newMigration("tenant-a", "p", true)}, true},
		{"not succeeded", []runtime.Object{newMigration("tenant-a", "p", false)}, false},
		{"missing migration", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &AdoptionController{dynamicClient: fakeClient(tc.objs...)}
			if got := c.isMigrationComplete(context.Background(), "tenant-a", "p"); got != tc.want {
				t.Errorf("isMigrationComplete = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsAdoptionEnabled(t *testing.T) {
	const annKey = "vm-import.cozystack.io/adoption-enabled"
	cases := []struct {
		name string
		ann  map[string]string
		plan bool
		want bool
	}{
		{"explicitly enabled", map[string]string{annKey: "true"}, true, true},
		{"explicitly disabled", map[string]string{annKey: "false"}, true, false},
		{"default when unset", nil, true, true},
		{"missing plan defaults disabled", nil, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var objs []runtime.Object
			if tc.plan {
				objs = append(objs, newForkliftObj("Plan", "tenant-a", "p", "uid-1", tc.ann))
			}
			c := &AdoptionController{
				dynamicClient: fakeClient(objs...),
				planCache:     make(map[string]*PlanCacheEntry),
			}
			if got := c.isAdoptionEnabled(context.Background(), "tenant-a", "p"); got != tc.want {
				t.Errorf("isAdoptionEnabled = %v, want %v", got, tc.want)
			}
		})
	}
}
