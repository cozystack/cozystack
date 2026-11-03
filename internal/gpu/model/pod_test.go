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
	"k8s.io/apimachinery/pkg/util/sets"
)

func gpuContainer(limits, requests corev1.ResourceList) corev1.Container {
	return corev1.Container{Resources: corev1.ResourceRequirements{Limits: limits, Requests: requests}}
}

func sidecar(limits corev1.ResourceList) corev1.Container {
	c := gpuContainer(limits, nil)
	always := corev1.ContainerRestartPolicyAlways
	c.RestartPolicy = &always
	return c
}

func TestPodFrom(t *testing.T) {
	names := sets.New(h100Name, l40sName)
	one := resource.MustParse("1")
	two := resource.MustParse("2")
	tests := []struct {
		name   string
		pod    corev1.Pod
		want   map[corev1.ResourceName]int64
		wantOK bool
	}{
		{
			name:   "limits",
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
			name:   "two names in two containers",
			pod:    corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{gpuContainer(corev1.ResourceList{h100Name: two}, nil), gpuContainer(corev1.ResourceList{l40sName: one}, nil)}}},
			want:   map[corev1.ResourceName]int64{h100Name: 2, l40sName: 1},
			wantOK: true,
		},
		{
			name:   "name outside the set ignored",
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
			name: "sidecar adds to the main containers",
			pod: corev1.Pod{Spec: corev1.PodSpec{
				InitContainers: []corev1.Container{sidecar(corev1.ResourceList{h100Name: one})},
				Containers:     []corev1.Container{gpuContainer(corev1.ResourceList{h100Name: one}, nil)},
			}},
			want:   map[corev1.ResourceName]int64{h100Name: 2},
			wantOK: true,
		},
		{
			name: "init container runs beside an earlier sidecar",
			pod: corev1.Pod{Spec: corev1.PodSpec{
				InitContainers: []corev1.Container{
					sidecar(corev1.ResourceList{h100Name: one}),
					gpuContainer(corev1.ResourceList{h100Name: two}, nil),
				},
				Containers: []corev1.Container{gpuContainer(corev1.ResourceList{h100Name: one}, nil)},
			}},
			want:   map[corev1.ResourceName]int64{h100Name: 3},
			wantOK: true,
		},
		{
			name: "init container before a sidecar does not run beside it",
			pod: corev1.Pod{Spec: corev1.PodSpec{
				InitContainers: []corev1.Container{
					gpuContainer(corev1.ResourceList{h100Name: two}, nil),
					sidecar(corev1.ResourceList{h100Name: one}),
				},
				Containers: []corev1.Container{gpuContainer(corev1.ResourceList{h100Name: one}, nil)},
			}},
			want:   map[corev1.ResourceName]int64{h100Name: 2},
			wantOK: true,
		},
		{
			// The scheduler adds a RuntimeClass overhead to the pod's request.
			name: "overhead adds to the request",
			pod: corev1.Pod{Spec: corev1.PodSpec{
				Overhead:   corev1.ResourceList{h100Name: one},
				Containers: []corev1.Container{gpuContainer(corev1.ResourceList{h100Name: one}, nil)},
			}},
			want:   map[corev1.ResourceName]int64{h100Name: 2},
			wantOK: true,
		},
		{
			name: "succeeded pod requests nothing",
			pod: corev1.Pod{
				Spec:   corev1.PodSpec{Containers: []corev1.Container{gpuContainer(corev1.ResourceList{h100Name: two}, nil)}},
				Status: corev1.PodStatus{Phase: corev1.PodSucceeded},
			},
			wantOK: false,
		},
		{
			name: "failed pod requests nothing",
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
			got, ok := PodFrom(&tc.pod, names)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && (!reflect.DeepEqual(got.Units, tc.want) || got.Namespace != "tenant-acme" || got.NodeName != "n1") {
				t.Errorf("PodFrom = %+v, want units %v", got, tc.want)
			}
		})
	}
}
