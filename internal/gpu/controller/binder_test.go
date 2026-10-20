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
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu/model"
)

func TestOrderBindsOnTheFullestNodeWithRoom(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 4}),
		node("n2", map[string]int64{rnL40S: 4}),
		device("held", "l40s", 2, bound("n2")),
		device("order", "l40s", 1),
	)
	e.pass()
	expectPlacement(t, e.device("order"), gpuv1alpha1.GPUDeviceBound, "n2")
	if e.device("order").Status.BoundAt == nil {
		t.Fatal("boundAt not set")
	}
}

func TestCapacityLeavesOutUnitsRawNamePodsHold(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		launcherPod("virt-launcher-raw", "raw-vm", "n1", map[string]int64{rnL40S: 2}),
		device("order", "l40s", 1),
	)
	e.pass()
	d := e.device("order")
	expectPlacement(t, d, gpuv1alpha1.GPUDevicePending, "")
	if c := waiting(d); c == nil || c.Reason != ReasonCapacity {
		t.Fatalf("want a Capacity condition, got %+v", c)
	}
}

func TestPodOfAnAttachedDeviceIsNotCountedTwice(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		device("running", "l40s", 1, bound("n1"), attached("vm1")),
		vmiObj("vm1", "Running", "n1", ""),
		launcherPod("virt-launcher-vm1", "vm1", "n1", map[string]int64{rnL40S: 1}),
		device("order", "l40s", 1),
	)
	e.pass()
	expectPlacement(t, e.device("order"), gpuv1alpha1.GPUDeviceBound, "n1")
}

func TestConcurrentOrdersForTheLastUnitBindOnlyOne(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 1}),
		device("first", "l40s", 1),
		device("second", "l40s", 1),
	)
	e.pass()
	expectPlacement(t, e.device("first"), gpuv1alpha1.GPUDeviceBound, "n1")
	expectPlacement(t, e.device("second"), gpuv1alpha1.GPUDevicePending, "")
}

func TestOrderOverABudgetWaitsWithTheRefusal(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 4}),
		tenant("acme", map[string]string{"gpu.cozystack.io/total": "1"}),
		device("held", "l40s", 1, bound("n1")),
		device("order", "l40s", 1),
	)
	e.pass()
	d := e.device("order")
	expectPlacement(t, d, gpuv1alpha1.GPUDevicePending, "")
	c := waiting(d)
	if c == nil || c.Reason != string(model.ReasonBudget) || !strings.Contains(c.Message, "gpu.cozystack.io/total") {
		t.Fatalf("want a Budget condition naming the key, got %+v", c)
	}
}

func TestVGPUOrderNeedsTheEntitlement(t *testing.T) {
	e := newEnv(t,
		class("l40s-24c", rnL40S, vgpu),
		node("n1", map[string]int64{rnL40S: 4}),
		namespace(ns, false),
		device("order", "l40s-24c", 1),
	)
	e.pass()
	if c := waiting(e.device("order")); c == nil || c.Reason != string(model.ReasonNotEntitled) {
		t.Fatalf("want NotEntitled, got %+v", c)
	}

	n := namespace(ns, true)
	e.update(n)
	e.pass()
	expectPlacement(t, e.device("order"), gpuv1alpha1.GPUDeviceBound, "n1")
	if c := waiting(e.device("order")); c != nil {
		t.Fatalf("condition kept after binding: %+v", c)
	}
}

func TestOrderOfADeletedClassIsNeverBound(t *testing.T) {
	c := class("l40s", rnL40S)
	e := newEnv(t, c, node("n1", map[string]int64{rnL40S: 4}), device("held", "l40s", 1, bound("n1")))
	e.delete(c)
	e.create(device("order", "l40s", 1))
	e.pass()
	d := e.device("order")
	expectPlacement(t, d, gpuv1alpha1.GPUDevicePending, "")
	if w := waiting(d); w == nil || w.Reason != ReasonClassDeleted {
		t.Fatalf("want ClassDeleted, got %+v", w)
	}
}

func TestTaintedNodeGetsNoBindsAndLeavesFree(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("tainted", map[string]int64{rnL40S: 4}, tainted("dedicated", corev1.TaintEffectNoSchedule)),
		node("n2", map[string]int64{rnL40S: 1}),
		device("order", "l40s", 2),
	)
	e.pass()
	expectPlacement(t, e.device("order"), gpuv1alpha1.GPUDevicePending, "")
	if free := e.class("l40s").Status.Free; free == nil || *free != 1 {
		t.Fatalf("free = %v, want 1: the tainted node's units do not count", free)
	}
}

func TestFenceAgentsTaintIsNotACordon(t *testing.T) {
	far := "remediation.medik8s.io/fence-agents-remediation"
	e := newEnv(t,
		class("l40s", rnL40S),
		node("fenced", map[string]int64{rnL40S: 4}, tainted(far, corev1.TaintEffectNoSchedule)),
		node("n2", map[string]int64{rnL40S: 4}),
		device("held", "l40s", 1, bound("fenced")),
	)
	e.pass()
	d := e.device("held")
	expectPlacement(t, d, gpuv1alpha1.GPUDeviceBound, "fenced")
	if c := waiting(d); c != nil {
		t.Fatalf("a taint must not trigger onCordon: %+v", c)
	}
}

func TestLostSetsRebindBeforeNewOrdersWithoutBlockingSmallerOnes(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		device("big", "l40s", 4, lost("dead")),
		device("a", "l40s", 1, lost("dead")),
		device("b", "l40s", 1, lost("dead")),
		vmObj("vm-ab", "Always", "a", "b"),
		device("order", "l40s", 1),
	)
	e.pass()
	expectPlacement(t, e.device("a"), gpuv1alpha1.GPUDeviceBound, "n1")
	expectPlacement(t, e.device("b"), gpuv1alpha1.GPUDeviceBound, "n1")
	expectPlacement(t, e.device("big"), gpuv1alpha1.GPUDeviceLost, "dead")
	expectPlacement(t, e.device("order"), gpuv1alpha1.GPUDevicePending, "")
}

func TestLostRebindSkipsBudgets(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		tenant("acme", map[string]string{"gpu.cozystack.io/total": "1"}),
		device("a", "l40s", 1, lost("dead")),
	)
	e.pass()
	expectPlacement(t, e.device("a"), gpuv1alpha1.GPUDeviceBound, "n1")
}

func TestRawKeyOnAnAncestorCapsDevicesTogetherWithRawNameVMs(t *testing.T) {
	child := "tenant-acme-dev"
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 4}),
		tenant("acme", map[string]string{rnL40S: "2", "gpu.cozystack.io/l40s": "4"}),
		launcherPod("virt-launcher-raw", "raw-vm", "n1", map[string]int64{rnL40S: 1}),
		device("first", "l40s", 1, inNamespace(child)),
		device("second", "l40s", 1, inNamespace(child)),
	)
	e.pass()
	d := &gpuv1alpha1.GPUDevice{}
	_ = e.c.Get(t.Context(), key(child, "first"), d)
	expectPlacement(t, d, gpuv1alpha1.GPUDeviceBound, "n1")
	_ = e.c.Get(t.Context(), key(child, "second"), d)
	expectPlacement(t, d, gpuv1alpha1.GPUDevicePending, "")
}

// The second Pod entry buildSnapshot emits for a Lost device's VMI carries
// an empty namespace so it counts for node capacity only; that relies on
// no tenant subtree containing the empty namespace.
func TestNoTenantSubtreeContainsTheEmptyNamespace(t *testing.T) {
	for _, root := range []string{"tenant-root", "tenant-acme", "tenant-acme-dev", "default", ""} {
		if model.InSubtree("", root) {
			t.Fatalf("InSubtree(\"\", %q) is true: capacity-only pod usage would count against budgets", root)
		}
	}
}

// A Lost device counts against budgets itself, so the pod of its still
// running VMI must not count again under a raw key. Node capacity still
// counts that pod: TestRequestPassCountsThePodOfALostDevicesRunningVMI.
func TestLostDevicesPodIsNotCountedTwiceAgainstARawKey(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 1}, notReadySince(t0.Add(-time.Hour))),
		node("n2", map[string]int64{rnL40S: 1}),
		tenant("acme", map[string]string{rnL40S: "2", "gpu.cozystack.io/l40s": "4"}),
		device("b", "l40s", 1, lost("n1"), attached("vm1")),
		vmiObj("vm1", "Running", "n1", ""),
		launcherPod("virt-launcher-vm1", "vm1", "n1", map[string]int64{rnL40S: 1}),
		device("order", "l40s", 1),
	)
	e.pass()
	expectPlacement(t, e.device("order"), gpuv1alpha1.GPUDeviceBound, "n2")
}
