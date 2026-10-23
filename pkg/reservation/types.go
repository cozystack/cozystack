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

// Package reservation computes what an application consumes from a tenant's
// quota budget, from its declared values, before anything runs.
//
// A reservation is denominated in the shorthand keys a Tenant's
// resourceQuotas uses (cpu, memory, storage, services.loadbalancers, ...),
// never in rendered pod units: no allocation ratio is applied and no
// virtualization overhead is added. The evaluator holds no per-kind
// knowledge; each kind describes its consumption in a Declaration carried by
// its ApplicationDefinition, so a kind shipped from an out-of-tree catalog
// participates without a change here.
package reservation

import (
	"sort"

	"k8s.io/apimachinery/pkg/api/resource"
)

// Quota keys for KubeVirt instance-type classes. A shared vCPU and a pinned
// core are not the same capacity, nor are ordinary, hugepage-backed and
// overcommitted memory, so each class counts against its own key.
const (
	KeyCPU                 = "cpu"
	KeyDedicatedCPU        = "dedicated-cpu"
	KeyMemory              = "memory"
	KeyOvercommittedMemory = "overcommitted-memory"
	// KeyHugepagesPrefix is followed by the page size, e.g. "hugepages-2Mi",
	// matching the corev1 resource name for the same pages.
	KeyHugepagesPrefix = "hugepages-"
)

// Declaration is how a kind's reservation is computed from its values.
//
// Exactly one of Exempt or Resources is meaningful: a kind that consumes
// nothing says so with Exempt rather than with an empty Resources map, so
// that a zero reservation is always a reviewed statement and never an
// omission.
type Declaration struct {
	// Exempt declares that the kind consumes nothing from a tenant's budget.
	Exempt bool `json:"exempt,omitempty"`

	// Let binds intermediate CEL expressions, evaluated in order, each one
	// visible to the later bindings and to every Resources expression.
	Let []Binding `json:"let,omitempty"`

	// Resources maps a quota key to the CEL expression computing the
	// amount of it the application reserves. An expression may return an
	// int, a double, a quantity string or a quantity.
	Resources map[string]string `json:"resources,omitempty"`
}

// Binding names one intermediate CEL expression of a Declaration.
type Binding struct {
	Name       string `json:"name"`
	Expression string `json:"expression"`
}

// ResourceList is a reservation, keyed by quota key.
type ResourceList map[string]resource.Quantity

// Add adds other into l in place.
func (l ResourceList) Add(other ResourceList) {
	for k, v := range other {
		cur := l[k]
		cur.Add(v)
		l[k] = cur
	}
}

// Sub subtracts other from l in place. A key absent from l goes negative,
// which is what the delta of an Update needs.
func (l ResourceList) Sub(other ResourceList) {
	for k, v := range other {
		cur := l[k]
		cur.Sub(v)
		l[k] = cur
	}
}

// DeepCopy returns an independent copy of l.
func (l ResourceList) DeepCopy() ResourceList {
	out := make(ResourceList, len(l))
	for k, v := range l {
		out[k] = v.DeepCopy()
	}
	return out
}

// Keys returns the keys of l in a stable order, for deterministic messages.
func (l ResourceList) Keys() []string {
	keys := make([]string, 0, len(l))
	for k := range l {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
