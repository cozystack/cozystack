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
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/cache"
)

func appDefinition(name, kind, plural string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cozystack.io/v1alpha1",
		"kind":       "ApplicationDefinition",
		"metadata":   map[string]any{"name": name},
		"spec":       map[string]any{"application": map[string]any{"kind": kind, "plural": plural, "singular": name}},
	}}
}

func newTestKinds(t *testing.T, synced bool, defs ...*unstructured.Unstructured) *ApplicationKinds {
	t.Helper()
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, ApplicationPluralIndexers())
	for _, d := range defs {
		if err := indexer.Add(d); err != nil {
			t.Fatal(err)
		}
	}
	return NewApplicationKinds(indexer, func() bool { return synced })
}

func TestApplicationKinds(t *testing.T) {
	kinds := newTestKinds(t, true,
		appDefinition("postgres", "Postgres", "postgreses"),
		appDefinition("vm-instance", "VMInstance", "vminstances"),
		appDefinition("broken", "", "brokens"),
	)
	for plural, want := range map[string]string{"postgreses": "Postgres", "vminstances": "VMInstance", "postgres": "", "brokens": "", "": ""} {
		got, err := kinds.Kind(plural)
		if err != nil || got != want {
			t.Errorf("Kind(%q) = %q, %v; want %q", plural, got, err, want)
		}
	}
}

func TestApplicationKindsNotSynced(t *testing.T) {
	kinds := newTestKinds(t, false, appDefinition("postgres", "Postgres", "postgreses"))
	if _, err := kinds.Kind("postgreses"); !errors.Is(err, ErrKindsNotSynced) {
		t.Fatalf("Kind before sync: err = %v, want ErrKindsNotSynced", err)
	}
}

func TestApplicationKindsAmbiguous(t *testing.T) {
	kinds := newTestKinds(t, true, appDefinition("a", "Postgres", "postgreses"), appDefinition("b", "Postgresql", "postgreses"))
	if kind, err := kinds.Kind("postgreses"); err == nil {
		t.Fatalf("two kinds for one plural resolved to %q", kind)
	}
	same := newTestKinds(t, true, appDefinition("a", "Postgres", "postgreses"), appDefinition("b", "Postgres", "postgreses"))
	if kind, err := same.Kind("postgreses"); err != nil || kind != "Postgres" {
		t.Fatalf("duplicate definitions of one kind: %q, %v", kind, err)
	}
}
