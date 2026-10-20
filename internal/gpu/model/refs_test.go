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

func TestParseDeviceRefs(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    []DeviceRef
		wantErr bool
	}{
		{
			name: "device and template",
			in:   `[{"device":"train-0"},{"deviceTemplate":{"className":"l40s-24c","count":2}}]`,
			want: []DeviceRef{{Device: "train-0"}, {DeviceTemplate: &DeviceTemplate{ClassName: "l40s-24c", Count: 2}}},
		},
		{name: "empty list", in: `[]`, wantErr: true},
		{name: "not JSON", in: `train-0`, wantErr: true},
		{name: "both set", in: `[{"device":"a","deviceTemplate":{"className":"h100","count":1}}]`, wantErr: true},
		{name: "neither set", in: `[{}]`, wantErr: true},
		{name: "template count 0", in: `[{"deviceTemplate":{"className":"h100","count":0}}]`, wantErr: true},
		{name: "template without class", in: `[{"deviceTemplate":{"count":1}}]`, wantErr: true},
		{name: "unknown field", in: `[{"name":"nvidia.com/gpu"}]`, wantErr: true},
		{name: "same device twice", in: `[{"device":"a"},{"device":"a"}]`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseDeviceRefs(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestFormatDeviceRefsRoundTrip(t *testing.T) {
	refs := []DeviceRef{{Device: "train-0"}, {DeviceTemplate: &DeviceTemplate{ClassName: "h100", Count: 1}}}
	s, err := FormatDeviceRefs(refs)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseDeviceRefs(s)
	if err != nil || !reflect.DeepEqual(got, refs) {
		t.Errorf("round trip: %+v, %v", got, err)
	}
}

func TestTemplateDeviceName(t *testing.T) {
	if got := TemplateDeviceName("vm-instance-trainer", 1); got != "vm-instance-trainer-gpu-1" {
		t.Errorf("TemplateDeviceName = %q", got)
	}
}
