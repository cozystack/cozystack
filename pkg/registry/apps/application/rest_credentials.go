// SPDX-License-Identifier: Apache-2.0

package application

import (
	"context"
	"fmt"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/pkg/credentials/account"
)

// markForSeeding lets the CredentialTemplate controller seed the account
// Secrets of a release the API creates: its accounts are new, so no client
// holds a password a seeded one would replace. A release created before the
// mark existed gets it from the conversion, once the conversion has written
// its Secrets. The mark is outside the apps.cozystack.io- prefix, so an update
// keeps it.
func markForSeeding(hr *helmv2.HelmRelease) {
	if hr.Annotations == nil {
		hr.Annotations = map[string]string{}
	}
	hr.Annotations[account.SeedAnnotation] = "true"
}

// keepAccountValuesFrom carries over the valuesFrom entries the
// CredentialTemplate controller added to the live release: the rebuilt
// release lists only the platform values, and dropping the entries would
// take every account's stored value out of the chart until the controller
// put them back.
func keepAccountValuesFrom(hr, cur *helmv2.HelmRelease) {
	for _, ref := range cur.Spec.ValuesFrom {
		if account.IsAccountValuesFrom(ref) {
			hr.Spec.ValuesFrom = append(hr.Spec.ValuesFrom, ref)
		}
	}
}

// refuseDeleteSuspended refuses to delete an application whose release is
// suspended and carries accounts: Flux skips the uninstall of a suspended
// release, so the database would keep running with its last passwords after
// the account Secrets went with the release's templates.
func (r *REST) refuseDeleteSuspended(ctx context.Context, name string, hr *helmv2.HelmRelease) error {
	if !hr.Spec.Suspend {
		return nil
	}
	templates := &cozyv1alpha1.CredentialTemplateList{}
	if err := r.c.List(ctx, templates, client.InNamespace(hr.Namespace)); err != nil {
		return err
	}
	for _, t := range templates.Items {
		if t.Annotations[account.ReleaseNameAnnotation] == hr.Name {
			return apierrors.NewConflict(r.gvr.GroupResource(), name, fmt.Errorf(
				"release %s is suspended, and deleting it would leave the database running after its account Secrets are gone; resume the release first", hr.Name))
		}
	}
	return nil
}
