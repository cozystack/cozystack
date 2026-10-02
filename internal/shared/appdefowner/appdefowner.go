// SPDX-License-Identifier: Apache-2.0
// Copyright 2025 The Cozystack Authors.

// Package appdefowner decides which ApplicationDefinition owns an application
// kind when more than one declares it. The Go consumers that map a kind back to
// a definition (the cozystack-api resource registry and its restart hash, the
// HelmRelease chartRef reconciler, the lineage webhook and the CA projection
// reconciler) use this one rule, so they settle on the same definition.
//
// The definition created first owns the kind; creation time ties are broken by
// name. Creation time has one-second resolution, so only a definition created
// in a later second is reliably kept out; within one second the name decides.
// A definition that arrives later with a kind already in use therefore does not
// take over the releases the earlier one backs.
package appdefowner

import (
	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
)

// Owners maps every non-empty application kind to the name of the definition
// that owns it. The result does not depend on the order of defs.
func Owners(defs []cozyv1alpha1.ApplicationDefinition) map[string]string {
	owners := make(map[string]string)
	owner := make(map[string]*cozyv1alpha1.ApplicationDefinition)
	for i := range defs {
		d := &defs[i]
		kind := d.Spec.Application.Kind
		if kind == "" {
			continue
		}
		if cur, ok := owner[kind]; !ok || before(d, cur) {
			owner[kind] = d
		}
	}
	for kind, d := range owner {
		owners[kind] = d.Name
	}
	return owners
}

func before(a, b *cozyv1alpha1.ApplicationDefinition) bool {
	at, bt := a.CreationTimestamp.Time, b.CreationTimestamp.Time
	if !at.Equal(bt) {
		return at.Before(bt)
	}
	return a.Name < b.Name
}
