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

import (
	"encoding/json"
	"strings"
	"testing"
)

// mustSchema parses a JSON schema literal, mirroring how cozyrd openAPISchema
// strings and CRD openAPIV3Schema nodes decode at runtime.
func mustSchema(t *testing.T, j string) Schema {
	t.Helper()
	var s Schema
	if err := json.Unmarshal([]byte(j), &s); err != nil {
		t.Fatalf("bad schema literal: %v", err)
	}
	return s
}

func TestDiffSchema(t *testing.T) {
	tests := []struct {
		name     string
		base     string
		head     string
		breaking bool // whether at least one breaking change is expected
		wantSub  string
	}{
		{
			name: "identical is safe",
			base: `{"type":"object","properties":{"replicas":{"type":"integer"}}}`,
			head: `{"type":"object","properties":{"replicas":{"type":"integer"}}}`,
		},
		{
			name: "new optional field is additive",
			base: `{"type":"object","properties":{"a":{"type":"string"}}}`,
			head: `{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"integer"}}}`,
		},
		{
			name: "description-only change is safe",
			base: `{"type":"object","properties":{"a":{"type":"string","description":"old"}}}`,
			head: `{"type":"object","properties":{"a":{"type":"string","description":"new"}}}`,
		},
		{
			name: "default change is safe",
			base: `{"type":"object","properties":{"a":{"type":"integer","default":1}}}`,
			head: `{"type":"object","properties":{"a":{"type":"integer","default":2}}}`,
		},
		{
			name:     "removed field is breaking",
			base:     `{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"}}}`,
			head:     `{"type":"object","properties":{"a":{"type":"string"}}}`,
			breaking: true,
			wantSub:  `"b" was removed`,
		},
		{
			name:     "new required field is breaking",
			base:     `{"type":"object","properties":{"a":{"type":"string"}}}`,
			head:     `{"type":"object","required":["b"],"properties":{"a":{"type":"string"},"b":{"type":"string"}}}`,
			breaking: true,
			wantSub:  `"b" is now required`,
		},
		{
			name:     "existing optional field becoming required is breaking",
			base:     `{"type":"object","properties":{"a":{"type":"string"}}}`,
			head:     `{"type":"object","required":["a"],"properties":{"a":{"type":"string"}}}`,
			breaking: true,
			wantSub:  `"a" is now required`,
		},
		{
			name: "new enum value is additive",
			base: `{"type":"object","properties":{"v":{"type":"string","enum":["a","b"]}}}`,
			head: `{"type":"object","properties":{"v":{"type":"string","enum":["a","b","c"]}}}`,
		},
		{
			name:     "removed enum value is breaking",
			base:     `{"type":"object","properties":{"v":{"type":"string","enum":["a","b","c"]}}}`,
			head:     `{"type":"object","properties":{"v":{"type":"string","enum":["a","b"]}}}`,
			breaking: true,
			wantSub:  "enum value(s) removed: c",
		},
		{
			name:     "adding an enum where none existed is breaking",
			base:     `{"type":"object","properties":{"v":{"type":"string"}}}`,
			head:     `{"type":"object","properties":{"v":{"type":"string","enum":["a"]}}}`,
			breaking: true,
			wantSub:  "enum constraint added",
		},
		{
			name:     "type narrowing is breaking",
			base:     `{"type":"object","properties":{"v":{"anyOf":[{"type":"integer"},{"type":"string"}]}}}`,
			head:     `{"type":"object","properties":{"v":{"type":"string"}}}`,
			breaking: true,
			wantSub:  "type narrowed",
		},
		{
			name: "type widening is safe",
			base: `{"type":"object","properties":{"v":{"type":"string"}}}`,
			head: `{"type":"object","properties":{"v":{"anyOf":[{"type":"integer"},{"type":"string"}]}}}`,
		},
		{
			name:     "added pattern is breaking",
			base:     `{"type":"object","properties":{"v":{"type":"string"}}}`,
			head:     `{"type":"object","properties":{"v":{"type":"string","pattern":"^x"}}}`,
			breaking: true,
			wantSub:  "pattern constraint added",
		},
		{
			name: "removed pattern is safe",
			base: `{"type":"object","properties":{"v":{"type":"string","pattern":"^x"}}}`,
			head: `{"type":"object","properties":{"v":{"type":"string"}}}`,
		},
		{
			name:     "raised minimum is breaking",
			base:     `{"type":"object","properties":{"n":{"type":"integer","minimum":1}}}`,
			head:     `{"type":"object","properties":{"n":{"type":"integer","minimum":2}}}`,
			breaking: true,
			wantSub:  "minimum raised",
		},
		{
			name: "lowered minimum is safe",
			base: `{"type":"object","properties":{"n":{"type":"integer","minimum":2}}}`,
			head: `{"type":"object","properties":{"n":{"type":"integer","minimum":1}}}`,
		},
		{
			name:     "lowered maximum is breaking",
			base:     `{"type":"object","properties":{"n":{"type":"integer","maximum":10}}}`,
			head:     `{"type":"object","properties":{"n":{"type":"integer","maximum":5}}}`,
			breaking: true,
			wantSub:  "maximum lowered",
		},
		{
			name:     "nested map value break is caught",
			base:     `{"type":"object","properties":{"users":{"type":"object","additionalProperties":{"type":"object","properties":{"pw":{"type":"string"}}}}}}`,
			head:     `{"type":"object","properties":{"users":{"type":"object","additionalProperties":{"type":"object","properties":{}}}}}`,
			breaking: true,
			wantSub:  `"pw" was removed`,
		},
		{
			name:     "nested array item break is caught",
			base:     `{"type":"object","properties":{"list":{"type":"array","items":{"type":"object","properties":{"x":{"type":"string"}}}}}}`,
			head:     `{"type":"object","properties":{"list":{"type":"array","items":{"type":"object","properties":{}}}}}`,
			breaking: true,
			wantSub:  `"x" was removed`,
		},

		// B1(a): removing a nested constraint entirely is a widening, not a break.
		{
			name: "removing items schema is safe (widening)",
			base: `{"type":"object","properties":{"list":{"type":"array","items":{"type":"string"}}}}`,
			head: `{"type":"object","properties":{"list":{"type":"array"}}}`,
		},
		{
			name: "removing additionalProperties schema is safe (widening)",
			base: `{"type":"object","properties":{"m":{"type":"object","additionalProperties":{"type":"string"}}}}`,
			head: `{"type":"object","properties":{"m":{"type":"object"}}}`,
		},
		// B1(b): adding a type to a previously-unconstrained field is breaking.
		{
			name:     "adding a type to an unconstrained field is breaking",
			base:     `{"type":"object","properties":{"v":{}}}`,
			head:     `{"type":"object","properties":{"v":{"type":"string"}}}`,
			breaking: true,
			wantSub:  "type constraint added",
		},
		// B1(c): a field becoming unconstrained is a widening, not a break.
		{
			name: "field becoming unconstrained is safe (widening)",
			base: `{"type":"object","properties":{"v":{"type":"string"}}}`,
			head: `{"type":"object","properties":{"v":{}}}`,
		},
		// B2: restricting additionalProperties to false rejects undeclared fields.
		{
			name:     "additionalProperties true to false is breaking",
			base:     `{"type":"object","properties":{"m":{"type":"object","additionalProperties":true}}}`,
			head:     `{"type":"object","properties":{"m":{"type":"object","additionalProperties":false}}}`,
			breaking: true,
			wantSub:  "additionalProperties restricted to false",
		},
		{
			name:     "additionalProperties absent to false is breaking",
			base:     `{"type":"object","properties":{"m":{"type":"object"}}}`,
			head:     `{"type":"object","properties":{"m":{"type":"object","additionalProperties":false}}}`,
			breaking: true,
			wantSub:  "additionalProperties restricted to false",
		},
		{
			name: "additionalProperties false to true is safe (widening)",
			base: `{"type":"object","properties":{"m":{"type":"object","additionalProperties":false}}}`,
			head: `{"type":"object","properties":{"m":{"type":"object","additionalProperties":true}}}`,
		},
		{
			name: "additionalProperties false to schema is safe (relaxation)",
			base: `{"type":"object","properties":{"m":{"type":"object","additionalProperties":false}}}`,
			head: `{"type":"object","properties":{"m":{"type":"object","additionalProperties":{"type":"string"}}}}`,
		},
		// Adding a schema where the map/array element was previously
		// unconstrained is a restriction and must be flagged.
		{
			name:     "adding additionalProperties schema to open map is breaking",
			base:     `{"type":"object","properties":{"m":{"type":"object"}}}`,
			head:     `{"type":"object","properties":{"m":{"type":"object","additionalProperties":{"type":"string"}}}}`,
			breaking: true,
			wantSub:  "type constraint added",
		},
		{
			name:     "adding items schema to unconstrained array is breaking",
			base:     `{"type":"object","properties":{"list":{"type":"array"}}}`,
			head:     `{"type":"object","properties":{"list":{"type":"array","items":{"type":"object","properties":{"foo":{"type":"string"}}}}}}`,
			breaking: true,
			wantSub:  "type constraint added",
		},
		// B7(b): CEL validation rules — additions break, removals are safe.
		{
			name:     "added CEL validation rule is breaking",
			base:     `{"type":"object","properties":{"v":{"type":"integer"}}}`,
			head:     `{"type":"object","properties":{"v":{"type":"integer","x-kubernetes-validations":[{"rule":"self > 0","message":"must be positive"}]}}}`,
			breaking: true,
			wantSub:  "validation rule added",
		},
		{
			name: "removed CEL validation rule is safe",
			base: `{"type":"object","properties":{"v":{"type":"integer","x-kubernetes-validations":[{"rule":"self > 0"}]}}}`,
			head: `{"type":"object","properties":{"v":{"type":"integer"}}}`,
		},
		// nullable removal is breaking; adding nullable is a widening.
		{
			name:     "removing nullable is breaking",
			base:     `{"type":"object","properties":{"v":{"type":"string","nullable":true}}}`,
			head:     `{"type":"object","properties":{"v":{"type":"string"}}}`,
			breaking: true,
			wantSub:  "no longer nullable",
		},
		{
			name: "adding nullable is safe (widening)",
			base: `{"type":"object","properties":{"v":{"type":"string"}}}`,
			head: `{"type":"object","properties":{"v":{"type":"string","nullable":true}}}`,
		},
		// oneOf/allOf/not composition added is breaking; removed is safe.
		{
			name:     "adding oneOf composition is breaking",
			base:     `{"type":"object","properties":{"v":{"type":"object"}}}`,
			head:     `{"type":"object","properties":{"v":{"type":"object","oneOf":[{"required":["a"]},{"required":["b"]}]}}}`,
			breaking: true,
			wantSub:  "oneOf composition constraint added",
		},
		{
			name: "removing allOf composition is safe",
			base: `{"type":"object","properties":{"v":{"type":"object","allOf":[{"required":["a"]}]}}}`,
			head: `{"type":"object","properties":{"v":{"type":"object"}}}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := diffSchema("spec", mustSchema(t, tc.base), mustSchema(t, tc.head))
			if tc.breaking && len(got) == 0 {
				t.Fatalf("expected a breaking change, got none")
			}
			if !tc.breaking && len(got) != 0 {
				t.Fatalf("expected no breaking change, got: %v", got)
			}
			if tc.wantSub != "" {
				joined := strings.Join(got, "\n")
				if !strings.Contains(joined, tc.wantSub) {
					t.Fatalf("expected finding containing %q, got:\n%s", tc.wantSub, joined)
				}
			}
		})
	}
}
