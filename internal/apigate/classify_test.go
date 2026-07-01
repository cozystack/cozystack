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

	t.Run("removed resource is gated", func(t *testing.T) {
		// Deleting a resource is the most disruptive single-resource change and
		// must require owner review. The resource's group still has another
		// member here, so this is a resource removal, not a group removal.
		other := res("apps.cozystack.io", "Redis", "redises", SourceCozyRD, Schema{"type": "object"})
		got := Classify(snap(pgBase, other), snap(other))
		if n := countCategory(got, RemovedResource); n != 1 {
			t.Fatalf("expected 1 removed-resource finding, got %d (%v)", n, got)
		}
		if countCategory(got, RemovedGroup) != 0 {
			t.Fatalf("group still has a member; must not be flagged as removed group: %v", got)
		}
	})

	t.Run("removed group flags group once and does not double-count its resources", func(t *testing.T) {
		g1 := res("gone.cozystack.io", "Widget", "widgets", SourceCRD, Schema{"type": "object"})
		g2 := res("gone.cozystack.io", "Gadget", "gadgets", SourceCRD, Schema{"type": "object"})
		got := Classify(snap(pgBase, g1, g2), snap(pgBase))
		if n := countCategory(got, RemovedGroup); n != 1 {
			t.Fatalf("expected exactly 1 removed-group finding, got %d (%v)", n, got)
		}
		if n := countCategory(got, RemovedResource); n != 0 {
			t.Fatalf("resources of a fully-removed group must not also be flagged as removed resources, got %d (%v)", n, got)
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
