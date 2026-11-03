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

package observabilityproxy

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
)

// The cases mirror packages/system/monitoring-agents/tests/
// fluent-bit_tenant_log_routing_test.yaml: reads must resolve to the store
// the collector wrote to.
func TestStoreResolver(t *testing.T) {
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	add := func(name string, labels map[string]string) {
		if err := indexer.Add(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}); err != nil {
			t.Fatal(err)
		}
	}
	add("cozy-system", nil)
	add("tenant-empty", map[string]string{monitoringLabel: ""})
	for ns, target := range map[string]string{
		"tenant-root":          "tenant-root",
		"tenant-inherits-root": "tenant-root",
		"tenant-a":             "tenant-a",
		"tenant-a-child":       "tenant-a",
		"tenant-b":             "tenant-b",
	} {
		add(ns, map[string]string{monitoringLabel: target})
	}
	add("tenant-forged", map[string]string{monitoringLabel: "pod.tenant-b"})
	lister := corev1listers.NewNamespaceLister(indexer)
	if got, err := NewStoreResolver(lister, "tenant-root").Resolve("tenant-forged"); err == nil {
		t.Fatalf("label naming a host outside a namespace resolved to %q", got)
	}

	tests := []struct {
		root, ns, want string
	}{
		{"tenant-root", "cozy-system", "tenant-root"},
		{"tenant-root", "tenant-empty", "tenant-root"},
		{"tenant-root", "tenant-root", "tenant-root"},
		{"tenant-root", "tenant-inherits-root", "tenant-root"},
		{"tenant-root", "tenant-a", "tenant-a"},
		{"tenant-root", "tenant-a-child", "tenant-a"},
		{"tenant-root", "tenant-b", "tenant-b"},
		{"tenant-root", "tenant-gone", "tenant-root"},
		{"tenant-a", "tenant-a", "tenant-a"},
		{"tenant-a", "tenant-a-child", "tenant-a"},
		{"tenant-a", "tenant-root", "tenant-root"},
		{"tenant-a", "tenant-inherits-root", "tenant-root"},
		{"tenant-a", "cozy-system", "tenant-a"},
	}
	for _, tt := range tests {
		r := NewStoreResolver(lister, tt.root)
		got, err := r.Resolve(tt.ns)
		if err != nil {
			t.Fatalf("root=%s Resolve(%s) error: %v", tt.root, tt.ns, err)
		}
		if got != tt.want {
			t.Errorf("root=%s Resolve(%s) = %s, want %s", tt.root, tt.ns, got, tt.want)
		}
	}
}
