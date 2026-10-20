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
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu/model"
)

// nodeProblem is why a device's node cannot keep serving it, strongest
// first; problemNone means it can.
type nodeProblem int

const (
	problemNone nodeProblem = iota
	// problemCordoned alone is the problem under which a device of a Move
	// class with no room elsewhere stays.
	problemCordoned
	// problemTainted and problemNotReady keep a bound device in place; a
	// gather at VMI creation takes devices off such a node, unless a Stay pin
	// holds the set there.
	problemTainted
	problemNotReady
	// problemNotServed and every problem after it break a Stay pin.
	problemNotServed
	problemLostCapacity
	problemGone
)

func (p nodeProblem) String() string {
	return [...]string{"", "is cordoned", "has a taint VMs do not tolerate", "is not Ready",
		"no longer serves the class", "has lost capacity", "is gone"}[p]
}

// layoutPending reports a pending host-layer change on the node. Node
// layouts arrive with the host layer; until then no node has one.
func layoutPending(string) bool { return false }

// problem returns the strongest reason the node of a held device cannot
// keep serving it.
func (w *world) problem(d *gpuv1alpha1.GPUDevice) nodeProblem {
	n := w.nodes[d.Status.NodeName]
	switch {
	case n == nil:
		return problemGone
	case w.lost[n.Name].covers(w.resourceName(d.Spec.ClassName)):
		return problemLostCapacity
	case layoutPending(n.Name), !w.matches(d.Spec.ClassName, n.Name):
		return problemNotServed
	case !model.Ready(n):
		return problemNotReady
	case model.UntoleratedTaint(n, nil) != nil:
		return problemTainted
	case model.Cordoned(n):
		return problemCordoned
	}
	return problemNone
}

// matches reports whether the class selects the node and no other class
// claims its resource name there.
func (w *world) matches(class, node string) bool {
	c := w.snap.Classes[class]
	n := w.nodes[node]
	if n == nil || c.ResourceName == "" {
		return false
	}
	for _, conflict := range w.snap.Conflicts(node) {
		if conflict == class {
			return false
		}
	}
	for k, v := range c.NodeSelector {
		if n.Labels[k] != v {
			return false
		}
	}
	return true
}

func (w *world) onCordon(class string) gpuv1alpha1.OnCordonPolicy {
	if c := w.classes[class]; c != nil && c.Spec.OnCordon == gpuv1alpha1.OnCordonStay {
		return gpuv1alpha1.OnCordonStay
	}
	return gpuv1alpha1.OnCordonMove
}

func holds(d *gpuv1alpha1.GPUDevice) bool { return model.HoldsGPUs(d.Status.Phase) }

// fits reports whether every device of set can hold its units on node:
// the node serves each class, has not lost capacity for it, and has room
// for the units of devices not already held there.
func (w *world) fits(set []*gpuv1alpha1.GPUDevice, node string) bool {
	need := map[string]int64{}
	for _, d := range set {
		need[d.Spec.ClassName] += 0
		if !holds(d) || d.Status.NodeName != node {
			need[d.Spec.ClassName] += int64(d.Spec.Count)
		}
	}
	for class, units := range need {
		if !w.snap.Serves(class, node) || layoutPending(node) || w.lost[node].covers(w.resourceName(class)) {
			return false
		}
		if w.snap.FreeOnNode(class, node) < units {
			return false
		}
	}
	return true
}

// pick returns the node set fits on, preferring the node that already
// holds most of it and then the fullest one, so whole nodes stay free for
// large sets.
func (w *world) pick(set []*gpuv1alpha1.GPUDevice) string {
	type candidate struct {
		name       string
		here, free int64
	}
	var cands []candidate
	for _, n := range w.snap.Nodes {
		if !w.fits(set, n.Name) {
			continue
		}
		c := candidate{name: n.Name}
		classes := map[string]bool{}
		for _, d := range set {
			if holds(d) && d.Status.NodeName == n.Name {
				c.here += int64(d.Spec.Count)
			}
			classes[d.Spec.ClassName] = true
		}
		for class := range classes {
			c.free += w.snap.FreeOnNode(class, n.Name)
		}
		cands = append(cands, c)
	}
	if len(cands) == 0 {
		return ""
	}
	sort.Slice(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.here != b.here {
			return a.here > b.here
		}
		if a.free != b.free {
			return a.free < b.free
		}
		return a.name < b.name
	})
	return cands[0].name
}

// placeOn binds or moves every device of set onto node.
func (w *world) placeOn(set []*gpuv1alpha1.GPUDevice, node string) {
	now := metav1.NewTime(w.now)
	for _, d := range set {
		if holds(d) && d.Status.NodeName == node {
			continue
		}
		if !holds(d) {
			d.Status.Phase = gpuv1alpha1.GPUDeviceBound
		}
		d.Status.NodeName = node
		d.Status.BoundAt = &now
		meta.RemoveStatusCondition(&d.Status.Conditions, ConditionWaiting)
		w.setDevice(d)
	}
}

// describe names each device of set and its node, for refusals.
func describe(set []*gpuv1alpha1.GPUDevice) string {
	parts := make([]string, 0, len(set))
	for _, d := range set {
		node := d.Status.NodeName
		if !holds(d) || node == "" {
			node = "no node"
		}
		parts = append(parts, fmt.Sprintf("GPUDevice %s (%s, %s)", d.Name, d.Status.Phase, node))
	}
	return strings.Join(parts, ", ")
}

// vmSets groups the devices that keep moves, Lost and rebinds together:
// those one VirtualMachine names. A device no VM names is a set of its own.
// Only devices keep accepts are grouped.
func (w *world) vmSets(keep func(*gpuv1alpha1.GPUDevice) bool) [][]*gpuv1alpha1.GPUDevice {
	parent := map[*gpuv1alpha1.GPUDevice]*gpuv1alpha1.GPUDevice{}
	var find func(*gpuv1alpha1.GPUDevice) *gpuv1alpha1.GPUDevice
	find = func(d *gpuv1alpha1.GPUDevice) *gpuv1alpha1.GPUDevice {
		if parent[d] == d {
			return d
		}
		parent[d] = find(parent[d])
		return parent[d]
	}
	for _, d := range w.devices {
		if keep(d) {
			parent[d] = d
		}
	}
	vms := make([]*vm, 0, len(w.vms))
	for _, v := range w.vms {
		vms = append(vms, v)
	}
	sort.Slice(vms, func(i, j int) bool { return vms[i].Namespace+"/"+vms[i].Name < vms[j].Namespace+"/"+vms[j].Name })
	for _, v := range vms {
		var first *gpuv1alpha1.GPUDevice
		for _, name := range v.Devices {
			d := w.device(v.Namespace, name)
			if d == nil || parent[d] == nil {
				continue
			}
			if first == nil {
				first = d
				continue
			}
			parent[find(d)] = find(first)
		}
	}
	groups := map[*gpuv1alpha1.GPUDevice][]*gpuv1alpha1.GPUDevice{}
	var roots []*gpuv1alpha1.GPUDevice
	for _, d := range w.devices {
		if parent[d] == nil {
			continue
		}
		root := find(d)
		if groups[root] == nil {
			roots = append(roots, root)
		}
		groups[root] = append(groups[root], d)
	}
	out := make([][]*gpuv1alpha1.GPUDevice, 0, len(roots))
	for _, root := range roots {
		out = append(out, groups[root])
	}
	return out
}
