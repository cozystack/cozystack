// SPDX-License-Identifier: Apache-2.0

package option

import (
	"context"
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

func TestAppKindProviderUsesApplicationDefinitionCRDGroup(t *testing.T) {
	// ApplicationDefinition is served from the cozystack.io group (Cluster-scoped) —
	// see packages/system/application-definition-crd. Querying any other group makes
	// the appkind dropdown silently empty in every cluster. The fake dynamic client
	// in the other appkind test cannot catch a wrong group: it is built from
	// gvrAppDefs itself, so it stays self-consistent with whatever group is set.
	if gvrAppDefs.Group != "cozystack.io" || gvrAppDefs.Resource != "applicationdefinitions" {
		t.Fatalf("appkind must list cozystack.io/applicationdefinitions, got %s/%s",
			gvrAppDefs.Group, gvrAppDefs.Resource)
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

// A failing provider must not take down the whole list: List logs and skips it
// (so a partial RBAC config silently drops one dropdown rather than breaking the
// rest), while Get on that source surfaces the error as a 500.
func okProvider(items ...string) providerFunc {
	return func(context.Context, string) ([]corev1alpha1.OptionItem, error) {
		out := make([]corev1alpha1.OptionItem, 0, len(items))
		for _, v := range items {
			out = append(out, corev1alpha1.OptionItem{Value: v})
		}
		return out, nil
	}
}

func failingProvider() providerFunc {
	return func(context.Context, string) ([]corev1alpha1.OptionItem, error) {
		return nil, errors.New("forbidden: provider has no RBAC")
	}
}

func TestRESTListToleratesFailingProvider(t *testing.T) {
	r := NewREST(map[string]providerFunc{
		"good": okProvider("a"),
		"bad":  failingProvider(),
	})
	obj, err := r.List(context.Background(), nil)
	if err != nil {
		t.Fatalf("List must not fail when a single provider errors: %v", err)
	}
	have := map[string]bool{}
	for _, it := range obj.(*corev1alpha1.OptionList).Items {
		have[it.Name] = true
	}
	if !have["good"] {
		t.Error("List dropped the healthy source")
	}
	if have["bad"] {
		t.Error("List included the failing source; it must be skipped")
	}
}

func TestRESTGetSurfacesProviderError(t *testing.T) {
	r := NewREST(map[string]providerFunc{"bad": failingProvider()})
	_, err := r.Get(context.Background(), "bad", &metav1.GetOptions{})
	if err == nil {
		t.Fatal("Get must surface the provider error, not swallow it")
	}
	if !apierrors.IsInternalError(err) {
		t.Errorf("Get should return an InternalError (500), got %v", err)
	}
}

// The parsing-heavy providers below feed dropdowns that List drops silently on
// any provider error, so a logic regression ships as an empty/wrong dropdown
// rather than a failure. These exercise that logic against the fake client.

func itemByValue(items []corev1alpha1.OptionItem, value string) (corev1alpha1.OptionItem, bool) {
	for _, it := range items {
		if it.Value == value {
			return it, true
		}
	}
	return corev1alpha1.OptionItem{}, false
}

func TestGPUProviderIntersectsWhitelistWithNodeAllocatable(t *testing.T) {
	kv := newObj(gvrKubevirts.GroupVersion().WithKind("KubeVirt"), kubevirtNamespace, "kubevirt", map[string]interface{}{
		"configuration": map[string]interface{}{
			"permittedHostDevices": map[string]interface{}{
				"pciHostDevices": []interface{}{
					map[string]interface{}{"resourceName": "nvidia.com/GP100"},
					map[string]interface{}{"resourceName": "nvidia.com/A100"},
				},
			},
		},
	})
	node := &unstructured.Unstructured{}
	node.SetGroupVersionKind(gvrNodes.GroupVersion().WithKind("Node"))
	node.SetName("node-a")
	_ = unstructured.SetNestedStringMap(node.Object, map[string]string{"nvidia.com/GP100": "2"}, "status", "allocatable")

	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds(), kv, node)
	items, err := DefaultProviders(dyn)["gpu"](context.Background(), "")
	if err != nil {
		t.Fatalf("gpu provider: %v", err)
	}
	if got := values(items); len(got) != 2 || got[0] != "nvidia.com/A100" || got[1] != "nvidia.com/GP100" {
		t.Fatalf("gpu: got %v, want sorted [A100 GP100]", got)
	}
	if it, _ := itemByValue(items, "nvidia.com/GP100"); it.Description != "2 available on node-a" {
		t.Errorf("GP100 description = %q, want availability count", it.Description)
	}
	if it, _ := itemByValue(items, "nvidia.com/A100"); it.Description != "not currently available on any node" {
		t.Errorf("A100 description = %q, want not-available", it.Description)
	}
}

func TestImageProviderStripsPrefixAndFilters(t *testing.T) {
	pvcGVK := gvrPVCs.GroupVersion().WithKind("PersistentVolumeClaim")
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds(),
		newObj(pvcGVK, publicImagesNamespace, "vm-default-images-ubuntu", nil),
		newObj(pvcGVK, publicImagesNamespace, "vm-default-images-fedora", nil),
		newObj(pvcGVK, publicImagesNamespace, "some-other-pvc", nil),
	)
	items, err := DefaultProviders(dyn)["image"](context.Background(), "")
	if err != nil {
		t.Fatalf("image provider: %v", err)
	}
	if got := values(items); len(got) != 2 || got[0] != "fedora" || got[1] != "ubuntu" {
		t.Fatalf("image: got %v, want [fedora ubuntu] (prefix stripped, non-prefixed dropped)", got)
	}
}

func TestStorageClassProviderMarksDefault(t *testing.T) {
	scGVK := gvrStorageClass.GroupVersion().WithKind("StorageClass")
	def := newObj(scGVK, "", "fast", nil)
	def.SetAnnotations(map[string]string{defaultStorageClassAnnotation: "true"})
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds(),
		def, newObj(scGVK, "", "slow", nil),
	)
	items, err := DefaultProviders(dyn)["storageclass"](context.Background(), "")
	if err != nil {
		t.Fatalf("storageclass provider: %v", err)
	}
	fast, ok := itemByValue(items, "fast")
	if !ok || !fast.Default || fast.Label != "fast (default)" {
		t.Errorf("fast = %+v, want Default=true Label=\"fast (default)\"", fast)
	}
	slow, ok := itemByValue(items, "slow")
	if !ok || slow.Default || slow.Label != "" {
		t.Errorf("slow = %+v, want no default mark", slow)
	}
}

func TestStoragePoolProviderWalksPoolsAndZones(t *testing.T) {
	hr := newObj(gvrHelmReleases.GroupVersion().WithKind("HelmRelease"), "tenant-x", "seaweedfs", map[string]interface{}{
		"values": map[string]interface{}{
			"volume": map[string]interface{}{
				"pools": map[string]interface{}{"default": map[string]interface{}{}, "ssd": map[string]interface{}{}},
				"zones": map[string]interface{}{
					"zoneA": map[string]interface{}{"pools": map[string]interface{}{"archive": map[string]interface{}{}}},
				},
			},
		},
	})
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds(), hr)
	items, err := DefaultProviders(dyn)["storagepool"](context.Background(), "tenant-x")
	if err != nil {
		t.Fatalf("storagepool provider: %v", err)
	}
	if got := values(items); len(got) != 3 || got[0] != "archive" || got[1] != "default" || got[2] != "ssd" {
		t.Fatalf("storagepool: got %v, want [archive default ssd] (pools + zone pools)", got)
	}
	// Namespaced provider opts out without a namespace.
	if empty, _ := DefaultProviders(dyn)["storagepool"](context.Background(), ""); len(empty) != 0 {
		t.Fatalf("storagepool: expected no items without a namespace")
	}
}

func TestVMDiskProviderShowsSizeInLabel(t *testing.T) {
	vdGVK := gvrVMDisks.GroupVersion().WithKind("VMDisk")
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds(),
		newObj(vdGVK, "tenant-x", "data", map[string]interface{}{"storage": "10Gi"}),
		newObj(vdGVK, "tenant-x", "blank", nil),
	)
	items, err := DefaultProviders(dyn)["vmdisk"](context.Background(), "tenant-x")
	if err != nil {
		t.Fatalf("vmdisk provider: %v", err)
	}
	if data, _ := itemByValue(items, "data"); data.Label != "data (10Gi)" {
		t.Errorf("data label = %q, want \"data (10Gi)\"", data.Label)
	}
	if blank, ok := itemByValue(items, "blank"); !ok || blank.Label != "" {
		t.Errorf("blank = %+v, want bare value with no size label", blank)
	}
}
