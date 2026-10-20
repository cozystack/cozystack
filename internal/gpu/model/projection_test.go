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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
)

func gpuContainer(limits, requests corev1.ResourceList) corev1.Container {
	return corev1.Container{Resources: corev1.ResourceRequirements{Limits: limits, Requests: requests}}
}

func TestPodFrom(t *testing.T) {
	names := sets.New(h100Name, l40sName)
	one := resource.MustParse("1")
	two := resource.MustParse("2")
	tests := []struct {
		name    string
		pod     corev1.Pod
		covered map[corev1.ResourceName]int64
		want    map[corev1.ResourceName]int64
		wantOK  bool
	}{
		{
			name:   "raw-name pod",
			pod:    corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{gpuContainer(corev1.ResourceList{h100Name: two}, nil)}}},
			want:   map[corev1.ResourceName]int64{h100Name: 2},
			wantOK: true,
		},
		{
			name:   "requests only",
			pod:    corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{gpuContainer(nil, corev1.ResourceList{h100Name: one})}}},
			want:   map[corev1.ResourceName]int64{h100Name: 1},
			wantOK: true,
		},
		{
			name:    "fully covered by devices",
			pod:     corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{gpuContainer(corev1.ResourceList{h100Name: two}, nil)}}},
			covered: map[corev1.ResourceName]int64{h100Name: 2},
			wantOK:  false,
		},
		{
			name:    "partly covered",
			pod:     corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{gpuContainer(corev1.ResourceList{h100Name: two, l40sName: one}, nil)}}},
			covered: map[corev1.ResourceName]int64{h100Name: 1},
			want:    map[corev1.ResourceName]int64{h100Name: 1, l40sName: 1},
			wantOK:  true,
		},
		{
			name:   "non-GPU resource ignored",
			pod:    corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{gpuContainer(corev1.ResourceList{"nvidia.com/hostdev": one}, nil)}}},
			wantOK: false,
		},
		{
			name: "init container larger than the sum",
			pod: corev1.Pod{Spec: corev1.PodSpec{
				InitContainers: []corev1.Container{gpuContainer(corev1.ResourceList{h100Name: two}, nil)},
				Containers:     []corev1.Container{gpuContainer(corev1.ResourceList{h100Name: one}, nil)},
			}},
			want:   map[corev1.ResourceName]int64{h100Name: 2},
			wantOK: true,
		},
		{
			name: "finished pod holds nothing",
			pod: corev1.Pod{
				Spec:   corev1.PodSpec{Containers: []corev1.Container{gpuContainer(corev1.ResourceList{h100Name: two}, nil)}},
				Status: corev1.PodStatus{Phase: corev1.PodFailed},
			},
			wantOK: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.pod.Namespace, tc.pod.Spec.NodeName = "tenant-acme", "n1"
			got, ok := PodFrom(&tc.pod, names, tc.covered)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && (!reflect.DeepEqual(got.Units, tc.want) || got.Namespace != "tenant-acme" || got.NodeName != "n1") {
				t.Errorf("PodFrom = %+v, want units %v", got, tc.want)
			}
		})
	}
}

func TestProjections(t *testing.T) {
	class := &gpuv1alpha1.GPUClass{
		ObjectMeta: metav1.ObjectMeta{Name: "h100"},
		Spec: gpuv1alpha1.GPUClassSpec{
			Mode:         gpuv1alpha1.GPUModePassthrough,
			NodeSelector: map[string]string{"gpu": "h100"},
			Source:       gpuv1alpha1.GPUClassSource{DevicePlugin: &gpuv1alpha1.DevicePluginSource{ResourceName: string(h100Name)}},
		},
	}
	if got, want := ClassFrom(class), (Class{Name: "h100", Mode: gpuv1alpha1.GPUModePassthrough, ResourceName: h100Name, NodeSelector: map[string]string{"gpu": "h100"}}); !reflect.DeepEqual(got, want) {
		t.Errorf("ClassFrom = %+v, want %+v", got, want)
	}

	device := &gpuv1alpha1.GPUDevice{
		ObjectMeta: metav1.ObjectMeta{Name: "train-0", Namespace: "tenant-acme"},
		Spec:       gpuv1alpha1.GPUDeviceSpec{ClassName: "h100", Count: 2},
		Status:     gpuv1alpha1.GPUDeviceStatus{Phase: gpuv1alpha1.GPUDeviceBound, NodeName: "n1"},
	}
	if got, want := DeviceFrom(device), (Device{Namespace: "tenant-acme", Name: "train-0", Class: "h100", Count: 2, Phase: gpuv1alpha1.GPUDeviceBound, NodeName: "n1"}); got != want {
		t.Errorf("DeviceFrom = %+v, want %+v", got, want)
	}

	n := readyNode(func(n *corev1.Node) {
		n.Labels = map[string]string{"gpu": "h100"}
		n.Status.Allocatable = corev1.ResourceList{h100Name: resource.MustParse("8")}
		n.Spec.Unschedulable = true
	})
	got := NodeFrom(n, nil)
	if got.Name != "gpu-node-3" || got.Allocatable[h100Name] != 8 || got.Eligible || got.Labels["gpu"] != "h100" {
		t.Errorf("NodeFrom = %+v", got)
	}
}
