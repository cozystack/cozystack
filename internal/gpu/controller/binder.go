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
	"slices"
	"sort"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu/model"
)

// ConditionWaiting is set on a device the binder cannot place yet; its
// reason names the shortage and it is removed once the device is placed.
const ConditionWaiting = "Waiting"

const (
	ReasonCapacity     = "Capacity"
	ReasonClassDeleted = "ClassDeleted"
	ReasonCordoned     = "Cordoned"
	ReasonBudgetsRead  = "BudgetsUnavailable"
)

// setWaiting records why a device waits and reports whether it changed.
func (w *world) setWaiting(d *gpuv1alpha1.GPUDevice, reason, message string) bool {
	return meta.SetStatusCondition(&d.Status.Conditions, metav1.Condition{
		Type: ConditionWaiting, Status: metav1.ConditionTrue, Reason: reason, Message: message,
		ObservedGeneration: d.Generation, LastTransitionTime: metav1.NewTime(w.now),
	})
}

func waitingCondition(d *gpuv1alpha1.GPUDevice) *metav1.Condition {
	return meta.FindStatusCondition(d.Status.Conditions, ConditionWaiting)
}

type bindRequest struct {
	namespace string
	req       *gpuv1alpha1.BindRequest
	devices   []*gpuv1alpha1.GPUDevice
}

// pendingRequests returns the complete, unanswered bind requests, oldest
// first. A request is complete when every device it names carries its ID.
func (w *world) pendingRequests() []bindRequest {
	byID := map[string]*bindRequest{}
	var order []*bindRequest
	for _, d := range w.devices {
		req := d.Status.BindRequest
		if req == nil || req.Result != "" {
			continue
		}
		k := d.Namespace + "/" + req.ID
		if byID[k] == nil {
			byID[k] = &bindRequest{namespace: d.Namespace, req: req}
			order = append(order, byID[k])
		}
	}
	var out []bindRequest
	for _, br := range order {
		complete := true
		for _, name := range br.req.Devices {
			d := w.device(br.namespace, name)
			if d == nil || d.Status.BindRequest == nil || d.Status.BindRequest.ID != br.req.ID {
				complete = false
				break
			}
			br.devices = append(br.devices, d)
		}
		if complete {
			out = append(out, *br)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].req.RequestedAt.Before(&out[j].req.RequestedAt)
	})
	return out
}

// bindRequests answers the webhook's requests: each set lands on one node
// with room for all of it, or nothing moves and the request is refused.
func (r *Reconciler) bindRequests(ctx context.Context, w *world) []error {
	var errs []error
	for _, br := range w.pendingRequests() {
		if w.aborted {
			break
		}
		result, message := r.decide(w, br)
		for _, d := range br.devices {
			d.Status.BindRequest.Result = result
			d.Status.BindRequest.Message = message
			if err := r.updateStatus(ctx, w, d); err != nil {
				errs = append(errs, err)
				break
			}
		}
	}
	return errs
}

// decide places one request's set and returns the answer. On refusal no
// placement is changed.
func (r *Reconciler) decide(w *world, br bindRequest) (gpuv1alpha1.BindResult, string) {
	refuse := func(format string, args ...any) (gpuv1alpha1.BindResult, string) {
		return gpuv1alpha1.BindRefused, fmt.Sprintf(format, args...)
	}
	var pending, held []*gpuv1alpha1.GPUDevice
	for _, d := range br.devices {
		class := w.classes[d.Spec.ClassName]
		switch {
		case d.Status.AttachedTo == nil || d.Status.AttachedTo.VMI != br.req.VMI:
			return refuse("GPUDevice %s is not attached to VMI %s", d.Name, br.req.VMI)
		case !d.DeletionTimestamp.IsZero():
			return refuse("GPUDevice %s is being deleted", d.Name)
		case holds(d):
			held = append(held, d)
		case d.Status.Phase == gpuv1alpha1.GPUDeviceLost || !isTemplate(d):
			return refuse("GPUDevice %s is %s", d.Name, phaseOrPending(d))
		case class == nil:
			return refuse("GPUClass %s of GPUDevice %s does not exist", d.Spec.ClassName, d.Name)
		case !class.DeletionTimestamp.IsZero():
			return refuse("GPUClass %s of GPUDevice %s is being deleted", d.Spec.ClassName, d.Name)
		default:
			pending = append(pending, d)
		}
	}

	if len(pending) > 0 {
		if w.quotaErr != nil {
			return refuse("tenant GPU budgets cannot be read: %v", w.quotaErr)
		}
		saved := slices.Clone(w.snap.Devices)
		for _, d := range pending {
			if err := w.snap.CheckOrder(d.Namespace, d.Spec.ClassName, int64(d.Spec.Count), w.entitled[d.Namespace]); err != nil {
				w.snap.Devices = saved
				return refuse("GPUDevice %s: %v", d.Name, err)
			}
			// Later template devices of the set count this one.
			probe := d.DeepCopy()
			probe.Status.Phase = gpuv1alpha1.GPUDeviceBound
			w.setDevice(probe)
		}
		w.snap.Devices = saved
	}

	if node, stay := w.stayPin(held); stay {
		if node == "" || len(pending) > 0 || !onOneNode(held, node) {
			return refuse("%s: a device of a class with onCordon Stay is on a cordoned node, so the set cannot be gathered", describe(br.devices))
		}
		return gpuv1alpha1.BindSucceeded, ""
	}

	cordonOnly := false
	if len(pending) == 0 && len(held) > 0 && onOneNode(held, held[0].Status.NodeName) {
		worst := problemNone
		for _, d := range held {
			worst = max(worst, w.problem(d))
		}
		if worst == problemNone {
			return gpuv1alpha1.BindSucceeded, ""
		}
		cordonOnly = worst == problemCordoned
	}

	target := w.pick(br.devices)
	switch {
	case target != "":
		w.placeOn(br.devices, target)
		return gpuv1alpha1.BindSucceeded, ""
	case cordonOnly:
		// With no room elsewhere a cordon alone does not move a set: the
		// VMI is pinned to the cordoned node and waits.
		return gpuv1alpha1.BindSucceeded, ""
	}
	return refuse("%s: no Ready node has room for all of them", describe(br.devices))
}

func phaseOrPending(d *gpuv1alpha1.GPUDevice) gpuv1alpha1.GPUDevicePhase {
	if d.Status.Phase == "" {
		return gpuv1alpha1.GPUDevicePending
	}
	return d.Status.Phase
}

func onOneNode(set []*gpuv1alpha1.GPUDevice, node string) bool {
	for _, d := range set {
		if d.Status.NodeName != node {
			return false
		}
	}
	return true
}

// stayPin returns the cordoned node a device of a Stay class pins the set
// to. The pin holds through a NotReady spell or a taint, and breaks when a
// member held on that node has lost capacity there, or the node is gone or
// no longer serves the member's class. It returns "" and true when two such
// devices sit on different nodes, and false when nothing pins the set.
// decide, relocateSet and relocateAttached all read the pin here, so the
// binder never admits a VMI that the full pass would then delete.
func (w *world) stayPin(held []*gpuv1alpha1.GPUDevice) (string, bool) {
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

// relocate moves held devices off nodes that cannot keep them, as sets,
// and deletes unscheduled VMIs that would otherwise wait on such a node.
func (r *Reconciler) relocate(ctx context.Context, w *world) []error {
	var errs []error
	leaving := w.excessOnLostNodes()
	for _, set := range w.vmSets(func(d *gpuv1alpha1.GPUDevice) bool {
		return holds(d) && d.DeletionTimestamp.IsZero()
	}) {
		if w.aborted {
			break
		}
		var unattached []*gpuv1alpha1.GPUDevice
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
// but is still Ready, the devices that no longer fit there: unattached ones
// first, then those whose VMI has not been scheduled. A device whose VMI
// runs keeps it; it is refused at the VMI's next start.
func (w *world) excessOnLostNodes() map[*gpuv1alpha1.GPUDevice]bool {
	out := map[*gpuv1alpha1.GPUDevice]bool{}
	for node, loss := range w.lost {
		if loss.notReady {
			continue
		}
		for rn, healthy := range loss.healthy {
			var held int64
			var candidates []*gpuv1alpha1.GPUDevice
			for _, d := range w.devices {
				if d.Status.NodeName != node || !holds(d) || w.resourceName(d.Spec.ClassName) != rn {
					continue
				}
				held += int64(d.Spec.Count)
				candidates = append(candidates, d)
			}
			rank := func(d *gpuv1alpha1.GPUDevice) int {
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

// mustLeave reports whether an unattached device has to leave its node,
// and whether the reason is a cordon alone.
func (w *world) mustLeave(d *gpuv1alpha1.GPUDevice, excess bool) (leave, cordonOnly bool) {
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

func (r *Reconciler) relocateSet(ctx context.Context, w *world, set []*gpuv1alpha1.GPUDevice, excess map[*gpuv1alpha1.GPUDevice]bool) error {
	hard, soft := false, false
	mustGo := map[*gpuv1alpha1.GPUDevice]bool{}
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
		// Nothing to move, or a Stay device on a cordoned node keeps the set.
		for _, d := range set {
			if c := meta.FindStatusCondition(d.Status.Conditions, ConditionWaiting); c != nil && c.Reason == ReasonCordoned {
				meta.RemoveStatusCondition(&d.Status.Conditions, ConditionWaiting)
				errs = append(errs, r.updateStatus(ctx, w, d))
			}
		}
		return errors.Join(errs...)
	}
	if target := w.pick(set); target != "" {
		before := map[*gpuv1alpha1.GPUDevice]string{}
		for _, d := range set {
			before[d] = d.Status.NodeName
		}
		w.placeOn(set, target)
		return r.writeSet(ctx, w, set, func(d *gpuv1alpha1.GPUDevice) bool { return before[d] != target })
	}
	if hard {
		// The set goes to Lost together, so it is rebound together.
		for _, d := range set {
			reason := fmt.Sprintf("node %s %s and no node has room for the device's set", d.Status.NodeName, w.problem(d))
			if !mustGo[d] {
				reason = "another device of its set must leave its node and no node has room for the whole set"
			}
			d.Status.Phase = gpuv1alpha1.GPUDeviceLost
			w.setWaiting(d, ReasonCapacity, reason)
			w.setDevice(d)
		}
		return r.writeSet(ctx, w, set, func(*gpuv1alpha1.GPUDevice) bool { return true })
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
			msg = fmt.Sprintf("node %s of another device of its set is cordoned; waiting for room for the whole set", cordonedNode)
		}
		if w.setWaiting(d, ReasonCordoned, msg) {
			errs = append(errs, r.updateStatus(ctx, w, d))
		}
	}
	return errors.Join(errs...)
}

// relocateAttached handles an attached device whose node cannot keep it.
// A VMI that has not been scheduled is deleted so the device can move; a
// running one keeps it, except on a node that is gone or NotReady past
// grace, where the device goes to Lost until the VMI ends.
func (r *Reconciler) relocateAttached(ctx context.Context, w *world, d *gpuv1alpha1.GPUDevice, set []*gpuv1alpha1.GPUDevice, excess bool) error {
	v := w.attachedVMI(d)
	if v == nil || v.final() {
		return nil
	}
	p := w.problem(d)
	loss := w.lost[d.Status.NodeName]
	hard := p == problemGone || p == problemNotServed || (p == problemLostCapacity && (loss.notReady || excess))
	switch {
	case hard && v.unscheduled():
		return r.deleteVMI(ctx, v, fmt.Sprintf("node %s of GPUDevice %s %s", d.Status.NodeName, d.Name, p))
	case p == problemGone || (p == problemLostCapacity && loss.notReady):
		if d.Status.Phase != gpuv1alpha1.GPUDeviceAttached {
			return nil
		}
		d.Status.Phase = gpuv1alpha1.GPUDeviceLost
		return r.updateStatus(ctx, w, d)
	case p == problemCordoned && v.unscheduled() && w.onCordon(d.Spec.ClassName) == gpuv1alpha1.OnCordonMove:
		owner := w.vms[key(v.Namespace, v.VM)]
		if owner == nil || !owner.restartsDeletedVMI() {
			// Deleting it would leave a Manual or RerunOnFailure VM stopped.
			return nil
		}
		var mine []*gpuv1alpha1.GPUDevice
		for _, m := range set {
			if m.Status.AttachedTo != nil && m.Status.AttachedTo.VMI == v.Name {
				mine = append(mine, m)
			}
		}
		// A Stay device among them pins the VMI here, as the binder answered.
		if _, pinned := w.stayPin(mine); pinned || w.pick(mine) == "" {
			return nil
		}
		return r.deleteVMI(ctx, v, fmt.Sprintf("node %s of GPUDevice %s is cordoned", d.Status.NodeName, d.Name))
	}
	return nil
}

// rebindLost places Lost devices that no VMI uses, before new orders, a
// VM's set onto one node. A set that does not fit does not hold up the
// sets after it. A rebind skips budgets: a Lost device already counts.
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
			if err := r.writeSet(ctx, w, set, func(*gpuv1alpha1.GPUDevice) bool { return true }); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		var changed []*gpuv1alpha1.GPUDevice
		for _, d := range set {
			// Keep the condition that says why the device left its node.
			if waitingCondition(d) == nil && w.setWaiting(d, ReasonCapacity, "no Ready node has room for the device's set") {
				changed = append(changed, d)
			}
		}
		if err := r.writeSet(ctx, w, changed, func(*gpuv1alpha1.GPUDevice) bool { return true }); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// writeSet writes the devices of a set that changed and stops at the first
// failure, so the rest of the pass places nothing on a view the cluster
// does not share.
func (r *Reconciler) writeSet(ctx context.Context, w *world, set []*gpuv1alpha1.GPUDevice, changed func(*gpuv1alpha1.GPUDevice) bool) error {
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
	for _, d := range w.devices {
		if w.aborted {
			break
		}
		if holds(d) || d.Status.Phase == gpuv1alpha1.GPUDeviceLost || isTemplate(d) ||
			d.Status.AttachedTo != nil || !d.DeletionTimestamp.IsZero() {
			continue
		}
		changed := false
		if d.Status.Phase == "" {
			d.Status.Phase = gpuv1alpha1.GPUDevicePending
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

// order binds one pending device, or returns the reason it waits.
func (r *Reconciler) order(w *world, d *gpuv1alpha1.GPUDevice) (string, string) {
	class := w.classes[d.Spec.ClassName]
	switch {
	case class == nil:
		return string(model.ReasonClassNotFound), fmt.Sprintf("GPUClass %s does not exist", d.Spec.ClassName)
	case !class.DeletionTimestamp.IsZero():
		return ReasonClassDeleted, fmt.Sprintf("GPUClass %s is being deleted and takes no new orders", d.Spec.ClassName)
	case w.quotaErr != nil:
		return ReasonBudgetsRead, "tenant GPU budgets cannot be read"
	}
	if err := w.snap.CheckOrder(d.Namespace, d.Spec.ClassName, int64(d.Spec.Count), w.entitled[d.Namespace]); err != nil {
		var refusal *model.Refusal
		if errors.As(err, &refusal) {
			return string(refusal.Reason), refusal.Error()
		}
		return string(model.ReasonBudget), err.Error()
	}
	set := []*gpuv1alpha1.GPUDevice{d}
	if target := w.pick(set); target != "" {
		w.placeOn(set, target)
		return "", ""
	}
	return ReasonCapacity, fmt.Sprintf("no Ready node has %d free units of GPUClass %s", d.Spec.Count, d.Spec.ClassName)
}
