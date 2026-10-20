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
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cozystack/cozystack/internal/gpu"
	"github.com/cozystack/cozystack/internal/gpu/model"
)

// ConditionResourceNameNotPermitted is set on a class whose resource name
// the KubeVirt permittedHostDevices table leaves out while the table is
// set: KubeVirt then never renders the virt-launcher pod and the VMI hangs
// Pending, so the VMI webhook refuses such claims up front.
const ConditionResourceNameNotPermitted = "ResourceNameNotPermitted"

func condition(t string, on bool, reason, message string, w *world, generation int64) metav1.Condition {
	c := metav1.Condition{Type: t, Status: metav1.ConditionFalse, Reason: "None", ObservedGeneration: generation,
		LastTransitionTime: metav1.NewTime(w.now)}
	if on {
		c.Status, c.Reason, c.Message = metav1.ConditionTrue, reason, message
	}
	return c
}

// classStatus publishes free capacity and the class conditions. The status
// is readable by every authenticated user, so conditions name no nodes and
// carry no counts.
func (r *Reconciler) classStatus(ctx context.Context, w *world) []error {
	var errs []error
	for name, c := range w.classes {
		before := c.Status.DeepCopy()
		free, maxFree := w.snap.ClassCapacity(name)
		c.Status.Free = ptr.To(int32(free))
		c.Status.MaxFreePerNode = ptr.To(int32(maxFree))

		rn := w.resourceName(name)
		lost := false
		conflicting := map[string]bool{}
		for _, n := range w.snap.Nodes {
			if !selects(w.snap.Classes[name], n.Labels) {
				continue
			}
			// A NotReady node covers every name, so it counts only where it
			// carries the class; a loss of served units is per name already.
			l := w.lost[n.Name]
			lost = lost || (l.covers(rn) && (!l.notReady || w.carries(n, name, rn)))
			for _, other := range w.snap.Conflicts(n.Name) {
				if w.resourceName(other) == rn {
					conflicting[other] = true
				}
			}
		}
		inConflict := conflicting[name]
		delete(conflicting, name)
		others := sortedKeys(conflicting)

		for _, cond := range []metav1.Condition{
			condition(gpu.ConditionLostCapacity, lost, "NodeLostCapacity",
				"a node serving this class has lost capacity; its claims move or wait", w, c.Generation),
			condition(gpu.ConditionResourceNameConflict, inConflict, "SharedResourceName",
				fmt.Sprintf("a node is selected by this class and by %s with the same resource name and serves none of them", strings.Join(others, ", ")), w, c.Generation),
			condition(ConditionResourceNameNotPermitted, w.permitted != nil && !w.permitted.Has(string(rn)), "NotInPermittedHostDevices",
				fmt.Sprintf("resource name %s is not in the KubeVirt permittedHostDevices table, so VMs cannot start with devices of this class", rn), w, c.Generation),
		} {
			meta.SetStatusCondition(&c.Status.Conditions, cond)
		}
		if equality.Semantic.DeepEqual(before, &c.Status) {
			continue
		}
		errs = append(errs, client.IgnoreNotFound(r.Client.Status().Update(ctx, c)))
	}
	return errs
}

// carries reports whether the node has units of rn or has a claim of class
// on it: a NotReady node without the class's GPUs has lost none of them.
func (w *world) carries(n model.Node, class string, rn corev1.ResourceName) bool {
	if n.Allocatable[rn] > 0 {
		return true
	}
	for _, d := range w.claims {
		// A claim that went Lost when the node did still names it.
		if d.Spec.ClassName == class && d.Status.NodeName == n.Name && model.CountsAgainstBudget(d.Status.Phase) {
			return true
		}
	}
	return false
}

// selects reads a class's node selector as model.Snapshot does: a key the
// selector names with an empty value must be on the node.
func selects(c model.Class, nodeLabels map[string]string) bool {
	return labels.SelectorFromSet(c.NodeSelector).Matches(labels.Set(nodeLabels))
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// nodeStatus sets the controller's conditions on GPUNode. It patches only
// conditions, so the agent's inventory in the same status is left alone.
func (r *Reconciler) nodeStatus(ctx context.Context, w *world) []error {
	var errs []error
	for name, gn := range w.gpuNodes {
		// The agent writes the same status; a conditions list replaced from
		// a stale copy would drop what it wrote in between.
		patch := client.MergeFromWithOptions(gn.DeepCopy(), client.MergeFromWithOptimisticLock{})
		before := gn.Status.DeepCopy()
		conflicts := w.snap.Conflicts(name)
		var lostNames []string
		if l := w.lost[name]; l != nil && !l.notReady {
			for rn := range l.healthy {
				lostNames = append(lostNames, string(rn))
			}
			sort.Strings(lostNames)
		}
		lostMsg := "the node has been NotReady for longer than the grace period"
		if len(lostNames) > 0 {
			lostMsg = "devices of " + strings.Join(lostNames, ", ") + " have been missing or unhealthy for longer than the grace period"
		}
		meta.SetStatusCondition(&gn.Status.Conditions, condition(gpu.ConditionLostCapacity, w.lost[name] != nil,
			"NodeLostCapacity", lostMsg, w, gn.Generation))
		meta.SetStatusCondition(&gn.Status.Conditions, condition(gpu.ConditionResourceNameConflict, len(conflicts) > 0,
			"SharedResourceName", "GPUClasses "+strings.Join(conflicts, ", ")+" share a resource name on this node, which serves none of them",
			w, gn.Generation))
		if equality.Semantic.DeepEqual(before, &gn.Status) {
			continue
		}
		errs = append(errs, client.IgnoreNotFound(r.Client.Status().Patch(ctx, gn, patch)))
	}
	return errs
}

// settings writes the effective GPU resource-name set and the raw-name
// policy for the admission policies and cozystack-api.
func (r *Reconciler) settings(ctx context.Context, w *world) error {
	data := map[string]string{
		gpu.SettingsKeyResourceNames:    model.FormatResourceNames(w.resourceNames),
		gpu.SettingsKeyRawResourceNames: string(r.rawPolicy()),
	}
	cm := &corev1.ConfigMap{}
	err := r.Client.Get(ctx, key(gpu.SettingsNamespace, gpu.SettingsConfigMap), cm)
	if apierrors.IsNotFound(err) {
		cm = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: gpu.SettingsConfigMap, Namespace: gpu.SettingsNamespace},
			Data:       data,
		}
		// A stale cache right after the first create reads NotFound.
		if err := r.Client.Create(ctx, cm); !apierrors.IsAlreadyExists(err) {
			return err
		}
		return nil
	}
	if err != nil {
		return err
	}
	if equality.Semantic.DeepEqual(cm.Data, data) {
		return nil
	}
	cm.Data = data
	return r.Client.Update(ctx, cm)
}

func (r *Reconciler) rawPolicy() gpu.RawNamesPolicy {
	if r.Config.RawResourceNames == "" {
		return gpu.RawNamesAllowed
	}
	return r.Config.RawResourceNames
}
