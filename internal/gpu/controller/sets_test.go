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
	"maps"
	"math/rand"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu"
	"github.com/cozystack/cozystack/internal/gpu/model"
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
			var opt claimOpt
			switch rng.Intn(3) {
			case 0:
				opt = lost("gone")
				startedLost[fmt.Sprint(v)] = true
				if rng.Intn(2) == 0 {
					// Its VMI ended: the pass detaches it before it is rebound.
					vmi := fmt.Sprintf("vm%d", v)
					opt = func(d *gpuv1alpha1.GPUClaim) { lost("gone")(d); attached(vmi)(d) }
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
			objs = append(objs, claim(name, "l40s", 1, opt))
		}
		objs = append(objs, vmObj(fmt.Sprintf("vm%d", v), "Always", names...))
	}

	failing := true
	failedThisPass := false
	c := fake.NewClientBuilder().WithScheme(testScheme(true)).WithObjects(objs...).
		WithStatusSubresource(&gpuv1alpha1.GPUClass{}, &gpuv1alpha1.GPUClaim{}, &gpuv1alpha1.GPUNode{}).
		WithInterceptorFuncs(interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, cl client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			if _, ok := obj.(*gpuv1alpha1.GPUClaim); ok && failing && rng.Intn(3) == 0 {
				failedThisPass = true
				return apierrors.NewConflict(gpuv1alpha1.GroupVersion.WithResource("gpuclaims").GroupResource(), obj.GetName(), nil)
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

	claims := &gpuv1alpha1.GPUClaimList{}
	if err := c.List(context.Background(), claims); err != nil {
		t.Fatal(err)
	}
	byName := map[string]*gpuv1alpha1.GPUClaim{}
	held := map[string]int64{}
	for i := range claims.Items {
		d := &claims.Items[i]
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
		var set []*gpuv1alpha1.GPUClaim
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
			t.Fatalf("set vm%d mixes Lost and held claims: %s", v, describe(set))
		}
		if startedLost[fmt.Sprint(v)] && !allLost(set) && !onOneNode(set, set[0].Status.NodeName) {
			t.Fatalf("set vm%d had a Lost member and is held on several nodes: %s", v, describe(set))
		}
	}
}

// A VMI that went final leaves its Lost claim unattached in the same pass
// that must keep the claim's set whole.
func TestADetachedLostClaimDoesNotLeaveItsSetMixedAfterOnePass(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 1}, notReadySince(t0.Add(-time.Hour))),
		node("n2", map[string]int64{rnL40S: 1}),
		node("n3", map[string]int64{rnL40S: 1}),
		claim("a", "l40s", 1, lost("n1"), attached("vm1")),
		vmiObj("vm1", "Failed", "n1", "vm"),
		claim("b", "l40s", 1, bound("n2")),
		vmObj("vm", "Always", "a", "b"),
	)
	if _, err := e.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: passKey}); err != nil {
		t.Fatalf("pass: %v", err)
	}
	set := []*gpuv1alpha1.GPUClaim{e.claim("a"), e.claim("b")}
	if mixesLost(set) {
		t.Fatalf("one pass left the set mixed: %s", describe(set))
	}
}

const gatePropertyCases = 400

// Random cordons, NotReady spells, taints, lost capacity and rejected nodes
// under Stay and Move classes, with held and template claims: what a pass
// does with a gated launcher pod, the next pass keeps. A released pod is
// pinned to a node that holds its whole set, template claims included, a
// node that refused the set only when a cordon keeps the set there; the
// set stays there and its VMI is not deleted. A pod left gated changes no
// placement but a hold its lost node ends.
func TestTheNextPassKeepsWhatTheGateReleased(t *testing.T) {
	for seed := int64(0); seed < gatePropertyCases; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) { checkGateAgreement(t, seed) })
	}
}

type startedVM struct {
	held, all []string
	rejected  string
}

func checkGateAgreement(t *testing.T, seed int64) {
	rng := rand.New(rand.NewSource(seed))
	created = t0.Add(-time.Hour)
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
		alloc["spare"] = map[string]int64{rnL40S: 4, rnH100: 4}
		objs = append(objs, node("spare", alloc["spare"]))
	}
	used := map[string]int64{}
	stopped := map[string][]string{}
	started := map[string]*startedVM{}
	start := func(vmName string, names []string, sets ...func(*gpuv1alpha1.GPUClaim)) {
		vm := &startedVM{held: names}
		entries := slices.Clone(names)
		if rng.Intn(2) == 0 {
			entries = append(entries, classNames[rng.Intn(2)]+":1")
		}
		for i, e := range entries {
			if slices.Contains(names, e) {
				vm.all = append(vm.all, e)
			} else {
				vm.all = append(vm.all, model.TemplateClaimName(vmName, i))
			}
		}
		if rng.Intn(3) == 0 {
			vm.rejected = fmt.Sprintf("n%d", rng.Intn(nodeCount))
		}
		for _, o := range objs {
			if d, ok := o.(*gpuv1alpha1.GPUClaim); ok && slices.Contains(names, d.Name) {
				recorded(vmName)(d)
				if vm.rejected != "" && d.Name == names[0] {
					d.Status.RejectedNodes = []gpuv1alpha1.RejectedNode{{NodeName: vm.rejected, Until: metav1.NewTime(t0.Add(5 * time.Minute))}}
				}
			}
		}
		started[vmName] = vm
		objs = append(objs, vmObj(vmName, "Always", names...), refs(vmiObj(vmName, "Scheduling", "", vmName), entries...), gatedPod(vmName))
	}
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
			objs = append(objs, claim(name, classNames[ci], 1, bound(n)))
		}
		if len(names) == 0 {
			continue
		}
		if rng.Intn(2) == 0 {
			// A stopped VM: no VMI, no pod; only moves apply to it.
			objs = append(objs, vmObj(vmName, "Always", names...))
			stopped[vmName] = names
			continue
		}
		start(vmName, names)
	}

	// One set of a Stay and a Move claim on cordoned nodes in every case,
	// under a random extra condition, so the pin rules are always exercised.
	const rnStay, rnMove = "example.com/stay", "example.com/move"
	stayClass["pinned"] = true
	alloc["spare-x"] = map[string]int64{rnStay: 2, rnMove: 2}
	objs = append(objs, class("pinned", rnStay, stay), class("moving", rnMove), node("spare-x", alloc["spare-x"]))
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
	alloc["x1"] = map[string]int64{rnStay: 1, rnMove: 1}
	alloc["x2"] = map[string]int64{rnStay: 1, rnMove: 1}
	objs = append(objs, node("x1", alloc["x1"], x1...), node("x2", alloc["x2"], cordoned))
	moveNode := "x1"
	if rng.Intn(2) == 0 {
		moveNode = "x2"
	}
	xs := []string{"vmx-d0", "vmx-d1"}
	objs = append(objs, claim(xs[0], "pinned", 1, bound("x1")), claim(xs[1], "moving", 1, bound(moveNode)))
	if rng.Intn(2) == 0 {
		start("vmx", xs)
	} else {
		objs = append(objs, vmObj("vmx", "Always", xs...))
		stopped["vmx"] = xs
	}

	e := newEnv(t, objs...)
	e.r.attachedSince = map[string]time.Time{}
	e.r.rejectedAs = map[string]nodeState{}
	// The reconciler forgets a degradation once nothing is held there.
	e.r.degradedSince = maps.Clone(degraded)
	if _, err := e.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: passKey}); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	first := listClaims(t, e)
	pods := map[string]*corev1.Pod{}
	for vmName := range started {
		pods[vmName] = e.pod("virt-launcher-" + vmName)
	}
	if _, err := e.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: passKey}); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	after := listClaims(t, e)

	held := map[string]int64{}
	for _, d := range first {
		if holds(d) && !lostNode[d.Status.NodeName] {
			held[resourceOf(d.Spec.ClassName)+"@"+d.Status.NodeName] += int64(d.Spec.Count)
		}
	}
	for k, units := range held {
		rn, n, _ := strings.Cut(k, "@")
		if _, isDegraded := degraded[k]; !isDegraded && units > alloc[n][rn] {
			t.Fatalf("node %s holds %d units of %s, has %d", n, units, rn, alloc[n][rn])
		}
	}

	for vmName, vm := range started {
		p := pods[vmName]
		if gated(p) {
			if pinnedNode(p) != "" {
				t.Fatalf("VMI %s: a gated pod carries a pin", vmName)
			}
			for _, name := range vm.held {
				orig, d := initial(objs, name), first[name]
				if d.Status.NodeName != orig.Status.NodeName || (holds(d) != holds(orig) && !lostNode[orig.Status.NodeName]) {
					t.Fatalf("VMI %s stays gated but GPUClaim %s moved from %s %s to %s %s", vmName, name,
						orig.Status.Phase, orig.Status.NodeName, d.Status.Phase, d.Status.NodeName)
				}
			}
			continue
		}
		pin := pinnedNode(p)
		if pin == "" {
			t.Fatalf("VMI %s: released pod without a pin", vmName)
		}
		if pin == vm.rejected && !cordonedNode[pin] {
			t.Fatalf("VMI %s: released onto node %s, which refused its set", vmName, pin)
		}
		if e.gone(vmiObj(vmName, "", "", "")) {
			t.Fatalf("the next pass deleted VMI %s, whose pod the gate had released", vmName)
		}
		if got := pinnedNode(e.pod("virt-launcher-" + vmName)); got != pin {
			t.Fatalf("VMI %s: the next pass changed the pin from %s to %s", vmName, pin, got)
		}
		for _, name := range vm.all {
			d, a := first[name], after[name]
			if d == nil || !holds(d) || d.Status.NodeName != pin {
				t.Fatalf("VMI %s released onto %s, but GPUClaim %s is %+v", vmName, pin, name, d)
			}
			if a == nil || !holds(a) || a.Status.NodeName != pin {
				t.Fatalf("GPUClaim %s of a released set left %s: %+v", name, pin, a)
			}
		}
	}

	// A stopped VM's set with a Stay claim on a cordoned node does not move
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
				t.Fatalf("stopped VM %s: GPUClaim %s left %s for %s %s although a Stay claim pins its set",
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

func listClaims(t *testing.T, e *env) map[string]*gpuv1alpha1.GPUClaim {
	t.Helper()
	l := &gpuv1alpha1.GPUClaimList{}
	if err := e.c.List(t.Context(), l); err != nil {
		t.Fatal(err)
	}
	out := map[string]*gpuv1alpha1.GPUClaim{}
	for i := range l.Items {
		out[l.Items[i].Name] = &l.Items[i]
	}
	return out
}

func initial(objs []client.Object, name string) *gpuv1alpha1.GPUClaim {
	for _, o := range objs {
		if d, ok := o.(*gpuv1alpha1.GPUClaim); ok && d.Name == name {
			return d
		}
	}
	return nil
}
