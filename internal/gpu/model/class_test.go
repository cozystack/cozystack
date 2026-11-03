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
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
)

const (
	h100Name = corev1.ResourceName("nvidia.com/GH100_H100")
	l40sName = corev1.ResourceName("nvidia.com/NVIDIA_L40S-24C")
)

func TestEffectiveMaxCount(t *testing.T) {
	tests := []struct {
		name      string
		spec      cozyv1alpha1.GPUClassSpec
		want      int32
		wantLimit bool
	}{
		{"vgpu unset defaults to 1", cozyv1alpha1.GPUClassSpec{Mode: cozyv1alpha1.GPUModeVGPU}, 1, true},
		{"vgpu set", cozyv1alpha1.GPUClassSpec{Mode: cozyv1alpha1.GPUModeVGPU, MaxCount: ptr.To[int32](2)}, 2, true},
		{"passthrough unset is no limit", cozyv1alpha1.GPUClassSpec{Mode: cozyv1alpha1.GPUModePassthrough}, 0, false},
		{"passthrough set", cozyv1alpha1.GPUClassSpec{Mode: cozyv1alpha1.GPUModePassthrough, MaxCount: ptr.To[int32](4)}, 4, true},
	}
	for _, tc := range tests {
		got, limited := EffectiveMaxCount(&tc.spec)
		if got != tc.want || limited != tc.wantLimit {
			t.Errorf("%s: EffectiveMaxCount = (%d, %v), want (%d, %v)", tc.name, got, limited, tc.want, tc.wantLimit)
		}
	}
}

func TestClassFrom(t *testing.T) {
	seconds := int64(300)
	class := &cozyv1alpha1.GPUClass{
		ObjectMeta: metav1.ObjectMeta{Name: "h100", CreationTimestamp: metav1.Unix(1000, 0)},
		Spec: cozyv1alpha1.GPUClassSpec{
			Mode: cozyv1alpha1.GPUModePassthrough,
			Tolerations: []cozyv1alpha1.GPUClassToleration{
				{Key: dedicatedTaint, Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
				{Key: "example.com/maintenance", Operator: corev1.TolerationOpEqual, Value: "planned", Effect: corev1.TaintEffectNoExecute, TolerationSeconds: &seconds},
			},
			Source: cozyv1alpha1.GPUClassSource{DevicePlugin: &cozyv1alpha1.DevicePluginSource{ResourceName: string(h100Name)}},
		},
	}
	want := Class{Name: "h100", Created: metav1.Unix(1000, 0), Mode: cozyv1alpha1.GPUModePassthrough, ResourceName: h100Name, Tolerations: append(slices.Clone(tolerateDedicated),
		corev1.Toleration{Key: "example.com/maintenance", Operator: corev1.TolerationOpEqual, Value: "planned", Effect: corev1.TaintEffectNoExecute, TolerationSeconds: &seconds})}
	if got := ClassFrom(class); !reflect.DeepEqual(got, want) {
		t.Errorf("ClassFrom = %+v, want %+v", got, want)
	}
}

func TestConflicts(t *testing.T) {
	older, newer := metav1.Unix(1000, 0), metav1.Unix(2000, 0)
	tests := []struct {
		name    string
		classes []Class
		want    []string
	}{
		{name: "distinct names", classes: []Class{{Name: "h100", ResourceName: h100Name}, {Name: "l40s-24c", ResourceName: l40sName}}},
		{
			name:    "the newer class conflicts",
			classes: []Class{{Name: "h100", Created: older, ResourceName: h100Name}, {Name: "h100-alt", Created: newer, ResourceName: h100Name}, {Name: "l40s-24c", Created: newer, ResourceName: l40sName}},
			want:    []string{"h100-alt"},
		},
		{
			name:    "the newer class conflicts whatever the list order",
			classes: []Class{{Name: "h100-alt", Created: newer, ResourceName: h100Name}, {Name: "h100", Created: older, ResourceName: h100Name}},
			want:    []string{"h100-alt"},
		},
		{
			name:    "creation time decides before the name",
			classes: []Class{{Name: "a", Created: newer, ResourceName: h100Name}, {Name: "z", Created: older, ResourceName: h100Name}},
			want:    []string{"a"},
		},
		{
			name:    "the name breaks a tie in creation time",
			classes: []Class{{Name: "b", Created: older, ResourceName: h100Name}, {Name: "a", Created: older, ResourceName: h100Name}},
			want:    []string{"b"},
		},
		{
			name:    "three classes on one name",
			classes: []Class{{Name: "c", Created: newer, ResourceName: h100Name}, {Name: "a", Created: older, ResourceName: h100Name}, {Name: "b", Created: newer, ResourceName: h100Name}},
			want:    []string{"b", "c"},
		},
		{name: "classes without a resource name do not conflict", classes: []Class{{Name: "a"}, {Name: "b"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Conflicts(tc.classes); !got.Equal(sets.New(tc.want...)) {
				t.Errorf("Conflicts = %v, want %v", sets.List(got), tc.want)
			}
		})
	}
}
