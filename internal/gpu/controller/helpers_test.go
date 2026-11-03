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

package controller

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
)

const (
	rnL40S = "nvidia.com/NVIDIA_L40S-24C"
	rnH100 = "nvidia.com/GH100_H100"
)

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := cozyv1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func newClient(t *testing.T, funcs *interceptor.Funcs, objs ...client.Object) client.WithWatch {
	t.Helper()
	b := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).
		WithStatusSubresource(&cozyv1alpha1.GPUClass{})
	if funcs != nil {
		b = b.WithInterceptorFuncs(*funcs)
	}
	return b.Build()
}

// gpuClass is created age minutes after epoch, so tests order classes by age.
func gpuClass(name, resourceName string, age int, mutate ...func(*cozyv1alpha1.GPUClass)) *cozyv1alpha1.GPUClass {
	c := &cozyv1alpha1.GPUClass{
		ObjectMeta: metav1.ObjectMeta{Name: name, Generation: 1, CreationTimestamp: metav1.NewTime(epoch.Add(time.Duration(age) * time.Minute))},
		Spec: cozyv1alpha1.GPUClassSpec{
			Vendor: "nvidia", Model: "L40S", Mode: cozyv1alpha1.GPUModePassthrough,
			Source: cozyv1alpha1.GPUClassSource{DevicePlugin: &cozyv1alpha1.DevicePluginSource{ResourceName: resourceName}},
		},
	}
	for _, m := range mutate {
		m(c)
	}
	return c
}

func gpuNode(name string, alloc map[string]int64, mutate ...func(*corev1.Node)) *corev1.Node {
	n := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{},
			Conditions:  []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}
	for rn, v := range alloc {
		n.Status.Allocatable[corev1.ResourceName(rn)] = *resource.NewQuantity(v, resource.DecimalSI)
	}
	for _, m := range mutate {
		m(n)
	}
	return n
}

func gpuPod(name, node, resourceName string, units int64, mutate ...func(*corev1.Pod)) *corev1.Pod {
	q := *resource.NewQuantity(units, resource.DecimalSI)
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-a", Name: name},
		Spec: corev1.PodSpec{NodeName: node, Containers: []corev1.Container{{
			Name:      "compute",
			Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceName(resourceName): q}},
		}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	for _, m := range mutate {
		m(p)
	}
	return p
}

func getClass(t *testing.T, c client.Client, name string) *cozyv1alpha1.GPUClass {
	t.Helper()
	out := &cozyv1alpha1.GPUClass{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: name}, out); err != nil {
		t.Fatalf("get GPUClass %s: %v", name, err)
	}
	return out
}
