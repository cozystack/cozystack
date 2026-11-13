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

import "testing"

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
