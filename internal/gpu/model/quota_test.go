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
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestParseQuotaKey(t *testing.T) {
	tests := []struct {
		key       corev1.ResourceName
		wantClass string
		wantTotal bool
		wantOK    bool
	}{
		{key: "l40s-24c.gpuclass.cozystack.io/devices", wantClass: "l40s-24c", wantOK: true},
		{key: "total.gpuclass.cozystack.io/devices", wantClass: "total", wantOK: true},
		{key: "gpuclass.cozystack.io/devices", wantTotal: true, wantOK: true},
		{key: ".gpuclass.cozystack.io/devices"},
		{key: "a.b.gpuclass.cozystack.io/devices"},
		{key: "H100.gpuclass.cozystack.io/devices"},
		{key: corev1.ResourceName(strings.Repeat("a", 64) + ".gpuclass.cozystack.io/devices")},
		{key: "h100.gpuclass.cozystack.io/units"},
		{key: "h100.gpuclass.cozystack.io/devices/x"},
		{key: "gpuclass.cozystack.io/"},
		{key: "xgpuclass.cozystack.io/devices"},
		{key: "requests.h100.gpuclass.cozystack.io/devices"},
		{key: "nvidia.com/GH100_H100"},
		{key: "cpu"},
	}
	for _, tc := range tests {
		t.Run(string(tc.key), func(t *testing.T) {
			k, ok := ParseQuotaKey(tc.key)
			if ok != tc.wantOK || k.Class != tc.wantClass || k.Total != tc.wantTotal {
				t.Errorf("ParseQuotaKey = (%+v, %v), want ({Class:%s Total:%v}, %v)", k, ok, tc.wantClass, tc.wantTotal, tc.wantOK)
			}
		})
	}
}

func TestClassQuotaKey(t *testing.T) {
	if got := ClassQuotaKey("h100"); got != "h100.gpuclass.cozystack.io/devices" {
		t.Errorf("ClassQuotaKey = %s", got)
	}
	k, ok := ParseQuotaKey(ClassQuotaKey("h100"))
	if !ok || k.Class != "h100" || k.Total {
		t.Errorf("round trip gave (%+v, %v)", k, ok)
	}
}

// Every key in the budget domain is a GPU key, well-formed or not, so a
// mistyped budget never renders into a ResourceQuota as a pod resource.
func TestIsGPUQuotaKey(t *testing.T) {
	for key, want := range map[corev1.ResourceName]bool{
		"h100.gpuclass.cozystack.io/devices": true,
		"gpuclass.cozystack.io/devices":      true,
		"gpuclass.cozystack.io/":             true,
		"a.b.gpuclass.cozystack.io/units":    true,
		"xgpuclass.cozystack.io/devices":     false,
		"gpuclass.cozystack.io.example/x":    false,
		"nvidia.com/GH100_H100":              false,
		"cpu":                                false,
		"requests.cpu":                       false,
	} {
		if got := IsGPUQuotaKey(key); got != want {
			t.Errorf("IsGPUQuotaKey(%s) = %v, want %v", key, got, want)
		}
	}
}
