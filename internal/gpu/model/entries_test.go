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

	"k8s.io/apimachinery/pkg/util/sets"
)

func TestParseClassEntries(t *testing.T) {
	tests := []struct {
		name, raw string
		want      []ClassEntry
		wantErr   bool
	}{
		{name: "two entries", raw: `[{"className":"h100","count":2},{"className":"l40s-24c"}]`, want: []ClassEntry{{ClassName: "h100", Count: 2}, {ClassName: "l40s-24c", Count: 1}}},
		{name: "empty annotation", raw: "", wantErr: true},
		{name: "empty list", raw: "[]", wantErr: true},
		{name: "not JSON", raw: "h100", wantErr: true},
		{name: "no class name", raw: `[{"count":1}]`, wantErr: true},
		{name: "class name not a DNS label", raw: `[{"className":"H100"}]`, wantErr: true},
		{name: "zero count", raw: `[{"className":"h100","count":0}]`, wantErr: true},
		{name: "negative count", raw: `[{"className":"h100","count":-1}]`, wantErr: true},
		{name: "unknown field", raw: `[{"className":"h100","name":"nvidia.com/x"}]`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseClassEntries(tc.raw)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParseClassEntries = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestGPUResourceNames(t *testing.T) {
	got := GPUResourceNames([]string{"amd.com/gpu", ""}, []Class{{ResourceName: h100Name}, {}}, []string{"nvidia.com/L40S"})
	if want := []string{"amd.com/gpu", "nvidia.com/GH100_H100", "nvidia.com/L40S"}; !reflect.DeepEqual(sets.List(got), want) {
		t.Errorf("GPUResourceNames = %v, want %v", sets.List(got), want)
	}
}
