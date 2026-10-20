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
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
)

func expectResult(t *testing.T, d *gpuv1alpha1.GPUDevice, want gpuv1alpha1.BindResult) {
	t.Helper()
	if d.Status.BindRequest == nil || d.Status.BindRequest.Result != want {
		t.Fatalf("GPUDevice %s: bind request %+v, want result %s", d.Name, d.Status.BindRequest, want)
	}
}

func TestRequestOnOneServingNodeSucceedsWithoutAMove(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		node("n2", map[string]int64{rnL40S: 8}),
		device("a", "l40s", 1, bound("n1"), requested("r1", "vm1", t0, "a")),
	)
	e.pass()
	d := e.device("a")
	expectResult(t, d, gpuv1alpha1.BindSucceeded)
	expectPlacement(t, d, gpuv1alpha1.GPUDeviceBound, "n1")
}

func TestRequestGathersDevicesOfTwoClassesOntoOneNode(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		class("h100", rnH100),
		node("n1", map[string]int64{rnL40S: 2}),
		node("n2", map[string]int64{rnH100: 2}),
		node("both", map[string]int64{rnL40S: 2, rnH100: 2}),
		device("a", "l40s", 1, bound("n1"), requested("r1", "vm1", t0, "a", "b")),
		device("b", "h100", 1, bound("n2"), requested("r1", "vm1", t0, "a", "b")),
	)
	e.pass()
	for _, name := range []string{"a", "b"} {
		d := e.device(name)
		expectResult(t, d, gpuv1alpha1.BindSucceeded)
		expectPlacement(t, d, gpuv1alpha1.GPUDeviceBound, "both")
	}
}

func TestRequestRefusedWhenNoNodeFitsNamesEachDeviceAndNode(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		class("h100", rnH100),
		node("n1", map[string]int64{rnL40S: 2}),
		node("n2", map[string]int64{rnH100: 2}),
		device("a", "l40s", 1, bound("n1"), requested("r1", "vm1", t0, "a", "b")),
		device("b", "h100", 1, bound("n2"), requested("r1", "vm1", t0, "a", "b")),
	)
	e.pass()
	a := e.device("a")
	expectResult(t, a, gpuv1alpha1.BindRefused)
	for _, want := range []string{"GPUDevice a (Bound, n1)", "GPUDevice b (Bound, n2)"} {
		if !strings.Contains(a.Status.BindRequest.Message, want) {
			t.Fatalf("message %q does not name %q", a.Status.BindRequest.Message, want)
		}
	}
	expectPlacement(t, a, gpuv1alpha1.GPUDeviceBound, "n1")
	expectPlacement(t, e.device("b"), gpuv1alpha1.GPUDeviceBound, "n2")
}

func TestTemplateDeviceIsBoundOnTheNodeOfTheHeldSet(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		node("n2", map[string]int64{rnL40S: 1}),
		device("held", "l40s", 1, bound("n1"), requested("r1", "vm1", t0, "held", "vm1-gpu-1")),
		device("vm1-gpu-1", "l40s", 1, template("vm1"), requested("r1", "vm1", t0, "held", "vm1-gpu-1")),
	)
	e.pass()
	expectResult(t, e.device("vm1-gpu-1"), gpuv1alpha1.BindSucceeded)
	expectPlacement(t, e.device("vm1-gpu-1"), gpuv1alpha1.GPUDeviceBound, "n1")
	expectPlacement(t, e.device("held"), gpuv1alpha1.GPUDeviceBound, "n1")
}

func TestHeldDeviceMovesWhenItsNodeHasNoRoomForTheTemplateDevice(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 1}),
		node("n2", map[string]int64{rnL40S: 2}),
		device("held", "l40s", 1, bound("n1"), requested("r1", "vm1", t0, "held", "vm1-gpu-1")),
		device("vm1-gpu-1", "l40s", 1, template("vm1"), requested("r1", "vm1", t0, "held", "vm1-gpu-1")),
	)
	e.pass()
	expectPlacement(t, e.device("held"), gpuv1alpha1.GPUDeviceBound, "n2")
	expectPlacement(t, e.device("vm1-gpu-1"), gpuv1alpha1.GPUDeviceBound, "n2")
}

func TestTemplateDeviceOverBudgetIsRefused(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 4}),
		tenant("acme", map[string]string{"gpu.cozystack.io/l40s": "1"}),
		device("vm1-gpu-0", "l40s", 1, template("vm1"), requested("r1", "vm1", t0, "vm1-gpu-0", "vm1-gpu-1")),
		device("vm1-gpu-1", "l40s", 1, template("vm1"), requested("r1", "vm1", t0, "vm1-gpu-0", "vm1-gpu-1")),
	)
	e.pass()
	d := e.device("vm1-gpu-1")
	expectResult(t, d, gpuv1alpha1.BindRefused)
	if !strings.Contains(d.Status.BindRequest.Message, "gpu.cozystack.io/l40s") {
		t.Fatalf("refusal %q does not name the budget", d.Status.BindRequest.Message)
	}
	expectPlacement(t, e.device("vm1-gpu-0"), gpuv1alpha1.GPUDevicePending, "")
}

func TestPartialRequestIsNotActedOn(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		device("a", "l40s", 1, bound("n1"), requested("r1", "vm1", t0, "a", "b")),
		device("b", "l40s", 1, bound("n1")),
	)
	e.pass()
	if r := e.device("a").Status.BindRequest; r.Result != "" {
		t.Fatalf("a partial request was answered: %+v", r)
	}
}

func TestConcurrentRequestsForTheLastUnitOneWins(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 1}),
		device("vm2-gpu-0", "l40s", 1, template("vm2"), requested("r2", "vm2", t0.Add(time.Second), "vm2-gpu-0")),
		device("vm1-gpu-0", "l40s", 1, template("vm1"), requested("r1", "vm1", t0, "vm1-gpu-0")),
	)
	e.pass()
	expectResult(t, e.device("vm1-gpu-0"), gpuv1alpha1.BindSucceeded)
	expectResult(t, e.device("vm2-gpu-0"), gpuv1alpha1.BindRefused)
	expectPlacement(t, e.device("vm2-gpu-0"), gpuv1alpha1.GPUDevicePending, "")
}

func TestDeviceAttachedToAnotherVMIIsRefused(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		device("a", "l40s", 1, bound("n1"), requested("r1", "vm1", t0, "a"), attached("vm2")),
		vmiObj("vm2", "Running", "n1", ""),
	)
	e.pass()
	expectResult(t, e.device("a"), gpuv1alpha1.BindRefused)
}

func TestRequestGathersDevicesOffANotReadyNode(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("down", map[string]int64{rnL40S: 2}, notReadySince(t0.Add(-time.Minute))),
		node("up", map[string]int64{rnL40S: 2}),
		device("a", "l40s", 1, bound("down"), requested("r1", "vm1", t0, "a")),
	)
	e.pass()
	expectResult(t, e.device("a"), gpuv1alpha1.BindSucceeded)
	expectPlacement(t, e.device("a"), gpuv1alpha1.GPUDeviceBound, "up")
}

func TestStayDeviceOnACordonedNodePinsAVMIThatHoldsOnlyIt(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S, stay),
		node("n1", map[string]int64{rnL40S: 2}, cordoned, notReadySince(t0.Add(-time.Minute))),
		node("n2", map[string]int64{rnL40S: 2}),
		device("a", "l40s", 1, bound("n1"), requested("r1", "vm1", t0, "a")),
	)
	e.pass()
	expectResult(t, e.device("a"), gpuv1alpha1.BindSucceeded)
	expectPlacement(t, e.device("a"), gpuv1alpha1.GPUDeviceBound, "n1")
}

func TestStayDeviceOnACordonedNodeRefusesAGather(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S, stay),
		class("h100", rnH100),
		node("n1", map[string]int64{rnL40S: 2, rnH100: 2}, cordoned),
		node("n2", map[string]int64{rnL40S: 2, rnH100: 2}),
		device("a", "l40s", 1, bound("n1"), requested("r1", "vm1", t0, "a", "b")),
		device("b", "h100", 1, bound("n2"), requested("r1", "vm1", t0, "a", "b")),
	)
	e.pass()
	expectResult(t, e.device("a"), gpuv1alpha1.BindRefused)
	expectPlacement(t, e.device("b"), gpuv1alpha1.GPUDeviceBound, "n2")
}

func TestCordonUnderMoveGathersWhenAnotherNodeHasRoom(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}, cordoned),
		node("n2", map[string]int64{rnL40S: 2}),
		device("a", "l40s", 1, bound("n1"), requested("r1", "vm1", t0, "a")),
	)
	e.pass()
	expectResult(t, e.device("a"), gpuv1alpha1.BindSucceeded)
	expectPlacement(t, e.device("a"), gpuv1alpha1.GPUDeviceBound, "n2")
}

func TestCordonUnderMoveWithNoRoomElsewhereStaysAndSucceeds(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}, cordoned),
		node("n2", map[string]int64{rnL40S: 2}),
		device("other", "l40s", 2, bound("n2"), inNamespace("tenant-other")),
		device("a", "l40s", 1, bound("n1"), requested("r1", "vm1", t0, "a")),
	)
	e.pass()
	expectResult(t, e.device("a"), gpuv1alpha1.BindSucceeded)
	expectPlacement(t, e.device("a"), gpuv1alpha1.GPUDeviceBound, "n1")
}

func TestOnlyTemplateDevicesGetOneNodeWithRoomForAll(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 1}),
		node("n2", map[string]int64{rnL40S: 2}),
		device("vm1-gpu-0", "l40s", 1, template("vm1"), requested("r1", "vm1", t0, "vm1-gpu-0", "vm1-gpu-1")),
		device("vm1-gpu-1", "l40s", 1, template("vm1"), requested("r1", "vm1", t0, "vm1-gpu-0", "vm1-gpu-1")),
	)
	e.pass()
	expectPlacement(t, e.device("vm1-gpu-0"), gpuv1alpha1.GPUDeviceBound, "n2")
	expectPlacement(t, e.device("vm1-gpu-1"), gpuv1alpha1.GPUDeviceBound, "n2")
}

func TestRequestPassAnswersRequestsAndNothingElse(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 4}),
		device("a", "l40s", 1, bound("n1"), requested("r1", "vm1", t0, "a")),
		device("order", "l40s", 1),
	)
	if _, err := e.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: requestsKey}); err != nil {
		t.Fatalf("request pass: %v", err)
	}
	expectResult(t, e.device("a"), gpuv1alpha1.BindSucceeded)
	if d := e.device("order"); d.Status.Phase != "" {
		t.Fatalf("the request pass bound an order: %+v", d.Status)
	}
}

// newEnvFailingOnce fails the first status write of device name with a
// conflict, as a webhook writing the same device in between would.
func newEnvFailingOnce(t *testing.T, name string, objs ...client.Object) *env {
	t.Helper()
	failed := false
	c := fake.NewClientBuilder().WithScheme(testScheme(true)).WithObjects(objs...).
		WithStatusSubresource(&gpuv1alpha1.GPUClass{}, &gpuv1alpha1.GPUDevice{}, &gpuv1alpha1.GPUNode{}, &cozyv1alpha1.WorkloadMonitor{}).
		WithInterceptorFuncs(interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, cl client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			if obj.GetName() == name && !failed {
				failed = true
				return apierrors.NewConflict(gpuv1alpha1.GroupVersion.WithResource("gpudevices").GroupResource(), name, nil)
			}
			return cl.SubResource(sub).Update(ctx, obj, opts...)
		}}).Build()
	e := &env{t: t, c: c, now: t0}
	e.r = &Reconciler{Client: c, Reader: c, Now: func() time.Time { return e.now },
		Config: gpu.Config{Enabled: true, LostCapacityGrace: time.Minute, AttachmentGrace: time.Minute}}
	return e
}

// A failed write must not leave the pass believing a unit was freed: an
// order placed on it would overbook the node for good.
func TestAFailedMoveWritePlacesNothingMoreInThatPass(t *testing.T) {
	e := newEnvFailingOnce(t, "a",
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 1}),
		node("n2", map[string]int64{rnL40S: 2}),
		device("a", "l40s", 1, bound("n1"), requested("r1", "vm1", t0, "a", "b")),
		device("b", "l40s", 1, bound("n2"), requested("r1", "vm1", t0, "a", "b")),
		device("o", "l40s", 1),
	)
	res, err := e.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: passKey})
	if err != nil || res.RequeueAfter == 0 {
		t.Fatalf("a conflict must requeue: %v, %+v", err, res)
	}
	held := map[string]int32{}
	for _, name := range []string{"a", "b", "o"} {
		if d := e.device(name); holds(d) {
			held[d.Status.NodeName] += d.Spec.Count
		}
	}
	if held["n1"] > 1 || held["n2"] > 2 {
		t.Fatalf("overbooked after a failed write: %v", held)
	}
	e.pass()
	expectResult(t, e.device("a"), gpuv1alpha1.BindSucceeded)
	expectPlacement(t, e.device("a"), gpuv1alpha1.GPUDeviceBound, "n2")
	expectPlacement(t, e.device("o"), gpuv1alpha1.GPUDeviceBound, "n1")
}

// A Lost device holds no units, so a VMI that still runs on a node that came
// back holds them through its pod until the full pass re-attaches it; a
// request answered first must see them taken.
func TestRequestPassCountsThePodOfALostDevicesRunningVMI(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		device("b", "l40s", 2, lost("n1"), attached("vm1")),
		vmiObj("vm1", "Running", "n1", ""),
		launcherPod("virt-launcher-vm1", "vm1", "n1", map[string]int64{rnL40S: 2}),
		device("vm2-gpu-0", "l40s", 1, template("vm2"), requested("r2", "vm2", t0, "vm2-gpu-0")),
	)
	if _, err := e.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: requestsKey}); err != nil {
		t.Fatalf("request pass: %v", err)
	}
	expectResult(t, e.device("vm2-gpu-0"), gpuv1alpha1.BindRefused)
}

func TestAFailedRebindWriteKeepsTheSetTogether(t *testing.T) {
	e := newEnvFailingOnce(t, "b",
		class("l40s", rnL40S),
		node("n0", map[string]int64{rnL40S: 1}),
		node("n1", map[string]int64{rnL40S: 2}),
		device("a", "l40s", 1, lost("gone")),
		device("b", "l40s", 1, lost("gone")),
		vmObj("vm", "Always", "a", "b"),
	)
	e.pass()
	expectPlacement(t, e.device("a"), gpuv1alpha1.GPUDeviceBound, "n1")
	expectPlacement(t, e.device("b"), gpuv1alpha1.GPUDeviceBound, "n1")
}

func TestAFailedOrderWriteCreatesNoUsageRecord(t *testing.T) {
	e := newEnvFailingOnce(t, "o",
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		device("o", "l40s", 1),
	)
	if _, err := e.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: passKey}); err != nil {
		t.Fatalf("pass: %v", err)
	}
	if e.device("o").Status.Phase != "" || e.recordExists("o") {
		t.Fatal("a usage record exists for a bind that was never written")
	}
	e.pass()
	if !e.recordExists("o") {
		t.Fatal("no usage record once the bind is written")
	}
}

// A set whose move to Lost was only half written must not have its held
// half moved alone onto the last free unit.
func TestAHalfWrittenLostSetIsNotSplitAcrossNodes(t *testing.T) {
	e := newEnvFailingOnce(t, "b",
		class("l40s", rnL40S),
		node("n2", map[string]int64{rnL40S: 1}),
		device("a", "l40s", 1, bound("gone")),
		device("b", "l40s", 1, bound("gone")),
		vmObj("vm", "Always", "a", "b"),
	)
	e.pass()
	expectPlacement(t, e.device("a"), gpuv1alpha1.GPUDeviceLost, "gone")
	expectPlacement(t, e.device("b"), gpuv1alpha1.GPUDeviceLost, "gone")
}

func TestASetWriteStopsAtTheFirstFailure(t *testing.T) {
	e := newEnvFailingOnce(t, "a",
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		device("a", "l40s", 1, lost("gone")),
		device("b", "l40s", 1, lost("gone")),
		vmObj("vm", "Always", "a", "b"),
	)
	res, err := e.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: passKey})
	if err != nil || res.RequeueAfter != conflictRetry {
		t.Fatalf("want a retry after %v, got %v, %+v", conflictRetry, err, res)
	}
	expectPlacement(t, e.device("b"), gpuv1alpha1.GPUDeviceLost, "gone")
}

// A Stay device pins a mixed set to its cordoned node; the full pass must
// not then delete, through the Move member, the VMI the request pass
// admitted.
func TestAStayPinnedMixedSetKeepsItsAdmittedVMI(t *testing.T) {
	v := vmiObj("vm1", "Pending", "", "vm")
	e := newEnv(t,
		class("l40s", rnL40S, stay),
		class("h100", rnH100),
		node("n1", map[string]int64{rnL40S: 1, rnH100: 1}, cordoned),
		node("n2", map[string]int64{rnL40S: 1, rnH100: 1}),
		device("a", "l40s", 1, bound("n1"), requested("r1", "vm1", t0, "a", "b")),
		device("b", "h100", 1, bound("n1"), requested("r1", "vm1", t0, "a", "b")),
		vmObj("vm", "Always", "a", "b"),
		v,
	)
	if _, err := e.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: requestsKey}); err != nil {
		t.Fatalf("request pass: %v", err)
	}
	expectResult(t, e.device("a"), gpuv1alpha1.BindSucceeded)
	if _, err := e.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: passKey}); err != nil {
		t.Fatalf("pass: %v", err)
	}
	if e.gone(v) {
		t.Fatal("the full pass deleted the VMI the binder had just pinned")
	}
}

func TestAStayDeviceOnACordonedNodeThatLostCapacityIsGathered(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S, stay),
		node("n1", map[string]int64{rnL40S: 1}, cordoned, notReadySince(t0.Add(-time.Hour))),
		node("n2", map[string]int64{rnL40S: 1}),
		device("a", "l40s", 1, bound("n1"), requested("r1", "vm1", t0, "a")),
	)
	if _, err := e.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: requestsKey}); err != nil {
		t.Fatalf("request pass: %v", err)
	}
	d := e.device("a")
	expectResult(t, d, gpuv1alpha1.BindSucceeded)
	expectPlacement(t, d, gpuv1alpha1.GPUDeviceBound, "n2")
}

// Lost capacity on any member held on the pinned node breaks a Stay pin,
// so the set is gathered instead of admitted to a node the full pass would
// then take its VMI off.
func TestLostCapacityOnAMoveMemberBreaksTheStayPin(t *testing.T) {
	gn := &gpuv1alpha1.GPUNode{}
	gn.Name = "n1"
	gn.Status.ResourceNames = []string{rnH100}
	gn.Status.Devices = []gpuv1alpha1.GPUNodeDevice{
		{PCIAddress: "0000:41:00.0", Function: gpuv1alpha1.PCIFunctionWhole, ResourceName: rnH100, Healthy: ptr.To(false)},
	}
	v := vmiObj("vm1", "Pending", "", "vm")
	e := newEnv(t,
		class("l40s", rnL40S, stay),
		class("h100", rnH100),
		node("n1", map[string]int64{rnL40S: 1, rnH100: 1}, cordoned),
		node("n2", map[string]int64{rnL40S: 1, rnH100: 1}),
		gn,
		device("a", "l40s", 1, bound("n1"), requested("r1", "vm1", t0, "a", "b")),
		device("b", "h100", 1, bound("n1"), requested("r1", "vm1", t0, "a", "b")),
		vmObj("vm", "Always", "a", "b"),
		v,
	)
	e.r.attachedSince = map[string]time.Time{}
	e.r.degradedSince = map[string]time.Time{rnH100 + "@n1": t0.Add(-time.Hour)}
	if _, err := e.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: requestsKey}); err != nil {
		t.Fatalf("request pass: %v", err)
	}
	for _, name := range []string{"a", "b"} {
		expectResult(t, e.device(name), gpuv1alpha1.BindSucceeded)
		expectPlacement(t, e.device(name), gpuv1alpha1.GPUDeviceBound, "n2")
	}
	if _, err := e.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: passKey}); err != nil {
		t.Fatalf("pass: %v", err)
	}
	if e.gone(v) {
		t.Fatal("the full pass deleted the VMI the binder had just admitted")
	}
}

// A node that no longer serves a member's class breaks the Stay pin the
// same way lost capacity does.
func TestANodeThatNoLongerServesAMoveMemberBreaksTheStayPin(t *testing.T) {
	v := vmiObj("vm1", "Pending", "", "vm")
	e := newEnv(t,
		class("l40s", rnL40S, stay),
		class("h100", rnH100, selecting("pool", "b")),
		node("n1", map[string]int64{rnL40S: 1, rnH100: 1}, cordoned, labelled("pool", "a")),
		node("n2", map[string]int64{rnL40S: 1, rnH100: 1}, labelled("pool", "b")),
		device("a", "l40s", 1, bound("n1"), requested("r1", "vm1", t0, "a", "b")),
		device("b", "h100", 1, bound("n1"), requested("r1", "vm1", t0, "a", "b")),
		vmObj("vm", "Always", "a", "b"),
		v,
	)
	if _, err := e.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: requestsKey}); err != nil {
		t.Fatalf("request pass: %v", err)
	}
	for _, name := range []string{"a", "b"} {
		expectResult(t, e.device(name), gpuv1alpha1.BindSucceeded)
		expectPlacement(t, e.device(name), gpuv1alpha1.GPUDeviceBound, "n2")
	}
	if _, err := e.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: passKey}); err != nil {
		t.Fatalf("pass: %v", err)
	}
	if e.gone(v) {
		t.Fatal("the full pass deleted the VMI the binder had just admitted")
	}
}
