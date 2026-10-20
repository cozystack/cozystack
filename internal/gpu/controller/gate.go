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
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu"
	"github.com/cozystack/cozystack/internal/gpu/model"
)

// rejectFor is how long a node that refused a pinned pod is skipped for
// that pod's set unless the node changes first. It outlasts a kubelet
// restart and the device plugin registering again.
const rejectFor = 10 * time.Minute

const (
	ReasonBudget       = "Budget"
	ReasonClaimMissing = "ClaimMissing"
	ReasonNameTaken    = "NameTaken"
	ReasonNodeRejected = "NodeRejected"
	ReasonNodeAffinity = "NodeAffinity"
)

// nodeNameField is the only field a node selector requirement can match.
const nodeNameField = "metadata.name"

func gated(p *corev1.Pod) bool {
	for _, g := range p.Spec.SchedulingGates {
		if g.Name == gpu.SchedulingGateClaims {
			return true
		}
	}
	return false
}

// pinnedNode returns the node the controller pinned p to, or "". release
// appends the pin last to every non-empty required term, so it is the last
// matchFields entry of the first such term; a VM's own metadata.name
// requirements come before it.
func pinnedNode(p *corev1.Pod) string {
	a := p.Spec.Affinity
	if a == nil || a.NodeAffinity == nil || a.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		return ""
	}
	for _, t := range a.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
		if len(t.MatchExpressions) == 0 && len(t.MatchFields) == 0 {
			continue
		}
		if n := len(t.MatchFields); n > 0 {
			f := t.MatchFields[n-1]
			if f.Key == nodeNameField && f.Operator == corev1.NodeSelectorOpIn && len(f.Values) == 1 {
				return f.Values[0]
			}
		}
		return ""
	}
	return ""
}

// unpinned returns a copy of p as it was before release pinned it.
func unpinned(p *corev1.Pod) *corev1.Pod {
	out := p.DeepCopy()
	node := pinnedNode(p)
	if node == "" {
		return out
	}
	req := out.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	var terms []corev1.NodeSelectorTerm
	for _, t := range req.NodeSelectorTerms {
		if n := len(t.MatchFields); n > 0 && t.MatchFields[n-1].Key == nodeNameField &&
			t.MatchFields[n-1].Operator == corev1.NodeSelectorOpIn && slices.Equal(t.MatchFields[n-1].Values, []string{node}) {
			t.MatchFields = t.MatchFields[:n-1]
			if len(t.MatchExpressions) == 0 && len(t.MatchFields) == 0 {
				// The term release created for a pod without one.
				continue
			}
		}
		terms = append(terms, t)
	}
	if len(terms) == 0 {
		out.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution = nil
	} else {
		req.NodeSelectorTerms = terms
	}
	return out
}

// gates places the set of each VMI whose launcher pod waits at
// gpu.SchedulingGateClaims, oldest pod first, and releases the pod onto
// the set's node. A set that cannot be placed keeps its pod gated, with
// ConditionWaiting on its claims and a Warning event on the VMI.
func (r *Reconciler) gates(ctx context.Context, w *world) []error {
	var pods []*corev1.Pod
	for _, ps := range w.launchers {
		for _, p := range ps {
			if gated(p) && p.DeletionTimestamp.IsZero() {
				pods = append(pods, p)
			}
		}
	}
	sort.Slice(pods, func(i, j int) bool {
		a, b := pods[i], pods[j]
		if !a.CreationTimestamp.Equal(&b.CreationTimestamp) {
			return a.CreationTimestamp.Before(&b.CreationTimestamp)
		}
		return a.Namespace+"/"+a.Name < b.Namespace+"/"+b.Name
	})
	var errs []error
	for _, p := range pods {
		if w.aborted {
			break
		}
		k, _ := w.launcherVMI(p)
		v := w.vmis[k]
		if v == nil || v.final() || v.Deleting {
			continue
		}
		if v.RefsErr != nil || len(v.Refs) == 0 {
			// The VMI webhook refuses such a VMI; a pod that still got
			// here waits for its VMI to be fixed or deleted.
			log.FromContext(ctx).V(1).Info("gated launcher pod without valid claim references",
				"namespace", p.Namespace, "pod", p.Name, "vmi", v.Name, "error", v.RefsErr)
			continue
		}
		set, reason, message, err := r.claimSet(ctx, w, v)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if set == nil && reason == "" {
			continue
		}
		node := ""
		before := map[*gpuv1alpha1.GPUClaim]string{}
		for _, d := range set {
			before[d] = string(d.Status.Phase) + "@" + d.Status.NodeName
		}
		if reason == "" {
			c, err := r.constraints(ctx, w, v, p, set)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			node, reason, message = w.placeSet(set, c)
		}
		if node == "" {
			var changed []*gpuv1alpha1.GPUClaim
			for _, d := range set {
				if w.setWaiting(d, reason, message) {
					changed = append(changed, d)
				}
			}
			r.warn(v, reason, message)
			errs = append(errs, r.writeSet(ctx, w, changed, func(*gpuv1alpha1.GPUClaim) bool { return true }))
			continue
		}
		cleared := map[*gpuv1alpha1.GPUClaim]bool{}
		for _, d := range set {
			cleared[d] = meta.RemoveStatusCondition(&d.Status.Conditions, ConditionWaiting)
		}
		if err := r.writeSet(ctx, w, set, func(d *gpuv1alpha1.GPUClaim) bool {
			return cleared[d] || before[d] != string(d.Status.Phase)+"@"+d.Status.NodeName
		}); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := r.release(ctx, p, node); err != nil {
			errs = append(errs, err)
			continue
		}
		// The launcher follows a released WaitForFirstConsumer pod, and a
		// wait of its own is news again.
		r.mu.Lock()
		delete(r.waitReason, v.UID)
		r.mu.Unlock()
	}
	live := sets.New[types.UID]()
	for _, v := range w.vmis {
		live.Insert(v.UID)
	}
	r.mu.Lock()
	for uid := range r.waitReason {
		if !live.Has(uid) {
			delete(r.waitReason, uid)
		}
	}
	r.mu.Unlock()
	return errs
}

// claimSet returns the claims of v's references, the template claims
// created when missing, or nil while the set is not complete yet. A
// referenced claim that is gone or being deleted, and a template name a
// tenant's claim already has, are returned as the reason the set waits,
// with the claims the set does have.
func (r *Reconciler) claimSet(ctx context.Context, w *world, v *vmi) ([]*gpuv1alpha1.GPUClaim, string, string, error) {
	var set []*gpuv1alpha1.GPUClaim
	var taken, missing []string
	complete := true
	for i, ref := range v.Refs {
		if ref.Claim != "" {
			d := w.claim(v.Namespace, ref.Claim)
			if d == nil || !d.DeletionTimestamp.IsZero() {
				missing = append(missing, ref.Claim)
				complete = false
				continue
			}
			if d.Status.AttachedTo == nil || d.Status.AttachedTo.VMI != v.Name || isTemplate(d) {
				// The webhook attached it before the VMI was created; a
				// claim not attached to it here is not this VMI's, and a
				// template claim is only its owner's.
				complete = false
				continue
			}
			set = append(set, d)
			continue
		}
		name := model.TemplateClaimName(v.Name, i)
		d := w.claim(v.Namespace, name)
		switch {
		case d == nil:
			created, err := r.createTemplate(ctx, w, v, name, ref.ClaimTemplate)
			if err != nil {
				return nil, "", "", err
			}
			d = created
		case !isTemplate(d):
			taken = append(taken, name)
			complete = false
			continue
		case vmiOwner(d) != v.UID || !d.DeletionTimestamp.IsZero():
			// The previous VMI's, deleted once that VMI is final.
			complete = false
			continue
		case d.Status.AttachedTo != nil && d.Status.AttachedTo.VMI != v.Name:
			// Attached elsewhere; attachments deletes it.
			complete = false
			continue
		}
		if d.Status.AttachedTo == nil {
			d.Status.AttachedTo = &gpuv1alpha1.GPUClaimAttachment{VMI: v.Name}
			if d.Status.Phase == "" {
				d.Status.Phase = gpuv1alpha1.GPUClaimPending
			}
			if err := r.updateStatus(ctx, w, d); err != nil {
				return nil, "", "", err
			}
		}
		set = append(set, d)
	}
	if len(missing) > 0 {
		return set, ReasonClaimMissing, fmt.Sprintf("VMI %s cannot start: GPUClaim %s does not exist or is being deleted", v.Name, strings.Join(missing, ", ")), nil
	}
	if len(taken) > 0 {
		return set, ReasonNameTaken, fmt.Sprintf("VMI %s cannot start: GPUClaim %s exists and is not its template claim", v.Name, strings.Join(taken, ", ")), nil
	}
	if !complete {
		return nil, "", "", nil
	}
	return set, "", "", nil
}

// createTemplate creates the claim of a claimTemplate entry, owned by its
// VMI. Not a controller reference and no blockOwnerDeletion: the claim's
// own finalizer, not the garbage collector, decides when it goes.
func (r *Reconciler) createTemplate(ctx context.Context, w *world, v *vmi, name string, t *model.ClaimTemplate) (*gpuv1alpha1.GPUClaim, error) {
	d := &gpuv1alpha1.GPUClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  v.Namespace,
			Labels:     map[string]string{gpu.LabelTemplateClaim: gpu.LabelValueTrue},
			Finalizers: []string{gpu.FinalizerClaim},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: vmiGVK.GroupVersion().String(),
				Kind:       vmiGVK.Kind,
				Name:       v.Name,
				UID:        v.UID,
			}},
		},
		Spec: gpuv1alpha1.GPUClaimSpec{ClassName: t.ClassName, Count: t.Count},
	}
	log.FromContext(ctx).Info("creating template GPUClaim", "namespace", d.Namespace, "name", d.Name, "vmi", v.Name)
	if err := r.Client.Create(ctx, d); err != nil {
		w.aborted = true
		return nil, fmt.Errorf("create template GPUClaim %s/%s: %w", d.Namespace, d.Name, err)
	}
	w.claims = append(w.claims, d)
	w.setClaim(d)
	return d, nil
}

// placement is what restricts a VMI's set beyond capacity.
type placement struct {
	// rejected are the nodes that refused a pinned pod of the set, as the
	// claims' status and the VM's earlier VMIs record them.
	rejected []string
	// volumes accepts the nodes the PVs bound to the VMI's PVCs allow; nil
	// accepts every node.
	volumes func(string) bool
	// pod accepts the nodes the launcher pod's own node selector and
	// required node affinity allow, such as KubeVirt's schedulable label
	// and the VM's own placement.
	pod func(string) bool
}

func (c placement) allows(node string) bool {
	return !slices.Contains(c.rejected, node) && c.reaches(node)
}

// reaches reports whether the pod can run on node at all, whatever its
// capacity.
func (c placement) reaches(node string) bool {
	return (c.volumes == nil || c.volumes(node)) && (c.pod == nil || c.pod(node))
}

func (r *Reconciler) constraints(ctx context.Context, w *world, v *vmi, p *corev1.Pod, set []*gpuv1alpha1.GPUClaim) (placement, error) {
	volumes, err := r.volumeNodes(ctx, w, v)
	if err != nil {
		return placement{}, err
	}
	rejected := rejectedNodes(set)
	for _, rn := range r.vmRejections(v) {
		if !slices.Contains(rejected, rn.NodeName) {
			rejected = append(rejected, rn.NodeName)
		}
	}
	sort.Strings(rejected)
	return placement{rejected: rejected, volumes: volumes, pod: func(node string) bool { return podAllows(p, w.nodes[node]) }}, nil
}

// placeSet binds or moves a VMI's set onto one node and returns it, or
// returns the reason the set waits. On a wait no placement is changed.
func (w *world) placeSet(set []*gpuv1alpha1.GPUClaim, c placement) (string, string, string) {
	var pending, lost, held []*gpuv1alpha1.GPUClaim
	for _, d := range set {
		class := w.classes[d.Spec.ClassName]
		switch {
		case holds(d):
			held = append(held, d)
			continue
		case class == nil:
			return "", string(model.ReasonClassNotFound), fmt.Sprintf("GPUClass %s of GPUClaim %s does not exist", d.Spec.ClassName, d.Name)
		case d.Status.Phase == gpuv1alpha1.GPUClaimLost:
			lost = append(lost, d)
		case !class.DeletionTimestamp.IsZero():
			return "", ReasonClassDeleted, fmt.Sprintf("GPUClass %s of GPUClaim %s is being deleted and takes no new orders", d.Spec.ClassName, d.Name)
		default:
			pending = append(pending, d)
		}
	}

	if len(pending) > 0 {
		if why := w.budgetsUnreadable(pending[0].Namespace); why != "" {
			return "", ReasonBudgetsRead, why
		}
		saved := slices.Clone(w.snap.Claims)
		for _, d := range pending {
			if err := w.snap.CheckOrder(d.Namespace, d.Spec.ClassName, int64(d.Spec.Count), w.entitled[d.Namespace]); err != nil {
				w.snap.Claims = saved
				reason := ReasonBudget
				var refusal *model.Refusal
				if errors.As(err, &refusal) {
					reason = string(refusal.Reason)
				}
				return "", reason, fmt.Sprintf("GPUClaim %s: %v", d.Name, err)
			}
			// Later pending claims of the set count this one.
			probe := d.DeepCopy()
			probe.Status.Phase = gpuv1alpha1.GPUClaimBound
			w.setClaim(probe)
		}
		w.snap.Claims = saved
	}

	if node, stay := w.stayPin(held); stay {
		if node == "" || len(pending)+len(lost) > 0 || !onOneNode(held, node) {
			return "", ReasonCordoned, fmt.Sprintf("%s: a claim of a class with onCordon Stay is on a cordoned node, so the set cannot be gathered", describe(set))
		}
		if !c.reaches(node) {
			return "", ReasonNodeAffinity, fmt.Sprintf("%s: a claim of a class with onCordon Stay keeps the set on node %s, which the VMI's pod or volumes cannot use", describe(set), node)
		}
		return node, "", ""
	}

	cordonOnly := false
	if len(pending)+len(lost) == 0 && len(held) > 0 && onOneNode(held, held[0].Status.NodeName) {
		worst := problemNone
		for _, d := range held {
			worst = max(worst, w.problem(d))
		}
		if worst == problemNone && c.allows(held[0].Status.NodeName) {
			return held[0].Status.NodeName, "", ""
		}
		cordonOnly = worst == problemCordoned && c.reaches(held[0].Status.NodeName)
	}

	if target := w.pickWhere(set, c.allows); target != "" {
		w.placeOn(set, target)
		return target, "", ""
	}
	if cordonOnly {
		// With no room elsewhere a cordon alone does not move a set: the pod
		// is pinned to the cordoned node and waits.
		return held[0].Status.NodeName, "", ""
	}
	if w.pickWhere(set, func(node string) bool { return !slices.Contains(c.rejected, node) }) != "" {
		return "", ReasonNodeAffinity, fmt.Sprintf("%s: the nodes with room for all of them are ones the VMI's pod or its volumes cannot use", describe(set))
	}
	if c.volumes != nil {
		return "", ReasonNodeAffinity, fmt.Sprintf("%s: no node the VMI's volumes reach has room for all of them", describe(set))
	}
	if len(c.rejected) > 0 {
		return "", ReasonNodeRejected, fmt.Sprintf("%s: no Ready node has room for all of them, besides nodes that refused the VMI's pod: %s",
			describe(set), strings.Join(c.rejected, ", "))
	}
	return "", ReasonCapacity, fmt.Sprintf("%s: no Ready node has room for all of them", describe(set))
}

// release pins p to node and removes the gate in one update: the API
// server lets the node affinity of a pod change only while the pod being
// updated is still gated, and a gated pod's required terms can only be
// narrowed, never added to.
func (r *Reconciler) release(ctx context.Context, p *corev1.Pod, node string) error {
	pod := p.DeepCopy()
	req := corev1.NodeSelectorRequirement{Key: nodeNameField, Operator: corev1.NodeSelectorOpIn, Values: []string{node}}
	if pod.Spec.Affinity == nil {
		pod.Spec.Affinity = &corev1.Affinity{}
	}
	if pod.Spec.Affinity.NodeAffinity == nil {
		pod.Spec.Affinity.NodeAffinity = &corev1.NodeAffinity{}
	}
	na := pod.Spec.Affinity.NodeAffinity
	if na.RequiredDuringSchedulingIgnoredDuringExecution == nil || len(na.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms) == 0 {
		na.RequiredDuringSchedulingIgnoredDuringExecution = &corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchFields: []corev1.NodeSelectorRequirement{req}}},
		}
	} else {
		terms := na.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
		for i := range terms {
			// An empty term matches no node and cannot gain requirements.
			if len(terms[i].MatchExpressions) == 0 && len(terms[i].MatchFields) == 0 {
				continue
			}
			terms[i].MatchFields = append(terms[i].MatchFields, req)
		}
	}
	pod.Spec.SchedulingGates = slices.DeleteFunc(pod.Spec.SchedulingGates, func(g corev1.PodSchedulingGate) bool {
		return g.Name == gpu.SchedulingGateClaims
	})
	log.FromContext(ctx).Info("releasing gated launcher pod", "namespace", pod.Namespace, "pod", pod.Name, "node", node)
	if err := r.Client.Update(ctx, pod); err != nil {
		return fmt.Errorf("release pod %s/%s onto node %s: %w", pod.Namespace, pod.Name, node, err)
	}
	return nil
}

// unschedulable returns the scheduler's message when it refused p.
func unschedulable(p *corev1.Pod) (string, bool) {
	if p.Spec.NodeName != "" || p.Status.Phase != corev1.PodPending {
		return "", false
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Reason == corev1.PodReasonUnschedulable {
			return c.Message, true
		}
	}
	return "", false
}

// recoverRefused deletes a pinned launcher pod that the scheduler refuses
// on a node that is Ready, not cordoned and still serves the whole set, and
// records the node as rejected on every claim of the set. A pin cannot be
// undone, so the pod has to go; the VM's runStrategy decides what follows.
// A cordon, a node that is not Ready and a Stay pin are not such a refusal,
// and lost capacity follows its own rules in relocate.
func (r *Reconciler) recoverRefused(ctx context.Context, w *world) []error {
	var errs []error
	for k, pods := range w.launchers {
		v := w.vmis[k]
		if v == nil || v.final() {
			continue
		}
		var set []*gpuv1alpha1.GPUClaim
		for _, d := range w.claims {
			if w.attachedVMI(d) == v {
				set = append(set, d)
			}
		}
		for _, p := range pods {
			if w.aborted {
				return errs
			}
			node := pinnedNode(p)
			msg, refused := unschedulable(p)
			if node == "" || !refused || gated(p) || !p.DeletionTimestamp.IsZero() || !w.servesWhole(set, node) {
				continue
			}
			log.FromContext(ctx).Info("deleting launcher pod its pinned node refuses",
				"namespace", p.Namespace, "pod", p.Name, "node", node, "reason", msg)
			// The pod is read from the cache; the version precondition keeps
			// one the scheduler has bound since then from being deleted.
			err := r.Client.Delete(ctx, p, client.Preconditions{UID: ptr.To(p.UID), ResourceVersion: ptr.To(p.ResourceVersion)})
			if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
				continue
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("delete pod %s/%s: %w", p.Namespace, p.Name, err))
				continue
			}
			// A node the pod no longer matches, after a label change, did
			// not refuse it for lack of resources: the pod goes, so the next
			// one is placed where it can run, but the node is not rejected.
			if podAllows(p, w.nodes[node]) {
				errs = append(errs, r.reject(ctx, w, v, set, node, msg))
			}
		}
	}
	return errs
}

// servesWhole reports whether node holds every claim of set with nothing
// wrong: it exists, is Ready, not cordoned, tolerated and still serves.
func (w *world) servesWhole(set []*gpuv1alpha1.GPUClaim, node string) bool {
	if len(set) == 0 {
		return false
	}
	for _, d := range set {
		if !holds(d) || d.Status.NodeName != node || w.problem(d) != problemNone {
			return false
		}
	}
	return true
}

func (r *Reconciler) reject(ctx context.Context, w *world, v *vmi, set []*gpuv1alpha1.GPUClaim, node, reason string) error {
	until := metav1.NewTime(w.now.Add(rejectFor))
	r.mu.Lock()
	r.rejectedAs[node] = stateOf(w.nodes[node])
	if v.VM != "" {
		// The template claims of the set go with this VMI, and their
		// rejections with them; the VM's next VMI reads these. They do not
		// survive a restart of the controller.
		k := key(v.Namespace, v.VM)
		if !slices.ContainsFunc(r.vmRejected[k], func(rn gpuv1alpha1.RejectedNode) bool { return rn.NodeName == node }) {
			r.vmRejected[k] = append(r.vmRejected[k], gpuv1alpha1.RejectedNode{NodeName: node, Reason: reason, Until: until})
		}
	}
	r.mu.Unlock()
	for _, d := range set {
		if slices.ContainsFunc(d.Status.RejectedNodes, func(rn gpuv1alpha1.RejectedNode) bool { return rn.NodeName == node }) {
			continue
		}
		d.Status.RejectedNodes = append(d.Status.RejectedNodes, gpuv1alpha1.RejectedNode{NodeName: node, Reason: reason, Until: until})
		w.setWaiting(d, ReasonNodeRejected, fmt.Sprintf("node %s refused the VMI's pod after it was pinned there: %s", node, reason))
		if err := r.updateStatus(ctx, w, d); err != nil {
			return err
		}
	}
	return nil
}

// nodeState is what ends a rejection when it changes: the node's
// allocatable, its taints and its readiness.
type nodeState struct {
	allocatable corev1.ResourceList
	taints      []corev1.Taint
	ready       bool
}

func stateOf(n *corev1.Node) nodeState {
	if n == nil {
		return nodeState{}
	}
	return nodeState{allocatable: n.Status.Allocatable.DeepCopy(), taints: slices.Clone(n.Spec.Taints), ready: model.Ready(n)}
}

func (s nodeState) changed(n *corev1.Node) bool {
	o := stateOf(n)
	return s.ready != o.ready || !apiequality.Semantic.DeepEqual(s.allocatable, o.allocatable) ||
		!apiequality.Semantic.DeepEqual(s.taints, o.taints)
}

// pruneRejections drops rejected nodes whose until has passed or that have
// changed since, so a node the hoster has fixed is not skipped for long.
// A Ready transition after the rejection is a change even when this
// controller restarted and forgot how the node looked.
func (r *Reconciler) pruneRejections(ctx context.Context, w *world) []error {
	r.mu.Lock()
	rejectedAs := maps.Clone(r.rejectedAs)
	r.mu.Unlock()
	ended := func(rn gpuv1alpha1.RejectedNode) bool {
		n := w.nodes[rn.NodeName]
		if !w.now.Before(rn.Until.Time) || n == nil {
			return true
		}
		if was, ok := rejectedAs[rn.NodeName]; ok && was.changed(n) {
			return true
		}
		rejectedAt := rn.Until.Add(-rejectFor)
		for _, c := range n.Status.Conditions {
			if c.Type == corev1.NodeReady && c.LastTransitionTime.After(rejectedAt) {
				return true
			}
		}
		return false
	}
	inUse := sets.New[string]()
	var errs []error
	for _, d := range w.claims {
		if len(d.Status.RejectedNodes) == 0 {
			continue
		}
		kept := slices.DeleteFunc(slices.Clone(d.Status.RejectedNodes), ended)
		for _, rn := range kept {
			inUse.Insert(rn.NodeName)
		}
		if len(kept) == len(d.Status.RejectedNodes) || w.aborted {
			continue
		}
		d.Status.RejectedNodes = kept
		if c := waitingCondition(d); len(kept) == 0 && c != nil && c.Reason == ReasonNodeRejected {
			meta.RemoveStatusCondition(&d.Status.Conditions, ConditionWaiting)
		}
		errs = append(errs, r.updateStatus(ctx, w, d))
	}
	r.mu.Lock()
	for k, rns := range r.vmRejected {
		kept := slices.DeleteFunc(rns, ended)
		if len(kept) == 0 {
			delete(r.vmRejected, k)
			continue
		}
		r.vmRejected[k] = kept
		for _, rn := range kept {
			inUse.Insert(rn.NodeName)
		}
	}
	for node := range r.rejectedAs {
		if !inUse.Has(node) {
			delete(r.rejectedAs, node)
		}
	}
	r.mu.Unlock()
	return errs
}

// vmRejections returns the nodes that refused a pinned pod of an earlier
// VMI of v's VM.
func (r *Reconciler) vmRejections(v *vmi) []gpuv1alpha1.RejectedNode {
	if v.VM == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.vmRejected[key(v.Namespace, v.VM)])
}

// warn records a Warning event on v when the reason its set waits changes.
func (r *Reconciler) warn(v *vmi, reason, message string) {
	r.mu.Lock()
	same := r.waitReason[v.UID] == reason
	r.waitReason[v.UID] = reason
	r.mu.Unlock()
	if same || r.Recorder == nil {
		return
	}
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(vmiGVK)
	obj.SetNamespace(v.Namespace)
	obj.SetName(v.Name)
	obj.SetUID(v.UID)
	r.Recorder.Eventf(obj, nil, corev1.EventTypeWarning, reason, "Start", "%s", message)
}

// rejected reports whether node refused a pinned pod of set and is still
// skipped for it.
func rejected(set []*gpuv1alpha1.GPUClaim, node string) bool {
	return slices.Contains(rejectedNodes(set), node)
}

func rejectedNodes(set []*gpuv1alpha1.GPUClaim) []string {
	var out []string
	for _, d := range set {
		for _, rn := range d.Status.RejectedNodes {
			if !slices.Contains(out, rn.NodeName) {
				out = append(out, rn.NodeName)
			}
		}
	}
	sort.Strings(out)
	return out
}
