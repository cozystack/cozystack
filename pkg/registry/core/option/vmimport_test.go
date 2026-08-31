// SPDX-License-Identifier: Apache-2.0

package option

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/endpoints/request"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	migrationv1alpha1 "github.com/cozystack/cozystack/api/migration/v1alpha1"
	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
)

func inventoryConfigMap(namespace, source, payload string) *unstructured.Unstructured {
	o := &unstructured.Unstructured{}
	o.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"})
	o.SetNamespace(namespace)
	o.SetName(migrationv1alpha1.InventoryConfigMapName(source))
	_ = unstructured.SetNestedStringMap(o.Object,
		map[string]string{migrationv1alpha1.InventoryVMsKey: payload}, "data")
	return o
}

func fakeDynWithConfigMaps(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds(), objs...)
}

// The picker has to show a name and write a managed-object reference; getting
// that pairing backwards produces a form that looks right and imports nothing.
func TestImportVMProviderShowsNamesAndWritesRefs(t *testing.T) {
	dyn := fakeDynWithConfigMaps(inventoryConfigMap("tenant-a", "vcenter",
		`[{"id":"vm-52","name":"web-01","path":"/dc/vm/web-01"},{"id":"vm-7","name":"db-01"}]`))

	items, err := DefaultParamProviders(dyn)["vmimportvm"](context.Background(), "tenant-a", "vcenter")
	if err != nil {
		t.Fatalf("provider returned %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	// Sorted by the label a person reads, not by the reference.
	if items[0].Label != "db-01" || items[0].Value != "vm-7" {
		t.Errorf("first item = %+v, want db-01/vm-7", items[0])
	}
	if items[1].Label != "web-01" || items[1].Value != "vm-52" {
		t.Errorf("second item = %+v, want web-01/vm-52", items[1])
	}
	if items[1].Description != "/dc/vm/web-01" {
		t.Errorf("path not carried: %q", items[1].Description)
	}
}

// A source that has not published yet is a normal state, not a failure: the
// dropdown is empty and the form still renders.
func TestImportVMProviderToleratesAnUnpublishedSource(t *testing.T) {
	dyn := fakeDynWithConfigMaps()

	items, err := DefaultParamProviders(dyn)["vmimportvm"](context.Background(), "tenant-a", "vcenter")
	if err != nil {
		t.Fatalf("provider returned %v", err)
	}
	if len(items) != 0 {
		t.Errorf("got %d items, want none", len(items))
	}
}

// Each source answers with its own machines. Serving one tenant's list for
// another source is the failure this addressing scheme exists to prevent.
func TestImportVMProviderIsScopedToItsArgument(t *testing.T) {
	dyn := fakeDynWithConfigMaps(
		inventoryConfigMap("tenant-a", "vcenter-one", `[{"id":"vm-1","name":"one"}]`),
		inventoryConfigMap("tenant-a", "vcenter-two", `[{"id":"vm-2","name":"two"}]`),
	)
	provider := DefaultParamProviders(dyn)["vmimportvm"]

	for _, tc := range []struct{ source, want string }{
		{"vcenter-one", "vm-1"},
		{"vcenter-two", "vm-2"},
	} {
		items, err := provider(context.Background(), "tenant-a", tc.source)
		if err != nil {
			t.Fatalf("%s: %v", tc.source, err)
		}
		if len(items) != 1 || items[0].Value != tc.want {
			t.Errorf("%s: got %v, want [%s]", tc.source, values(items), tc.want)
		}
	}
}

// The whole point of the parameterised name: `<source>.<argument>` reaches the
// provider with the argument intact.
func TestGetRoutesAParameterisedName(t *testing.T) {
	dyn := fakeDynWithConfigMaps(inventoryConfigMap("tenant-a", "vcenter",
		`[{"id":"vm-52","name":"web-01"}]`))
	r := NewRESTWithParams(DefaultProviders(dyn), DefaultParamProviders(dyn))
	ctx := request.WithNamespace(context.Background(), "tenant-a")

	obj, err := r.Get(ctx, "vmimportvm.vcenter", &metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get returned %v", err)
	}
	opt, ok := obj.(*corev1alpha1.Option)
	if !ok {
		t.Fatalf("Get returned %T", obj)
	}
	if len(opt.Spec.Items) != 1 || opt.Spec.Items[0].Value != "vm-52" {
		t.Errorf("items = %v", values(opt.Spec.Items))
	}
}

// Without an argument the name describes nothing, and answering it with an
// empty list would look like a source with no machines.
func TestGetRejectsAParameterisedNameWithoutAnArgument(t *testing.T) {
	dyn := fakeDynWithConfigMaps()
	r := NewRESTWithParams(DefaultProviders(dyn), DefaultParamProviders(dyn))
	ctx := request.WithNamespace(context.Background(), "tenant-a")

	for _, name := range []string{"vmimportvm", "vmimportvm.", "nosuch.arg"} {
		if _, err := r.Get(ctx, name, &metav1.GetOptions{}); err == nil {
			t.Errorf("Get(%q) succeeded, want NotFound", name)
		}
	}
}

// A parameterised source must stay out of List: the console iterates it to fill
// every static dropdown, and an entry that needs an argument has no contents
// there.
func TestListOmitsParameterisedSources(t *testing.T) {
	dyn := fakeDynWithConfigMaps()
	r := NewRESTWithParams(DefaultProviders(dyn), DefaultParamProviders(dyn))
	ctx := request.WithNamespace(context.Background(), "tenant-a")

	obj, err := r.List(ctx, nil)
	if err != nil {
		t.Fatalf("List returned %v", err)
	}
	list, ok := obj.(*corev1alpha1.OptionList)
	if !ok {
		t.Fatalf("List returned %T", obj)
	}
	for _, o := range list.Items {
		if o.Name == "vmimportvm" {
			t.Fatalf("parameterised source appeared in List")
		}
	}
}
