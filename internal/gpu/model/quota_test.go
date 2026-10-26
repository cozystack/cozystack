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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestParseQuotaKey(t *testing.T) {
	tests := []struct {
		key       corev1.ResourceName
		wantClass string
		wantTotal bool
		wantOK    bool
	}{
		{key: "gpu.cozystack.io/l40s-24c", wantClass: "l40s-24c", wantOK: true},
		{key: "gpu.cozystack.io/total", wantTotal: true, wantOK: true},
		{key: "gpu.cozystack.io/", wantOK: false},
		{key: "gpu.cozystack.io/a/b", wantOK: false},
		{key: "nvidia.com/GH100_H100", wantOK: false},
		{key: "cpu", wantOK: false},
		{key: "requests.gpu.cozystack.io/h100", wantOK: false},
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

func TestClassQuotaKeyRoundTrip(t *testing.T) {
	k, ok := ParseQuotaKey(ClassQuotaKey("h100"))
	if !ok || k.Class != "h100" || k.Total {
		t.Errorf("round trip gave (%+v, %v)", k, ok)
	}
}

func TestIsGPUQuotaKey(t *testing.T) {
	for key, want := range map[corev1.ResourceName]bool{
		"gpu.cozystack.io/h100":  true,
		"gpu.cozystack.io/total": true,
		"gpu.cozystack.io/":      true,
		"nvidia.com/GH100_H100":  false,
		"cpu":                    false,
	} {
		if got := IsGPUQuotaKey(key); got != want {
			t.Errorf("IsGPUQuotaKey(%s) = %v, want %v", key, got, want)
		}
	}
}

func TestWithoutGPUQuotaKeys(t *testing.T) {
	in := corev1.ResourceList{
		"cpu":                    resource.MustParse("4"),
		"gpu.cozystack.io/h100":  resource.MustParse("2"),
		"gpu.cozystack.io/total": resource.MustParse("4"),
		"nvidia.com/GH100_H100":  resource.MustParse("1"),
	}
	got := WithoutGPUQuotaKeys(in)
	want := corev1.ResourceList{"cpu": resource.MustParse("4"), "nvidia.com/GH100_H100": resource.MustParse("1")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if len(in) != 4 {
		t.Error("input was modified")
	}
	if got := WithoutGPUQuotaKeys(corev1.ResourceList{"gpu.cozystack.io/h100": resource.MustParse("1")}); len(got) != 0 {
		t.Errorf("GPU-only list left %v", got)
	}
}

func TestRawQuotaLimit(t *testing.T) {
	const name = corev1.ResourceName("nvidia.com/GH100_H100")
	tests := []struct {
		name   string
		quota  corev1.ResourceList
		want   int64
		wantOK bool
	}{
		{name: "bare key", quota: corev1.ResourceList{name: resource.MustParse("2")}, want: 2, wantOK: true},
		{name: "limits key", quota: corev1.ResourceList{"limits." + name: resource.MustParse("3")}, want: 3, wantOK: true},
		{name: "requests key", quota: corev1.ResourceList{"requests." + name: resource.MustParse("1")}, want: 1, wantOK: true},
		{name: "two forms take the smaller", quota: corev1.ResourceList{name: resource.MustParse("2"), "requests." + name: resource.MustParse("1")}, want: 1, wantOK: true},
		{name: "other name", quota: corev1.ResourceList{"nvidia.com/other": resource.MustParse("2")}, wantOK: false},
		{name: "none", quota: nil, wantOK: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := RawQuotaLimit(tc.quota, name)
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("RawQuotaLimit = (%d, %v), want (%d, %v)", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestDeclaredQuotas(t *testing.T) {
	got, err := DeclaredQuotas([]byte(`{"host":"x","resourceQuotas":{"gpu.cozystack.io/total":"4","cpu":"2"}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := corev1.ResourceList{"gpu.cozystack.io/total": resource.MustParse("4"), "cpu": resource.MustParse("2")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got, err := DeclaredQuotas(nil); err != nil || len(got) != 0 {
		t.Fatalf("no values: %v, %v", got, err)
	}
	if _, err := DeclaredQuotas([]byte(`{"resourceQuotas":{"gpu.cozystack.io/total":"four"}}`)); err == nil {
		t.Fatal("a value that is not a quantity must fail the read, not read as no budget")
	}
}
