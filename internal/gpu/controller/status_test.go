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
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu"
)

func TestClassStatusCarriesFreeAndMaxFreePerNodeOnly(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 4}),
		node("n2", map[string]int64{rnL40S: 2}),
		node("cordoned", map[string]int64{rnL40S: 8}, cordoned),
		claim("a", "l40s", 1, bound("n1")),
		claim("l", "l40s", 5, lost("gone")),
		claim("p", "l40s", 9),
	)
	e.pass()
	st := e.class("l40s").Status
	if *st.Free != 5 || *st.MaxFreePerNode != 3 {
		t.Fatalf("free %d, maxFreePerNode %d, want 5 and 3", *st.Free, *st.MaxFreePerNode)
	}
	raw, _ := json.Marshal(st)
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	for f := range fields {
		if f != "free" && f != "maxFreePerNode" && f != "conditions" {
			t.Errorf("GPUClass status carries %q, which every authenticated user reads", f)
		}
	}
	for _, c := range st.Conditions {
		for _, n := range []string{"n1", "n2", "cordoned", "gone"} {
			if strings.Contains(c.Message, n) {
				t.Errorf("condition %s names node %s: %q", c.Type, n, c.Message)
			}
		}
	}

	if got := testutil.ToFloat64(classCapacity.WithLabelValues("l40s")); got != 6 {
		t.Errorf("capacity metric %v, want 6: the cordoned node does not count", got)
	}
	if got := testutil.ToFloat64(classBound.WithLabelValues("l40s")); got != 1 {
		t.Errorf("bound metric %v, want 1", got)
	}
	if got := testutil.ToFloat64(classLost.WithLabelValues("l40s")); got != 5 {
		t.Errorf("lost metric %v, want 5", got)
	}
	if got := testutil.ToFloat64(classWaiting.WithLabelValues("l40s")); got != 14 {
		t.Errorf("waiting metric %v, want 14 (Pending 9 and Lost 5)", got)
	}
}

func TestTwoClassesSharingAResourceNameOnANodeConflict(t *testing.T) {
	e := newEnv(t,
		class("a", rnL40S, selecting("gpu", "l40s")),
		class("b", rnL40S, selecting("gpu", "l40s")),
		class("c", rnL40S, selecting("gpu", "other")),
		// Two more classes conflict with each other on n1, under another
		// name; they are not a's conflict.
		class("x", rnH100),
		class("y", rnH100),
		node("n1", map[string]int64{rnL40S: 4}, labelled("gpu", "l40s")),
		&gpuv1alpha1.GPUNode{ObjectMeta: metav1.ObjectMeta{Name: "n1"}},
		claim("order", "a", 1),
	)
	e.pass()
	expectPlacement(t, e.claim("order"), gpuv1alpha1.GPUClaimPending, "")
	for name, want := range map[string]metav1.ConditionStatus{"a": metav1.ConditionTrue, "b": metav1.ConditionTrue, "c": metav1.ConditionFalse} {
		c := meta.FindStatusCondition(e.class(name).Status.Conditions, gpu.ConditionResourceNameConflict)
		if c == nil || c.Status != want {
			t.Fatalf("class %s conflict condition %+v, want %s", name, c, want)
		}
	}
	if msg := meta.FindStatusCondition(e.class("a").Status.Conditions, gpu.ConditionResourceNameConflict).Message; !strings.Contains(msg, "by b with") || strings.Contains(msg, "n1") || strings.Contains(msg, "x") {
		t.Fatalf("class condition %q must name only the other class of its name, and no node", msg)
	}
	gn := &gpuv1alpha1.GPUNode{}
	_ = e.c.Get(t.Context(), key("", "n1"), gn)
	c := meta.FindStatusCondition(gn.Status.Conditions, gpu.ConditionResourceNameConflict)
	if c == nil || c.Status != metav1.ConditionTrue || !strings.Contains(c.Message, "a, b") {
		t.Fatalf("GPUNode condition %+v must name both classes", c)
	}
}

func TestClassOutsideThePermittedHostDevicesTableGetsACondition(t *testing.T) {
	e := newEnv(t, class("l40s", rnL40S), class("h100", rnH100), kubeVirt(rnH100))
	e.pass()
	for name, want := range map[string]metav1.ConditionStatus{"l40s": metav1.ConditionTrue, "h100": metav1.ConditionFalse} {
		c := meta.FindStatusCondition(e.class(name).Status.Conditions, ConditionResourceNameNotPermitted)
		if c == nil || c.Status != want {
			t.Fatalf("class %s: %+v, want %s", name, c, want)
		}
	}
}

func TestAnUnsetPermittedHostDevicesTablePermitsEveryName(t *testing.T) {
	kv := kubeVirt()
	delete(kv.Object, "spec")
	e := newEnv(t, class("l40s", rnL40S), kv)
	e.pass()
	if c := meta.FindStatusCondition(e.class("l40s").Status.Conditions, ConditionResourceNameNotPermitted); c.Status != metav1.ConditionFalse {
		t.Fatalf("%+v", c)
	}
}

func TestSettingsConfigMapHoldsTheEffectiveResourceNames(t *testing.T) {
	gn := &gpuv1alpha1.GPUNode{ObjectMeta: metav1.ObjectMeta{Name: "n1"}}
	gn.Status.ResourceNames = []string{"nvidia.com/gpu"}
	e := newEnv(t, class("l40s", rnL40S), gn)
	e.r.Config.ResourceNames = []string{rnH100}
	e.r.Config.RawResourceNames = gpu.RawNamesForbidden
	e.pass()
	cm := &corev1.ConfigMap{}
	if err := e.c.Get(t.Context(), key(gpu.SettingsNamespace, gpu.SettingsConfigMap), cm); err != nil {
		t.Fatalf("settings: %v", err)
	}
	if got := cm.Data[gpu.SettingsKeyResourceNames]; got != "nvidia.com/H100,nvidia.com/L40S,nvidia.com/gpu" {
		t.Fatalf("resourceNames = %q", got)
	}
	if got := cm.Data[gpu.SettingsKeyRawResourceNames]; got != "forbidden" {
		t.Fatalf("rawResourceNames = %q", got)
	}

	e.r.Config.RawResourceNames = ""
	e.now = e.now.Add(time.Minute)
	e.pass()
	_ = e.c.Get(t.Context(), key(gpu.SettingsNamespace, gpu.SettingsConfigMap), cm)
	if got := cm.Data[gpu.SettingsKeyRawResourceNames]; got != "allowed" {
		t.Fatalf("rawResourceNames default = %q", got)
	}
}

// The NVIDIA and mdev names of the KubeVirt table are GPU names besides the
// hoster's list, also when the hoster sets one: a name only the table
// carries is a GPU name too; a NIC there is not.
func TestSettingsConfigMapTakesTheKubeVirtTableGPUNames(t *testing.T) {
	kv := kubeVirt("nvidia.com/GA102GL_A10")
	entries, _, _ := unstructured.NestedSlice(kv.Object, "spec", "configuration", "permittedHostDevices", "pciHostDevices")
	entries = append(entries, map[string]any{"resourceName": "nvidia.com/hostdev", "pciVendorSelector": "15B3:101E"})
	_ = unstructured.SetNestedSlice(kv.Object, entries, "spec", "configuration", "permittedHostDevices", "pciHostDevices")
	e := newEnv(t, class("l40s", rnL40S), kv)
	e.r.Config.ResourceNames = []string{"nvidia.com/gpu"}
	e.pass()
	cm := &corev1.ConfigMap{}
	if err := e.c.Get(t.Context(), key(gpu.SettingsNamespace, gpu.SettingsConfigMap), cm); err != nil {
		t.Fatalf("settings: %v", err)
	}
	if got := cm.Data[gpu.SettingsKeyResourceNames]; got != "nvidia.com/GA102GL_A10,nvidia.com/L40S,nvidia.com/gpu" {
		t.Fatalf("resourceNames = %q", got)
	}
}

// Right after the first create a stale cache still reads NotFound.
func TestSettingsCreateOnAStaleCacheIsNotAnError(t *testing.T) {
	existing := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: gpu.SettingsNamespace, Name: gpu.SettingsConfigMap}}
	c := fake.NewClientBuilder().WithScheme(testScheme(true)).WithObjects(existing).
		WithInterceptorFuncs(interceptor.Funcs{Get: func(ctx context.Context, cl client.WithWatch, k client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.ConfigMap); ok {
				return apierrors.NewNotFound(corev1.Resource("configmaps"), k.Name)
			}
			return cl.Get(ctx, k, obj, opts...)
		}}).Build()
	r := &Reconciler{Client: c, Reader: c, Config: gpu.Config{Enabled: true}}
	if err := r.settings(t.Context(), &world{}); err != nil {
		t.Fatalf("settings: %v", err)
	}
}

// Only a node that carries the class's GPUs can lose capacity for it: a
// NotReady CPU node the class's empty selector matches does not set the
// condition, and a NotReady GPU node does.
func TestClassLostCapacityCountsOnlyNodesWithItsGPUs(t *testing.T) {
	for name, tc := range map[string]struct {
		alloc map[string]int64
		held  bool
		want  metav1.ConditionStatus
	}{
		// kubelet reports no allocatable until the plugin registers again.
		"GPU node reporting none with a held claim": {map[string]int64{}, true, metav1.ConditionTrue},
		"CPU node": {map[string]int64{}, false, metav1.ConditionFalse},
		"GPU node": {map[string]int64{rnL40S: 2}, false, metav1.ConditionTrue},
	} {
		t.Run(name, func(t *testing.T) {
			objs := []client.Object{
				class("l40s", rnL40S),
				node("gpu1", map[string]int64{rnL40S: 2}),
				node("down", tc.alloc, notReadySince(t0.Add(-time.Hour))),
			}
			if tc.held {
				objs = append(objs, claim("a", "l40s", 1, bound("down"), attached("vm1")), vmiObj("vm1", "Running", "down", ""))
			}
			e := newEnv(t, objs...)
			e.pass()
			c := meta.FindStatusCondition(e.class("l40s").Status.Conditions, gpu.ConditionLostCapacity)
			if c == nil || c.Status != tc.want {
				t.Fatalf("LostCapacity = %+v, want %s", c, tc.want)
			}
		})
	}
}
