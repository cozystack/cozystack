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

package apigate

import "testing"

// res is a small constructor for building test snapshots.
func res(group, kind, plural string, src Source, schema Schema) Resource {
	r := Resource{Group: group, Kind: kind, Plural: plural, Source: src, Versions: map[string]Schema{}}
	if schema != nil {
		r.Versions["v1alpha1"] = schema
	}
	return r
}

func snap(rs ...Resource) Snapshot {
	s := Snapshot{}
	for _, r := range rs {
		s[r.key()] = r
	}
	return s
}

func countCategory(fs []Finding, c Category) int {
	n := 0
	for _, f := range fs {
		if f.Category == c {
			n++
		}
	}
	return n
}

func TestClassify(t *testing.T) {
	pgBase := res("apps.cozystack.io", "Postgres", "postgreses", SourceCozyRD,
		Schema{"type": "object", "properties": map[string]any{"replicas": map[string]any{"type": "integer"}}})
	pgBreaking := res("apps.cozystack.io", "Postgres", "postgreses", SourceCozyRD,
		Schema{"type": "object", "properties": map[string]any{}})

	t.Run("no change", func(t *testing.T) {
		got := Classify(snap(pgBase), snap(pgBase))
		if len(got) != 0 {
			t.Fatalf("expected no findings, got %v", got)
		}
	})

	t.Run("new resource in existing group", func(t *testing.T) {
		redis := res("apps.cozystack.io", "Redis", "redises", SourceCozyRD, Schema{"type": "object"})
		got := Classify(snap(pgBase), snap(pgBase, redis))
		if n := countCategory(got, NewResource); n != 1 {
			t.Fatalf("expected 1 new-resource finding, got %d (%v)", n, got)
		}
		if countCategory(got, NewGroup) != 0 {
			t.Fatalf("existing group must not be flagged as new: %v", got)
		}
	})

	t.Run("new group flags group once and does not double-count its resources", func(t *testing.T) {
		sg1 := res("brandnew.cozystack.io", "Widget", "widgets", SourceCRD, Schema{"type": "object"})
		sg2 := res("brandnew.cozystack.io", "Gadget", "gadgets", SourceCRD, Schema{"type": "object"})
		got := Classify(snap(pgBase), snap(pgBase, sg1, sg2))
		if n := countCategory(got, NewGroup); n != 1 {
			t.Fatalf("expected exactly 1 new-group finding, got %d (%v)", n, got)
		}
		if n := countCategory(got, NewResource); n != 0 {
			t.Fatalf("resources of a brand-new group must not also be flagged as new resources, got %d (%v)", n, got)
		}
	})

	t.Run("breaking schema change", func(t *testing.T) {
		got := Classify(snap(pgBase), snap(pgBreaking))
		if n := countCategory(got, Breaking); n != 1 {
			t.Fatalf("expected 1 breaking finding, got %d (%v)", n, got)
		}
	})

	t.Run("removed resource is not sizeable by these rules", func(t *testing.T) {
		// Removing a resource is a different (also serious) event, but it is
		// not one of the three gated categories; ensure we do not misreport it
		// as any of them.
		got := Classify(snap(pgBase), snap())
		if len(got) != 0 {
			t.Fatalf("expected no findings for a pure removal, got %v", got)
		}
	})

	t.Run("static go-backed new resource is detected", func(t *testing.T) {
		base := snap(res("core.cozystack.io", "", "tenantsecrets", SourceAPIServer, nil))
		head := snap(
			res("core.cozystack.io", "", "tenantsecrets", SourceAPIServer, nil),
			res("core.cozystack.io", "", "tenantwidgets", SourceAPIServer, nil),
		)
		got := Classify(base, head)
		if n := countCategory(got, NewResource); n != 1 {
			t.Fatalf("expected 1 new-resource finding for static group, got %d (%v)", n, got)
		}
	})
}
