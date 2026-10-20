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

package model

import (
	"reflect"
	"testing"
)

func TestParentNamespace(t *testing.T) {
	for ns, want := range map[string]string{
		"tenant-root":           "",
		"tenant-acme":           "tenant-root",
		"tenant-acme-dev":       "tenant-acme",
		"tenant-acme-dev-alice": "tenant-acme-dev",
		"cozy-system":           "",
		"default":               "",
	} {
		if got := ParentNamespace(ns); got != want {
			t.Errorf("ParentNamespace(%s) = %q, want %q", ns, got, want)
		}
	}
}

func TestAncestors(t *testing.T) {
	tests := map[string][]string{
		"tenant-acme-dev": {"tenant-acme-dev", "tenant-acme", "tenant-root"},
		"tenant-root":     {"tenant-root"},
		"cozy-system":     {"cozy-system"},
	}
	for ns, want := range tests {
		if got := Ancestors(ns); !reflect.DeepEqual(got, want) {
			t.Errorf("Ancestors(%s) = %v, want %v", ns, got, want)
		}
	}
}

func TestInSubtree(t *testing.T) {
	tests := []struct {
		ns, root string
		want     bool
	}{
		{"tenant-acme-dev", "tenant-acme", true},
		{"tenant-acme", "tenant-acme", true},
		{"tenant-acme", "tenant-root", true},
		{"tenant-acme-dev", "tenant-root", true},
		{"tenant-acmex", "tenant-acme", false},
		{"tenant-root", "tenant-acme", false},
		{"tenant-other", "tenant-acme", false},
	}
	for _, tc := range tests {
		if got := InSubtree(tc.ns, tc.root); got != tc.want {
			t.Errorf("InSubtree(%s, %s) = %v, want %v", tc.ns, tc.root, got, tc.want)
		}
	}
}

func TestEntitled(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   bool
	}{
		{"label true", map[string]string{"namespace.cozystack.io/vgpu": "true"}, true},
		{"label false", map[string]string{"namespace.cozystack.io/vgpu": "false"}, false},
		{"label True is not true", map[string]string{"namespace.cozystack.io/vgpu": "True"}, false},
		{"no label", map[string]string{"other": "true"}, false},
		{"nil labels", nil, false},
	}
	for _, tc := range tests {
		if got := Entitled(tc.labels); got != tc.want {
			t.Errorf("%s: Entitled = %v, want %v", tc.name, got, tc.want)
		}
	}
}
