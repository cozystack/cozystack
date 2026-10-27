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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
)

func TestLauncherVMI(t *testing.T) {
	vm := types.NamespacedName{Namespace: "tenant-a", Name: "vm"}
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-a", UID: "pod-uid", OwnerReferences: []metav1.OwnerReference{
		{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "rs"},
		{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachineInstance", Name: "vm"},
	}}}
	active := func(uids ...types.UID) func(types.NamespacedName) (sets.Set[types.UID], error) {
		return func(k types.NamespacedName) (sets.Set[types.UID], error) {
			if k != vm {
				t.Fatalf("asked for %v", k)
			}
			if uids == nil {
				return nil, nil
			}
			return sets.New(uids...), nil
		}
	}
	if k, ok, err := LauncherVMI(p, active("pod-uid")); err != nil || !ok || k != vm {
		t.Fatalf("an active pod: %v, %v, %v", k, ok, err)
	}
	if _, ok, _ := LauncherVMI(p, active("another-pod")); ok {
		t.Fatal("a pod the VMI does not list is not its launcher")
	}
	if _, ok, _ := LauncherVMI(p, active()); ok {
		t.Fatal("a missing VMI launches nothing")
	}
	p.OwnerReferences[1].APIVersion = "example.com/v1"
	if _, ok, _ := LauncherVMI(p, active("pod-uid")); ok {
		t.Fatal("a VirtualMachineInstance kind of another group is not KubeVirt's")
	}
}

func TestActivePods(t *testing.T) {
	vmi := &unstructured.Unstructured{Object: map[string]any{}}
	_ = unstructured.SetNestedMap(vmi.Object, map[string]any{"pod-a": "node-1", "pod-b": ""}, "status", "activePods")
	if got := ActivePods(vmi); !got.Equal(sets.New[types.UID]("pod-a", "pod-b")) {
		t.Fatalf("ActivePods = %v", got)
	}
}

func TestCoveredForBudget(t *testing.T) {
	claim := func(name, class string, count int32, phase gpuv1alpha1.GPUClaimPhase, vmi string) *gpuv1alpha1.GPUClaim {
		d := &gpuv1alpha1.GPUClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-a", Name: name},
			Spec: gpuv1alpha1.GPUClaimSpec{ClassName: class, Count: count}, Status: gpuv1alpha1.GPUClaimStatus{Phase: phase}}
		if vmi != "" {
			d.Status.AttachedTo = &gpuv1alpha1.GPUClaimAttachment{VMI: vmi}
		}
		return d
	}
	classes := map[string]Class{"h100": {Name: "h100", ResourceName: "nvidia.com/H100"}, "dra": {Name: "dra"}}
	got := CoveredForBudget([]*gpuv1alpha1.GPUClaim{
		claim("a", "h100", 2, gpuv1alpha1.GPUClaimAttached, "vm"),
		claim("b", "h100", 1, gpuv1alpha1.GPUClaimLost, "vm"),
		claim("c", "h100", 4, gpuv1alpha1.GPUClaimBound, ""),
		claim("d", "dra", 1, gpuv1alpha1.GPUClaimAttached, "vm"),
		claim("e", "gone", 1, gpuv1alpha1.GPUClaimAttached, "vm"),
	}, classes)
	want := map[types.NamespacedName]map[corev1.ResourceName]int64{{Namespace: "tenant-a", Name: "vm"}: {"nvidia.com/H100": 3}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v: attached claims of a device-plugin class, a Lost one included", got, want)
	}
}
