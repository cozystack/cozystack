/*
Copyright 2024 The Cozystack Authors.

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

package server

import (
	"testing"

	"k8s.io/kube-openapi/pkg/validation/spec"
)

func TestPatchObjectMetaNameValidation(t *testing.T) {
	tests := []struct {
		name          string
		schemas       map[string]*spec.Schema
		wantPattern   string
		wantMaxLength *int64
	}{
		{
			name: "patches ObjectMeta name field",
			schemas: map[string]*spec.Schema{
				objectMetaRef: {
					SchemaProps: spec.SchemaProps{
						Properties: map[string]spec.Schema{
							"name": {SchemaProps: spec.SchemaProps{Type: []string{"string"}}},
						},
					},
				},
			},
			wantPattern:   applicationNamePattern,
			wantMaxLength: ptr(int64(applicationNameMaxLength)),
		},
		{
			name:          "no panic when ObjectMeta missing",
			schemas:       map[string]*spec.Schema{},
			wantPattern:   "",
			wantMaxLength: nil,
		},
		{
			name: "no panic when name field missing",
			schemas: map[string]*spec.Schema{
				objectMetaRef: {
					SchemaProps: spec.SchemaProps{
						Properties: map[string]spec.Schema{},
					},
				},
			},
			wantPattern:   "",
			wantMaxLength: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			patchObjectMetaNameValidation(tt.schemas)

			objMeta, ok := tt.schemas[objectMetaRef]
			if !ok {
				if tt.wantPattern != "" || tt.wantMaxLength != nil {
					t.Error("expected ObjectMeta to exist")
				}
				return
			}

			name, ok := objMeta.Properties["name"]
			if !ok {
				if tt.wantPattern != "" || tt.wantMaxLength != nil {
					t.Error("expected name field to exist")
				}
				return
			}

			if name.Pattern != tt.wantPattern {
				t.Errorf("Pattern = %q, want %q", name.Pattern, tt.wantPattern)
			}
			if (name.MaxLength == nil) != (tt.wantMaxLength == nil) {
				t.Errorf("MaxLength nil mismatch")
			} else if name.MaxLength != nil && *name.MaxLength != *tt.wantMaxLength {
				t.Errorf("MaxLength = %d, want %d", *name.MaxLength, *tt.wantMaxLength)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }
