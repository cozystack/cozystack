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
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu"
)

// scheduling is a VMI whose launcher pod waits at the gate, with refs.
func scheduling(name string, entries ...string) client.Object {
	return refs(vmiObj(name, "Scheduling", "", ""), entries...)
}

func expectWaiting(t *testing.T, d *gpuv1alpha1.GPUClaim, reason string, mentions ...string) {
	t.Helper()
	c := waiting(d)
	if c == nil || c.Reason != reason {
		t.Fatalf("GPUClaim %s: condition %+v, want Waiting with reason %s", d.Name, c, reason)
	}
	for _, m := range mentions {
		if !strings.Contains(c.Message, m) {
			t.Fatalf("GPUClaim %s: message %q does not name %q", d.Name, c.Message, m)
		}
	}
}

func TestSetOnOneServingNodeIsReleasedThereWithoutAMove(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		node("n2", map[string]int64{rnL40S: 8}),
		claim("a", "l40s", 1, bound("n1"), attached("vm1")),
		scheduling("vm1", "a"),
		gatedPod("vm1"),
	)
	e.pass()
	expectPlacement(t, e.claim("a"), gpuv1alpha1.GPUClaimAttached, "n1")
	p := e.pod("virt-launcher-vm1")
	expectReleased(t, p, "n1")
	terms := p.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	if len(terms) != 1 || len(terms[0].MatchExpressions) != 1 || len(terms[0].MatchFields) != 1 {
		t.Fatalf("the pin must narrow KubeVirt's term in place, got %+v", terms)
	}
}

// A gated pod's required terms can be narrowed but not added to, so the
// pin goes into every term, and is the only term when there is none. An
// empty term matches no node and cannot take a requirement.
func TestThePinNarrowsEveryRequiredTermOrIsTheOnlyOne(t *testing.T) {
	bare := gatedPod("vm1")
	bare.Spec.Affinity = nil
	two := gatedPod("vm2")
	terms := &two.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	*terms = append(*terms, corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "zone", Operator: corev1.NodeSelectorOpIn, Values: []string{"a"}}}}, corev1.NodeSelectorTerm{})
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		claim("a", "l40s", 1, bound("n1"), attached("vm1")),
		claim("b", "l40s", 1, bound("n1"), attached("vm2")),
		scheduling("vm1", "a"), bare,
		scheduling("vm2", "b"), two,
	)
	e.pass()
	got := e.pod("virt-launcher-vm1").Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	if len(got) != 1 || len(got[0].MatchFields) != 1 || got[0].MatchFields[0].Values[0] != "n1" {
		t.Fatalf("a pod without terms gets one pinning term, got %+v", got)
	}
	got = e.pod("virt-launcher-vm2").Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	if len(got) != 3 || len(got[0].MatchFields) != 1 || len(got[1].MatchFields) != 1 || len(got[2].MatchFields) != 0 {
		t.Fatalf("every non-empty term carries the pin, the empty one stays empty: %+v", got)
	}
}

func TestSetOfTwoClassesIsGatheredOntoOneNode(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		class("h100", rnH100),
		node("n1", map[string]int64{rnL40S: 2}),
		node("n2", map[string]int64{rnH100: 2}),
		node("both", map[string]int64{rnL40S: 2, rnH100: 2}),
		claim("a", "l40s", 1, bound("n1"), attached("vm1")),
		claim("b", "h100", 1, bound("n2"), attached("vm1")),
		scheduling("vm1", "a", "b"),
		gatedPod("vm1"),
	)
	e.pass()
	for _, name := range []string{"a", "b"} {
		expectPlacement(t, e.claim(name), gpuv1alpha1.GPUClaimAttached, "both")
	}
	expectReleased(t, e.pod("virt-launcher-vm1"), "both")
}

func TestASetNoNodeFitsKeepsItsPodGatedAndNamesEachClaimAndNode(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		class("h100", rnH100),
		node("n1", map[string]int64{rnL40S: 2}),
		node("n2", map[string]int64{rnH100: 2}),
		claim("a", "l40s", 1, bound("n1"), attached("vm1")),
		claim("b", "h100", 1, bound("n2"), attached("vm1")),
		scheduling("vm1", "a", "b"),
		gatedPod("vm1"),
	)
	e.pass()
	expectGated(t, e.pod("virt-launcher-vm1"))
	for _, name := range []string{"a", "b"} {
		expectWaiting(t, e.claim(name), ReasonCapacity, "GPUClaim a (Attached, n1)", "GPUClaim b (Attached, n2)")
	}
	expectPlacement(t, e.claim("a"), gpuv1alpha1.GPUClaimAttached, "n1")
	expectPlacement(t, e.claim("b"), gpuv1alpha1.GPUClaimAttached, "n2")

	e.create(node("both", map[string]int64{rnL40S: 2, rnH100: 2}))
	e.pass()
	expectReleased(t, e.pod("virt-launcher-vm1"), "both")
	if waiting(e.claim("a")) != nil {
		t.Fatal("the condition stays after the set was placed")
	}
}

// The controller creates a claimTemplate entry's claim, named after the
// VMI and the entry's index, marked and owned by the VMI, and binds it on
// the node of the held set.
func TestTemplateClaimIsCreatedAndBoundOnTheNodeOfTheHeldSet(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		node("n2", map[string]int64{rnL40S: 1}),
		claim("held", "l40s", 1, bound("n1"), attached("vm1")),
		scheduling("vm1", "held", "l40s:1"),
		gatedPod("vm1"),
	)
	e.pass()
	d := e.claim("vm1-gpu-1")
	expectPlacement(t, d, gpuv1alpha1.GPUClaimAttached, "n1")
	if !isTemplate(d) || vmiOwner(d) != "vmi-uid-vm1" || d.Status.AttachedTo == nil || d.Status.AttachedTo.VMI != "vm1" {
		t.Fatalf("template claim: labels %v, owners %+v, attachment %+v", d.Labels, d.OwnerReferences, d.Status.AttachedTo)
	}
	if d.Spec.ClassName != "l40s" || d.Spec.Count != 1 {
		t.Fatalf("template claim spec %+v", d.Spec)
	}
	expectPlacement(t, e.claim("held"), gpuv1alpha1.GPUClaimAttached, "n1")
	expectReleased(t, e.pod("virt-launcher-vm1"), "n1")

	list := &gpuv1alpha1.GPUClaimList{}
	if err := e.c.List(t.Context(), list); err != nil || len(list.Items) != 2 {
		t.Fatalf("repeated passes must create the template claim once: %d claims, %v", len(list.Items), err)
	}
}

func TestHeldClaimMovesWhenItsNodeHasNoRoomForTheTemplateClaim(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 1}),
		node("n2", map[string]int64{rnL40S: 2}),
		claim("held", "l40s", 1, bound("n1"), attached("vm1")),
		scheduling("vm1", "held", "l40s:1"),
		gatedPod("vm1"),
	)
	e.pass()
	expectPlacement(t, e.claim("held"), gpuv1alpha1.GPUClaimAttached, "n2")
	expectPlacement(t, e.claim("vm1-gpu-1"), gpuv1alpha1.GPUClaimAttached, "n2")
	expectReleased(t, e.pod("virt-launcher-vm1"), "n2")
}

// The second template claim of the set counts the first against the
// budget; a refusal keeps the pod gated and leaves every claim unbound.
func TestTemplateClaimsOverBudgetKeepThePodGated(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 4}),
		tenant("acme", map[string]string{"gpu.cozystack.io/l40s": "1"}),
		scheduling("vm1", "l40s:1", "l40s:1"),
		gatedPod("vm1"),
	)
	e.pass()
	expectGated(t, e.pod("virt-launcher-vm1"))
	for _, name := range []string{"vm1-gpu-0", "vm1-gpu-1"} {
		d := e.claim(name)
		expectPlacement(t, d, gpuv1alpha1.GPUClaimPending, "")
		expectWaiting(t, d, ReasonBudget, "gpu.cozystack.io/l40s")
	}
	if e.recordExists("vm1-gpu-0") {
		t.Fatal("a refused template claim has a usage record")
	}
}

func TestVGPUTemplateClaimOfAnUnentitledTenantKeepsThePodGated(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S, vgpu),
		node("n1", map[string]int64{rnL40S: 4}),
		namespace(ns, false),
		scheduling("vm1", "l40s:1"),
		gatedPod("vm1"),
	)
	e.pass()
	expectGated(t, e.pod("virt-launcher-vm1"))
	expectWaiting(t, e.claim("vm1-gpu-0"), "NotEntitled")
}

// A held claim the webhook has not attached to this VMI is not its own:
// the pod waits and nothing is placed for it.
func TestASetWithAClaimNotAttachedToTheVMIIsNotActedOn(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		node("n2", map[string]int64{rnL40S: 2}),
		claim("a", "l40s", 1, bound("n1"), attached("vm1")),
		claim("b", "l40s", 1, bound("n2")),
		claim("c", "l40s", 1, bound("n2"), attached("vm2")),
		vmiObj("vm2", "Running", "n2", ""),
		scheduling("vm1", "a", "b", "c"),
		gatedPod("vm1"),
	)
	e.pass()
	expectGated(t, e.pod("virt-launcher-vm1"))
	expectPlacement(t, e.claim("a"), gpuv1alpha1.GPUClaimAttached, "n1")
	expectPlacement(t, e.claim("b"), gpuv1alpha1.GPUClaimBound, "n2")
}

func TestTwoGatedPodsForTheLastUnitTheOlderWins(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 1}),
		scheduling("vm1", "l40s:1"),
		gatedPod("vm1"),
		scheduling("vm2", "l40s:1"),
		gatedPod("vm2"),
	)
	e.pass()
	expectReleased(t, e.pod("virt-launcher-vm1"), "n1")
	expectGated(t, e.pod("virt-launcher-vm2"))
	expectPlacement(t, e.claim("vm2-gpu-0"), gpuv1alpha1.GPUClaimPending, "")
	expectWaiting(t, e.claim("vm2-gpu-0"), ReasonCapacity)
}

// A tenant's own claim is never taken as a template claim, even under the
// template claim's name.
func TestAnUnmarkedClaimUnderTheTemplateNameIsNotTaken(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 4}),
		claim("held", "l40s", 1, bound("n1"), attached("vm1")),
		claim("vm1-gpu-1", "l40s", 1, bound("n1")),
		scheduling("vm1", "held", "l40s:1"),
		gatedPod("vm1"),
	)
	e.pass()
	expectGated(t, e.pod("virt-launcher-vm1"))
	expectWaiting(t, e.claim("held"), ReasonNameTaken, "vm1-gpu-1")
	if d := e.claim("vm1-gpu-1"); d.Status.AttachedTo != nil || isTemplate(d) {
		t.Fatalf("the tenant's claim was taken: %+v", d)
	}

	e.delete(e.claim("vm1-gpu-1"))
	e.pass()
	expectReleased(t, e.pod("virt-launcher-vm1"), "n1")
	if c := waiting(e.claim("held")); c != nil {
		t.Fatalf("the held claim keeps its condition once the set is placed: %+v", c)
	}
}

// A template claim still owned by the VM's previous VMI is waited for,
// not reused; once it is gone the new VMI gets one of its own.
func TestATemplateClaimOfThePreviousVMIIsWaitedFor(t *testing.T) {
	old := claim("vm1-gpu-0", "l40s", 1, template("vm1"), bound("n1"))
	old.OwnerReferences[0].UID = "vmi-uid-previous"
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 4}),
		old,
		scheduling("vm1", "l40s:1"),
		gatedPod("vm1"),
	)
	if _, err := e.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: passKey}); err != nil {
		t.Fatalf("pass: %v", err)
	}
	expectGated(t, e.pod("virt-launcher-vm1"))
	if !e.gone(old) {
		if d := e.claim("vm1-gpu-0"); vmiOwner(d) != "vmi-uid-previous" {
			t.Fatalf("the previous VMI's template claim was taken over: %+v", d.OwnerReferences)
		}
	}
	e.pass()
	if d := e.claim("vm1-gpu-0"); vmiOwner(d) != "vmi-uid-vm1" {
		t.Fatalf("the new VMI's template claim: %+v", d.OwnerReferences)
	}
	expectReleased(t, e.pod("virt-launcher-vm1"), "n1")
}

// The window before the previous VMI's template claim is deleted: the set
// is not complete, and the claim is not taken over.
func TestTheSetWaitsWhileTheTemplateNameBelongsToThePreviousVMI(t *testing.T) {
	old := claim("vm1-gpu-0", "l40s", 1, template("vm1"), bound("n1"))
	old.OwnerReferences[0].UID = "vmi-uid-previous"
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 4}),
		old,
		scheduling("vm1", "l40s:1"),
		gatedPod("vm1"),
	)
	e.r.attachedSince, e.r.degradedSince, e.r.rejectedAs = map[string]time.Time{}, map[string]time.Time{}, map[string]nodeState{}
	w, err := e.r.load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	set, reason, _, err := e.r.claimSet(t.Context(), w, w.vmis[key(ns, "vm1")])
	if err != nil || set != nil || reason != "" {
		t.Fatalf("set %v, err %v; want no set while the name belongs to the previous VMI", describe(set), err)
	}
	if d := e.claim("vm1-gpu-0"); d.Status.AttachedTo != nil {
		t.Fatalf("the previous VMI's template claim was attached: %+v", d.Status)
	}
}

func TestASetIsGatheredOffANotReadyNode(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("down", map[string]int64{rnL40S: 2}, notReadySince(t0.Add(-time.Minute))),
		node("up", map[string]int64{rnL40S: 2}),
		claim("a", "l40s", 1, bound("down"), attached("vm1")),
		scheduling("vm1", "a"),
		gatedPod("vm1"),
	)
	e.pass()
	expectPlacement(t, e.claim("a"), gpuv1alpha1.GPUClaimAttached, "up")
	expectReleased(t, e.pod("virt-launcher-vm1"), "up")
}

// A Lost claim the VMI references is rebound for it, without a budget
// check: a Lost claim already counts.
func TestALostClaimIsReboundForItsGatedVMI(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		tenant("acme", map[string]string{"gpu.cozystack.io/l40s": "1"}),
		claim("a", "l40s", 1, lost("gone"), attached("vm1")),
		scheduling("vm1", "a"),
		gatedPod("vm1"),
	)
	e.pass()
	expectPlacement(t, e.claim("a"), gpuv1alpha1.GPUClaimAttached, "n1")
	expectReleased(t, e.pod("virt-launcher-vm1"), "n1")
}

func TestATaintTheClassToleratesDoesNotMoveTheSet(t *testing.T) {
	tolerating := func(c *gpuv1alpha1.GPUClass) {
		c.Spec.Tolerations = []gpuv1alpha1.GPUClassToleration{{Key: "gpu", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}}
	}
	e := newEnv(t,
		class("l40s", rnL40S, tolerating),
		class("h100", rnH100),
		node("tainted", map[string]int64{rnL40S: 2, rnH100: 2}, tainted("gpu", corev1.TaintEffectNoSchedule)),
		node("plain", map[string]int64{rnL40S: 2, rnH100: 2}),
		claim("a", "l40s", 1, bound("tainted"), attached("vm1")),
		claim("b", "h100", 1, bound("tainted"), attached("vm2")),
		scheduling("vm1", "a"), gatedPod("vm1"),
		scheduling("vm2", "b"), gatedPod("vm2"),
	)
	e.pass()
	expectReleased(t, e.pod("virt-launcher-vm1"), "tainted")
	// h100 does not tolerate the taint, so its set leaves the node.
	expectReleased(t, e.pod("virt-launcher-vm2"), "plain")
}

func TestAStayClaimOnACordonedNodePinsTheVMIThatHoldsOnlyIt(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S, stay),
		node("n1", map[string]int64{rnL40S: 2}, cordoned, notReadySince(t0.Add(-time.Minute))),
		node("n2", map[string]int64{rnL40S: 2}),
		claim("a", "l40s", 1, bound("n1"), attached("vm1")),
		scheduling("vm1", "a"),
		gatedPod("vm1"),
	)
	e.pass()
	expectPlacement(t, e.claim("a"), gpuv1alpha1.GPUClaimAttached, "n1")
	expectReleased(t, e.pod("virt-launcher-vm1"), "n1")
}

func TestAStayClaimOnACordonedNodeKeepsAGatherPending(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S, stay),
		class("h100", rnH100),
		node("n1", map[string]int64{rnL40S: 2, rnH100: 2}, cordoned),
		node("n2", map[string]int64{rnL40S: 2, rnH100: 2}),
		claim("a", "l40s", 1, bound("n1"), attached("vm1")),
		claim("b", "h100", 1, bound("n2"), attached("vm1")),
		scheduling("vm1", "a", "b"),
		gatedPod("vm1"),
	)
	e.pass()
	expectGated(t, e.pod("virt-launcher-vm1"))
	expectWaiting(t, e.claim("a"), ReasonCordoned)
	expectPlacement(t, e.claim("b"), gpuv1alpha1.GPUClaimAttached, "n2")
}

func TestACordonUnderMoveGathersWhenAnotherNodeHasRoom(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}, cordoned),
		node("n2", map[string]int64{rnL40S: 2}),
		claim("a", "l40s", 1, bound("n1"), attached("vm1")),
		scheduling("vm1", "a"),
		gatedPod("vm1"),
	)
	e.pass()
	expectPlacement(t, e.claim("a"), gpuv1alpha1.GPUClaimAttached, "n2")
	expectReleased(t, e.pod("virt-launcher-vm1"), "n2")
}

func TestACordonUnderMoveWithNoRoomElsewherePinsInPlace(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}, cordoned),
		node("n2", map[string]int64{rnL40S: 2}),
		claim("other", "l40s", 2, bound("n2"), inNamespace("tenant-other")),
		claim("a", "l40s", 1, bound("n1"), attached("vm1")),
		scheduling("vm1", "a"),
		gatedPod("vm1"),
	)
	e.pass()
	expectPlacement(t, e.claim("a"), gpuv1alpha1.GPUClaimAttached, "n1")
	expectReleased(t, e.pod("virt-launcher-vm1"), "n1")
}

func TestOnlyTemplateClaimsGetOneNodeWithRoomForAll(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 1}),
		node("n2", map[string]int64{rnL40S: 2}),
		scheduling("vm1", "l40s:1", "l40s:1"),
		gatedPod("vm1"),
	)
	e.pass()
	expectPlacement(t, e.claim("vm1-gpu-0"), gpuv1alpha1.GPUClaimAttached, "n2")
	expectPlacement(t, e.claim("vm1-gpu-1"), gpuv1alpha1.GPUClaimAttached, "n2")
	expectReleased(t, e.pod("virt-launcher-vm1"), "n2")
}

// KubeVirt's temporary pod for WaitForFirstConsumer volumes is a full
// launcher render, gated like the launcher, so a local volume binds on the
// node of the claims.
func TestTheWaitForFirstConsumerTemporaryPodIsReleasedOntoTheSetsNode(t *testing.T) {
	temp := gatedPod("vm1")
	temp.Name = "virt-launcher-vm1-tmp"
	temp.UID = podUID("virt-launcher-vm1")
	temp.Annotations = map[string]string{"kubevirt.io/ephemeral-provisioning": "true"}
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		claim("a", "l40s", 1, bound("n1"), attached("vm1")),
		scheduling("vm1", "a"),
		temp,
	)
	e.pass()
	expectReleased(t, e.pod("virt-launcher-vm1-tmp"), "n1")
}

// Whoever creates a pod sets its owner reference; a gated pod its VMI does
// not list as active is not released.
func TestAGatedPodTheVMIDoesNotListIsLeftAlone(t *testing.T) {
	forged := gatedPod("vm1")
	forged.UID = "forged"
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		claim("a", "l40s", 1, bound("n1"), attached("vm1")),
		scheduling("vm1", "a"),
		forged,
	)
	e.pass()
	expectGated(t, e.pod("virt-launcher-vm1"))
}

// refused marks a pinned pod as the scheduler leaves it when the node
// cannot take it.
func refused(e *env, name string) {
	e.t.Helper()
	p := e.pod(name)
	p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse,
		Reason: corev1.PodReasonUnschedulable, Message: "0/2 nodes are available: 1 Insufficient memory."}}
	if err := e.c.Status().Update(context.Background(), p); err != nil {
		e.t.Fatalf("mark pod %s unschedulable: %v", name, err)
	}
}

// A pin cannot be undone, so a pinned pod its Ready, uncordoned node
// refuses is deleted, the node is recorded as rejected on every claim of
// the set, and the next start of the VM avoids it.
func TestAPinnedPodItsNodeRefusesIsDeletedAndTheNodeAvoided(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		node("n2", map[string]int64{rnL40S: 1}),
		claim("a", "l40s", 1, bound("n1"), attached("vm1")),
		scheduling("vm1", "a", "l40s:1"),
		gatedPod("vm1"),
	)
	e.pass()
	expectReleased(t, e.pod("virt-launcher-vm1"), "n1")
	refused(e, "virt-launcher-vm1")
	e.pass()
	if !e.gone(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "virt-launcher-vm1"}}) {
		t.Fatal("the refused pinned pod was not deleted")
	}
	for _, name := range []string{"a", "vm1-gpu-1"} {
		d := e.claim(name)
		if len(d.Status.RejectedNodes) != 1 || d.Status.RejectedNodes[0].NodeName != "n1" ||
			!d.Status.RejectedNodes[0].Until.Time.Equal(t0.Add(rejectFor)) ||
			!strings.Contains(d.Status.RejectedNodes[0].Reason, "Insufficient memory") {
			t.Fatalf("GPUClaim %s: rejected nodes %+v", name, d.Status.RejectedNodes)
		}
	}

	// KubeVirt renders a new launcher pod for a new start of the VM.
	next := gatedPod("vm1")
	e.create(next)
	e.pass()
	expectGated(t, e.pod("virt-launcher-vm1"))
	expectWaiting(t, e.claim("a"), ReasonNodeRejected, "n1")

	e.create(node("n3", map[string]int64{rnL40S: 2}))
	e.pass()
	expectReleased(t, e.pod("virt-launcher-vm1"), "n3")
	expectPlacement(t, e.claim("a"), gpuv1alpha1.GPUClaimAttached, "n3")
}

// A taint the class tolerates is not a reason to keep a refused pod: the
// node still serves the set.
func TestARefusedPinnedPodOnATaintTheClassToleratesIsDeleted(t *testing.T) {
	tolerating := func(c *gpuv1alpha1.GPUClass) {
		c.Spec.Tolerations = []gpuv1alpha1.GPUClassToleration{{Key: "gpu", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}}
	}
	for name, cls := range map[string]*gpuv1alpha1.GPUClass{"tolerated": class("l40s", rnL40S, tolerating), "not tolerated": class("l40s", rnL40S)} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t,
				cls,
				node("n1", map[string]int64{rnL40S: 2}, tainted("gpu", corev1.TaintEffectNoSchedule)),
				claim("a", "l40s", 1, bound("n1"), attached("vm1")),
				scheduling("vm1", "a"),
				pinnedPod("vm1", "n1"),
			)
			refused(e, "virt-launcher-vm1")
			e.pass()
			deleted := e.gone(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "virt-launcher-vm1"}})
			if deleted != (name == "tolerated") {
				t.Fatalf("refused pod deleted = %v", deleted)
			}
		})
	}
}

// A pod pinned to a node that does not hold its whole set is not the
// node's refusal of the set, so it is not recorded against the node.
func TestARefusedPodPinnedAwayFromItsClaimsIsNotDeleted(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		node("n2", map[string]int64{rnL40S: 2}),
		claim("a", "l40s", 1, bound("n2"), attached("vm1")),
		scheduling("vm1", "a"),
		pinnedPod("vm1", "n1"),
	)
	refused(e, "virt-launcher-vm1")
	e.pass()
	if e.gone(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "virt-launcher-vm1"}}) {
		t.Fatal("a pod pinned away from its claims was deleted as a refusal of that node")
	}
}

// A cordon, a node that is not Ready and a Stay pin keep a pinned pod
// waiting where it is: they are not a refusal of the node.
func TestAPinnedPodOnACordonedOrNotReadyNodeIsNotDeleted(t *testing.T) {
	for name, opt := range map[string]nodeOpt{"cordoned": cordoned, "NotReady": notReadySince(t0.Add(-time.Minute))} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t,
				class("l40s", rnL40S),
				node("n1", map[string]int64{rnL40S: 2}, opt),
				claim("a", "l40s", 1, bound("n1"), attached("vm1")),
				scheduling("vm1", "a"),
				pinnedPod("vm1", "n1"),
			)
			refused(e, "virt-launcher-vm1")
			e.pass()
			if e.gone(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "virt-launcher-vm1"}}) {
				t.Fatal("a pinned pod on a cordoned or NotReady node was deleted")
			}
			if len(e.claim("a").Status.RejectedNodes) != 0 {
				t.Fatal("a cordoned or NotReady node was recorded as rejected")
			}
		})
	}
}

// A rejection ends when its until passes, or earlier when the node changes:
// its allocatable, its taints or its readiness.
func TestARejectionEndsWhenTheNodeChangesOrUntilPasses(t *testing.T) {
	rejectedOn := func(d *gpuv1alpha1.GPUClaim) {
		d.Status.RejectedNodes = []gpuv1alpha1.RejectedNode{{NodeName: "n1", Until: metav1.NewTime(t0.Add(rejectFor))}}
	}
	for name, change := range map[string]func(e *env){
		"until passes": func(e *env) { e.now = t0.Add(rejectFor) },
		"allocatable": func(e *env) {
			n := &corev1.Node{}
			_ = e.c.Get(t.Context(), types.NamespacedName{Name: "n1"}, n)
			n.Status.Allocatable[corev1.ResourceMemory] = resource.MustParse("1Gi")
			e.updateStatus(n)
		},
		"Ready transition": func(e *env) {
			n := &corev1.Node{}
			_ = e.c.Get(t.Context(), types.NamespacedName{Name: "n1"}, n)
			n.Status.Conditions[0].LastTransitionTime = metav1.NewTime(t0.Add(time.Minute))
			e.updateStatus(n)
			e.now = t0.Add(2 * time.Minute)
		},
	} {
		t.Run(name, func(t *testing.T) {
			// A taint's TimeAdded is a pointer, so the node read again is
			// equal but not identical.
			soft := func(n *corev1.Node) {
				n.Spec.Taints = append(n.Spec.Taints, corev1.Taint{Key: "soft", Effect: corev1.TaintEffectPreferNoSchedule, TimeAdded: &metav1.Time{Time: t0}})
			}
			e := newEnv(t,
				class("l40s", rnL40S),
				node("n1", map[string]int64{rnL40S: 2}, soft),
				claim("a", "l40s", 1, bound("n1"), attached("vm1"), rejectedOn),
				scheduling("vm1", "a"),
				gatedPod("vm1"),
			)
			// The rejection was recorded by this controller, which knows
			// how the node looked then.
			e.r.rejectedAs = map[string]nodeState{}
			e.r.attachedSince = map[string]time.Time{}
			e.r.degradedSince = map[string]time.Time{}
			n := &corev1.Node{}
			_ = e.c.Get(t.Context(), types.NamespacedName{Name: "n1"}, n)
			e.r.rejectedAs["n1"] = stateOf(n)
			e.pass()
			expectGated(t, e.pod("virt-launcher-vm1"))
			change(e)
			e.pass()
			if d := e.claim("a"); len(d.Status.RejectedNodes) != 0 {
				t.Fatalf("rejection kept: %+v", d.Status.RejectedNodes)
			}
			expectReleased(t, e.pod("virt-launcher-vm1"), "n1")
		})
	}
}

// A VMI whose pod is pinned is deleted when its claim's node goes, so the
// claim can move; a VMI whose pod is still gated, or not created yet, is
// not: its set is gathered at the gate.
func TestOnlyAPinnedVMIIsDeletedWhenItsClaimsNodeGoes(t *testing.T) {
	pinned := vmiObj("vm1", "Scheduling", "", "vm")
	gatedVMI := refs(vmiObj("vm2", "Scheduling", "", "vm-b"), "b")
	// KubeVirt has not created this one's pod yet; the pod webhook will gate it.
	podless := vmiObj("vm3", "Pending", "", "vm-c")
	// No node serves this one's class, so its set waits at the gate.
	stuck := refs(vmiObj("vm4", "Scheduling", "", "vm-d"), "d")
	released := gatedPod("vm1")
	released.Spec.SchedulingGates = nil
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n2", map[string]int64{rnL40S: 2}),
		claim("a", "l40s", 1, bound("gone"), attached("vm1")),
		claim("b", "l40s", 1, bound("gone"), attached("vm2")),
		claim("c", "l40s", 1, bound("gone"), attached("vm3")),
		vmObj("vm", "Always", "a"),
		vmObj("vm-b", "Always", "b"),
		vmObj("vm-c", "Always", "c"),
		podless,
		class("h100", rnH100),
		claim("d", "h100", 1, bound("gone"), attached("vm4")),
		vmObj("vm-d", "Always", "d"),
		stuck, gatedPod("vm4"),
		pinned, released,
		gatedVMI, gatedPod("vm2"),
	)
	e.pass()
	if !e.gone(pinned) {
		t.Fatal("the pinned VMI of a claim on a gone node was kept")
	}
	if e.gone(gatedVMI) || e.gone(podless) || e.gone(stuck) {
		t.Fatal("a VMI whose pod is still gated, or not created yet, was deleted")
	}
	expectPlacement(t, e.claim("b"), gpuv1alpha1.GPUClaimAttached, "n2")
	expectReleased(t, e.pod("virt-launcher-vm2"), "n2")
}

// newEnvFailingOnce fails the first status write of claim name with a
// conflict, as a webhook writing the same claim in between would.
func newEnvFailingOnce(t *testing.T, name string, objs ...client.Object) *env {
	t.Helper()
	failed := false
	c := fake.NewClientBuilder().WithScheme(testScheme(true)).WithObjects(objs...).
		WithStatusSubresource(&gpuv1alpha1.GPUClass{}, &gpuv1alpha1.GPUClaim{}, &gpuv1alpha1.GPUNode{}).
		WithInterceptorFuncs(interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, cl client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			if obj.GetName() == name && !failed {
				failed = true
				return apierrors.NewConflict(gpuv1alpha1.GroupVersion.WithResource("gpuclaims").GroupResource(), name, nil)
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
		claim("a", "l40s", 1, bound("n1"), attached("vm1")),
		claim("b", "l40s", 1, bound("n2"), attached("vm1")),
		claim("o", "l40s", 1),
		scheduling("vm1", "a", "b"),
		gatedPod("vm1"),
	)
	res, err := e.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: passKey})
	if err != nil || res.RequeueAfter == 0 {
		t.Fatalf("a conflict must requeue: %v, %+v", err, res)
	}
	held := map[string]int32{}
	for _, name := range []string{"a", "b", "o"} {
		if d := e.claim(name); holds(d) {
			held[d.Status.NodeName] += d.Spec.Count
		}
	}
	if held["n1"] > 1 || held["n2"] > 2 {
		t.Fatalf("overbooked after a failed write: %v", held)
	}
	expectGated(t, e.pod("virt-launcher-vm1"))
	e.pass()
	expectPlacement(t, e.claim("a"), gpuv1alpha1.GPUClaimAttached, "n2")
	expectPlacement(t, e.claim("o"), gpuv1alpha1.GPUClaimBound, "n1")
	expectReleased(t, e.pod("virt-launcher-vm1"), "n2")
}

// A Lost claim holds no units, so a VMI that still runs on a node that came
// back holds them through its pod; a gated pod's template claim must see
// them taken.
func TestAGatedSetCountsThePodOfALostClaimsRunningVMI(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		claim("b", "l40s", 2, lost("n1"), attached("vm1")),
		vmiObj("vm1", "Running", "n1", ""),
		launcherPod("virt-launcher-vm1", "vm1", "n1", map[string]int64{rnL40S: 2}),
		scheduling("vm2", "l40s:1"),
		gatedPod("vm2"),
	)
	if _, err := e.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: passKey}); err != nil {
		t.Fatalf("pass: %v", err)
	}
	expectGated(t, e.pod("virt-launcher-vm2"))
}

func TestAFailedRebindWriteKeepsTheSetTogether(t *testing.T) {
	e := newEnvFailingOnce(t, "b",
		class("l40s", rnL40S),
		node("n0", map[string]int64{rnL40S: 1}),
		node("n1", map[string]int64{rnL40S: 2}),
		claim("a", "l40s", 1, lost("gone")),
		claim("b", "l40s", 1, lost("gone")),
		vmObj("vm", "Always", "a", "b"),
	)
	e.pass()
	expectPlacement(t, e.claim("a"), gpuv1alpha1.GPUClaimBound, "n1")
	expectPlacement(t, e.claim("b"), gpuv1alpha1.GPUClaimBound, "n1")
}

func TestAFailedOrderWriteCreatesNoUsageRecord(t *testing.T) {
	e := newEnvFailingOnce(t, "o",
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		claim("o", "l40s", 1),
	)
	if _, err := e.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: passKey}); err != nil {
		t.Fatalf("pass: %v", err)
	}
	if e.claim("o").Status.Phase != "" || e.recordExists("o") {
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
		claim("a", "l40s", 1, bound("gone")),
		claim("b", "l40s", 1, bound("gone")),
		vmObj("vm", "Always", "a", "b"),
	)
	e.pass()
	expectPlacement(t, e.claim("a"), gpuv1alpha1.GPUClaimLost, "gone")
	expectPlacement(t, e.claim("b"), gpuv1alpha1.GPUClaimLost, "gone")
}

func TestASetWriteStopsAtTheFirstFailure(t *testing.T) {
	e := newEnvFailingOnce(t, "a",
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		claim("a", "l40s", 1, lost("gone")),
		claim("b", "l40s", 1, lost("gone")),
		vmObj("vm", "Always", "a", "b"),
	)
	res, err := e.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: passKey})
	if err != nil || res.RequeueAfter != conflictRetry {
		t.Fatalf("want a retry after %v, got %v, %+v", conflictRetry, err, res)
	}
	expectPlacement(t, e.claim("b"), gpuv1alpha1.GPUClaimLost, "gone")
}

// A Stay claim pins a mixed set to its cordoned node; the pass must not
// then delete, through the Move member, the VMI whose pod it just pinned.
func TestAStayPinnedMixedSetKeepsItsVMI(t *testing.T) {
	v := refs(vmiObj("vm1", "Scheduling", "", "vm"), "a", "b")
	e := newEnv(t,
		class("l40s", rnL40S, stay),
		class("h100", rnH100),
		node("n1", map[string]int64{rnL40S: 1, rnH100: 1}, cordoned),
		node("n2", map[string]int64{rnL40S: 1, rnH100: 1}),
		claim("a", "l40s", 1, bound("n1"), attached("vm1")),
		claim("b", "h100", 1, bound("n1"), attached("vm1")),
		vmObj("vm", "Always", "a", "b"),
		v, gatedPod("vm1"),
	)
	e.pass()
	expectReleased(t, e.pod("virt-launcher-vm1"), "n1")
	if e.gone(v) {
		t.Fatal("the pass deleted the VMI whose pod it had just pinned")
	}
}

func TestAStayClaimOnACordonedNodeThatLostCapacityIsGathered(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S, stay),
		node("n1", map[string]int64{rnL40S: 1}, cordoned, notReadySince(t0.Add(-time.Hour))),
		node("n2", map[string]int64{rnL40S: 1}),
		claim("a", "l40s", 1, bound("n1"), attached("vm1")),
		scheduling("vm1", "a"),
		gatedPod("vm1"),
	)
	e.pass()
	expectPlacement(t, e.claim("a"), gpuv1alpha1.GPUClaimAttached, "n2")
	expectReleased(t, e.pod("virt-launcher-vm1"), "n2")
}

// Lost capacity on any member held on the pinned node breaks a Stay pin,
// so the set is gathered and its pod released elsewhere, and the VMI is
// kept.
func TestLostCapacityOnAMoveMemberBreaksTheStayPin(t *testing.T) {
	gn := &gpuv1alpha1.GPUNode{}
	gn.Name = "n1"
	gn.Status.ResourceNames = []string{rnH100}
	gn.Status.Devices = []gpuv1alpha1.GPUNodeDevice{
		{PCIAddress: "0000:41:00.0", Function: gpuv1alpha1.PCIFunctionWhole, ResourceName: rnH100, Healthy: ptr.To(false)},
	}
	v := refs(vmiObj("vm1", "Scheduling", "", "vm"), "a", "b")
	e := newEnv(t,
		class("l40s", rnL40S, stay),
		class("h100", rnH100),
		node("n1", map[string]int64{rnL40S: 1, rnH100: 1}, cordoned),
		node("n2", map[string]int64{rnL40S: 1, rnH100: 1}),
		gn,
		claim("a", "l40s", 1, bound("n1"), attached("vm1")),
		claim("b", "h100", 1, bound("n1"), attached("vm1")),
		vmObj("vm", "Always", "a", "b"),
		v, gatedPod("vm1"),
	)
	e.r.attachedSince = map[string]time.Time{}
	e.r.rejectedAs = map[string]nodeState{}
	e.r.degradedSince = map[string]time.Time{rnH100 + "@n1": t0.Add(-time.Hour)}
	e.pass()
	for _, name := range []string{"a", "b"} {
		expectPlacement(t, e.claim(name), gpuv1alpha1.GPUClaimAttached, "n2")
	}
	expectReleased(t, e.pod("virt-launcher-vm1"), "n2")
	if e.gone(v) {
		t.Fatal("the pass deleted the VMI whose set it had just gathered")
	}
}

// A node that no longer serves a member's class breaks the Stay pin the
// same way lost capacity does.
func TestANodeThatNoLongerServesAMoveMemberBreaksTheStayPin(t *testing.T) {
	v := refs(vmiObj("vm1", "Scheduling", "", "vm"), "a", "b")
	e := newEnv(t,
		class("l40s", rnL40S, stay),
		class("h100", rnH100, selecting("pool", "b")),
		node("n1", map[string]int64{rnL40S: 1, rnH100: 1}, cordoned, labelled("pool", "a")),
		node("n2", map[string]int64{rnL40S: 1, rnH100: 1}, labelled("pool", "b")),
		claim("a", "l40s", 1, bound("n1"), attached("vm1")),
		claim("b", "h100", 1, bound("n1"), attached("vm1")),
		vmObj("vm", "Always", "a", "b"),
		v, gatedPod("vm1"),
	)
	e.pass()
	for _, name := range []string{"a", "b"} {
		expectPlacement(t, e.claim(name), gpuv1alpha1.GPUClaimAttached, "n2")
	}
	expectReleased(t, e.pod("virt-launcher-vm1"), "n2")
	if e.gone(v) {
		t.Fatal("the pass deleted the VMI whose set it had just gathered")
	}
}

var _ = meta.FindStatusCondition

func withRecorder(e *env) *events.FakeRecorder {
	rec := events.NewFakeRecorder(16)
	e.r.Recorder = rec
	return rec
}

func drain(rec *events.FakeRecorder) []string {
	var out []string
	for {
		select {
		case ev := <-rec.Events:
			out = append(out, ev)
		default:
			return out
		}
	}
}

// A set that waits reports its reason on the VMI once per change of
// reason, with the reason its claims carry.
func TestAWaitingSetWarnsOnTheVMIOncePerReason(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 1}),
		claim("other", "l40s", 1, bound("n1"), inNamespace("tenant-other")),
		scheduling("vm1", "l40s:1"),
		gatedPod("vm1"),
	)
	rec := withRecorder(e)
	e.pass()
	got := drain(rec)
	if len(got) != 1 || !strings.HasPrefix(got[0], "Warning "+ReasonCapacity+" ") {
		t.Fatalf("events %q, want one Warning %s", got, ReasonCapacity)
	}
	e.pass()
	if got := drain(rec); len(got) != 0 {
		t.Fatalf("the same reason was reported again: %q", got)
	}
	e.create(tenant("acme", map[string]string{"gpu.cozystack.io/l40s": "0"}))
	e.pass()
	if got := drain(rec); len(got) != 1 || !strings.HasPrefix(got[0], "Warning "+ReasonBudget+" ") {
		t.Fatalf("a new reason must be reported once: %q", got)
	}
}

// The tenant's claim under the template name leaves the set with no other
// claim to carry the reason; the VMI carries it.
func TestATakenTemplateNameWarnsOnTheVMI(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 4}),
		claim("vm1-gpu-0", "l40s", 1, bound("n1")),
		scheduling("vm1", "l40s:1"),
		gatedPod("vm1"),
	)
	rec := withRecorder(e)
	e.pass()
	got := drain(rec)
	if len(got) != 1 || !strings.HasPrefix(got[0], "Warning "+ReasonNameTaken+" ") || !strings.Contains(got[0], "vm1-gpu-0") {
		t.Fatalf("events %q, want one Warning %s naming the claim", got, ReasonNameTaken)
	}
	expectGated(t, e.pod("virt-launcher-vm1"))
}

// pvOn is a local PersistentVolume on node, bound to PVC name.
func pvOn(name, node string) (*corev1.PersistentVolumeClaim, *corev1.PersistentVolume) {
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
	pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv-" + name}}
	if node != "" {
		pvc.Spec.VolumeName = pv.Name
		pv.Spec.NodeAffinity = &corev1.VolumeNodeAffinity{Required: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{
			MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "kubernetes.io/hostname", Operator: corev1.NodeSelectorOpIn, Values: []string{node}}},
		}}}}
	}
	return pvc, pv
}

func withVolume(u client.Object, pvc string) client.Object {
	vmi := u.(*unstructured.Unstructured)
	_ = unstructured.SetNestedSlice(vmi.Object, []any{map[string]any{"name": "disk", "dataVolume": map[string]any{"name": pvc}}}, "spec", "volumes")
	return vmi
}

func host(name string, alloc map[string]int64, opts ...nodeOpt) *corev1.Node {
	return node(name, alloc, append([]nodeOpt{labelled("kubernetes.io/hostname", name)}, opts...)...)
}

// A set whose VMI has a volume bound to a node's local PV stays on that
// node: a cordon under Move does not take it elsewhere.
func TestASetWithABoundLocalVolumeNeverLeavesItsNode(t *testing.T) {
	pvc, pv := pvOn("root", "n1")
	e := newEnv(t,
		class("l40s", rnL40S),
		host("n1", map[string]int64{rnL40S: 2}, cordoned),
		host("n2", map[string]int64{rnL40S: 2}),
		pvc, pv,
		claim("a", "l40s", 1, bound("n1"), attached("vm1")),
		withVolume(scheduling("vm1", "a"), "root"),
		gatedPod("vm1"),
	)
	e.pass()
	expectPlacement(t, e.claim("a"), gpuv1alpha1.GPUClaimAttached, "n1")
	expectReleased(t, e.pod("virt-launcher-vm1"), "n1")
}

// When the volume's node cannot take the set, the pod waits with a reason
// rather than going where its volume is not.
func TestASetWhoseVolumeNodeHasNoRoomWaits(t *testing.T) {
	pvc, pv := pvOn("root", "n1")
	e := newEnv(t,
		class("l40s", rnL40S),
		host("n1", map[string]int64{rnL40S: 0}),
		host("n2", map[string]int64{rnL40S: 2}),
		pvc, pv,
		withVolume(scheduling("vm1", "l40s:1"), "root"),
		gatedPod("vm1"),
	)
	e.pass()
	expectGated(t, e.pod("virt-launcher-vm1"))
	expectWaiting(t, e.claim("vm1-gpu-0"), ReasonNodeAffinity)
}

// A WaitForFirstConsumer PVC not bound yet constrains nothing.
func TestAnUnboundVolumeAddsNoConstraint(t *testing.T) {
	pvc, pv := pvOn("root", "")
	e := newEnv(t,
		class("l40s", rnL40S),
		host("n1", map[string]int64{rnL40S: 0}),
		host("n2", map[string]int64{rnL40S: 2}),
		pvc, pv,
		withVolume(scheduling("vm1", "l40s:1"), "root"),
		gatedPod("vm1"),
	)
	e.pass()
	expectReleased(t, e.pod("virt-launcher-vm1"), "n2")
}

// The template claims of a refused pod's set go with its failed VMI. The
// VM's next VMI still avoids the node: the controller keeps the rejection
// for the VM. A VMI without a VM has no next VMI and leaves nothing.
func TestTheNextVMIOfAVMAvoidsTheNodeItsTemplateSetWasRefusedOn(t *testing.T) {
	v := refs(vmiObj("vm1", "Scheduling", "", "vm"), "l40s:1")
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 1}),
		node("n2", map[string]int64{rnL40S: 2}),
		vmObj("vm", "Always"),
		v, gatedPod("vm1"),
	)
	e.pass()
	expectReleased(t, e.pod("virt-launcher-vm1"), "n1")
	refused(e, "virt-launcher-vm1")
	e.pass()

	// KubeVirt fails the VMI and the VM controller creates the next one.
	e.delete(v)
	next := refs(vmiObj("vm1", "Scheduling", "", "vm"), "l40s:1")
	next.SetUID("vmi-uid-vm1-next")
	e.create(next)
	p := gatedPod("vm1")
	p.OwnerReferences[0].UID = "vmi-uid-vm1-next"
	e.create(p)
	e.pass()
	if d := e.claim("vm1-gpu-0"); vmiOwner(d) != "vmi-uid-vm1-next" {
		t.Fatalf("the next VMI's template claim: %+v", d.OwnerReferences)
	}
	expectReleased(t, e.pod("virt-launcher-vm1"), "n2")

	lone := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		scheduling("vm1", "l40s:1"), gatedPod("vm1"),
	)
	lone.pass()
	refused(lone, "virt-launcher-vm1")
	lone.pass()
	if len(lone.r.vmRejected) != 0 {
		t.Fatalf("a VMI without a VM left rejections behind: %v", lone.r.vmRejected)
	}
}

func TestAPVNodeAffinityIsReadAsTheSchedulerReadsIt(t *testing.T) {
	n := node("n1", nil, labelled("zone", "a"), labelled("size", "8"))
	req := func(key string, op corev1.NodeSelectorOperator, values ...string) corev1.NodeSelectorRequirement {
		return corev1.NodeSelectorRequirement{Key: key, Operator: op, Values: values}
	}
	for name, tc := range map[string]struct {
		terms []corev1.NodeSelectorTerm
		want  bool
	}{
		"In":                 {[]corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{req("zone", corev1.NodeSelectorOpIn, "a")}}}, true},
		"NotIn":              {[]corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{req("zone", corev1.NodeSelectorOpNotIn, "a")}}}, false},
		"Gt":                 {[]corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{req("size", corev1.NodeSelectorOpGt, "4")}}}, true},
		"requirements ANDed": {[]corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{req("zone", corev1.NodeSelectorOpIn, "a"), req("size", corev1.NodeSelectorOpLt, "4")}}}, false},
		"terms ORed":         {[]corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{req("zone", corev1.NodeSelectorOpIn, "b")}}, {MatchFields: []corev1.NodeSelectorRequirement{req(nodeNameField, corev1.NodeSelectorOpIn, "n1")}}}, true},
		"field NotIn":        {[]corev1.NodeSelectorTerm{{MatchFields: []corev1.NodeSelectorRequirement{req(nodeNameField, corev1.NodeSelectorOpNotIn, "n1")}}}, false},
		"empty term":         {[]corev1.NodeSelectorTerm{{}}, false},
		"unknown operator":   {[]corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{req("zone", "Like", "a")}}}, false},
	} {
		if got := matchesNodeSelector(n, &corev1.NodeSelector{NodeSelectorTerms: tc.terms}); got != tc.want {
			t.Errorf("%s: matches = %v, want %v", name, got, tc.want)
		}
	}
}

// A cordon alone keeps a set in place only where its volume is; a set
// whose volume is on another node waits for room there.
func TestACordonedSetAwayFromItsVolumeWaits(t *testing.T) {
	pvc, pv := pvOn("root", "n2")
	e := newEnv(t,
		class("l40s", rnL40S),
		host("n1", map[string]int64{rnL40S: 2}, cordoned),
		host("n2", map[string]int64{rnL40S: 1}),
		claim("other", "l40s", 1, bound("n2"), inNamespace("tenant-other")),
		pvc, pv,
		claim("a", "l40s", 1, bound("n1"), attached("vm1")),
		withVolume(scheduling("vm1", "a"), "root"),
		gatedPod("vm1"),
	)
	e.pass()
	expectGated(t, e.pod("virt-launcher-vm1"))
	expectWaiting(t, e.claim("a"), ReasonNodeAffinity)
}

// A rejection kept for a VM ends with its until, as one on a claim does.
func TestAVMRejectionEndsWithItsUntil(t *testing.T) {
	e := newEnv(t, class("l40s", rnL40S), node("n1", map[string]int64{rnL40S: 2}))
	e.r.vmRejected = map[types.NamespacedName][]gpuv1alpha1.RejectedNode{
		key(ns, "vm"): {{NodeName: "n1", Until: metav1.NewTime(t0.Add(rejectFor))}},
	}
	e.pass()
	if len(e.r.vmRejected) != 1 {
		t.Fatal("a VM rejection ended before its until")
	}
	e.now = t0.Add(rejectFor)
	e.pass()
	if len(e.r.vmRejected) != 0 {
		t.Fatalf("a VM rejection outlived its until: %v", e.r.vmRejected)
	}
}

// A set held on a node its VMI's volumes do not reach leaves that node.
func TestASetHeldAwayFromItsVolumeMovesToIt(t *testing.T) {
	pvc, pv := pvOn("root", "n2")
	e := newEnv(t,
		class("l40s", rnL40S),
		host("n1", map[string]int64{rnL40S: 2}),
		host("n2", map[string]int64{rnL40S: 1}),
		pvc, pv,
		claim("a", "l40s", 1, bound("n1"), attached("vm1")),
		withVolume(scheduling("vm1", "a"), "root"),
		gatedPod("vm1"),
	)
	e.pass()
	expectPlacement(t, e.claim("a"), gpuv1alpha1.GPUClaimAttached, "n2")
	expectReleased(t, e.pod("virt-launcher-vm1"), "n2")
}

// A Stay pin cannot hold a set on a node its volumes do not reach.
func TestAStayPinAwayFromTheVolumeWaits(t *testing.T) {
	pvc, pv := pvOn("root", "n2")
	e := newEnv(t,
		class("l40s", rnL40S, stay),
		host("n1", map[string]int64{rnL40S: 2}, cordoned),
		host("n2", map[string]int64{rnL40S: 2}),
		pvc, pv,
		claim("a", "l40s", 1, bound("n1"), attached("vm1")),
		withVolume(scheduling("vm1", "a"), "root"),
		gatedPod("vm1"),
	)
	e.pass()
	expectGated(t, e.pod("virt-launcher-vm1"))
	expectWaiting(t, e.claim("a"), ReasonNodeAffinity)
}

// A claim bound to no volume yet names no PV, and the API server refuses a
// get with an empty name rather than answering NotFound.
func TestAnUnboundVolumeIsNotLookedUp(t *testing.T) {
	pvc, pv := pvOn("root", "")
	e := newEnv(t,
		class("l40s", rnL40S),
		host("n1", map[string]int64{rnL40S: 2}),
		pvc, pv,
		withVolume(scheduling("vm1", "l40s:1"), "root"),
		gatedPod("vm1"),
	)
	e.r.Client = interceptor.NewClient(e.c.(client.WithWatch), interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, k client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if k.Name == "" {
				return apierrors.NewBadRequest("resource name may not be empty")
			}
			return c.Get(ctx, k, obj, opts...)
		},
	})
	e.pass()
	expectReleased(t, e.pod("virt-launcher-vm1"), "n1")
}

// A rejection holds for every placement of the set, a rebind from Lost
// included, not only at the gate.
func TestARebindSkipsARejectedNode(t *testing.T) {
	rejectedOn := func(d *gpuv1alpha1.GPUClaim) {
		d.Status.RejectedNodes = []gpuv1alpha1.RejectedNode{{NodeName: "n1", Until: metav1.NewTime(t0.Add(rejectFor))}}
	}
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 1}),
		node("n2", map[string]int64{rnL40S: 2}),
		claim("a", "l40s", 1, lost("gone"), rejectedOn),
	)
	e.pass()
	expectPlacement(t, e.claim("a"), gpuv1alpha1.GPUClaimBound, "n2")
}

// The launcher pod's own node selector, which KubeVirt fills from the VM,
// narrows where its set goes: a set held on a node the pod cannot use
// moves to one it can.
func TestTheSetGoesWhereTheLauncherPodCanRun(t *testing.T) {
	for name, restrict := range map[string]func(*corev1.Pod){
		"node selector": func(p *corev1.Pod) { p.Spec.NodeSelector = map[string]string{"zone": "a"} },
		"required affinity": func(p *corev1.Pod) {
			terms := p.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
			terms[0].MatchExpressions = append(terms[0].MatchExpressions,
				corev1.NodeSelectorRequirement{Key: "zone", Operator: corev1.NodeSelectorOpIn, Values: []string{"a"}})
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := gatedPod("vm1")
			restrict(p)
			e := newEnv(t,
				class("l40s", rnL40S),
				node("n1", map[string]int64{rnL40S: 2}),
				node("n2", map[string]int64{rnL40S: 2}, labelled("zone", "a")),
				claim("a", "l40s", 1, bound("n1"), attached("vm1")),
				scheduling("vm1", "a"),
				p,
			)
			e.pass()
			expectReleased(t, e.pod("virt-launcher-vm1"), "n2")
			expectPlacement(t, e.claim("a"), gpuv1alpha1.GPUClaimAttached, "n2")
		})
	}
}

// A pinned pod refused by a node it no longer matches, after a label
// change, is not a refusal for lack of resources: the pod is deleted so
// the next one goes where it can run, and the node is not rejected.
func TestARefusedPodItsNodeNoLongerMatchesIsReplacedWithoutARejection(t *testing.T) {
	p := pinnedPod("vm1", "n1")
	p.Spec.NodeSelector = map[string]string{"zone": "a"}
	v := refs(vmiObj("vm1", "Scheduling", "", "vm"), "a")
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		node("n2", map[string]int64{rnL40S: 2}, labelled("zone", "a")),
		claim("a", "l40s", 1, bound("n1"), attached("vm1")),
		vmObj("vm", "Always", "a"),
		v, p,
	)
	refused(e, "virt-launcher-vm1")
	e.pass()
	if !e.gone(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "virt-launcher-vm1"}}) {
		t.Fatal("a pod pinned to a node it no longer matches was kept")
	}
	if got := e.claim("a").Status.RejectedNodes; len(got) != 0 || len(e.r.vmRejected) != 0 {
		t.Fatalf("the node was recorded as rejected: %+v %v", got, e.r.vmRejected)
	}
	next := gatedPod("vm1")
	next.Spec.NodeSelector = map[string]string{"zone": "a"}
	e.create(next)
	e.pass()
	expectReleased(t, e.pod("virt-launcher-vm1"), "n2")
}

// A set that waits only because no node with room is one the pod may use
// says so, rather than blaming capacity.
func TestAWaitForThePodsOwnSelectorNamesIt(t *testing.T) {
	p := gatedPod("vm1")
	p.Spec.NodeSelector = map[string]string{"zone": "a"}
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		claim("a", "l40s", 1, bound("n1"), attached("vm1")),
		scheduling("vm1", "a"),
		p,
	)
	e.pass()
	expectGated(t, e.pod("virt-launcher-vm1"))
	expectWaiting(t, e.claim("a"), ReasonNodeAffinity, "pod")
}

// A finished temporary pod is the only pod of the VMI once its volume is
// bound and before KubeVirt creates the launcher: it does not make the VMI
// one whose pod is pinned, so the VMI is not deleted.
func TestAFinishedTemporaryPodDoesNotPinItsVMI(t *testing.T) {
	v := vmiObj("vm1", "Scheduling", "", "vm")
	temp := pinnedPod("vm1", "gone")
	temp.Status.Phase = corev1.PodSucceeded
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n2", map[string]int64{rnL40S: 2}),
		claim("a", "l40s", 1, bound("gone"), attached("vm1")),
		vmObj("vm", "Always", "a"),
		v, temp,
	)
	e.pass()
	if e.gone(v) {
		t.Fatal("a VMI whose only pod has finished was deleted as pinned")
	}
}

// A release whose pod update fails leaves the set placed; the next pass
// releases the pod onto the same node without moving the set.
func TestAFailedReleaseIsRetriedOnTheSameNode(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 1}),
		node("n2", map[string]int64{rnL40S: 2}),
		claim("a", "l40s", 1, bound("n1"), attached("vm1")),
		scheduling("vm1", "a", "l40s:1"),
		gatedPod("vm1"),
	)
	failed := false
	e.r.Client = interceptor.NewClient(e.c.(client.WithWatch), interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if _, ok := obj.(*corev1.Pod); ok && !failed {
				failed = true
				return apierrors.NewConflict(corev1.Resource("pods"), obj.GetName(), nil)
			}
			return c.Update(ctx, obj, opts...)
		},
	})
	if _, err := e.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: passKey}); err != nil {
		t.Fatalf("a conflict must requeue, not fail: %v", err)
	}
	expectGated(t, e.pod("virt-launcher-vm1"))
	expectPlacement(t, e.claim("a"), gpuv1alpha1.GPUClaimAttached, "n2")
	e.pass()
	expectReleased(t, e.pod("virt-launcher-vm1"), "n2")
	expectPlacement(t, e.claim("vm1-gpu-1"), gpuv1alpha1.GPUClaimAttached, "n2")
}

// A failed create stops the pass, so nothing is placed on a view that
// lacks the claim; the next pass creates it.
func TestAFailedTemplateCreateStopsThePass(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		scheduling("vm1", "l40s:1", "l40s:1"),
		gatedPod("vm1"),
		claim("order", "l40s", 1),
	)
	failed := false
	e.r.Client = interceptor.NewClient(e.c.(client.WithWatch), interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*gpuv1alpha1.GPUClaim); ok && !failed {
				failed = true
				return apierrors.NewInternalError(errors.New("etcd unavailable"))
			}
			return c.Create(ctx, obj, opts...)
		},
	})
	if _, err := e.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: passKey}); err == nil {
		t.Fatal("a failed create must fail the pass")
	}
	if e.claim("order").Status.Phase != "" {
		t.Fatal("the pass went on to place an order after a failed create")
	}
	e.pass()
	expectReleased(t, e.pod("virt-launcher-vm1"), "n1")
}

// A failed delete of a refused pod records nothing yet; the next pass
// deletes the pod and records the node once.
func TestAFailedDeleteOfARefusedPodIsRetried(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		claim("a", "l40s", 1, bound("n1"), attached("vm1")),
		scheduling("vm1", "a"),
		pinnedPod("vm1", "n1"),
	)
	refused(e, "virt-launcher-vm1")
	failed := false
	e.r.Client = interceptor.NewClient(e.c.(client.WithWatch), interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if _, ok := obj.(*corev1.Pod); ok && !failed {
				failed = true
				return apierrors.NewInternalError(errors.New("etcd unavailable"))
			}
			return c.Delete(ctx, obj, opts...)
		},
	})
	if _, err := e.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: passKey}); err == nil {
		t.Fatal("a failed delete must fail the pass")
	}
	if got := e.claim("a").Status.RejectedNodes; len(got) != 0 {
		t.Fatalf("a node was rejected for a pod that was not deleted: %+v", got)
	}
	e.pass()
	if !e.gone(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "virt-launcher-vm1"}}) {
		t.Fatal("the refused pod was not deleted on the next pass")
	}
	if got := e.claim("a").Status.RejectedNodes; len(got) != 1 {
		t.Fatalf("the node is not recorded exactly once: %+v", got)
	}
}

// The gate judges a template claim under the same rule: its VMI waits on
// the unreadable budgets of its own chain only.
func TestAGatedTemplateWaitsOnlyOnUnreadableBudgetsOfItsChain(t *testing.T) {
	broken := tenant("bad", nil)
	broken.Spec.Values.Raw = []byte(`{"resourceQuotas":{"gpu.cozystack.io/l40s":"many"}}`)
	badVMI := scheduling("vm2", "l40s:1")
	badVMI.SetNamespace("tenant-bad")
	badPod := gatedPod("vm2")
	badPod.Namespace = "tenant-bad"
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 4}),
		broken,
		scheduling("vm1", "l40s:1"), gatedPod("vm1"),
		badVMI, badPod,
	)
	e.pass()
	expectReleased(t, e.pod("virt-launcher-vm1"), "n1")
	got := &corev1.Pod{}
	if err := e.c.Get(t.Context(), key("tenant-bad", "virt-launcher-vm2"), got); err != nil {
		t.Fatal(err)
	}
	expectGated(t, got)
	d := &gpuv1alpha1.GPUClaim{}
	if err := e.c.Get(t.Context(), key("tenant-bad", "vm2-gpu-0"), d); err != nil {
		t.Fatal(err)
	}
	expectWaiting(t, d, ReasonBudgetsRead, "tenant-bad ")
}

// The pass reads pods from the cache. A pod the scheduler has bound since
// that read is not deleted on the stale view, and its node is not rejected.
func TestARefusedPodBoundSinceTheReadIsKept(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		claim("a", "l40s", 1, bound("n1"), attached("vm1")),
		scheduling("vm1", "a"),
		pinnedPod("vm1", "n1"),
	)
	refused(e, "virt-launcher-vm1")
	e.r.attachedSince, e.r.degradedSince, e.r.rejectedAs = map[string]time.Time{}, map[string]time.Time{}, map[string]nodeState{}
	e.r.vmRejected = map[types.NamespacedName][]gpuv1alpha1.RejectedNode{}
	w, err := e.r.load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	p := e.pod("virt-launcher-vm1")
	p.Spec.NodeName = "n1"
	e.update(p)
	for _, err := range e.r.recoverRefused(t.Context(), w) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if e.gone(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "virt-launcher-vm1"}}) {
		t.Fatal("a pod bound since the read was deleted")
	}
	if got := e.claim("a").Status.RejectedNodes; len(got) != 0 {
		t.Fatalf("the node that took the pod was rejected: %+v", got)
	}
}

// The VM's own metadata.name requirements come before the pin in a term;
// the pin is the one the controller appended last.
func TestThePinIsReadPastTheVMsOwnNodeNames(t *testing.T) {
	p := gatedPod("vm1")
	p.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms = []corev1.NodeSelectorTerm{
		{MatchFields: []corev1.NodeSelectorRequirement{{Key: nodeNameField, Operator: corev1.NodeSelectorOpIn, Values: []string{"n0"}}}},
		{MatchFields: []corev1.NodeSelectorRequirement{{Key: nodeNameField, Operator: corev1.NodeSelectorOpIn, Values: []string{"n1"}}}},
	}
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n0", map[string]int64{rnL40S: 0}),
		node("n1", map[string]int64{rnL40S: 2}),
		scheduling("vm1", "l40s:1"),
		p,
	)
	e.pass()
	expectReleased(t, e.pod("virt-launcher-vm1"), "n1")
	refused(e, "virt-launcher-vm1")
	e.pass()
	if !e.gone(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "virt-launcher-vm1"}}) {
		t.Fatal("the pod n1 refused was kept")
	}
}

// A class selector key with an empty value must be on the node, as the
// shared model reads it: a claim on a node that lost the label moves.
func TestAnEmptyValuedSelectorKeyMustBeOnTheNode(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S, selecting("node-role.kubernetes.io/gpu", "")),
		node("n1", map[string]int64{rnL40S: 2}),
		node("n2", map[string]int64{rnL40S: 2}, labelled("node-role.kubernetes.io/gpu", "")),
		claim("a", "l40s", 1, bound("n1")),
	)
	e.pass()
	expectPlacement(t, e.claim("a"), gpuv1alpha1.GPUClaimBound, "n2")
}

// A released pod ends the wait it was warned about: KubeVirt's temporary
// pod is followed by the launcher, whose own wait is reported again.
func TestALauncherAfterAReleasedTemporaryPodIsWarnedAgain(t *testing.T) {
	temp := gatedPod("vm1")
	temp.Name = "virt-launcher-vm1-tmp"
	temp.UID = podUID("virt-launcher-vm1")
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		claim("other", "l40s", 1, bound("n1"), inNamespace("tenant-other")),
		claim("a", "l40s", 1, bound("n1"), attached("vm1")),
		scheduling("vm1", "a", "l40s:1"),
		temp,
	)
	rec := withRecorder(e)
	e.pass()
	if got := drain(rec); len(got) != 1 {
		t.Fatalf("events %q, want one for the waiting temporary pod", got)
	}
	n := &corev1.Node{}
	_ = e.c.Get(t.Context(), key("", "n1"), n)
	n.Status.Allocatable[rnL40S] = resource.MustParse("3")
	e.updateStatus(n)
	e.pass()
	expectReleased(t, e.pod("virt-launcher-vm1-tmp"), "n1")

	// The node goes NotReady before KubeVirt's launcher arrives, and no
	// other node has room for the set.
	e.delete(e.pod("virt-launcher-vm1-tmp"))
	_ = e.c.Get(t.Context(), key("", "n1"), n)
	notReadySince(t0)(n)
	e.updateStatus(n)
	e.create(gatedPod("vm1"))
	e.pass()
	if got := drain(rec); len(got) != 1 || !strings.Contains(got[0], ReasonCapacity) {
		t.Fatalf("events %q, want the launcher's wait reported", got)
	}
}

// Only the first non-empty term carries the pin. A pod whose gate someone
// else removed has none there, and a VM's own node name in a later term
// is not taken for one: the refused pod is not recovered as pinned.
func TestAVMsOwnNodeNameInALaterTermIsNotAPin(t *testing.T) {
	p := gatedPod("vm1")
	p.Spec.SchedulingGates = nil
	terms := &p.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	*terms = append(*terms, corev1.NodeSelectorTerm{
		MatchFields: []corev1.NodeSelectorRequirement{{Key: nodeNameField, Operator: corev1.NodeSelectorOpIn, Values: []string{"n1"}}},
	})
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		claim("a", "l40s", 1, bound("n1"), attached("vm1")),
		scheduling("vm1", "a"),
		p,
	)
	refused(e, "virt-launcher-vm1")
	e.pass()
	if e.gone(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "virt-launcher-vm1"}}) {
		t.Fatal("a pod the controller never pinned was deleted as refused")
	}
	if got := e.claim("a").Status.RejectedNodes; len(got) != 0 {
		t.Fatalf("the VM's own node name was rejected: %+v", got)
	}
}
