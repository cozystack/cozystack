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
	"fmt"
	"sort"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu/model"
)

// ConditionWaiting is set on a claim the binder cannot place yet, and on
// every claim of a gated launcher pod's set that cannot be placed; its
// reason names the shortage and it is removed once the claim is placed.
const ConditionWaiting = "Waiting"

const (
	ReasonCapacity     = "Capacity"
	ReasonClassDeleted = "ClassDeleted"
	ReasonCordoned     = "Cordoned"
	ReasonBudgetsRead  = "BudgetsUnavailable"
)

// setWaiting records why a claim waits and reports whether it changed.
func (w *world) setWaiting(d *gpuv1alpha1.GPUClaim, reason, message string) bool {
	return meta.SetStatusCondition(&d.Status.Conditions, metav1.Condition{
		Type: ConditionWaiting, Status: metav1.ConditionTrue, Reason: reason, Message: message,
		ObservedGeneration: d.Generation, LastTransitionTime: metav1.NewTime(w.now),
	})
}

func waitingCondition(d *gpuv1alpha1.GPUClaim) *metav1.Condition {
	return meta.FindStatusCondition(d.Status.Conditions, ConditionWaiting)
}

func onOneNode(set []*gpuv1alpha1.GPUClaim, node string) bool {
	for _, d := range set {
		if d.Status.NodeName != node {
			return false
		}
	}
	return true
}

// stayPin returns the cordoned node a claim of a Stay class pins the set
// to. The pin holds through a NotReady spell or a taint, and breaks when a
// member held on that node has lost capacity there, or the node is gone or
// no longer serves the member's class. It returns "" and true when two such
// claims sit on different nodes, and false when nothing pins the set.
// placeSet, relocateSet and relocateAttached all read the pin here, so a
// pod pinned to a set's node is never followed by the deletion of its VMI
// for the same cordon.
func (w *world) stayPin(held []*gpuv1alpha1.GPUClaim) (string, bool) {
	node, pinned := "", false
	for _, d := range held {
		n := w.nodes[d.Status.NodeName]
		if n == nil || !model.Cordoned(n) || w.onCordon(d.Spec.ClassName) != gpuv1alpha1.OnCordonStay {
			continue
		}
		if pinned && node != n.Name {
			return "", true
		}
		node, pinned = n.Name, true
	}
	if !pinned {
		return "", false
	}
	for _, d := range held {
		if d.Status.NodeName == node && w.problem(d) >= problemNotServed {
			return "", false
		}
	}
	return node, true
}

// relocate moves held claims off nodes that cannot keep them, as sets,
// and deletes unscheduled VMIs that would otherwise wait on such a node.
func (r *Reconciler) relocate(ctx context.Context, w *world) []error {
	var errs []error
	leaving := w.excessOnLostNodes()
	for _, set := range w.vmSets(func(d *gpuv1alpha1.GPUClaim) bool {
		return holds(d) && d.DeletionTimestamp.IsZero()
	}) {
		if w.aborted {
			break
		}
		var unattached []*gpuv1alpha1.GPUClaim
		for _, d := range set {
			if d.Status.AttachedTo == nil {
				unattached = append(unattached, d)
				continue
			}
			errs = append(errs, r.relocateAttached(ctx, w, d, set, leaving[d]))
		}
		if len(unattached) > 0 {
			errs = append(errs, r.relocateSet(ctx, w, unattached, leaving))
		}
	}
	return errs
}

// excessOnLostNodes picks, on each node that lost capacity for a resource
// but is still Ready, the claims that no longer fit there: unattached ones
// first, then those whose VMI has not been scheduled. A claim whose VMI
// runs keeps it; it is refused at the VMI's next start.
func (w *world) excessOnLostNodes() map[*gpuv1alpha1.GPUClaim]bool {
	out := map[*gpuv1alpha1.GPUClaim]bool{}
	for node, loss := range w.lost {
		if loss.notReady {
			continue
		}
		for rn, healthy := range loss.healthy {
			var held int64
			var candidates []*gpuv1alpha1.GPUClaim
			for _, d := range w.claims {
				if d.Status.NodeName != node || !holds(d) || w.resourceName(d.Spec.ClassName) != rn {
					continue
				}
				held += int64(d.Spec.Count)
				candidates = append(candidates, d)
			}
			rank := func(d *gpuv1alpha1.GPUClaim) int {
				if d.Status.AttachedTo == nil {
					return 0
				}
				if v := w.attachedVMI(d); v != nil && v.unscheduled() {
					return 1
				}
				return 2
			}
			sort.SliceStable(candidates, func(i, j int) bool { return rank(candidates[i]) < rank(candidates[j]) })
			for _, d := range candidates {
				if held <= healthy || rank(d) == 2 {
					break
				}
				out[d] = true
				held -= int64(d.Spec.Count)
			}
		}
	}
	return out
}

// mustLeave reports whether an unattached claim has to leave its node,
// and whether the reason is a cordon alone.
func (w *world) mustLeave(d *gpuv1alpha1.GPUClaim, excess bool) (leave, cordonOnly bool) {
	switch w.problem(d) {
	case problemLostCapacity:
		if w.lost[d.Status.NodeName].notReady || excess {
			return true, false
		}
		return false, false
	case problemGone, problemNotServed:
		return true, false
	case problemCordoned:
		return w.onCordon(d.Spec.ClassName) == gpuv1alpha1.OnCordonMove, true
	}
	return false, false
}

func (r *Reconciler) relocateSet(ctx context.Context, w *world, set []*gpuv1alpha1.GPUClaim, excess map[*gpuv1alpha1.GPUClaim]bool) error {
	hard, soft := false, false
	mustGo := map[*gpuv1alpha1.GPUClaim]bool{}
	for _, d := range set {
		leave, cordonOnly := w.mustLeave(d, excess[d])
		switch {
		case leave && cordonOnly:
			soft = true
		case leave:
			hard = true
			mustGo[d] = true
		}
	}
	_, stayPinned := w.stayPin(set)
	var errs []error
	if !hard && (!soft || stayPinned) {
		// Nothing to move, or a Stay claim on a cordoned node keeps the set.
		for _, d := range set {
			if c := meta.FindStatusCondition(d.Status.Conditions, ConditionWaiting); c != nil && c.Reason == ReasonCordoned {
				meta.RemoveStatusCondition(&d.Status.Conditions, ConditionWaiting)
				errs = append(errs, r.updateStatus(ctx, w, d))
			}
		}
		return errors.Join(errs...)
	}
	if target := w.pick(set); target != "" {
		before := map[*gpuv1alpha1.GPUClaim]string{}
		for _, d := range set {
			before[d] = d.Status.NodeName
		}
		w.placeOn(set, target)
		return r.writeSet(ctx, w, set, func(d *gpuv1alpha1.GPUClaim) bool { return before[d] != target })
	}
	if hard {
		// The set goes to Lost together, so it is rebound together.
		for _, d := range set {
			reason := fmt.Sprintf("node %s %s and no node has room for the claim's set", d.Status.NodeName, w.problem(d))
			if !mustGo[d] {
				reason = "another claim of its set must leave its node and no node has room for the whole set"
			}
			d.Status.Phase = gpuv1alpha1.GPUClaimLost
			w.setWaiting(d, ReasonCapacity, reason)
			w.setClaim(d)
		}
		return r.writeSet(ctx, w, set, func(*gpuv1alpha1.GPUClaim) bool { return true })
	}
	cordonedNode := ""
	for _, d := range set {
		if w.problem(d) == problemCordoned {
			cordonedNode = d.Status.NodeName
		}
	}
	for _, d := range set {
		msg := fmt.Sprintf("node %s is cordoned; waiting for room on another node", d.Status.NodeName)
		if w.problem(d) != problemCordoned {
			msg = fmt.Sprintf("node %s of another claim of its set is cordoned; waiting for room for the whole set", cordonedNode)
		}
		if w.setWaiting(d, ReasonCordoned, msg) {
			errs = append(errs, r.updateStatus(ctx, w, d))
		}
	}
	return errors.Join(errs...)
}

// relocateAttached handles an attached claim whose node cannot keep it.
// An unscheduled VMI whose pod is already pinned is deleted so the claim
// can move. One whose pod is gated, or not created yet, keeps it: gates
// gathers its set before the pod leaves the gate. A running VMI keeps it,
// except on a node that is gone or NotReady past grace, where the claim
// goes to Lost until the VMI ends.
func (r *Reconciler) relocateAttached(ctx context.Context, w *world, d *gpuv1alpha1.GPUClaim, set []*gpuv1alpha1.GPUClaim, excess bool) error {
	v := w.attachedVMI(d)
	if v == nil || v.final() {
		return nil
	}
	p := w.problem(d)
	loss := w.lost[d.Status.NodeName]
	hard := p == problemGone || p == problemNotServed || (p == problemLostCapacity && (loss.notReady || excess))
	podPinned := v.unscheduled() && w.pinned[key(v.Namespace, v.Name)]
	switch {
	case hard && podPinned:
		return r.deleteVMI(ctx, v, fmt.Sprintf("node %s of GPUClaim %s %s", d.Status.NodeName, d.Name, p))
	case p == problemGone || (p == problemLostCapacity && loss.notReady):
		if d.Status.Phase != gpuv1alpha1.GPUClaimAttached {
			return nil
		}
		d.Status.Phase = gpuv1alpha1.GPUClaimLost
		return r.updateStatus(ctx, w, d)
	case p == problemCordoned && podPinned && w.onCordon(d.Spec.ClassName) == gpuv1alpha1.OnCordonMove:
		owner := w.vms[key(v.Namespace, v.VM)]
		if owner == nil || !owner.restartsDeletedVMI() {
			// Deleting it would leave a Manual or RerunOnFailure VM stopped.
			return nil
		}
		var mine []*gpuv1alpha1.GPUClaim
		for _, m := range set {
			if m.Status.AttachedTo != nil && m.Status.AttachedTo.VMI == v.Name {
				mine = append(mine, m)
			}
		}
		// A Stay claim among them pinned the pod here.
		if _, pinned := w.stayPin(mine); pinned {
			return nil
		}
		// Only a node the next VMI can be released onto is worth the delete.
		c, err := r.constraints(ctx, w, v, unpinned(w.launchers[key(v.Namespace, v.Name)][0]), mine)
		if err != nil {
			return err
		}
		if w.pickWhere(mine, c.allows) == "" {
			return nil
		}
		return r.deleteVMI(ctx, v, fmt.Sprintf("node %s of GPUClaim %s is cordoned", d.Status.NodeName, d.Name))
	}
	return nil
}

// rebindLost places Lost claims that no VMI uses, before new orders, a
// VM's set onto one node. A set that does not fit does not hold up the
// sets after it. A rebind skips budgets: a Lost claim already counts.
func (r *Reconciler) rebindLost(ctx context.Context, w *world) []error {
	var errs []error
	for _, set := range w.vmSets(unattachedSetMember) {
		if w.aborted {
			break
		}
		if !allLost(set) {
			continue
		}
		if target := w.pick(set); target != "" {
			w.placeOn(set, target)
			if err := r.writeSet(ctx, w, set, func(*gpuv1alpha1.GPUClaim) bool { return true }); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		var changed []*gpuv1alpha1.GPUClaim
		for _, d := range set {
			// Keep the condition that says why the claim left its node.
			if waitingCondition(d) == nil && w.setWaiting(d, ReasonCapacity, "no Ready node has room for the claim's set") {
				changed = append(changed, d)
			}
		}
		if err := r.writeSet(ctx, w, changed, func(*gpuv1alpha1.GPUClaim) bool { return true }); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// writeSet writes the claims of a set that changed and stops at the first
// failure, so the rest of the pass places nothing on a view the cluster
// does not share.
func (r *Reconciler) writeSet(ctx context.Context, w *world, set []*gpuv1alpha1.GPUClaim, changed func(*gpuv1alpha1.GPUClaim) bool) error {
	for _, d := range set {
		if !changed(d) {
			continue
		}
		if err := r.updateStatus(ctx, w, d); err != nil {
			return err
		}
	}
	return nil
}

// bindOrders binds new orders, oldest first, each onto the fullest node
// with room. An order that does not fit waits with a condition and does
// not hold up the orders after it.
func (r *Reconciler) bindOrders(ctx context.Context, w *world) []error {
	var errs []error
	for _, d := range w.claims {
		if w.aborted {
			break
		}
		if holds(d) || d.Status.Phase == gpuv1alpha1.GPUClaimLost || isTemplate(d) ||
			d.Status.AttachedTo != nil || !d.DeletionTimestamp.IsZero() {
			continue
		}
		changed := false
		if d.Status.Phase == "" {
			d.Status.Phase = gpuv1alpha1.GPUClaimPending
			changed = true
		}
		reason, message := r.order(w, d)
		if reason == "" {
			changed = true
		} else if w.setWaiting(d, reason, message) {
			changed = true
		}
		if changed {
			errs = append(errs, r.updateStatus(ctx, w, d))
		}
	}
	return errs
}

// order binds one pending claim, or returns the reason it waits.
func (r *Reconciler) order(w *world, d *gpuv1alpha1.GPUClaim) (string, string) {
	class := w.classes[d.Spec.ClassName]
	switch {
	case class == nil:
		return string(model.ReasonClassNotFound), fmt.Sprintf("GPUClass %s does not exist", d.Spec.ClassName)
	case !class.DeletionTimestamp.IsZero():
		return ReasonClassDeleted, fmt.Sprintf("GPUClass %s is being deleted and takes no new orders", d.Spec.ClassName)
	}
	if why := w.budgetsUnreadable(d.Namespace); why != "" {
		return ReasonBudgetsRead, why
	}
	if err := w.snap.CheckOrder(d.Namespace, d.Spec.ClassName, int64(d.Spec.Count), w.entitled[d.Namespace]); err != nil {
		var refusal *model.Refusal
		if errors.As(err, &refusal) {
			return string(refusal.Reason), refusal.Error()
		}
		return string(model.ReasonBudget), err.Error()
	}
	set := []*gpuv1alpha1.GPUClaim{d}
	if target := w.pick(set); target != "" {
		w.placeOn(set, target)
		return "", ""
	}
	return ReasonCapacity, fmt.Sprintf("no Ready node has %d free units of GPUClass %s", d.Spec.Count, d.Spec.ClassName)
}
