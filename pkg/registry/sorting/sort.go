// SPDX-License-Identifier: Apache-2.0

// Package sorting provides generic sorting utilities for registry resources.
package sorting

import (
	"slices"
	"strings"
)

// NameGetter is an interface for objects that have a Name.
type NameGetter interface {
	GetName() string
}

// NamespaceGetter is an interface for objects that have both Name and Namespace.
type NamespaceGetter interface {
	NameGetter
	GetNamespace() string
}

// ByName sorts a slice of objects by their Name in alphabetical order.
// Use this for cluster-scoped resources.
func ByName[T any, PT interface {
	*T
	NameGetter
}](items []T) {
	slices.SortFunc(items, func(a, b T) int {
		pa, pb := PT(&a), PT(&b)
		return strings.Compare(pa.GetName(), pb.GetName())
	})
}

// ByNamespacedName sorts a slice of objects by Namespace/Name in alphabetical order.
// Use this for namespace-scoped resources.
func ByNamespacedName[T any, PT interface {
	*T
	NamespaceGetter
}](items []T) {
	slices.SortFunc(items, func(a, b T) int {
		pa, pb := PT(&a), PT(&b)
		if res := strings.Compare(pa.GetNamespace(), pb.GetNamespace()); res != 0 {
			return res
		}
		return strings.Compare(pa.GetName(), pb.GetName())
	})
}
