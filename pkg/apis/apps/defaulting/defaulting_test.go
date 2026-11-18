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

package defaulting

import "testing"

// A nil map[string]any in an interface is not == nil, so it has to be
// recognised as empty explicitly, at the root and below it, or writing the
// first default into it panics.
func TestApplyDefaultsTypedNilMaps(t *testing.T) {
	s, err := BuildSchema(`{"type":"object","properties":{
		"replicas":{"type":"integer","default":2},
		"roles":{"type":"array","default":["worker"],"items":{"type":"string"}},
		"resources":{"type":"object","default":{},"properties":{"preset":{"type":"string","default":"t1.micro"}}}}}`)
	if err != nil {
		t.Fatal(err)
	}

	var root map[string]any
	if err := Apply(&root, s); err != nil {
		t.Fatal(err)
	}
	if root["replicas"] != int64(2) {
		t.Errorf("root: replicas = %#v, want 2", root["replicas"])
	}

	var nested map[string]any
	var roles []any
	root = map[string]any{"resources": nested, "roles": roles}
	if err := Apply(&root, s); err != nil {
		t.Fatal(err)
	}
	resources, _ := root["resources"].(map[string]any)
	if resources["preset"] != "t1.micro" {
		t.Errorf("nested: resources = %#v, want the preset default", root["resources"])
	}
	if got, _ := root["roles"].([]any); len(got) != 1 || got[0] != "worker" {
		t.Errorf("nested: roles = %#v, want the array default", root["roles"])
	}
}
