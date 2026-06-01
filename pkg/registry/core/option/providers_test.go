// SPDX-License-Identifier: Apache-2.0

package option

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
)

// listKinds maps the GVRs the providers query to the synthetic List kinds the
// fake dynamic client needs in order to serve List() calls for unstructured
// resources.
func listKinds() map[schema.GroupVersionResource]string {
	return map[schema.GroupVersionResource]string{
		gvrBackupClass:  "BackupClassList",
		gvrPlans:        "PlanList",
		gvrBackups:      "BackupList",
		gvrAppDefs:      "ApplicationDefinitionList",
		gvrNodes:        "NodeList",
		gvrPVCs:         "PersistentVolumeClaimList",
		gvrKubevirts:    "KubeVirtList",
		gvrInstancetype: "VirtualMachineClusterInstancetypeList",
		gvrPreference:   "VirtualMachineClusterPreferenceList",
		gvrNADs:         "NetworkAttachmentDefinitionList",
		gvrHelmReleases: "HelmReleaseList",
		gvrStorageClass: "StorageClassList",
		gvrVMDisks:      "VMDiskList",
	}
}

func newObj(gvk schema.GroupVersionKind, namespace, name string, spec map[string]interface{}) *unstructured.Unstructured {
	o := &unstructured.Unstructured{}
	o.SetGroupVersionKind(gvk)
	o.SetName(name)
	if namespace != "" {
		o.SetNamespace(namespace)
	}
	if spec != nil {
		_ = unstructured.SetNestedMap(o.Object, spec, "spec")
	}
	return o
}

func values(items []corev1alpha1.OptionItem) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Value)
	}
	return out
}

func TestBackupClassProviderIsClusterScoped(t *testing.T) {
	gvk := gvrBackupClass.GroupVersion().WithKind("BackupClass")
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds(),
		newObj(gvk, "", "s3-default", nil),
		newObj(gvk, "", "minio", nil),
	)

	providers := DefaultProviders(dyn)
	items, err := providers["backupclass"](context.Background(), "tenant-foo")
	if err != nil {
		t.Fatalf("backupclass provider: %v", err)
	}
	got := values(items)
	want := []string{"minio", "s3-default"} // sorted
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("backupclass: got %v want %v", got, want)
	}
}

func TestPlanProviderIsNamespaced(t *testing.T) {
	gvk := gvrPlans.GroupVersion().WithKind("Plan")
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds(),
		newObj(gvk, "tenant-a", "daily", nil),
		newObj(gvk, "tenant-a", "weekly", nil),
		newObj(gvk, "tenant-b", "other", nil),
	)
	providers := DefaultProviders(dyn)

	items, err := providers["plan"](context.Background(), "tenant-a")
	if err != nil {
		t.Fatalf("plan provider: %v", err)
	}
	got := values(items)
	if len(got) != 2 || got[0] != "daily" || got[1] != "weekly" {
		t.Fatalf("plan (tenant-a): got %v", got)
	}

	// Empty namespace yields no options (namespaced provider opts out).
	empty, err := providers["plan"](context.Background(), "")
	if err != nil {
		t.Fatalf("plan provider empty ns: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("plan (no ns): expected no items, got %v", values(empty))
	}
}

func TestBackupProviderIsNamespaced(t *testing.T) {
	gvk := gvrBackups.GroupVersion().WithKind("Backup")
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds(),
		newObj(gvk, "tenant-a", "backup-2", nil),
		newObj(gvk, "tenant-a", "backup-1", nil),
	)
	providers := DefaultProviders(dyn)
	items, err := providers["backup"](context.Background(), "tenant-a")
	if err != nil {
		t.Fatalf("backup provider: %v", err)
	}
	got := values(items)
	if len(got) != 2 || got[0] != "backup-1" || got[1] != "backup-2" {
		t.Fatalf("backup: got %v", got)
	}
}

func TestAppKindProviderDedupesAndSorts(t *testing.T) {
	gvk := gvrAppDefs.GroupVersion().WithKind("ApplicationDefinition")
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds(),
		newObj(gvk, "", "postgres", map[string]interface{}{"application": map[string]interface{}{"kind": "Postgres"}}),
		newObj(gvk, "", "redis", map[string]interface{}{"application": map[string]interface{}{"kind": "Redis"}}),
		newObj(gvk, "", "postgres-dup", map[string]interface{}{"application": map[string]interface{}{"kind": "Postgres"}}),
		newObj(gvk, "", "broken", map[string]interface{}{"application": map[string]interface{}{}}),
	)
	providers := DefaultProviders(dyn)
	items, err := providers["appkind"](context.Background(), "")
	if err != nil {
		t.Fatalf("appkind provider: %v", err)
	}
	got := values(items)
	if len(got) != 2 || got[0] != "Postgres" || got[1] != "Redis" {
		t.Fatalf("appkind: got %v", got)
	}
}

func TestRESTListExposesNewSources(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds())
	r := NewREST(DefaultProviders(dyn))

	obj, err := r.List(context.Background(), nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	list, ok := obj.(*corev1alpha1.OptionList)
	if !ok {
		t.Fatalf("List returned %T", obj)
	}
	have := map[string]bool{}
	for i := range list.Items {
		have[list.Items[i].Name] = true
	}
	for _, src := range []string{"backupclass", "plan", "backup", "appkind"} {
		if !have[src] {
			t.Errorf("List missing source %q", src)
		}
	}
}

func TestRESTGetUnknownSourceNotFound(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds())
	r := NewREST(DefaultProviders(dyn))
	if _, err := r.Get(context.Background(), "does-not-exist", &metav1.GetOptions{}); err == nil {
		t.Fatal("expected NotFound for unknown source")
	}
}
