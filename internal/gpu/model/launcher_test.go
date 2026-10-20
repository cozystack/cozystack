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
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
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
