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

package model

import (
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu"
)

// Class is the part of a GPUClass that placement and budgets read.
type Class struct {
	Name         string
	Mode         gpuv1alpha1.GPUMode
	ResourceName corev1.ResourceName
	NodeSelector map[string]string
}

// ClassFrom projects a GPUClass.
func ClassFrom(c *gpuv1alpha1.GPUClass) Class {
	out := Class{Name: c.Name, Mode: c.Spec.Mode, NodeSelector: c.Spec.NodeSelector}
	if dp := c.Spec.Source.DevicePlugin; dp != nil {
		out.ResourceName = corev1.ResourceName(dp.ResourceName)
	}
	return out
}

// Device is the part of a GPUDevice that placement and budgets read.
type Device struct {
	Namespace string
	Name      string
	Class     string
	Count     int64
	Phase     gpuv1alpha1.GPUDevicePhase
	NodeName  string
}

// DeviceFrom projects a GPUDevice.
func DeviceFrom(d *gpuv1alpha1.GPUDevice) Device {
	return Device{
		Namespace: d.Namespace,
		Name:      d.Name,
		Class:     d.Spec.ClassName,
		Count:     int64(d.Spec.Count),
		Phase:     d.Status.Phase,
		NodeName:  d.Status.NodeName,
	}
}

// Node is the part of a Node that placement reads.
type Node struct {
	Name        string
	Labels      map[string]string
	Allocatable map[corev1.ResourceName]int64
	// Eligible is Eligible() of the node, evaluated by the caller with the
	// tolerations that apply.
	Eligible bool
}

// NodeFrom projects a Node; tolerations are passed to Eligible.
func NodeFrom(n *corev1.Node, tolerations []corev1.Toleration) Node {
	alloc := make(map[corev1.ResourceName]int64, len(n.Status.Allocatable))
	for k, v := range n.Status.Allocatable {
		alloc[k] = v.Value()
	}
	return Node{Name: n.Name, Labels: n.Labels, Allocatable: alloc, Eligible: Eligible(n, tolerations)}
}

// Pod is the GPU usage of a pod beyond what its attached devices cover:
// the units of VMs on raw resource names.
type Pod struct {
	Namespace string
	// NodeName is empty for a pod not yet scheduled; such a pod counts
	// against raw quota keys but not against any node's capacity.
	NodeName string
	Units    map[corev1.ResourceName]int64
}

// PodFrom returns the GPU units p requests under gpuNames beyond covered,
// the units its attached devices account for, and false when nothing is
// left or p is finished.
func PodFrom(p *corev1.Pod, gpuNames sets.Set[corev1.ResourceName], covered map[corev1.ResourceName]int64) (Pod, bool) {
	if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
		return Pod{}, false
	}
	units := map[corev1.ResourceName]int64{}
	for name := range gpuNames {
		if left := podUnits(p, name) - covered[name]; left > 0 {
			units[name] = left
		}
	}
	if len(units) == 0 {
		return Pod{}, false
	}
	return Pod{Namespace: p.Namespace, NodeName: p.Spec.NodeName, Units: units}, true
}

// podUnits is the pod's effective request of name, as the scheduler
// computes it: sidecars (init containers that always restart) run beside
// the later init containers and the main ones.
func podUnits(p *corev1.Pod, name corev1.ResourceName) int64 {
	var sum, sidecars, initPeak int64
	for i := range p.Spec.Containers {
		sum += containerUnits(&p.Spec.Containers[i], name)
	}
	for i := range p.Spec.InitContainers {
		c := &p.Spec.InitContainers[i]
		u := containerUnits(c, name)
		if c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			sidecars += u
			initPeak = max(initPeak, sidecars)
			continue
		}
		initPeak = max(initPeak, sidecars+u)
	}
	return max(sum+sidecars, initPeak)
}

// containerUnits reads the limit first: an extended resource may be given
// as a limit alone, and when both are set they are equal.
func containerUnits(c *corev1.Container, name corev1.ResourceName) int64 {
	if q, ok := c.Resources.Limits[name]; ok {
		return q.Value()
	}
	if q, ok := c.Resources.Requests[name]; ok {
		return q.Value()
	}
	return 0
}

// Snapshot is one consistent view of what budgets and capacity depend on.
type Snapshot struct {
	Classes map[string]Class
	Devices []Device
	Nodes   []Node
	Pods    []Pod
	// Quotas maps a tenant namespace to its declared resourceQuotas, all
	// keys as written.
	Quotas map[string]corev1.ResourceList
}

// RefusalReason says which limit refused an order.
type RefusalReason string

const (
	ReasonClassNotFound RefusalReason = "ClassNotFound"
	ReasonNotEntitled   RefusalReason = "NotEntitled"
	// ReasonBudget is a GPU key, or a raw key, that the order would exceed.
	ReasonBudget RefusalReason = "Budget"
	// ReasonRawKey is a raw key on a tenant that declares neither the
	// class key nor total; it blocks the class until one is declared.
	ReasonRawKey RefusalReason = "RawKey"
)

// Refusal is an order a budget or the entitlement refuses.
type Refusal struct {
	Reason RefusalReason
	Class  string
	Count  int64
	// Namespace is the tenant whose key refused the order.
	Namespace string
	Key       corev1.ResourceName
	Limit     int64
	Used      int64
}

func (r *Refusal) Error() string {
	switch r.Reason {
	case ReasonClassNotFound:
		return fmt.Sprintf("GPUClass %s does not exist", r.Class)
	case ReasonNotEntitled:
		return fmt.Sprintf("cannot order GPUClass %s: the tenant is not entitled to vGPU", r.Class)
	case ReasonRawKey:
		return fmt.Sprintf("cannot order %d of GPUClass %s: tenant %s limits %s directly and needs %s or %s first",
			r.Count, r.Class, r.Namespace, r.Key, ClassQuotaKey(r.Class), gpu.QuotaKeyTotal)
	default:
		return fmt.Sprintf("cannot order %d of GPUClass %s: budget %s of tenant %s is %d and %d is in use",
			r.Count, r.Class, r.Key, r.Namespace, r.Limit, r.Used)
	}
}

type limit struct {
	namespace string
	key       corev1.ResourceName
	limit     int64
	used      int64
	blocked   bool
}

// limits lists every key on ns or an ancestor that applies to orders of c,
// nearest tenant first.
func (s *Snapshot) limits(ns string, c Class) []limit {
	var out []limit
	for _, a := range Ancestors(ns) {
		q := s.Quotas[a]
		classQ, hasClass := q[ClassQuotaKey(c.Name)]
		totalQ, hasTotal := q[gpu.QuotaKeyTotal]
		if hasClass {
			out = append(out, limit{namespace: a, key: ClassQuotaKey(c.Name), limit: classQ.Value(),
				used: s.devicesUsed(a, func(d Device) bool { return d.Class == c.Name })})
		}
		if hasTotal {
			out = append(out, limit{namespace: a, key: gpu.QuotaKeyTotal, limit: totalQ.Value(),
				used: s.devicesUsed(a, func(Device) bool { return true })})
		}
		if c.ResourceName == "" {
			continue
		}
		raw, ok := RawQuotaLimit(q, c.ResourceName)
		if !ok {
			continue
		}
		used := s.devicesUsed(a, func(d Device) bool { return s.Classes[d.Class].ResourceName == c.ResourceName }) +
			s.podUnits(a, c.ResourceName)
		out = append(out, limit{namespace: a, key: c.ResourceName, limit: raw, used: used, blocked: !hasClass && !hasTotal})
	}
	return out
}

func (s *Snapshot) devicesUsed(root string, match func(Device) bool) int64 {
	var n int64
	for _, d := range s.Devices {
		if CountsAgainstBudget(d.Phase) && InSubtree(d.Namespace, root) && match(d) {
			n += d.Count
		}
	}
	return n
}

func (s *Snapshot) podUnits(root string, name corev1.ResourceName) int64 {
	var n int64
	for _, p := range s.Pods {
		if InSubtree(p.Namespace, root) {
			n += p.Units[name]
		}
	}
	return n
}

// CheckOrder returns a *Refusal when an order of count GPUs of class in
// tenant namespace ns would exceed a budget of ns or an ancestor, or needs
// a vGPU entitlement ns lacks. Pending devices in the snapshot, the order
// itself included, do not count.
func (s *Snapshot) CheckOrder(ns, class string, count int64, entitled bool) error {
	c, ok := s.Classes[class]
	if !ok {
		return &Refusal{Reason: ReasonClassNotFound, Class: class, Count: count}
	}
	if c.Mode == gpuv1alpha1.GPUModeVGPU && !entitled {
		return &Refusal{Reason: ReasonNotEntitled, Class: class, Count: count}
	}
	for _, l := range s.limits(ns, c) {
		r := &Refusal{Class: class, Count: count, Namespace: l.namespace, Key: l.key, Limit: l.limit, Used: l.used}
		switch {
		case l.blocked:
			r.Reason = ReasonRawKey
			return r
		case l.used+count > l.limit:
			r.Reason = ReasonBudget
			return r
		}
	}
	return nil
}

// RemainingBudget returns how many GPUs of class tenant ns may still
// order, and false when no budget limits it. It is 0 for a vGPU class
// without the entitlement.
func (s *Snapshot) RemainingBudget(ns, class string, entitled bool) (int64, bool) {
	c, ok := s.Classes[class]
	if !ok || (c.Mode == gpuv1alpha1.GPUModeVGPU && !entitled) {
		return 0, true
	}
	var left int64
	limited := false
	for _, l := range s.limits(ns, c) {
		v := l.limit - l.used
		if l.blocked {
			v = 0
		}
		if !limited || v < left {
			left = v
		}
		limited = true
	}
	return max(left, 0), limited
}

func (s *Snapshot) node(name string) *Node {
	for i := range s.Nodes {
		if s.Nodes[i].Name == name {
			return &s.Nodes[i]
		}
	}
	return nil
}

func matches(c Class, n *Node) bool {
	return labels.SelectorFromSet(c.NodeSelector).Matches(labels.Set(n.Labels))
}

// Conflicts returns, sorted, the classes that select the node and share a
// resource name with another class that selects it. Such a node serves
// none of them.
func (s *Snapshot) Conflicts(nodeName string) []string {
	n := s.node(nodeName)
	if n == nil {
		return nil
	}
	byName := map[corev1.ResourceName][]string{}
	for _, c := range s.Classes {
		if c.ResourceName != "" && matches(c, n) {
			byName[c.ResourceName] = append(byName[c.ResourceName], c.Name)
		}
	}
	var out []string
	for _, names := range byName {
		if len(names) > 1 {
			out = append(out, names...)
		}
	}
	sort.Strings(out)
	return out
}

// Serves reports whether the node is eligible, selected by the class and
// free of a resource-name conflict for it.
func (s *Snapshot) Serves(class, nodeName string) bool {
	c, ok := s.Classes[class]
	n := s.node(nodeName)
	if !ok || n == nil || c.ResourceName == "" || !n.Eligible || !matches(c, n) {
		return false
	}
	return !sets.New(s.Conflicts(nodeName)...).Has(class)
}

// FreeOnNode returns the free units of class on the node: allocatable,
// minus units held by devices there, minus units pods hold beyond their
// devices. It is 0 on a node that does not serve the class.
func (s *Snapshot) FreeOnNode(class, nodeName string) int64 {
	if !s.Serves(class, nodeName) {
		return 0
	}
	name := s.Classes[class].ResourceName
	free := s.node(nodeName).Allocatable[name]
	for _, d := range s.Devices {
		if d.NodeName == nodeName && HoldsGPUs(d.Phase) && s.Classes[d.Class].ResourceName == name {
			free -= d.Count
		}
	}
	for _, p := range s.Pods {
		if p.NodeName == nodeName {
			free -= p.Units[name]
		}
	}
	return max(free, 0)
}

// ClassCapacity returns the class's free units over the nodes that serve
// it and the most free units on one of them.
func (s *Snapshot) ClassCapacity(class string) (free, maxFreePerNode int64) {
	for i := range s.Nodes {
		f := s.FreeOnNode(class, s.Nodes[i].Name)
		free += f
		maxFreePerNode = max(maxFreePerNode, f)
	}
	return free, maxFreePerNode
}
