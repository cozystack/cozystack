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

	"k8s.io/utils/ptr"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
)

func TestEffectiveMaxCount(t *testing.T) {
	tests := []struct {
		name      string
		spec      gpuv1alpha1.GPUClassSpec
		want      int32
		wantLimit bool
	}{
		{"vgpu unset defaults to 1", gpuv1alpha1.GPUClassSpec{Mode: gpuv1alpha1.GPUModeVGPU}, 1, true},
		{"vgpu set", gpuv1alpha1.GPUClassSpec{Mode: gpuv1alpha1.GPUModeVGPU, MaxCount: ptr.To[int32](2)}, 2, true},
		{"passthrough unset is no limit", gpuv1alpha1.GPUClassSpec{Mode: gpuv1alpha1.GPUModePassthrough}, 0, false},
		{"passthrough set", gpuv1alpha1.GPUClassSpec{Mode: gpuv1alpha1.GPUModePassthrough, MaxCount: ptr.To[int32](4)}, 4, true},
	}
	for _, tc := range tests {
		got, limited := EffectiveMaxCount(&tc.spec)
		if got != tc.want || limited != tc.wantLimit {
			t.Errorf("%s: EffectiveMaxCount = (%d, %v), want (%d, %v)", tc.name, got, limited, tc.want, tc.wantLimit)
		}
	}
}

func TestResourceNames(t *testing.T) {
	classes := []gpuv1alpha1.GPUClass{
		{Spec: gpuv1alpha1.GPUClassSpec{Source: gpuv1alpha1.GPUClassSource{DevicePlugin: &gpuv1alpha1.DevicePluginSource{ResourceName: "nvidia.com/NVIDIA_L40S-24C"}}}},
		{Spec: gpuv1alpha1.GPUClassSpec{Source: gpuv1alpha1.GPUClassSource{DevicePlugin: &gpuv1alpha1.DevicePluginSource{ResourceName: "nvidia.com/GH100_H100"}}}},
		{Spec: gpuv1alpha1.GPUClassSpec{}},
	}
	nodes := []gpuv1alpha1.GPUNode{
		{Status: gpuv1alpha1.GPUNodeStatus{ResourceNames: []string{"nvidia.com/gpu", "nvidia.com/GH100_H100"}}},
	}
	got := ResourceNames(classes, []string{"nvidia.com/AD102GL_L40S", "", "nvidia.com/GH100_H100"}, nodes)
	want := []string{"nvidia.com/AD102GL_L40S", "nvidia.com/GH100_H100", "nvidia.com/NVIDIA_L40S-24C", "nvidia.com/gpu"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ResourceNames = %v, want %v", got, want)
	}
}

func TestResourceNamesFormatRoundTrip(t *testing.T) {
	names := []string{"nvidia.com/AD102GL_L40S", "nvidia.com/gpu"}
	s := FormatResourceNames(names)
	if s != "nvidia.com/AD102GL_L40S,nvidia.com/gpu" {
		t.Errorf("FormatResourceNames = %q", s)
	}
	if got := ParseResourceNames(s); !reflect.DeepEqual(got, names) {
		t.Errorf("ParseResourceNames = %v, want %v", got, names)
	}
	if got := ParseResourceNames(""); len(got) != 0 {
		t.Errorf("ParseResourceNames(\"\") = %v, want empty", got)
	}
	if got := ParseResourceNames(" a/b , ,c/d"); !reflect.DeepEqual(got, []string{"a/b", "c/d"}) {
		t.Errorf("ParseResourceNames tolerates spaces and empties: got %v", got)
	}
}
