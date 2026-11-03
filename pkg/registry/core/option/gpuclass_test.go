// SPDX-License-Identifier: Apache-2.0

package option

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
)

func gpuClassObj(name string, spec map[string]any, status map[string]any) *unstructured.Unstructured {
	o := newObj(schema.GroupVersionKind{Group: "cozystack.io", Version: "v1alpha1", Kind: "GPUClass"}, "", name, spec)
	if status != nil {
		_ = unstructured.SetNestedMap(o.Object, status, "status")
	}
	return o
}

func namespaceObj(name string, labels map[string]string) *unstructured.Unstructured {
	o := newObj(schema.GroupVersionKind{Version: "v1", Kind: "Namespace"}, "", name, nil)
	o.SetLabels(labels)
	return o
}

func gpuClassDyn(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds(), objs...)
}

func i64(v int64) *int64 { return &v }

func TestGPUClassProvider(t *testing.T) {
	dyn := gpuClassDyn(
		gpuClassObj("h100", map[string]any{"displayName": "NVIDIA H100", "vendor": "nvidia", "model": "H100", "mode": "passthrough", "memory": "80Gi"},
			map[string]any{"free": int64(5), "maxFreePerNode": int64(4)}),
		gpuClassObj("l40s-24c", map[string]any{"vendor": "nvidia", "model": "L40S", "mode": "vgpu", "profile": "L40S-24C", "maxCount": int64(2)}, nil),
		namespaceObj("tenant-acme", map[string]string{"namespace.cozystack.io/vgpu": "false"}),
	)
	items, err := DefaultProviders(dyn)["gpuclass"](context.Background(), "tenant-acme")
	if err != nil {
		t.Fatal(err)
	}
	want := []corev1alpha1.OptionItem{
		{Value: "h100", Label: "NVIDIA H100", Description: "nvidia H100, passthrough, 80Gi", Free: i64(5), MaxFreePerNode: i64(4)},
		{Value: "l40s-24c", Description: "nvidia L40S, vgpu, L40S-24C", MaxCount: i64(2), RemainingBudget: i64(0)},
	}
	got, _ := json.Marshal(items)
	exp, _ := json.Marshal(want)
	if string(got) != string(exp) {
		t.Errorf("items = %s, want %s", got, exp)
	}
}

// Every authenticated user reads the catalog, so an item says nothing about
// a node: not its name, its address or its own count.
func TestGPUClassProviderNamesNoNode(t *testing.T) {
	node := newObj(schema.GroupVersionKind{Version: "v1", Kind: "Node"}, "", "gpu-node-7", nil)
	_ = unstructured.SetNestedSlice(node.Object, []any{map[string]any{"type": "InternalIP", "address": "192.0.2.17"}}, "status", "addresses")
	_ = unstructured.SetNestedMap(node.Object, map[string]any{"nvidia.com/GH100_H100": "8"}, "status", "allocatable")
	dyn := gpuClassDyn(node,
		gpuClassObj("h100", map[string]any{"vendor": "nvidia", "model": "H100", "mode": "passthrough",
			"source": map[string]any{"devicePlugin": map[string]any{"resourceName": "nvidia.com/GH100_H100"}}},
			map[string]any{"free": int64(8), "maxFreePerNode": int64(8)}),
	)
	items, err := DefaultProviders(dyn)["gpuclass"](context.Background(), "tenant-acme")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(items)
	for _, leak := range []string{"gpu-node-7", "192.0.2.17", "nodeName", "nodes"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("items %s carry %q", raw, leak)
		}
	}
}

func TestGPUClassProviderLeavesOutADeletingClass(t *testing.T) {
	c := gpuClassObj("h100", map[string]any{"vendor": "nvidia", "model": "H100", "mode": "passthrough"}, nil)
	now := metav1.Now()
	c.SetDeletionTimestamp(&now)
	c.SetFinalizers([]string{"test/keep"})
	items, err := DefaultProviders(gpuClassDyn(c))["gpuclass"](context.Background(), "tenant-acme")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Errorf("items = %v", items)
	}
}

func TestGPUClassProviderRemainingBudget(t *testing.T) {
	dyn := gpuClassDyn(
		gpuClassObj("h100", map[string]any{"vendor": "nvidia", "model": "H100", "mode": "passthrough"}, nil),
		gpuClassObj("a100", map[string]any{"vendor": "nvidia", "model": "A100", "mode": "passthrough"}, nil),
		gpuClassObj("l40s-24c", map[string]any{"vendor": "nvidia", "model": "L40S", "mode": "vgpu", "profile": "L40S-24C"}, nil),
	)
	budget := func(_ context.Context, namespace, class string) (int64, bool, error) {
		if namespace != "tenant-acme" {
			t.Errorf("budget asked for namespace %q", namespace)
		}
		switch class {
		case "h100":
			return 3, true, nil
		case "l40s-24c":
			return 7, true, nil
		}
		return 0, false, nil
	}
	items, err := GPUClassProvider(dyn, budget)(context.Background(), "tenant-acme")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*int64{}
	for _, it := range items {
		got[it.Value] = it.RemainingBudget
	}
	if got["h100"] == nil || *got["h100"] != 3 {
		t.Errorf("h100 remainingBudget = %v, want 3", got["h100"])
	}
	if got["a100"] != nil {
		t.Errorf("a100 remainingBudget = %v, want none for an unlimited class", *got["a100"])
	}
	// Without the entitlement no budget makes a vGPU class usable.
	if got["l40s-24c"] == nil || *got["l40s-24c"] != 0 {
		t.Errorf("l40s-24c remainingBudget = %v, want 0", got["l40s-24c"])
	}
}

func TestGPUClassProviderOmitsUnavailableClasses(t *testing.T) {
	for _, condition := range []string{"ResourceNameConflict", "ResourceNameNotPermitted"} {
		t.Run(condition, func(t *testing.T) {
			c := gpuClassObj("h100", map[string]any{"vendor": "nvidia", "model": "H100", "mode": "passthrough"}, map[string]any{
				"conditions": []any{map[string]any{"type": condition, "status": "True"}},
			})
			items, err := GPUClassProvider(gpuClassDyn(c), nil)(context.Background(), "tenant-acme")
			if err != nil {
				t.Fatal(err)
			}
			if len(items) != 0 {
				t.Errorf("unavailable class offered: %v", items)
			}
			_ = unstructured.SetNestedSlice(c.Object, []any{map[string]any{"type": condition, "status": "False"}}, "status", "conditions")
			items, err = GPUClassProvider(gpuClassDyn(c), nil)(context.Background(), "tenant-acme")
			if err != nil || len(items) != 1 {
				t.Errorf("available class hidden: %v, %v", items, err)
			}
		})
	}
}
