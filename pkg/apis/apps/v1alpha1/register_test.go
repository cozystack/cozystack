// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Cozystack Authors.

package v1alpha1

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/cozystack/cozystack/pkg/config"
)

// RegisterDynamicTypes must register kinds in the group their
// ApplicationDefinition selected — the default apps.cozystack.io when unset —
// including the internal version the apiserver machinery converts through,
// and must set a version priority for every non-default group so
// NewDefaultAPIGroupInfo can resolve it.
func TestRegisterDynamicTypes_MultiGroup(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	if err := scheme.SetVersionPriority(SchemeGroupVersion); err != nil {
		t.Fatalf("SetVersionPriority: %v", err)
	}

	cfg := &config.ResourceConfig{Resources: []config.Resource{
		{Application: config.ApplicationConfig{Kind: "Bucket", Plural: "buckets"}},
		{Application: config.ApplicationConfig{Group: "apps.example.com", Kind: "Widget", Plural: "widgets"}},
	}}
	if err := RegisterDynamicTypes(scheme, cfg); err != nil {
		t.Fatalf("RegisterDynamicTypes: %v", err)
	}

	for _, gvk := range []schema.GroupVersionKind{
		{Group: GroupName, Version: "v1alpha1", Kind: "Bucket"},
		{Group: GroupName, Version: runtime.APIVersionInternal, Kind: "Bucket"},
		{Group: "apps.example.com", Version: "v1alpha1", Kind: "Widget"},
		{Group: "apps.example.com", Version: "v1alpha1", Kind: "WidgetList"},
		{Group: "apps.example.com", Version: runtime.APIVersionInternal, Kind: "Widget"},
	} {
		if !scheme.Recognizes(gvk) {
			t.Errorf("scheme does not recognize %v", gvk)
		}
	}

	prioritized := scheme.PrioritizedVersionsForGroup("apps.example.com")
	if len(prioritized) != 1 || prioritized[0].Version != "v1alpha1" {
		t.Errorf("prioritized versions for apps.example.com = %v, want [v1alpha1]", prioritized)
	}
}

func TestGroupOrDefault(t *testing.T) {
	if got := GroupOrDefault(""); got != GroupName {
		t.Errorf("GroupOrDefault(\"\") = %q, want %q", got, GroupName)
	}
	if got := GroupOrDefault("apps.example.com"); got != "apps.example.com" {
		t.Errorf("GroupOrDefault passthrough = %q, want apps.example.com", got)
	}
}
