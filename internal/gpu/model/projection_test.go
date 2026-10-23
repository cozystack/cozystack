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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
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
			name:    "fully covered by claims",
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
	seconds := int64(300)
	class := &gpuv1alpha1.GPUClass{
		ObjectMeta: metav1.ObjectMeta{Name: "h100"},
		Spec: gpuv1alpha1.GPUClassSpec{
			Mode:         gpuv1alpha1.GPUModePassthrough,
			NodeSelector: map[string]string{"gpu": "h100"},
			Tolerations: []gpuv1alpha1.GPUClassToleration{
				{Key: dedicatedTaint, Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
				{Key: "example.com/maintenance", Operator: corev1.TolerationOpEqual, Value: "planned", Effect: corev1.TaintEffectNoExecute, TolerationSeconds: &seconds},
			},
			Source: gpuv1alpha1.GPUClassSource{DevicePlugin: &gpuv1alpha1.DevicePluginSource{ResourceName: string(h100Name)}},
		},
	}
	if got, want := ClassFrom(class), (Class{Name: "h100", Mode: gpuv1alpha1.GPUModePassthrough, ResourceName: h100Name, NodeSelector: map[string]string{"gpu": "h100"}, Tolerations: append(slices.Clone(tolerateDedicated),
		corev1.Toleration{Key: "example.com/maintenance", Operator: corev1.TolerationOpEqual, Value: "planned", Effect: corev1.TaintEffectNoExecute, TolerationSeconds: &seconds})}); !reflect.DeepEqual(got, want) {
		t.Errorf("ClassFrom = %+v, want %+v", got, want)
	}

	claim := &gpuv1alpha1.GPUClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "train-0", Namespace: "tenant-acme"},
		Spec:       gpuv1alpha1.GPUClaimSpec{ClassName: "h100", Count: 2},
		Status:     gpuv1alpha1.GPUClaimStatus{Phase: gpuv1alpha1.GPUClaimBound, NodeName: "n1"},
	}
	if got, want := ClaimFrom(claim), (Claim{Namespace: "tenant-acme", Name: "train-0", Class: "h100", Count: 2, Phase: gpuv1alpha1.GPUClaimBound, NodeName: "n1"}); got != want {
		t.Errorf("ClaimFrom = %+v, want %+v", got, want)
	}

	n := readyNode(func(n *corev1.Node) {
		n.Labels = map[string]string{"gpu": "h100"}
		n.Status.Allocatable = corev1.ResourceList{h100Name: resource.MustParse("8")}
		n.Spec.Unschedulable = true
		n.Spec.Taints = []corev1.Taint{{Key: dedicatedTaint, Effect: corev1.TaintEffectNoSchedule}}
	})
	got := NodeFrom(n)
	if got.Name != "gpu-node-3" || got.Allocatable[h100Name] != 8 || got.Usable || got.Labels["gpu"] != "h100" || !reflect.DeepEqual(got.Taints, n.Spec.Taints) {
		t.Errorf("NodeFrom = %+v", got)
	}
	if !NodeFrom(readyNode(nil)).Usable {
		t.Error("NodeFrom of a ready, schedulable node is not usable")
	}
}
