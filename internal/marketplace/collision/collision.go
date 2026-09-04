// SPDX-License-Identifier: Apache-2.0
// Copyright 2025 The Cozystack Authors.

// Package collision detects when a tapped repository would shadow a core
// component. Tapped repositories keep their own declared names (there is no
// community. namespacing), so a name or ApplicationDefinition kind that clashes
// with an existing component must be a hard error rather than a silent
// overwrite. The same checks run on the CLI tap path and in the operator
// materializer, so this logic lives in one place.
package collision

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/marketplace/tapconst"
)

// Owns reports whether an existing PackageSource is the given tap source's own
// materialization, identified by the marketplace-tap label and the source
// annotation. Such an object is safe to overwrite on an idempotent re-tap; a
// foreign object of the same name is never owned.
func Owns(ps *cozyv1alpha1.PackageSource, sourceName string) bool {
	return ps.GetLabels()[tapconst.Label] == "true" &&
		ps.GetAnnotations()[tapconst.SourceAnnotation] == sourceName
}

// PackageSourceName returns an error if a PackageSource named name already
// exists and is not the given tap source's own materialization.
func PackageSourceName(ctx context.Context, cl client.Client, name, sourceName string) error {
	existing := &cozyv1alpha1.PackageSource{}
	err := cl.Get(ctx, client.ObjectKey{Name: name}, existing)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to check for an existing PackageSource %q: %w", name, err)
	}
	if Owns(existing, sourceName) {
		return nil
	}
	return fmt.Errorf("a PackageSource named %q already exists and is not managed by this repository; choose a name that does not conflict with a core component", name)
}
