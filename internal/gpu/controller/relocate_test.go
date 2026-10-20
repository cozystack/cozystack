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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu"
)

func TestUnattachedDeviceMovesOffACordonedNodeUnderMove(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}, cordoned),
		node("n2", map[string]int64{rnL40S: 2}),
		device("a", "l40s", 1, bound("n1")),
	)
	e.pass()
	expectPlacement(t, e.device("a"), gpuv1alpha1.GPUDeviceBound, "n2")
}

func TestCordonUnderMoveWithNoRoomKeepsTheDeviceWithACondition(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}, cordoned),
		device("a", "l40s", 1, bound("n1")),
	)
	e.pass()
	d := e.device("a")
	expectPlacement(t, d, gpuv1alpha1.GPUDeviceBound, "n1")
	if c := waiting(d); c == nil || c.Reason != ReasonCordoned {
		t.Fatalf("want a Cordoned condition, got %+v", c)
	}

	n := node("n1", map[string]int64{rnL40S: 2})
	_ = e.c.Get(t.Context(), key("", "n1"), n)
	n.Spec.Unschedulable = false
	e.update(n)
	e.pass()
	if c := waiting(e.device("a")); c != nil {
		t.Fatalf("condition kept after the uncordon: %+v", c)
	}
}

func TestStayClassKeepsItsNodeOnACordon(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S, stay),
		node("n1", map[string]int64{rnL40S: 2}, cordoned),
		node("n2", map[string]int64{rnL40S: 2}),
		device("a", "l40s", 1, bound("n1")),
	)
	e.pass()
	expectPlacement(t, e.device("a"), gpuv1alpha1.GPUDeviceBound, "n1")
}

func TestMixedSetWithAStayDeviceOnACordonedNodeStays(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S, stay),
		class("h100", rnH100),
		node("n1", map[string]int64{rnL40S: 2, rnH100: 2}, cordoned),
		node("n2", map[string]int64{rnL40S: 2, rnH100: 2}),
		device("a", "l40s", 1, bound("n1")),
		device("b", "h100", 1, bound("n1")),
		vmObj("vm", "Always", "a", "b"),
	)
	e.pass()
	expectPlacement(t, e.device("a"), gpuv1alpha1.GPUDeviceBound, "n1")
	expectPlacement(t, e.device("b"), gpuv1alpha1.GPUDeviceBound, "n1")
}

func TestSetMovesTogetherOffADeletedNode(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("small", map[string]int64{rnL40S: 1}),
		node("big", map[string]int64{rnL40S: 2}),
		device("a", "l40s", 1, bound("gone")),
		device("b", "l40s", 1, bound("gone")),
		vmObj("vm", "Always", "a", "b"),
	)
	e.pass()
	expectPlacement(t, e.device("a"), gpuv1alpha1.GPUDeviceBound, "big")
	expectPlacement(t, e.device("b"), gpuv1alpha1.GPUDeviceBound, "big")
}

func TestSetGoesToLostTogetherWhenNoNodeHasRoomForAll(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 1}),
		node("n2", map[string]int64{rnL40S: 1}),
		device("a", "l40s", 1, bound("gone")),
		device("b", "l40s", 1, bound("gone")),
		vmObj("vm", "Always", "a", "b"),
	)
	e.pass()
	expectPlacement(t, e.device("a"), gpuv1alpha1.GPUDeviceLost, "gone")
	expectPlacement(t, e.device("b"), gpuv1alpha1.GPUDeviceLost, "gone")

	e.create(node("n3", map[string]int64{rnL40S: 2}))
	e.pass()
	expectPlacement(t, e.device("a"), gpuv1alpha1.GPUDeviceBound, "n3")
	expectPlacement(t, e.device("b"), gpuv1alpha1.GPUDeviceBound, "n3")
}

func TestAShortNotReadyMovesNothing(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}, notReadySince(t0.Add(-time.Minute))),
		node("n2", map[string]int64{rnL40S: 2}),
		device("a", "l40s", 1, bound("n1")),
		device("b", "l40s", 1, bound("n1"), attached("vm1")),
		vmiObj("vm1", "Running", "n1", ""),
	)
	e.pass()
	expectPlacement(t, e.device("a"), gpuv1alpha1.GPUDeviceBound, "n1")
	expectPlacement(t, e.device("b"), gpuv1alpha1.GPUDeviceAttached, "n1")
	if c := meta.FindStatusCondition(e.class("l40s").Status.Conditions, gpu.ConditionLostCapacity); c == nil || c.Status != metav1.ConditionFalse {
		t.Fatalf("LostCapacity before the grace period: %+v", c)
	}
}

func TestNotReadyPastGraceLosesAttachedAndMovesUnattached(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}, notReadySince(t0.Add(-10*time.Minute))),
		node("n2", map[string]int64{rnL40S: 2}),
		device("a", "l40s", 1, bound("n1")),
		device("b", "l40s", 1, bound("n1"), attached("vm1")),
		vmiObj("vm1", "Running", "n1", ""),
	)
	e.pass()
	expectPlacement(t, e.device("a"), gpuv1alpha1.GPUDeviceBound, "n2")
	expectPlacement(t, e.device("b"), gpuv1alpha1.GPUDeviceLost, "n1")
	c := meta.FindStatusCondition(e.class("l40s").Status.Conditions, gpu.ConditionLostCapacity)
	if c == nil || c.Status != metav1.ConditionTrue {
		t.Fatalf("want LostCapacity on the class, got %+v", c)
	}

	// The node returns while the VMI still runs: the device is Attached there again.
	n := node("n1", nil)
	_ = e.c.Get(t.Context(), key("", "n1"), n)
	n.Status.Conditions[0].Status = "True"
	e.updateStatus(n)
	e.pass()
	expectPlacement(t, e.device("b"), gpuv1alpha1.GPUDeviceAttached, "n1")
}

func TestLostAttachedDeviceIsReboundOnceItsVMIIsFinal(t *testing.T) {
	v := vmiObj("vm1", "Running", "n1", "")
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}, notReadySince(t0.Add(-10*time.Minute))),
		node("n2", map[string]int64{rnL40S: 2}),
		device("b", "l40s", 1, lost("n1"), attached("vm1")),
		v,
	)
	e.pass()
	expectPlacement(t, e.device("b"), gpuv1alpha1.GPUDeviceLost, "n1")

	_ = unstructured.SetNestedField(v.Object, "Failed", "status", "phase")
	e.update(v)
	e.pass()
	d := e.device("b")
	expectPlacement(t, d, gpuv1alpha1.GPUDeviceBound, "n2")
	if d.Status.AttachedTo != nil {
		t.Fatalf("attachment kept: %+v", d.Status.AttachedTo)
	}
}

func TestLostCapacityDeletesTheUnscheduledVMIWhateverTheRunStrategy(t *testing.T) {
	for _, strategy := range []string{"Always", "Manual"} {
		t.Run(strategy, func(t *testing.T) {
			v := vmiObj("vm1", "Pending", "", "vm")
			e := newEnv(t,
				class("l40s", rnL40S),
				node("n1", map[string]int64{rnL40S: 2}, notReadySince(t0.Add(-10*time.Minute))),
				node("n2", map[string]int64{rnL40S: 2}),
				device("a", "l40s", 1, bound("n1"), attached("vm1")),
				vmObj("vm", strategy, "a"),
				v,
			)
			e.pass()
			if !e.gone(v) {
				t.Fatal("the unscheduled VMI on a node that lost capacity was kept")
			}
			expectPlacement(t, e.device("a"), gpuv1alpha1.GPUDeviceBound, "n2")
		})
	}
}

func TestCordonDeletesTheUnscheduledVMIOnlyForAVMThatRestartsIt(t *testing.T) {
	for strategy, wantDeleted := range map[string]bool{"Always": true, "Once": true, "Manual": false, "RerunOnFailure": false} {
		t.Run(strategy, func(t *testing.T) {
			v := vmiObj("vm1", "Scheduling", "", "vm")
			e := newEnv(t,
				class("l40s", rnL40S),
				node("n1", map[string]int64{rnL40S: 2}, cordoned),
				node("n2", map[string]int64{rnL40S: 2}),
				device("a", "l40s", 1, bound("n1"), attached("vm1")),
				vmObj("vm", strategy, "a"),
				v,
			)
			e.pass()
			if got := e.gone(v); got != wantDeleted {
				t.Fatalf("VMI deleted = %v, want %v", got, wantDeleted)
			}
			if wantDeleted {
				expectPlacement(t, e.device("a"), gpuv1alpha1.GPUDeviceBound, "n2")
			} else {
				expectPlacement(t, e.device("a"), gpuv1alpha1.GPUDeviceAttached, "n1")
			}
		})
	}
}

func TestCordonKeepsTheUnscheduledVMIWhenNoNodeHasRoom(t *testing.T) {
	v := vmiObj("vm1", "Scheduling", "", "vm")
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}, cordoned),
		device("a", "l40s", 1, bound("n1"), attached("vm1")),
		vmObj("vm", "Always", "a"),
		v,
	)
	e.pass()
	if e.gone(v) {
		t.Fatal("the VMI was deleted with nowhere for its device to go")
	}
}

func TestUnhealthyDevicesPastGraceMoveTheExcessUnattachedDevices(t *testing.T) {
	gn := &gpuv1alpha1.GPUNode{}
	gn.Name = "n1"
	gn.Status.ResourceNames = []string{rnL40S}
	gn.Status.Devices = []gpuv1alpha1.GPUNodeDevice{
		{PCIAddress: "0000:41:00.0", Function: gpuv1alpha1.PCIFunctionWhole, ResourceName: rnL40S, Healthy: ptr.To(true)},
		{PCIAddress: "0000:42:00.0", Function: gpuv1alpha1.PCIFunctionWhole, ResourceName: rnL40S, Healthy: ptr.To(false)},
	}
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		node("n2", map[string]int64{rnL40S: 2}),
		gn,
		device("running", "l40s", 1, bound("n1"), attached("vm1")),
		vmiObj("vm1", "Running", "n1", ""),
		device("idle", "l40s", 1, bound("n1")),
	)
	e.pass()
	expectPlacement(t, e.device("idle"), gpuv1alpha1.GPUDeviceBound, "n1")

	e.now = t0.Add(6 * time.Minute)
	// One pass: once the excess device has moved, the node serves what it
	// holds and the condition clears.
	if _, err := e.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: passKey}); err != nil {
		t.Fatalf("pass: %v", err)
	}
	got := &gpuv1alpha1.GPUNode{}
	_ = e.c.Get(t.Context(), key("", "n1"), got)
	if c := meta.FindStatusCondition(got.Status.Conditions, gpu.ConditionLostCapacity); c == nil || c.Status != metav1.ConditionTrue {
		t.Fatalf("want LostCapacity on the GPUNode, got %+v", c)
	}
	if len(got.Status.Devices) != 2 {
		t.Fatal("the condition patch dropped the agent's inventory")
	}
	e.pass()
	expectPlacement(t, e.device("idle"), gpuv1alpha1.GPUDeviceBound, "n2")
	expectPlacement(t, e.device("running"), gpuv1alpha1.GPUDeviceAttached, "n1")
	_ = e.c.Get(t.Context(), key("", "n1"), got)
	if c := meta.FindStatusCondition(got.Status.Conditions, gpu.ConditionLostCapacity); c == nil || c.Status != metav1.ConditionFalse {
		t.Fatalf("LostCapacity kept on the GPUNode once it serves what it holds: %+v", c)
	}
	if s := lostCapacity(e, "l40s"); s != metav1.ConditionFalse {
		t.Fatalf("LostCapacity %q kept on the class once the node serves what it holds", s)
	}
}

func TestNodeSelectorChangeMovesUnattachedDevices(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S, selecting("pool", "b")),
		node("n1", map[string]int64{rnL40S: 2}, labelled("pool", "a")),
		node("n2", map[string]int64{rnL40S: 2}, labelled("pool", "b")),
		device("a", "l40s", 1, bound("n1")),
	)
	e.pass()
	expectPlacement(t, e.device("a"), gpuv1alpha1.GPUDeviceBound, "n2")
}

func TestASplitSetGoesToLostTogetherAndSaysWhy(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 1}),
		device("a", "l40s", 1, bound("n1")),
		device("b", "l40s", 1, bound("gone")),
		vmObj("vm", "Always", "a", "b"),
	)
	e.pass()
	a, b := e.device("a"), e.device("b")
	expectPlacement(t, a, gpuv1alpha1.GPUDeviceLost, "n1")
	expectPlacement(t, b, gpuv1alpha1.GPUDeviceLost, "gone")
	if c := waiting(b); c == nil || !strings.Contains(c.Message, "node gone is gone") {
		t.Fatalf("the condition must say why the device left: %+v", c)
	}
	if c := waiting(a); c == nil || !strings.Contains(c.Message, "another device of its set") {
		t.Fatalf("the condition must say why a healthy member left: %+v", c)
	}

	e.create(node("n2", map[string]int64{rnL40S: 2}))
	e.pass()
	expectPlacement(t, e.device("a"), gpuv1alpha1.GPUDeviceBound, "n2")
	expectPlacement(t, e.device("b"), gpuv1alpha1.GPUDeviceBound, "n2")
}

func TestExcessDeviceOfASetWithALostMemberJoinsItInLost(t *testing.T) {
	gn := &gpuv1alpha1.GPUNode{}
	gn.Name = "n1"
	gn.Status.ResourceNames = []string{rnL40S}
	gn.Status.Devices = []gpuv1alpha1.GPUNodeDevice{
		{PCIAddress: "0000:41:00.0", Function: gpuv1alpha1.PCIFunctionWhole, ResourceName: rnL40S, Healthy: ptr.To(true)},
		{PCIAddress: "0000:42:00.0", Function: gpuv1alpha1.PCIFunctionWhole, ResourceName: rnL40S, Healthy: ptr.To(false)},
	}
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		gn,
		device("running", "l40s", 1, bound("n1"), attached("vm1")),
		vmiObj("vm1", "Running", "n1", ""),
		device("a", "l40s", 1, lost("gone")),
		device("b", "l40s", 1, bound("n1")),
		vmObj("vm", "Always", "a", "b"),
	)
	e.pass()
	e.now = t0.Add(6 * time.Minute)
	e.pass()
	expectPlacement(t, e.device("b"), gpuv1alpha1.GPUDeviceLost, "n1")
	expectPlacement(t, e.device("running"), gpuv1alpha1.GPUDeviceAttached, "n1")
}

func unhealthyOneOfTwo(name string) *gpuv1alpha1.GPUNode {
	gn := &gpuv1alpha1.GPUNode{}
	gn.Name = name
	gn.Status.ResourceNames = []string{rnL40S}
	gn.Status.Devices = []gpuv1alpha1.GPUNodeDevice{
		{PCIAddress: "0000:41:00.0", Function: gpuv1alpha1.PCIFunctionWhole, ResourceName: rnL40S, Healthy: ptr.To(true)},
		{PCIAddress: "0000:42:00.0", Function: gpuv1alpha1.PCIFunctionWhole, ResourceName: rnL40S, Healthy: ptr.To(false)},
	}
	return gn
}

// A node that lost part of its GPUs still serves the units it has left, so
// an unscheduled VMI whose device fits there is not deleted; one whose
// device is in excess is.
func TestPartialLossDeletesOnlyTheUnscheduledVMIOfAnExcessDevice(t *testing.T) {
	for name, extra := range map[string]bool{"fits": false, "excess": true} {
		t.Run(name, func(t *testing.T) {
			v := vmiObj("vm1", "Pending", "", "vm")
			objs := []client.Object{
				class("l40s", rnL40S),
				node("n1", map[string]int64{rnL40S: 2}),
				node("n2", map[string]int64{rnL40S: 2}),
				unhealthyOneOfTwo("n1"),
				device("a", "l40s", 1, bound("n1"), attached("vm1")),
				vmObj("vm", "Manual", "a"),
				v,
			}
			if extra {
				objs = append(objs, device("running", "l40s", 1, bound("n1"), attached("vm2")), vmiObj("vm2", "Running", "n1", ""))
			}
			e := newEnv(t, objs...)
			e.pass()
			e.now = t0.Add(6 * time.Minute)
			e.pass()
			if got := e.gone(v); got != extra {
				t.Fatalf("VMI deleted = %v, want %v", got, extra)
			}
		})
	}
}

func TestCordonConditionNamesTheCordonedNode(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 1}, cordoned),
		node("n2", map[string]int64{rnL40S: 1}),
		device("a", "l40s", 1, bound("n1")),
		device("b", "l40s", 1, bound("n2")),
		vmObj("vm", "Always", "a", "b"),
	)
	e.pass()
	if c := waiting(e.device("b")); c == nil || !strings.Contains(c.Message, "node n1 of another device") || strings.Contains(c.Message, "node n2 is cordoned") {
		t.Fatalf("condition on the member of the healthy node: %+v", c)
	}
	if c := waiting(e.device("a")); c == nil || !strings.Contains(c.Message, "node n1 is cordoned") {
		t.Fatalf("condition on the member of the cordoned node: %+v", c)
	}
}

// The VMI comes from the cache; one scheduled since the cache saw it is not
// deleted on the stale view.
func TestUnscheduledVMIDeletionIsPreconditionedOnTheReadVersion(t *testing.T) {
	v := vmiObj("vm1", "Scheduling", "", "vm")
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}, cordoned),
		node("n2", map[string]int64{rnL40S: 2}),
		device("a", "l40s", 1, bound("n1"), attached("vm1")),
		vmObj("vm", "Always", "a"),
		v,
	)
	stale := v.DeepCopy()
	_ = e.c.Get(t.Context(), client.ObjectKeyFromObject(v), stale)
	e.r.Cache = fake.NewClientBuilder().WithScheme(testScheme(true)).WithObjects(stale.DeepCopy(), vmObj("vm", "Always", "a")).Build()
	_ = e.c.Get(t.Context(), client.ObjectKeyFromObject(v), v)
	_ = unstructured.SetNestedField(v.Object, "n1", "status", "nodeName")
	e.update(v)
	if _, err := e.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: passKey}); err != nil {
		t.Fatalf("pass: %v", err)
	}
	if e.gone(v) {
		t.Fatal("a VMI scheduled since the cache read was deleted")
	}
}

// Capacity lost for another resource on the node says nothing about this
// device's GPUs, so it comes back to Attached when the node returns.
func TestLostDeviceReturnsToAttachedDespiteLossOfAnotherResource(t *testing.T) {
	gn := &gpuv1alpha1.GPUNode{}
	gn.Name = "n1"
	gn.Status.ResourceNames = []string{"example.com/other"}
	gn.Status.Devices = []gpuv1alpha1.GPUNodeDevice{
		{PCIAddress: "0000:43:00.0", Function: gpuv1alpha1.PCIFunctionWhole, ResourceName: "example.com/other", Healthy: ptr.To(false)},
	}
	e := newEnv(t,
		class("l40s", rnL40S),
		class("other", "example.com/other"),
		node("n1", map[string]int64{rnL40S: 2, "example.com/other": 1}),
		gn,
		// Holds the other resource, which the node no longer serves, and
		// keeps running, so the node stays lost for it.
		device("o", "other", 1, bound("n1"), attached("vm0")),
		vmiObj("vm0", "Running", "n1", ""),
	)
	e.pass()
	e.now = t0.Add(6 * time.Minute)
	e.pass()
	if s := lostCapacity(e, "other"); s != metav1.ConditionTrue {
		t.Fatalf("the other resource is not lost: %q", s)
	}
	e.create(device("b", "l40s", 1, lost("n1"), attached("vm1")))
	e.create(vmiObj("vm1", "Running", "n1", ""))
	e.pass()
	expectPlacement(t, e.device("b"), gpuv1alpha1.GPUDeviceAttached, "n1")
}

func mdevParent(name string, allocatable int32, healthy bool) *gpuv1alpha1.GPUNode {
	gn := &gpuv1alpha1.GPUNode{}
	gn.Name = name
	gn.Status.ResourceNames = []string{rnL40S}
	gn.Status.Devices = []gpuv1alpha1.GPUNodeDevice{{PCIAddress: "0000:41:00.0", Function: gpuv1alpha1.PCIFunctionWhole,
		ResourceName: rnL40S, Healthy: ptr.To(healthy), Allocatable: ptr.To(allocatable)}}
	return gn
}

// One mdev parent function serves several plugin devices, so the healthy
// capacity of a resource is the sum of allocatable, not one per function.
func TestAnMdevParentServingThreeHoldsTwoWithoutLosingCapacity(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 3}),
		node("n2", map[string]int64{rnL40S: 3}),
		mdevParent("n1", 3, true),
		device("a", "l40s", 1, bound("n1")),
		device("b", "l40s", 1, bound("n1")),
	)
	e.pass()
	e.now = t0.Add(6 * time.Minute)
	e.pass()
	expectPlacement(t, e.device("a"), gpuv1alpha1.GPUDeviceBound, "n1")
	expectPlacement(t, e.device("b"), gpuv1alpha1.GPUDeviceBound, "n1")
	if c := meta.FindStatusCondition(e.class("l40s").Status.Conditions, gpu.ConditionLostCapacity); c == nil || c.Status != metav1.ConditionFalse {
		t.Fatalf("LostCapacity on a parent that serves both devices: %+v", c)
	}
}

func TestAnMdevParentThatDropsBelowItsHeldDevicesLosesCapacityAfterTheGrace(t *testing.T) {
	gn := mdevParent("n1", 1, true)
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 3}),
		node("n2", map[string]int64{rnL40S: 3}),
		gn,
		device("a", "l40s", 1, bound("n1")),
		device("b", "l40s", 1, bound("n1")),
	)
	e.pass()
	expectPlacement(t, e.device("b"), gpuv1alpha1.GPUDeviceBound, "n1")
	e.now = t0.Add(6 * time.Minute)
	// One pass: once the excess device has moved, the parent serves what it
	// holds and the condition clears.
	if _, err := e.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: passKey}); err != nil {
		t.Fatalf("pass: %v", err)
	}
	if c := meta.FindStatusCondition(e.class("l40s").Status.Conditions, gpu.ConditionLostCapacity); c == nil || c.Status != metav1.ConditionTrue {
		t.Fatalf("no LostCapacity after the grace: %+v", c)
	}
	moved := 0
	for _, name := range []string{"a", "b"} {
		if e.device(name).Status.NodeName == "n2" {
			moved++
		}
	}
	if moved != 1 {
		t.Fatalf("%d devices moved, want the one the parent no longer serves", moved)
	}
}

// A NotReady spell or a taint on the Stay device's cordoned node keeps the
// pin, exactly as a plain cordon does.
func TestAStayPinOnACordonedNodeHoldsThroughNotReadyAndTaints(t *testing.T) {
	for name, extra := range map[string][]nodeOpt{
		"cordon only":       nil,
		"cordon + taint":    {tainted("dedicated", corev1.TaintEffectNoSchedule)},
		"cordon + notready": {notReadySince(t0.Add(-time.Minute))},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t,
				class("l40s", rnL40S, stay),
				class("h100", rnH100),
				node("n1", map[string]int64{rnL40S: 1}, append([]nodeOpt{cordoned}, extra...)...),
				node("n2", map[string]int64{rnH100: 1}, cordoned),
				node("n3", map[string]int64{rnL40S: 1, rnH100: 1}),
				device("a", "l40s", 1, bound("n1")),
				device("b", "h100", 1, bound("n2")),
				vmObj("vm", "Always", "a", "b"),
			)
			e.pass()
			expectPlacement(t, e.device("a"), gpuv1alpha1.GPUDeviceBound, "n1")
			expectPlacement(t, e.device("b"), gpuv1alpha1.GPUDeviceBound, "n2")
		})
	}
}

// The agent's report as internal/gpu/agent builds it from kubelet's
// pod-resources API.
func agentReport(name string, names []string, devices ...gpuv1alpha1.GPUNodeDevice) *gpuv1alpha1.GPUNode {
	gn := &gpuv1alpha1.GPUNode{}
	gn.Name = name
	gn.Status.ResourceNames = names
	gn.Status.Devices = devices
	return gn
}

func lostCapacity(e *env, class string) metav1.ConditionStatus {
	c := meta.FindStatusCondition(e.class(class).Status.Conditions, gpu.ConditionLostCapacity)
	if c == nil {
		return ""
	}
	return c.Status
}

// A name whose devices the agent sees only by NVML UUID maps to no PCI
// function, so the agent says nothing about its capacity. The kubelet's
// allocatable count is then the only witness: it is what the binder placed
// against, and judging such a name lost would move the device away, rebind
// it once nothing is held, and lose it again every grace period.
func TestANameTheAgentCannotMapIsJudgedByKubeletAllocatable(t *testing.T) {
	e := newEnv(t,
		class("gpu", "nvidia.com/gpu"),
		node("n1", map[string]int64{"nvidia.com/gpu": 2}),
		node("n2", map[string]int64{"nvidia.com/gpu": 2}),
		agentReport("n1", []string{"nvidia.com/gpu"},
			gpuv1alpha1.GPUNodeDevice{PCIAddress: "0000:41:00.0", Function: gpuv1alpha1.PCIFunctionWhole}),
		device("a", "gpu", 1, bound("n1")),
	)
	e.pass()
	e.now = t0.Add(6 * time.Minute)
	e.pass()
	expectPlacement(t, e.device("a"), gpuv1alpha1.GPUDeviceBound, "n1")
	if s := lostCapacity(e, "gpu"); s != metav1.ConditionFalse {
		t.Fatalf("LostCapacity %q for a name kubelet still serves", s)
	}
}

// When the last GPU of a name turns unhealthy with no pod on it, kubelet
// drops the name from allocatable and the agent from its report; a held
// unit there is lost all the same.
func TestAHeldNameThatLeftTheReportIsLostAfterTheGrace(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 0}),
		node("n2", map[string]int64{rnL40S: 1}),
		agentReport("n1", nil,
			gpuv1alpha1.GPUNodeDevice{PCIAddress: "0000:41:00.0", Function: gpuv1alpha1.PCIFunctionWhole}),
		device("a", "l40s", 1, bound("n1")),
	)
	e.pass()
	expectPlacement(t, e.device("a"), gpuv1alpha1.GPUDeviceBound, "n1")
	e.now = t0.Add(6 * time.Minute)
	e.pass()
	expectPlacement(t, e.device("a"), gpuv1alpha1.GPUDeviceBound, "n2")
}

// The agent marks an mdev parent unhealthy when a pod holds one slot that
// went bad and counts only the slots kubelet still lists as allocatable:
// three slots with one bad report 2, which serve the 2 held exactly.
func TestAnUnhealthyMdevParentThatStillServesItsHeldSlotsIsNotLost(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 3}),
		node("n2", map[string]int64{rnL40S: 3}),
		agentReport("n1", []string{rnL40S}, gpuv1alpha1.GPUNodeDevice{PCIAddress: "0000:41:00.0",
			Function: gpuv1alpha1.PCIFunctionWhole, ResourceName: rnL40S, Healthy: ptr.To(false), Allocatable: ptr.To[int32](2)}),
		device("a", "l40s", 1, bound("n1")),
		device("b", "l40s", 1, bound("n1")),
	)
	e.pass()
	e.now = t0.Add(6 * time.Minute)
	e.pass()
	expectPlacement(t, e.device("a"), gpuv1alpha1.GPUDeviceBound, "n1")
	expectPlacement(t, e.device("b"), gpuv1alpha1.GPUDeviceBound, "n1")
	if s := lostCapacity(e, "l40s"); s != metav1.ConditionFalse {
		t.Fatalf("LostCapacity %q for a parent that serves its held slots", s)
	}
}

// Passthrough GPUs are one function each; a name's units are the sum over
// its functions.
func TestTwoWholeGPUsOfOneNameServeTwoHeldDevices(t *testing.T) {
	whole := func(addr string) gpuv1alpha1.GPUNodeDevice {
		return gpuv1alpha1.GPUNodeDevice{PCIAddress: addr, Function: gpuv1alpha1.PCIFunctionWhole,
			ResourceName: rnL40S, Healthy: ptr.To(true), Allocatable: ptr.To[int32](1)}
	}
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		node("n2", map[string]int64{rnL40S: 2}),
		agentReport("n1", []string{rnL40S}, whole("0000:41:00.0"), whole("0000:42:00.0")),
		device("a", "l40s", 1, bound("n1")),
		device("b", "l40s", 1, bound("n1")),
	)
	e.pass()
	e.now = t0.Add(6 * time.Minute)
	e.pass()
	expectPlacement(t, e.device("a"), gpuv1alpha1.GPUDeviceBound, "n1")
	expectPlacement(t, e.device("b"), gpuv1alpha1.GPUDeviceBound, "n1")
	if s := lostCapacity(e, "l40s"); s != metav1.ConditionFalse {
		t.Fatalf("LostCapacity %q on a node that serves both", s)
	}
}

// A plugin that names a device by IOMMU group reports it on every GPU
// function of the group, so the agent's per-function counts can add up to
// more than kubelet serves; kubelet's allocatable count caps them.
func TestOnePluginDeviceOverAGroupOfFunctionsIsCountedOnce(t *testing.T) {
	inGroup := func(addr string) gpuv1alpha1.GPUNodeDevice {
		return gpuv1alpha1.GPUNodeDevice{PCIAddress: addr, Function: gpuv1alpha1.PCIFunctionWhole,
			ResourceName: rnL40S, Healthy: ptr.To(true), Allocatable: ptr.To[int32](1)}
	}
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 1}),
		node("n2", map[string]int64{rnL40S: 2}),
		agentReport("n1", []string{rnL40S}, inGroup("0000:41:00.0"), inGroup("0000:42:00.0")),
		device("a", "l40s", 1, bound("n1")),
		device("b", "l40s", 1, bound("n1")),
	)
	e.pass()
	e.now = t0.Add(6 * time.Minute)
	e.pass()
	moved := 0
	for _, name := range []string{"a", "b"} {
		if e.device(name).Status.NodeName == "n2" {
			moved++
		}
	}
	if moved != 1 {
		t.Fatalf("%d devices moved, want the one kubelet no longer serves", moved)
	}
}
