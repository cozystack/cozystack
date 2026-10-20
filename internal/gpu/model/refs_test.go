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
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/validation"
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
		{name: "data after the list", in: `[{"device":"a"}] x`, wantErr: true},
		{name: "bracket after the list", in: `[{"device":"a"}]]`, wantErr: true},
		{name: "brace after the list", in: `[{"device":"a"}]}`, wantErr: true},
		{name: "trailing whitespace", in: "[{\"device\":\"a\"}]\n", want: []DeviceRef{{Device: "a"}}},
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
	// Worker VMIs are named after Cluster API machines and can reach the
	// 253 characters of a DNS subdomain.
	long := strings.Repeat("a", 120) + "." + strings.Repeat("b", 132)
	other := strings.Repeat("a", 120) + "." + strings.Repeat("b", 131) + "c"
	// The cut falls on character 235 with a two-digit index; a dot or dash
	// there must not end up next to the dash before the hash.
	cutAtDot := strings.Repeat("a", 234) + "." + strings.Repeat("b", 18)
	cutAtDash := strings.Repeat("a", 234) + "-" + strings.Repeat("b", 18)
	for _, vmi := range []string{long, other, cutAtDot, cutAtDash} {
		got := TemplateDeviceName(vmi, 12)
		if strings.Contains(got, ".-") || strings.Contains(got, "--") {
			t.Errorf("TemplateDeviceName(%d chars) = %q joins the cut to the hash badly", len(vmi), got)
		}
		if errs := validation.IsDNS1123Subdomain(got); len(errs) > 0 {
			t.Errorf("TemplateDeviceName(%d chars) = %q: %v", len(vmi), got, errs)
		}
		if !strings.HasSuffix(got, "-gpu-12") {
			t.Errorf("TemplateDeviceName lost the index: %q", got)
		}
	}
	if TemplateDeviceName(long, 0) == TemplateDeviceName(other, 0) {
		t.Error("two long VMI names sharing a prefix map to one device")
	}
	// sha256 of long starts with a2a436117d; the cut keeps 236 characters.
	if got, want := TemplateDeviceName(long, 0), strings.Repeat("a", 120)+"."+strings.Repeat("b", 115)+"-a2a436117d-gpu-0"; got != want {
		t.Errorf("TemplateDeviceName(long, 0) = %q, want %q", got, want)
	}
}
