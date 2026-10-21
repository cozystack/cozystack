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
	"fmt"
	"math/rand"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu"
)

const setPropertyCases = 400

// Random clusters, random write failures: once a pass completes without a
// failed write, the set invariant of normalizeSets holds, every set that
// started with a Lost member is all Lost or held on one node, and no node
// holds more units than it has.
func TestSetInvariantHoldsAfterACleanPassWhateverWritesFailed(t *testing.T) {
	for seed := int64(0); seed < setPropertyCases; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) { checkSetInvariant(t, seed) })
	}
}

func checkSetInvariant(t *testing.T, seed int64) {
	rng := rand.New(rand.NewSource(seed))
	alloc := map[string]int64{}
	objs := []client.Object{class("l40s", rnL40S)}
	for i := range 1 + rng.Intn(4) {
		name := fmt.Sprintf("n%d", i)
		alloc[name] = int64(rng.Intn(4))
		var opts []nodeOpt
		if rng.Intn(4) == 0 {
			opts = append(opts, notReadySince(t0.Add(-time.Hour)))
		}
		objs = append(objs, node(name, map[string]int64{rnL40S: alloc[name]}, opts...))
	}
	free := map[string]int64{}
	for n, a := range alloc {
		free[n] = a
	}
	startedLost := map[string]bool{}
	finalVMI := map[string]bool{}
	for v := range 1 + rng.Intn(4) {
		var names []string
		for m := range 1 + rng.Intn(3) {
			name := fmt.Sprintf("vm%d-d%d", v, m)
			names = append(names, name)
			var opt deviceOpt
			switch rng.Intn(3) {
			case 0:
				opt = lost("gone")
				startedLost[fmt.Sprint(v)] = true
				if rng.Intn(2) == 0 {
					// Its VMI ended: the pass detaches it before it is rebound.
					vmi := fmt.Sprintf("vm%d", v)
					opt = func(d *gpuv1alpha1.GPUDevice) { lost("gone")(d); attached(vmi)(d) }
					if !finalVMI[vmi] {
						finalVMI[vmi] = true
						objs = append(objs, vmiObj(vmi, "Failed", "", vmi))
					}
				}
			case 1:
				opt = bound("gone")
			default:
				n := fmt.Sprintf("n%d", rng.Intn(len(alloc)))
				if free[n] == 0 {
					opt = lost("gone")
					startedLost[fmt.Sprint(v)] = true
					break
				}
				free[n]--
				opt = bound(n)
			}
			objs = append(objs, device(name, "l40s", 1, opt))
		}
		objs = append(objs, vmObj(fmt.Sprintf("vm%d", v), "Always", names...))
	}

	failing := true
	failedThisPass := false
	c := fake.NewClientBuilder().WithScheme(testScheme(true)).WithObjects(objs...).
		WithStatusSubresource(&gpuv1alpha1.GPUClass{}, &gpuv1alpha1.GPUDevice{}, &gpuv1alpha1.GPUNode{}, &cozyv1alpha1.WorkloadMonitor{}).
		WithInterceptorFuncs(interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, cl client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			if _, ok := obj.(*gpuv1alpha1.GPUDevice); ok && failing && rng.Intn(3) == 0 {
				failedThisPass = true
				return apierrors.NewConflict(gpuv1alpha1.GroupVersion.WithResource("gpudevices").GroupResource(), obj.GetName(), nil)
			}
			return cl.SubResource(sub).Update(ctx, obj, opts...)
		}}).Build()
	r := &Reconciler{Client: c, Reader: c, Now: func() time.Time { return t0 },
		Config: gpu.Config{Enabled: true, LostCapacityGrace: time.Minute, AttachmentGrace: time.Minute}}

	clean := false
	for pass := 0; pass < 20 && !clean; pass++ {
		failing = pass < 6
		failedThisPass = false
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: passKey}); err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
		clean = !failedThisPass
	}
	if !clean {
		t.Fatal("no clean pass")
	}

	devices := &gpuv1alpha1.GPUDeviceList{}
	if err := c.List(context.Background(), devices); err != nil {
		t.Fatal(err)
	}
	byName := map[string]*gpuv1alpha1.GPUDevice{}
	held := map[string]int64{}
	for i := range devices.Items {
		d := &devices.Items[i]
		byName[d.Name] = d
		if holds(d) {
			held[d.Status.NodeName] += int64(d.Spec.Count)
		}
	}
	for n, units := range held {
		if units > alloc[n] && n != "gone" {
			t.Fatalf("node %s holds %d units of %d", n, units, alloc[n])
		}
	}
	for v := 0; ; v++ {
		var set []*gpuv1alpha1.GPUDevice
		for m := 0; ; m++ {
			d := byName[fmt.Sprintf("vm%d-d%d", v, m)]
			if d == nil {
				break
			}
			set = append(set, d)
		}
		if len(set) == 0 {
			return
		}
		if mixesLost(set) {
			t.Fatalf("set vm%d mixes Lost and held devices: %s", v, describe(set))
		}
		if startedLost[fmt.Sprint(v)] && !allLost(set) && !onOneNode(set, set[0].Status.NodeName) {
			t.Fatalf("set vm%d had a Lost member and is held on several nodes: %s", v, describe(set))
		}
	}
}

// A VMI that went final leaves its Lost device unattached in the same pass
// that must keep the device's set whole.
func TestADetachedLostDeviceDoesNotLeaveItsSetMixedAfterOnePass(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 1}, notReadySince(t0.Add(-time.Hour))),
		node("n2", map[string]int64{rnL40S: 1}),
		node("n3", map[string]int64{rnL40S: 1}),
		device("a", "l40s", 1, lost("n1"), attached("vm1")),
		vmiObj("vm1", "Failed", "n1", "vm"),
		device("b", "l40s", 1, bound("n2")),
		vmObj("vm", "Always", "a", "b"),
	)
	if _, err := e.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: passKey}); err != nil {
		t.Fatalf("pass: %v", err)
	}
	set := []*gpuv1alpha1.GPUDevice{e.device("a"), e.device("b")}
	if mixesLost(set) {
		t.Fatalf("one pass left the set mixed: %s", describe(set))
	}
}

const requestPropertyCases = 400

// Random cordons, NotReady spells, taints and lost capacity under Stay and
// Move classes: what the request pass answers, the next full pass keeps. A
// Succeeded set stays on the node the answer left it on and its VMI is not
// deleted; a Refused request changes no placement.
func TestTheFullPassKeepsWhatTheRequestPassAnswered(t *testing.T) {
	for seed := int64(0); seed < requestPropertyCases; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) { checkRequestAgreement(t, seed) })
	}
}

func checkRequestAgreement(t *testing.T, seed int64) {
	rng := rand.New(rand.NewSource(seed))
	resources := []string{rnL40S, rnH100}
	classNames := []string{"l40s", "h100"}
	stayClass := map[string]bool{}
	var objs []client.Object
	for i, name := range classNames {
		var opts []classOpt
		if rng.Intn(2) == 0 {
			opts = append(opts, stay)
			stayClass[name] = true
		}
		objs = append(objs, class(name, resources[i], opts...))
	}
	degraded := map[string]time.Time{}
	lostNode := map[string]bool{}
	cordonedNode := map[string]bool{}
	alloc := map[string]map[string]int64{}
	nodeCount := 2 + rng.Intn(3)
	for i := range nodeCount {
		name := fmt.Sprintf("n%d", i)
		alloc[name] = map[string]int64{rnL40S: int64(rng.Intn(3)), rnH100: int64(rng.Intn(3))}
		var opts []nodeOpt
		if rng.Intn(2) == 0 {
			opts = append(opts, cordoned)
		}
		switch rng.Intn(5) {
		case 0:
			opts = append(opts, notReadySince(t0.Add(-time.Minute)))
		case 1:
			opts = append(opts, notReadySince(t0.Add(-time.Hour)))
			lostNode[name] = true
		case 2:
			opts = append(opts, tainted("dedicated", corev1.TaintEffectNoSchedule))
		}
		cordonedNode[name] = hasCordon(opts)
		objs = append(objs, node(name, alloc[name], opts...))
		if rng.Intn(4) == 0 {
			rn := resources[rng.Intn(2)]
			gn := &gpuv1alpha1.GPUNode{}
			gn.Name = name
			gn.Status.ResourceNames = []string{rn}
			gn.Status.Devices = []gpuv1alpha1.GPUNodeDevice{{PCIAddress: "0000:41:00.0",
				Function: gpuv1alpha1.PCIFunctionWhole, ResourceName: rn, Healthy: ptr.To(false)}}
			objs = append(objs, gn)
			degraded[rn+"@"+name] = t0.Add(-time.Hour)
		}
	}
	if rng.Intn(2) == 0 {
		// A node with room, so that moving a set off a cordon is possible.
		objs = append(objs, node("spare", map[string]int64{rnL40S: 4, rnH100: 4}))
	}
	used := map[string]int64{}
	stopped := map[string][]string{}
	vms := 1 + rng.Intn(3)
	for v := range vms {
		vmName := fmt.Sprintf("vm%d", v)
		var names []string
		// Most sets sit on one node, the case a Stay pin can keep in place.
		home := fmt.Sprintf("n%d", rng.Intn(nodeCount))
		for m := range 1 + rng.Intn(3) {
			ci := rng.Intn(2)
			n := home
			if rng.Intn(3) == 0 {
				n = fmt.Sprintf("n%d", rng.Intn(nodeCount))
			}
			k := resources[ci] + "@" + n
			if used[k] >= alloc[n][resources[ci]] {
				continue
			}
			used[k]++
			name := fmt.Sprintf("%s-d%d", vmName, m)
			names = append(names, name)
			objs = append(objs, device(name, classNames[ci], 1, bound(n)))
		}
		if len(names) == 0 {
			continue
		}
		if rng.Intn(2) == 0 {
			// A stopped VM: no VMI, no request; only the full pass moves it.
			objs = append(objs, vmObj(vmName, "Always", names...))
			stopped[vmName] = names
			continue
		}
		for _, o := range objs {
			if d, ok := o.(*gpuv1alpha1.GPUDevice); ok && slices.Contains(names, d.Name) {
				requested("r-"+vmName, vmName, t0, names...)(d)
			}
		}
		objs = append(objs, vmObj(vmName, "Always", names...), vmiObj(vmName, "Pending", "", vmName))
	}

	// One set of a Stay and a Move device on cordoned nodes in every case,
	// under a random extra condition, so the pin rules are always exercised.
	const rnStay, rnMove = "example.com/stay", "example.com/move"
	stayClass["pinned"] = true
	objs = append(objs, class("pinned", rnStay, stay), class("moving", rnMove),
		node("spare-x", map[string]int64{rnStay: 2, rnMove: 2}))
	x1 := []nodeOpt{cordoned}
	switch rng.Intn(6) {
	case 1:
		x1 = append(x1, tainted("dedicated", corev1.TaintEffectNoSchedule))
	case 2:
		x1 = append(x1, notReadySince(t0.Add(-time.Minute)))
	case 3:
		x1 = append(x1, notReadySince(t0.Add(-time.Hour)))
		lostNode["x1"] = true
	case 4, 5:
		rn := rnStay
		if rng.Intn(2) == 0 {
			rn = rnMove
		}
		gn := &gpuv1alpha1.GPUNode{}
		gn.Name = "x1"
		gn.Status.ResourceNames = []string{rn}
		gn.Status.Devices = []gpuv1alpha1.GPUNodeDevice{{PCIAddress: "0000:41:00.0",
			Function: gpuv1alpha1.PCIFunctionWhole, ResourceName: rn, Healthy: ptr.To(false)}}
		objs = append(objs, gn)
		degraded[rn+"@x1"] = t0.Add(-time.Hour)
	}
	cordonedNode["x1"], cordonedNode["x2"] = true, true
	objs = append(objs, node("x1", map[string]int64{rnStay: 1, rnMove: 1}, x1...),
		node("x2", map[string]int64{rnStay: 1, rnMove: 1}, cordoned))
	moveNode := "x1"
	if rng.Intn(2) == 0 {
		moveNode = "x2"
	}
	xs := []string{"vmx-d0", "vmx-d1"}
	sd, md := device(xs[0], "pinned", 1, bound("x1")), device(xs[1], "moving", 1, bound(moveNode))
	objs = append(objs, sd, md, vmObj("vmx", "Always", xs...))
	if rng.Intn(2) == 0 {
		requested("r-vmx", "vmx", t0, xs...)(sd)
		requested("r-vmx", "vmx", t0, xs...)(md)
		objs = append(objs, vmiObj("vmx", "Pending", "", "vmx"))
	} else {
		stopped["vmx"] = xs
	}

	e := newEnv(t, objs...)
	e.r.attachedSince = map[string]time.Time{}
	e.r.degradedSince = degraded
	if _, err := e.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: requestsKey}); err != nil {
		t.Fatalf("request pass: %v", err)
	}
	answered := listDevices(t, e)
	if _, err := e.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: passKey}); err != nil {
		t.Fatalf("full pass: %v", err)
	}
	after := listDevices(t, e)
	for name, d := range answered {
		req := d.Status.BindRequest
		if req == nil {
			continue
		}
		switch req.Result {
		case gpuv1alpha1.BindSucceeded:
			if e.gone(vmiObj(req.VMI, "", "", "")) {
				t.Fatalf("the full pass deleted VMI %s, whose request was answered Succeeded", req.VMI)
			}
			if a := after[name]; a.Status.NodeName != d.Status.NodeName || !holds(a) {
				t.Fatalf("GPUDevice %s of an admitted set moved from %s to %s %s", name, d.Status.NodeName, a.Status.Phase, a.Status.NodeName)
			}
		case gpuv1alpha1.BindRefused:
			orig := initial(objs, name)
			if d.Status.NodeName != orig.Status.NodeName || d.Status.Phase != orig.Status.Phase {
				t.Fatalf("a refused request moved GPUDevice %s from %s %s to %s %s", name,
					orig.Status.Phase, orig.Status.NodeName, d.Status.Phase, d.Status.NodeName)
			}
		default:
			t.Fatalf("GPUDevice %s: request not answered", name)
		}
	}

	// A stopped VM's set with a Stay device on a cordoned node does not move
	// unless a member has to leave its node: the node lost capacity for it.
	for vmName, names := range stopped {
		pinned, mustLeave := false, false
		for _, n := range names {
			d := initial(objs, n)
			node := d.Status.NodeName
			if stayClass[d.Spec.ClassName] && cordonedNode[node] {
				pinned = true
			}
			if _, ok := degraded[resourceOf(d.Spec.ClassName)+"@"+node]; ok || lostNode[node] {
				mustLeave = true
			}
		}
		if !pinned || mustLeave {
			continue
		}
		for _, n := range names {
			if a, d := after[n], initial(objs, n); a.Status.NodeName != d.Status.NodeName || !holds(a) {
				t.Fatalf("stopped VM %s: GPUDevice %s left %s for %s %s although a Stay device pins its set",
					vmName, n, d.Status.NodeName, a.Status.Phase, a.Status.NodeName)
			}
		}
	}
}

func resourceOf(class string) string {
	return map[string]string{"l40s": rnL40S, "h100": rnH100, "pinned": "example.com/stay", "moving": "example.com/move"}[class]
}

func hasCordon(opts []nodeOpt) bool {
	n := node("probe", nil)
	for _, o := range opts {
		o(n)
	}
	return n.Spec.Unschedulable
}

func listDevices(t *testing.T, e *env) map[string]*gpuv1alpha1.GPUDevice {
	t.Helper()
	l := &gpuv1alpha1.GPUDeviceList{}
	if err := e.c.List(t.Context(), l); err != nil {
		t.Fatal(err)
	}
	out := map[string]*gpuv1alpha1.GPUDevice{}
	for i := range l.Items {
		out[l.Items[i].Name] = &l.Items[i]
	}
	return out
}

func initial(objs []client.Object, name string) *gpuv1alpha1.GPUDevice {
	for _, o := range objs {
		if d, ok := o.(*gpuv1alpha1.GPUDevice); ok && d.Name == name {
			return d
		}
	}
	return nil
}
