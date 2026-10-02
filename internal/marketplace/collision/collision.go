// SPDX-License-Identifier: Apache-2.0
// Copyright 2025 The Cozystack Authors.

// Package collision detects when a tapped repository would shadow a core
// component. Tapped repositories keep their own declared names (there is no
// community. namespacing), so a PackageSource name that clashes with an
// existing component must be a hard error rather than a silent overwrite. The
// same checks run on the CLI tap path and in the operator materializer, so this
// logic lives in one place. An ApplicationDefinition kind that clashes is not
// checked here: internal/shared/appdefowner keeps the existing definition in
// charge of the kind, and the ApplicationDefinition admission check refuses the
// second one.
package collision

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/marketplace/tapconst"
)

// Owns reports whether an object (a PackageSource or the registration Package)
// belongs to the given tap source, identified by BOTH the marketplace-tap label
// and the source annotation. Keying on the label alone is not enough: a leftover
// object from an earlier tap keeps the label, so a later tap that reuses the
// name would otherwise adopt or delete an object it did not create. A foreign
// object of the same name is never owned.
func Owns(obj metav1.Object, sourceName string) bool {
	// An empty sourceName (a PackageSource with no SourceRef) must never match a
	// label-only object, whose absent annotation also reads as "": that would
	// delete or adopt an object this source does not own.
	return sourceName != "" &&
		obj.GetLabels()[tapconst.Label] == "true" &&
		obj.GetAnnotations()[tapconst.SourceAnnotation] == sourceName
}

// ManagedRegistration reports whether an object is the given tap source's
// auto-created registration Package that the operator still manages: owned by the
// source AND still at the auto default (empty variant). A Package a user pinned to
// a variant via `cozypkg add` sheds its tap markers, and the empty-variant clause
// is a second line of defence. It takes the variant separately so both a typed
// *Package and an unstructured Package (the dashboard-disconnect path) call the
// one predicate, and no path can diverge on which Package is the operator's to
// manage.
func ManagedRegistration(obj metav1.Object, sourceName, variant string) bool {
	return Owns(obj, sourceName) && variant == ""
}

// PrivilegedConfirmed reports whether the privileged install components of a
// Package may be installed. It is false only for a Package that still carries the
// marketplace-tap label: such a Package is a tap auto-registration created without
// any operator confirmation, so the component that performs the install (the
// Package reconciler) must refuse its privileged components and not mark their
// namespaces privileged. `cozypkg add --allow-privileged` sheds the label
// (pinRegistrationToUser), which is the operator's confirmation, and a platform
// Package never carries the label, so both are permitted. Placing the check here,
// at the install site, closes the window in which a tap materializer guard would
// otherwise race the Package reconciler.
func PrivilegedConfirmed(pkg metav1.Object) bool {
	return pkg.GetLabels()[tapconst.Label] != "true"
}

// PrivilegedInstallComponents lists the names of the given variant's
// install-marked components that request privileged access. Both the CLI
// (cozypkg add) and the operator's tap materializer gate on it, so it lives here
// as the single shared definition.
func PrivilegedInstallComponents(ps *cozyv1alpha1.PackageSource, variant string) []string {
	var out []string
	for i := range ps.Spec.Variants {
		if ps.Spec.Variants[i].Name != variant {
			continue
		}
		for _, c := range ps.Spec.Variants[i].Components {
			if c.Install != nil && c.Install.Privileged {
				out = append(out, c.Name)
			}
		}
	}
	return out
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
