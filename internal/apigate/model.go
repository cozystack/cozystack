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

// Package apigate detects "sizeable" Cozystack API changes between two
// checkouts of the repository so CI can require a review from a designated
// API owner. Sizeable means one of: a new API group, a new resource, or a
// breaking change to an existing resource's schema. The detection is fully
// deterministic — it parses the checked-in, codegen-kept-in-sync artifacts
// that define the served API surface, with no network or LLM calls.
package apigate

// Category classifies why a change is sizeable. The three values mirror the
// gate's contract exactly.
type Category string

const (
	// NewGroup is emitted when an API group appears in head but not base.
	NewGroup Category = "new-api-group"
	// NewResource is emitted when a (group, resource) pair appears in head
	// but not base.
	NewResource Category = "new-resource"
	// Breaking is emitted when an existing resource's schema changes in a
	// way that can reject requests or objects that the base schema accepted
	// (removed field, narrowed type/enum, newly-required field, tightened
	// constraint, removed served version).
	Breaking Category = "breaking-change"
)

// Source identifies which checked-in artifact a resource was parsed from.
// It is carried through to findings so the report points reviewers at the
// right file family.
type Source string

const (
	// SourceCRD is a CustomResourceDefinition YAML manifest.
	SourceCRD Source = "crd"
	// SourceCozyRD is an ApplicationDefinition (cozyrd) backing the
	// apps.cozystack.io aggregated API.
	SourceCozyRD Source = "cozyrd"
	// SourceAPIServer is a static Go-backed aggregated resource discovered
	// from the apiserver storage registrations. These carry no checked-in
	// schema, so they participate only in new-group / new-resource
	// detection.
	SourceAPIServer Source = "apiserver"
)

// Resource is the normalized identity + schema of one API resource, keyed
// across base and head by (Group, Plural). Plural is always present; Kind may
// be empty for static Go-backed resources whose kind is not recoverable from
// the storage registration alone.
type Resource struct {
	Group  string
	Kind   string
	Plural string
	Source Source
	// Origin is the repo-relative path the resource was parsed from, used
	// only for human-facing reporting.
	Origin string
	// Versions maps a served version name to its parsed schema. Empty for
	// SourceAPIServer resources (schema lives only in Go structs, out of
	// scope for breaking detection).
	Versions map[string]Schema
}

// key is the base/head correlation key for a resource.
func (r Resource) key() resourceKey { return resourceKey{Group: r.Group, Plural: r.Plural} }

type resourceKey struct {
	Group  string
	Plural string
}

// Schema is a parsed JSON/OpenAPI-v3 schema node, using the generic
// map[string]any shape produced by sigs.k8s.io/yaml (JSON number/string/bool
// semantics). Comparing parsed nodes rather than raw text makes the diff
// insensitive to key ordering and formatting, and lets us ignore purely
// descriptive fields.
type Schema map[string]any

// Snapshot is the full set of API resources discovered in one checkout,
// indexed by correlation key.
type Snapshot map[resourceKey]Resource

// groups returns the set of API group names present in the snapshot.
func (s Snapshot) groups() map[string]struct{} {
	out := make(map[string]struct{})
	for k := range s {
		out[k.Group] = struct{}{}
	}
	return out
}

// Finding is one reason a change is sizeable, ready to render into the CI
// report.
type Finding struct {
	Category Category
	Group    string
	Kind     string
	Plural   string
	Source   Source
	Origin   string
	// Detail is a human-readable explanation. For Breaking findings it names
	// the schema path and the nature of the break.
	Detail string
}
