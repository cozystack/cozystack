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
	"errors"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu/model"
)

func kubeVirt(permitted ...string) *unstructured.Unstructured {
	kv := &unstructured.Unstructured{}
	kv.SetGroupVersionKind(kubeVirtGVK)
	kv.SetNamespace(model.KubeVirtNamespace)
	kv.SetName(model.KubeVirtName)
	if permitted != nil {
		var devs []any
		for _, rn := range permitted {
			devs = append(devs, map[string]any{"resourceName": rn})
		}
		_ = unstructured.SetNestedSlice(kv.Object, devs, "spec", "configuration", "permittedHostDevices", "pciHostDevices")
	}
	return kv
}

// kubeVirtReader serves kv, or err when set, for the KubeVirt CR and
// delegates everything else.
func kubeVirtReader(c client.Client, kv *unstructured.Unstructured, err error) client.Reader {
	return interceptor.NewClient(c.(client.WithWatch), interceptor.Funcs{
		Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			u, ok := obj.(*unstructured.Unstructured)
			if !ok || u.GroupVersionKind() != kubeVirtGVK {
				return cl.Get(ctx, key, obj, opts...)
			}
			if err != nil {
				return err
			}
			if kv == nil {
				return apierrors.NewNotFound(schema.GroupResource{Group: "kubevirt.io", Resource: "kubevirts"}, key.Name)
			}
			kv.DeepCopyInto(u)
			return nil
		},
	})
}

func runStatus(t *testing.T, c client.Client, reader client.Reader) reconcile.Result {
	t.Helper()
	r := &StatusReconciler{Client: c, Reader: reader}
	res, err := r.Reconcile(context.Background(), reconcile.Request{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return res
}

func cordoned(n *corev1.Node) { n.Spec.Unschedulable = true }
func notReady(n *corev1.Node) { n.Status.Conditions[0].Status = corev1.ConditionFalse }
func tainted(key string) func(*corev1.Node) {
	return func(n *corev1.Node) {
		n.Spec.Taints = append(n.Spec.Taints, corev1.Taint{Key: key, Effect: corev1.TaintEffectNoSchedule})
	}
}

func TestStatusFreeAndMaxFreePerNode(t *testing.T) {
	class := gpuClass("l40s", rnL40S, 0, func(c *cozyv1alpha1.GPUClass) {
		c.Spec.Tolerations = []cozyv1alpha1.GPUClassToleration{{Key: "dedicated", Operator: "Exists"}}
	})
	c := newClient(t, nil, class,
		gpuNode("busy", map[string]int64{rnL40S: 4}),
		gpuNode("idle", map[string]int64{rnL40S: 2}),
		gpuNode("cordoned", map[string]int64{rnL40S: 8}, cordoned),
		gpuNode("not-ready", map[string]int64{rnL40S: 8}, notReady),
		gpuNode("tainted", map[string]int64{rnL40S: 8}, tainted("maintenance")),
		gpuNode("dedicated", map[string]int64{rnL40S: 1}, tainted("dedicated")),
		gpuNode("overcommitted", map[string]int64{rnL40S: 1}),
		gpuNode("cpu-only", nil),
		gpuPod("vm-1", "busy", rnL40S, 1),
		gpuPod("done", "idle", rnL40S, 2, func(p *corev1.Pod) { p.Status.Phase = corev1.PodSucceeded }),
		gpuPod("pending", "", rnL40S, 2),
		gpuPod("other-name", "idle", rnH100, 2),
		gpuPod("vm-2", "overcommitted", rnL40S, 3),
	)
	runStatus(t, c, kubeVirtReader(c, nil, nil))

	got := getClass(t, c, "l40s").Status
	if got.Free == nil || *got.Free != 6 {
		t.Errorf("free = %v, want 6 (busy 3, idle 2, dedicated 1)", ptr.Deref(got.Free, -1))
	}
	if got.MaxFreePerNode == nil || *got.MaxFreePerNode != 3 {
		t.Errorf("maxFreePerNode = %v, want 3", ptr.Deref(got.MaxFreePerNode, -1))
	}
}

func TestStatusNoNodesIsZeroNotAbsent(t *testing.T) {
	c := newClient(t, nil, gpuClass("l40s", rnL40S, 0))
	runStatus(t, c, kubeVirtReader(c, nil, nil))
	got := getClass(t, c, "l40s").Status
	if got.Free == nil || *got.Free != 0 || got.MaxFreePerNode == nil || *got.MaxFreePerNode != 0 {
		t.Errorf("free/maxFreePerNode = %v/%v, want 0/0", got.Free, got.MaxFreePerNode)
	}
}

func TestStatusResourceNameConflict(t *testing.T) {
	c := newClient(t, nil,
		gpuClass("younger", rnL40S, 5),
		gpuClass("older", rnL40S, 1),
		gpuClass("other", rnH100, 9),
	)
	runStatus(t, c, kubeVirtReader(c, nil, nil))

	cond := meta.FindStatusCondition(getClass(t, c, "younger").Status.Conditions, cozyv1alpha1.GPUClassResourceNameConflict)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Fatalf("younger: ResourceNameConflict = %+v, want True", cond)
	}
	if !strings.Contains(cond.Message, "GPUClass older") || !strings.Contains(cond.Message, rnL40S) {
		t.Errorf("younger: message %q should name the class that keeps %s", cond.Message, rnL40S)
	}
	if cond.ObservedGeneration != 1 {
		t.Errorf("observedGeneration = %d, want 1", cond.ObservedGeneration)
	}
	for _, name := range []string{"older", "other"} {
		cond := meta.FindStatusCondition(getClass(t, c, name).Status.Conditions, cozyv1alpha1.GPUClassResourceNameConflict)
		if cond == nil || cond.Status != metav1.ConditionFalse {
			t.Errorf("%s: ResourceNameConflict = %+v, want False", name, cond)
		}
	}
}

func TestStatusResourceNameNotPermitted(t *testing.T) {
	noMatch := &meta.NoKindMatchError{GroupKind: kubeVirtGVK.GroupKind()}
	tests := []struct {
		name string
		kv   *unstructured.Unstructured
		err  error
		want metav1.ConditionStatus
	}{
		{"table leaves the name out", kubeVirt(rnH100), nil, metav1.ConditionTrue},
		{"table lists the name", kubeVirt(rnL40S, rnH100), nil, metav1.ConditionFalse},
		{"table unset", kubeVirt(), nil, metav1.ConditionFalse},
		{"no KubeVirt CR", nil, nil, metav1.ConditionFalse},
		{"KubeVirt not installed", nil, noMatch, metav1.ConditionFalse},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newClient(t, nil, gpuClass("l40s", rnL40S, 0))
			runStatus(t, c, kubeVirtReader(c, tc.kv, tc.err))
			cond := meta.FindStatusCondition(getClass(t, c, "l40s").Status.Conditions, cozyv1alpha1.GPUClassResourceNameNotPermitted)
			if cond == nil || cond.Status != tc.want {
				t.Fatalf("ResourceNameNotPermitted = %+v, want %s", cond, tc.want)
			}
			if tc.want == metav1.ConditionTrue && !strings.Contains(cond.Message, rnL40S) {
				t.Errorf("message %q should name %s", cond.Message, rnL40S)
			}
		})
	}
}

func TestStatusKubeVirtReadErrorWritesNothing(t *testing.T) {
	c := newClient(t, nil, gpuClass("l40s", rnL40S, 0))
	r := &StatusReconciler{Client: c, Reader: kubeVirtReader(c, nil, errors.New("connection refused"))}
	if _, err := r.Reconcile(context.Background(), reconcile.Request{}); err == nil {
		t.Fatal("Reconcile succeeded on a failed KubeVirt read")
	}
	if got := getClass(t, c, "l40s").Status; got.Free != nil || len(got.Conditions) != 0 {
		t.Errorf("status written from an unknown table: %+v", got)
	}
}

func TestStatusKeepsOtherConditionsAndTransitionTimes(t *testing.T) {
	old := metav1.NewTime(epoch)
	class := gpuClass("l40s", rnL40S, 0, func(c *cozyv1alpha1.GPUClass) {
		c.Status.Conditions = []metav1.Condition{
			{Type: cozyv1alpha1.GPUClassLicenseSecretMissing, Status: metav1.ConditionTrue, Reason: "SecretNotFound", LastTransitionTime: old},
			{Type: cozyv1alpha1.GPUClassResourceNameConflict, Status: metav1.ConditionFalse, Reason: reasonUniqueResourceName, LastTransitionTime: old},
		}
	})
	c := newClient(t, nil, class)
	runStatus(t, c, kubeVirtReader(c, nil, nil))

	conds := getClass(t, c, "l40s").Status.Conditions
	if lic := meta.FindStatusCondition(conds, cozyv1alpha1.GPUClassLicenseSecretMissing); lic == nil || lic.Status != metav1.ConditionTrue {
		t.Errorf("LicenseSecretMissing = %+v, want it left True", lic)
	}
	if cond := meta.FindStatusCondition(conds, cozyv1alpha1.GPUClassResourceNameConflict); cond == nil || !cond.LastTransitionTime.Equal(&old) {
		t.Errorf("ResourceNameConflict = %+v, want lastTransitionTime kept at %v", cond, old)
	}
}

func TestStatusSecondPassWritesNothing(t *testing.T) {
	updates := 0
	c := newClient(t, &interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, cl client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			updates++
			return cl.SubResource(sub).Update(ctx, obj, opts...)
		},
	}, gpuClass("l40s", rnL40S, 0), gpuNode("n1", map[string]int64{rnL40S: 2}))
	runStatus(t, c, kubeVirtReader(c, nil, nil))
	runStatus(t, c, kubeVirtReader(c, nil, nil))
	if updates != 1 {
		t.Errorf("status updates = %d, want 1", updates)
	}
}

func TestStatusRequeuesForTheKubeVirtTable(t *testing.T) {
	c := newClient(t, nil)
	if res := runStatus(t, c, kubeVirtReader(c, nil, nil)); res.RequeueAfter <= 0 {
		t.Errorf("RequeueAfter = %v, want a recheck of the unwatched KubeVirt table", res.RequeueAfter)
	}
}

func TestStatusCapacityMetrics(t *testing.T) {
	c := newClient(t, nil,
		gpuClass("l40s", rnL40S, 0),
		gpuNode("n1", map[string]int64{rnL40S: 4}),
		gpuNode("n2", map[string]int64{rnL40S: 2}, cordoned),
		gpuPod("vm-1", "n1", rnL40S, 1),
		gpuPod("vm-2", "n2", rnL40S, 2),
		gpuPod("pending", "", rnL40S, 4),
		gpuPod("done", "n1", rnL40S, 3, func(p *corev1.Pod) { p.Status.Phase = corev1.PodFailed }),
		gpuPod("other", "n1", rnH100, 5),
	)
	runStatus(t, c, kubeVirtReader(c, nil, nil))
	if got := testutil.ToFloat64(classCapacity.WithLabelValues("l40s")); got != 6 {
		t.Errorf("capacity = %v, want 6: allocatable on every node, used or not", got)
	}
	if got := testutil.ToFloat64(classUsed.WithLabelValues("l40s")); got != 3 {
		t.Errorf("used = %v, want 3: scheduled, unfinished pods on every node", got)
	}
	if got := getClass(t, c, "l40s").Status; got.Free == nil || *got.Free != 3 {
		t.Errorf("free = %v, want 3", got.Free)
	}

	if err := c.Delete(context.Background(), getClass(t, c, "l40s")); err != nil {
		t.Fatal(err)
	}
	runStatus(t, c, kubeVirtReader(c, nil, nil))
	if n := testutil.CollectAndCount(classCapacity) + testutil.CollectAndCount(classUsed); n != 0 {
		t.Errorf("metric series after the class went = %d, want 0", n)
	}
}

func TestNodeCapacityChanged(t *testing.T) {
	base := gpuNode("n1", map[string]int64{rnL40S: 4})
	tests := []struct {
		name   string
		mutate func(*corev1.Node)
		want   bool
	}{
		{"heartbeat only", func(n *corev1.Node) { n.Status.Conditions[0].LastHeartbeatTime = metav1.Now() }, false},
		{"label only", func(n *corev1.Node) { n.Labels = map[string]string{"x": "y"} }, false},
		{"cordon", cordoned, true},
		{"readiness", notReady, true},
		{"taint", tainted("maintenance"), true},
		{"allocatable", func(n *corev1.Node) { delete(n.Status.Allocatable, rnL40S) }, true},
	}
	for _, tc := range tests {
		n := base.DeepCopy()
		tc.mutate(n)
		if got := nodeCapacityChanged.Update(event.UpdateEvent{ObjectOld: base, ObjectNew: n}); got != tc.want {
			t.Errorf("%s: nodeCapacityChanged = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRequestsExtendedResource(t *testing.T) {
	kvm := gpuPod("launcher", "n1", "devices.kubevirt.io/kvm", 1)
	tests := []struct {
		name string
		pod  *corev1.Pod
		want bool
	}{
		{"GPU limit", gpuPod("vm", "n1", rnL40S, 1), true},
		{"GPU in an init container", gpuPod("init", "n1", rnL40S, 1, func(p *corev1.Pod) {
			p.Spec.InitContainers = p.Spec.Containers
			p.Spec.Containers = nil
		}), true},
		{"GPU request without a limit", gpuPod("req", "n1", "cpu", 1, func(p *corev1.Pod) {
			p.Spec.Containers[0].Resources = corev1.ResourceRequirements{Requests: corev1.ResourceList{rnL40S: p.Spec.Containers[0].Resources.Limits["cpu"]}}
		}), true},
		{"a class may name a devices.kubevirt.io resource", kvm, true},
		{"CPU only", gpuPod("web", "n1", "cpu", 1), false},
	}
	for _, tc := range tests {
		if got := requestsExtendedResource(tc.pod); got != tc.want {
			t.Errorf("%s: requestsExtendedResource = %v, want %v", tc.name, got, tc.want)
		}
	}
}
