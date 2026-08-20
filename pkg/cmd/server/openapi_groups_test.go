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

package server

import (
	"testing"

	"k8s.io/kube-openapi/pkg/spec3"
	"k8s.io/kube-openapi/pkg/validation/spec"
)

// newAppsV3Doc builds a minimal OpenAPI v3 group-version document the way the
// generic apiserver produces one for an application group: base Application
// schemas present, paths rooted at /apis/<group>/v1alpha1.
func newAppsV3Doc(group string) *spec3.OpenAPI {
	baseSchema := func() *spec.Schema {
		s := newObjectContainer()
		s.Properties["spec"] = spec.Schema{SchemaProps: spec.SchemaProps{Type: spec.StringOrArray{"object"}}}
		s.Properties["status"] = spec.Schema{SchemaProps: spec.SchemaProps{Ref: spec.MustCreateRef("#/components/schemas/" + baseStatusRef)}}
		return s
	}
	listSchema := newObjectContainer()
	listSchema.Properties["items"] = spec.Schema{SchemaProps: spec.SchemaProps{
		Type: spec.StringOrArray{"array"},
		Items: &spec.SchemaOrArray{Schema: &spec.Schema{SchemaProps: spec.SchemaProps{
			Ref: spec.MustCreateRef("#/components/schemas/" + baseRef),
		}}},
	}}
	return &spec3.OpenAPI{
		Paths: &spec3.Paths{Paths: map[string]*spec3.Path{
			"/apis/" + group + "/v1alpha1/namespaces/{namespace}/buckets": {},
		}},
		Components: &spec3.Components{Schemas: map[string]*spec.Schema{
			baseRef:       baseSchema(),
			baseListRef:   listSchema,
			baseStatusRef: newObjectContainer(),
		}},
	}
}

func kindGVKGroup(t *testing.T, s *spec.Schema) string {
	t.Helper()
	gvks, ok := s.Extensions["x-kubernetes-group-version-kind"].([]any)
	if !ok || len(gvks) == 0 {
		t.Fatalf("schema has no x-kubernetes-group-version-kind extension: %#v", s.Extensions)
	}
	gvk, ok := gvks[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected gvk shape: %#v", gvks[0])
	}
	group, _ := gvk["group"].(string)
	return group
}

// The v3 post-processor runs once per group-version document: each document
// must receive only the kinds served in its own group, stamped with that
// group, while kinds of other groups stay out of it entirely.
func TestBuildPostProcessV3_FiltersKindsByDocumentGroup(t *testing.T) {
	kindSchemas := map[string]KindSchema{
		"Bucket": {Group: "apps.cozystack.io", Schema: ""},
		"Widget": {Group: "apps.example.com", Schema: ""},
	}
	process := BuildPostProcessV3(kindSchemas)

	cases := []struct {
		docGroup   string
		wantKind   string
		absentKind string
	}{
		{"apps.cozystack.io", "Bucket", "Widget"},
		{"apps.example.com", "Widget", "Bucket"},
	}
	for _, tc := range cases {
		doc, err := process(newAppsV3Doc(tc.docGroup))
		if err != nil {
			t.Fatalf("post-process %s: %v", tc.docGroup, err)
		}
		wantRef := apiPrefix + "." + tc.wantKind
		got, ok := doc.Components.Schemas[wantRef]
		if !ok {
			t.Fatalf("doc for %s is missing schema %s", tc.docGroup, wantRef)
		}
		if group := kindGVKGroup(t, got); group != tc.docGroup {
			t.Errorf("kind %s stamped with group %q, want %q", tc.wantKind, group, tc.docGroup)
		}
		if _, ok := doc.Components.Schemas[apiPrefix+"."+tc.absentKind]; ok {
			t.Errorf("doc for %s must not contain foreign kind %s", tc.docGroup, tc.absentKind)
		}
		if _, ok := doc.Components.Schemas[baseRef]; ok {
			t.Errorf("doc for %s still contains the base Application schema", tc.docGroup)
		}
	}
}

// The v2 swagger is one merged document across all groups: every kind is
// cloned into it, each stamped with its own group.
func TestBuildPostProcessV2_StampsPerKindGroup(t *testing.T) {
	base := *newObjectContainer()
	base.Properties["spec"] = spec.Schema{SchemaProps: spec.SchemaProps{Type: spec.StringOrArray{"object"}}}
	list := *newObjectContainer()
	stat := *newObjectContainer()
	sw := &spec.Swagger{SwaggerProps: spec.SwaggerProps{
		Paths: &spec.Paths{Paths: map[string]spec.PathItem{}},
		Definitions: spec.Definitions{
			baseRef:       base,
			baseListRef:   list,
			baseStatusRef: stat,
		},
	}}

	process := BuildPostProcessV2(map[string]KindSchema{
		"Bucket": {Group: "apps.cozystack.io", Schema: ""},
		"Widget": {Group: "apps.example.com", Schema: ""},
	})
	out, err := process(sw)
	if err != nil {
		t.Fatalf("post-process: %v", err)
	}

	for kind, wantGroup := range map[string]string{
		"Bucket": "apps.cozystack.io",
		"Widget": "apps.example.com",
	} {
		def, ok := out.Definitions[apiPrefix+"."+kind]
		if !ok {
			t.Fatalf("merged swagger is missing kind %s", kind)
		}
		if group := kindGVKGroup(t, &def); group != wantGroup {
			t.Errorf("kind %s stamped with group %q, want %q", kind, group, wantGroup)
		}
	}
}

func TestDocGroupV3(t *testing.T) {
	if got := docGroupV3(newAppsV3Doc("apps.example.com")); got != "apps.example.com" {
		t.Errorf("docGroupV3 = %q, want apps.example.com", got)
	}
	if got := docGroupV3(&spec3.OpenAPI{}); got != "" {
		t.Errorf("docGroupV3 on empty doc = %q, want empty", got)
	}
}
